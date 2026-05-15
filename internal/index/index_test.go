package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattwynne/holocron/internal/session"
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
	if results[0].ID != "codex:abcdef12" {
		t.Fatalf("id = %q", results[0].ID)
	}
	if results[0].Size != "42 KB" {
		t.Fatalf("size = %q", results[0].Size)
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
	if ids[session.StableKey(rec)] != "codex:abcdef12" {
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
	if _, err := db.Exec(`UPDATE sessions SET resume_argv = ? WHERE id = ?`, "{not-json", "codex:badargv1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = store.Get("codex:badargv1")
	if err == nil || !strings.Contains(err.Error(), "invalid stored resume_argv") {
		t.Fatalf("err = %v", err)
	}
}
