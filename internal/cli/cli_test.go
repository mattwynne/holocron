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
	for _, want := range []string{`"id": "codex:abcdef12"`, `"tool": "codex"`, `"title": "Fix auth redirect handling"`, `"resume_argv"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("json missing %s: %s", want, out.String())
		}
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
	if err := app.open([]string{"codex:abcdef12"}); !errors.Is(err, errExec) {
		t.Fatalf("open err = %v", err)
	}
	if gotPath != fakeCodex {
		t.Fatalf("exec path = %q", gotPath)
	}
	if strings.Join(gotArgv, " ") != "codex resume abcdef1234567890" {
		t.Fatalf("argv = %#v", gotArgv)
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
