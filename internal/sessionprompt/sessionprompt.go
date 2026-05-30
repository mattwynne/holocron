package sessionprompt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

type Store interface {
	Transcript(id string) (transcript.Transcript, error)
	Get(id string) (session.Record, error)
}

func LoadContext(store Store, id, title string) (Context, error) {
	tr, err := store.Transcript(id)
	if err == nil {
		return ContextFromTranscript(tr, title), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Context{}, err
	}
	rec, err := store.Get(id)
	if err != nil {
		return Context{}, err
	}
	if rec.Transcript == "" && rec.SourcePath != "" {
		parsed, err := session.ParseFile(rec.Tool, filepath.Dir(rec.SourcePath), rec.SourcePath, session.ParseOptions{})
		if err != nil {
			return Context{}, err
		}
		parsed.ID = rec.ID
		if parsed.Title == "" {
			parsed.Title = title
		}
		rec = parsed
	}
	if rec.Title == "" {
		rec.Title = title
	}
	return ContextFromRecord(rec), nil
}

func ContextFromTranscript(tr transcript.Transcript, title string) Context {
	return Context{
		HolocronID:        tr.HolocronID,
		Tool:              tr.Tool,
		ProviderSessionID: tr.ProviderSessionID,
		Title:             title,
		CWD:               tr.CWD,
		CreatedAt:         tr.CreatedAt,
		UpdatedAt:         tr.UpdatedAt,
		TranscriptText:    RenderTranscript(tr),
	}
}

func ContextFromRecord(rec session.Record) Context {
	return Context{
		HolocronID:        rec.ID,
		Tool:              rec.Tool,
		ProviderSessionID: rec.SessionID,
		Title:             rec.Title,
		CWD:               rec.CWD,
		CreatedAt:         rec.CreatedAt,
		UpdatedAt:         rec.UpdatedAt,
		TranscriptText:    rec.Transcript,
	}
}

func RenderTranscript(tr transcript.Transcript) string {
	var b strings.Builder
	for _, turn := range tr.Turns {
		label := string(turn.Role)
		if turn.Role == transcript.RoleTool && turn.ToolName != "" {
			label = "tool:" + turn.ToolName
		}
		fmt.Fprintf(&b, "[%s]\n", label)
		if turn.Text != "" {
			b.WriteString(turn.Text)
			b.WriteByte('\n')
		}
		if turn.ToolInput != "" {
			b.WriteString("input: ")
			b.WriteString(turn.ToolInput)
			b.WriteByte('\n')
		}
		if turn.ToolOutput != "" {
			b.WriteString("output: ")
			b.WriteString(turn.ToolOutput)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
