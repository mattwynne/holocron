# Picker Refresh and Snippets Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Keep picker results visible while searches refresh, and show more session-match context with highlighted snippets.

**Architecture:** Add a `Snippet` field to index search results populated by SQLite FTS `snippet(...)` for search queries. Update the picker view so loading only replaces the list during initial load; subsequent searches keep stale results visible with a refreshing status. Render snippets as an extra highlighted detail line under each result.

**Tech Stack:** Go, SQLite FTS5, Bubble Tea/Lipgloss TUI, `just test`, `just build`.

---

### Task 1: Preserve picker results while refreshing

- Add tests in `internal/tui/picker_test.go` proving stale results remain visible when `loading` is true.
- Change `Model.View()` to show `loading...` only when `loading && len(results) == 0`.
- Add a `refreshing...` status when `loading && len(results) > 0`.

### Task 2: Add FTS snippets to search results

- Add `Snippet string` to `index.SearchResult` and `storedRow`.
- Update `queryStoredRows` and `readRows`/`searchResult` to carry snippets.
- Update `Store.Search` and `Store.SearchInFolder` to select `snippet(sessions_fts, -1, char(0), char(1), '…', 12) AS snippet`.
- Add index tests for snippets and marker preservation.

### Task 3: Render snippets and highlight matches in picker

- Render `SearchResult.Snippet` under the row detail when present.
- Split marker-delimited snippets on `\x00` and `\x01`; render matched spans with a highlight style.
- Keep rows compact and truncate to terminal width.
- Add TUI rendering tests.

### Task 4: Validate

- Run `just test`.
- Run `just build`.
- Smoke test `./holocron pick --all holocron` and `./holocron search holocron --all --json`.
