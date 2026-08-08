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

// TestLoadRuntime 验证显式配置路径构建运行时：Config + 默认 Resolved。
func TestLoadRuntime(t *testing.T) {
	rt, err := loadRuntime(testConfig(t, basicConfig))
	if err != nil {
		t.Fatalf("loadRuntime: %v", err)
	}
	if rt.Config.DefaultProvider != "p" {
		t.Errorf("config provider: %q", rt.Config.DefaultProvider)
	}
	if rt.Resolved == nil || rt.Resolved.Model != "m" {
		t.Errorf("resolved: %+v", rt.Resolved)
	}
	if rt.Resolved.ThinkingEffort != "high" {
		t.Errorf("default effort: %q", rt.Resolved.ThinkingEffort)
	}
}

// TestLoadRuntimeMissing 验证缺失配置报错。
func TestLoadRuntimeMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := loadRuntime(path); err == nil {
		t.Fatal("expected error for missing config")
	}
}

// TestDefaultRuntimeCached 验证 defaultRuntime 惰性单例：多次调用返回同一实例。
// 同步缓存本身保证 a==b；本测试固化该契约（ADR-026）。
func TestDefaultRuntimeCached(t *testing.T) {
	a, ea := defaultRuntime()
	b, eb := defaultRuntime()
	if ea != eb {
		t.Fatalf("两次 defaultRuntime 错误不一致: %v vs %v", ea, eb)
	}
	if a != b {
		t.Error("defaultRuntime 应缓存同一实例")
	}
}
