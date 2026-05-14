# Iteration 002: harden MVP implementation

## Context

Iteration 001 implemented the first Holocron MVP skeleton:

- session discovery/parsing for Pi, Codex, Claude, and Gemini
- stable IDs and resume commands
- SQLite/FTS search
- `search`, `open`, placeholder `pick`, `daemon`, and `service` commands
- launchd plist generation and basic tests

A three-model code review was run after the implementation. Source reports:

- Gemini: `/tmp/pi-review-vRsXt0/gemini.md`
- Codex: `/tmp/pi-review-vRsXt0/codex.md`
- Claude: `/tmp/pi-review-vRsXt0/claude.md`

This iteration focuses on correctness and spec compliance before adding more product features.

## Goals

1. Replace fragile SQLite CLI usage with a real Go SQLite integration.
2. Make daemon indexing near-instant using filesystem watching and debounce.
3. Prevent bulky tool outputs/file contents from being indexed.
4. Make daemon locking robust.
5. Improve error handling/logging around parsing and index corruption.
6. Keep the code well-tested and aligned with `docs/spec.md`.

## Non-goals

- Full polished Bubble Tea TUI for `pick`.
- Custom configurable source paths.
- Incremental per-file indexing beyond what is needed for watcher-driven updates, unless it naturally falls out of the SQLite refactor.
- Cross-platform service management.

## Findings to address

### 1. Replace `sqlite3` subprocess calls

Current code shells out to `sqlite3` in `internal/index/index.go`. This causes:

- no parameterized queries;
- fragile quoting;
- runtime dependency on `sqlite3` being installed and on `PATH`;
- connection-local pragmas like `busy_timeout` not applying to later queries;
- process-spawn overhead.

Implement a Go SQLite driver instead.

Preferred path:

- Try `modernc.org/sqlite` first.
- Verify FTS5 works in tests.
- Use `database/sql` parameterized queries throughout.
- Apply connection setup consistently:
  - `PRAGMA journal_mode=WAL`
  - `PRAGMA busy_timeout=5000`
  - schema version via `PRAGMA user_version`
- Add schema version checking. For MVP, rebuilding the index on incompatible schema is acceptable.

Acceptance checks:

- No production code calls `exec.Command("sqlite3", ...)`.
- Tests cover FTS search and JSON result shape.
- Tests prove query inputs containing quotes/metacharacters do not break SQL.

### 2. Implement filesystem watching and debounce

Current daemon does a full scan every 30 seconds. The spec requires near-instant background indexing:

- recursively walk all source roots on startup;
- watch all existing source subdirectories;
- add watches for newly created subdirectories;
- debounce file changes for 1 second;
- keep periodic full rescan as a safety net.

Use `fsnotify` unless there is a strong reason not to.

Acceptance checks:

- Daemon has a watcher path, not just polling.
- File create/write/delete events schedule indexing after a 1s debounce.
- New subdirectories get watched.
- Deleted source files are removed from the index.
- Existing periodic rescan can remain as safety, but not as the primary freshness mechanism.

### 3. Make transcript extraction tool-result aware

Current parser can append generic `content`, `message`, or `response` fields from any JSON object. That can index huge tool outputs or file contents, contrary to the spec.

Improve extraction so that:

- user/assistant conversational text is included;
- tool names and compact arguments/paths/commands are included;
- tool stdout/stderr/result/content fields over 1 KiB are skipped;
- file-read outputs index the file path/tool call, not full file contents, where detectable;
- malformed final JSONL lines in active files are ignored and retried later;
- malformed non-final lines are logged with path and line number.

Acceptance checks:

- Tests with large tool output show it is not present in `Record.Transcript`.
- Tests with normal user/assistant text still index the text.
- Tests with tool calls index useful command/path metadata.

### 4. Make daemon locking atomic

Current lock code checks then writes a lock file, which is racy. Replace with an atomic/exclusive mechanism.

Options:

- `os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)` plus stale-lock cleanup/retry;
- or an OS-level lock held for the daemon lifetime.

Lock file should still contain PID and start time for diagnostics.

Acceptance checks:

- Tests cover stale lock removal.
- Tests cover active lock refusal.
- Implementation cannot let two daemon startups both acquire the lock through a check/write race.

### 5. Improve error handling and logging

Current code ignores several errors, including JSON unmarshal of stored `resume_argv` and stale index deletion. Parsing also silently skips malformed non-final JSONL lines.

Add explicit error handling/logging for:

- malformed JSONL non-final lines: warn and continue;
- invalid stored `resume_argv`: return a clear error or repair if safe;
- stale source deletion failures: report/log;
- SQLite/schema/FTS initialization failures: clear actionable messages.

Daemon logs go to launchd stdout/stderr paths already defined by the service plist.

Acceptance checks:

- No ignored errors in critical index/open paths unless accompanied by a comment explaining why it is safe.
- Tests cover invalid stored resume argv if practical.

### 6. Fix or document known secondary issues

Address if cheap while working nearby:

- FTS duplicate insertion risk when incremental updates are introduced: ensure FTS rows are replaced/deleted atomically with session rows.
- Claude project slug fallback should verify decoded cwd exists before using it. Prefer explicit `cwd` in JSONL.
- Add parser fixtures/tests for Pi, Claude, and Gemini beyond the current Codex-heavy tests.
- Use standard XML escaping for plist generation if touching service code.
- Canonicalize `os.Executable()` with `filepath.EvalSymlinks` if practical.

## Suggested implementation order

1. Refactor `internal/index` to `database/sql` + SQLite driver with parameterized queries and schema init/version checks.
2. Add/repair tests for search, stable IDs, deletes, invalid JSON, and SQL metacharacters.
3. Refactor transcript extraction and add bulky-output tests.
4. Replace daemon lock with atomic locking.
5. Add fsnotify watcher/debounce daemon loop.
6. Run `gofmt` and `go test ./...`.

## Definition of done

- `go test ./...` passes.
- `docs/spec.md` remains accurate; update it only if an implementation discovery invalidates it.
- Review findings above are either fixed or explicitly called out with a small TODO and rationale.
- No new private session content or machine-specific sensitive data is committed.
