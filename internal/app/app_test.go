package app

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

// TestLoadFrom 验证显式配置路径构建运行时：Config + 默认 ProviderConfig。
func TestLoadFrom(t *testing.T) {
	a, err := LoadFrom(testConfig(t, basicConfig))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if a.Config.DefaultProvider != "p" {
		t.Errorf("config provider: %q", a.Config.DefaultProvider)
	}
	if a.Provider == nil || a.Provider.Model != "m" {
		t.Errorf("provider: %+v", a.Provider)
	}
	if a.Provider.ThinkingEffort != "high" {
		t.Errorf("default effort: %q", a.Provider.ThinkingEffort)
	}
}

// TestLoadFromMissing 验证缺失配置报错。
func TestLoadFromMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := LoadFrom(path); err == nil {
		t.Fatal("expected error for missing config")
	}
}

// TestLoadCached 验证 Load 惰性单例：多次调用返回同一实例。
// 同步缓存本身保证 a==b；本测试固化该契约。
func TestLoadCached(t *testing.T) {
	a, ea := Load()
	b, eb := Load()
	if ea != eb {
		t.Fatalf("两次 Load 错误不一致: %v vs %v", ea, eb)
	}
	if a != b {
		t.Error("Load 应缓存同一实例")
	}
}

// TestResolveFlags 验证 --model / --effort 覆盖校验。
func TestResolveFlags(t *testing.T) {
	a, err := LoadFrom(testConfig(t, basicConfig))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.ResolveFlags("", "", false, false)
	if err != nil || res.Model != "m" {
		t.Fatalf("无 flags 应返回默认: %v %+v", err, res)
	}
	if _, err := a.ResolveFlags("m", "low", false, false); err != nil {
		t.Fatalf("合法 effort 应通过: %v", err)
	}
	if _, err := a.ResolveFlags("m", "turbo", false, false); err == nil {
		t.Fatal("非法 effort 应报错")
	}
	if _, err := a.ResolveFlags("", "", true, true); err == nil {
		t.Fatal("--thinking 与 --no-thinking 应互斥")
	}
}

// TestDefaultApprovalMode 验证 approval.mode 播种与回退。
func TestDefaultApprovalMode(t *testing.T) {
	a, err := LoadFrom(testConfig(t, basicConfig))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.DefaultApprovalMode(); got != "acceptedit" {
		t.Errorf("未配置回退 acceptedit，got %q", got)
	}
	cfg := testConfig(t, `default_provider: p
providers:
  p:
    api_key: k
    models:
      m: {}
approval:
  mode: readonly
`)
	a, err = LoadFrom(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.DefaultApprovalMode(); got != "readonly" {
		t.Errorf("approval.mode=readonly，got %q", got)
	}
}
