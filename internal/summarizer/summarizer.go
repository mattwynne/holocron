package summarizer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mattwynne/holocron/internal/llm"
	"github.com/mattwynne/holocron/internal/session"
	"github.com/mattwynne/holocron/internal/sessionprompt"
)

type Config struct {
	LLM                llm.Config
	ChunkTokens        int
	MaxChunks          int
	Background         bool
	BackgroundInterval time.Duration
	BackgroundJobs     int
	Selection          SelectionConfig
}

type SelectionConfig struct {
	IncludeFirstTurns    int
	IncludeLastTurns     int
	IncludeUserMessages  bool
	IncludeAssistantText bool
	IncludeToolCalls     bool
	IncludeFilePaths     bool
	MaxToolOutputChars   int
}

type Final struct {
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Status     string   `json:"status"`
	KeyFiles   []string `json:"key_files"`
	Confidence string   `json:"confidence"`
}

type ChunkSummary struct {
	Goals         []string `json:"goals"`
	Decisions     []string `json:"decisions"`
	Files         []string `json:"files"`
	Commands      []string `json:"commands"`
	Outcomes      []string `json:"outcomes"`
	OpenQuestions []string `json:"open_questions"`
}

type Runner struct {
	Config Config
	Stderr *bytes.Buffer
}

func DefaultConfig() Config {
	return Config{
		LLM:                llm.DefaultConfig(),
		ChunkTokens:        6000,
		MaxChunks:          20,
		Background:         true,
		BackgroundInterval: 30 * time.Second,
		BackgroundJobs:     1,
		Selection: SelectionConfig{
			IncludeFirstTurns:    3,
			IncludeLastTurns:     5,
			IncludeUserMessages:  true,
			IncludeAssistantText: true,
			IncludeToolCalls:     true,
			IncludeFilePaths:     true,
			MaxToolOutputChars:   1000,
		},
	}
}

func LoadConfig(home string) (Config, error) {
	cfg := DefaultConfig()
	llmCfg, err := llm.LoadConfig(home)
	if err != nil {
		return cfg, err
	}
	cfg.LLM = llmCfg
	path := filepath.Join(home, ".config", "holocron", "config.toml")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	section := ""
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(strings.Split(raw, "#")[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch section + "." + key {
		case "summarizer.chunk_tokens":
			cfg.ChunkTokens = parseInt(val, cfg.ChunkTokens)
		case "summarizer.max_chunks":
			cfg.MaxChunks = parseInt(val, cfg.MaxChunks)
		case "summarizer.background":
			cfg.Background = parseBool(val, cfg.Background)
		case "summarizer.background_interval_seconds":
			cfg.BackgroundInterval = time.Duration(parseInt(val, int(cfg.BackgroundInterval/time.Second))) * time.Second
		case "summarizer.background_jobs":
			cfg.BackgroundJobs = parseInt(val, cfg.BackgroundJobs)
		case "summarizer.selection.include_first_turns":
			cfg.Selection.IncludeFirstTurns = parseInt(val, cfg.Selection.IncludeFirstTurns)
		case "summarizer.selection.include_last_turns":
			cfg.Selection.IncludeLastTurns = parseInt(val, cfg.Selection.IncludeLastTurns)
		case "summarizer.selection.include_user_messages":
			cfg.Selection.IncludeUserMessages = parseBool(val, cfg.Selection.IncludeUserMessages)
		case "summarizer.selection.include_assistant_finals":
			cfg.Selection.IncludeAssistantText = parseBool(val, cfg.Selection.IncludeAssistantText)
		case "summarizer.selection.include_tool_calls":
			cfg.Selection.IncludeToolCalls = parseBool(val, cfg.Selection.IncludeToolCalls)
		case "summarizer.selection.include_file_paths":
			cfg.Selection.IncludeFilePaths = parseBool(val, cfg.Selection.IncludeFilePaths)
		case "summarizer.selection.max_tool_output_chars":
			cfg.Selection.MaxToolOutputChars = parseInt(val, cfg.Selection.MaxToolOutputChars)
		}
	}
	return cfg, nil
}

func parseInt(v string, d int) int {
	n, err := strconv.Atoi(strings.Trim(v, `"`))
	if err != nil {
		return d
	}
	return n
}
func parseBool(v string, d bool) bool {
	b, err := strconv.ParseBool(strings.Trim(v, `"`))
	if err != nil {
		return d
	}
	return b
}

func (r Runner) Summarize(ctx context.Context, rec session.Record) (Final, error) {
	return r.SummarizeContext(ctx, sessionprompt.ContextFromRecord(rec))
}

func (r Runner) SummarizeContext(ctx context.Context, sp sessionprompt.Context) (Final, error) {
	cfg := r.Config
	skeleton := BuildContextSkeleton(sp, cfg)
	excerpts := SelectedTranscriptExcerpt(sp.TranscriptText, cfg)
	prompt := finalPrompt(skeleton, excerpts)
	var out Final
	if err := r.callJSON(ctx, prompt, &out); err != nil {
		return Final{}, err
	}
	out.Title = strings.TrimSpace(strings.TrimSuffix(out.Title, "."))
	out.Summary = strings.TrimSpace(out.Summary)
	if out.Title == "" || out.Summary == "" {
		return Final{}, fmt.Errorf("model returned empty title or summary")
	}
	return out, nil
}

func BuildSkeleton(rec session.Record, cfg Config) string {
	return BuildContextSkeleton(sessionprompt.ContextFromRecord(rec), cfg)
}

func BuildContextSkeleton(sp sessionprompt.Context, cfg Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool: %s\nsession_id: %s\ncwd: %s\nprovider_title: %s\nupdated_at: %s\n", sp.Tool, sp.ProviderSessionID, sp.CWD, sp.Title, sp.UpdatedAt.Format(time.RFC3339))
	return b.String()
}

func SelectedTranscriptExcerpt(text string, cfg Config) string {
	text = redactSecrets(text)
	maxChars := cfg.LLM.MaxInputTokens * 4 / 2
	if maxChars <= 0 {
		maxChars = 24000
	}
	if len(text) <= maxChars {
		return text
	}
	head := maxChars / 2
	tail := maxChars - head
	return text[:head] + "\n... transcript truncated ...\n" + text[len(text)-tail:]
}

func (r Runner) callJSON(ctx context.Context, prompt string, dst any) error {
	runner := llm.Runner{Config: r.Config.LLM}
	result, err := runner.RunPromptResult(ctx, prompt)
	out := result.Stdout
	if err == nil {
		err = json.Unmarshal([]byte(strictJSON(out)), dst)
	}
	if err == nil {
		return nil
	}
	repair := "Repair this response into strict JSON matching the requested schema. Return only JSON.\n\n" + out
	result2, err2 := runner.RunPromptResult(ctx, repair)
	if r.Stderr != nil {
		r.Stderr.WriteString(result.Stderr)
		r.Stderr.WriteString(result2.Stderr)
	}
	if err2 != nil {
		return err2
	}
	if err := json.Unmarshal([]byte(strictJSON(result2.Stdout)), dst); err != nil {
		return fmt.Errorf("parse model JSON: %w; original error: %v", err, err)
	}
	return nil
}

func chunkPrompt(rec session.Record, idx, total int, chunk string) string {
	return fmt.Sprintf(`Summarize chunk %d/%d of a coding-agent session. Return strict JSON only with this schema: {"goals":[],"decisions":[],"files":[],"commands":[],"outcomes":[],"open_questions":[]}.
Do not invent files or outcomes.
Session: %s %s cwd=%s
Chunk:
%s`, idx, total, rec.Tool, rec.SessionID, rec.CWD, chunk)
}

func finalPrompt(skeleton, excerpts string) string {
	return fmt.Sprintf(`Name and summarize this coding-agent session. Return strict JSON only with schema:
{"title":"Add recent sessions command","summary":"One or two sentence factual summary.","status":"implemented|partial|unknown","key_files":["path"],"confidence":"high|medium|low"}
Title rules: eight words or fewer, no trailing period, concrete and searchable, prefer task/outcome, avoid "Session about". Do not invent files or outcomes.
Skeleton:
%s
Transcript excerpts:
%s`, skeleton, excerpts)
}

func strictJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j >= i {
			return s[i : j+1]
		}
	}
	return s
}
func estimateTokens(s string) int { return len(s)/4 + 1 }
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var secretRE = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[:=]\s*['\"]?[^\s'\"]+`)

func redactSecrets(s string) string { return secretRE.ReplaceAllString(s, "$1=[REDACTED]") }
