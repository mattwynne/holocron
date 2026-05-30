# Holocron spec

## Mission

Help me find and reopen old AI coding-agent sessions by keyword, date, project, tool, and title/first prompt.

Holocron is local-only and intended to be safe to open source: the spec should describe formats and behaviour without embedding private session content.

## Platform

MVP is macOS-only because background indexing is managed by a per-user `launchd` service.

## Sources

Default sources for the current user:

- Pi: root `~/.pi/agent/sessions`, files `*.jsonl` recursively
- Codex: root `~/.codex/sessions`, files `*.jsonl` recursively; metadata may also be available in `~/.codex/state_5.sqlite` table `threads`
- Claude Code: root `~/.claude/projects`, files `*/*.jsonl`; optional project indexes at `~/.claude/projects/*/sessions-index.json`
- Gemini CLI: root `~/.gemini/tmp`, files matching `*/chats/session-*.jsonl`; project metadata in `~/.gemini/projects.json`

Configurable source paths are not required for the MVP.

Implementation note: `**` glob syntax is shorthand only. Go code must explicitly walk source roots recursively; `filepath.Glob` is not sufficient for recursive sources.

Missing source roots are normal and should not fail indexing.

## Normalized session record

- `id`: readable/stable ID
- `tool`: `pi`, `codex`, `claude`, or `gemini`
- `source_path`
- `session_id`
- `title`: explicit stored title/name/summary if present, otherwise first user prompt
- `first_prompt`
- `project_path` / `cwd`
- `created_at`
- `updated_at`
- `file_size_bytes`
- `message_count`
- `resume_argv`: argv for reopening the session
- `resume_command`: display shell command, equivalent to `cd <cwd> && exec ...`
- searchable transcript text

The indexer must resolve and store the correct original working directory during indexing. Opening a session always changes to the indexed `cwd` first. If the `cwd` no longer exists at open time, fail with a clear error.

### Stable IDs

IDs are normally `<tool-prefix>:<prefix>`, where `<prefix>` is the shortest unique prefix of `session_id`, with a minimum of 8 characters. Tool prefixes are two letters: `pi`, `cx` for Codex, `cd` for Claude, and `gm` for Gemini.

Stability rule:

- Store assigned IDs in SQLite.
- Never rename an existing assigned ID during normal reindexing.
- If a new session collides with an existing ID, lengthen only the new session's prefix until unique.
- If the full `session_id` still collides for the same tool, append a short stable hash of `source_path`.

This keeps copied `holocron open <id>` commands stable as new sessions are discovered.

### Working directory resolution

Prefer explicit cwd/project metadata from the session file or companion metadata.

Known strategies:

- Pi: first JSONL record has `type: "session"`, an `id`, timestamp, and `cwd`.
- Codex: first JSONL record has `type: "session_meta"`; `payload.id`, `payload.timestamp`, and `payload.cwd` are authoritative. Later `turn_context.payload.cwd` may be used as fallback. Codex sqlite metadata may be used only if JSONL cwd is missing.
- Claude Code: prefer `cwd` in JSONL entries and `sessionId` from the file contents or filename. If `cwd` is absent, decode the project directory slug under `~/.claude/projects/`; it encodes the absolute path by replacing path separators with `-` and preserving path components. Example: `-Users-matt-git-example-project` decodes to `/Users/matt/git/example/project`. Verify the decoded path exists.
- Gemini CLI: first JSONL record has `sessionId`, `projectHash`, `startTime`, and `lastUpdated`. Use `~/.gemini/projects.json` to map `projectHash` or project slug/session location back to project path.

Index sessions even if their cwd no longer exists, because they remain useful search results. `open` is responsible for failing clearly if the indexed cwd does not exist.

## Resume commands

Holocron should reopen by `exec`ing the original tool from the indexed cwd.

Expected argv shapes:

- Claude Code: `["claude", "--resume", "<session_id>"]`
- Codex: `["codex", "resume", "<session_id>"]`
- Pi: `["pi", "--session", "<source_path>"]`; prefer `source_path` because Pi accepts an exact session file path and this avoids ambiguous partial IDs
- Gemini CLI: `["gemini", "--resume", "<session_id>"]`

`holocron open <id>` and TUI selection both validate that `cwd` exists, then `chdir` there and execute the tool-specific command.

Actual reopening must not invoke a shell. Use argv execution (`execve`-style). `resume_command` is display-only and must be POSIX-shell-quoted, including cwd and any source path.

## Search index

- Index location: `~/.local/share/holocron/index.sqlite`
- Use SQLite with FTS5.
- Use Go; try `modernc.org/sqlite` first and verify FTS5 support.
- Store schema version in `PRAGMA user_version`; for MVP, a schema mismatch may rebuild the index from source files.
- Use WAL mode and a busy timeout so the daemon can write while CLI commands search:
  - `PRAGMA journal_mode=WAL`
  - `PRAGMA busy_timeout=5000`
- Index full transcript text locally.
- Transcript text should include user/assistant conversational text and useful tool names/commands/arguments, but avoid bulky tool outputs/file contents where detectable.
- Ranking should use the search engine rather than hand-rolled matching.

Suggested FTS columns and weights:

```sql
-- Use equivalent BM25 weights even if the physical schema differs.
-- title and first_prompt are most important, cwd is useful, transcript is normal.
bm25(sessions_fts, 10.0, 10.0, 3.0, 1.0)
```

### Transcript extraction rules

MVP extraction rules:

- Include all user-authored prompt/message text.
- Include assistant response text.
- Include tool names and compact tool arguments such as shell commands, file paths, and edit summaries.
- Exclude tool stdout/stderr/result fields over 1 KiB.
- For file-read outputs, index the file path and tool call, not the full file contents, when detectable.
- Ignore malformed partial JSONL lines at the end of actively written files; retry on the next file event.

Per-tool parsers should be tested against sanitized fixtures.

## Background indexing

Indexing is owned by a macOS `launchd` service, not by a user-facing `index` command.

- Service label: `net.mattwynne.holocron`
- Plist path: `~/Library/LaunchAgents/net.mattwynne.holocron.plist`
- Logs:
  - `~/Library/Logs/holocron/daemon.log`
  - `~/Library/Logs/holocron/daemon.err.log`
- Lock file: `~/.local/share/holocron/daemon.lock`

Daemon behaviour:

- On start, perform a full scan of default source locations.
- Recursively walk all source roots on full scan.
- Watch all existing source subdirectories.
- Add watches for newly created subdirectories.
- Tolerate missing source roots.
- Debounce file changes for 1 second before indexing.
- Include active/in-progress sessions in search results.
- Remove sessions from the index when their source files are deleted.
- Optionally perform periodic safety rescans.
- Ensure only one daemon/indexer runs at a time.

`holocron daemon` may exist as an internal/hidden command used by launchd.

### Summary job leases

Generated-summary jobs use `pending`, `running`, `done`, and `failed` states. When the daemon claims a pending summary, it records a `summary_started_at` lease timestamp and increments an attempt count. If a daemon exits or loses the database before finalising a job, a later daemon run resets stale `running` jobs back to `pending` so they are not stranded forever.

Model and Pi execution errors mark the row `failed` with `summary_error`. Failed rows are visible in summary queue health and can be retried manually with `holocron summarize ID --force`.

### Daemon lock and health

The lock file contains the daemon PID and start time. On daemon startup, if the lock exists but the PID is not running, remove the stale lock and continue. If the PID is running, exit with a clear error.

CLI commands check service health with `launchctl` for label `gui/$UID/net.mattwynne.holocron`. A valid loaded/running service is required for `search` and `pick` in the MVP.

### launchd plist contract

`holocron service install` writes a plist using:

- absolute executable path in `ProgramArguments`
- arguments equivalent to `["/absolute/path/to/holocron", "daemon"]`
- `RunAtLoad: true`
- `KeepAlive: true`
- `StandardOutPath: ~/Library/Logs/holocron/daemon.log`
- `StandardErrorPath: ~/Library/Logs/holocron/daemon.err.log`

Do not rely on shell `PATH` inside launchd.

## MVP commands

```sh
holocron search "query terms"
holocron search "query terms" --json
holocron pick [query terms]
holocron open <id>
holocron ask <id> [question]
holocron service install
holocron service uninstall
holocron service start
holocron service stop
holocron service status
```

No user-facing `holocron index` command in the MVP.

### `search`

`holocron search "query terms"` is agent/script friendly:

- requires a non-empty query; `holocron search` with no query prints usage/help
- does not open a TUI
- does not exec/open sessions
- prints a compact ranked table: `ID`, `TOOL`, `UPDATED`, `SIZE`, `TITLE`
- no filter flags required for MVP
- no verbose mode required for MVP

For MVP, date/project/tool lookup can work through indexed fields and normal query terms. Dedicated filters such as `--tool`, `--cwd`, `--after`, and `--before` are deferred.

`holocron search "query terms" --json` returns a JSON array:

```json
[
  {
    "id": "cd:0cd2fc8c",
    "tool": "claude",
    "title": "Fix auth redirect handling",
    "cwd": "/Users/matt/git/example/project",
    "updated_at": "2026-05-13T15:04:05Z",
    "size": "42 KB",
    "file_size_bytes": 43008,
    "message_count": 37,
    "resume_argv": ["claude", "--resume", "0cd2fc8c-84e8-45a6-8a19-5645a83f6d4a"],
    "resume_command": "cd /Users/matt/git/example/project && exec claude --resume 0cd2fc8c-84e8-45a6-8a19-5645a83f6d4a"
  }
]
```

JSON snippets/highlights are not required for MVP.

If the service is not running, search should fail clearly and tell the user to install/start the service.

### `pick`

`holocron pick [query terms]` is the human TUI:

- opens an interactive picker, optionally prefilled with query terms
- with no query, shows the 100 most recently updated sessions and allows filtering/refining
- result rows should show enough to choose confidently:
  - title
  - project path / cwd
  - last modified
  - relative scale, e.g. file size and message count
- selecting a result immediately `exec`s the resumed tool session from the indexed cwd

TUI library and keybindings are implementation details; Bubble Tea is a good default for Go.

If the service is not running, pick should fail clearly and tell the user to install/start the service.

### `open`

`holocron open <id>`:

- looks up the indexed session
- validates `cwd` exists
- changes to `cwd`
- uses argv execution to `exec` the appropriate resume command

If the source session file is missing at open time, fail clearly and remove the stale session from the index.

### `ask`

`holocron ask <id> [question]`:

- loads the indexed session transcript as context
- reads the question from arguments, piped stdin, or an interactive prompt
- runs Pi prompt mode without tools or session mutation
- prints only the model answer to stdout

### `service`

`holocron service install` should write/load the launchd plist and start the daemon immediately. The daemon then performs initial indexing in the background.

`start`, `stop`, `status`, and `uninstall` are for normal service management and troubleshooting.

## Error handling

MVP error behaviour:

- Missing source roots during indexing: ignore.
- Malformed JSONL line before end-of-file: log warning and continue parsing later lines if possible.
- Malformed final JSONL line in active file: ignore for now and retry on later change.
- Permission error reading a source file: log warning, skip that file, continue.
- Corrupt SQLite index: print a clear error suggesting reinstalling/restarting the service; daemon may rebuild if safe.
- Service not running for `search`/`pick`: exit non-zero with instructions to run `holocron service install`.
- Unknown `open` ID: exit non-zero with a clear not-found error.
- Missing cwd for `open`: exit non-zero with the indexed path in the error.

## Testing guidance

Use sanitized fixtures for each source format. Tests should cover:

- parser extraction of session id, cwd, timestamps, title, first prompt, transcript text, message count
- stable ID collision behaviour
- recursive source walking
- deleted source file removal
- JSON output schema
- argv construction and shell-quoted display command
