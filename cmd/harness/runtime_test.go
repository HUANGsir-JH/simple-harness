package main

import (
	"os"
	"path/filepath"
	"testing"
)

// testConfig 写一个指向任意端点的测试配置并返回路径。
func testConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const basicConfig = `default_provider: p
providers:
  p:
    api_key: sk-test
    models:
      m:
        thinking:
          efforts: [low, high, max]
`

// TestLoadApp 验证显式配置路径构建运行时：Config + 默认 Resolved。
func TestLoadApp(t *testing.T) {
	app, err := loadApp(testConfig(t, basicConfig))
	if err != nil {
		t.Fatalf("loadApp: %v", err)
	}
	if app.Config.DefaultProvider != "p" {
		t.Errorf("config provider: %q", app.Config.DefaultProvider)
	}
	if app.Resolved == nil || app.Resolved.Model != "m" {
		t.Errorf("resolved: %+v", app.Resolved)
	}
	if app.Resolved.ThinkingEffort != "high" {
		t.Errorf("default effort: %q", app.Resolved.ThinkingEffort)
	}
}

// TestLoadRuntimeMissing 验证缺失配置报错。
func TestLoadRuntimeMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := loadApp(path); err == nil {
		t.Fatal("expected error for missing config")
	}
}

// TestDefaultAppCached 验证 defaultApp 惰性单例：多次调用返回同一实例。
// 同步缓存本身保证 a==b；本测试固化该契约（ADR-026）。
func TestDefaultAppCached(t *testing.T) {
	a, ea := defaultApp()
	b, eb := defaultApp()
	if ea != eb {
		t.Fatalf("两次 defaultApp 错误不一致: %v vs %v", ea, eb)
	}
	if a != b {
		t.Error("defaultApp 应缓存同一实例")
	}
}
