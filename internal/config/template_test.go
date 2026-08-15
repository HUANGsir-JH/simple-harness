package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGlobalConfigPath 验证全局配置候选：HARNESS_HOME 优先，否则
// ~/.harness/config.yaml（2026-08-14 对齐 session 的 workspace 根）。
func TestGlobalConfigPath(t *testing.T) {
	t.Setenv(EnvHome, filepath.Join("tmp", "hh"))
	if got := globalConfigPath(); got != filepath.Join("tmp", "hh", "config.yaml") {
		t.Errorf("HARNESS_HOME 优先: %q", got)
	}
	t.Setenv(EnvHome, "")
	t.Setenv("HOME", filepath.Join("tmp", "homex"))
	if got := globalConfigPath(); got != filepath.Join("tmp", "homex", ".harness", "config.yaml") {
		t.Errorf("默认 ~/.harness: %q", got)
	}
}

// TestEnsureConfigCreatesTemplate 验证不存在时写入全注释模板（无激活值——
// 除注释外不产生任何解析生效的配置，避免误连端点）。
func TestEnsureConfigCreatesTemplate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	created, err := EnsureConfig(p)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "default_provider") || !strings.Contains(text, "approval") {
		t.Error("模板应含引导注释")
	}
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			t.Errorf("模板出现非注释行（激活配置）: %q", line)
		}
	}
}

// TestEnsureConfigIdempotent 验证已存在时完全不动（不覆盖用户编辑）。
func TestEnsureConfigIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := EnsureConfig(p); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureConfig(p)
	if err != nil || created {
		t.Fatalf("第二次应 created=false: %v %v", created, err)
	}
	userEdit := "# 用户已编辑\n"
	if err := os.WriteFile(p, []byte(userEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureConfig(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != userEdit {
		t.Errorf("用户编辑不应被覆盖: %q", data)
	}
}
