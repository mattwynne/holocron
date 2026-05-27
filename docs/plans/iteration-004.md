# Iteration 004: session summarization and naming

Status: Done

## Goal

Add Holocron-native generated titles and summaries for indexed agent sessions, using a configurable model backend. The first implementation should write generated metadata to Holocron's SQLite index only. Provider-native write-back can remain a later, explicit feature.

## User Experience

Add commands like:

```sh
holocron summarize ID
holocron summarize --missing --limit 20
holocron summarize --recent --limit 20
holocron summarize --dry-run ID
```

The command should show progress while it works:

```text
Reading pi ~/.pi/agent/sessions/...jsonl
Extracting session skeleton
Summarizing chunk 2/8
Writing generated metadata
```

Search, `today`, and `pick` should display titles in this priority order:

1. Provider-native manual/generated title.
2. Holocron generated title.
3. First user prompt.
4. `(untitled session)`.

## Configuration

Add a config file, for example:

```text
~/.config/holocron/config.toml
```

Initial config shape:

```toml
[summarizer]
command = ["pi", "-p", "--no-tools"]
max_input_tokens = 4000
chunk_tokens = 6000
max_chunks = 20
timeout_seconds = 120

[summarizer.selection]
include_first_turns = 3
include_last_turns = 5
include_user_messages = true
include_assistant_finals = true
include_tool_calls = true
include_file_paths = true
max_tool_output_chars = 1000
```

The command backend should be generic: Holocron sends a prompt on stdin and expects strict JSON on stdout. Pi can be the default backend because it is already available locally and can route to configured providers.

## SQLite Schema

Extend the `sessions` table with generated metadata fields:

```sql
generated_title TEXT NOT NULL DEFAULT '';
generated_summary TEXT NOT NULL DEFAULT '';
generated_at TEXT NOT NULL DEFAULT '';
generated_by TEXT NOT NULL DEFAULT '';
```

Consider a future separate `session_annotations` table if we want multiple summaries per session, versioned prompts, or separate user-edited annotations. For the first pass, columns on `sessions` are simpler.

## Large Session Strategy

Do not send a whole JSONL file to the model. Use an extractive skeleton plus hierarchical summarization.

### 1. Parse the Session

Reuse the existing session parser to extract normalized transcript text and metadata:

- tool/provider
- session ID
- cwd/folder
- timestamps
- provider title, if any
- first user prompt
- message count
- file size

Improve parsing where needed so Holocron can identify:

- user messages
- assistant messages
- tool calls
- file paths read/edited/written
- shell commands
- final assistant response
- errors or failed commands

### 2. Build a Skeleton

Create a compact, mostly extractive summary before calling the model:

- first few user prompts
- all later user prompts, capped if necessary
- final few assistant responses
- file paths mentioned by tools
- commands run
- explicit commits, tests, URLs, or artifacts
- cwd and tool name

This skeleton should often be enough to name a session without chunking.

### 3. Chunk by Turns

For sessions that exceed the configured input budget:

- split by conversational turns, not bytes
- preserve message order inside each chunk
- keep chunks under `chunk_tokens`
- always include the beginning and end of the session
- cap huge tool outputs to `max_tool_output_chars`
- drop or compress repeated logs, dependency output, and long generated diffs

### 4. Map Summaries

Ask the model to summarize each chunk into strict JSON:

```json
{
  "goals": [],
  "decisions": [],
  "files": [],
  "commands": [],
  "outcomes": [],
  "open_questions": []
}
```

Store chunk summaries in memory for the first implementation. A later version can cache them.

### 5. Reduce Summaries

Feed the skeleton plus chunk summaries to the model and ask for final JSON:

```json
{
  "title": "Add recent sessions command",
  "summary": "Implemented a Holocron command that lists sessions updated in the last 24 hours across supported agents. Added SQLite query support, CLI output, tests, and docs.",
  "status": "implemented",
  "key_files": ["internal/cli/cli.go", "internal/index/index.go"],
  "confidence": "high"
}
```

Title guidance:

- eight words or fewer
- no trailing period
- concrete and searchable
- prefer task/outcome over vague topic
- avoid `Session about...`

## Prompt Contract

Holocron should require strict JSON and validate it. If parsing fails, retry once with a repair prompt. If it still fails, report the error and do not update the index.

The final prompt should include:

- the output JSON schema
- title style rules
- instruction to avoid inventing files or outcomes
- the skeleton
- chunk summaries or selected transcript excerpts

## Safety and Cost Controls

- Default to `--dry-run` behavior in tests only; normal command writes Holocron metadata.
- Never write to provider session files in this feature.
- Respect `--limit` and `max_chunks`.
- Skip sessions that already have `generated_title` unless `--force` is passed.
- Show model command and prompt size in verbose mode.
- Time out model calls.
- Do not include raw secrets if the parser can cheaply detect common secret patterns.

## Implementation Steps

1. Add config loading from `~/.config/holocron/config.toml` with sensible defaults.
2. Add schema migration support for generated metadata fields.
3. Extend index result/title selection to prefer generated titles when provider title is empty.
4. Add transcript selection code that produces a skeleton and turn chunks.
5. Add a model command runner that sends prompt via stdin and captures stdout/stderr with timeout.
6. Add strict JSON parsing and one retry/repair path.
7. Add `holocron summarize` CLI with `ID`, `--missing`, `--recent`, `--limit`, `--force`, `--dry-run`, and `--json`.
8. Add tests for config defaults, skeleton extraction, chunking, JSON parsing, SQLite persistence, and CLI behavior.
9. Document the config and command in README.

## Open Questions

- Should generated summaries be searchable immediately via FTS, or only displayed in details/JSON?
- Should Holocron store the full prompt/model response for audit/debugging?
- Should user-edited titles be a separate Holocron concept from model-generated titles?
- Should provider-native write-back be a separate command such as `holocron write-back-title ID`?
