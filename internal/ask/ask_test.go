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
		HolocronID:        "cx:abcdef12",
		ProviderSessionID: "abcdef123456",
		Tool:              session.ToolCodex,
		CWD:               "/tmp/project",
		CreatedAt:         time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC),
		UpdatedAt:         time.Date(2026, 5, 30, 11, 0, 0, 0, time.UTC),
		Turns: []transcript.Turn{
			{Role: transcript.RoleUser, Text: "Fix auth"},
			{Role: transcript.RoleAssistant, Text: "Changed redirect handling"},
		},
	}
	prompt := BuildPrompt(tr, "What changed?", 200000)
	for _, want := range []string{"cx:abcdef12", "codex", "/tmp/project", "Fix auth", "Changed redirect handling", "What changed?"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
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
	if !strings.Contains(prompt, "start") {
		t.Fatal("missing start")
	}
	if !strings.Contains(prompt, "end") {
		t.Fatal("missing end")
	}
	if !strings.Contains(prompt, "transcript truncated by holocron") {
		t.Fatal("missing truncation marker")
	}
}
