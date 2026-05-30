package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattwynne/holocron/internal/index"
	"github.com/mattwynne/holocron/internal/session"
)

func TestSearchJSONCommand(t *testing.T) {
	home := t.TempDir()
	srcRoot := filepath.Join(home, ".codex", "sessions", "2026")
	if err := os.MkdirAll(srcRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(srcRoot, "codex.jsonl")
	writeFile(t, sessionFile, strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"abcdef1234567890","timestamp":"2026-05-13T10:00:00Z","cwd":` + quote(project) + `}}`,
		`{"role":"user","content":"Fix auth redirect handling"}`,
		`{"role":"assistant","content":"Updated the login callback."}`,
	}, "\n")+"\n")
	app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := app.fullScan(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOLOCRON_SKIP_SERVICE_CHECK", "1")
	var out bytes.Buffer
	app.Stdout = &out
	code := app.Run([]string{"search", "auth", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
	}
	for _, want := range []string{`"id": "cx:abcdef12"`, `"tool": "codex"`, `"title": "Fix auth redirect handling"`, `"folder": "project"`, `"resume_argv"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("json missing %s: %s", want, out.String())
		}
	}
}

func TestSearchTableShowsFolder(t *testing.T) {
	home := t.TempDir()
	srcRoot := filepath.Join(home, ".codex", "sessions", "2026")
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(srcRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(srcRoot, "codex.jsonl")
	writeFile(t, sessionFile, strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"abcdef1234567890","timestamp":"2026-05-13T10:00:00Z","cwd":` + quote(project) + `}}`,
		`{"role":"user","content":"Fix auth redirect handling"}`,
	}, "\n")+"\n")
	app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := app.fullScan(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOLOCRON_SKIP_SERVICE_CHECK", "1")
	var out bytes.Buffer
	app.Stdout = &out
	code := app.Run([]string{"search", "auth"})
	if code != 0 {
		t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
	}
	for _, want := range []string{"FOLDER", "project", "Fix auth redirect handling"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("table missing %q: %s", want, out.String())
		}
	}
}

func TestSearchTableSanitizesAndTruncatesTitles(t *testing.T) {
	var out bytes.Buffer
	printTable(&out, []index.SearchResult{{
		ID:        "pi:abc123",
		Tool:      "pi",
		UpdatedAt: "2026-05-29T10:00:00Z",
		Size:      "1 KB",
		Folder:    "project",
		Title:     "first line\nsecond\tline " + strings.Repeat("x", 100),
	}})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, output:\n%s", len(lines), out.String())
	}
	if strings.Contains(lines[1], "\t") || strings.Contains(lines[1], "\n") || !strings.Contains(lines[1], "first line second line") {
		t.Fatalf("title was not collapsed to one line: %q", lines[1])
	}
	if !strings.Contains(lines[1], "…") {
		t.Fatalf("title was not truncated: %q", lines[1])
	}
}

func TestTodayCommandListsSessionsUpdatedInLast24HoursAcrossAgents(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	codexRoot := filepath.Join(home, ".codex", "sessions", "2026")
	claudeRoot := filepath.Join(home, ".claude", "projects", "-tmp-project")
	for _, dir := range []string{project, codexRoot, claudeRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(codexRoot, "today.jsonl"), strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"codextoday123456","timestamp":"2026-05-27T10:00:00Z","cwd":` + quote(project) + `}}`,
		`{"role":"user","content":"Codex task today"}`,
	}, "\n")+"\n")
	writeFile(t, filepath.Join(claudeRoot, "today.jsonl"), strings.Join([]string{
		`{"sessionId":"claudetoday123456","cwd":` + quote(project) + `,"timestamp":"2026-05-27T12:00:00Z"}`,
		`{"role":"user","content":"Claude task today"}`,
	}, "\n")+"\n")
	writeFile(t, filepath.Join(codexRoot, "old.jsonl"), strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"codexold123456","timestamp":"2026-05-26T10:00:00Z","cwd":` + quote(project) + `}}`,
		`{"role":"user","content":"Codex task too old"}`,
	}, "\n")+"\n")

	app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Now: func() time.Time {
		return time.Date(2026, 5, 27, 15, 0, 0, 0, time.UTC)
	}}
	if err := app.fullScan(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOLOCRON_SKIP_SERVICE_CHECK", "1")
	var out bytes.Buffer
	app.Stdout = &out
	code := app.Run([]string{"today"})
	if code != 0 {
		t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
	}
	for _, want := range []string{"TOOL", "claude", "Claude task today", "codex", "Codex task today"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("today output missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "too old") {
		t.Fatalf("today output included session older than 24 hours: %s", out.String())
	}
}

func TestRegisterSessionIndexesProjectLocalPiSession(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(project, "data", "email-triage", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(sessionDir, "thread.jsonl")
	writeFile(t, sessionFile, strings.Join([]string{
		`{"type":"session","id":"pi-local-session-123456","cwd":` + quote(project) + `,"timestamp":"2026-05-16T10:00:00Z"}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"Draft KMC digital systems proposal for Mike"}]}}`,
	}, "\n")+"\n")

	app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if code := app.Run([]string{"register-session", "pi", sessionFile}); code != 0 {
		t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
	}

	t.Setenv("HOLOCRON_SKIP_SERVICE_CHECK", "1")
	var out bytes.Buffer
	app.Stdout = &out
	if code := app.Run([]string{"search", "KMC Mike", "--json"}); code != 0 {
		t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
	}
	for _, want := range []string{`"tool": "pi"`, `"folder": "project"`, sessionFile} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("json missing %s: %s", want, out.String())
		}
	}

	if err := app.fullScan(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := app.Run([]string{"search", "KMC Mike", "--json"}); code != 0 {
		t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
	}
	if !strings.Contains(out.String(), sessionFile) {
		t.Fatalf("registered session was lost after full scan: %s", out.String())
	}
}

func TestSummarizeDryRunUsesConfiguredModelCommand(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	srcRoot := filepath.Join(home, ".codex", "sessions")
	for _, dir := range []string{project, srcRoot, filepath.Join(home, ".config", "holocron")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(home, "model")
	writeFile(t, script, "#!/bin/sh\necho '{\"title\":\"Summarize sessions\",\"summary\":\"Generated a Holocron session summary.\",\"status\":\"implemented\",\"key_files\":[],\"confidence\":\"high\"}'\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".config", "holocron", "config.toml"), "[summarizer]\ncommand = ["+quote(script)+"]\n")
	sessionFile := filepath.Join(srcRoot, "codex.jsonl")
	writeFile(t, sessionFile, strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"sum123456789","timestamp":"2026-05-13T10:00:00Z","cwd":` + quote(project) + `}}`,
		`{"role":"user","content":"Summarize my sessions"}`,
	}, "\n")+"\n")
	app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Spinner: false}
	if err := app.fullScan(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app.Stdout = &out
	if code := app.Run([]string{"summarize", "cx:sum12345", "--dry-run", "--json"}); code != 0 {
		t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
	}
	if !strings.Contains(out.String(), `"title": "Summarize sessions"`) || !strings.Contains(out.String(), `"dry_run": true`) {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestSummarizeSkipsExistingGeneratedMetadataWithoutForce(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	srcRoot := filepath.Join(home, ".codex", "sessions")
	for _, dir := range []string{project, srcRoot, filepath.Join(home, ".config", "holocron")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(home, "model-was-called")
	script := filepath.Join(home, "model")
	writeFile(t, script, "#!/bin/sh\ntouch "+quote(marker)+"\necho '{\"title\":\"New title\",\"summary\":\"New summary.\",\"status\":\"implemented\",\"key_files\":[],\"confidence\":\"high\"}'\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".config", "holocron", "config.toml"), "[summarizer]\ncommand = ["+quote(script)+"]\n")
	sessionFile := filepath.Join(srcRoot, "codex.jsonl")
	writeFile(t, sessionFile, strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"skip123456789","timestamp":"2026-05-13T10:00:00Z","cwd":` + quote(project) + `}}`,
		`{"role":"user","content":"Already summarized"}`,
	}, "\n")+"\n")
	app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Spinner: false}
	if err := app.fullScan(); err != nil {
		t.Fatal(err)
	}
	if err := app.store().UpdateGenerated("cx:skip1234", "Existing title", "Existing summary.", "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app.Stdout = &out
	if code := app.Run([]string{"summarize", "cx:skip1234", "--json"}); code != 0 {
		t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("expected no work, got: %s", out.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("model command should not have been called, stat err=%v", err)
	}
}

func TestSummarizeTodayOnlyUsesLast24Hours(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	srcRoot := filepath.Join(home, ".codex", "sessions")
	for _, dir := range []string{project, srcRoot, filepath.Join(home, ".config", "holocron")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(home, "model")
	writeFile(t, script, "#!/bin/sh\necho '{\"title\":\"Today summary\",\"summary\":\"Summarized a recent session.\",\"status\":\"implemented\",\"key_files\":[],\"confidence\":\"high\"}'\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".config", "holocron", "config.toml"), "[summarizer]\ncommand = ["+quote(script)+"]\n")
	writeFile(t, filepath.Join(srcRoot, "today.jsonl"), strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"today123456789","timestamp":"2026-05-27T10:00:00Z","cwd":` + quote(project) + `}}`,
		`{"role":"user","content":"Summarize today"}`,
	}, "\n")+"\n")
	writeFile(t, filepath.Join(srcRoot, "old.jsonl"), strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"old1234567890","timestamp":"2026-05-26T10:00:00Z","cwd":` + quote(project) + `}}`,
		`{"role":"user","content":"Too old"}`,
	}, "\n")+"\n")
	app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Spinner: false, Now: func() time.Time {
		return time.Date(2026, 5, 27, 15, 0, 0, 0, time.UTC)
	}}
	if err := app.fullScan(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app.Stdout = &out
	if code := app.Run([]string{"summarize", "--today", "--dry-run", "--json"}); code != 0 {
		t.Fatalf("exit code = %d stderr=%s", code, app.Stderr)
	}
	if !strings.Contains(out.String(), `"id": "cx:today123"`) || strings.Contains(out.String(), "old123") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestOpenValidatesCWDAndExecsArgv(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	project := filepath.Join(home, "project")
	srcRoot := filepath.Join(home, ".codex", "sessions")
	for _, dir := range []string{binDir, project, srcRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fakeCodex := filepath.Join(binDir, "codex")
	writeFile(t, fakeCodex, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(fakeCodex, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	sessionFile := filepath.Join(srcRoot, "codex.jsonl")
	writeFile(t, sessionFile, `{"type":"session_meta","payload":{"id":"abcdef1234567890","timestamp":"2026-05-13T10:00:00Z","cwd":`+quote(project)+`}}`+"\n")
	app := App{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := app.fullScan(); err != nil {
		t.Fatal(err)
	}

	errExec := errors.New("exec intercepted")
	var gotPath string
	var gotArgv []string
	app.Exec = func(path string, argv []string, env []string) error {
		gotPath = path
		gotArgv = append([]string(nil), argv...)
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		wantCWD, err := filepath.EvalSymlinks(project)
		if err != nil {
			t.Fatal(err)
		}
		gotCWD, err := filepath.EvalSymlinks(cwd)
		if err != nil {
			t.Fatal(err)
		}
		if gotCWD != wantCWD {
			t.Fatalf("cwd = %q want %q", gotCWD, wantCWD)
		}
		return errExec
	}
	if err := app.open([]string{"cx:abcdef12"}); !errors.Is(err, errExec) {
		t.Fatalf("open err = %v", err)
	}
	if gotPath != fakeCodex {
		t.Fatalf("exec path = %q", gotPath)
	}
	if strings.Join(gotArgv, " ") != "codex resume abcdef1234567890" {
		t.Fatalf("argv = %#v", gotArgv)
	}
}

func TestDaemonRunningFastUsesLockPIDWithoutLaunchctl(t *testing.T) {
	home := t.TempDir()
	lockPath := filepath.Join(home, ".local", "share", "holocron", "daemon.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, lockPath, fmt.Sprintf("%d 2026-05-13T10:00:00Z\n", os.Getpid()))
	app := App{Home: home}
	if !app.daemonRunningFast() {
		t.Fatal("expected daemon lock for current pid to be running")
	}
	writeFile(t, lockPath, "99999999 2026-05-13T10:00:00Z\n")
	if app.daemonRunningFast() {
		t.Fatal("expected stale daemon lock to report not running")
	}
}

func TestAcquireLockRefusesActiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	writeFile(t, path, fmt.Sprintf("%d 2026-05-13T10:00:00Z\n", os.Getpid()))

	release, err := acquireLock(path)
	if err == nil {
		release()
		t.Fatal("expected active lock error")
	}
	if !strings.Contains(err.Error(), "daemon already running") {
		t.Fatalf("err = %v", err)
	}
}

func TestAcquireLockReplacesStaleLockAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	writeFile(t, path, "99999999 2026-05-13T10:00:00Z\n")

	release, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), fmt.Sprintf("%d ", os.Getpid())) {
		t.Fatalf("lock content = %q", string(b))
	}
}

func TestSourceWatcherNoticesNewSubdirectory(t *testing.T) {
	root := t.TempDir()
	w, err := newSourceWatcher([]session.Source{{Tool: session.ToolCodex, Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	subdir := filepath.Join(root, "nested")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	waitWatcherEvent(t, w.Events)
	if err := w.Refresh(); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	_, watched := w.fds[subdir]
	w.mu.Unlock()
	if !watched {
		t.Fatalf("new subdirectory was not watched")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitWatcherEvent(t *testing.T, events <-chan struct{}) {
	t.Helper()
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher event")
	}
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
