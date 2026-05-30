# Holocron Ask Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `holocron ask ID [QUESTION...]`, a single-turn question command that uses an indexed session transcript as context, runs Pi in prompt mode, prints the answer, and leaves all sessions untouched.

**Architecture:** Introduce a small shared LLM/Pi runner config used by both `ask` and summarization. Build the `ask` prompt from normalized transcript turns stored in SQLite, using full context when it fits and start/end truncation when it does not. Add CLI question sources in priority order: command args, piped stdin, Bubble Tea multiline prompt.

**Tech Stack:** Go 1.26, SQLite store in `internal/index`, Pi prompt mode, Bubble Tea plus Bubbles textarea, existing `just test` workflow.

---

## Design Summary

### User-facing command

```sh
holocron ask pi:019e31be "What happened with the cache?"
echo "What files changed?" | holocron ask cx:abcdef12
holocron ask cd:abcdef12
```

Rules:

- Print only the model answer to stdout.
- Do not mutate original provider sessions.
- Do not create Pi continuation sessions.
- Do not enable tools in this iteration.
- Do not chdir into the original session cwd in this iteration.
- Do not add `--model`; Pi uses its configured default.

### Config

Use shared Pi/LLM config:

```toml
[pi]
executable = "/absolute/path/to/pi"

[llm]
timeout_seconds = 300
max_input_tokens = 200000
```

Rules:

- Missing `pi.executable` defaults to `exec.LookPath("pi")`, falling back to `"pi"`.
- Both `ask` and summarizer run:

  ```sh
  <pi.executable> -p --no-tools --no-session
  ```

- Both use `[llm].timeout_seconds` and `[llm].max_input_tokens`.
- Remove old execution settings: `[summarizer].command`, `[summarizer].timeout_seconds`, `[summarizer].max_input_tokens`.
- Keep `[summarizer.selection]` unchanged.

---

### Task 1: Add shared LLM config and runner

**Files:**
- Create: `internal/llm/llm.go`
- Create: `internal/llm/llm_test.go`
- Modify later: `internal/summarizer/summarizer.go`

**Step 1: Write failing config tests**

Create `internal/llm/llm_test.go` with tests like:

```go
package llm

import (
    "context"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func TestLoadConfigReadsPiAndLLMSections(t *testing.T) {
    home := t.TempDir()
    cfgDir := filepath.Join(home, ".config", "holocron")
    if err := os.MkdirAll(cfgDir, 0o755); err != nil { t.Fatal(err) }
    path := filepath.Join(cfgDir, "config.toml")
    if err := os.WriteFile(path, []byte(`[pi]
executable = "/tmp/custom-pi"

[llm]
timeout_seconds = 42
max_input_tokens = 12345
`), 0o644); err != nil { t.Fatal(err) }

    cfg, err := LoadConfig(home)
    if err != nil { t.Fatal(err) }
    if cfg.PiExecutable != "/tmp/custom-pi" { t.Fatalf("PiExecutable = %q", cfg.PiExecutable) }
    if cfg.Timeout != 42*time.Second { t.Fatalf("Timeout = %v", cfg.Timeout) }
    if cfg.MaxInputTokens != 12345 { t.Fatalf("MaxInputTokens = %d", cfg.MaxInputTokens) }
}

func TestDefaultConfigUsesLookPathOrPiFallback(t *testing.T) {
    cfg, err := LoadConfig(t.TempDir())
    if err != nil { t.Fatal(err) }
    if strings.TrimSpace(cfg.PiExecutable) == "" { t.Fatal("PiExecutable is empty") }
    if cfg.Timeout != 300*time.Second { t.Fatalf("Timeout = %v", cfg.Timeout) }
    if cfg.MaxInputTokens != 200000 { t.Fatalf("MaxInputTokens = %d", cfg.MaxInputTokens) }
}
```

Add a runner test with a fake script:

```go
func TestRunnerInvokesPiPromptMode(t *testing.T) {
    dir := t.TempDir()
    script := filepath.Join(dir, "pi")
    log := filepath.Join(dir, "log")
    if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\n' \"$@\" > \""+log+"\"\ncat >/dev/null\nprintf answer\n"), 0o755); err != nil { t.Fatal(err) }

    r := Runner{Config: Config{PiExecutable: script, Timeout: time.Second, MaxInputTokens: 1000}}
    out, err := r.RunPrompt(context.Background(), "prompt text")
    if err != nil { t.Fatal(err) }
    if out != "answer" { t.Fatalf("out = %q", out) }
    b, err := os.ReadFile(log)
    if err != nil { t.Fatal(err) }
    if got := strings.TrimSpace(string(b)); got != "-p --no-tools --no-session" { t.Fatalf("argv = %q", got) }
}
```

**Step 2: Run tests to verify they fail**

Run:

```sh
go test ./internal/llm
```

Expected: FAIL because `internal/llm` does not exist.

**Step 3: Implement `internal/llm`**

Create `internal/llm/llm.go`:

```go
package llm

import (
    "bytes"
    "context"
    "errors"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "strings"
    "time"
)

type Config struct {
    PiExecutable  string
    Timeout       time.Duration
    MaxInputTokens int
}

type Runner struct { Config Config }

func DefaultConfig() Config {
    pi, err := exec.LookPath("pi")
    if err != nil { pi = "pi" }
    return Config{PiExecutable: pi, Timeout: 300 * time.Second, MaxInputTokens: 200000}
}

func LoadConfig(home string) (Config, error) {
    cfg := DefaultConfig()
    path := filepath.Join(home, ".config", "holocron", "config.toml")
    b, err := os.ReadFile(path)
    if errors.Is(err, os.ErrNotExist) { return cfg, nil }
    if err != nil { return cfg, err }
    section := ""
    for _, raw := range strings.Split(string(b), "\n") {
        line := strings.TrimSpace(strings.Split(raw, "#")[0])
        if line == "" { continue }
        if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
            section = strings.Trim(line, "[]")
            continue
        }
        parts := strings.SplitN(line, "=", 2)
        if len(parts) != 2 { continue }
        key := strings.TrimSpace(parts[0])
        val := strings.Trim(strings.TrimSpace(parts[1]), `"`)
        switch section + "." + key {
        case "pi.executable":
            if val != "" { cfg.PiExecutable = val }
        case "llm.timeout_seconds":
            if n, err := strconv.Atoi(val); err == nil { cfg.Timeout = time.Duration(n) * time.Second }
        case "llm.max_input_tokens":
            if n, err := strconv.Atoi(val); err == nil { cfg.MaxInputTokens = n }
        }
    }
    return cfg, nil
}

func (c Config) PromptCommand() []string {
    return []string{c.PiExecutable, "-p", "--no-tools", "--no-session"}
}

func (r Runner) RunPrompt(ctx context.Context, prompt string) (string, error) {
    cfg := r.Config
    if cfg.Timeout <= 0 { cfg.Timeout = DefaultConfig().Timeout }
    ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
    defer cancel()
    argv := cfg.PromptCommand()
    if len(argv) == 0 || argv[0] == "" { return "", errors.New("pi executable is empty") }
    cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
    cmd.Stdin = strings.NewReader(prompt)
    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr
    err := cmd.Run()
    if ctx.Err() == context.DeadlineExceeded { return "", fmt.Errorf("pi prompt timed out after %s", cfg.Timeout) }
    if err != nil { return "", fmt.Errorf("pi prompt failed: %w: %s", err, strings.TrimSpace(stderr.String())) }
    return stdout.String(), nil
}
```

Keep helper parsing intentionally minimal; this project already uses a minimal TOML parser in `internal/summarizer`.

**Step 4: Run tests**

```sh
go test ./internal/llm
```

Expected: PASS.

**Step 5: Commit**

```sh
git add internal/llm
git commit -m "feat: add shared llm runner"
```

---

### Task 2: Move summarizer execution onto shared LLM config

**Files:**
- Modify: `internal/summarizer/summarizer.go`
- Modify: `internal/summarizer/summarizer_test.go`
- Modify: `internal/cli/cli.go`

**Step 1: Write failing summarizer config tests**

Update `internal/summarizer/summarizer_test.go` so config tests use `[pi]` and `[llm]`, not `[summarizer].command`, `[summarizer].timeout_seconds`, or `[summarizer].max_input_tokens`.

Add a test proving old execution settings are ignored:

```go
func TestLoadConfigIgnoresOldSummarizerExecutionSettings(t *testing.T) {
    home := t.TempDir()
    cfgDir := filepath.Join(home, ".config", "holocron")
    if err := os.MkdirAll(cfgDir, 0o755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`[pi]
executable = "/tmp/pi"

[llm]
timeout_seconds = 77
max_input_tokens = 888

[summarizer]
command = ["/tmp/old"]
timeout_seconds = 1
max_input_tokens = 2

[summarizer.selection]
include_first_turns = 9
`), 0o644); err != nil { t.Fatal(err) }

    cfg, err := LoadConfig(home)
    if err != nil { t.Fatal(err) }
    if cfg.LLM.PiExecutable != "/tmp/pi" { t.Fatalf("pi = %q", cfg.LLM.PiExecutable) }
    if cfg.LLM.Timeout != 77*time.Second { t.Fatalf("timeout = %v", cfg.LLM.Timeout) }
    if cfg.LLM.MaxInputTokens != 888 { t.Fatalf("tokens = %d", cfg.LLM.MaxInputTokens) }
    if cfg.Selection.IncludeFirstTurns != 9 { t.Fatalf("selection not preserved") }
}
```

**Step 2: Run tests to verify failure**

```sh
go test ./internal/summarizer
```

Expected: FAIL because `Config.LLM` does not exist and old config is still honored.

**Step 3: Update summarizer config and runner**

In `internal/summarizer/summarizer.go`:

- Import `github.com/mattwynne/holocron/internal/llm`.
- Replace `Command []string`, `MaxInputTokens int`, and `Timeout time.Duration` with `LLM llm.Config`.
- Keep `ChunkTokens`, `MaxChunks`, `Background`, `BackgroundInterval`, `BackgroundJobs`, and `Selection`.
- In `DefaultConfig`, set `LLM: llm.DefaultConfig()`.
- In `LoadConfig`, first call `llm.LoadConfig(home)` and assign the result to `cfg.LLM`.
- Remove parsing cases for `summarizer.command`, `summarizer.max_input_tokens`, and `summarizer.timeout_seconds`.
- Keep parsing `[summarizer.selection]`.
- Keep background parsing for now unless a test says otherwise.
- Change `SelectedTranscriptExcerpt` to use `cfg.LLM.MaxInputTokens`.
- Change model invocation to use `llm.Runner{Config: cfg.LLM}.RunPrompt(ctx, prompt)`.
- Remove `EnsureEphemeralPiCommand` and `runCommand` if no longer used.

In `internal/cli/cli.go`, update summary metadata:

```go
generatedBy := strings.Join(cfg.LLM.PromptCommand(), " ")
```

Use that instead of `strings.Join(cfg.Command, " ")`.

**Step 4: Run focused tests**

```sh
go test ./internal/summarizer ./internal/cli
```

Expected: PASS after updating fixtures that wrote `[summarizer] command = ...` to write `[pi] executable = ...` instead.

**Step 5: Commit**

```sh
git add internal/summarizer internal/cli
git commit -m "refactor: share llm config with summarizer"
```

---

### Task 3: Add ask prompt builder

**Files:**
- Create: `internal/ask/ask.go`
- Create: `internal/ask/ask_test.go`

**Step 1: Write failing prompt tests**

Create `internal/ask/ask_test.go`:

```go
package ask

import (
    "strings"
    "testing"
    "time"

    "github.com/mattwynne/holocron/internal/session"
    "github.com/mattwynne/holocron/internal/transcript"
)

func TestBuildPromptIncludesMetadataTranscriptAndQuestion(t *testing.T) {
    tr := transcript.Transcript{
        HolocronID: "cx:abcdef12",
        ProviderSessionID: "abcdef123456",
        Tool: session.ToolCodex,
        CWD: "/tmp/project",
        CreatedAt: time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC),
        UpdatedAt: time.Date(2026, 5, 30, 11, 0, 0, 0, time.UTC),
        Turns: []transcript.Turn{
            {Role: transcript.RoleUser, Text: "Fix auth"},
            {Role: transcript.RoleAssistant, Text: "Changed redirect handling"},
        },
    }
    prompt := BuildPrompt(tr, "What changed?", 200000)
    for _, want := range []string{"cx:abcdef12", "codex", "/tmp/project", "Fix auth", "Changed redirect handling", "What changed?"} {
        if !strings.Contains(prompt, want) { t.Fatalf("prompt missing %q:\n%s", want, prompt) }
    }
}

func TestBuildPromptTruncatesTranscriptStartAndEnd(t *testing.T) {
    tr := transcript.Transcript{HolocronID: "pi:long", Tool: session.ToolPi}
    tr.Turns = []transcript.Turn{
        {Role: transcript.RoleUser, Text: strings.Repeat("start ", 200)},
        {Role: transcript.RoleAssistant, Text: strings.Repeat("middle ", 200)},
        {Role: transcript.RoleUser, Text: strings.Repeat("end ", 200)},
    }
    prompt := BuildPrompt(tr, "Question?", 200)
    if !strings.Contains(prompt, "start") { t.Fatal("missing start") }
    if !strings.Contains(prompt, "end") { t.Fatal("missing end") }
    if !strings.Contains(prompt, "transcript truncated by holocron") { t.Fatal("missing truncation marker") }
}
```

**Step 2: Run tests to verify failure**

```sh
go test ./internal/ask
```

Expected: FAIL because package does not exist.

**Step 3: Implement prompt builder**

Create `internal/ask/ask.go` with:

- `BuildPrompt(tr transcript.Transcript, question string, maxInputTokens int) string`
- `RenderTranscript(tr transcript.Transcript) string`
- `truncateMiddle(text string, maxChars int) string`

Implementation rules:

```go
const charsPerToken = 4
const truncationMarker = "\n... transcript truncated by holocron ...\n"
```

Render turns as simple labeled text:

```text
[user]
...

[assistant]
...

[tool:Bash]
input: ...
output: ...
```

Build the prompt with sections:

```text
You are answering a question about an old AI coding-agent session.
Use only the session transcript below. If the transcript does not contain enough information, say so.

Session metadata:
holocron_id: ...
tool: ...
provider_session_id: ...
cwd: ...
created_at: ...
updated_at: ...

Transcript:
...

Question:
...

Answer:
```

Estimate prompt budget as `maxInputTokens * charsPerToken`. Reserve room for instruction, metadata, and question before truncating transcript.

**Step 4: Run tests**

```sh
go test ./internal/ask
```

Expected: PASS.

**Step 5: Commit**

```sh
git add internal/ask
git commit -m "feat: build ask prompts"
```

---

### Task 4: Add interactive ask prompt UI

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/tui/ask.go`
- Create: `internal/tui/ask_test.go`

**Step 1: Add dependency**

Run:

```sh
go get github.com/charmbracelet/bubbles@latest
```

If this upgrades Bubble Tea unexpectedly, pin to a compatible Bubbles version and keep the diff small.

**Step 2: Write a pure model test**

Create `internal/tui/ask_test.go` with a small test for model submission behavior. Keep it simple: instantiate the model, set textarea text, send key messages for submit, and assert the returned question.

If Bubble Tea internals make this awkward, test a small exported helper instead:

```go
func TestAskPromptResultTrimsWhitespace(t *testing.T) {
    if got := normalizeAskQuestion("\n hello \n"); got != "hello" {
        t.Fatalf("got %q", got)
    }
}
```

**Step 3: Implement `PromptAskQuestion`**

Create `internal/tui/ask.go`:

```go
func PromptAskQuestion(stdout, stderr io.Writer, id, title string) (string, error)
```

Use Bubbles textarea. Display:

```text
Ask <id> — <title>
Ctrl+D or Ctrl+S to submit, Esc to cancel
```

Accept a simple submit key if easier, for example `ctrl+s`. Return a trimmed question. Return empty string and nil on cancel.

**Step 4: Run tests**

```sh
go test ./internal/tui
```

Expected: PASS.

**Step 5: Commit**

```sh
git add go.mod go.sum internal/tui
git commit -m "feat: add ask prompt ui"
```

---

### Task 5: Wire `holocron ask` into the CLI

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`

**Step 1: Write failing CLI test for argument mode**

In `internal/cli/cli_test.go`, add a test that:

1. Creates a temp home and fixture session file.
2. Rebuilds the store with the parsed fixture record.
3. Writes a fake Pi executable that records stdin and argv and prints `fake answer`.
4. Writes config:

   ```toml
   [pi]
   executable = "/path/to/fake/pi"
   ```

5. Runs:

   ```go
   app := cli.App{Home: home, Stdout: &out, Stderr: &errOut, Now: fixedNow, Spinner: false}
   code := app.Run([]string{"ask", "cx:abcdef12", "What", "changed?"})
   ```

6. Asserts exit code `0`, stdout exactly `fake answer`, fake argv `-p --no-tools --no-session`, and fake stdin contains transcript plus `What changed?`.

**Step 2: Add stdin mode test**

Use an `App` test seam if needed. If `App` does not currently expose stdin, add:

```go
type App struct {
    Stdout io.Writer
    Stderr io.Writer
    Stdin  io.Reader
    // ...
}
```

Default `New()` sets `Stdin: os.Stdin`. The test sets `Stdin: strings.NewReader("stdin question")` and marks it as piped using a helper seam if necessary.

If terminal detection is hard to fake, isolate question selection in:

```go
func (a App) askQuestionFrom(args []string, row index.SearchResult) (string, error)
```

and unit-test the non-interactive branches directly.

**Step 3: Add command routing**

In `App.Run`, add:

```go
case "ask":
    err = a.ask(args[1:])
```

In `Usage`, add:

```text
holocron ask ID [QUESTION]        Ask one question about an indexed session
```

**Step 4: Implement `App.ask`**

Implementation outline:

```go
func (a App) ask(args []string) error {
    if len(args) < 1 || args[0] == "" { return errors.New("ask requires a session id") }
    id := args[0]
    row, err := a.store().Lookup(id)
    if err != nil { return err }
    question, err := a.readAskQuestion(row, args[1:])
    if err != nil { return err }
    question = strings.TrimSpace(question)
    if question == "" { return errors.New("ask requires a non-empty question") }

    tr, err := a.store().Transcript(id)
    if err != nil { return err }
    cfg, err := llm.LoadConfig(a.Home)
    if err != nil { return err }
    prompt := ask.BuildPrompt(tr, question, cfg.MaxInputTokens)
    answer, err := llm.Runner{Config: cfg}.RunPrompt(context.Background(), prompt)
    if err != nil { return err }
    fmt.Fprint(a.Stdout, answer)
    return nil
}
```

Question source order:

1. `strings.Join(args[1:], " ")`
2. piped `a.Stdin`
3. `tui.PromptAskQuestion(a.Stdout, a.Stderr, row.ID, displayTitle(row))`

For terminal detection, use `github.com/mattn/go-isatty`, already in `go.mod` indirectly, or a small app seam:

```go
IsTerminal func() bool
```

Set `New().IsTerminal` from `os.Stdin.Stat()`/isatty. Tests can set it to false.

**Step 5: Run focused CLI tests**

```sh
go test ./internal/cli -run 'Ask|Summarize'
```

Expected: PASS.

**Step 6: Commit**

```sh
git add internal/cli
git commit -m "feat: add holocron ask command"
```

---

### Task 6: Add integration-style ask test

**Files:**
- Modify: `internal/cli/cli_test.go` or create `internal/cli/ask_integration_test.go`

**Step 1: Write full-path test**

Create `internal/cli/ask_integration_test.go` with a test named:

```go
func TestAskIntegrationUsesIndexedNormalizedTranscript(t *testing.T)
```

The test should:

1. Create temp home.
2. Create a Codex JSONL fixture under `home/.codex/sessions/2026/session.jsonl` with user and assistant messages.
3. Parse it using existing session parsing helpers.
4. Rebuild the store.
5. Configure fake Pi.
6. Run `holocron ask <assigned-id> "What did we decide?"` through `App.Run`.
7. Assert fake Pi stdin contains the normalized transcript text, not raw JSONL.
8. Assert stdout is only the fake answer.

**Step 2: Run the integration test**

```sh
go test ./internal/cli -run TestAskIntegrationUsesIndexedNormalizedTranscript -count=1
```

Expected: PASS.

**Step 3: Commit**

```sh
git add internal/cli/ask_integration_test.go internal/cli/cli_test.go
git commit -m "test: cover ask integration path"
```

---

### Task 7: Update docs

**Files:**
- Modify: `README.md`
- Modify: `docs/spec.md` if command list needs to stay current
- Modify: `docs/plans/readme.md`

**Step 1: Update README usage**

Add under Use:

```md
Ask one question about an indexed session without modifying it:

```sh
holocron ask pi:019e31be "What did we decide?"
echo "What files changed?" | holocron ask cx:abcdef12
```
```

Update config docs from `[summarizer] command = ...` to:

```toml
[pi]
executable = "/absolute/path/to/pi"

[llm]
timeout_seconds = 300
max_input_tokens = 200000
```

Keep `[summarizer.selection]` docs if present.

**Step 2: Update plans readme**

Add iteration 006 row:

```md
| 006 | [iteration-006.md](iteration-006.md) | Planned | Add `holocron ask` for single-turn questions over indexed transcripts using Pi prompt mode. |
```

**Step 3: Run docs-adjacent tests**

```sh
go test ./...
```

Expected: PASS.

**Step 4: Commit**

```sh
git add README.md docs/spec.md docs/plans/readme.md
git commit -m "docs: document holocron ask"
```

---

### Task 8: Final verification

**Files:**
- No new files expected unless fixes are needed.

**Step 1: Run full test suite**

```sh
just test
```

Expected: `go test ./...` passes.

**Step 2: Build binary for manual test**

```sh
just build
```

Expected: `./holocron` is rebuilt.

**Step 3: Manual smoke test**

Run against a real indexed session:

```sh
./holocron ask <known-id> "What was this session about?"
```

Expected:

- Answer prints to stdout.
- No provider session file changes.
- If interactive mode is tested, Bubble Tea prompt opens and submits a multiline question.

**Step 4: Commit final fixes**

```sh
git status --short
git add <changed-files>
git commit -m "chore: finish ask verification"
```

Only commit if there are final changes.

---

## Acceptance Criteria

- `holocron ask ID "question"` answers using the indexed normalized transcript.
- Piped stdin question mode works.
- Interactive Bubble Tea prompt mode works.
- Stdout contains only the answer in non-interactive modes.
- Original session files are untouched.
- Pi runs as `<pi.executable> -p --no-tools --no-session`.
- Summarizer uses shared `[pi]` and `[llm]` execution config.
- `[summarizer.selection]` still works.
- README documents the command and new config.
- `just test` and `just build` pass.
