# Folder-Scoped Search Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `holocron search` and `holocron pick` default to sessions from the current folder tree, with `--all` restoring global search.

**Architecture:** Add scoped query methods in `internal/index` so ranking and limits happen inside the folder scope. Parse `--all` in the CLI and pass either the global store or a scoped wrapper to the picker. Keep the TUI search model unchanged.

**Tech Stack:** Go, SQLite FTS5 via `modernc.org/sqlite`, Bubble Tea TUI, `just test`/`go test`.

---

### Task 1: Add scoped index tests

**Files:**
- Modify: `internal/index/index_test.go`
- Modify: `internal/index/index.go`

**Step 1: Write the failing tests**

Add tests near the existing search tests in `internal/index/index_test.go`:

```go
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
	got := resultIDs(results)
	want := []string{"cx:exact123", "cx:child123"}
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
```

If helpers do not already exist, add small test helpers at the bottom of the file:

```go
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
```

**Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/index -run 'Test(SearchInFolder|RecentInFolder)' -count=1
```

Expected: compile failure because `SearchInFolder` and `RecentInFolder` do not exist.

**Step 3: Implement scoped index methods**

In `internal/index/index.go`, add methods beside `Search` and `Recent`:

```go
func (s Store) SearchInFolder(query, folder string, limit int) ([]SearchResult, error) {
	folder = filepath.Clean(folder)
	if strings.TrimSpace(query) == "" {
		return s.RecentInFolder(folder, limit)
	}
	return s.readRows(`SELECT s.id, s.tool, s.title, s.first_prompt, s.cwd, s.updated_at, s.file_size_bytes, s.message_count, s.resume_argv, s.resume_command, s.generated_title, s.generated_summary, s.generated_at, s.generated_by, s.summary_status, s.summary_error
FROM sessions_fts f JOIN sessions s ON s.id = f.id
WHERE sessions_fts MATCH ? AND (s.cwd = ? OR s.cwd LIKE ?)
ORDER BY bm25(sessions_fts, 10.0, 10.0, 3.0, 1.0)
LIMIT ?`, ftsQuery(query), folder, childFolderPattern(folder), limit)
}

func (s Store) RecentInFolder(folder string, limit int) ([]SearchResult, error) {
	folder = filepath.Clean(folder)
	return s.readRows(`SELECT id, tool, title, first_prompt, cwd, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error
FROM sessions
WHERE cwd = ? OR cwd LIKE ?
ORDER BY updated_at DESC LIMIT ?`, folder, childFolderPattern(folder), limit)
}

func childFolderPattern(folder string) string {
	clean := filepath.Clean(folder)
	if clean == string(filepath.Separator) {
		return string(filepath.Separator) + "%"
	}
	return clean + string(filepath.Separator) + "%"
}
```

Check imports: `index.go` already imports `path/filepath` and `strings`, so no new imports should be needed.

**Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/index -run 'Test(SearchInFolder|RecentInFolder)' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/index/index.go internal/index/index_test.go
git commit -m "feat: add folder-scoped index queries"
```

---

### Task 2: Parse `search --all` and scope CLI search

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`

**Step 1: Write failing CLI tests**

Add tests in `internal/cli/cli_test.go` near the search command tests:

```go
func TestSearchDefaultsToCurrentFolderScope(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	other := filepath.Join(home, "other")
	srcRoot := filepath.Join(home, ".codex", "sessions")
	for _, dir := range []string{project, other, srcRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCodexSession(t, filepath.Join(srcRoot, "project.jsonl"), "project123456", project, "Fix local auth")
	writeCodexSession(t, filepath.Join(srcRoot, "other.jsonl"), "other123456", other, "Fix global auth")

	app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Spinner: false}
	if err := app.fullScan(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOLOCRON_SKIP_SERVICE_CHECK", "1")
	withWorkingDir(t, project, func() {
		var out bytes.Buffer
		app.Stdout = &out
		if code := app.Run([]string{"search", "auth"}); code != 0 {
			t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
		}
		if !strings.Contains(out.String(), "Fix local auth") {
			t.Fatalf("missing local result: %s", out.String())
		}
		if strings.Contains(out.String(), "Fix global auth") {
			t.Fatalf("included global result: %s", out.String())
		}
	})
}

func TestSearchAllSearchesAcrossFolders(t *testing.T) {
	// Same fixture as TestSearchDefaultsToCurrentFolderScope.
	// Run from project with args []string{"search", "auth", "--all"}.
	// Assert output contains both "Fix local auth" and "Fix global auth".
}
```

Use existing helper style in this file. If needed, add helpers:

```go
func writeCodexSession(t *testing.T, path, id, cwd, prompt string) {
	t.Helper()
	writeFile(t, path, strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-05-30T10:00:00Z","cwd":` + quote(cwd) + `}}`,
		`{"role":"user","content":` + quote(prompt) + `}`,
	}, "\n")+"\n")
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(old); err != nil {
			t.Fatal(err)
		}
	}()
	fn()
}
```

**Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/cli -run 'TestSearch(Default|All)' -count=1
```

Expected: default-scope test fails because search still returns global results; `--all` may currently be an unknown flag.

**Step 3: Implement search args and scoped search**

Change `parseSearchArgs` to return an `all` boolean:

```go
func parseSearchArgs(args []string) (string, bool, bool, error) {
	var jsonOut bool
	var all bool
	var terms []string
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		case "--all":
			all = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, false, fmt.Errorf("unknown search flag %s", arg)
			}
			terms = append(terms, arg)
		}
	}
	return strings.Join(terms, " "), jsonOut, all, nil
}
```

Update `search`:

```go
query, jsonOut, all, err := parseSearchArgs(args)
...
store := a.store()
if all {
	results, err = store.Search(query, 100)
} else {
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		return fmt.Errorf("cannot determine current folder: %w", cwdErr)
	}
	results, err = store.SearchInFolder(query, cwd, 100)
}
```

**Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/cli -run 'TestSearch(Default|All|JSON|Table)' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat: scope search to current folder by default"
```

---

### Task 3: Parse `pick --all` and scope picker data

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`

**Step 1: Write failing tests for pick parsing and scoped wrapper**

Add a focused parser test in `internal/cli/cli_test.go`:

```go
func TestParsePickArgsSupportsAll(t *testing.T) {
	query, all, err := parsePickArgs([]string{"--all", "auth", "redirect"})
	if err != nil {
		t.Fatal(err)
	}
	if query != "auth redirect" || !all {
		t.Fatalf("query=%q all=%v", query, all)
	}
}

func TestParsePickArgsRejectsUnknownFlag(t *testing.T) {
	_, _, err := parsePickArgs([]string{"--bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown pick flag --bogus") {
		t.Fatalf("err = %v", err)
	}
}
```

Add a small unit test for the scoped TUI store wrapper after it exists or write it first as a failing compile test:

```go
func TestScopedPickerStoreUsesFolderQueries(t *testing.T) {
	store := index.Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
	// Reuse records from Task 2 or build records directly.
	// Wrap with scopedPickerStore{store: store, folder: "/tmp/project"}.
	// Assert Recent and Search only return matching folder-tree records.
}
```

**Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/cli -run 'TestParsePickArgs|TestScopedPickerStore' -count=1
```

Expected: compile failure because `parsePickArgs` and/or `scopedPickerStore` do not exist.

**Step 3: Implement pick arg parsing**

In `internal/cli/cli.go`, add:

```go
func parsePickArgs(args []string) (string, bool, error) {
	var all bool
	var terms []string
	for _, arg := range args {
		switch arg {
		case "--all":
			all = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unknown pick flag %s", arg)
			}
			terms = append(terms, arg)
		}
	}
	return strings.Join(terms, " "), all, nil
}
```

Update `pick`:

```go
query, all, err := parsePickArgs(args)
if err != nil {
	return err
}
store := tui.Store(a.store())
if !all {
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		return fmt.Errorf("cannot determine current folder: %w", cwdErr)
	}
	store = scopedPickerStore{store: a.store(), folder: cwd}
}
result, err := tui.PickWithStatus(store, query, ...)
```

If a direct cast to `tui.Store` is awkward, use a local variable typed as the interface:

```go
var store tui.Store = a.store()
```

Add the wrapper type in `internal/cli/cli.go`:

```go
type scopedPickerStore struct {
	store  index.Store
	folder string
}

func (s scopedPickerStore) Search(query string, limit int) ([]index.SearchResult, error) {
	return s.store.SearchInFolder(query, s.folder, limit)
}

func (s scopedPickerStore) Recent(limit int) ([]index.SearchResult, error) {
	return s.store.RecentInFolder(s.folder, limit)
}
```

**Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/cli -run 'TestParsePickArgs|TestScopedPickerStore' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat: scope picker to current folder by default"
```

---

### Task 4: Update help and README

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `README.md`
- Optional test: `internal/cli/cli_test.go`

**Step 1: Write or update help expectations**

If there is an existing usage test, update it. Otherwise add a minimal test:

```go
func TestUsageMentionsAllFlagForSearchAndPick(t *testing.T) {
	var out bytes.Buffer
	app := App{Stdout: &out}
	app.Usage()
	for _, want := range []string{"holocron search Q [--json] [--all]", "holocron pick [--all] [Q]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("usage missing %q:\n%s", want, out.String())
		}
	}
}
```

**Step 2: Run test to verify failure**

Run:

```bash
go test ./internal/cli -run TestUsageMentionsAllFlagForSearchAndPick -count=1
```

Expected: FAIL until usage text changes.

**Step 3: Update user-facing docs**

In `internal/cli/cli.go`, update usage lines:

```text
holocron search Q [--json] [--all]  Search indexed sessions from the current folder tree
holocron pick [--all] [Q]           Pick and reopen a session in the TUI
```

In `README.md`, update the search and pick examples:

```markdown
Search sessions from the current folder tree:

```sh
holocron search "auth redirect"
```

Search all indexed sessions:

```sh
holocron search "auth redirect" --all
```

Pick and reopen a session in the TUI, scoped to the current folder tree by default:

```sh
holocron pick
holocron pick "auth redirect"
holocron pick --all "auth redirect"
```
```

**Step 4: Run test to verify pass**

Run:

```bash
go test ./internal/cli -run TestUsageMentionsAllFlagForSearchAndPick -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go README.md
git commit -m "docs: document folder-scoped search"
```

---

### Task 5: Full validation and build

**Files:**
- No planned source changes unless tests reveal a bug.

**Step 1: Run all tests**

Run:

```bash
just test
```

Expected: all packages pass.

**Step 2: Build the demo binary**

Run:

```bash
just build
```

Expected: `./holocron` builds. If the launch agent is installed, the command may also update the daemon.

**Step 3: Manual smoke test**

From the repo root, run:

```bash
./holocron search holocron --all
./holocron search holocron
./holocron pick --all holocron
```

Expected: `--all` can show global results; default commands show only sessions whose indexed `cwd` is the repo root or below.

**Step 4: Commit any fixes**

If validation required changes:

```bash
git add <changed-files>
git commit -m "fix: validate folder-scoped search"
```

Otherwise no commit is needed.
