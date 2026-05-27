package session

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Tool string

const (
	ToolPi     Tool = "pi"
	ToolCodex  Tool = "codex"
	ToolClaude Tool = "claude"
	ToolGemini Tool = "gemini"
)

type Record struct {
	ID            string    `json:"id"`
	Tool          Tool      `json:"tool"`
	SourcePath    string    `json:"source_path"`
	SessionID     string    `json:"session_id"`
	Title         string    `json:"title"`
	FirstPrompt   string    `json:"first_prompt"`
	CWD           string    `json:"cwd"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	FileSizeBytes int64     `json:"file_size_bytes"`
	MessageCount  int       `json:"message_count"`
	ResumeArgv    []string  `json:"resume_argv"`
	ResumeCommand string    `json:"resume_command"`
	Transcript    string    `json:"-"`
}

type Source struct {
	Tool Tool
	Root string
}

type ParseOptions struct {
	GeminiProjects                   map[string]string
	DeleteHolocronSummarizerSessions bool
	Warnf                            func(format string, args ...any)
	Progressf                        func(format string, args ...any)
}

func DefaultSources(home string) []Source {
	return []Source{
		{Tool: ToolPi, Root: filepath.Join(home, ".pi", "agent", "sessions")},
		{Tool: ToolCodex, Root: filepath.Join(home, ".codex", "sessions")},
		{Tool: ToolClaude, Root: filepath.Join(home, ".claude", "projects")},
		{Tool: ToolGemini, Root: filepath.Join(home, ".gemini", "tmp")},
	}
}

func Discover(sources []Source, opts ParseOptions) ([]Record, error) {
	var out []Record
	for _, src := range sources {
		if _, err := os.Stat(src.Root); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		err := filepath.WalkDir(src.Root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				warnf(opts, "%s: %v", path, err)
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if !matchesSource(src, path) {
				return nil
			}
			progressf(opts, "Reading %s %s", src.Tool, path)
			rec, err := ParseFile(src.Tool, src.Root, path, opts)
			if err != nil {
				warnf(opts, "%s: %v", path, err)
				return nil
			}
			if IsHolocronSummarizerRecord(rec) {
				if opts.DeleteHolocronSummarizerSessions {
					if err := os.Remove(path); err != nil {
						warnf(opts, "%s: remove holocron summarizer session: %v", path, err)
					}
				}
				return nil
			}
			out = append(out, rec)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].SourcePath < out[j].SourcePath
	})
	return out, nil
}

func matchesSource(src Source, path string) bool {
	if filepath.Ext(path) != ".jsonl" {
		return false
	}
	switch src.Tool {
	case ToolPi, ToolCodex:
		return true
	case ToolClaude:
		rel, err := filepath.Rel(src.Root, path)
		return err == nil && len(strings.Split(rel, string(filepath.Separator))) == 2
	case ToolGemini:
		return strings.HasPrefix(filepath.Base(path), "session-") && filepath.Base(filepath.Dir(path)) == "chats"
	default:
		return false
	}
}

func ParseFile(tool Tool, root, path string, opts ParseOptions) (Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return Record{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Record{}, err
	}
	rec, err := parseJSONL(tool, root, path, f, opts)
	if err != nil {
		return Record{}, err
	}
	rec.Tool = tool
	rec.SourcePath = path
	rec.FileSizeBytes = info.Size()
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = info.ModTime().UTC()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = rec.UpdatedAt
	}
	if rec.SessionID == "" {
		rec.SessionID = sessionIDFromFilename(path)
	}
	if rec.CWD == "" && tool == ToolClaude {
		rec.CWD = decodeClaudeProjectCWD(root, path)
	}
	if rec.Title == "" {
		rec.Title = rec.FirstPrompt
	}
	if rec.Title == "" {
		rec.Title = "(untitled session)"
	}
	rec.ResumeArgv = ResumeArgv(rec)
	rec.ResumeCommand = ResumeCommand(rec.CWD, rec.ResumeArgv)
	return rec, nil
}

func parseJSONL(tool Tool, root, path string, r io.Reader, opts ParseOptions) (Record, error) {
	var rec Record
	var texts []string
	reader := bufio.NewReader(r)
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line != "" || err == nil {
			lines = append(lines, line)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return rec, err
		}
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			if i == len(lines)-1 {
				break
			}
			warnf(opts, "%s:%d: malformed JSONL line: %v", path, i+1, err)
			continue
		}
		rec.MessageCount++
		applyCommon(tool, &rec, obj, root, path, opts)
		if text := extractConversationText(obj); text != "" {
			if isUserMessage(obj) && rec.FirstPrompt == "" {
				rec.FirstPrompt = text
			}
			texts = append(texts, text)
		}
		if toolText := extractToolText(obj); toolText != "" {
			texts = append(texts, toolText)
		}
	}
	rec.Transcript = strings.Join(texts, "\n")
	return rec, nil
}

func applyCommon(tool Tool, rec *Record, obj map[string]any, root, path string, opts ParseOptions) {
	switch tool {
	case ToolPi:
		if str(obj, "type") == "session" {
			setString(&rec.SessionID, str(obj, "id"))
			setString(&rec.CWD, str(obj, "cwd"))
		}
	case ToolCodex:
		if str(obj, "type") == "session_meta" {
			if p, ok := obj["payload"].(map[string]any); ok {
				setString(&rec.SessionID, str(p, "id"))
				setString(&rec.CWD, str(p, "cwd"))
				setTime(&rec.CreatedAt, parseTimeAny(p["timestamp"]))
			}
		}
		if str(obj, "type") == "turn_context" && rec.CWD == "" {
			if p, ok := obj["payload"].(map[string]any); ok {
				setString(&rec.CWD, str(p, "cwd"))
			}
		}
	case ToolClaude:
		setString(&rec.SessionID, str(obj, "sessionId"))
		setString(&rec.CWD, str(obj, "cwd"))
	case ToolGemini:
		setString(&rec.SessionID, str(obj, "sessionId"))
		hash := str(obj, "projectHash")
		if rec.CWD == "" && hash != "" {
			rec.CWD = opts.GeminiProjects[hash]
		}
		setTime(&rec.CreatedAt, parseTimeAny(obj["startTime"]))
		setTime(&rec.UpdatedAt, parseTimeAny(obj["lastUpdated"]))
	}
	if t := explicitTitle(obj); rec.Title == "" && t != "" {
		rec.Title = t
	}
	if ts := firstTime(obj); !ts.IsZero() {
		if rec.CreatedAt.IsZero() || ts.Before(rec.CreatedAt) {
			rec.CreatedAt = ts
		}
		if rec.UpdatedAt.IsZero() || ts.After(rec.UpdatedAt) {
			rec.UpdatedAt = ts
		}
	}
}

func IsHolocronSummarizerRecord(rec Record) bool {
	if rec.Tool != ToolPi {
		return false
	}
	text := strings.TrimSpace(firstNonEmpty(rec.FirstPrompt, rec.Transcript, rec.Title))
	return strings.HasPrefix(text, "Summarize chunk ") ||
		strings.HasPrefix(text, "Name and summarize this coding-agent session.") ||
		strings.HasPrefix(text, "Repair this response into strict JSON")
}

func ResumeArgv(rec Record) []string {
	switch rec.Tool {
	case ToolClaude:
		return []string{"claude", "--resume", rec.SessionID}
	case ToolCodex:
		return []string{"codex", "resume", rec.SessionID}
	case ToolPi:
		return []string{"pi", "--session", rec.SourcePath}
	case ToolGemini:
		return []string{"gemini", "--resume", rec.SessionID}
	default:
		return nil
	}
}

func ResumeCommand(cwd string, argv []string) string {
	parts := []string{"cd", ShellQuote(cwd), "&&", "exec"}
	for _, arg := range argv {
		parts = append(parts, ShellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r == ':' || r == '+' || r == '=' || r == ',' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func AssignStableIDs(records []Record, existing map[string]string) []Record {
	used := map[string]bool{}
	for _, id := range existing {
		used[id] = true
	}
	for i := range records {
		key := StableKey(records[i])
		if id := existing[key]; id != "" {
			records[i].ID = id
			continue
		}
		records[i].ID = nextStableID(records[i], used)
		used[records[i].ID] = true
	}
	return records
}

func StableKey(rec Record) string {
	return string(rec.Tool) + "\x00" + rec.SessionID + "\x00" + rec.SourcePath
}

func nextStableID(rec Record, used map[string]bool) string {
	sid := rec.SessionID
	if sid == "" {
		sid = hash8(rec.SourcePath)
	}
	prefix := ToolIDPrefix(rec.Tool)
	for n := 8; n <= len(sid); n++ {
		id := fmt.Sprintf("%s:%s", prefix, sid[:n])
		if !used[id] {
			return id
		}
	}
	return fmt.Sprintf("%s:%s-%s", prefix, sid, hash8(rec.SourcePath))
}

func ToolIDPrefix(tool Tool) string {
	switch tool {
	case ToolPi:
		return "pi"
	case ToolClaude:
		return "cd"
	case ToolCodex:
		return "cx"
	case ToolGemini:
		return "gm"
	default:
		return string(tool)
	}
}

func hash8(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

func LoadGeminiProjects(home string) map[string]string {
	path := filepath.Join(home, ".gemini", "projects.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	out := map[string]string{}
	walkProjectMap(raw, out)
	return out
}

func walkProjectMap(v any, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		hash := str(x, "projectHash")
		path := firstNonEmpty(str(x, "path"), str(x, "root"), str(x, "cwd"))
		if hash != "" && path != "" {
			out[hash] = path
		}
		for k, val := range x {
			if strings.HasPrefix(k, "/") {
				if m, ok := val.(map[string]any); ok {
					if h := str(m, "projectHash"); h != "" {
						out[h] = k
					}
				}
			}
			walkProjectMap(val, out)
		}
	case []any:
		for _, val := range x {
			walkProjectMap(val, out)
		}
	}
}

func decodeClaudeProjectCWD(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	slug := strings.Split(rel, string(filepath.Separator))[0]
	if !strings.HasPrefix(slug, "-") {
		return ""
	}
	cwd := strings.ReplaceAll(slug, "-", string(filepath.Separator))
	if st, err := os.Stat(cwd); err == nil && st.IsDir() {
		return cwd
	}
	return ""
}

func sessionIDFromFilename(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func setString(dst *string, value string) {
	if *dst == "" && value != "" {
		*dst = value
	}
}

func setTime(dst *time.Time, value time.Time) {
	if dst.IsZero() && !value.IsZero() {
		*dst = value
	}
}

func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func explicitTitle(obj map[string]any) string {
	for _, k := range []string{"title", "name", "summary"} {
		if v := str(obj, k); v != "" {
			return v
		}
	}
	if p, ok := obj["payload"].(map[string]any); ok {
		for _, k := range []string{"title", "name", "summary"} {
			if v := str(p, k); v != "" {
				return v
			}
		}
	}
	return ""
}

func firstTime(obj map[string]any) time.Time {
	for _, key := range []string{"timestamp", "time", "created_at", "updated_at", "createdAt", "updatedAt"} {
		if t := parseTimeAny(obj[key]); !t.IsZero() {
			return t
		}
	}
	if p, ok := obj["payload"].(map[string]any); ok {
		for _, key := range []string{"timestamp", "time", "created_at", "updated_at", "createdAt", "updatedAt"} {
			if t := parseTimeAny(p[key]); !t.IsZero() {
				return t
			}
		}
	}
	return time.Time{}
}

func parseTimeAny(v any) time.Time {
	switch x := v.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z07:00"} {
			if t, err := time.Parse(layout, x); err == nil {
				return t.UTC()
			}
		}
	case float64:
		return time.Unix(int64(x), 0).UTC()
	}
	return time.Time{}
}

func isUserMessage(obj map[string]any) bool {
	role := strings.ToLower(firstNonEmpty(str(obj, "role"), str(obj, "speaker"), str(obj, "author")))
	if role == "user" || role == "human" {
		return true
	}
	if p, ok := obj["payload"].(map[string]any); ok {
		role = strings.ToLower(firstNonEmpty(str(p, "role"), str(p, "speaker"), str(p, "author")))
		return role == "user" || role == "human"
	}
	return false
}

func extractConversationText(obj map[string]any) string {
	role := strings.ToLower(firstNonEmpty(str(obj, "role"), str(obj, "speaker"), str(obj, "author")))
	typ := strings.ToLower(str(obj, "type"))
	if p, ok := obj["payload"].(map[string]any); ok && role == "" {
		role = strings.ToLower(firstNonEmpty(str(p, "role"), str(p, "speaker"), str(p, "author")))
	}
	if role != "user" && role != "human" && role != "assistant" && role != "ai" && typ != "message" {
		return ""
	}
	for _, key := range []string{"content", "message", "text", "prompt", "response"} {
		if text := extractText(obj[key]); text != "" {
			return text
		}
	}
	if p, ok := obj["payload"].(map[string]any); ok {
		for _, key := range []string{"content", "message", "text", "prompt", "response"} {
			if text := extractText(p[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func extractText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, item := range x {
			if text := extractText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "content", "message", "prompt", "response"} {
			if text := extractText(x[key]); text != "" {
				return text
			}
		}
		if p, ok := x["payload"]; ok {
			return extractText(p)
		}
	}
	return ""
}

func extractToolText(obj map[string]any) string {
	name := firstNonEmpty(str(obj, "tool"), str(obj, "tool_name"), str(obj, "name"))
	if p, ok := obj["payload"].(map[string]any); ok && name == "" {
		name = firstNonEmpty(str(p, "tool"), str(p, "tool_name"), str(p, "name"))
	}
	if name == "" {
		return ""
	}
	var parts []string
	parts = append(parts, "tool:"+name)
	for _, key := range []string{"cmd", "command", "path", "file", "args", "arguments"} {
		if v := compactValue(obj[key]); v != "" {
			parts = append(parts, key+":"+v)
		}
	}
	if p, ok := obj["payload"].(map[string]any); ok {
		for _, key := range []string{"cmd", "command", "path", "file", "args", "arguments"} {
			if v := compactValue(p[key]); v != "" {
				parts = append(parts, key+":"+v)
			}
		}
	}
	return strings.Join(parts, " ")
}

func compactValue(v any) string {
	b, _ := json.Marshal(v)
	if len(b) == 0 || len(b) > 1024 {
		return ""
	}
	return string(b)
}

func warnf(opts ParseOptions, format string, args ...any) {
	if opts.Warnf != nil {
		opts.Warnf(format, args...)
	}
}

func progressf(opts ParseOptions, format string, args ...any) {
	if opts.Progressf != nil {
		opts.Progressf(format, args...)
	}
}
