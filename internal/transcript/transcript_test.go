package transcript

import (
	"strings"
	"testing"
	"time"

	"github.com/mattwynne/holocron/internal/session"
)

func TestParseClaudeNormalizesMessagesAndTools(t *testing.T) {
	rec := session.Record{ID: "cd:abc12345", Tool: session.ToolClaude, SourcePath: "session.jsonl", SessionID: "abc12345", CWD: "/tmp/project", CreatedAt: time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)}
	input := strings.Join([]string{
		`{"sessionId":"abc12345","cwd":"/tmp/project","timestamp":"2026-05-27T10:00:00Z","type":"user","message":{"role":"user","content":[{"type":"text","text":"Fix auth"}]}}`,
		`{"timestamp":"2026-05-27T10:00:01Z","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll inspect it."},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"timestamp":"2026-05-27T10:00:02Z","type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"README.md\n","is_error":false}]}}`,
	}, "\n")
	tr, err := ParseClaude(rec, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	tr.Hash = Hash(tr)
	if len(tr.Turns) != 4 {
		t.Fatalf("turns = %#v", tr.Turns)
	}
	if tr.Turns[0].Role != RoleUser || tr.Turns[0].Text != "Fix auth" {
		t.Fatalf("user turn = %#v", tr.Turns[0])
	}
	if tr.Turns[2].Role != RoleTool || tr.Turns[2].ToolName != "Bash" || !strings.Contains(tr.Turns[2].ToolInput, "ls") {
		t.Fatalf("tool use turn = %#v", tr.Turns[2])
	}
	if tr.Turns[3].Role != RoleTool || !strings.Contains(tr.Turns[3].ToolOutput, "README.md") {
		t.Fatalf("tool result turn = %#v", tr.Turns[3])
	}
	if tr.Hash == "" {
		t.Fatal("hash was empty")
	}
}
