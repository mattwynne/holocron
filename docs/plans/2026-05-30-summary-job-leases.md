# Summary Job Leases Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Prevent generated-summary jobs from getting permanently stuck in `running` after daemon crashes, cancellations, SQLite busy errors, or missing Pi executable failures.

**Architecture:** Treat background summarization as a leased job queue. Store when a summary job is claimed, reclaim stale `running` jobs on daemon startup and before polling, and make each job finalize deterministically to `done`, `failed`, or retryable `pending`. Keep the first slice small: one daemon worker still runs at a time, but the state machine becomes recoverable.

**Tech Stack:** Go 1.26, SQLite schema migration in `internal/index`, daemon/background worker in `internal/cli`, existing summarizer/LLM runner, `just test` / `just build`.

---

## Problem Summary

Observed row:

```text
id:             pi:019e7592
title:          (untitled session)
generated_title:
summary_status: running
summary_error:
```

Current background summary flow:

1. Select one pending row with `PendingSummaries`.
2. Mark it `running` with `SetSummaryStatus`.
3. Run summarization.
4. On success, `UpdateGenerated` sets `done`; on error, `SetSummaryStatus(..., "failed", ...)`.

If Holocron exits, is restarted, is cancelled, panics, or cannot write status after step 2, the row can remain `running` forever. `PendingSummaries` only selects `pending`, so the row is stranded.

## Desired Behavior

- A `running` summary has a lease timestamp.
- Stale `running` leases are reset to `pending` automatically.
- Daemon startup resets stale/abandoned `running` jobs.
- Background polling also resets stale jobs before selecting new work.
- Missing Pi executable and model errors mark the row `failed`, not `running`.
- `failed` rows remain visible and can be retried with existing `summarize --force` behavior.
- Existing generated titles/summaries remain untouched.

---

### Task 1: Add summary lease columns to the schema

**Files:**
- Modify: `internal/index/index.go`
- Modify: `internal/index/index_test.go`

**Step 1: Write failing schema migration test**

In `internal/index/index_test.go`, add a test that initializes a fresh store and verifies the new columns exist. Use SQLite `PRAGMA table_info(sessions)`.

Example:

```go
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
```

If `database/sql` is not imported yet in the test file, add it.

**Step 2: Run test to verify it fails**

```sh
go test ./internal/index -run TestInitAddsSummaryLeaseColumns -count=1
```

Expected: FAIL because the columns do not exist.

**Step 3: Implement schema changes**

In `internal/index/index.go`:

1. Increment `SchemaVersion` from `5` to `6`.
2. Add columns to the `CREATE TABLE sessions` statement:

```sql
summary_started_at TEXT NOT NULL DEFAULT '',
summary_attempts INTEGER NOT NULL DEFAULT 0,
```

Place them near `summary_status` / `summary_error`.

3. In the schema migration logic, add `ALTER TABLE` calls for existing databases:

```go
ALTER TABLE sessions ADD COLUMN summary_started_at TEXT NOT NULL DEFAULT ''
ALTER TABLE sessions ADD COLUMN summary_attempts INTEGER NOT NULL DEFAULT 0
```

Follow the existing add-column pattern used for summary fields and normalized fields. If the helper tolerates duplicate-column errors, use it; otherwise add the same duplicate-safe logic as the existing migrations.

4. Extend `storedRow` if needed only for reads that need the new columns. Do not add fields unless the compiler or tests need them.

**Step 4: Run focused test**

```sh
go test ./internal/index -run TestInitAddsSummaryLeaseColumns -count=1
```

Expected: PASS.

**Step 5: Run all index tests**

```sh
go test ./internal/index
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/index/index.go internal/index/index_test.go
git commit -m "feat: add summary lease columns"
```

---

### Task 2: Claim summary jobs with a lease timestamp

**Files:**
- Modify: `internal/index/index.go`
- Modify: `internal/index/index_test.go`
- Modify: `internal/cli/cli.go`

**Step 1: Write failing claim test**

Add a test to `internal/index/index_test.go` that verifies claiming a job sets `running`, clears error, increments attempts, and records `summary_started_at`.

Example:

```go
func TestClaimPendingSummarySetsLease(t *testing.T) {
    store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
    now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
    rec := session.Record{ID: "", Tool: session.ToolPi, SourcePath: "/tmp/pi.jsonl", SessionID: "abcdef123456", Title: "(untitled session)", FirstPrompt: "(untitled session)", CWD: "/tmp", CreatedAt: now, UpdatedAt: now, ResumeArgv: []string{"pi"}, ResumeCommand: "pi"}
    if err := store.Rebuild([]session.Record{rec}); err != nil {
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

    db, _ := sql.Open("sqlite", store.Path)
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
```

**Step 2: Run failing test**

```sh
go test ./internal/index -run TestClaimPendingSummarySetsLease -count=1
```

Expected: FAIL because `ClaimPendingSummary` does not exist.

**Step 3: Implement `ClaimPendingSummary`**

In `internal/index/index.go`, add:

```go
func (s Store) ClaimPendingSummary(now time.Time) (SearchResult, bool, error)
```

Implementation can be a transaction to keep compatibility simple:

1. Open initialized DB.
2. Begin transaction.
3. Select one pending row using the same query as `PendingSummaries(1)`:

```sql
SELECT id ... FROM sessions
WHERE generated_title = ''
  AND NOT (<providerTitleSQL>)
  AND summary_status = 'pending'
ORDER BY updated_at DESC
LIMIT 1
```

4. If no row, commit/rollback and return `SearchResult{}, false, nil`.
5. Update that row:

```sql
UPDATE sessions
SET summary_status = 'running',
    summary_error = '',
    summary_started_at = ?,
    summary_attempts = summary_attempts + 1
WHERE id = ? AND generated_title = '' AND summary_status = 'pending'
```

6. If rows affected is 0, another worker claimed it; return `false, nil`.
7. Commit and return the selected row.

Do not remove `PendingSummaries` yet; it is still used by tests and `summarize --missing` flow.

**Step 4: Update background worker**

In `internal/cli/cli.go`, change `backgroundSummaryWorker` to use `ClaimPendingSummary(a.now())` instead of `PendingSummaries(1)` plus `SetSummaryStatus(..., "running", ...)`.

Old:

```go
rows, err := a.store().PendingSummaries(1)
if err != nil || len(rows) == 0 { return }
row := rows[0]
if err := a.store().SetSummaryStatus(row.ID, "running", ""); err != nil { return }
```

New:

```go
row, ok, err := a.store().ClaimPendingSummary(a.now())
if err != nil || !ok { return }
```

**Step 5: Run tests**

```sh
go test ./internal/index ./internal/cli
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/index/index.go internal/index/index_test.go internal/cli/cli.go
git commit -m "feat: claim summary jobs with leases"
```

---

### Task 3: Reset stale running summaries

**Files:**
- Modify: `internal/index/index.go`
- Modify: `internal/index/index_test.go`
- Modify: `internal/cli/cli.go`

**Step 1: Write failing reset test**

Add a test in `internal/index/index_test.go`:

```go
func TestResetStaleRunningSummaries(t *testing.T) {
    store := Store{Path: filepath.Join(t.TempDir(), "index.sqlite")}
    now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
    rec := session.Record{Tool: session.ToolPi, SourcePath: "/tmp/pi.jsonl", SessionID: "stale123456", Title: "(untitled session)", FirstPrompt: "(untitled session)", CWD: "/tmp", CreatedAt: now, UpdatedAt: now, ResumeArgv: []string{"pi"}, ResumeCommand: "pi"}
    if err := store.Rebuild([]session.Record{rec}); err != nil { t.Fatal(err) }
    row, ok, err := store.ClaimPendingSummary(now.Add(-2 * time.Hour))
    if err != nil || !ok { t.Fatalf("claim ok=%v err=%v", ok, err) }

    n, err := store.ResetStaleRunningSummaries(now, time.Hour)
    if err != nil { t.Fatal(err) }
    if n != 1 { t.Fatalf("reset %d rows", n) }
    got, err := store.Lookup(row.ID)
    if err != nil { t.Fatal(err) }
    if got.SummaryStatus != "pending" || !strings.Contains(got.SummaryError, "reset stale running summary") {
        t.Fatalf("status/error = %q/%q", got.SummaryStatus, got.SummaryError)
    }
}
```

Also add a test that a fresh running lease is not reset.

**Step 2: Run failing test**

```sh
go test ./internal/index -run ResetStaleRunningSummaries -count=1
```

Expected: FAIL because method does not exist.

**Step 3: Implement reset method**

In `internal/index/index.go`, add:

```go
func (s Store) ResetStaleRunningSummaries(now time.Time, maxAge time.Duration) (int64, error)
```

Rules:

- If `maxAge <= 0`, use `30 * time.Minute`.
- Cutoff is `now.Add(-maxAge).UTC().Format(time.RFC3339)`.
- Reset rows where:

```sql
summary_status = 'running'
generated_title = ''
AND (
  summary_started_at = ''
  OR summary_started_at < ?
)
```

- Set:

```sql
summary_status = 'pending',
summary_error = 'reset stale running summary after abandoned lease',
summary_started_at = ''
```

Return rows affected.

**Step 4: Call reset on daemon startup and polling**

In `internal/cli/cli.go`:

- Add a constant near `serviceLabel`:

```go
const summaryLeaseTimeout = 30 * time.Minute
```

- In `daemon()`, after `runScan()` and before starting `backgroundSummaryWorker`, call:

```go
if n, err := a.store().ResetStaleRunningSummaries(a.now(), summaryLeaseTimeout); err != nil {
    fmt.Fprintln(a.Stderr, "holocron daemon:", err)
} else if n > 0 {
    fmt.Fprintf(a.Stderr, "holocron daemon: reset %d stale running summaries\n", n)
}
```

- In `backgroundSummaryWorker.run`, before claiming work, call reset too. This catches stale rows even without daemon restart:

```go
if _, err := a.store().ResetStaleRunningSummaries(a.now(), summaryLeaseTimeout); err != nil {
    fmt.Fprintln(a.Stderr, "holocron background summary:", err)
    return
}
```

Because `backgroundSummaryWorker` already takes `writeMu`, keep this inside the locked section.

**Step 5: Run focused tests**

```sh
go test ./internal/index ./internal/cli
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/index/index.go internal/index/index_test.go internal/cli/cli.go
git commit -m "fix: reset stale running summaries"
```

---

### Task 4: Finalize jobs robustly on background errors

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`

**Step 1: Write background worker error test if practical**

The current background worker is hard to unit-test because it loops and calls real store methods. Prefer a small extraction rather than complex goroutine tests.

Extract one pure-ish method:

```go
func (a App) runOneBackgroundSummary(ctx context.Context, cfg summarizer.Config, writeMu *sync.Mutex) error
```

The method should perform one locked reset/claim/summarize/finalize cycle and return.

Before implementing, write a test that uses a fake Pi executable that exits non-zero and verifies the row becomes `failed`.

Example:

```go
func TestBackgroundSummaryMarksModelErrorFailed(t *testing.T) {
    home := t.TempDir()
    // create untitled fixture session and config with fake pi that exits 42
    // run fullScan
    cfg, err := summarizer.LoadConfig(home)
    if err != nil { t.Fatal(err) }
    app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Spinner: false}
    if err := app.runOneBackgroundSummary(context.Background(), cfg, nil); err != nil { t.Fatal(err) }
    row, err := app.store().Lookup("...")
    if err != nil { t.Fatal(err) }
    if row.SummaryStatus != "failed" || row.SummaryError == "" {
        t.Fatalf("row = %#v", row)
    }
}
```

Use helper patterns already present in `internal/cli/cli_test.go` for fixture sessions and fake executables.

**Step 2: Run failing test**

```sh
go test ./internal/cli -run TestBackgroundSummaryMarksModelErrorFailed -count=1
```

Expected: FAIL until the extraction exists.

**Step 3: Extract one-cycle worker**

In `internal/cli/cli.go`:

- Move the body of `backgroundSummaryWorker.run` into:

```go
func (a App) runOneBackgroundSummary(ctx context.Context, cfg summarizer.Config, writeMu *sync.Mutex) error
```

- The function should:
  1. lock `writeMu` if non-nil;
  2. reset stale running summaries;
  3. claim one pending summary;
  4. if no job, return nil;
  5. run `summarizeOne`;
  6. if error, attempt `SetSummaryStatus(row.ID, "failed", result.Err.Error())`;
  7. if that status write fails, return/log an error containing both the original and status-write error.

- `backgroundSummaryWorker` calls this function and logs returned errors.

**Step 4: Ensure success still becomes done**

`summarizeOne` already calls `UpdateGenerated` on success, which sets `summary_status = 'done'`. Do not duplicate success status updates.

**Step 5: Run focused tests**

```sh
go test ./internal/cli
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "fix: finalize background summary failures"
```

---

### Task 5: Add retry path for failed summaries

**Files:**
- Modify: `internal/index/index.go`
- Modify: `internal/index/index_test.go`
- Modify: `internal/cli/cli.go`

**Step 1: Verify existing force behavior**

Read `parseSummarizeArgs`, `summarize`, and current tests around `--force` in `internal/cli/cli.go` / `internal/cli/cli_test.go`.

Current `summarize` builds `todo` with:

```go
if opts.Force || r.SummaryStatus != "done" { ... }
```

That means failed/running rows are included when selected by ID. We need to ensure manual `--force` clears status and does not get blocked by `running` state.

**Step 2: Add test for forced retry of failed row**

In `internal/cli/cli_test.go`, add a test:

1. Create untitled session fixture.
2. Set summary status to `failed` with `store.SetSummaryStatus(id, "failed", "old error")`.
3. Configure fake Pi that returns valid JSON.
4. Run `holocron summarize <id> --force --json`.
5. Assert generated title exists and `summary_status = done`.

**Step 3: Run failing or confirming test**

```sh
go test ./internal/cli -run TestSummarizeForceRetriesFailedSummary -count=1
```

Expected: This may already pass. If it passes, keep it as regression coverage.

**Step 4: Add helper only if needed**

If the test reveals forced retry does not clear failed/running state, add a store method:

```go
func (s Store) ResetSummaryForRetry(id string) error
```

Set:

```sql
summary_status = 'pending', summary_error = '', summary_started_at = ''
```

Call it before summarizing when `opts.Force` is true.

**Step 5: Run focused tests**

```sh
go test ./internal/index ./internal/cli
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/index/index.go internal/index/index_test.go internal/cli/cli.go internal/cli/cli_test.go
git commit -m "test: cover forced summary retry"
```

If code changes were needed, use:

```sh
git commit -m "fix: reset summary state for forced retry"
```

---

### Task 6: Add visibility for stuck summary state

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`
- Optional modify: `internal/index/index.go`

**Step 1: Decide the smallest visibility surface**

Use existing `holocron service status`; do not add a new command in this iteration.

Add summary counts to status output:

```text
summaries: 95 pending, 3 running, 2 failed, 516 done
```

If stale running count is easy after Task 3, include:

```text
stale running summaries: 3
```

**Step 2: Add store method test**

In `internal/index/index_test.go`, add:

```go
func TestSummaryStatusCounts(t *testing.T) { ... }
```

Target method:

```go
func (s Store) SummaryStatusCounts() (map[string]int, error)
```

**Step 3: Implement counts**

In `internal/index/index.go`:

```sql
SELECT summary_status, COUNT(*) FROM sessions GROUP BY summary_status
```

Return a map. Ensure missing statuses read as zero at display time.

**Step 4: Update service status output**

Find `service status` implementation in `internal/cli/cli.go`. Append a summaries line after launchd/daemon status.

Test by invoking the service status function or `App.Run([]string{"service", "status"})` with `HOLOCRON_SKIP_SERVICE_CHECK` if needed. If launchctl makes this awkward, keep coverage at the store method and manually verify output.

**Step 5: Run tests**

```sh
go test ./internal/index ./internal/cli
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/index/index.go internal/index/index_test.go internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat: show summary queue health"
```

---

### Task 7: Documentation and manual recovery note

**Files:**
- Modify: `README.md`
- Modify: `docs/spec.md`
- Modify: `docs/plans/readme.md`

**Step 1: Update README**

In the summarize section, add a short note:

```md
Background summaries use recoverable leases. If the daemon restarts while a summary is running, Holocron resets stale running jobs and retries them later. Failed summaries can be retried with `holocron summarize ID --force`.
```

If Task 6 added status output, document:

```sh
holocron service status
```

as showing summary queue counts.

**Step 2: Update spec**

In `docs/spec.md`, add a short section under generated summaries/background indexing:

- summary jobs have `pending`, `running`, `done`, `failed` statuses
- running jobs have leases
- stale running jobs reset to pending
- failed jobs are retryable manually

**Step 3: Update plan index**

Add row:

```md
| 008 | [2026-05-30-summary-job-leases.md](2026-05-30-summary-job-leases.md) | Planned | Protect background summary jobs from abandoned `running` state with leases, stale reset, and queue health visibility. |
```

If implementing immediately, mark it `Done` in the final task.

**Step 4: Run docs-adjacent tests**

```sh
go test ./...
```

Expected: PASS.

**Step 5: Commit**

```sh
git add README.md docs/spec.md docs/plans/readme.md
git commit -m "docs: document summary job leases"
```

---

### Task 8: Final verification and local cleanup

**Files:**
- No planned changes unless fixes are needed.

**Step 1: Run full tests**

```sh
just test
```

Expected: PASS.

**Step 2: Build for demo/manual testing**

```sh
just build
```

Expected: binary builds and daemon update hook completes.

**Step 3: Verify current stuck rows recover**

Before or after installing the new daemon, inspect:

```sh
sqlite3 ~/.local/share/holocron/index.sqlite \
  "select id, summary_status, summary_started_at, summary_error from sessions where summary_status='running';"
```

Then restart daemon or run the new reset path. Verify stuck rows move out of `running` after the lease timeout/reset policy.

For immediate local cleanup after the fix is installed, if needed:

```sh
sqlite3 ~/.local/share/holocron/index.sqlite \
  "update sessions set summary_status='pending', summary_error='', summary_started_at='' where summary_status='running' and generated_title='';"
```

Then run:

```sh
./holocron summarize pi:019e7592 --force
```

Expected: generated title is written or the row is marked `failed` with a visible error.

**Step 4: Final commit if needed**

```sh
git status --short
git add <changed-files>
git commit -m "chore: finish summary lease verification"
```

Only commit if final fixes were required.

---

## Acceptance Criteria

- Running summary jobs have a lease timestamp and attempt count.
- Stale `running` jobs reset to `pending` automatically.
- Daemon startup cannot leave old `running` rows stranded forever.
- Background summary model errors mark rows `failed` with an error message.
- Manual forced summarize can retry failed/stuck rows.
- Queue health is visible through `service status` or equivalent CLI output.
- Existing generated titles/summaries survive rebuilds and migrations.
- `just test` passes.
- `just build` passes.
