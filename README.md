# Holocron

Holocron is a local search index for AI coding-agent sessions.

It solves the "I know I talked about this with an agent, but where?" problem by indexing session files from Pi, Claude Code, Codex, and Gemini CLI, then letting you search and reopen the original conversation.

Holocron is local-first: it reads session files from disk and stores its index in SQLite at:

```text
~/.local/share/holocron/index.sqlite
```

## Install

Requires Go and macOS. The background indexer currently uses `launchd`.

```sh
git clone https://github.com/mattwynne/holocron.git
cd holocron
go install ./cmd/holocron
```

Start the background indexer:

```sh
holocron service install
```

This installs and starts a per-user launch agent. If `~/.pi` exists, Holocron also installs a Pi extension at:

```text
~/.pi/agent/extensions/holocron.ts
```

That extension registers project-local Pi sessions that are not stored under the normal `~/.pi/agent/sessions` tree.

## Use

Search sessions:

```sh
holocron search "auth redirect"
```

Search as JSON:

```sh
holocron search "auth redirect" --json
```

List sessions updated in the last 24 hours across all indexed agents:

```sh
holocron today
holocron today --json
```

Pick and reopen a session in the TUI:

```sh
holocron pick
holocron pick "auth redirect"
```

Open a result by ID:

```sh
holocron open pi:019e31be
```

Manually register a Pi session outside the default session tree:

```sh
holocron register-session pi /path/to/session.jsonl
```

Manage the service:

```sh
holocron service status
holocron service start
holocron service stop
holocron service uninstall
```

## Development

```sh
direnv allow
go test ./...
go run ./cmd/holocron help
```
