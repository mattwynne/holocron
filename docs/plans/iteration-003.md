# Iteration 003: interactive picker TUI

## Context

Holocron now has a working service, index, search, JSON output, and open command. `holocron pick` is still a placeholder that opens the top-ranked result immediately, which is unsafe and does not satisfy the product goal of choosing between multiple matches.

## Goal

Implement a real interactive TUI for:

```sh
holocron pick [query terms]
```

The TUI should let Matt browse/refine search results and explicitly choose a session to reopen.

## User experience

### Startup

- `holocron pick` opens with recent sessions, ordered by `updated_at DESC`, limit 100.
- `holocron pick foo bar` opens with query prefilled as `foo bar` and shows search results.
- If the background service is not running, fail clearly before opening the TUI.

### Layout

Show:

- query input at the top;
- result list below;
- help/status line with keybindings;
- selected row highlighted.

Each row should show enough to choose confidently:

- title;
- tool;
- cwd/project path;
- last modified date/time;
- relative scale, e.g. file size and message count.

Long title/path values should truncate gracefully to terminal width.

### Interaction

Minimum keybindings:

- type text: edit/refine query live;
- backspace/delete: edit query;
- up/down or ctrl-p/ctrl-n: move selection;
- enter: select session and return its ID to caller;
- esc or ctrl-c: quit without opening;
- ctrl-u: clear query;
- optional: page up/down.

Search behaviour:

- Empty query shows recent sessions.
- Non-empty query searches FTS.
- Debounce live search, e.g. 150-300ms, so every keystroke does not hammer SQLite.
- Show loading/error/no-results states.

### Open behaviour

After the TUI exits with a selected ID, `holocron pick` should call the same open/exec path as `holocron open <id>`.

Do not exec from inside the Bubble Tea update loop; return the selected ID to the CLI layer, restore terminal state, then exec.

## Implementation guidance

Preferred libraries:

- `github.com/charmbracelet/bubbletea`
- `github.com/charmbracelet/lipgloss`

Use custom rendering rather than a heavy list component if simpler.

Suggested structure:

- `internal/tui` package for the picker model and rendering.
- CLI `pick` command calls something like:

```go
id, ok, err := tui.Pick(ctx, store, initialQuery, stdout, stderr)
if err != nil { return err }
if !ok { return nil }
return a.execResult(id)
```

Testing:

- Unit-test pure model update behaviour where practical:
  - initial query;
  - typing changes query;
  - selection movement bounds;
  - enter returns selected ID;
  - escape cancels.
- Keep integration tests light; TUI rendering does not need exhaustive snapshot tests.

## Acceptance checks

- `holocron pick` no longer auto-opens the top result.
- `holocron pick holocron` opens a TUI with matching results.
- Pressing enter opens only the explicitly selected session.
- Pressing esc/ctrl-c exits without opening.
- `go test ./...` passes.
- `go build -o holocron ./cmd/holocron` succeeds.

## Non-goals

- Perfect fuzzy ranking beyond existing FTS search.
- Mouse support.
- Multi-select.
- Preview pane.
- Configurable themes.
