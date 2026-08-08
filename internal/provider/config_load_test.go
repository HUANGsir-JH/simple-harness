package provider

import (
	"os"
	"path/filepath"
	"testing"
)

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

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
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

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DefaultProvider != "p" || len(cfg.Providers) != 1 {
		t.Errorf("cfg mismatch: %+v", cfg)
	}
}

// TestLoadConfigMissing 验证缺失配置时报错。
func TestLoadConfigMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.yaml")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for missing config")
	}
}
