package impl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/middleware"
)

// writeCatalogSkill 在根下写一个目录包技能。
func writeCatalogSkill(t *testing.T, root, name, description, whenToUse, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fm := "---\nname: " + name + "\ndescription: \"" + description + "\"\n"
	if whenToUse != "" {
		fm += "whenToUse: \"" + whenToUse + "\"\n"
	}
	fm += "---\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(fm+body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func catalogPrompt(t *testing.T, dir, current string) string {
	t.Helper()
	m := SkillsCatalogMiddleware{SkillsDir: dir}
	out, err := m.OnSystemPrompt(context.Background(), middleware.NewRuntimeContext(), current)
	if err != nil {
		t.Fatalf("OnSystemPrompt: %v", err)
	}
	return out
}

func TestSkillsCatalogInjectsSection(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "demo-skill", "做演示用", "演示时", "body")
	writeCatalogSkill(t, root, "alpha", "第一个", "", "body")

	out := catalogPrompt(t, root, "BASE")
	if !strings.HasPrefix(out, "BASE") {
		t.Errorf("应保留现有内容在前: %s", out)
	}
	if !strings.Contains(out, "# Skills（技能）") {
		t.Errorf("应含目录标题: %s", out)
	}
	// 按名排序 + 行格式（含 whenToUse）。
	if !strings.Contains(out, "- alpha: 第一个\n") {
		t.Errorf("应含 alpha 行: %s", out)
	}
	if !strings.Contains(out, "- demo-skill: 做演示用（适用：演示时）\n") {
		t.Errorf("应含 demo-skill 行（含 whenToUse）: %s", out)
	}
	// 触发引导。
	if !strings.Contains(out, "调用 skill 工具加载其完整指令") {
		t.Errorf("应含触发引导: %s", out)
	}
	// 正文不得进目录。
	if strings.Contains(out, "body") {
		t.Errorf("目录不得含技能正文: %s", out)
	}
}

func TestSkillsCatalogEmptyPassthrough(t *testing.T) {
	// 空目录：原样透传。
	empty := t.TempDir()
	if out := catalogPrompt(t, empty, "BASE"); out != "BASE" {
		t.Errorf("空目录应透传: %q", out)
	}
	// 目录不存在：透传。
	if out := catalogPrompt(t, filepath.Join(t.TempDir(), "none"), "BASE"); out != "BASE" {
		t.Errorf("目录不存在应透传: %q", out)
	}
	// SkillsDir 空：透传。
	if out := catalogPrompt(t, "", "BASE"); out != "BASE" {
		t.Errorf("空 SkillsDir 应透传: %q", out)
	}
	// 只有非法技能：透传。
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "plain.md"), []byte("no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := catalogPrompt(t, bad, "BASE"); out != "BASE" {
		t.Errorf("仅非法技能应透传: %q", out)
	}
}

func TestSkillsCatalogEmptyCurrent(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "demo", "演示", "", "body")

	out := catalogPrompt(t, root, "")
	if !strings.HasPrefix(out, "# Skills（技能）") {
		t.Errorf("空 current 应直接以目录段开头: %s", out)
	}
}

func TestSkillsCatalogSkipsInvalidFiles(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "good", "好的", "", "body")
	// 非法技能不阻断合法技能。
	bad := filepath.Join(root, "bad", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("---\nname: Bad_Name\ndescription: x\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := catalogPrompt(t, root, "BASE")
	if !strings.Contains(out, "- good: 好的") || strings.Contains(out, "Bad_Name") {
		t.Errorf("非法技能应跳过、合法技能应保留: %s", out)
	}
}
