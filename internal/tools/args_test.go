package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestParseArgs 验证泛型参数解析（B4）：有效参数 → 类型化结果；非法 JSON →
// RespondToModel 工具错误（带工具名前缀，错误二分类语义可回填重试，ADR-003）。
func TestParseArgs(t *testing.T) {
	got, err := parseArgs[readFileArgs]("read_file", json.RawMessage(`{"path":"a.go","start_line":2}`))
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if got.Path != "a.go" || got.StartLine != 2 || got.EndLine != 0 {
		t.Errorf("parseArgs = %+v, want path=a.go start=2 end=0", got)
	}

	_, err = parseArgs[readFileArgs]("read_file", json.RawMessage(`{bad`))
	var te *ToolError
	if !errors.As(err, &te) || te == nil {
		t.Fatalf("非法 JSON 应为 *ToolError，got %v", err)
	}
	if !te.RespondToModel {
		t.Error("解析失败应 RespondToModel=true（回填重试）")
	}
	if !strings.Contains(te.Message, "read_file") {
		t.Errorf("错误信息应含工具名: %q", te.Message)
	}
}

// TestBuiltinSchemasValid 验证 7 个内置工具的 Spec 参数 schema（C4 生成）：
// 有效 JSON、type=object、含 properties，且 required 与参数形状一致——
// 约束了 jsonschema 生成（omitempty → 可选）与手写 schema 对齐，防回归。
func TestBuiltinSchemasValid(t *testing.T) {
	cases := []struct {
		name     string
		tool     Tool
		required []string // 期望 required 列表（nil = 无）
	}{
		{"read_file", ReadFileTool{}, []string{"path"}},
		{"write_file", WriteFileTool{}, []string{"path", "content"}},
		{"list_dir", ListDirTool{}, nil},
		{"glob", GlobTool{}, []string{"pattern"}},
		{"apply_patch", ApplyPatchTool{}, []string{"patch"}},
		// command 为 omitempty（ADR-038）：kill_pid 模式无需 command，无 required。
		{"shell_command", ShellCommandTool{}, nil},
		{"update_todo", UpdateTodoTool{}, []string{"todos"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := c.tool.Spec()
			if spec.Name != c.name {
				t.Errorf("Spec.Name = %q, want %q", spec.Name, c.name)
			}
			var m map[string]any
			if err := json.Unmarshal(spec.Parameters, &m); err != nil {
				t.Fatalf("Parameters 非法 JSON: %v\nraw: %s", err, spec.Parameters)
			}
			if m["type"] != "object" {
				t.Errorf("type = %v, want object", m["type"])
			}
			props, ok := m["properties"].(map[string]any)
			if !ok {
				t.Fatalf("缺 properties: %+v", m)
			}
			if len(props) == 0 {
				t.Error("properties 为空")
			}
			var got []string
			if r, ok := m["required"].([]any); ok {
				for _, x := range r {
					s, _ := x.(string)
					got = append(got, s)
				}
			}
			if !sameStringSet(got, c.required) {
				t.Errorf("required = %v, want %v", got, c.required)
			}
		})
	}
}

// TestTodoStatusEnum 验证 update_todo 的 status 枚举落进生成 schema（C4）。
func TestTodoStatusEnum(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(UpdateTodoTool{}.Spec().Parameters, &m); err != nil {
		t.Fatal(err)
	}
	items := m["properties"].(map[string]any)["todos"].(map[string]any)["items"].(map[string]any)
	status := items["properties"].(map[string]any)["status"].(map[string]any)
	enum, ok := status["enum"].([]any)
	if !ok {
		t.Fatalf("status 缺 enum: %+v", status)
	}
	if len(enum) != 3 {
		t.Errorf("enum = %v, want 3 项", enum)
	}
	set := map[string]bool{}
	for _, e := range enum {
		s, _ := e.(string)
		set[s] = true
	}
	for _, want := range []string{"pending", "in_progress", "completed"} {
		if !set[want] {
			t.Errorf("enum 缺 %q: %v", want, enum)
		}
	}
}

// TestSchemaOfCaches 验证 schema 缓存（Spec() 每轮采样调用）：同类型重复
// 生成返回同一份（不重复 reflect），不同类型不同。
func TestSchemaOfCaches(t *testing.T) {
	a1 := schemaOf[readFileArgs]()
	a2 := schemaOf[readFileArgs]()
	if string(a1) != string(a2) {
		t.Error("同类型两次 schemaOf 结果不一致（缓存失效）")
	}
	b := schemaOf[globArgs]()
	if string(a1) == string(b) {
		t.Error("不同类型 schema 不应相同")
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		if !set[x] {
			return false
		}
	}
	return true
}
