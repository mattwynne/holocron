package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	PiExecutable   string
	Timeout        time.Duration
	MaxInputTokens int
}

type Result struct {
	Stdout string
	Stderr string
}

type Runner struct {
	Config Config
}

func DefaultConfig() Config {
	pi, err := exec.LookPath("pi")
	if err != nil {
		pi = "pi"
	}
	return Config{PiExecutable: pi, Timeout: 300 * time.Second, MaxInputTokens: 200000}
}

func LoadConfig(home string) (Config, error) {
	cfg := DefaultConfig()
	path := filepath.Join(home, ".config", "holocron", "config.toml")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	section := ""
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(strings.Split(raw, "#")[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), `"`)
		switch section + "." + key {
		case "pi.executable":
			if val != "" {
				cfg.PiExecutable = val
			}
		case "llm.timeout_seconds":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.Timeout = time.Duration(n) * time.Second
			}
		case "llm.max_input_tokens":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.MaxInputTokens = n
			}
		}
	}
	return cfg, nil
}

func (c Config) PromptCommand() []string {
	return []string{c.PiExecutable, "-p", "--no-tools", "--no-session"}
}

func (r Runner) RunPrompt(ctx context.Context, prompt string) (string, error) {
	result, err := r.RunPromptResult(ctx, prompt)
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func (r Runner) RunPromptResult(ctx context.Context, prompt string) (Result, error) {
	cfg := r.Config
	defaults := DefaultConfig()
	if cfg.PiExecutable == "" {
		cfg.PiExecutable = defaults.PiExecutable
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaults.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	argv := cfg.PromptCommand()
	if len(argv) == 0 || argv[0] == "" {
		return Result{}, errors.New("pi executable is empty")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("pi prompt timed out after %s", cfg.Timeout)
	}
	if err != nil {
		return result, fmt.Errorf("pi prompt failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return result, nil
}
