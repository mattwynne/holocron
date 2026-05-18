package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/mattwynne/holocron/internal/index"
	"github.com/mattwynne/holocron/internal/session"
	"github.com/mattwynne/holocron/internal/tui"
)

const serviceLabel = "net.mattwynne.holocron"

type App struct {
	Stdout io.Writer
	Stderr io.Writer
	Home   string
	Exec   func(string, []string, []string) error
}

func New() App {
	home, _ := os.UserHomeDir()
	return App{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Home:   home,
		Exec:   syscall.Exec,
	}
}

func (a App) Run(args []string) int {
	if len(args) == 0 {
		a.Usage()
		return 2
	}
	var err error
	switch args[0] {
	case "help", "--help", "-h":
		a.Usage()
		return 0
	case "version":
		fmt.Fprintln(a.Stdout, Version)
		return 0
	case "search":
		err = a.search(args[1:])
	case "pick":
		err = a.pick(args[1:])
	case "open":
		err = a.open(args[1:])
	case "service":
		err = a.service(args[1:])
	case "daemon":
		err = a.daemon()
	case "register-session":
		err = a.registerSession(args[1:])
	default:
		a.Usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(a.Stderr, "holocron:", err)
		return 1
	}
	return 0
}

var Version = "dev"

func (a App) Usage() {
	fmt.Fprintln(a.Stdout, `holocron - search and reopen your AI coding agent sessions

Usage:
  holocron search Q [--json]        Search indexed sessions
  holocron pick [Q]                 Pick and reopen a session in the TUI
  holocron open ID                  Reopen an indexed session
  holocron service COMMAND          Manage the background indexer
  holocron version                  Print version

Service commands:
  install, uninstall, start, stop, status`)
}

func (a App) store() index.Store {
	return index.Store{Path: index.DefaultPath(a.Home)}
}

func (a App) search(args []string) error {
	query, jsonOut, err := parseSearchArgs(args)
	if err != nil {
		return err
	}
	if query == "" {
		return errors.New("search requires a non-empty query")
	}
	if err := a.requireService(); err != nil {
		return err
	}
	results, err := a.store().Search(query, 100)
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	printTable(a.Stdout, results)
	return nil
}

func (a App) pick(args []string) error {
	if err := a.requireService(); err != nil {
		return err
	}
	result, err := tui.Pick(a.store(), strings.Join(args, " "), a.Stdout, a.Stderr)
	if err != nil {
		return err
	}
	if !result.OK {
		return nil
	}
	return a.execResult(result.ID)
}

func (a App) open(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("open requires exactly one session id")
	}
	return a.execResult(args[0])
}

func (a App) execResult(id string) error {
	rec, err := a.store().Get(id)
	if err != nil {
		if index.IsMissing(err) {
			return fmt.Errorf("session %q not found", id)
		}
		return err
	}
	if _, err := os.Stat(rec.SourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if deleteErr := a.store().Delete(rec.ID); deleteErr != nil {
				return fmt.Errorf("source session file is missing: %s; additionally failed to remove stale index entry: %w", rec.SourcePath, deleteErr)
			}
			return fmt.Errorf("source session file is missing: %s", rec.SourcePath)
		}
		return err
	}
	if rec.CWD == "" {
		return fmt.Errorf("indexed cwd is empty for %s", rec.ID)
	}
	if st, err := os.Stat(rec.CWD); err != nil || !st.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return fmt.Errorf("indexed cwd does not exist: %s: %w", rec.CWD, err)
	}
	argv := rec.ResumeArgv
	if len(argv) == 0 {
		argv = session.ResumeArgv(rec)
	}
	if len(argv) == 0 {
		return fmt.Errorf("no resume argv for %s", rec.ID)
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	if err := os.Chdir(rec.CWD); err != nil {
		return err
	}
	return a.Exec(bin, argv, os.Environ())
}

func parseSearchArgs(args []string) (string, bool, error) {
	var jsonOut bool
	var terms []string
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unknown search flag %s", arg)
			}
			terms = append(terms, arg)
		}
	}
	return strings.Join(terms, " "), jsonOut, nil
}

func printTable(w io.Writer, results []index.SearchResult) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTOOL\tUPDATED\tSIZE\tFOLDER\tTITLE")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Tool, shortTime(r.UpdatedAt), r.Size, r.Folder, r.Title)
	}
	_ = tw.Flush()
}

func shortTime(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return t.Local().Format("2006-01-02")
}

func (a App) daemon() error {
	if err := a.ensurePiExtension(); err != nil {
		fmt.Fprintln(a.Stderr, "holocron daemon:", err)
	}
	lock, err := acquireLock(filepath.Join(a.Home, ".local", "share", "holocron", "daemon.lock"))
	if err != nil {
		return err
	}
	defer lock()
	if err := a.fullScan(); err != nil {
		fmt.Fprintln(a.Stderr, "holocron daemon:", err)
	}
	watcher, err := newSourceWatcher(session.DefaultSources(a.Home))
	if err != nil {
		return err
	}
	defer watcher.Close()
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	var debounce <-chan time.Time
	var debounceTimer *time.Timer
	schedule := func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.NewTimer(time.Second)
		debounce = debounceTimer.C
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case err := <-watcher.Errors:
			fmt.Fprintln(a.Stderr, "holocron daemon watcher:", err)
		case <-watcher.Events:
			schedule()
		case <-debounce:
			debounce = nil
			if err := a.fullScan(); err != nil {
				fmt.Fprintln(a.Stderr, "holocron daemon:", err)
			}
			if err := watcher.Refresh(); err != nil {
				fmt.Fprintln(a.Stderr, "holocron daemon watcher:", err)
			}
		case <-ticker.C:
			if err := a.fullScan(); err != nil {
				fmt.Fprintln(a.Stderr, "holocron daemon:", err)
			}
			if err := watcher.Refresh(); err != nil {
				fmt.Fprintln(a.Stderr, "holocron daemon watcher:", err)
			}
		case <-stop:
			return nil
		}
	}
}

func (a App) fullScan() error {
	opts := session.ParseOptions{
		GeminiProjects: session.LoadGeminiProjects(a.Home),
		Warnf: func(format string, args ...any) {
			fmt.Fprintf(a.Stderr, "holocron parser: "+format+"\n", args...)
		},
	}
	records, err := session.Discover(session.DefaultSources(a.Home), opts)
	if err != nil {
		return err
	}
	registered, err := a.registeredSessionRecords(opts)
	if err != nil {
		return err
	}
	records = append(records, registered...)
	return a.store().Rebuild(records)
}

func (a App) registerSession(args []string) error {
	tool, path, err := parseRegisterSessionArgs(args)
	if err != nil {
		return err
	}
	if tool != session.ToolPi {
		return fmt.Errorf("unsupported tool %q", tool)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rec, err := session.ParseFile(tool, filepath.Dir(abs), abs, session.ParseOptions{})
	if err != nil {
		return err
	}
	if err := a.rememberRegisteredSession(abs); err != nil {
		return err
	}
	return a.store().Upsert([]session.Record{rec})
}

func parseRegisterSessionArgs(args []string) (session.Tool, string, error) {
	if len(args) == 2 {
		return session.Tool(args[0]), args[1], nil
	}
	if len(args) == 4 && args[0] == "--tool" && args[2] == "--path" {
		return session.Tool(args[1]), args[3], nil
	}
	return "", "", errors.New("register-session requires: holocron register-session pi PATH")
}

func (a App) registeredSessionRecords(opts session.ParseOptions) ([]session.Record, error) {
	paths, err := a.registeredSessionPaths()
	if err != nil {
		return nil, err
	}
	var records []session.Record
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		rec, err := session.ParseFile(session.ToolPi, filepath.Dir(path), path, opts)
		if err != nil {
			if opts.Warnf != nil {
				opts.Warnf("%s: %v", path, err)
			}
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

func (a App) registeredSessionPaths() ([]string, error) {
	data, err := os.ReadFile(a.registeredSessionsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		path := strings.TrimSpace(line)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out, nil
}

func (a App) rememberRegisteredSession(path string) error {
	paths, err := a.registeredSessionPaths()
	if err != nil {
		return err
	}
	for _, existing := range paths {
		if existing == path {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(a.registeredSessionsPath()), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(a.registeredSessionsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, path)
	return err
}

func (a App) registeredSessionsPath() string {
	return filepath.Join(a.Home, ".local", "share", "holocron", "registered-pi-sessions.txt")
}

func acquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	data := fmt.Sprintf("%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			if _, err := f.WriteString(data); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			if pid, err := strconv.Atoi(fields[0]); err == nil && processRunning(pid) {
				return nil, fmt.Errorf("daemon already running with pid %d", pid)
			}
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

type sourceWatcher struct {
	watcher *fsnotify.Watcher
	sources []session.Source
	mu      sync.Mutex
	fds     map[string]struct{}
	Events  chan struct{}
	Errors  chan error
	done    chan struct{}
}

func newSourceWatcher(sources []session.Source) (*sourceWatcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &sourceWatcher{
		watcher: fw,
		sources: sources,
		fds:     map[string]struct{}{},
		Events:  make(chan struct{}, 1),
		Errors:  make(chan error, 1),
		done:    make(chan struct{}),
	}
	for _, src := range sources {
		if err := w.addTree(src.Root); err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	go w.loop()
	return w, nil
}

func (w *sourceWatcher) Refresh() error {
	w.mu.Lock()
	watched := make([]string, 0, len(w.fds))
	for path := range w.fds {
		watched = append(watched, path)
	}
	w.mu.Unlock()
	for _, path := range watched {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			w.remove(path)
		}
	}
	for _, src := range w.sources {
		if err := w.addTree(src.Root); err != nil {
			return err
		}
	}
	return nil
}

func (w *sourceWatcher) addTree(root string) error {
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return w.addDir(path)
		}
		return nil
	})
}

func (w *sourceWatcher) addDir(path string) error {
	w.mu.Lock()
	if _, ok := w.fds[path]; ok {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()
	if err := w.watcher.Add(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	w.mu.Lock()
	w.fds[path] = struct{}{}
	w.mu.Unlock()
	return nil
}

func (w *sourceWatcher) remove(path string) {
	w.mu.Lock()
	_, ok := w.fds[path]
	if ok {
		delete(w.fds, path)
	}
	w.mu.Unlock()
	if ok {
		_ = w.watcher.Remove(path)
	}
}

func (w *sourceWatcher) loop() {
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Create) {
				if st, err := os.Stat(event.Name); err == nil && st.IsDir() {
					if err := w.addTree(event.Name); err != nil {
						select {
						case w.Errors <- err:
						default:
						}
					}
				}
			}
			select {
			case w.Events <- struct{}{}:
			default:
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			select {
			case w.Errors <- err:
			default:
			}
		}
	}
}

func (w *sourceWatcher) Close() error {
	close(w.done)
	return w.watcher.Close()
}

func (a App) requireService() error {
	if os.Getenv("HOLOCRON_SKIP_SERVICE_CHECK") == "1" {
		return nil
	}
	if err := launchctl("print", serviceTarget()); err != nil {
		return fmt.Errorf("background service is not running; run `holocron service install` or `holocron service start`")
	}
	return nil
}

func (a App) service(args []string) error {
	if len(args) != 1 {
		return errors.New("service requires one command: install, uninstall, start, stop, or status")
	}
	switch args[0] {
	case "install":
		return a.serviceInstall()
	case "uninstall":
		return a.serviceUninstall()
	case "start":
		if err := a.ensurePiExtension(); err != nil {
			return err
		}
		return launchctl("bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plistPath(a.Home))
	case "stop":
		return launchctl("bootout", serviceTarget())
	case "status":
		return a.serviceStatus()
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func (a App) serviceInstall() error {
	if err := a.ensurePiExtension(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath(a.Home)), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(a.Home, "Library", "Logs", "holocron"), 0o755); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := os.WriteFile(plistPath(a.Home), []byte(plist(a.Home, exe)), 0o644); err != nil {
		return err
	}
	_ = launchctl("bootout", serviceTarget())
	if err := launchctl("bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plistPath(a.Home)); err != nil {
		if statusErr := launchctl("print", serviceTarget()); statusErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func (a App) serviceUninstall() error {
	_ = launchctl("bootout", serviceTarget())
	return os.Remove(plistPath(a.Home))
}

func (a App) ensurePiExtension() error {
	piDir := filepath.Join(a.Home, ".pi")
	if st, err := os.Stat(piDir); err != nil || !st.IsDir() {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	extDir := filepath.Join(a.Home, ".pi", "agent", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		return err
	}
	content := holocronPiExtension(exe)
	return os.WriteFile(filepath.Join(extDir, "holocron.ts"), []byte(content), 0o644)
}

func holocronPiExtension(exe string) string {
	exeJSON, _ := json.Marshal(exe)
	return fmt.Sprintf(`import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";

const HOLOCRON = %s;

export default function (pi: ExtensionAPI) {
  let sessionFile: string | undefined;

  const register = () => {
    if (!sessionFile || process.env.HOLOCRON_REGISTER_SESSION === "0") return false;
    if (!existsSync(sessionFile)) return false;
    spawnSync(HOLOCRON, ["register-session", "pi", sessionFile], {
      stdio: "ignore",
      timeout: 5000,
    });
    return true;
  };

  const registerSoon = (attempt = 0) => {
    setTimeout(() => {
      if (!register() && attempt < 20) registerSoon(attempt + 1);
    }, 250);
  };

  pi.on("session_start", async (_event, ctx) => {
    sessionFile = ctx.sessionManager.getSessionFile();
    registerSoon();
  });

  pi.on("agent_end", async () => {
    register();
  });

  pi.on("session_shutdown", async () => {
    register();
  });
}
`, string(exeJSON))
}

func (a App) serviceStatus() error {
	if err := launchctl("print", serviceTarget()); err != nil {
		return fmt.Errorf("service is not running")
	}
	fmt.Fprintln(a.Stdout, "service is running")
	return nil
}

func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func plistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
}

func serviceTarget() string {
	return "gui/" + strconv.Itoa(os.Getuid()) + "/" + serviceLabel
}

func plist(home, exe string) string {
	logDir := filepath.Join(home, "Library", "Logs", "holocron")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, serviceLabel, xmlEscape(exe), xmlEscape(filepath.Join(logDir, "daemon.log")), xmlEscape(filepath.Join(logDir, "daemon.err.log")))
}

func xmlEscape(s string) string {
	repl := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return repl.Replace(s)
}
