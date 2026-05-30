# Session Prompt Refactor Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Refactor `ask` and `summarize` to share one session-prompt execution path, add cancellation for in-flight Pi calls, and show a spinner while `ask` is waiting for Pi.

**Architecture:** Introduce an internal `sessionprompt` package that owns loading session context, choosing normalized transcript or parsed transcript fallback, building prompts, applying token-budget truncation, and invoking `llm.Runner`. Keep command-specific behavior thin: `ask` supplies a free-form prompt and writes stdout; `summarize` supplies a strict-JSON prompt and parses the result. Use `signal.NotifyContext` at the CLI command boundary so Ctrl-C cancels the child Pi process through `exec.CommandContext`.

**Tech Stack:** Go 1.26, existing `internal/llm` runner, existing `internal/ask` prompt code, existing `internal/summarizer` JSON repair, existing CLI spinner helpers, SQLite-backed `index.Store`.

---

## Scope

In scope:

- Extract shared session context loading used by `ask` and `summarize`.
- Let summarizer reuse the same session context path as `ask`.
- Keep `ask` stdout as the raw model answer. No output styling.
- Add spinner on stderr while `ask` is waiting for Pi.
- Make Ctrl-C interrupt `ask` and stop the Pi subprocess.
- Preserve summarizer JSON repair behavior and background summarization behavior.

Out of scope:

- Streaming model output.
- Tool-enabled `ask`.
- Running `ask` from the original session cwd.
- Styling LLM output.
- Changing prompt semantics beyond the refactor needed to share code.

---

### Task 1: Add shared session context loader

**Files:**
- Create: `internal/sessionprompt/sessionprompt.go`
- Create: `internal/sessionprompt/sessionprompt_test.go`
- Read/reference: `internal/cli/cli.go:264-284`, `internal/ask/ask.go`, `internal/index/index.go:730-762`

**Step 1: Write the failing test**

Create `internal/sessionprompt/sessionprompt_test.go` with tests for the pure conversion functions first. Avoid database setup in this first task.

```go
package sessionprompt

import (
    "strings"
    "testing"
    "time"

    "github.com/mattwynne/holocron/internal/session"
    "github.com/mattwynne/holocron/internal/transcript"
)

func TestContextFromTranscriptRendersNormalizedTurns(t *testing.T) {
    tr := transcript.Transcript{
        HolocronID: "cx:abc12345",
        ProviderSessionID: "abc123456789",
        Tool: session.ToolCodex,
        CWD: "/tmp/project",
        UpdatedAt: time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC),
        Turns: []transcript.Turn{
            {Role: transcript.RoleUser, Text: "Fix auth"},
            {Role: transcript.RoleAssistant, Text: "Done"},
        },
    }

    ctx := ContextFromTranscript(tr, "Generated title")

    for _, want := range []string{"cx:abc12345", "abc123456789", "codex", "/tmp/project", "Generated title", "Fix auth", "Done"} {
        if !strings.Contains(ctx.TranscriptText+ctx.Title+ctx.HolocronID+ctx.ProviderSessionID+string(ctx.Tool)+ctx.CWD, want) {
            t.Fatalf("context missing %q: %#v", want, ctx)
        }
    }
}

func TestContextFromRecordUsesParsedTranscriptFallback(t *testing.T) {
    rec := session.Record{ID: "pi:abc12345", Tool: session.ToolPi, SessionID: "abc12345", Title: "Title", Transcript: "user: hello\nassistant: hi"}
    ctx := ContextFromRecord(rec)
    if ctx.TranscriptText != rec.Transcript || ctx.Title != "Title" || ctx.HolocronID != rec.ID {
        t.Fatalf("unexpected context: %#v", ctx)
    }
}
```

**Step 2: Run the test to verify it fails**

```sh
go test ./internal/sessionprompt
```

Expected: FAIL because the package does not exist.

**Step 3: Implement the shared context types**

Create `internal/sessionprompt/sessionprompt.go`:

```go
package sessionprompt

import (
    "fmt"
    "strings"
    "time"

    "github.com/mattwynne/holocron/internal/session"
    "github.com/mattwynne/holocron/internal/transcript"
)

type Context struct {
    HolocronID        string
    Tool              session.Tool
    ProviderSessionID string
    Title             string
    CWD               string
    CreatedAt         time.Time
    UpdatedAt         time.Time
    TranscriptText    string
}

func ContextFromTranscript(tr transcript.Transcript, title string) Context {
    return Context{HolocronID: tr.HolocronID, Tool: tr.Tool, ProviderSessionID: tr.ProviderSessionID, Title: title, CWD: tr.CWD, CreatedAt: tr.CreatedAt, UpdatedAt: tr.UpdatedAt, TranscriptText: RenderTranscript(tr)}
}

func ContextFromRecord(rec session.Record) Context {
    return Context{HolocronID: rec.ID, Tool: rec.Tool, ProviderSessionID: rec.SessionID, Title: rec.Title, CWD: rec.CWD, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, TranscriptText: rec.Transcript}
}

func RenderTranscript(tr transcript.Transcript) string {
    var b strings.Builder
    for _, turn := range tr.Turns {
        label := string(turn.Role)
        if turn.Role == transcript.RoleTool && turn.ToolName != "" {
            label = "tool:" + turn.ToolName
        }
        fmt.Fprintf(&b, "[%s]\n", label)
        if turn.Text != "" { b.WriteString(turn.Text); b.WriteByte('\n') }
        if turn.ToolInput != "" { b.WriteString("input: "); b.WriteString(turn.ToolInput); b.WriteByte('\n') }
        if turn.ToolOutput != "" { b.WriteString("output: "); b.WriteString(turn.ToolOutput); b.WriteByte('\n') }
        b.WriteByte('\n')
    }
    return strings.TrimSpace(b.String())
}
```

Do not add store loading yet. Keep this commit mechanical.

**Step 4: Run the test**

```sh
go test ./internal/sessionprompt
```

Expected: PASS.

**Step 5: Commit**

```sh
git add internal/sessionprompt
git commit -m "refactor: add shared session prompt context"
```

---

### Task 2: Move ask prompt building onto `sessionprompt`

**Files:**
- Modify: `internal/ask/ask.go`
- Modify: `internal/ask/ask_test.go`
- Modify: `internal/cli/cli.go`

**Step 1: Write/adjust failing tests**

Update `internal/ask/ask_test.go` to build prompts from `sessionprompt.Context` instead of `transcript.Transcript` directly. Keep one compatibility test only if desired.

Example assertion:

```go
ctx := sessionprompt.Context{HolocronID: "cx:abcdef12", Tool: session.ToolCodex, Title: "Auth fix", CWD: "/tmp/project", TranscriptText: "[user]\nFix auth\n\n[assistant]\nDone"}
prompt := BuildPrompt(ctx, "What changed?", 200000)
```

Expected: this fails until `ask.BuildPrompt` accepts the shared context type.

**Step 2: Run the failing test**

```sh
go test ./internal/ask
```

Expected: FAIL because the signature still uses ask-local context or transcript types.

**Step 3: Update `internal/ask`**

Refactor `internal/ask/ask.go`:

- Remove ask-local `Context`, `ContextFromTranscript`, `ContextFromRecord`, and `RenderTranscript`.
- Import `github.com/mattwynne/holocron/internal/sessionprompt`.
- Change the prompt API to:

```go
func BuildPrompt(ctx sessionprompt.Context, question string, maxInputTokens int) string
```

- Keep the existing prompt text and truncation behavior.
- Keep tests for start/end truncation.

**Step 4: Update CLI call sites**

In `internal/cli/cli.go`:

- Replace `askprompt.ContextFromTranscript` with `sessionprompt.ContextFromTranscript`.
- Replace `askprompt.ContextFromRecord` with `sessionprompt.ContextFromRecord`.
- Change `askprompt.BuildPromptFromContext(...)` to `askprompt.BuildPrompt(...)`.

**Step 5: Run focused tests**

```sh
go test ./internal/ask ./internal/cli
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/ask internal/cli internal/sessionprompt
git commit -m "refactor: build ask prompts from shared context"
```

---

### Task 3: Add store-backed session context loading

**Files:**
- Modify: `internal/sessionprompt/sessionprompt.go`
- Modify: `internal/sessionprompt/sessionprompt_test.go`
- Modify: `internal/cli/cli.go`

**Step 1: Write a failing loader test**

Add a test that uses a fake loader rather than a real SQLite store. Define a small interface in the package first through the test:

```go
type fakeStore struct {
    tr transcript.Transcript
    trErr error
    rec session.Record
}

func (f fakeStore) Transcript(id string) (transcript.Transcript, error) { return f.tr, f.trErr }
func (f fakeStore) Get(id string) (session.Record, error) { return f.rec, nil }
```

Test normalized-first behavior:

```go
func TestLoadContextPrefersNormalizedTranscript(t *testing.T) {
    store := fakeStore{tr: transcript.Transcript{HolocronID: "cx:abc12345", Tool: session.ToolCodex, Turns: []transcript.Turn{{Role: transcript.RoleUser, Text: "normalized"}}}}
    ctx, err := LoadContext(store, "cx:abc12345", "Title")
    if err != nil { t.Fatal(err) }
    if !strings.Contains(ctx.TranscriptText, "normalized") { t.Fatalf("ctx = %#v", ctx) }
}
```

Test fallback behavior when `Transcript` returns `os.ErrNotExist`:

```go
func TestLoadContextFallsBackToParsedRecord(t *testing.T) {
    store := fakeStore{trErr: os.ErrNotExist, rec: session.Record{ID: "cx:abc12345", Tool: session.ToolCodex, Transcript: "fallback transcript"}}
    ctx, err := LoadContext(store, "cx:abc12345", "")
    if err != nil { t.Fatal(err) }
    if ctx.TranscriptText != "fallback transcript" { t.Fatalf("ctx = %#v", ctx) }
}
```

**Step 2: Run the failing test**

```sh
go test ./internal/sessionprompt
```

Expected: FAIL because `LoadContext` does not exist.

**Step 3: Implement `LoadContext`**

Add interfaces and function:

```go
type Store interface {
    Transcript(id string) (transcript.Transcript, error)
    Get(id string) (session.Record, error)
}

func LoadContext(store Store, id, title string) (Context, error) {
    tr, err := store.Transcript(id)
    if err == nil { return ContextFromTranscript(tr, title), nil }
    if !errors.Is(err, os.ErrNotExist) { return Context{}, err }
    rec, err := store.Get(id)
    if err != nil { return Context{}, err }
    if rec.Transcript == "" && rec.SourcePath != "" {
        parsed, err := session.ParseFile(rec.Tool, filepath.Dir(rec.SourcePath), rec.SourcePath, session.ParseOptions{})
        if err != nil { return Context{}, err }
        parsed.ID = rec.ID
        if parsed.Title == "" { parsed.Title = title }
        rec = parsed
    }
    if rec.Title == "" { rec.Title = title }
    return ContextFromRecord(rec), nil
}
```

This deliberately moves the fallback currently in `App.askContext` into the shared package.

**Step 4: Replace CLI-local context loading**

In `internal/cli/cli.go`, remove `askContext`. In `ask`, call:

```go
ctx, err := sessionprompt.LoadContext(a.store(), id, displayTitle(row))
```

Remove imports that are now unused by `askContext`.

**Step 5: Run focused tests**

```sh
go test ./internal/sessionprompt ./internal/cli
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/sessionprompt internal/cli
git commit -m "refactor: share session context loading"
```

---

### Task 4: Let summarizer use shared session context

**Files:**
- Modify: `internal/summarizer/summarizer.go`
- Modify: `internal/summarizer/summarizer_test.go`
- Modify: `internal/cli/cli.go`

**Step 1: Write failing tests for context-based summarization**

Add a test in `internal/summarizer/summarizer_test.go`:

```go
func TestSummarizeContextUsesSharedTranscriptText(t *testing.T) {
    dir := t.TempDir()
    script := filepath.Join(dir, "model")
    promptPath := filepath.Join(dir, "prompt")
    if err := os.WriteFile(script, []byte("#!/bin/sh\ncat > "+quoteForShell(promptPath)+"\necho '{\"title\":\"Shared context\",\"summary\":\"Used shared context.\",\"status\":\"implemented\",\"key_files\":[],\"confidence\":\"high\"}'\n"), 0o755); err != nil { t.Fatal(err) }

    r := Runner{Config: DefaultConfig()}
    r.Config.LLM.PiExecutable = script
    ctx := sessionprompt.Context{HolocronID: "cx:abc12345", Tool: session.ToolCodex, TranscriptText: "shared transcript text"}
    got, err := r.SummarizeContext(context.Background(), ctx)
    if err != nil { t.Fatal(err) }
    if got.Title != "Shared context" { t.Fatalf("got %#v", got) }
    b, _ := os.ReadFile(promptPath)
    if !strings.Contains(string(b), "shared transcript text") { t.Fatalf("prompt = %s", string(b)) }
}
```

If there is no shell quote helper in this test file, add a small local helper.

**Step 2: Run the failing test**

```sh
go test ./internal/summarizer
```

Expected: FAIL because `SummarizeContext` does not exist.

**Step 3: Implement context-based summarization**

In `internal/summarizer/summarizer.go`:

- Import `internal/sessionprompt`.
- Add:

```go
func (r Runner) SummarizeContext(ctx context.Context, sp sessionprompt.Context) (Final, error)
```

- Build skeleton from `sessionprompt.Context`.
- Use `SelectedTranscriptExcerpt(sp.TranscriptText, cfg)`.
- Reuse `callJSON` unchanged.
- Keep the existing `Summarize(ctx, rec session.Record)` as a compatibility wrapper:

```go
func (r Runner) Summarize(ctx context.Context, rec session.Record) (Final, error) {
    return r.SummarizeContext(ctx, sessionprompt.ContextFromRecord(rec))
}
```

- Add `BuildContextSkeleton(sp sessionprompt.Context, cfg Config) string`.
- Keep `BuildSkeleton(rec, cfg)` as a wrapper if tests or call sites still use it.

**Step 4: Update CLI summarization path**

In `App.summarizeOne` in `internal/cli/cli.go`:

- Remove direct `a.store().Get` + `session.ParseFile` logic.
- Use:

```go
ctx, err := sessionprompt.LoadContext(a.store(), r.ID, displayTitle(r))
final, err := runner.SummarizeContext(ctxContext, ctx)
```

Be careful with variable names because both Go context and session prompt context may be named `ctx`.

**Step 5: Run focused tests**

```sh
go test ./internal/summarizer ./internal/cli
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/summarizer internal/cli
git commit -m "refactor: summarize shared session prompts"
```

---

### Task 5: Add Ctrl-C cancellation for ask

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/llm/llm_test.go` if needed

**Step 1: Add a test seam for ask context**

Add an optional field to `App`:

```go
Context func() context.Context
```

`New()` should set this through a helper that returns `context.Background()` or a signal context. For testability, `ask` should call a method:

```go
func (a App) commandContext() context.Context
```

Test can set `Context` to return an already-canceled context.

**Step 2: Write failing cancellation test**

In `internal/cli/cli_test.go`, add a test that uses `setupAskFixture`, then overwrites fake Pi with a script that sleeps. Use an already canceled context:

```go
func TestAskCancelsPiWhenContextCancelled(t *testing.T) {
    home, _, _ := setupAskFixture(t)
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    var out, errOut bytes.Buffer
    app := App{Home: home, Stdout: &out, Stderr: &errOut, Spinner: false, Context: func() context.Context { return ctx }}
    code := app.Run([]string{"ask", "cx:ask12345", "question"})
    if code == 0 { t.Fatal("expected cancellation failure") }
    if !strings.Contains(errOut.String(), "context canceled") && !strings.Contains(errOut.String(), "canceled") {
        t.Fatalf("stderr = %s", errOut.String())
    }
}
```

**Step 3: Run the failing test**

```sh
go test ./internal/cli -run TestAskCancelsPiWhenContextCancelled -count=1
```

Expected: FAIL because `ask` uses `context.Background()` directly.

**Step 4: Implement signal-aware context**

In `internal/cli/cli.go`:

- Add `Context func() context.Context` to `App`.
- Add helper:

```go
func (a App) commandContext() context.Context {
    if a.Context != nil { return a.Context() }
    ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)
    return ctx
}
```

Note: if you use `signal.NotifyContext`, retain and call the cancel function if possible to avoid leaking signal handlers. A better helper is:

```go
func (a App) commandContext() (context.Context, func())
```

Then `ask` can:

```go
ctx, cancel := a.commandContext()
defer cancel()
answer, err := llm.Runner{Config: cfg}.RunPrompt(ctx, prompt)
```

Keep background summarization using its existing daemon context, not the CLI signal helper.

**Step 5: Run the test**

```sh
go test ./internal/cli -run TestAskCancelsPiWhenContextCancelled -count=1
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/cli
git commit -m "fix: cancel ask on interrupt"
```

---

### Task 6: Show spinner while ask is thinking

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go` only if needed

**Step 1: Add a simple non-terminal-safe test if practical**

Because `startSpinner` only renders on terminal stderr, unit testing visual spinner output is low value. Instead, keep this covered by implementation and final manual smoke test.

If you want a test seam, add `StartSpinner func(label string) func()` to `App`; otherwise skip.

**Step 2: Implement spinner in `ask`**

In `App.ask`, wrap only the Pi call:

```go
stop := a.startSpinner("Thinking")
answer, err := llm.Runner{Config: cfg}.RunPrompt(ctx, prompt)
stop()
if err != nil { return err }
```

Use `defer stop()` to handle errors, but stop before writing stdout so spinner cleanup cannot interleave with the answer:

```go
stop := a.startSpinner("Thinking")
answer, err := runner.RunPrompt(ctx, prompt)
stop()
if err != nil { return err }
fmt.Fprint(a.Stdout, answer)
```

Do not show spinner during prompt input.

**Step 3: Run focused tests**

```sh
go test ./internal/cli
```

Expected: PASS.

**Step 4: Commit**

```sh
git add internal/cli
git commit -m "feat: show ask thinking spinner"
```

---

### Task 7: Clean up docs and plan status

**Files:**
- Modify: `README.md`
- Modify: `docs/plans/readme.md`
- Optional modify: `docs/spec.md`

**Step 1: Update README**

Add one sentence to the `ask` docs:

```md
Press Ctrl-C to stop an in-flight `ask` request.
```

Do not document styled output, because that requirement was dropped.

**Step 2: Update plan index**

Add this plan to `docs/plans/readme.md`:

```md
| 007 | [2026-05-30-session-prompt-refactor.md](2026-05-30-session-prompt-refactor.md) | Planned | Refactor `ask` and `summarize` around a shared session-prompt path, with ask cancellation and spinner feedback. |
```

If iteration numbering should stay numeric-only, rename this file before committing to `docs/plans/iteration-007.md` and update the row accordingly.

**Step 3: Run tests**

```sh
go test ./...
```

Expected: PASS.

**Step 4: Commit**

```sh
git add README.md docs/plans/readme.md docs/spec.md
git commit -m "docs: document ask cancellation"
```

---

### Task 8: Final verification

**Files:**
- No planned file changes unless fixes are needed.

**Step 1: Run the full suite**

```sh
just test
```

Expected: PASS.

**Step 2: Rebuild for demo/manual testing**

```sh
just build
```

Expected: `./holocron` rebuilds successfully and the daemon update hook completes.

**Step 3: Manual smoke test**

Run:

```sh
./holocron ask <known-id> "What was this session about?"
```

Expected:

- Spinner appears on stderr while Pi is running.
- Answer prints to stdout after spinner clears.
- Ctrl-C during thinking stops the command and the Pi subprocess.

**Step 4: Commit final fixes if any**

```sh
git status --short
git add <changed-files>
git commit -m "chore: finish session prompt refactor"
```

Only commit if final fixes were needed.

---

## Acceptance Criteria

- `ask` and `summarize` both use shared session context loading.
- Normalized transcript is preferred; parsed transcript fallback still works.
- Summarizer JSON repair and stderr capture still work.
- `holocron ask` displays a spinner while waiting for Pi.
- Ctrl-C cancels an in-flight `ask` and stops the Pi subprocess.
- `ask` stdout remains the raw model answer with no styling.
- `just test` passes.
- `just build` passes.
