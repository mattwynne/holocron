# Single Line Search Results Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Keep every search/today/picker result row to one terminal line per rendered line by collapsing multiline titles and truncating long titles.

**Architecture:** Add small display sanitizers at the presentation boundary, not in the index. JSON output should keep full stored titles. The CLI tables will collapse whitespace and cap visible titles; the TUI row renderer will collapse whitespace before applying existing terminal-width truncation.

**Tech Stack:** Go, tabwriter, Bubble Tea/Lip Gloss, existing `just test` workflow.

---

### Task 1: CLI table title sanitization

**Files:**
- Modify: `internal/cli/cli.go`
- Test: `internal/cli/cli_test.go`

Steps:
1. Add a failing unit test for `printTable` proving a title containing newlines/tabs is rendered as one result line and long titles are ellipsized.
2. Add helper functions to collapse whitespace and truncate to a fixed visible width for table titles.
3. Use the helper in `printTable` and `printTodayTable`, leaving `--json` unchanged.
4. Run targeted CLI tests.

### Task 2: TUI row title sanitization

**Files:**
- Modify: `internal/tui/picker.go`
- Test: `internal/tui/picker_test.go`

Steps:
1. Add a failing unit test proving `renderRow` does not leak title newlines and truncates to model width.
2. Collapse whitespace before building row strings; keep existing width truncation.
3. Run targeted TUI tests.

### Task 3: Full verification

Run `just test` because the project has a `justfile`.
