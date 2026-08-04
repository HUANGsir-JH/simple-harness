package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionCommand verifies the version subcommand output.
func TestVersionCommand(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()

	if err := run([]string{"version"}); err != nil {
		t.Fatalf("run version: %v", err)
	}
	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stdout = old

	out := buf.String()
	if !strings.Contains(out, "harness "+version) {
		t.Errorf("version output: %q", out)
	}
}

// TestHelpCommand verifies help output mentions subcommands.
func TestHelpCommand(t *testing.T) {
	if err := run([]string{"help"}); err != nil {
		t.Fatalf("run help: %v", err)
	}
}

// TestUnknownCommand verifies unknown commands error.
func TestUnknownCommand(t *testing.T) {
	if err := run([]string{"bogus"}); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

// TestRunMissingPrompt verifies run requires a prompt.
func TestRunMissingPrompt(t *testing.T) {
	if err := run([]string{"run"}); err == nil {
		t.Fatal("expected error when prompt missing")
	}
}

// TestLoadConfigFromFile verifies YAML config loading.
func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "provider: anthropic\nmodel: claude-sonnet-5\nbase_url: https://example.com\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Provider != "anthropic" || cfg.Model != "claude-sonnet-5" || cfg.BaseURL != "https://example.com" {
		t.Errorf("cfg mismatch: %+v", cfg)
	}
}

// TestLoadConfigMissing verifies missing config errors.
func TestLoadConfigMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.yaml")
	t.Setenv("HARNESS_PROVIDER", "")
	t.Setenv("HARNESS_MODEL", "")
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected error for missing config")
	}
}

// TestLoadConfigEnvFallback verifies env var fallback.
func TestLoadConfigEnvFallback(t *testing.T) {
	t.Setenv("HARNESS_PROVIDER", "openai")
	t.Setenv("HARNESS_MODEL", "gpt-4o")
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Provider != "openai" || cfg.Model != "gpt-4o" {
		t.Errorf("cfg mismatch: %+v", cfg)
	}
}
