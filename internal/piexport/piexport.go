package piexport

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattwynne/holocron/internal/transcript"
)

type Kind string

const (
	KindView     Kind = "view"
	KindContinue Kind = "continue"
)

type ExportResult struct {
	Path       string
	Kind       Kind
	SourceHash string
	FileHash   string
}

func ViewPath(home, id string) string {
	return filepath.Join(home, ".local", "share", "holocron", "pi-exports", "view", safeID(id)+".jsonl")
}

func ContinueDir(home, id string) string {
	return filepath.Join(home, ".local", "share", "holocron", "pi-exports", "continue", safeID(id))
}

func NewContinuePath(home, id string, now time.Time) (string, error) {
	dir := ContinueDir(home, id)
	for i := 0; i < 3; i++ {
		suffix, err := randomHex(4)
		if err != nil {
			return "", err
		}
		path := filepath.Join(dir, now.UTC().Format("20060102T150405Z")+"-"+suffix+".jsonl")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		}
	}
	return "", fmt.Errorf("could not allocate unique continuation export path")
}

func Write(path string, tr transcript.Transcript, kind Kind, seed string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := tempName(path)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	w := bufio.NewWriter(f)
	if err := writeJSONLine(w, map[string]any{"type": "session", "version": 3, "id": "holocron-" + safeID(tr.HolocronID), "timestamp": formatISO(firstTime(tr.CreatedAt, tr.UpdatedAt, time.Now().UTC())), "cwd": tr.CWD}); err != nil {
		return "", err
	}
	var parent any
	for i, turn := range tr.Turns {
		id := messageID(tr.HolocronID, seed, i, string(turn.Role))
		contentText := renderTurn(turn)
		if contentText == "" {
			continue
		}
		ts := firstTime(turn.Timestamp, tr.UpdatedAt, tr.CreatedAt, time.Now().UTC())
		role := string(turn.Role)
		if turn.Role == transcript.RoleTool || turn.Role == transcript.RoleSystem {
			role = "assistant"
		}
		msg := map[string]any{"type": "message", "id": id, "parentId": parent, "timestamp": formatISO(ts), "message": map[string]any{"role": role, "content": []map[string]string{{"type": "text", "text": contentText}}, "timestamp": ts.UnixMilli()}}
		if err := writeJSONLine(w, msg); err != nil {
			return "", err
		}
		parent = id
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	ok = true
	return FileHash(path)
}

func FileHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func renderTurn(t transcript.Turn) string {
	if t.Role != transcript.RoleTool {
		return t.Text
	}
	name := t.ToolName
	if name == "" {
		name = "unknown"
	}
	parts := []string{"Tool " + name}
	if t.ToolInput != "" {
		parts = append(parts, "called with input "+t.ToolInput)
	}
	if t.ToolOutput != "" {
		parts = append(parts, "Output:\n"+t.ToolOutput)
	}
	if t.IsError {
		parts = append(parts, "Error: true")
	}
	return strings.Join(parts, "\n\n")
}

func writeJSONLine(w *bufio.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func messageID(parts ...any) string {
	h := sha1.New()
	for _, p := range parts {
		fmt.Fprint(h, p, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

func safeID(id string) string {
	return strings.NewReplacer(":", "_", "/", "_", "\\", "_", " ", "_").Replace(id)
}

func tempName(path string) (string, error) {
	suffix, err := randomHex(6)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s.tmp.%d.%s", path, os.Getpid(), suffix), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func firstTime(values ...time.Time) time.Time {
	for _, v := range values {
		if !v.IsZero() {
			return v.UTC()
		}
	}
	return time.Now().UTC()
}

func formatISO(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
