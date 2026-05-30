# Holocron iteration plans

| Iteration | Plan | Status | Summary |
|---|---|---|---|
| 002 | [iteration-002.md](iteration-002.md) | Done | Hardened the MVP: SQLite driver, watcher/debounce daemon, safer transcript extraction, atomic daemon lock, better errors/logging. |
| 003 | [iteration-003.md](iteration-003.md) | Done | Implemented the interactive `holocron pick` TUI for browsing/searching sessions and explicitly selecting one to reopen. |
| 004 | [iteration-004.md](iteration-004.md) | Done | Added generated titles/summaries, config-driven summarizer backend, `holocron summarize`, `--today`, progress, parallel jobs, preservation across rebuilds, and cleanup of summarizer backend sessions. |
| 005 | [iteration-005.md](iteration-005.md) | Planned | Store normalized transcript turns in SQLite and lazily export sessions into Pi-compatible format for `holocron view` and `holocron continue`. |
| 006 | [iteration-006.md](iteration-006.md) | In progress | Add `holocron ask` for single-turn questions over indexed transcripts using Pi prompt mode. |
| 007 | [2026-05-30-session-prompt-refactor.md](2026-05-30-session-prompt-refactor.md) | Done | Refactored `ask` and `summarize` around a shared session-prompt path, with ask cancellation and spinner feedback. |

## Status key

- Planned: designed but not started.
- In progress: implementation exists but design/behavior is still actively changing.
- Done: implemented and accepted for now.
- Superseded: replaced by a later plan.
