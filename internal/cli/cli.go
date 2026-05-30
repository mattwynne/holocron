package cli

import (
	"bytes"
	"context"
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
	"github.com/mattwynne/holocron/internal/piexport"
	"github.com/mattwynne/holocron/internal/session"
	"github.com/mattwynne/holocron/internal/summarizer"
	"github.com/mattwynne/holocron/internal/tui"
)

const serviceLabel = "net.mattwynne.holocron"

type App struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Home    string
	Exec    func(string, []string, []string) error
	Now     func() time.Time
	Spinner bool
}

func New() App {
	home, _ := os.UserHomeDir()
	return App{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Home:    home,
		Exec:    syscall.Exec,
		Now:     time.Now,
		Spinner: true,
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
	case "today":
		err = a.today(args[1:])
	case "pick":
		err = a.pick(args[1:])
	case "open":
		err = a.open(args[1:])
	case "view":
		err = a.view(args[1:])
	case "continue":
		err = a.continueSession(args[1:])
	case "export-pi":
		err = a.exportPi(args[1:])
	case "summarize":
		err = a.summarize(args[1:])
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
  holocron today [--json]           List sessions updated in the last 24 hours
  holocron pick [Q]                 Pick and reopen a session in the TUI
  holocron open ID                  Reopen an indexed session in its native tool
  holocron view ID [--model MODEL]  View an indexed session in Pi
  holocron continue ID [--model MODEL] Continue an indexed session in Pi
  holocron export-pi ID             Export an indexed session to Pi JSONL
  holocron summarize [ID|--today|--missing] Generate Holocron titles and summaries
  holocron service COMMAND          Manage the background indexer
  holocron version                  Print version

Service commands:
  install, uninstall, start, stop, restart, status`)
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
	stop := a.startSpinner("Searching sessions")
	defer stop()
	results, err := a.store().Search(query, 100)
	if err != nil {
		return err
	}
	stop()
	if jsonOut {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	printTable(a.Stdout, results)
	return nil
}

func (a App) today(args []string) error {
	jsonOut, err := parseTodayArgs(args)
	if err != nil {
		return err
	}
	stop := a.startSpinner("Loading recent sessions")
	defer stop()
	end := a.now()
	start := end.Add(-24 * time.Hour)
	results, err := a.store().UpdatedBetween(start, end, 1000)
	if err != nil {
		return err
	}
	stop()
	if jsonOut {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	printTodayTable(a.Stdout, results)
	return nil
}

func (a App) pick(args []string) error {
	result, err := tui.PickWithStatus(a.store(), strings.Join(args, " "), a.Stdout, a.Stderr, func() string {
		if a.daemonRunningFast() {
			return "daemon running"
		}
		return "daemon not running; results may be stale"
	})
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

func (a App) view(args []string) error {
	opts, err := parsePiCommandArgs("view", args)
	if err != nil {
		return err
	}
	path, err := a.ensurePiExport(opts.ID, piexport.KindView)
	if err != nil {
		return err
	}
	return a.execPiExport(opts.Model, path, opts.ID)
}

func (a App) continueSession(args []string) error {
	opts, err := parsePiCommandArgs("continue", args)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("pi"); err != nil {
		return errors.New("pi executable not found in PATH")
	}
	path, err := a.ensurePiExport(opts.ID, piexport.KindContinue)
	if err != nil {
		return err
	}
	return a.execPiExport(opts.Model, path, opts.ID)
}

func (a App) exportPi(args []string) error {
	opts, err := parseExportPiArgs(args)
	if err != nil {
		return err
	}
	path, err := a.ensurePiExport(opts.ID, opts.Kind)
	if err != nil {
		return err
	}
	if opts.JSON {
		return json.NewEncoder(a.Stdout).Encode(map[string]string{"id": opts.ID, "kind": string(opts.Kind), "path": path})
	}
	fmt.Fprintln(a.Stdout, path)
	return nil
}

type piCommandOptions struct{ ID, Model string }
type exportPiOptions struct {
	ID   string
	Kind piexport.Kind
	JSON bool
}

func parsePiCommandArgs(command string, args []string) (piCommandOptions, error) {
	var opts piCommandOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model":
			i++
			if i >= len(args) {
				return opts, errors.New("--model requires a value")
			}
			opts.Model = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return opts, fmt.Errorf("unknown %s flag %s", command, args[i])
			}
			if opts.ID != "" {
				return opts, fmt.Errorf("%s accepts exactly one session id", command)
			}
			opts.ID = args[i]
		}
	}
	if opts.ID == "" {
		return opts, fmt.Errorf("%s requires exactly one session id", command)
	}
	return opts, nil
}

func parseExportPiArgs(args []string) (exportPiOptions, error) {
	opts := exportPiOptions{Kind: piexport.KindView}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--kind":
			i++
			if i >= len(args) {
				return opts, errors.New("--kind requires a value")
			}
			switch args[i] {
			case "view":
				opts.Kind = piexport.KindView
			case "continue":
				opts.Kind = piexport.KindContinue
			default:
				return opts, errors.New("--kind must be view or continue")
			}
		case "--json":
			opts.JSON = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return opts, fmt.Errorf("unknown export-pi flag %s", args[i])
			}
			if opts.ID != "" {
				return opts, errors.New("export-pi accepts exactly one session id")
			}
			opts.ID = args[i]
		}
	}
	if opts.ID == "" {
		return opts, errors.New("export-pi requires exactly one session id")
	}
	return opts, nil
}

func (a App) ensurePiExport(id string, kind piexport.Kind) (string, error) {
	tr, err := a.store().Transcript(id)
	if err != nil {
		if index.IsMissing(err) {
			return "", fmt.Errorf("session %s has no normalized transcript; wait for the daemon to re-index it or restart the service", id)
		}
		return "", err
	}
	var path string
	if kind == piexport.KindView {
		path = piexport.ViewPath(a.Home, id)
		oldPath, oldSource, oldFile, err := a.store().ViewExport(id)
		if err == nil && oldPath == path && oldSource == tr.Hash {
			if currentHash, hashErr := piexport.FileHash(path); hashErr == nil {
				if oldFile != "" && currentHash != oldFile {
					return "", errors.New("view export was modified; use holocron continue to preserve follow-up work or delete the cached export to regenerate it")
				}
				return path, nil
			}
		}
		if oldPath == path && oldFile != "" {
			if currentHash, hashErr := piexport.FileHash(path); hashErr == nil && currentHash != oldFile {
				return "", errors.New("view export was modified; use holocron continue to preserve follow-up work or delete the cached export to regenerate it")
			}
		}
	} else {
		path, err = piexport.NewContinuePath(a.Home, id, a.now())
		if err != nil {
			return "", err
		}
	}
	fileHash, err := piexport.Write(path, tr, kind, string(kind))
	if err != nil {
		return "", err
	}
	if err := a.store().ExportRecord(id, string(kind), path, tr.Hash, fileHash, a.now()); err != nil {
		return "", err
	}
	return path, nil
}

func (a App) execPiExport(model, path, id string) error {
	tr, err := a.store().Transcript(id)
	if err != nil {
		return err
	}
	if tr.CWD != "" {
		if err := os.Chdir(tr.CWD); err != nil {
			return err
		}
	}
	argv := []string{"pi", "--session", path}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	bin, err := exec.LookPath("pi")
	if err != nil {
		return errors.New("pi executable not found in PATH")
	}
	return a.Exec(bin, argv, os.Environ())
}

type summarizeOptions struct {
	ID      string
	Missing bool
	Recent  bool
	Today   bool
	Limit   int
	Force   bool
	DryRun  bool
	JSON    bool
	Jobs    int
}

type summaryOutput struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	DryRun  bool   `json:"dry_run"`
}

type summarizeJob struct {
	Index int
	Row   index.SearchResult
}

type summarizeResult struct {
	Index  int
	Output summaryOutput
	Err    error
}

func (a App) summarize(args []string) error {
	opts, err := parseSummarizeArgs(args)
	if err != nil {
		return err
	}
	cfg, err := summarizer.LoadConfig(a.Home)
	if err != nil {
		return err
	}
	var results []index.SearchResult
	if opts.ID != "" {
		result, err := a.store().Lookup(opts.ID)
		if err != nil {
			return err
		}
		results = []index.SearchResult{result}
	} else if opts.Missing {
		results, err = a.store().MissingGenerated(opts.Limit)
	} else if opts.Today {
		end := a.now()
		start := end.Add(-24 * time.Hour)
		results, err = a.store().UpdatedBetween(start, end, opts.Limit)
	} else {
		results, err = a.store().Recent(opts.Limit)
	}
	if err != nil {
		return err
	}
	var todo []index.SearchResult
	for _, r := range results {
		if opts.Force || r.SummaryStatus != "done" {
			todo = append(todo, r)
		}
	}
	updateBar, stop := a.startProgressBar("Summarizing sessions", len(todo))
	defer stop()
	outputs := make([]summaryOutput, len(todo))
	jobs := opts.Jobs
	if jobs < 1 {
		jobs = 1
	}
	if jobs > len(todo) && len(todo) > 0 {
		jobs = len(todo)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobCh := make(chan summarizeJob)
	resultCh := make(chan summarizeResult)
	statusCh := make(chan string, jobs)
	var wg sync.WaitGroup
	for worker := 0; worker < jobs; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for job := range jobCh {
				select {
				case statusCh <- fmt.Sprintf("worker %d summarizing %s", worker+1, job.Row.ID):
				default:
				}
				resultCh <- a.summarizeOne(ctx, cfg, opts, job)
			}
		}(worker)
	}
	go func() {
		defer close(jobCh)
		for i, r := range todo {
			select {
			case <-ctx.Done():
				return
			case jobCh <- summarizeJob{Index: i, Row: r}:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(resultCh)
	}()
	completed := 0
	var firstErr error
	updateBar(completed, fmt.Sprintf("Starting %d worker(s)", jobs))
	for completed < len(todo) {
		select {
		case status := <-statusCh:
			updateBar(completed, status)
		case result, ok := <-resultCh:
			if !ok {
				completed = len(todo)
				break
			}
			if result.Err != nil && firstErr == nil {
				firstErr = result.Err
				cancel()
			}
			if result.Err == nil {
				outputs[result.Index] = result.Output
			}
			completed++
			updateBar(completed, fmt.Sprintf("Completed %d/%d", completed, len(todo)))
		case <-time.After(250 * time.Millisecond):
			updateBar(completed, fmt.Sprintf("Running %d worker(s)", jobs))
		}
	}
	stop()
	if firstErr != nil {
		return firstErr
	}
	if opts.JSON {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(outputs)
	}
	for _, out := range outputs {
		fmt.Fprintf(a.Stdout, "%s\t%s\t%s\n", out.ID, out.Title, out.Summary)
	}
	return nil
}

func (a App) backgroundSummaryWorker(ctx context.Context, writeMu *sync.Mutex) {
	cfg, err := summarizer.LoadConfig(a.Home)
	if err != nil || !cfg.Background {
		return
	}
	interval := cfg.BackgroundInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	run := func() {
		if writeMu != nil {
			writeMu.Lock()
			defer writeMu.Unlock()
		}
		rows, err := a.store().PendingSummaries(1)
		if err != nil || len(rows) == 0 {
			return
		}
		row := rows[0]
		if err := a.store().SetSummaryStatus(row.ID, "running", ""); err != nil {
			return
		}
		result := a.summarizeOne(ctx, cfg, summarizeOptions{}, summarizeJob{Row: row})
		if result.Err != nil {
			_ = a.store().SetSummaryStatus(row.ID, "failed", result.Err.Error())
			fmt.Fprintln(a.Stderr, "holocron background summary:", result.Err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (a App) summarizeOne(ctx context.Context, cfg summarizer.Config, opts summarizeOptions, job summarizeJob) summarizeResult {
	r := job.Row
	rec, err := a.store().Get(r.ID)
	if err != nil {
		return summarizeResult{Index: job.Index, Err: fmt.Errorf("summarize %s: %w", r.ID, err)}
	}
	rec, err = session.ParseFile(rec.Tool, filepath.Dir(rec.SourcePath), rec.SourcePath, session.ParseOptions{})
	if err != nil {
		return summarizeResult{Index: job.Index, Err: fmt.Errorf("summarize %s: %w", r.ID, err)}
	}
	rec.ID = r.ID
	runner := summarizer.Runner{Config: cfg}
	final, err := runner.Summarize(ctx, rec)
	if err != nil {
		return summarizeResult{Index: job.Index, Err: fmt.Errorf("summarize %s: %w", r.ID, err)}
	}
	if !opts.DryRun {
		if err := a.store().UpdateGenerated(r.ID, final.Title, final.Summary, strings.Join(cfg.Command, " "), a.now()); err != nil {
			return summarizeResult{Index: job.Index, Err: fmt.Errorf("summarize %s: %w", r.ID, err)}
		}
	}
	return summarizeResult{Index: job.Index, Output: summaryOutput{ID: r.ID, Title: final.Title, Summary: final.Summary, DryRun: opts.DryRun}}
}

func parseSummarizeArgs(args []string) (summarizeOptions, error) {
	opts := summarizeOptions{Limit: 20, Jobs: 4}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--missing":
			opts.Missing = true
		case "--recent":
			opts.Recent = true
		case "--today":
			opts.Today = true
		case "--force":
			opts.Force = true
		case "--dry-run":
			opts.DryRun = true
		case "--json":
			opts.JSON = true
		case "--limit":
			i++
			if i >= len(args) {
				return opts, errors.New("--limit requires a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return opts, errors.New("--limit requires a positive integer")
			}
			opts.Limit = n
		case "--jobs":
			i++
			if i >= len(args) {
				return opts, errors.New("--jobs requires a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return opts, errors.New("--jobs requires a positive integer")
			}
			opts.Jobs = n
		default:
			if strings.HasPrefix(a, "-") {
				return opts, fmt.Errorf("unknown summarize flag %s", a)
			}
			if opts.ID != "" {
				return opts, errors.New("summarize accepts at most one session id")
			}
			opts.ID = a
		}
	}
	modes := 0
	if opts.ID != "" {
		modes++
	}
	if opts.Missing {
		modes++
	}
	if opts.Recent {
		modes++
	}
	if opts.Today {
		modes++
	}
	if modes == 0 {
		return opts, errors.New("summarize requires an ID, --today, --missing, or --recent")
	}
	if modes > 1 {
		return opts, errors.New("summarize accepts only one of ID, --today, --missing, or --recent")
	}
	return opts, nil
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

func parseTodayArgs(args []string) (bool, error) {
	var jsonOut bool
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		default:
			return false, fmt.Errorf("unknown today argument %s", arg)
		}
	}
	return jsonOut, nil
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
		updated := shortDate(r.UpdatedAt)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Tool, updated, r.Size, r.Folder, displayTableTitle(r.Title, tableTitleWidth(w, r.ID, r.Tool, updated, r.Size, r.Folder)))
	}
	_ = tw.Flush()
}

func printTodayTable(w io.Writer, results []index.SearchResult) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTOOL\tUPDATED\tSIZE\tFOLDER\tTITLE")
	for _, r := range results {
		updated := shortDateTime(r.UpdatedAt)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Tool, updated, r.Size, r.Folder, displayTableTitle(r.Title, tableTitleWidth(w, r.ID, r.Tool, updated, r.Size, r.Folder)))
	}
	_ = tw.Flush()
}

func displayTableTitle(title string, width int) string {
	return truncateRunes(strings.Join(strings.Fields(title), " "), width)
}

func tableTitleWidth(_ io.Writer, fields ...string) int {
	columns := 100
	if value := os.Getenv("COLUMNS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			columns = parsed
		}
	}
	used := 0
	for _, field := range fields {
		used += len([]rune(field))
	}
	// Account for tabwriter padding between the six visible columns.
	return columns - used - 14
}

func truncateRunes(s string, width int) string {
	if width <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

func (a App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a App) startSpinner(label string) func() {
	_, stop := a.startProgress(label)
	return stop
}

func (a App) startProgress(label string) (func(string), func()) {
	if !a.Spinner || !isTerminal(a.Stderr) {
		return func(string) {}, func() {}
	}
	updates := make(chan string, 1)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		current := label
		for i := 0; ; i++ {
			fmt.Fprintf(a.Stderr, "\r\033[K%s %s", frames[i%len(frames)], current)
			select {
			case <-done:
				fmt.Fprint(a.Stderr, "\r\033[K")
				return
			case next := <-updates:
				current = next
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(done)
			<-stopped
		})
	}
	update := func(message string) {
		select {
		case updates <- message:
		default:
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- message:
			default:
			}
		}
	}
	return update, stop
}

func (a App) startProgressBar(label string, total int) (func(int, string), func()) {
	if !a.Spinner || !isTerminal(a.Stderr) {
		return func(int, string) {}, func() {}
	}
	done := false
	render := func(current int, message string) {
		if total < 1 {
			fmt.Fprintf(a.Stderr, "\r\033[K%s 0/0 %s", label, message)
			return
		}
		if current < 0 {
			current = 0
		}
		if current > total {
			current = total
		}
		width := 24
		filled := current * width / total
		bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
		pct := current * 100 / total
		fmt.Fprintf(a.Stderr, "\r\033[K%s [%s] %3d%% %d/%d %s", label, bar, pct, current, total, message)
	}
	render(0, "")
	return func(current int, message string) {
			if done {
				return
			}
			render(current, message)
		}, func() {
			if done {
				return
			}
			done = true
			fmt.Fprint(a.Stderr, "\r\033[K")
		}
}

func compactProgressMessage(home, message string) string {
	return strings.ReplaceAll(message, home, "~")
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func shortDate(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return t.Local().Format("2006-01-02")
}

func shortDateTime(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return t.Local().Format("2006-01-02 15:04")
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lastScan := time.Time{}
	var writeMu sync.Mutex
	runScan := func() {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := a.fullScanAndRebuildIndex(); err != nil {
			fmt.Fprintln(a.Stderr, "holocron daemon:", err)
			return
		}
		lastScan = time.Now()
	}
	runScan()
	go a.backgroundSummaryWorker(ctx, &writeMu)
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
		delay := time.Second
		if since := time.Since(lastScan); !lastScan.IsZero() && since < 30*time.Second {
			delay = 30*time.Second - since
		}
		debounceTimer = time.NewTimer(delay)
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
			runScan()
			if err := watcher.Refresh(); err != nil {
				fmt.Fprintln(a.Stderr, "holocron daemon watcher:", err)
			}
		case <-ticker.C:
			runScan()
			if err := watcher.Refresh(); err != nil {
				fmt.Fprintln(a.Stderr, "holocron daemon watcher:", err)
			}
		case <-stop:
			cancel()
			return nil
		}
	}
}

func (a App) fullScan() error {
	return a.fullScanAndRebuildIndex()
}

func (a App) fullScanAndRebuildIndex() error {
	updateProgress, stopProgress := a.startProgress("Scanning sessions")
	defer stopProgress()
	opts := session.ParseOptions{
		GeminiProjects:                   session.LoadGeminiProjects(a.Home),
		DeleteHolocronSummarizerSessions: true,
		Warnf: func(format string, args ...any) {
			fmt.Fprintf(a.Stderr, "holocron parser: "+format+"\n", args...)
		},
		Progressf: func(format string, args ...any) {
			updateProgress(compactProgressMessage(a.Home, fmt.Sprintf(format, args...)))
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
	updateProgress("Updating index")
	if err := a.store().Rebuild(records); err != nil {
		return err
	}
	return nil
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
		if session.IsHolocronSummarizerRecord(rec) {
			if opts.DeleteHolocronSummarizerSessions {
				if err := os.Remove(path); err != nil && opts.Warnf != nil {
					opts.Warnf("%s: remove holocron summarizer session: %v", path, err)
				}
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
	if !a.daemonRunningFast() {
		return fmt.Errorf("background service is not running; run `holocron service install` or `holocron service start`")
	}
	return nil
}

func (a App) daemonRunningFast() bool {
	pid, ok := readDaemonLockPID(filepath.Join(a.Home, ".local", "share", "holocron", "daemon.lock"))
	return ok && processRunning(pid)
}

func readDaemonLockPID(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func (a App) service(args []string) error {
	if len(args) != 1 {
		return errors.New("service requires one command: install, uninstall, start, stop, restart, or status")
	}
	switch args[0] {
	case "install":
		return a.serviceInstall()
	case "uninstall":
		return a.serviceUninstall()
	case "start":
		return a.serviceStart()
	case "stop":
		return a.serviceStop()
	case "restart":
		return a.serviceRestart()
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

func (a App) serviceStart() error {
	if err := a.ensurePiExtension(); err != nil {
		return err
	}
	return launchctl("bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plistPath(a.Home))
}

func (a App) serviceStop() error {
	return launchctl("bootout", serviceTarget())
}

func (a App) serviceRestart() error {
	_ = a.serviceStop()
	return a.serviceStart()
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
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";

const HOLOCRON = %s;
const REGISTER_TIMEOUT_MS = 5000;

export default function (pi: ExtensionAPI) {
  let sessionFile: string | undefined;

  const register = () => {
    if (!sessionFile || process.env.HOLOCRON_REGISTER_SESSION === "0") return false;
    if (!existsSync(sessionFile)) return false;

    const child = spawn(HOLOCRON, ["register-session", "pi", sessionFile], {
      stdio: "ignore",
      detached: true,
    });

    const timeout = setTimeout(() => child.kill(), REGISTER_TIMEOUT_MS);
    timeout.unref();
    child.on("exit", () => clearTimeout(timeout));
    child.unref();

    return true;
  };

  const registerSoon = (attempt = 0) => {
    const timeout = setTimeout(() => {
      if (!register() && attempt < 20) registerSoon(attempt + 1);
    }, 250);
    timeout.unref();
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
	if a.daemonRunningFast() {
		fmt.Fprintln(a.Stdout, "service is running")
		return nil
	}
	return fmt.Errorf("service is not running")
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
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s</string>
  </dict>
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
`, serviceLabel, xmlEscape(exe), xmlEscape(servicePath(home)), xmlEscape(filepath.Join(logDir, "daemon.log")), xmlEscape(filepath.Join(logDir, "daemon.err.log")))
}

func servicePath(home string) string {
	var parts []string
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		parts = append(parts, path)
	}
	for _, path := range filepath.SplitList(os.Getenv("PATH")) {
		add(path)
	}
	for _, path := range []string{
		filepath.Join(home, ".pi", "agent", "bin"),
		filepath.Join(home, ".asdf", "shims"),
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	} {
		add(path)
	}
	return strings.Join(parts, ":")
}

func xmlEscape(s string) string {
	repl := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return repl.Replace(s)
}
