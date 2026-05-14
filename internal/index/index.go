package index

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mattwynne/holocron/internal/session"
)

const SchemaVersion = 1

type Store struct {
	Path string
}

type SearchResult struct {
	ID            string   `json:"id"`
	Tool          string   `json:"tool"`
	Title         string   `json:"title"`
	CWD           string   `json:"cwd"`
	UpdatedAt     string   `json:"updated_at"`
	Size          string   `json:"size"`
	FileSizeBytes int64    `json:"file_size_bytes"`
	MessageCount  int      `json:"message_count"`
	ResumeArgv    []string `json:"resume_argv"`
	ResumeCommand string   `json:"resume_command"`
}

type storedRow struct {
	ID            string `json:"id"`
	Tool          string `json:"tool"`
	SourcePath    string `json:"source_path"`
	SessionID     string `json:"session_id"`
	Title         string `json:"title"`
	FirstPrompt   string `json:"first_prompt"`
	CWD           string `json:"cwd"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	MessageCount  int    `json:"message_count"`
	ResumeArgv    string `json:"resume_argv"`
	ResumeCommand string `json:"resume_command"`
}

func DefaultPath(home string) string {
	return filepath.Join(home, ".local", "share", "holocron", "index.sqlite")
}

func (s Store) Init() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	return s.exec(schemaSQL())
}

func (s Store) Rebuild(records []session.Record) error {
	if err := s.Init(); err != nil {
		return err
	}
	existing, err := s.AssignedIDs()
	if err != nil {
		return err
	}
	records = session.AssignStableIDs(records, existing)
	var b strings.Builder
	b.WriteString("BEGIN;\n")
	b.WriteString("DELETE FROM sessions;\nDELETE FROM sessions_fts;\n")
	for _, rec := range records {
		b.WriteString(assignSQL(rec))
		b.WriteString(sessionSQL(rec))
	}
	b.WriteString("COMMIT;\n")
	return s.exec(b.String())
}

func (s Store) AssignedIDs() (map[string]string, error) {
	if err := s.Init(); err != nil {
		return nil, err
	}
	out, err := s.queryJSON("SELECT tool, session_id, source_path, id FROM assigned_ids;")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Tool       string `json:"tool"`
		SessionID  string `json:"session_id"`
		SourcePath string `json:"source_path"`
		ID         string `json:"id"`
	}
	if len(bytes.TrimSpace(out)) != 0 {
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, err
		}
	}
	ids := map[string]string{}
	for _, row := range rows {
		ids[row.Tool+"\x00"+row.SessionID+"\x00"+row.SourcePath] = row.ID
	}
	return ids, nil
}

func (s Store) Search(query string, limit int) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return s.Recent(limit)
	}
	sql := fmt.Sprintf(`SELECT s.id, s.tool, s.title, s.cwd, s.updated_at, s.file_size_bytes, s.message_count, s.resume_argv, s.resume_command
FROM sessions_fts f JOIN sessions s ON s.id = f.id
WHERE sessions_fts MATCH %s
ORDER BY bm25(sessions_fts, 10.0, 10.0, 3.0, 1.0)
LIMIT %d;`, sqlQuote(ftsQuery(query)), limit)
	return s.searchRows(sql)
}

func (s Store) Recent(limit int) ([]SearchResult, error) {
	sql := fmt.Sprintf(`SELECT id, tool, title, cwd, updated_at, file_size_bytes, message_count, resume_argv, resume_command
FROM sessions ORDER BY updated_at DESC LIMIT %d;`, limit)
	return s.searchRows(sql)
}

func (s Store) Get(id string) (session.Record, error) {
	rows, err := s.rows("SELECT id, tool, source_path, session_id, title, first_prompt, cwd, created_at, updated_at, file_size_bytes, message_count, resume_argv, resume_command FROM sessions WHERE id = " + sqlQuote(id) + ";")
	if err != nil {
		return session.Record{}, err
	}
	if len(rows) == 0 {
		return session.Record{}, os.ErrNotExist
	}
	row := rows[0]
	var argv []string
	_ = json.Unmarshal([]byte(row.ResumeArgv), &argv)
	return session.Record{
		ID:            row.ID,
		Tool:          session.Tool(row.Tool),
		SourcePath:    row.SourcePath,
		SessionID:     row.SessionID,
		Title:         row.Title,
		FirstPrompt:   row.FirstPrompt,
		CWD:           row.CWD,
		CreatedAt:     parseTime(row.CreatedAt),
		UpdatedAt:     parseTime(row.UpdatedAt),
		FileSizeBytes: row.FileSizeBytes,
		MessageCount:  row.MessageCount,
		ResumeArgv:    argv,
		ResumeCommand: row.ResumeCommand,
	}, nil
}

func (s Store) Delete(id string) error {
	return s.exec("BEGIN;\nDELETE FROM sessions WHERE id = " + sqlQuote(id) + ";\nDELETE FROM sessions_fts WHERE id = " + sqlQuote(id) + ";\nCOMMIT;\n")
}

func (s Store) searchRows(sql string) ([]SearchResult, error) {
	rows, err := s.rows(sql)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		var argv []string
		_ = json.Unmarshal([]byte(row.ResumeArgv), &argv)
		out = append(out, SearchResult{
			ID:            row.ID,
			Tool:          row.Tool,
			Title:         row.Title,
			CWD:           row.CWD,
			UpdatedAt:     row.UpdatedAt,
			Size:          HumanSize(row.FileSizeBytes),
			FileSizeBytes: row.FileSizeBytes,
			MessageCount:  row.MessageCount,
			ResumeArgv:    argv,
			ResumeCommand: row.ResumeCommand,
		})
	}
	return out, nil
}

func (s Store) rows(sql string) ([]storedRow, error) {
	out, err := s.queryJSON(sql)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var rows []storedRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s Store) exec(sql string) error {
	cmd := exec.Command("sqlite3", s.Path)
	cmd.Stdin = strings.NewReader(sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sqlite3: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (s Store) queryJSON(sql string) ([]byte, error) {
	cmd := exec.Command("sqlite3", "-json", s.Path, sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite3: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func schemaSQL() string {
	return fmt.Sprintf(`
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
PRAGMA user_version=%d;
CREATE TABLE IF NOT EXISTS assigned_ids (
  tool TEXT NOT NULL,
  session_id TEXT NOT NULL,
  source_path TEXT NOT NULL,
  id TEXT NOT NULL UNIQUE,
  PRIMARY KEY (tool, session_id, source_path)
);
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  tool TEXT NOT NULL,
  source_path TEXT NOT NULL,
  session_id TEXT NOT NULL,
  title TEXT NOT NULL,
  first_prompt TEXT NOT NULL,
  cwd TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  file_size_bytes INTEGER NOT NULL,
  message_count INTEGER NOT NULL,
  resume_argv TEXT NOT NULL,
  resume_command TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(id UNINDEXED, title, first_prompt, cwd, transcript_text);
`, SchemaVersion)
}

func assignSQL(rec session.Record) string {
	return fmt.Sprintf("INSERT OR IGNORE INTO assigned_ids(tool, session_id, source_path, id) VALUES(%s,%s,%s,%s);\n",
		sqlQuote(string(rec.Tool)), sqlQuote(rec.SessionID), sqlQuote(rec.SourcePath), sqlQuote(rec.ID))
}

func sessionSQL(rec session.Record) string {
	argv, _ := json.Marshal(rec.ResumeArgv)
	created := rec.CreatedAt.UTC().Format(time.RFC3339)
	updated := rec.UpdatedAt.UTC().Format(time.RFC3339)
	return fmt.Sprintf(`INSERT OR REPLACE INTO sessions(id, tool, source_path, session_id, title, first_prompt, cwd, created_at, updated_at, file_size_bytes, message_count, resume_argv, resume_command)
VALUES(%s,%s,%s,%s,%s,%s,%s,%s,%s,%d,%d,%s,%s);
INSERT INTO sessions_fts(id, title, first_prompt, cwd, transcript_text) VALUES(%s,%s,%s,%s,%s);
`,
		sqlQuote(rec.ID), sqlQuote(string(rec.Tool)), sqlQuote(rec.SourcePath), sqlQuote(rec.SessionID),
		sqlQuote(rec.Title), sqlQuote(rec.FirstPrompt), sqlQuote(rec.CWD), sqlQuote(created), sqlQuote(updated),
		rec.FileSizeBytes, rec.MessageCount, sqlQuote(string(argv)), sqlQuote(rec.ResumeCommand),
		sqlQuote(rec.ID), sqlQuote(rec.Title), sqlQuote(rec.FirstPrompt), sqlQuote(rec.CWD), sqlQuote(rec.Transcript))
}

func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func ftsQuery(q string) string {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return ""
	}
	quoted := make([]string, len(fields))
	for i, f := range fields {
		f = strings.Trim(f, `"`)
		quoted[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " AND ")
}

func HumanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB"}
	v := float64(n)
	for _, unit := range units {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.0f %s", v, unit)
		}
	}
	return fmt.Sprintf("%.0f TB", v/1024)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func IsMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
