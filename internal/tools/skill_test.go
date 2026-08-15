package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// writeTestSkill 在 root/skills 下建一个目录包技能。
func writeTestSkill(t *testing.T, root, name, description, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: \"" + description + "\"\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// skillHandle 调用工具并断言错误路径为 RespondToModel 的 ToolError（agent
// 层会把 ToolError 转为失败 ToolResult 回填，工具自身不返回失败 ToolResult）。
func skillHandle(t *testing.T, dir, rawArgs string) (messages.ToolResult, error) {
	t.Helper()
	tool := SkillTool{SkillsDir: dir}
	return tool.Handle(context.Background(), middleware.NewRuntimeContext(), "c1", json.RawMessage(rawArgs))
}

// respondToModelErr 断言 err 是 RespondToModel 的 *ToolError 并返回其内容。
func respondToModelErr(t *testing.T, err error) string {
	t.Helper()
	te, ok := err.(*ToolError)
	if !ok {
		t.Fatalf("应返回 *ToolError，got %T: %v", err, err)
	}
	if !te.RespondToModel {
		t.Fatalf("应 RespondToModel=true: %+v", te)
	}
	return te.Message
}

func TestSkillToolLoadsDirBundle(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "demo-skill", "演示", "## 步骤\n1. 执行\n")

	res, err := skillHandle(t, root, `{"name":"demo-skill"}`)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !res.Success {
		t.Fatalf("应成功: %+v", res)
	}
	if !strings.Contains(res.Content, `<skill_content name="demo-skill">`) {
		t.Errorf("应含 skill_content 包装: %s", res.Content)
	}
	if !strings.Contains(res.Content, "## 步骤") || !strings.Contains(res.Content, "1. 执行") {
		t.Errorf("应含完整正文: %s", res.Content)
	}
}

func TestSkillToolUnknownSkill(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "demo-skill", "演示", "body")

	_, err := skillHandle(t, root, `{"name":"nope"}`)
	if msg := respondToModelErr(t, err); !strings.Contains(msg, "未知技能") {
		t.Errorf("错误信息应说明未知技能: %s", msg)
	}
}

func TestSkillToolInvalidName(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "demo-skill", "演示", "body")

	_, err := skillHandle(t, root, `{"name":"Bad_Name"}`)
	if msg := respondToModelErr(t, err); !strings.Contains(msg, "非法技能名") {
		t.Errorf("非法名应回填错误: %s", msg)
	}
}

func TestSkillToolEmptyDir(t *testing.T) {
	_, err := skillHandle(t, "", `{"name":"demo"}`)
	if msg := respondToModelErr(t, err); !strings.Contains(msg, "未配置") {
		t.Errorf("空 SkillsDir 应回填未配置: %s", msg)
	}
}

func TestSkillToolMissingDir(t *testing.T) {
	_, err := skillHandle(t, filepath.Join(t.TempDir(), "nonexistent"), `{"name":"demo"}`)
	if msg := respondToModelErr(t, err); !strings.Contains(msg, "未知技能") {
		t.Errorf("目录不存在应回填未知技能: %s", msg)
	}
}

func TestSkillToolBadArgs(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "demo-skill", "演示", "body")

	_, err := skillHandle(t, root, `{`)
	if msg := respondToModelErr(t, err); !strings.Contains(msg, "参数解析失败") {
		t.Errorf("坏参数应回填解析失败: %s", msg)
	}
}

func TestSkillToolFlatFallback(t *testing.T) {
	// 平铺技能也能加载（与 Discover 定位一致）。
	root := t.TempDir()
	flat := filepath.Join(root, "flat-skill.md")
	content := "---\nname: flat-skill\ndescription: \"平铺\"\n---\nbody flat"
	if err := os.WriteFile(flat, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := skillHandle(t, root, `{"name":"flat-skill"}`)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !res.Success || !strings.Contains(res.Content, "body flat") {
		t.Errorf("平铺技能应可加载: %+v", res)
	}
}

func TestSkillToolSpec(t *testing.T) {
	spec := SkillTool{}.Spec()
	if spec.Name != "skill" {
		t.Errorf("Name = %q", spec.Name)
	}
	if !strings.Contains(spec.Description, "加载一个可用技能的完整指令") {
		t.Errorf("Description 应说明用途: %s", spec.Description)
	}
	if len(spec.Parameters) == 0 {
		t.Error("应有参数 schema")
	}
}
