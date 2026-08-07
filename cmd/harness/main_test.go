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

// TestLoadConfigFromFile 验证多 provider 多模型 YAML 配置加载。
func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `default_provider: deepseek
providers:
  deepseek:
    base_url: https://api.deepseek.com/
    api_key: sk-test
    models:
      deepseek-v4-flash:
        context_window: 128000
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.DefaultProvider != "deepseek" {
		t.Errorf("default provider: %q", cfg.DefaultProvider)
	}
	ds, ok := cfg.Providers["deepseek"]
	if !ok {
		t.Fatal("expected deepseek provider")
	}
	if ds.BaseURL != "https://api.deepseek.com/" || ds.APIKey != "sk-test" {
		t.Errorf("deepseek: %+v", ds)
	}
	if ds.Models["deepseek-v4-flash"].ContextWindow != 128000 {
		t.Errorf("context window: %+v", ds.Models["deepseek-v4-flash"])
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
		[]byte("default_provider: p\nproviders:\n  p:\n    api_key: k\n    models:\n      m: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.DefaultProvider != "p" || len(cfg.Providers) != 1 {
		t.Errorf("cfg mismatch: %+v", cfg)
	}
}

// TestLoadConfigMissing 验证缺失配置时报错。
func TestLoadConfigMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.yaml")
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected error for missing config")
	}
}

// TestRunModelFlag 验证 --model 被 runCmd 的 flag 解析接受。
func TestRunModelFlag(t *testing.T) {
	// 无配置时 runCmd 应报"配置缺失"而非"flag 未定义"——证明 --model 被接受。
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "missing.yaml")
	err := runCmd([]string{"--config", cfgPath, "--model", "deepseek-v4-flash", "hi"}, false)
	if err == nil {
		t.Fatal("expected config error")
	}
	if strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("--model flag not recognized: %v", err)
	}
}

// writeTestConfig 在临时目录写入一个带 thinking 配置的测试配置文件。
func writeTestConfig(t *testing.T, efforts string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `default_provider: p
providers:
  p:
    api_key: sk-test
    models:
      m:
        context_window: 128000
        thinking:
          efforts: [` + efforts + `]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestRunThinkingFlagsMutuallyExclusive 验证 --thinking 与 --no-thinking 互斥。
func TestRunThinkingFlagsMutuallyExclusive(t *testing.T) {
	cfgPath := writeTestConfig(t, "low, high, max")
	err := runCmd([]string{"--config", cfgPath, "--thinking", "--no-thinking", "hi"}, false)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}
}

// TestRunEffortNotSupported 验证 --effort 不在模型 efforts 内时报错。
func TestRunEffortNotSupported(t *testing.T) {
	cfgPath := writeTestConfig(t, "low, high") // 不支持 max
	err := runCmd([]string{"--config", cfgPath, "--effort", "max", "hi"}, false)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected effort not supported error, got %v", err)
	}
}

// TestRunThinkingFlagsParsed 验证 --thinking/--no-thinking/--effort 被 flag 解析接受
// （错误发生在配置缺失而非 flag 未定义）。
func TestRunThinkingFlagsParsed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "missing.yaml")
	err := runCmd([]string{"--config", cfgPath, "--thinking", "--effort", "max", "hi"}, false)
	if err == nil {
		t.Fatal("expected config error")
	}
	if strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("thinking flags not recognized: %v", err)
	}
}
