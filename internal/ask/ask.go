package ask

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattwynne/holocron/internal/sessionprompt"
)

const charsPerToken = 4
const truncationMarker = "\n... transcript truncated by holocron ...\n"

func BuildPrompt(ctx sessionprompt.Context, question string, maxInputTokens int) string {
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
