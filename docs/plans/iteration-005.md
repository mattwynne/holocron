# Iteration 005: normalized transcript store and Pi continuation

## Context

Holocron currently indexes provider-native session files and can reopen them with their original tools. Summarization is out-of-band and provider parsing is duplicated in several places.

The product direction is to make agent sessions portable: find a session from Pi, Codex, Claude, or Gemini, then open or continue it in Pi with any model. Pi becomes the common viewer/continuation surface, while Holocron remains the local index and normalized memory store.

## Goal

Add a normalized transcript intermediate representation stored in SQLite, then generate Pi-compatible session exports lazily when the user wants to view or continue a session in Pi.

Core UX:

```sh
holocron view ID
holocron continue ID
holocron continue ID --model anthropic/claude-sonnet-4-5
```

Terminology:

- `open`: resume the original native provider session where possible.
- `view`: open a Pi-compatible exported copy of the session for inspection.
- `continue`: create/open a Pi-compatible continuation session intended to be mutated and continued with any Pi model.

## Non-goals

- Perfect replay fidelity for all provider-specific events.
- Writing to provider-native session files.
- Replacing `holocron open ID` native resume behavior.
- Storing generated Pi exports in Pi's normal session tree by default.
- Solving true read-only Pi sessions if Pi itself mutates `--session` files.

## User experience

### View a session in Pi

```sh
holocron view codex:abc123
```

Behavior:

1. Loads normalized transcript from SQLite.
2. Generates a Pi-compatible export under Holocron data if missing/stale.
3. Opens Pi on that exported session:

```sh
pi --session ~/.local/share/holocron/pi-exports/view/codex_abc123.jsonl
```

This is for reading and asking follow-up questions. If Pi mutates the export, that mutation is confined to Holocron's export cache, not the original provider session.

### Continue a session in Pi

```sh
holocron continue codex:abc123
holocron continue claude:def456 --model sonnet
```

Behavior:

1. Loads normalized transcript from SQLite.
2. Creates a new Pi continuation session under Holocron data, not a shared view cache.
3. Opens Pi on that continuation session.
4. Further conversation is intentionally persisted in that continuation file.

Suggested path:

```text
~/.local/share/holocron/pi-exports/continue/<id>/<timestamp>.jsonl
```

The continuation session header should include metadata pointing back to the original Holocron ID/source path, either in `parentSession` where appropriate or as a custom Holocron metadata entry.

### Native resume remains separate

```sh
holocron open codex:abc123
```

Still runs the native resume command, e.g. `codex resume ...`.

## SQLite normalized transcript schema

Extend the index with normalized transcript tables.

```sql
CREATE TABLE IF NOT EXISTS session_turns (
  session_id TEXT NOT NULL,
  idx INTEGER NOT NULL,
  role TEXT NOT NULL,
  text TEXT NOT NULL,
  tool_name TEXT NOT NULL DEFAULT '',
  tool_input TEXT NOT NULL DEFAULT '',
  is_error INTEGER NOT NULL DEFAULT 0,
  timestamp TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (session_id, idx),
  FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_exports (
  session_id TEXT NOT NULL,
  kind TEXT NOT NULL, -- view, continue
  path TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (session_id, kind, path)
);
```

Add to `sessions`:

```sql
normalized_hash TEXT NOT NULL DEFAULT '';
normalized_at TEXT NOT NULL DEFAULT '';
turn_count INTEGER NOT NULL DEFAULT 0;
```

`normalized_hash` is a digest of normalized turns. Use it to skip replacing unchanged turn rows and to decide whether view exports are stale.

## Internal model

Add a package such as `internal/transcript`:

```go
type Transcript struct {
    ID         string
    Tool       session.Tool
    SourcePath string
    SessionID  string
    CWD        string
    Title      string
    FirstPrompt string
    CreatedAt  time.Time
    UpdatedAt  time.Time
    Turns      []Turn
}

type Turn struct {
    Role      Role
    Text      string
    ToolName  string
    ToolInput string
    IsError   bool
    Timestamp time.Time
}

type Role string

const (
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
    RoleSystem    Role = "system"
)
```

The existing parser can either:

1. return both `session.Record` and `transcript.Transcript`, or
2. populate normalized turns on `session.Record`.

Prefer a separate `transcript.Transcript` if it keeps search/index concerns clean.

## Background service behavior

During daemon scans/rebuilds:

1. Discover provider session files.
2. Parse each file into existing `session.Record` metadata plus normalized `Transcript` turns.
3. Assign stable Holocron IDs.
4. Upsert `sessions` metadata.
5. Replace `session_turns` only when `normalized_hash` changed.
6. Delete stale `session_turns` for removed sessions.
7. Do not generate Pi exports by default, except optional recent prewarming later.

This keeps background indexing lightweight and avoids duplicating every session as files.

## Pi-compatible export format

Generate minimal Pi JSONL v3 from SQLite turns.

Header:

```json
{"type":"session","version":3,"id":"holocron-abc123","timestamp":"2026-05-27T10:00:00Z","cwd":"/original/cwd"}
```

User entry:

```json
{"type":"message","id":"a1b2c3d4","parentId":null,"timestamp":"2026-05-27T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"..."}],"timestamp":1770000001000}}
```

Assistant entry:

```json
{"type":"message","id":"b2c3d4e5","parentId":"a1b2c3d4","timestamp":"2026-05-27T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"..."}],"provider":"holocron","model":"imported-session","timestamp":1770000002000}}
```

Tool turns can be rendered as compact assistant text initially:

```text
Tool bash called with args {"command":"go test ./..."}
```

Prefer readable, safe exports over perfect fidelity.

## View vs continue export semantics

### View exports

Path:

```text
~/.local/share/holocron/pi-exports/view/<safe-id>.jsonl
```

- Deterministic path per Holocron session.
- Regenerated when `normalized_hash` changes.
- Safe to delete and recreate.
- If Pi mutates it during viewing, the next regeneration may overwrite it.

### Continue exports

Path:

```text
~/.local/share/holocron/pi-exports/continue/<safe-id>/<timestamp>.jsonl
```

- New file per continuation.
- Never overwritten by background rebuilds.
- Intended to be mutated by Pi.
- May later be indexed as a Pi/Holocron continuation session itself.

## Commands

### `holocron view ID [--model MODEL]`

- Ensure a fresh view export exists.
- Exec Pi:

```sh
pi --session <view-export> [--model MODEL]
```

### `holocron continue ID [--model MODEL]`

- Create a new continuation export.
- Exec Pi:

```sh
pi --session <continue-export> [--model MODEL]
```

### `holocron export-pi ID [--kind view|continue] [--json]`

- Generate export and print path/metadata.
- Useful for debugging and scripting.

### Existing commands

- `holocron open ID` remains native provider resume.
- `holocron today --json` should include normalized metadata eventually:

```json
{
  "turn_count": 42,
  "normalized_at": "..."
}
```

Avoid including export path in normal JSON unless it already exists; exports are lazy.

## Provider normalization rules

### Pi

- Parse existing Pi JSONL into normalized turns.
- Preserve user/assistant text.
- Convert tool calls/results to compact tool turns.
- Skip Holocron summarizer backend sessions.

### Codex

- Convert user messages to `RoleUser`.
- Convert assistant/final messages to `RoleAssistant`.
- Convert command/tool events to `RoleTool` with compact input/output.
- Preserve cwd/session metadata from `session_meta` and `turn_context`.

### Claude

- Convert human/user messages to `RoleUser`.
- Convert assistant messages to `RoleAssistant`.
- Convert tool use/result blocks to compact `RoleTool` turns.
- Prefer explicit cwd; fallback project slug only when path exists.

### Gemini

- Convert user/model turns into user/assistant turns.
- Preserve project cwd via Gemini project map.
- Compact tool/command outputs aggressively.

## Disk-space policy

This iteration should avoid eagerly writing Pi export files for every session.

Disk use should mainly be SQLite rows. Pi export files are created lazily for `view`, `continue`, or `export-pi`.

Add cleanup later:

```sh
holocron exports prune --older-than 30d
holocron exports status
```

Initial cleanup behavior:

- stale view exports can be overwritten or deleted;
- continuation exports are preserved unless explicitly pruned.

## Summarization implications

Summarization should eventually use normalized SQLite turns as its source of truth, not raw provider JSONL.

Future options:

1. Current generic model runner over normalized turns.
2. Pi-compatible view export plus temporary copy for Pi-native recap.
3. Pi SDK helper that seeds `SessionManager.inMemory()` from normalized turns and calls a model without writing a session.

This iteration does not need to replace summarization, but it should make that replacement straightforward.

## Tests

Add tests for:

- parser produces normalized turns for Pi fixture.
- parser produces normalized turns for Codex fixture.
- parser produces normalized turns for Claude fixture.
- parser produces normalized turns for Gemini fixture.
- huge tool outputs are truncated/omitted.
- `session_turns` are inserted and replaced only when hash changes.
- stale session turns are deleted on rebuild.
- Pi export JSONL is valid v3 and has deterministic linear parent IDs.
- `holocron view ID` execs `pi --session <view-export>`.
- `holocron continue ID --model sonnet` execs `pi --session <continue-export> --model sonnet`.
- view export is regenerated when normalized hash changes.
- continuation export is not overwritten by rebuild.
- `go test ./...` passes.

## Acceptance checks

- Background service stores normalized transcript turns in SQLite for indexed sessions.
- Running `holocron view ID` opens a Pi-compatible version of a Codex/Claude/Gemini/Pi session.
- Running `holocron continue ID --model MODEL` starts Pi from that imported history with the requested model.
- `holocron open ID` still resumes the native provider session.
- No eager Pi export file explosion during normal daemon indexing.
- Holocron summarizer backend sessions are not indexed or exported.
- Existing generated titles/summaries survive rebuilds.
- `go test ./...` passes.

## Risks and mitigations

- Risk: Pi mutates view exports.
  - Mitigation: view exports are cache files and can be regenerated; continuation has separate paths.

- Risk: SQLite grows large.
  - Mitigation: store compact normalized turns, truncate tool output, use VACUUM/prune later if needed.

- Risk: Provider-specific details are lost.
  - Mitigation: preserve essential readable transcript first; add richer conversion over time.

- Risk: Importing huge sessions into Pi is slow.
  - Mitigation: allow export compaction/truncation thresholds and warn on very large turn counts.

- Risk: The names `view` and `continue` are ambiguous.
  - Mitigation: document clearly: `open` = native resume, `view` = Pi inspection copy, `continue` = new Pi continuation.
