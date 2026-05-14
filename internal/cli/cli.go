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
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mattwynne/holocron/internal/index"
	"github.com/mattwynne/holocron/internal/session"
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
	query := strings.Join(args, " ")
	var (
		results []index.SearchResult
		err     error
	)
	if strings.TrimSpace(query) == "" {
		results, err = a.store().Recent(100)
	} else {
		results, err = a.store().Search(query, 100)
	}
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return errors.New("no sessions found")
	}
	// TODO: replace this non-interactive fallback with a Bubble Tea picker.
	fmt.Fprintln(a.Stderr, "interactive pick TUI is not implemented yet; opening the top-ranked result")
	return a.execResult(results[0].ID)
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
			_ = a.store().Delete(rec.ID)
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
	fmt.Fprintln(tw, "ID\tTOOL\tUPDATED\tSIZE\tTITLE")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Tool, shortTime(r.UpdatedAt), r.Size, r.Title)
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
	lock, err := acquireLock(filepath.Join(a.Home, ".local", "share", "holocron", "daemon.lock"))
	if err != nil {
		return err
	}
	defer lock()
	if err := a.fullScan(); err != nil {
		fmt.Fprintln(a.Stderr, "holocron daemon:", err)
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case <-ticker.C:
			if err := a.fullScan(); err != nil {
				fmt.Fprintln(a.Stderr, "holocron daemon:", err)
			}
		case <-stop:
			return nil
		}
	}
}

func (a App) fullScan() error {
	opts := session.ParseOptions{GeminiProjects: session.LoadGeminiProjects(a.Home)}
	records, err := session.Discover(session.DefaultSources(a.Home), opts)
	if err != nil {
		return err
	}
	return a.store().Rebuild(records)
}

func acquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(path); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			if pid, err := strconv.Atoi(fields[0]); err == nil && processRunning(pid) {
				return nil, fmt.Errorf("daemon already running with pid %d", pid)
			}
		}
		_ = os.Remove(path)
	}
	data := fmt.Sprintf("%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
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
	if err := os.WriteFile(plistPath(a.Home), []byte(plist(a.Home, exe)), 0o644); err != nil {
		return err
	}
	_ = launchctl("bootout", serviceTarget())
	return launchctl("bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plistPath(a.Home))
}

func (a App) serviceUninstall() error {
	_ = launchctl("bootout", serviceTarget())
	return os.Remove(plistPath(a.Home))
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
