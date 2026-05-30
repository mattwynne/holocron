package ask

import (
	"strings"
	"testing"
	"time"

	"github.com/mattwynne/holocron/internal/session"
	"github.com/mattwynne/holocron/internal/sessionprompt"
)

func TestBuildPromptIncludesMetadataTranscriptAndQuestion(t *testing.T) {
	ctx := sessionprompt.Context{
		HolocronID:        "cx:abcdef12",
		ProviderSessionID: "abcdef123456",
		Tool:              session.ToolCodex,
		Title:             "Auth fix",
		CWD:               "/tmp/project",
		CreatedAt:         time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC),
		UpdatedAt:         time.Date(2026, 5, 30, 11, 0, 0, 0, time.UTC),
		TranscriptText:    "[user]\nFix auth\n\n[assistant]\nChanged redirect handling",
	}
	prompt := BuildPrompt(ctx, "What changed?", 200000)
	for _, want := range []string{"cx:abcdef12", "codex", "/tmp/project", "Auth fix", "Fix auth", "Changed redirect handling", "What changed?"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildPromptTruncatesTranscriptStartAndEnd(t *testing.T) {
	ctx := sessionprompt.Context{
		HolocronID:     "pi:long",
		Tool:           session.ToolPi,
		TranscriptText: strings.Repeat("start ", 200) + strings.Repeat("middle ", 200) + strings.Repeat("end ", 200),
	}
	prompt := BuildPrompt(ctx, "Question?", 200)
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
