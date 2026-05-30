package piexport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattwynne/holocron/internal/session"
	"github.com/mattwynne/holocron/internal/transcript"
)

func TestWriteProducesPiJSONLWithLinearParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.jsonl")
	tr := transcript.Transcript{
		HolocronID: "cd:abc12345", Tool: session.ToolClaude, CWD: "/tmp/project", CreatedAt: time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC),
		Turns: []transcript.Turn{{Role: transcript.RoleUser, Text: "Hello"}, {Role: transcript.RoleAssistant, Text: "Hi"}, {Role: transcript.RoleTool, ToolName: "Bash", ToolInput: `{"command":"ls"}`, ToolOutput: "README.md"}},
	}
	fileHash, err := Write(path, tr, KindView, "view")
	if err != nil {
		t.Fatal(err)
	}
	if fileHash == "" {
		t.Fatal("file hash was empty")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitLines(string(b))
	if len(lines) != 4 {
		t.Fatalf("line count = %d content=%s", len(lines), string(b))
	}
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header["type"] != "session" || header["version"].(float64) != 3 {
		t.Fatalf("header = %#v", header)
	}
	var first, second map[string]any
	_ = json.Unmarshal([]byte(lines[1]), &first)
	_ = json.Unmarshal([]byte(lines[2]), &second)
	if first["parentId"] != nil {
		t.Fatalf("first parent = %#v", first["parentId"])
	}
	if second["parentId"] != first["id"] {
		t.Fatalf("second parent = %#v first id = %#v", second["parentId"], first["id"])
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
