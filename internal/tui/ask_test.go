package tui

import "testing"

func TestAskPromptResultTrimsWhitespace(t *testing.T) {
	if got := normalizeAskQuestion("\n hello \n"); got != "hello" {
		t.Fatalf("got %q", got)
	}
}
