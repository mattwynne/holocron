package sessionprompt

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mattwynne/holocron/internal/session"
	"github.com/mattwynne/holocron/internal/transcript"
)

func TestContextFromTranscriptRendersNormalizedTurns(t *testing.T) {
	tr := transcript.Transcript{
		HolocronID:        "cx:abc12345",
		ProviderSessionID: "abc123456789",
		Tool:              session.ToolCodex,
		CWD:               "/tmp/project",
		UpdatedAt:         time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC),
		Turns: []transcript.Turn{
			{Role: transcript.RoleUser, Text: "Fix auth"},
			{Role: transcript.RoleAssistant, Text: "Done"},
		},
	}

	ctx := ContextFromTranscript(tr, "Generated title")

	for _, want := range []string{"cx:abc12345", "abc123456789", "codex", "/tmp/project", "Generated title", "Fix auth", "Done"} {
		haystack := ctx.TranscriptText + ctx.Title + ctx.HolocronID + ctx.ProviderSessionID + string(ctx.Tool) + ctx.CWD
		if !strings.Contains(haystack, want) {
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

type fakeStore struct {
	tr    transcript.Transcript
	trErr error
	rec   session.Record
}

func (f fakeStore) Transcript(id string) (transcript.Transcript, error) { return f.tr, f.trErr }
func (f fakeStore) Get(id string) (session.Record, error)               { return f.rec, nil }

func TestLoadContextPrefersNormalizedTranscript(t *testing.T) {
	store := fakeStore{tr: transcript.Transcript{HolocronID: "cx:abc12345", Tool: session.ToolCodex, Turns: []transcript.Turn{{Role: transcript.RoleUser, Text: "normalized"}}}}
	ctx, err := LoadContext(store, "cx:abc12345", "Title")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx.TranscriptText, "normalized") || ctx.Title != "Title" {
		t.Fatalf("ctx = %#v", ctx)
	}
}

func TestLoadContextFallsBackToParsedRecord(t *testing.T) {
	store := fakeStore{trErr: os.ErrNotExist, rec: session.Record{ID: "cx:abc12345", Tool: session.ToolCodex, Transcript: "fallback transcript"}}
	ctx, err := LoadContext(store, "cx:abc12345", "")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.TranscriptText != "fallback transcript" {
		t.Fatalf("ctx = %#v", ctx)
	}
}
