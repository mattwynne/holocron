package ask

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattwynne/holocron/internal/session"
	"github.com/mattwynne/holocron/internal/transcript"
)

const charsPerToken = 4
const truncationMarker = "\n... transcript truncated by holocron ...\n"

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

func BuildPrompt(tr transcript.Transcript, question string, maxInputTokens int) string {
	return BuildPromptFromContext(ContextFromTranscript(tr, ""), question, maxInputTokens)
}

func BuildPromptFromContext(ctx Context, question string, maxInputTokens int) string {
	question = strings.TrimSpace(question)
	metadata := fmt.Sprintf(`Session metadata:
holocron_id: %s
tool: %s
provider_session_id: %s
title: %s
cwd: %s
created_at: %s
updated_at: %s
`, ctx.HolocronID, ctx.Tool, ctx.ProviderSessionID, ctx.Title, ctx.CWD, formatTime(ctx.CreatedAt), formatTime(ctx.UpdatedAt))
	prefix := `You are answering a question about an old AI coding-agent session.
Use only the session transcript below. If the transcript does not contain enough information, say so.

` + metadata + "\nTranscript:\n"
	suffix := "\n\nQuestion:\n" + question + "\n\nAnswer:\n"
	transcriptText := strings.TrimSpace(ctx.TranscriptText)
	if maxInputTokens <= 0 {
		maxInputTokens = 200000
	}
	maxChars := maxInputTokens * charsPerToken
	available := maxChars - len(prefix) - len(suffix)
	if available > 0 && len(transcriptText) > available {
		transcriptText = truncateMiddle(transcriptText, available)
	}
	return prefix + transcriptText + suffix
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

func truncateMiddle(text string, maxChars int) string {
	if maxChars <= len(truncationMarker)+20 {
		return truncationMarker
	}
	if len(text) <= maxChars {
		return text
	}
	keep := maxChars - len(truncationMarker)
	head := keep / 2
	tail := keep - head
	return text[:head] + truncationMarker + text[len(text)-tail:]
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
