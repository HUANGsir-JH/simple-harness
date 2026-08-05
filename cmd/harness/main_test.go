package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionCommand 验证 version 子命令的输出。
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

// TestHelpCommand 验证 help 输出提及子命令。
func TestHelpCommand(t *testing.T) {
	if err := run([]string{"help"}); err != nil {
		t.Fatalf("run help: %v", err)
	}
}

// TestUnknownCommand 验证未知命令报错。
func TestUnknownCommand(t *testing.T) {
	if err := run([]string{"bogus"}); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

// TestRunMissingPrompt 验证 run 需要 prompt。
func TestRunMissingPrompt(t *testing.T) {
	if err := run([]string{"run"}); err == nil {
		t.Fatal("expected error when prompt missing")
	}
}

// TestLoadConfigFromFile 验证 YAML 配置加载。
func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "provider: anthropic\nmodel: claude-sonnet-5\nbase_url: https://example.com\napi_key: sk-test\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Provider != "anthropic" || cfg.Model != "claude-sonnet-5" ||
		cfg.BaseURL != "https://example.com" || cfg.APIKey != "sk-test" {
		t.Errorf("cfg mismatch: %+v", cfg)
	}
}

// TestLoadConfigProjectLocal 验证项目级 config.local.yaml 优先于用户级配置。
func TestLoadConfigProjectLocal(t *testing.T) {
	// 在包含 config.local.yaml 的临时 cwd 中运行。
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.local.yaml"),
		[]byte("provider: openai\nmodel: gpt-4o\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Provider != "openai" || cfg.Model != "gpt-4o" {
		t.Errorf("cfg mismatch: %+v", cfg)
	}
}

// TestLoadConfigMissing 验证缺失配置时报错。
func TestLoadConfigMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.yaml")
	t.Setenv("HARNESS_PROVIDER", "")
	t.Setenv("HARNESS_MODEL", "")
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected error for missing config")
	}
}

// TestLoadConfigEnvFallback 验证环境变量回退。
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
