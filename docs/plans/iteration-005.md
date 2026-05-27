# Iteration 005: Claude-to-Pi normalized continuation

## Context

Holocron currently indexes provider-native session files and reopens them with their original tools. Summarization reads provider transcripts through ad hoc parsing. The product direction is to make sessions portable: find a session from Claude, Codex, Gemini, or Pi, then inspect or continue it in Pi with any model.

This iteration introduces the normalized transcript store and lazy Pi export path. To reduce risk, it proves the highest-value path first: **Claude Code session → normalized SQLite turns → Pi-compatible session → Pi continuation**.

## Goal

Make a real Claude Code session viewable and continuable in Pi:

```sh
holocron view cd:abc123
holocron continue cd:abc123 --model anthropic/claude-sonnet-4-5
```

Definitions:

- `open`: resume the original native provider session where possible.
- `view`: open a deterministic Pi-compatible cache copy for inspection.
- `continue`: create a new Pi-compatible session intended to be mutated and continued.

## Non-goals

- Perfect replay fidelity for provider-specific events.
- Writing to provider-native session files.
- Replacing `holocron open ID` native resume behavior.
- Storing generated Pi exports in Pi's normal session tree by default.
- Solving true read-only Pi sessions if Pi mutates `--session` files.
- Completing Codex, Gemini, and Pi normalization before the Claude-to-Pi path works end-to-end.

## Delivery strategy

### Phase 0: Pi export compatibility spike

Before schema or parser work, generate minimal Pi JSONL v3 from an in-memory transcript and verify manually:

```sh
pi --session /tmp/holocron-spike.jsonl
```

The spike transcript must include 2-3 user/assistant turns and at least one tool-use/tool-result pair, rendered in the candidate export format.

Acceptance for the spike:

- Pi opens the generated session.
- User and assistant messages appear in order.
- Pi accepts a follow-up user message and produces an answer that uses prior context.
- Pi appends new messages without corrupting the file.
- Pi does not regenerate or re-execute previous tool calls.
- Conversation threading works with Holocron-generated IDs and parent IDs.
- Model selection works with `pi --session <file> --model <model>`.
- The spike verifies whether Pi honors the session header `cwd`; if it does not, commands must `chdir` before exec.
- The spike documents required, optional, and ignored Pi JSONL fields, including timestamp formats and tool-call representation.

This phase prevents building storage around an export format Pi will not accept.

### Phase 1: Claude-to-Pi vertical slice

Implement the full path for Claude only:

1. Parse Claude JSONL into normalized turns.
2. Store those turns in SQLite during daemon indexing/rebuild.
3. Export stored turns to Pi JSONL lazily.
4. Add `holocron view` and `holocron continue`.
5. Keep `holocron open`, search, pick, and summarize working as they do today.

### Phase 2: provider expansion

After Claude works end-to-end, add Codex, Gemini, and Pi normalization through the same interfaces. These are follow-up tasks inside or after the iteration, not prerequisites for accepting the Claude-to-Pi slice.

Suggested order:

| Provider | Priority | Rationale |
|---|---:|---|
| Claude | 1 | Proves the target Claude-to-Pi workflow. |
| Pi | 2 | Provides a native-format baseline for export/import tests. |
| Codex | 3 | Valuable cross-provider continuation after the pipeline is proven. |
| Gemini | 4 | Likely highest parser complexity because of project mapping. |

## User experience

### View a Claude session in Pi

```sh
holocron view cd:abc123
```

Behavior:

1. Load normalized transcript turns from SQLite.
2. Generate or refresh a deterministic view export under Holocron data.
3. Exec Pi on that export:

```sh
pi --session ~/.local/share/holocron/pi-exports/view/cd_abc123.jsonl
```

View exports are cache files, not working sessions. If a view export was modified after Holocron wrote it, Holocron must not silently overwrite it. It should either warn and refuse, or overwrite only when an explicit force path exists later. Users who want to preserve follow-up work should use `holocron continue`.

### Continue a Claude session in Pi

```sh
holocron continue cd:abc123
holocron continue cd:abc123 --model sonnet
```

Behavior:

1. Load normalized transcript turns from SQLite.
2. Check that `pi` is available on `PATH`.
3. Create a new continuation export.
4. Exec Pi on that continuation session:

```sh
pi --session ~/.local/share/holocron/pi-exports/continue/cd_abc123/20260527T143012Z-a1b2c3d4.jsonl --model sonnet
```

Continuation files are never overwritten by rebuilds. They are intended to be mutated by Pi and may later be indexed as ordinary Pi/Holocron sessions.

### Native resume remains separate

```sh
holocron open cd:abc123
```

Still runs the native resume command, e.g. `claude --resume ...`.

## Internal model

Add `internal/transcript`.

```go
type ParsedSession struct {
    Record     session.Record
    Transcript Transcript
}

type Transcript struct {
    HolocronID        string
    ProviderSessionID string
    Tool              session.Tool
    SourcePath        string
    CWD               string
    CreatedAt         time.Time
    UpdatedAt         time.Time
    Turns             []Turn
    Hash              string
}

type Turn struct {
    Role         Role
    Text         string
    ToolName     string
    ToolInput    string
    ToolOutput   string
    IsError      bool
    Timestamp    time.Time
    MetadataJSON string
}

type Role string

const (
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
    RoleSystem    Role = "system"
)
```

Use `HolocronID` for Holocron's stable ID, and `ProviderSessionID` for Claude's native `sessionId`. Avoid naming any Holocron foreign key `session_id`; that name already means provider-native session ID in `session.Record`.

`ToolOutput` and `MetadataJSON` are lossy-preservation fields. The Claude-to-Pi slice may render tool data as text if Pi requires it, but Holocron should keep truncated tool output and compact provider metadata so the user can audit what was preserved.

Compute `Hash` from deterministic normalized content, not raw provider bytes. The hash input is:

- `role`, `text`, `tool_name`, truncated `tool_input`, truncated `tool_output`, `is_error`, and `metadata_json` for each turn;
- stable explicit provider timestamps when present;
- no fallback timestamps derived from file modtime or indexing time.

Use a stable encoding: either a canonical JSON encoder or an explicit struct-to-bytes function with fixed field order and no insignificant whitespace. Use SHA-256 and store lowercase hex.

The parser should return `ParsedSession` so search/open can keep using `session.Record` while the indexer also stores normalized turns. Avoid scattering transcript data across unrelated session metadata fields.

## SQLite schema

Increment `index.SchemaVersion` from 4 to 5.

Migration from v4 to v5:

```sql
ALTER TABLE sessions ADD COLUMN normalized_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN normalized_at TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN turn_count INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS session_turns (
  holocron_id TEXT NOT NULL,
  idx INTEGER NOT NULL,
  role TEXT NOT NULL,
  text TEXT NOT NULL,
  tool_name TEXT NOT NULL DEFAULT '',
  tool_input TEXT NOT NULL DEFAULT '',
  tool_output TEXT NOT NULL DEFAULT '',
  is_error INTEGER NOT NULL DEFAULT 0,
  timestamp TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (holocron_id, idx),
  FOREIGN KEY (holocron_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_exports (
  holocron_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  path TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  file_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (holocron_id, kind, path),
  FOREIGN KEY (holocron_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_session_exports_holocron_id ON session_exports(holocron_id);
CREATE INDEX IF NOT EXISTS idx_session_exports_kind_updated ON session_exports(kind, updated_at);
```

The `session_exports` table has two immediate uses:

1. record the source normalized hash used to generate an export;
2. record the generated file hash so Holocron can detect modified view exports before overwriting them.

It also supports future `exports status` and `exports prune`, but those commands are out of scope.

SQLite foreign keys are disabled by default. Implementation must either enable `PRAGMA foreign_keys=ON` on every connection or explicitly delete from `session_turns` and `session_exports` wherever sessions are deleted. Do not rely on cascade behavior unless tests prove it is enabled.

During `Rebuild` and `Upsert`:

1. Assign stable Holocron IDs.
2. Snapshot existing generated summaries and existing normalized metadata before destructive rebuild steps.
3. Upsert `sessions`, preserving generated titles and summaries.
4. If `normalized_hash` changed, replace that session's `session_turns` in one transaction.
5. Set `turn_count`, `normalized_hash`, and `normalized_at`.
6. Delete stale turns and export rows for removed sessions as part of rebuild cleanup.

Current rebuild deletes and reinserts `sessions`; therefore the implementation must either keep an old-normalization snapshot before deletion or change rebuild to be non-destructive. Without that, it cannot know whether hashes changed.

If normalization fails for a file, continue indexing its normal search metadata when possible and leave normalized fields empty.

## Claude normalization rules

For phase 1:

- Convert human/user messages to `RoleUser`.
- Convert assistant text to `RoleAssistant`.
- Convert tool use and tool result blocks to compact `RoleTool` turns.
- Store tool input in `ToolInput` and truncated tool result text in `ToolOutput`.
- Truncate large tool output aggressively and mark truncation in `Text` or `ToolOutput`.
- Preserve `sessionId`, `cwd`, timestamps, source path, and compact provider metadata.
- Prefer explicit `cwd`; fall back to decoded project path only when it exists.
- If no cwd is available, use the user's home directory as the export cwd and record the fallback in metadata.
- Continue skipping Holocron summarizer backend sessions.

Document what is preserved, discarded, or lossy. Known lossy areas include provider-specific thinking blocks, token usage, stop reasons, multiple structured tool calls in one model message, and attachments unless Phase 0 proves Pi can preserve them usefully.

For tool turns, prefer readable text over perfect fidelity unless the Phase 0 spike shows Pi has a first-class tool-call/tool-result format that works for imported sessions. Example readable rendering:

```text
Tool Bash called with input {"command":"go test ./..."}

Output:
<first N chars>
```

## Pi-compatible export

Create an exporter package, for example `internal/piexport`, that writes minimal Pi JSONL v3 from stored turns.

Header shape, confirmed or adjusted by the Phase 0 spike:

```json
{"type":"session","version":3,"id":"holocron-cd-abc123","timestamp":"2026-05-27T10:00:00Z","cwd":"/original/cwd"}
```

Message shape, confirmed or adjusted by the spike:

```json
{"type":"message","id":"a1b2c3d4","parentId":null,"timestamp":"2026-05-27T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"..."}],"timestamp":1770000001000}}
```

Exporter requirements:

- deterministic linear parent IDs for a given transcript;
- first message has `parentId: null` unless Phase 0 proves Pi requires another value;
- each later message has `parentId` set to the previous exported message ID;
- message IDs are deterministic, derived from `(holocron_id, export kind seed, turn index, role)` or an equivalent stable input;
- session-level and outer message timestamps use the ISO/RFC3339 shape Pi accepts;
- inner `message.timestamp` uses Unix milliseconds if Phase 0 confirms that Pi requires it;
- missing turn timestamps fall back to the transcript/session timestamp for export, but fallback timestamps are not included in `normalized_hash`;
- atomic writes use unique temp names such as `<path>.tmp.<pid>.<random>` followed by rename;
- safe path names are derived from Holocron IDs;
- tool turns use Pi's structured tool format if Phase 0 confirms one, otherwise render as assistant text;
- before execing Pi, command code must either `chdir` to transcript cwd or rely on Pi's session `cwd` only if Phase 0 proves Pi honors it for tool execution.

## Export semantics

### View exports

Path:

```text
~/.local/share/holocron/pi-exports/view/<safe-id>.jsonl
```

Rules:

- one deterministic path per Holocron session;
- regenerate when `normalized_hash` differs from the stored export hash;
- record both source hash and file hash in `session_exports`;
- before overwriting an existing view export, compare its current file hash with the recorded `file_hash`;
- if the file was modified, return a clear warning and do not overwrite silently;
- safe to delete and recreate when unmodified.

### Continue exports

Path:

```text
~/.local/share/holocron/pi-exports/continue/<safe-id>/<timestamp>-<random-8-hex>.jsonl
```

Rules:

- create a new file for each continuation;
- check that `pi` is available before creating the file;
- create with an existence check and never overwrite an existing file;
- retry up to three times with a new random suffix if a path collision occurs;
- record the export in `session_exports` after a successful write.

## Commands

### `holocron view ID [--model MODEL]`

Ensure a fresh view export exists, then exec:

```sh
pi --session <view-export> [--model MODEL]
```

### `holocron continue ID [--model MODEL]`

Create a continuation export, then exec:

```sh
pi --session <continue-export> [--model MODEL]
```

### `holocron export-pi ID [--kind view|continue] [--json]`

Optional but useful for debugging. It should generate the export and print path/metadata without execing Pi.

Implementation must update CLI dispatch, argument parsing, error messages, and usage/help text for all new commands.

## Error handling

If normalized turns are missing:

```text
session cd:abc123 has no normalized transcript; wait for the daemon to re-index it or restart the service
```

If Pi is unavailable:

```text
pi executable not found in PATH
```

For continuation exports, detect this before creating the file.

If a view export was modified since Holocron wrote it:

```text
view export was modified; use holocron continue to preserve follow-up work or delete the cached export to regenerate it
```

If export generation fails, never leave a partial file at the final path. Use atomic writes with unique temp names.

If one file fails normalization during daemon indexing, warn and continue indexing other files.

## Disk-space policy

Do not eagerly write Pi export files during normal daemon indexing. Disk use should mainly be SQLite rows. Pi exports are created only by `view`, `continue`, or `export-pi`.

Future cleanup commands are out of scope:

```sh
holocron exports status
holocron exports prune --older-than 30d
```

The `session_exports` table should make those commands straightforward later.

## Summarization implications

This iteration does not replace summarization. It should, however, make replacement straightforward by making normalized turns the future source of truth. Existing generated titles and summaries must survive rebuilds.

## Tests

Add tests in this order.

### Export-format tests

- Synthetic transcript exports to valid JSONL.
- Parent IDs are deterministic and linear.
- First message parent is `null`; later parents point to the previous exported message.
- Message IDs are deterministic for a stable transcript.
- Timestamps are present and use the documented Pi formats.
- Tool turns render in the Phase 0-approved shape.
- Atomic write failure does not leave a final partial file.
- Concurrent view export attempts use unique temp files and do not corrupt the final export.

### Claude normalization tests

- User messages become `RoleUser`.
- Assistant text becomes `RoleAssistant`.
- Tool use/result blocks become compact `RoleTool` turns.
- Huge tool output is truncated.
- `cwd`, provider `sessionId`, and timestamps survive.
- Missing cwd falls back to home and records metadata.
- Malformed or truncated JSONL is handled consistently with existing parser behavior.
- Unicode, emoji, and special characters survive normalization and export.
- Empty or system-only sessions do not crash normalization.

### Store tests

- Schema migrates from version 4 to version 5.
- `session_turns` and `session_exports` are deleted when a session is deleted, either through foreign keys or explicit deletes.
- Turns insert on first index.
- Turns are not replaced when hash is unchanged.
- Turns are replaced when hash changes.
- Rebuild snapshots old normalized hashes before deleting sessions, or uses a non-destructive strategy.
- Stale turns are deleted on rebuild.
- Generated titles and summaries survive rebuild.
- Modified view exports are detected by file hash.

### CLI tests

- `view` dispatches from `App.Run` and appears in usage text.
- `continue` dispatches from `App.Run` and appears in usage text.
- `export-pi` dispatches from `App.Run` and appears in usage text if included.
- Argument parsing rejects missing IDs and unknown flags.
- `view` execs `pi --session <view-export>`.
- `continue --model sonnet` execs `pi --session <continue-export> --model sonnet`.
- Command execution uses the transcript cwd, or tests prove Pi's session cwd is sufficient.
- Missing normalized transcript gives a useful error.
- Missing `pi` fails before creating a continuation export.
- Continuation export is never overwritten.
- Existing `open` still execs the native provider command.

## Acceptance checks

Iteration 005 is accepted when:

- Phase 0 documents the Pi JSONL fields, timestamp formats, cwd behavior, parent ID behavior, and tool-turn representation Holocron uses.
- A real Claude session opens in Pi with `holocron view`.
- A real Claude session continues in Pi with `holocron continue --model MODEL` and preserves useful prior context.
- Claude normalized turns are stored in SQLite during daemon indexing.
- View exports are lazy, deterministic, regenerable, and not silently overwritten after user modification.
- Continuation exports are lazy, unique, and preserved.
- Existing search, pick, open, and summarize behavior still works.
- Holocron summarizer backend sessions are not indexed or exported.
- `go test ./...` passes.
- `just build` passes.

Codex, Gemini, and Pi normalization can follow after the Claude-to-Pi path is proven.

## Risks and mitigations

- Risk: Pi rejects or corrupts generated JSONL.
  - Mitigation: run the Phase 0 export spike before schema/parser work, including real continuation and tool-turn checks.

- Risk: Pi runs continued tools from the wrong directory.
  - Mitigation: verify cwd behavior in Phase 0; `chdir` before exec unless Pi's session cwd is sufficient.

- Risk: Claude parsing loses important context.
  - Mitigation: preserve readable text, truncated tool output, and compact metadata; document what is lossy.

- Risk: SQLite migrations leave stale child rows.
  - Mitigation: enable foreign keys on every connection or explicitly delete child rows; test both delete and rebuild paths.

- Risk: Rebuild cannot compare normalized hashes after deleting sessions.
  - Mitigation: snapshot old normalized metadata before deletion or make rebuild non-destructive.

- Risk: SQLite grows large.
  - Mitigation: store compact normalized turns and truncate tool output.

- Risk: View exports are mutated by Pi.
  - Mitigation: record generated file hash and refuse to overwrite modified view exports silently.

- Risk: Continuation exports collide.
  - Mitigation: use timestamp plus random 8-hex suffix, existence checks, and retries.

- Risk: Normalization failures break indexing.
  - Mitigation: keep metadata indexing best-effort and leave normalized fields empty for failed files.
