package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCodexJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	write(t, path, strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"abcdef1234567890","timestamp":"2026-05-13T10:00:00Z","cwd":"/tmp/project"}}`,
		`{"type":"message","role":"user","content":"Fix auth redirect handling"}`,
		`{"type":"message","role":"assistant","content":"I changed the callback route."}`,
		`{"type":"tool_call","tool":"exec_command","payload":{"cmd":"go test ./..."}}`,
	}, "\n")+"\n")

	rec, err := ParseFile(ToolCodex, dir, path, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.SessionID != "abcdef1234567890" {
		t.Fatalf("session id = %q", rec.SessionID)
	}
	if rec.CWD != "/tmp/project" {
		t.Fatalf("cwd = %q", rec.CWD)
	}
	if rec.FirstPrompt != "Fix auth redirect handling" {
		t.Fatalf("first prompt = %q", rec.FirstPrompt)
	}
	if rec.Title != rec.FirstPrompt {
		t.Fatalf("title = %q", rec.Title)
	}
	if rec.MessageCount != 4 {
		t.Fatalf("message count = %d", rec.MessageCount)
	}
	for _, want := range []string{"Fix auth", "callback route", "exec_command", "go test ./..."} {
		if !strings.Contains(rec.Transcript, want) {
			t.Fatalf("transcript missing %q: %s", want, rec.Transcript)
		}
	}
	if got := strings.Join(rec.ResumeArgv, " "); got != "codex resume abcdef1234567890" {
		t.Fatalf("resume argv = %q", got)
	}
}

func TestParseClaudeCWDFromProjectSlug(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-Users-matt-git-example-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "claude-session.jsonl")
	write(t, path, `{"sessionId":"0cd2fc8c-84e8","role":"user","content":"Explain flaky tests","timestamp":"2026-05-13T12:00:00Z"}`+"\n")

	rec, err := ParseFile(ToolClaude, root, path, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.SessionID != "0cd2fc8c-84e8" {
		t.Fatalf("session id = %q", rec.SessionID)
	}
	if rec.CWD != "/Users/matt/git/example/project" {
		t.Fatalf("cwd = %q", rec.CWD)
	}
}

func TestParseIgnoresMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	write(t, path, strings.Join([]string{
		`{"type":"session","id":"pi-session-1234","cwd":"/tmp/project","timestamp":"2026-05-13T10:00:00Z"}`,
		`{"role":"user","content":"Find parser bug"}`,
		`{not-json`,
		`{"role":"assistant","content":"Parser still continues"}`,
		`{"role":"assistant","content":`,
	}, "\n"))

	rec, err := ParseFile(ToolPi, dir, path, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.MessageCount != 3 {
		t.Fatalf("message count = %d", rec.MessageCount)
	}
	if !strings.Contains(rec.Transcript, "Parser still continues") {
		t.Fatalf("transcript = %q", rec.Transcript)
	}
}

func TestAssignStableIDsLengthensOnlyNewCollisions(t *testing.T) {
	records := []Record{
		{Tool: ToolCodex, SessionID: "abcdef123456", SourcePath: "/a"},
		{Tool: ToolCodex, SessionID: "abcdef129999", SourcePath: "/b"},
	}
	assigned := AssignStableIDs(records, nil)
	if assigned[0].ID != "codex:abcdef12" {
		t.Fatalf("first id = %q", assigned[0].ID)
	}
	if assigned[1].ID != "codex:abcdef129" {
		t.Fatalf("second id = %q", assigned[1].ID)
	}

	existing := map[string]string{
		StableKey(assigned[0]): assigned[0].ID,
	}
	reindexed := AssignStableIDs([]Record{
		{Tool: ToolCodex, SessionID: "abcdef123456", SourcePath: "/a"},
		{Tool: ToolCodex, SessionID: "abcdef120000", SourcePath: "/c"},
	}, existing)
	if reindexed[0].ID != "codex:abcdef12" {
		t.Fatalf("existing id changed to %q", reindexed[0].ID)
	}
	if reindexed[1].ID != "codex:abcdef120" {
		t.Fatalf("new collision id = %q", reindexed[1].ID)
	}
}

func TestShellQuotedResumeCommand(t *testing.T) {
	got := ResumeCommand("/tmp/a dir", []string{"pi", "--session", "/tmp/it's.jsonl"})
	want := `cd '/tmp/a dir' && exec pi --session '/tmp/it'"'"'s.jsonl'`
	if got != want {
		t.Fatalf("resume command\n got: %s\nwant: %s", got, want)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
