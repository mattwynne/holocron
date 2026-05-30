package summarizer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattwynne/holocron/internal/session"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "holocron")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`[pi]
executable = "/bin/echo"

[llm]
max_input_tokens = 99
timeout_seconds = 3

[summarizer.selection]
include_first_turns = 1
max_tool_output_chars = 50
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.PiExecutable != "/bin/echo" || cfg.LLM.MaxInputTokens != 99 || cfg.LLM.Timeout != 3*time.Second || cfg.Selection.IncludeFirstTurns != 1 || cfg.Selection.MaxToolOutputChars != 50 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestRunnerSummarizeParsesStrictJSON(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "model")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"title\":\"Add summary command\",\"summary\":\"Implemented generated session summaries.\",\"status\":\"implemented\",\"key_files\":[\"internal/cli/cli.go\"],\"confidence\":\"high\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := Runner{Config: DefaultConfig()}
	r.Config.LLM.PiExecutable = script
	rec := session.Record{Tool: session.ToolPi, SessionID: "abc", CWD: "/tmp/project", SourcePath: "/tmp/session.jsonl", FirstPrompt: "summarize sessions", Title: "summarize sessions", UpdatedAt: time.Now(), Transcript: "user: summarize sessions\nassistant: done"}
	got, err := r.Summarize(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Add summary command" || !strings.Contains(got.Summary, "session summaries") {
		t.Fatalf("summary = %#v", got)
	}
}

func TestRunnerSummarizeUsesSingleModelCallForLargeSessions(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "model")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf x >> '"+calls+"'\necho '{\"title\":\"Summarize large session\",\"summary\":\"Summarized a large session without map reduce.\",\"status\":\"implemented\",\"key_files\":[],\"confidence\":\"high\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := Runner{Config: DefaultConfig()}
	r.Config.LLM.PiExecutable = script
	r.Config.LLM.MaxInputTokens = 10
	rec := session.Record{Tool: session.ToolPi, SessionID: "abc", CWD: "/tmp/project", SourcePath: "/tmp/session.jsonl", FirstPrompt: "summarize", Title: "summarize", UpdatedAt: time.Now(), Transcript: strings.Repeat("user: lots of transcript\nassistant: lots of output\n", 200)}
	if _, err := r.Summarize(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "x" {
		t.Fatalf("model calls = %d, want 1", len(b))
	}
}

func TestBuildSkeletonRedactsCommonSecrets(t *testing.T) {
	rec := session.Record{Transcript: "token=supersecret\npassword: hunter2"}
	got := BuildSkeleton(rec, DefaultConfig())
	if strings.Contains(got, "supersecret") || strings.Contains(got, "hunter2") {
		t.Fatalf("secret was not redacted: %s", got)
	}
}
