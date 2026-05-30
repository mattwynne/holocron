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

func TestSearchReturnsMatchSnippet(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	rec := searchRecord(t, "snippet123456", "Investigate login", "/tmp/project", time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC))
	rec.Transcript = "We traced the OAuth callback and fixed the redirect handling."
	if err := store.Rebuild([]session.Record{rec}); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search("OAuth redirect", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if !strings.Contains(results[0].Snippet, "⟪OAuth⟫") || !strings.Contains(results[0].Snippet, "⟪redirect⟫") {
		t.Fatalf("snippet did not mark matches: %q", results[0].Snippet)
	}
}

func TestSearchInFolderMatchesCurrentFolderAndDescendantsOnly(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	now := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	records := []session.Record{
		searchRecord(t, "exact123456", "Exact auth", "/tmp/project", now),
		searchRecord(t, "child123456", "Child auth", "/tmp/project/internal", now.Add(time.Minute)),
		searchRecord(t, "sibling123456", "Sibling auth", "/tmp/project-other", now.Add(2*time.Minute)),
		searchRecord(t, "other123456", "Other auth", "/tmp/other", now.Add(3*time.Minute)),
	}
	if err := store.Rebuild(records); err != nil {
		t.Fatal(err)
	}

	results, err := store.SearchInFolder("auth", "/tmp/project", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := resultIDSet(results)
	for _, want := range []string{"cx:exact123", "cx:child123"} {
		if !got[want] {
			t.Fatalf("ids = %#v, missing %s", resultIDs(results), want)
		}
	}
	for _, unwanted := range []string{"cx:sibling", "cx:other12"} {
		if got[unwanted] {
			t.Fatalf("ids = %#v, included %s", resultIDs(results), unwanted)
		}
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", resultIDs(results))
	}
}

func TestSearchInFolderEscapesLikeWildcards(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	now := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	records := []session.Record{
		searchRecord(t, "under123456", "Under auth", "/tmp/project_1/child", now),
		searchRecord(t, "wild123456", "Wildcard auth", "/tmp/projecta1/child", now.Add(time.Minute)),
	}
	if err := store.Rebuild(records); err != nil {
		t.Fatal(err)
	}

	results, err := store.SearchInFolder("auth", "/tmp/project_1", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := resultIDs(results)
	want := []string{"cx:under123"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %#v, want %#v", got, want)
	}
}

func TestRecentInFolderMatchesCurrentFolderAndDescendantsOnly(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	now := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	records := []session.Record{
		searchRecord(t, "old123456", "Old", "/tmp/project", now),
		searchRecord(t, "new123456", "New", "/tmp/project/internal", now.Add(time.Minute)),
		searchRecord(t, "bad123456", "Bad", "/tmp/project-other", now.Add(2*time.Minute)),
	}
	if err := store.Rebuild(records); err != nil {
		t.Fatal(err)
	}

	results, err := store.RecentInFolder("/tmp/project", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := resultIDs(results)
	want := []string{"cx:new12345", "cx:old12345"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %#v, want %#v", got, want)
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

func TestInitAddsSummaryLeaseColumns(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", store.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, want := range []string{"summary_started_at", "summary_attempts"} {
		if !cols[want] {
			t.Fatalf("missing column %s", want)
		}
	}
}

func TestClaimPendingSummarySetsLease(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	if err := store.Rebuild([]session.Record{untitledSummaryRecord(now, "claim123456")}); err != nil {
		t.Fatal(err)
	}
	row, ok, err := store.ClaimPendingSummary(now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || row.ID == "" {
		t.Fatalf("claim ok=%v row=%#v", ok, row)
	}
	got, err := store.Lookup(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SummaryStatus != "running" || got.SummaryError != "" {
		t.Fatalf("status/error = %q/%q", got.SummaryStatus, got.SummaryError)
	}
	db, err := sql.Open("sqlite", store.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var started string
	var attempts int
	if err := db.QueryRow(`SELECT summary_started_at, summary_attempts FROM sessions WHERE id = ?`, row.ID).Scan(&started, &attempts); err != nil {
		t.Fatal(err)
	}
	if started != now.Format(time.RFC3339) || attempts != 1 {
		t.Fatalf("lease = %q attempts=%d", started, attempts)
	}
}

func TestResetStaleRunningSummaries(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	if err := store.Rebuild([]session.Record{untitledSummaryRecord(now, "stale123456")}); err != nil {
		t.Fatal(err)
	}
	row, ok, err := store.ClaimPendingSummary(now.Add(-2 * time.Hour))
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	n, err := store.ResetStaleRunningSummaries(now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reset %d rows", n)
	}
	got, err := store.Lookup(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SummaryStatus != "pending" || !strings.Contains(got.SummaryError, "reset stale running summary") {
		t.Fatalf("status/error = %q/%q", got.SummaryStatus, got.SummaryError)
	}
}

func TestResetStaleRunningSummariesKeepsFreshLease(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	if err := store.Rebuild([]session.Record{untitledSummaryRecord(now, "fresh123456")}); err != nil {
		t.Fatal(err)
	}
	row, ok, err := store.ClaimPendingSummary(now.Add(-5 * time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	n, err := store.ResetStaleRunningSummaries(now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("reset %d rows", n)
	}
	got, err := store.Lookup(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SummaryStatus != "running" {
		t.Fatalf("status = %q", got.SummaryStatus)
	}
}

func TestSummaryStatusCounts(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	if err := store.Rebuild([]session.Record{untitledSummaryRecord(now, "counts123456")}); err != nil {
		t.Fatal(err)
	}
	counts, err := store.SummaryStatusCounts()
	if err != nil {
		t.Fatal(err)
	}
	if counts["pending"] != 1 {
		t.Fatalf("counts = %#v", counts)
	}
}

func untitledSummaryRecord(now time.Time, id string) session.Record {
	return session.Record{Tool: session.ToolPi, SourcePath: "/tmp/" + id + ".jsonl", SessionID: id, Title: "(untitled session)", FirstPrompt: "(untitled session)", CWD: "/tmp", CreatedAt: now, UpdatedAt: now, ResumeArgv: []string{"pi"}, ResumeCommand: "pi", Transcript: "user: hello"}
}

func searchRecord(t *testing.T, sessionID, title, cwd string, updatedAt time.Time) session.Record {
	t.Helper()
	return session.Record{
		Tool:          session.ToolCodex,
		SourcePath:    filepath.Join(t.TempDir(), sessionID+".jsonl"),
		SessionID:     sessionID,
		Title:         title,
		FirstPrompt:   title,
		CWD:           cwd,
		CreatedAt:     updatedAt.Add(-time.Hour),
		UpdatedAt:     updatedAt,
		ResumeArgv:    []string{"codex", "resume", sessionID},
		ResumeCommand: "cd " + cwd + " && exec codex resume " + sessionID,
		Transcript:    title,
	}
}

func resultIDs(results []SearchResult) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return ids
}

func resultIDSet(results []SearchResult) map[string]bool {
	ids := map[string]bool{}
	for _, result := range results {
		ids[result.ID] = true
	}
	return ids
}
