package session

import (
	"encoding/json"
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
	cwd := filepath.Join(t.TempDir(), "example", "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, strings.ReplaceAll(cwd, string(filepath.Separator), "-"))
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
	if rec.CWD != cwd {
		t.Fatalf("cwd = %q", rec.CWD)
	}
}

func TestParseClaudeDoesNotUseMissingProjectSlug(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-definitely-missing-holocron-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "claude-session.jsonl")
	write(t, path, `{"sessionId":"0cd2fc8c-84e8","role":"user","content":"Explain flaky tests"}`+"\n")

	rec, err := ParseFile(ToolClaude, root, path, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.CWD != "" {
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

	var warnings []string
	rec, err := ParseFile(ToolPi, dir, path, ParseOptions{Warnf: func(format string, args ...any) {
		warnings = append(warnings, format)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.MessageCount != 3 {
		t.Fatalf("message count = %d", rec.MessageCount)
	}
	if !strings.Contains(rec.Transcript, "Parser still continues") {
		t.Fatalf("transcript = %q", rec.Transcript)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func TestParseSkipsLargeToolOutputButKeepsToolMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	large := strings.Repeat("secret file contents\n", 100)
	write(t, path, strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"abcdef1234567890","timestamp":"2026-05-13T10:00:00Z","cwd":"/tmp/project"}}`,
		`{"type":"message","role":"user","content":"Find parser bug"}`,
		`{"type":"tool_call","tool":"exec_command","payload":{"cmd":"cat internal/session/session.go","path":"internal/session/session.go","stdout":` + quoteJSON(large) + `}}`,
		`{"role":"assistant","content":"The parser is fixed."}`,
	}, "\n")+"\n")

	rec, err := ParseFile(ToolCodex, dir, path, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Find parser bug", "The parser is fixed.", "exec_command", "cat internal/session/session.go", "internal/session/session.go"} {
		if !strings.Contains(rec.Transcript, want) {
			t.Fatalf("transcript missing %q: %s", want, rec.Transcript)
		}
	}
	if strings.Contains(rec.Transcript, "secret file contents") {
		t.Fatalf("large tool output was indexed: %s", rec.Transcript)
	}
}

func TestParsePiAndGeminiFixtures(t *testing.T) {
	dir := t.TempDir()
	piPath := filepath.Join(dir, "pi.jsonl")
	write(t, piPath, `{"type":"session","id":"pi-session-1234","cwd":"/tmp/pi","timestamp":"2026-05-13T10:00:00Z"}`+"\n"+
		`{"role":"user","content":"Summarize routes"}`+"\n")
	pi, err := ParseFile(ToolPi, dir, piPath, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pi.SessionID != "pi-session-1234" || pi.CWD != "/tmp/pi" || pi.FirstPrompt != "Summarize routes" {
		t.Fatalf("pi record = %#v", pi)
	}

	geminiPath := filepath.Join(dir, "session-gemini.jsonl")
	write(t, geminiPath, `{"sessionId":"gemini-session-1234","projectHash":"hash1","startTime":"2026-05-13T10:00:00Z","lastUpdated":"2026-05-13T11:00:00Z"}`+"\n"+
		`{"role":"user","content":"Review schema"}`+"\n")
	gemini, err := ParseFile(ToolGemini, dir, geminiPath, ParseOptions{GeminiProjects: map[string]string{"hash1": "/tmp/gemini"}})
	if err != nil {
		t.Fatal(err)
	}
	if gemini.SessionID != "gemini-session-1234" || gemini.CWD != "/tmp/gemini" || gemini.FirstPrompt != "Review schema" {
		t.Fatalf("gemini record = %#v", gemini)
	}
}

func TestAssignStableIDsLengthensOnlyNewCollisions(t *testing.T) {
	records := []Record{
		{Tool: ToolCodex, SessionID: "abcdef123456", SourcePath: "/a"},
		{Tool: ToolCodex, SessionID: "abcdef129999", SourcePath: "/b"},
	}
	assigned := AssignStableIDs(records, nil)
	if assigned[0].ID != "cx:abcdef12" {
		t.Fatalf("first id = %q", assigned[0].ID)
	}
	if assigned[1].ID != "cx:abcdef129" {
		t.Fatalf("second id = %q", assigned[1].ID)
	}

	existing := map[string]string{
		StableKey(assigned[0]): assigned[0].ID,
	}
	reindexed := AssignStableIDs([]Record{
		{Tool: ToolCodex, SessionID: "abcdef123456", SourcePath: "/a"},
		{Tool: ToolCodex, SessionID: "abcdef120000", SourcePath: "/c"},
	}, existing)
	if reindexed[0].ID != "cx:abcdef12" {
		t.Fatalf("existing id changed to %q", reindexed[0].ID)
	}
	if reindexed[1].ID != "cx:abcdef120" {
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

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDetectsHolocronSummarizerBackendSessions(t *testing.T) {
	rec := Record{Tool: ToolPi, FirstPrompt: "Summarize chunk 1/3 of a coding-agent session. Return strict JSON only."}
	if !IsHolocronSummarizerRecord(rec) {
		t.Fatal("expected summarizer backend session to be detected")
	}
	rec = Record{Tool: ToolPi, FirstPrompt: "Name and summarize this coding-agent session. Return strict JSON only."}
	if !IsHolocronSummarizerRecord(rec) {
		t.Fatal("expected final summarizer backend session to be detected")
	}
	rec = Record{Tool: ToolPi, FirstPrompt: "Implement Holocron session summarization"}
	if IsHolocronSummarizerRecord(rec) {
		t.Fatal("expected normal session not to be detected as summarizer backend")
	}
}
