# Holocron

A deep-memory index for your AI coding agents.

Holocron indexes local session files from Pi, Codex, Claude Code, and Gemini CLI so you can find and reopen old conversations.

## Platform support

MVP is macOS-only. Background indexing is managed by a per-user `launchd` service.

## Development

```sh
direnv allow
go test ./...
go run ./cmd/holocron help
```
