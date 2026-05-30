# Folder-Scoped Search Design

## Goal

Make Holocron search local by default: when run from a directory, `search` and `pick` should prefer sessions whose indexed working directory is that directory or a descendant. Add `--all` to keep the current cross-folder behaviour.

## User behaviour

- `holocron search Q` searches sessions whose `cwd` equals the process working directory or is below it.
- `holocron search Q --json` applies the same scope and prints scoped JSON results.
- `holocron search Q --all` searches every indexed session, matching today’s behaviour.
- `holocron pick [Q]` starts with recent/search results scoped to the current directory.
- `holocron pick --all [Q]` starts with global recent/search results.

## Architecture

Put folder filtering in `internal/index`, not in the CLI. This preserves ranking and limits inside the chosen scope. Add `SearchInFolder(query, folder, limit)` and `RecentInFolder(folder, limit)` next to the existing global methods. The SQL filter should match exact `cwd` or descendants with a path separator boundary.

Keep the TUI model mostly unchanged by passing it either the normal store or a small scoped store wrapper from the CLI. The wrapper implements the current TUI store interface and delegates to the scoped index methods.

## Components

- `internal/index/index.go`: add scoped search and recent queries plus a small helper for descendant path matching.
- `internal/cli/cli.go`: parse `--all` for `search` and `pick`; resolve `os.Getwd()` when not all; use scoped store methods or wrapper.
- `internal/tui/picker.go`: no behavioural change required if the CLI passes a scoped store.
- `README.md` and usage text: document default folder scope and `--all`.

## Error handling

If Holocron cannot determine the current directory, return a clear error such as `cannot determine current folder: ...`. Do not require a current directory when `--all` is present.

## Testing

Add tests for:

- scoped search includes exact folder matches;
- scoped search includes descendant `cwd` values;
- scoped search excludes siblings and prefix-only paths such as `/tmp/project-other`;
- scoped recent uses the same folder rules;
- `search --all` keeps global behaviour;
- `pick --all` parses as a flag instead of search text;
- usage and README examples reflect the new default.
