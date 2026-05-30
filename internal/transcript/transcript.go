package transcript

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattwynne/holocron/internal/session"
)

type Transcript struct {
	HolocronID        string
	ProviderSessionID string
	Tool              session.Tool
	SourcePath        string
	CWD               string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Turns             []Turn
	Hash              string
}

type Turn struct {
	Role         Role
	Text         string
	ToolName     string
	ToolInput    string
	ToolOutput   string
	IsError      bool
	Timestamp    time.Time
	MetadataJSON string
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

const maxToolOutput = 16 * 1024

func ParseFile(rec session.Record) (Transcript, error) {
	if rec.Tool != session.ToolClaude {
		return Transcript{}, nil
	}
	f, err := os.Open(rec.SourcePath)
	if err != nil {
		return Transcript{}, err
	}
	defer f.Close()
	tr, err := ParseClaude(rec, f)
	if err != nil {
		return Transcript{}, err
	}
	tr.Hash = Hash(tr)
	return tr, nil
}

func ParseClaude(rec session.Record, r io.Reader) (Transcript, error) {
	tr := Transcript{HolocronID: rec.ID, ProviderSessionID: rec.SessionID, Tool: rec.Tool, SourcePath: rec.SourcePath, CWD: rec.CWD, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if tr.ProviderSessionID == "" {
			tr.ProviderSessionID = str(obj, "sessionId")
		}
		if tr.CWD == "" {
			tr.CWD = str(obj, "cwd")
		}
		ts := parseTimeAny(obj["timestamp"])
		role := strings.ToLower(firstNonEmpty(str(obj, "role"), str(obj, "type")))
		content := obj["content"]
		if msg, ok := obj["message"].(map[string]any); ok {
			role = strings.ToLower(firstNonEmpty(str(msg, "role"), role))
			if content == nil {
				content = msg["content"]
			}
		}
		turns := turnsFromContent(role, content, ts)
		if len(turns) == 0 {
			if text := extractText(content); text != "" {
				turns = append(turns, Turn{Role: roleFor(role), Text: text, Timestamp: ts})
			}
		}
		for _, turn := range turns {
			if turn.Role == "" {
				continue
			}
			tr.Turns = append(tr.Turns, turn)
		}
	}
	if err := scanner.Err(); err != nil {
		return tr, err
	}
	return tr, nil
}

func turnsFromContent(role string, content any, ts time.Time) []Turn {
	switch c := content.(type) {
	case string:
		return []Turn{{Role: roleFor(role), Text: c, Timestamp: ts}}
	case []any:
		var turns []Turn
		var textParts []string
		flushText := func() {
			if len(textParts) > 0 {
				turns = append(turns, Turn{Role: roleFor(role), Text: strings.Join(textParts, "\n"), Timestamp: ts})
				textParts = nil
			}
		}
		for _, item := range c {
			m, ok := item.(map[string]any)
			if !ok {
				if text := extractText(item); text != "" {
					textParts = append(textParts, text)
				}
				continue
			}
			typ := strings.ToLower(str(m, "type"))
			switch typ {
			case "text":
				if text := extractText(m["text"]); text != "" {
					textParts = append(textParts, text)
				}
			case "tool_use", "tooluse", "tool-call", "toolcall":
				flushText()
				turns = append(turns, Turn{Role: RoleTool, Text: "Tool " + firstNonEmpty(str(m, "name"), "unknown") + " called", ToolName: firstNonEmpty(str(m, "name"), str(m, "toolName")), ToolInput: compact(m["input"]), Timestamp: ts, MetadataJSON: compactMeta(m, "input")})
			case "tool_result", "toolresult":
				flushText()
				out := truncate(extractText(firstNonNil(m["content"], m["text"])))
				turns = append(turns, Turn{Role: RoleTool, Text: "Tool result", ToolName: firstNonEmpty(str(m, "name"), str(m, "toolName")), ToolOutput: out, IsError: boolVal(m["is_error"]) || boolVal(m["isError"]), Timestamp: ts, MetadataJSON: compactMeta(m, "content")})
			default:
				if text := extractText(m); text != "" {
					textParts = append(textParts, text)
				}
			}
		}
		flushText()
		return turns
	default:
		return nil
	}
}

func roleFor(role string) Role {
	switch strings.ToLower(role) {
	case "user", "human":
		return RoleUser
	case "assistant", "ai":
		return RoleAssistant
	case "system":
		return RoleSystem
	case "tool", "toolresult":
		return RoleTool
	default:
		return ""
	}
}

func Hash(tr Transcript) string {
	type hashTurn struct {
		Role, Text, ToolName, ToolInput, ToolOutput string
		IsError                                     bool
		Timestamp, MetadataJSON                     string
	}
	turns := make([]hashTurn, 0, len(tr.Turns))
	for _, t := range tr.Turns {
		ts := ""
		if !t.Timestamp.IsZero() {
			ts = t.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		turns = append(turns, hashTurn{Role: string(t.Role), Text: t.Text, ToolName: t.ToolName, ToolInput: truncate(t.ToolInput), ToolOutput: truncate(t.ToolOutput), IsError: t.IsError, Timestamp: ts, MetadataJSON: t.MetadataJSON})
	}
	b, _ := json.Marshal(turns)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func extractText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := []string{}
		for _, item := range x {
			if text := extractText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "content", "message"} {
			if text := extractText(x[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func compact(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return truncate(string(b))
}

func compactMeta(m map[string]any, omit string) string {
	copy := map[string]any{}
	for k, v := range m {
		if k != omit {
			copy[k] = v
		}
	}
	return compact(copy)
}

func truncate(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	return s[:maxToolOutput] + "\n[truncated]"
}

func parseTimeAny(v any) time.Time {
	if s, ok := v.(string); ok {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

func str(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func boolVal(v any) bool                      { b, _ := v.(bool); return b }
func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
