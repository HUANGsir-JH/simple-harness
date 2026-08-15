package impl

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
)

// TestBaseInstructions 验证基础提示词注入：空当前内容 → 原样注入；
// 非空 → 前置拼接（基础提示词恒在最前）；Text 空 → 透传。
// 用无占位符的字面量 Text，聚焦注入顺序（占位符渲染见 TestBaseInstructionsRender）。
func TestBaseInstructions(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		text    string
		current string
		want    string
	}{
		{"空当前内容注入", "BASE", "", "BASE"},
		{"非空当前内容前置拼接", "BASE", "覆盖", "BASE" + "\n\n覆盖"},
		{"Text 空透传", "", "x", "x"},
		{"Text 空且当前空", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := BaseInstructionsMiddleware{Text: tc.text}
			got, err := m.OnSystemPrompt(ctx, nil, tc.current)
			if err != nil {
				t.Fatalf("OnSystemPrompt: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestBaseInstructionsRender 验证占位符渲染（ADR-043）：{{cwd}}/{{model}} 从 rc
// 注入；nil rc / 空 State / 空 Model 的兜底。
func TestBaseInstructionsRender(t *testing.T) {
	t.Run("显式 cwd 与 model", func(t *testing.T) {
		rc := middleware.NewRuntimeContext()
		rc.State = agentstate.New("s1", "", "D:\\repo")
		rc.Model = "deepseek-chat"
		m := BaseInstructionsMiddleware{Text: "cwd={{cwd}} model={{model}}"}
		got, err := m.OnSystemPrompt(context.Background(), rc, "")
		if err != nil {
			t.Fatalf("OnSystemPrompt: %v", err)
		}
		if got != "cwd=D:\\repo model=deepseek-chat" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("nil rc 回退 getwd 与默认模型", func(t *testing.T) {
		m := BaseInstructionsMiddleware{Text: "cwd={{cwd}} model={{model}}"}
		got, err := m.OnSystemPrompt(context.Background(), nil, "")
		if err != nil {
			t.Fatalf("OnSystemPrompt: %v", err)
		}
		if !strings.HasPrefix(got, "cwd=") || !strings.Contains(got, " model=默认") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("默认模板含环境段", func(t *testing.T) {
		rc := middleware.NewRuntimeContext()
		rc.State = agentstate.New("s1", "", "D:\\repo")
		rc.Model = "m"
		m := BaseInstructionsMiddleware{Text: DefaultBaseInstructions}
		got, err := m.OnSystemPrompt(context.Background(), rc, "")
		if err != nil {
			t.Fatalf("OnSystemPrompt: %v", err)
		}
		if !strings.Contains(got, "工作目录：D:\\repo") || !strings.Contains(got, "模型：m") {
			t.Fatalf("缺环境段：%q", got)
		}
		if strings.Contains(got, "{{cwd}}") || strings.Contains(got, "{{model}}") {
			t.Fatalf("占位符未替换：%q", got)
		}
	})
}
