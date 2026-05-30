package index

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mattwynne/holocron/internal/piexport"
	"github.com/mattwynne/holocron/internal/session"
	"github.com/mattwynne/holocron/internal/transcript"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 6

type Store struct {
	Path string
}

type SearchResult struct {
	ID               string   `json:"id"`
	Tool             string   `json:"tool"`
	Title            string   `json:"title"`
	Folder           string   `json:"folder"`
	CWD              string   `json:"cwd"`
	UpdatedAt        string   `json:"updated_at"`
	Size             string   `json:"size"`
	FileSizeBytes    int64    `json:"file_size_bytes"`
	MessageCount     int      `json:"message_count"`
	ResumeArgv       []string `json:"resume_argv"`
	ResumeCommand    string   `json:"resume_command"`
	GeneratedTitle   string   `json:"generated_title,omitempty"`
	GeneratedSummary string   `json:"generated_summary,omitempty"`
	GeneratedAt      string   `json:"generated_at,omitempty"`
	GeneratedBy      string   `json:"generated_by,omitempty"`
	SummaryStatus    string   `json:"summary_status,omitempty"`
	SummaryError     string   `json:"summary_error,omitempty"`
}

type storedRow struct {
	ID               string
	Tool             string
	SourcePath       string
	SessionID        string
	Title            string
	FirstPrompt      string
	CWD              string
	CreatedAt        string
	UpdatedAt        string
	FileSizeBytes    int64
	MessageCount     int
	ResumeArgv       string
	ResumeCommand    string
	GeneratedTitle   string
	GeneratedSummary string
	GeneratedAt      string
	GeneratedBy      string
	SummaryStatus    string
	SummaryError     string
	SummaryStartedAt string
	SummaryAttempts  int
	NormalizedHash   string
	NormalizedAt     string
	TurnCount        int
}

type dbtx interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func DefaultPath(home string) string {
	return filepath.Join(home, ".local", "share", "holocron", "index.sqlite")
}

func (s Store) Init() error {
	db, err := s.openInitialized()
	if err != nil {
		return err
	}
	return db.Close()
}

func (s Store) Rebuild(records []session.Record) error {
	db, err := s.openInitialized()
	if err != nil {
		return err
	}
	defer db.Close()

	existing, err := assignedIDs(db)
	if err != nil {
		return err
	}
	records = session.AssignStableIDs(records, existing)
	generated, err := generatedByID(db)
	if err != nil {
		return err
	}
	normalized := normalizeRecords(records)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer rollbackUnlessCommitted(tx, &err)

	if _, err = tx.Exec("DELETE FROM sessions_fts"); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM session_turns"); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM sessions"); err != nil {
		return err
	}
	for _, rec := range records {
		if err = insertAssignedID(tx, rec); err != nil {
			return err
		}
		if existingGenerated, ok := generated[rec.ID]; ok {
			if err = upsertSessionWithGenerated(tx, rec, existingGenerated); err != nil {
				return err
			}
		} else if err = upsertSession(tx, rec); err != nil {
			return err
		}
		if tr, ok := normalized[rec.ID]; ok {
			if err = replaceTurns(tx, tr); err != nil {
				return err
			}
		}
	}
	err = tx.Commit()
	return err
}

func (s Store) Upsert(records []session.Record) error {
	db, err := s.openInitialized()
	if err != nil {
		return err
	}
	defer db.Close()

	existing, err := assignedIDs(db)
	if err != nil {
		return err
	}
	records = session.AssignStableIDs(records, existing)
	normalized := normalizeRecords(records)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer rollbackUnlessCommitted(tx, &err)

	for _, rec := range records {
		if err = insertAssignedID(tx, rec); err != nil {
			return err
		}
		if err = upsertSession(tx, rec); err != nil {
			return err
		}
		if tr, ok := normalized[rec.ID]; ok {
			if err = replaceTurns(tx, tr); err != nil {
				return err
			}
		}
	}
	err = tx.Commit()
	return err
}

func (s Store) AssignedIDs() (map[string]string, error) {
	db, err := s.openInitialized()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return assignedIDs(db)
}

func (s Store) Search(query string, limit int) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return s.Recent(limit)
	}
	return s.readRows(`SELECT s.id, s.tool, s.title, s.first_prompt, s.cwd, s.updated_at, s.file_size_bytes, s.message_count, s.resume_argv, s.resume_command, s.generated_title, s.generated_summary, s.generated_at, s.generated_by, s.summary_status, s.summary_error
FROM sessions_fts f JOIN sessions s ON s.id = f.id
WHERE sessions_fts MATCH ?
ORDER BY bm25(sessions_fts, 10.0, 10.0, 3.0, 1.0)
LIMIT ?`, ftsQuery(query), limit)
}

func (s Store) SearchInFolder(query, folder string, limit int) ([]SearchResult, error) {
	folder = filepath.Clean(folder)
	if strings.TrimSpace(query) == "" {
		return s.RecentInFolder(folder, limit)
	}
	return s.readRows(`SELECT s.id, s.tool, s.title, s.first_prompt, s.cwd, s.updated_at, s.file_size_bytes, s.message_count, s.resume_argv, s.resume_command, s.generated_title, s.generated_summary, s.generated_at, s.generated_by, s.summary_status, s.summary_error
FROM sessions_fts f JOIN sessions s ON s.id = f.id
WHERE sessions_fts MATCH ? AND (s.cwd = ? OR s.cwd LIKE ? ESCAPE '\')
ORDER BY bm25(sessions_fts, 10.0, 10.0, 3.0, 1.0)
LIMIT ?`, ftsQuery(query), folder, childFolderPattern(folder), limit)
}

func (s Store) Recent(limit int) ([]SearchResult, error) {
	return s.readRows(`SELECT id, tool, title, first_prompt, cwd, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error
FROM sessions ORDER BY updated_at DESC LIMIT ?`, limit)
}

func (s Store) RecentInFolder(folder string, limit int) ([]SearchResult, error) {
	folder = filepath.Clean(folder)
	return s.readRows(`SELECT id, tool, title, first_prompt, cwd, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error
FROM sessions
WHERE cwd = ? OR cwd LIKE ? ESCAPE '\'
ORDER BY updated_at DESC LIMIT ?`, folder, childFolderPattern(folder), limit)
}

func childFolderPattern(folder string) string {
	clean := filepath.Clean(folder)
	if clean == string(filepath.Separator) {
		return string(filepath.Separator) + "%"
	}
	return escapeLike(clean) + string(filepath.Separator) + "%"
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func (s Store) UpdatedBetween(start, end time.Time, limit int) ([]SearchResult, error) {
	return s.readRows(`SELECT id, tool, title, first_prompt, cwd, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error
FROM sessions
WHERE updated_at >= ? AND updated_at < ?
ORDER BY updated_at DESC LIMIT ?`, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), limit)
}

func (s Store) Lookup(id string) (SearchResult, error) {
	rows, err := s.readRows(`SELECT id, tool, title, first_prompt, cwd, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error
FROM sessions WHERE id = ?`, id)
	if err != nil {
		return SearchResult{}, err
	}
	if len(rows) == 0 {
		return SearchResult{}, os.ErrNotExist
	}
	return rows[0], nil
}

func (s Store) Get(id string) (session.Record, error) {
	db, err := s.openInitialized()
	if err != nil {
		return session.Record{}, err
	}
	defer db.Close()

	rows, err := queryStoredRows(db, `SELECT id, tool, source_path, session_id, title, first_prompt, cwd, created_at, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by FROM sessions WHERE id = ?`, id)
	if err != nil {
		return session.Record{}, err
	}
	if len(rows) == 0 {
		return session.Record{}, os.ErrNotExist
	}
	row := rows[0]
	argv, err := decodeArgv(row.ResumeArgv, row.ID)
	if err != nil {
		return session.Record{}, err
	}
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
	db, err := s.openInitialized()
	if err != nil {
		return err
	}
	defer db.Close()
	return deleteID(db, id)
}

func (s Store) DeleteSource(path string) error {
	db, err := s.openInitialized()
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := queryStoredRows(db, `SELECT id FROM sessions WHERE source_path = ?`, path)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer rollbackUnlessCommitted(tx, &err)
	for _, row := range rows {
		if err = deleteID(tx, row.ID); err != nil {
			return err
		}
	}
	err = tx.Commit()
	return err
}

func (s Store) readRows(query string, args ...any) ([]SearchResult, error) {
	db, err := s.openReadOnly()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := queryStoredRows(db, query, args...)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		argv, err := decodeArgv(row.ResumeArgv, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, SearchResult{
			ID:               row.ID,
			Tool:             row.Tool,
			Title:            displayTitle(row),
			Folder:           FolderName(row.CWD),
			CWD:              row.CWD,
			UpdatedAt:        row.UpdatedAt,
			Size:             HumanSize(row.FileSizeBytes),
			FileSizeBytes:    row.FileSizeBytes,
			MessageCount:     row.MessageCount,
			ResumeArgv:       argv,
			ResumeCommand:    row.ResumeCommand,
			GeneratedTitle:   row.GeneratedTitle,
			GeneratedSummary: row.GeneratedSummary,
			GeneratedAt:      row.GeneratedAt,
			GeneratedBy:      row.GeneratedBy,
			SummaryStatus:    row.SummaryStatus,
			SummaryError:     row.SummaryError,
		})
	}
	return out, nil
}

func searchResult(row storedRow) SearchResult {
	argv, _ := decodeArgv(row.ResumeArgv, row.ID)
	return SearchResult{ID: row.ID, Tool: row.Tool, Title: displayTitle(row), Folder: FolderName(row.CWD), CWD: row.CWD, UpdatedAt: row.UpdatedAt, Size: HumanSize(row.FileSizeBytes), FileSizeBytes: row.FileSizeBytes, MessageCount: row.MessageCount, ResumeArgv: argv, ResumeCommand: row.ResumeCommand, GeneratedTitle: row.GeneratedTitle, GeneratedSummary: row.GeneratedSummary, GeneratedAt: row.GeneratedAt, GeneratedBy: row.GeneratedBy, SummaryStatus: row.SummaryStatus, SummaryError: row.SummaryError}
}

func (s Store) openReadOnly() (*sql.DB, error) {
	if _, err := os.Stat(s.Path); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(500)", filepath.ToSlash(s.Path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func (s Store) openInitialized() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func initSchema(db *sql.DB) error {
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=30000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version != 0 && version > SchemaVersion {
		if _, err := db.Exec(`DROP TABLE IF EXISTS sessions_fts; DROP TABLE IF EXISTS sessions; DROP TABLE IF EXISTS assigned_ids;`); err != nil {
			return fmt.Errorf("rebuild incompatible schema version %d: %w", version, err)
		}
		version = 0
	}
	if _, err := db.Exec(`
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
  resume_command TEXT NOT NULL,
  generated_title TEXT NOT NULL DEFAULT '',
  generated_summary TEXT NOT NULL DEFAULT '',
  generated_at TEXT NOT NULL DEFAULT '',
  generated_by TEXT NOT NULL DEFAULT '',
  summary_status TEXT NOT NULL DEFAULT 'pending',
  summary_error TEXT NOT NULL DEFAULT '',
  summary_started_at TEXT NOT NULL DEFAULT '',
  summary_attempts INTEGER NOT NULL DEFAULT 0,
  normalized_hash TEXT NOT NULL DEFAULT '',
  normalized_at TEXT NOT NULL DEFAULT '',
  turn_count INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS session_turns (
  holocron_id TEXT NOT NULL,
  idx INTEGER NOT NULL,
  role TEXT NOT NULL,
  text TEXT NOT NULL,
  tool_name TEXT NOT NULL DEFAULT '',
  tool_input TEXT NOT NULL DEFAULT '',
  tool_output TEXT NOT NULL DEFAULT '',
  is_error INTEGER NOT NULL DEFAULT 0,
  timestamp TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (holocron_id, idx)
);
CREATE TABLE IF NOT EXISTS session_exports (
  holocron_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  path TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  file_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (holocron_id, kind, path)
);
CREATE INDEX IF NOT EXISTS idx_session_exports_holocron_id ON session_exports(holocron_id);
CREATE INDEX IF NOT EXISTS idx_session_exports_kind_updated ON session_exports(kind, updated_at);
CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(id UNINDEXED, title, first_prompt, cwd, transcript_text);
`); err != nil {
		return fmt.Errorf("initialize sqlite schema, including FTS5 table: %w", err)
	}
	if version < 2 {
		if err := addGeneratedColumns(db); err != nil {
			return err
		}
	}
	if version < 3 {
		if err := addSummaryStatusColumns(db); err != nil {
			return err
		}
	}
	if version < 4 {
		if err := migrateLegacyIDPrefixes(db); err != nil {
			return err
		}
	}
	if version < 5 {
		if err := addNormalizedTables(db); err != nil {
			return err
		}
	}
	if version < 6 {
		if err := addSummaryLeaseColumns(db); err != nil {
			return err
		}
	}
	if err := markAlreadyNamedSessionsDone(db); err != nil {
		return err
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", SchemaVersion)); err != nil {
		return fmt.Errorf("store schema version: %w", err)
	}
	return nil
}

func addGeneratedColumns(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE sessions ADD COLUMN generated_title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN generated_summary TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN generated_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN generated_by TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") && !strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("migrate generated metadata: %w", err)
		}
	}
	return nil
}

func addSummaryStatusColumns(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE sessions ADD COLUMN summary_status TEXT NOT NULL DEFAULT 'pending'`,
		`ALTER TABLE sessions ADD COLUMN summary_error TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") && !strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("migrate summary status: %w", err)
		}
	}
	_, err := db.Exec(`UPDATE sessions SET summary_status = 'done' WHERE generated_title <> '' OR generated_summary <> ''`)
	return err
}

func addSummaryLeaseColumns(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE sessions ADD COLUMN summary_started_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN summary_attempts INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") && !strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("migrate summary leases: %w", err)
		}
	}
	return nil
}

func addNormalizedTables(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE sessions ADD COLUMN normalized_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN normalized_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN turn_count INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") && !strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("migrate normalized metadata: %w", err)
		}
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS session_turns (
  holocron_id TEXT NOT NULL,
  idx INTEGER NOT NULL,
  role TEXT NOT NULL,
  text TEXT NOT NULL,
  tool_name TEXT NOT NULL DEFAULT '',
  tool_input TEXT NOT NULL DEFAULT '',
  tool_output TEXT NOT NULL DEFAULT '',
  is_error INTEGER NOT NULL DEFAULT 0,
  timestamp TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (holocron_id, idx)
);
CREATE TABLE IF NOT EXISTS session_exports (
  holocron_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  path TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  file_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (holocron_id, kind, path)
);
CREATE INDEX IF NOT EXISTS idx_session_exports_holocron_id ON session_exports(holocron_id);
CREATE INDEX IF NOT EXISTS idx_session_exports_kind_updated ON session_exports(kind, updated_at);`)
	return err
}

func migrateLegacyIDPrefixes(db *sql.DB) error {
	for oldPrefix, newPrefix := range map[string]string{
		"claude": "cd",
		"codex":  "cx",
		"gemini": "gm",
	} {
		old := oldPrefix + ":"
		new := newPrefix + ":"
		for _, table := range []string{"assigned_ids", "sessions", "sessions_fts"} {
			if _, err := db.Exec(fmt.Sprintf(`UPDATE %s SET id = ? || substr(id, ?) WHERE id LIKE ?`, table), new, len(old)+1, old+"%"); err != nil {
				return fmt.Errorf("migrate %s IDs from %s to %s: %w", table, oldPrefix, newPrefix, err)
			}
		}
	}
	return nil
}

func assignedIDs(db dbtx) (map[string]string, error) {
	rows, err := queryStoredRows(db, "SELECT tool, session_id, source_path, id FROM assigned_ids")
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, row := range rows {
		ids[row.Tool+"\x00"+row.SessionID+"\x00"+row.SourcePath] = row.ID
	}
	return ids, nil
}

func insertAssignedID(db dbtx, rec session.Record) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO assigned_ids(tool, session_id, source_path, id) VALUES(?, ?, ?, ?)`, string(rec.Tool), rec.SessionID, rec.SourcePath, rec.ID)
	return err
}

func generatedByID(db dbtx) (map[string]storedRow, error) {
	rows, err := queryStoredRows(db, `SELECT id, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error, summary_started_at, summary_attempts FROM sessions WHERE generated_title <> '' OR generated_summary <> '' OR summary_status <> 'pending' OR summary_error <> '' OR summary_started_at <> '' OR summary_attempts <> 0`)
	if err != nil {
		return nil, err
	}
	out := map[string]storedRow{}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

func upsertSession(db dbtx, rec session.Record) error {
	if rows, err := queryStoredRows(db, `SELECT generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error, summary_started_at, summary_attempts FROM sessions WHERE id = ?`, rec.ID); err == nil && len(rows) > 0 {
		return upsertSessionWithGenerated(db, rec, rows[0])
	}
	return upsertSessionWithGenerated(db, rec, storedRow{})
}

func upsertSessionWithGenerated(db dbtx, rec session.Record, existing storedRow) error {
	argv, err := json.Marshal(rec.ResumeArgv)
	if err != nil {
		return err
	}
	created := rec.CreatedAt.UTC().Format(time.RFC3339)
	updated := rec.UpdatedAt.UTC().Format(time.RFC3339)
	if existing.SummaryStatus == "" {
		if existing.GeneratedTitle != "" || existing.GeneratedSummary != "" || hasProviderTitle(rec.Title, rec.FirstPrompt) {
			existing.SummaryStatus = "done"
		} else {
			existing.SummaryStatus = "pending"
		}
	}
	display := displayTitle(storedRow{Title: rec.Title, FirstPrompt: rec.FirstPrompt, GeneratedTitle: existing.GeneratedTitle})
	if _, err := db.Exec(`INSERT OR REPLACE INTO sessions(id, tool, source_path, session_id, title, first_prompt, cwd, created_at, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error, summary_started_at, summary_attempts, normalized_hash, normalized_at, turn_count)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rec.ID, string(rec.Tool), rec.SourcePath, rec.SessionID, rec.Title, rec.FirstPrompt, rec.CWD, created, updated, rec.FileSizeBytes, rec.MessageCount, string(argv), rec.ResumeCommand, existing.GeneratedTitle, existing.GeneratedSummary, existing.GeneratedAt, existing.GeneratedBy, existing.SummaryStatus, existing.SummaryError, existing.SummaryStartedAt, existing.SummaryAttempts, existing.NormalizedHash, existing.NormalizedAt, existing.TurnCount); err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM sessions_fts WHERE id = ?`, rec.ID); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO sessions_fts(id, title, first_prompt, cwd, transcript_text) VALUES(?, ?, ?, ?, ?)`, rec.ID, display, rec.FirstPrompt, rec.CWD, rec.Transcript)
	return err
}

func (s Store) UpdateGenerated(id, title, summary, generatedBy string, at time.Time) error {
	db, err := s.openInitialized()
	if err != nil {
		return err
	}
	defer db.Close()
	res, err := db.Exec(`UPDATE sessions SET generated_title = ?, generated_summary = ?, generated_at = ?, generated_by = ?, summary_status = 'done', summary_error = '', summary_started_at = '' WHERE id = ?`, title, summary, at.UTC().Format(time.RFC3339), generatedBy, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return os.ErrNotExist
	}
	if _, err := db.Exec(`UPDATE sessions_fts SET title = ? WHERE id = ?`, title, id); err != nil {
		return err
	}
	return nil
}

func (s Store) MissingGenerated(limit int) ([]SearchResult, error) {
	return s.readRows(`SELECT id, tool, title, first_prompt, cwd, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error FROM sessions WHERE generated_title = '' AND NOT (`+providerTitleSQL()+`) ORDER BY updated_at DESC LIMIT ?`, limit)
}

func (s Store) PendingSummaries(limit int) ([]SearchResult, error) {
	return s.readRows(`SELECT id, tool, title, first_prompt, cwd, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error FROM sessions WHERE generated_title = '' AND NOT (`+providerTitleSQL()+`) AND summary_status = 'pending' ORDER BY updated_at DESC LIMIT ?`, limit)
}

func (s Store) ClaimPendingSummary(now time.Time) (SearchResult, bool, error) {
	db, err := s.openInitialized()
	if err != nil {
		return SearchResult{}, false, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return SearchResult{}, false, err
	}
	defer tx.Rollback()
	rows, err := queryStoredRows(tx, `SELECT id, tool, title, first_prompt, cwd, updated_at, file_size_bytes, message_count, resume_argv, resume_command, generated_title, generated_summary, generated_at, generated_by, summary_status, summary_error FROM sessions WHERE generated_title = '' AND NOT (`+providerTitleSQL()+`) AND summary_status = 'pending' ORDER BY updated_at DESC LIMIT 1`)
	if err != nil {
		return SearchResult{}, false, err
	}
	if len(rows) == 0 {
		if err := tx.Commit(); err != nil {
			return SearchResult{}, false, err
		}
		return SearchResult{}, false, nil
	}
	row := rows[0]
	res, err := tx.Exec(`UPDATE sessions SET summary_status = 'running', summary_error = '', summary_started_at = ?, summary_attempts = summary_attempts + 1 WHERE id = ? AND generated_title = '' AND summary_status = 'pending'`, now.UTC().Format(time.RFC3339), row.ID)
	if err != nil {
		return SearchResult{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if err := tx.Commit(); err != nil {
			return SearchResult{}, false, err
		}
		return SearchResult{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return SearchResult{}, false, err
	}
	row.SummaryStatus = "running"
	row.SummaryError = ""
	return searchResult(row), true, nil
}

func (s Store) ResetStaleRunningSummaries(now time.Time, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		maxAge = 30 * time.Minute
	}
	db, err := s.openInitialized()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	cutoff := now.Add(-maxAge).UTC().Format(time.RFC3339)
	res, err := db.Exec(`UPDATE sessions SET summary_status = 'pending', summary_error = 'reset stale running summary after abandoned lease', summary_started_at = '' WHERE summary_status = 'running' AND generated_title = '' AND (summary_started_at = '' OR summary_started_at < ?)`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s Store) SummaryStatusCounts() (map[string]int, error) {
	db, err := s.openReadOnly()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT summary_status, COUNT(*) FROM sessions GROUP BY summary_status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}

func (s Store) SetSummaryStatus(id, status, message string) error {
	db, err := s.openInitialized()
	if err != nil {
		return err
	}
	defer db.Close()
	startedAt := ""
	if status == "running" {
		startedAt = time.Now().UTC().Format(time.RFC3339)
	}
	res, err := db.Exec(`UPDATE sessions SET summary_status = ?, summary_error = ?, summary_started_at = ? WHERE id = ? AND generated_title = ''`, status, message, startedAt, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return os.ErrNotExist
	}
	return nil
}

func displayTitle(row storedRow) string {
	if hasProviderTitle(row.Title, row.FirstPrompt) {
		return row.Title
	}
	if row.GeneratedTitle != "" {
		return row.GeneratedTitle
	}
	if row.FirstPrompt != "" {
		return row.FirstPrompt
	}
	if row.Title != "" {
		return row.Title
	}
	return "(untitled session)"
}

func hasProviderTitle(title, firstPrompt string) bool {
	return title != "" && title != firstPrompt && title != "(untitled session)"
}

func providerTitleSQL() string {
	return `title <> '' AND title <> first_prompt AND title <> '(untitled session)'`
}

func markAlreadyNamedSessionsDone(db *sql.DB) error {
	_, err := db.Exec(`UPDATE sessions SET summary_status = 'done', summary_error = '' WHERE generated_title <> '' OR generated_summary <> '' OR (` + providerTitleSQL() + `)`)
	return err
}

func deleteID(db dbtx, id string) error {
	if _, err := db.Exec(`DELETE FROM session_turns WHERE holocron_id = ?`, id); err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM session_exports WHERE holocron_id = ?`, id); err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM sessions_fts WHERE id = ?`, id)
	return err
}

func normalizeRecords(records []session.Record) map[string]transcript.Transcript {
	out := map[string]transcript.Transcript{}
	for _, rec := range records {
		tr, err := transcript.ParseFile(rec)
		if err == nil && len(tr.Turns) > 0 {
			out[rec.ID] = tr
		}
	}
	return out
}

func replaceTurns(db dbtx, tr transcript.Transcript) error {
	if _, err := db.Exec(`DELETE FROM session_turns WHERE holocron_id = ?`, tr.HolocronID); err != nil {
		return err
	}
	for i, turn := range tr.Turns {
		ts := ""
		if !turn.Timestamp.IsZero() {
			ts = turn.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		isError := 0
		if turn.IsError {
			isError = 1
		}
		if _, err := db.Exec(`INSERT INTO session_turns(holocron_id, idx, role, text, tool_name, tool_input, tool_output, is_error, timestamp, metadata_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, tr.HolocronID, i, string(turn.Role), turn.Text, turn.ToolName, turn.ToolInput, turn.ToolOutput, isError, ts, turn.MetadataJSON); err != nil {
			return err
		}
	}
	_, err := db.Exec(`UPDATE sessions SET normalized_hash = ?, normalized_at = ?, turn_count = ? WHERE id = ?`, tr.Hash, time.Now().UTC().Format(time.RFC3339), len(tr.Turns), tr.HolocronID)
	return err
}

func (s Store) Transcript(id string) (transcript.Transcript, error) {
	rec, err := s.Get(id)
	if err != nil {
		return transcript.Transcript{}, err
	}
	db, err := s.openReadOnly()
	if err != nil {
		return transcript.Transcript{}, err
	}
	defer db.Close()
	tr := transcript.Transcript{HolocronID: rec.ID, ProviderSessionID: rec.SessionID, Tool: rec.Tool, SourcePath: rec.SourcePath, CWD: rec.CWD, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt}
	if err := db.QueryRow(`SELECT normalized_hash FROM sessions WHERE id = ?`, id).Scan(&tr.Hash); err != nil {
		return transcript.Transcript{}, err
	}
	rows, err := db.Query(`SELECT role, text, tool_name, tool_input, tool_output, is_error, timestamp, metadata_json FROM session_turns WHERE holocron_id = ? ORDER BY idx`, id)
	if err != nil {
		return transcript.Transcript{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var role, text, toolName, toolInput, toolOutput, ts, meta string
		var isError int
		if err := rows.Scan(&role, &text, &toolName, &toolInput, &toolOutput, &isError, &ts, &meta); err != nil {
			return transcript.Transcript{}, err
		}
		tr.Turns = append(tr.Turns, transcript.Turn{Role: transcript.Role(role), Text: text, ToolName: toolName, ToolInput: toolInput, ToolOutput: toolOutput, IsError: isError != 0, Timestamp: parseTime(ts), MetadataJSON: meta})
	}
	if err := rows.Err(); err != nil {
		return transcript.Transcript{}, err
	}
	if len(tr.Turns) == 0 {
		return transcript.Transcript{}, os.ErrNotExist
	}
	return tr, nil
}

func (s Store) ExportRecord(id, kind, path, sourceHash, fileHash string, now time.Time) error {
	db, err := s.openInitialized()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT OR REPLACE INTO session_exports(holocron_id, kind, path, source_hash, file_hash, created_at, updated_at) VALUES(?, ?, ?, ?, ?, COALESCE((SELECT created_at FROM session_exports WHERE holocron_id = ? AND kind = ? AND path = ?), ?), ?)`, id, kind, path, sourceHash, fileHash, id, kind, path, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	return err
}

func (s Store) ViewExport(id string) (path, sourceHash, fileHash string, err error) {
	db, err := s.openReadOnly()
	if err != nil {
		return "", "", "", err
	}
	defer db.Close()
	err = db.QueryRow(`SELECT path, source_hash, file_hash FROM session_exports WHERE holocron_id = ? AND kind = ? ORDER BY updated_at DESC LIMIT 1`, id, string(piexport.KindView)).Scan(&path, &sourceHash, &fileHash)
	return path, sourceHash, fileHash, err
}

func queryStoredRows(db dbtx, query string, args ...any) ([]storedRow, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedRow
	for rows.Next() {
		cols, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		values := make([]sql.NullString, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		var row storedRow
		for i, col := range cols {
			val := ""
			if values[i].Valid {
				val = values[i].String
			}
			switch col {
			case "id":
				row.ID = val
			case "tool":
				row.Tool = val
			case "source_path":
				row.SourcePath = val
			case "session_id":
				row.SessionID = val
			case "title":
				row.Title = val
			case "first_prompt":
				row.FirstPrompt = val
			case "cwd":
				row.CWD = val
			case "created_at":
				row.CreatedAt = val
			case "updated_at":
				row.UpdatedAt = val
			case "file_size_bytes":
				row.FileSizeBytes, _ = strconv.ParseInt(val, 10, 64)
			case "message_count":
				row.MessageCount, _ = strconv.Atoi(val)
			case "resume_argv":
				row.ResumeArgv = val
			case "resume_command":
				row.ResumeCommand = val
			case "generated_title":
				row.GeneratedTitle = val
			case "generated_summary":
				row.GeneratedSummary = val
			case "generated_at":
				row.GeneratedAt = val
			case "generated_by":
				row.GeneratedBy = val
			case "summary_status":
				row.SummaryStatus = val
			case "summary_error":
				row.SummaryError = val
			case "summary_started_at":
				row.SummaryStartedAt = val
			case "summary_attempts":
				row.SummaryAttempts, _ = strconv.Atoi(val)
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func rollbackUnlessCommitted(tx *sql.Tx, errp *error) {
	if *errp != nil {
		_ = tx.Rollback()
	}
}

func decodeArgv(raw, id string) ([]string, error) {
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil {
		return nil, fmt.Errorf("invalid stored resume_argv for %s: %w", id, err)
	}
	return argv, nil
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

func FolderName(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(cwd))
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
