package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigReadsPiAndLLMSections(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "holocron")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`[pi]
executable = "/tmp/custom-pi"

[llm]
timeout_seconds = 42
max_input_tokens = 12345
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PiExecutable != "/tmp/custom-pi" {
		t.Fatalf("PiExecutable = %q", cfg.PiExecutable)
	}
	if cfg.Timeout != 42*time.Second {
		t.Fatalf("Timeout = %v", cfg.Timeout)
	}
	if cfg.MaxInputTokens != 12345 {
		t.Fatalf("MaxInputTokens = %d", cfg.MaxInputTokens)
	}
}

func TestDefaultConfigUsesLookPathOrPiFallback(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(cfg.PiExecutable) == "" {
		t.Fatal("PiExecutable is empty")
	}
	if cfg.Timeout != 300*time.Second {
		t.Fatalf("Timeout = %v", cfg.Timeout)
	}
	if cfg.MaxInputTokens != 200000 {
		t.Fatalf("MaxInputTokens = %d", cfg.MaxInputTokens)
	}
}

func TestRunnerInvokesPiPromptMode(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pi")
	log := filepath.Join(dir, "log")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+shellQuote(log)+"\ncat >/dev/null\nprintf answer\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := Runner{Config: Config{PiExecutable: script, Timeout: time.Second, MaxInputTokens: 1000}}
	out, err := r.RunPrompt(context.Background(), "prompt text")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer" {
		t.Fatalf("out = %q", out)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "-p\n--no-tools\n--no-session" {
		t.Fatalf("argv = %q", got)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
