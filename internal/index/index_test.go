package index

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattwynne/holocron/internal/session"
	_ "modernc.org/sqlite"
)

func TestSearchAndDeletedSourceRemoval(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	src := filepath.Join(t.TempDir(), "codex.jsonl")
	if err := os.WriteFile(src, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := session.Record{
		Tool:          session.ToolCodex,
		SourcePath:    src,
		SessionID:     "abcdef123456",
		Title:         "Fix auth redirect handling",
		FirstPrompt:   "Fix auth redirect handling",
		CWD:           "/tmp/project",
		CreatedAt:     time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC),
		FileSizeBytes: 43008,
		MessageCount:  37,
		ResumeArgv:    []string{"codex", "resume", "abcdef123456"},
		ResumeCommand: "cd /tmp/project && exec codex resume abcdef123456",
		Transcript:    "authentication callback redirect",
	}
	if err := store.Rebuild([]session.Record{rec}); err != nil {
		t.Fatal(err)
	}
	results, err := store.Search("auth redirect", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0].ID != "cx:abcdef12" {
		t.Fatalf("id = %q", results[0].ID)
	}
	if results[0].Size != "42 KB" {
		t.Fatalf("size = %q", results[0].Size)
	}
	if results[0].Folder != "project" {
		t.Fatalf("folder = %q", results[0].Folder)
	}

	if err := store.Rebuild(nil); err != nil {
		t.Fatal(err)
	}
	results, err = store.Search("auth", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("deleted source still returned %d results", len(results))
	}
	ids, err := store.AssignedIDs()
	if err != nil {
		t.Fatal(err)
	}
	if ids[session.StableKey(rec)] != "cx:abcdef12" {
		t.Fatalf("assigned id was not retained: %#v", ids)
	}
}

func TestSearchUsesBoundParametersForQuotesAndMetacharacters(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	rec := session.Record{
		Tool:          session.ToolCodex,
		SourcePath:    filepath.Join(t.TempDir(), "codex.jsonl"),
		SessionID:     "quote123456",
		Title:         `Fix "quoted" auth`,
		FirstPrompt:   `Fix "quoted" auth`,
		CWD:           "/tmp/project",
		CreatedAt:     time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC),
		ResumeArgv:    []string{"codex", "resume", "quote123456"},
		ResumeCommand: "cd /tmp/project && exec codex resume quote123456",
		Transcript:    `look for O'Reilly sqlite "quotes" and auth`,
	}
	if err := store.Rebuild([]session.Record{rec}); err != nil {
		t.Fatal(err)
	}
	results, err := store.Search(`O'Reilly "quotes"`, 10)
	if err != nil {
		t.Fatalf("search with quotes failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
}

func TestUpdatedBetweenReturnsSessionsAcrossToolsNewestFirst(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	records := []session.Record{
		{
			Tool:          session.ToolCodex,
			SourcePath:    filepath.Join(t.TempDir(), "codex.jsonl"),
			SessionID:     "codextoday123456",
			Title:         "Codex today",
			FirstPrompt:   "Codex today",
			CWD:           "/tmp/project",
			CreatedAt:     time.Date(2026, 5, 27, 9, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC),
			ResumeArgv:    []string{"codex", "resume", "codextoday123456"},
			ResumeCommand: "cd /tmp/project && exec codex resume codextoday123456",
		},
		{
			Tool:          session.ToolClaude,
			SourcePath:    filepath.Join(t.TempDir(), "claude.jsonl"),
			SessionID:     "claudetoday123456",
			Title:         "Claude today",
			FirstPrompt:   "Claude today",
			CWD:           "/tmp/project",
			CreatedAt:     time.Date(2026, 5, 27, 11, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC),
			ResumeArgv:    []string{"claude", "--resume", "claudetoday123456"},
			ResumeCommand: "cd /tmp/project && exec claude --resume claudetoday123456",
		},
		{
			Tool:          session.ToolPi,
			SourcePath:    filepath.Join(t.TempDir(), "pi.jsonl"),
			SessionID:     "piyesterday123456",
			Title:         "Pi yesterday",
			FirstPrompt:   "Pi yesterday",
			CWD:           "/tmp/project",
			CreatedAt:     time.Date(2026, 5, 26, 11, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
			ResumeArgv:    []string{"pi", "--session", "pi.jsonl"},
			ResumeCommand: "cd /tmp/project && exec pi --session pi.jsonl",
		},
	}
	if err := store.Rebuild(records); err != nil {
		t.Fatal(err)
	}
	results, err := store.UpdatedBetween(time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0].Tool != "claude" || results[1].Tool != "codex" {
		t.Fatalf("results order/tools = %#v", results)
	}
}

func TestGeneratedTitleIsUsedWhenProviderTitleIsFallback(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	rec := session.Record{
		Tool:          session.ToolCodex,
		SourcePath:    filepath.Join(t.TempDir(), "codex.jsonl"),
		SessionID:     "generated123456",
		Title:         "please help",
		FirstPrompt:   "please help",
		CWD:           "/tmp/project",
		CreatedAt:     time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC),
		ResumeArgv:    []string{"codex", "resume", "generated123456"},
		ResumeCommand: "cd /tmp/project && exec codex resume generated123456",
		Transcript:    "please help",
	}
	if err := store.Rebuild([]session.Record{rec}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateGenerated("cx:generate", "Generated useful title", "Summary", "test", time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	results, err := store.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Generated useful title" {
		t.Fatalf("results = %#v", results)
	}
}

func TestProviderNamedSessionsAreNotPendingSummaries(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	rec := session.Record{
		Tool:          session.ToolPi,
		SourcePath:    filepath.Join(t.TempDir(), "pi.jsonl"),
		SessionID:     "named123456",
		Title:         "User named session",
		FirstPrompt:   "please help",
		CWD:           "/tmp/project",
		CreatedAt:     time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC),
		ResumeArgv:    []string{"pi", "--session", "pi.jsonl"},
		ResumeCommand: "cd /tmp/project && exec pi --session pi.jsonl",
	}
	if err := store.Rebuild([]session.Record{rec}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingSummaries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("provider-named session should not be pending: %#v", pending)
	}
	results, err := store.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SummaryStatus != "done" {
		t.Fatalf("named session should be done: %#v", results)
	}
}

func TestPendingSummaryStatusLifecycle(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	rec := session.Record{
		Tool:          session.ToolPi,
		SourcePath:    filepath.Join(t.TempDir(), "pi.jsonl"),
		SessionID:     "pending123456",
		Title:         "please title this",
		FirstPrompt:   "please title this",
		CWD:           "/tmp/project",
		CreatedAt:     time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC),
		ResumeArgv:    []string{"pi", "--session", "pi.jsonl"},
		ResumeCommand: "cd /tmp/project && exec pi --session pi.jsonl",
	}
	if err := store.Rebuild([]session.Record{rec}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingSummaries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].SummaryStatus != "pending" {
		t.Fatalf("pending = %#v", pending)
	}
	if err := store.SetSummaryStatus("pi:pending1", "running", ""); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingSummaries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("running session should not be retried in background: %#v", pending)
	}
	if err := store.SetSummaryStatus("pi:pending1", "failed", "pi not found"); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingSummaries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("failed session should not be retried in background: %#v", pending)
	}
	if err := store.UpdateGenerated("pi:pending1", "Generated title", "Summary", "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingSummaries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("done session should not be pending: %#v", pending)
	}
}

func TestRebuildPreservesGeneratedMetadata(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	rec := session.Record{
		Tool:          session.ToolPi,
		SourcePath:    filepath.Join(t.TempDir(), "pi.jsonl"),
		SessionID:     "preserve123456",
		Title:         "(untitled session)",
		FirstPrompt:   "",
		CWD:           "/tmp/project",
		CreatedAt:     time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC),
		ResumeArgv:    []string{"pi", "--session", "pi.jsonl"},
		ResumeCommand: "cd /tmp/project && exec pi --session pi.jsonl",
	}
	if err := store.Rebuild([]session.Record{rec}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateGenerated("pi:preserve", "Preserved generated title", "Summary", "test", time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := store.Rebuild([]session.Record{rec}); err != nil {
		t.Fatal(err)
	}
	results, err := store.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Preserved generated title" || results[0].GeneratedSummary != "Summary" {
		t.Fatalf("generated metadata was not preserved: %#v", results)
	}
}

func TestMigratesLegacyIDPrefixes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE assigned_ids(tool TEXT NOT NULL, session_id TEXT NOT NULL, source_path TEXT NOT NULL, id TEXT NOT NULL UNIQUE, PRIMARY KEY (tool, session_id, source_path));
CREATE TABLE sessions(id TEXT PRIMARY KEY, tool TEXT NOT NULL, source_path TEXT NOT NULL, session_id TEXT NOT NULL, title TEXT NOT NULL, first_prompt TEXT NOT NULL, cwd TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, file_size_bytes INTEGER NOT NULL, message_count INTEGER NOT NULL, resume_argv TEXT NOT NULL, resume_command TEXT NOT NULL, generated_title TEXT NOT NULL DEFAULT '', generated_summary TEXT NOT NULL DEFAULT '', generated_at TEXT NOT NULL DEFAULT '', generated_by TEXT NOT NULL DEFAULT '', summary_status TEXT NOT NULL DEFAULT 'pending', summary_error TEXT NOT NULL DEFAULT '');
CREATE VIRTUAL TABLE sessions_fts USING fts5(id UNINDEXED, title, first_prompt, cwd, transcript_text);
INSERT INTO assigned_ids(tool, session_id, source_path, id) VALUES('codex', 'abcdef123456', '/tmp/codex.jsonl', 'codex:abcdef12');
INSERT INTO sessions(id, tool, source_path, session_id, title, first_prompt, cwd, created_at, updated_at, file_size_bytes, message_count, resume_argv, resume_command) VALUES('codex:abcdef12', 'codex', '/tmp/codex.jsonl', 'abcdef123456', 'Legacy codex', 'Legacy codex', '/tmp/project', '2026-05-13T10:00:00Z', '2026-05-13T11:00:00Z', 1, 1, '["codex","resume","abcdef123456"]', 'cd /tmp/project && exec codex resume abcdef123456');
INSERT INTO sessions_fts(id, title, first_prompt, cwd, transcript_text) VALUES('codex:abcdef12', 'Legacy codex', 'Legacy codex', '/tmp/project', 'legacy transcript');
PRAGMA user_version=3;
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := Store{Path: path}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	ids, err := store.AssignedIDs()
	if err != nil {
		t.Fatal(err)
	}
	if ids["codex\x00abcdef123456\x00/tmp/codex.jsonl"] != "cx:abcdef12" {
		t.Fatalf("assigned IDs not migrated: %#v", ids)
	}
	results, err := store.Search("legacy", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "cx:abcdef12" {
		t.Fatalf("results = %#v", results)
	}
}

func TestGetReportsInvalidStoredResumeArgv(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	rec := session.Record{
		Tool:          session.ToolCodex,
		SourcePath:    filepath.Join(t.TempDir(), "codex.jsonl"),
		SessionID:     "badargv123456",
		Title:         "Bad argv",
		FirstPrompt:   "Bad argv",
		CWD:           "/tmp/project",
		CreatedAt:     time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC),
		ResumeArgv:    []string{"codex", "resume", "badargv123456"},
		ResumeCommand: "cd /tmp/project && exec codex resume badargv123456",
		Transcript:    "bad argv",
	}
	if err := store.Rebuild([]session.Record{rec}); err != nil {
		t.Fatal(err)
	}
	db, err := store.openInitialized()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET resume_argv = ? WHERE id = ?`, "{not-json", "cx:badargv1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = store.Get("cx:badargv1")
	if err == nil || !strings.Contains(err.Error(), "invalid stored resume_argv") {
		t.Fatalf("err = %v", err)
	}
}
