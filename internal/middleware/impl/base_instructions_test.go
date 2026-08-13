package impl

import (
	"context"
	"testing"
)

// TestBaseInstructions 验证基础提示词注入：空当前内容 → 原样注入；
// 非空 → 前置拼接（基础提示词恒在最前）；Text 空 → 透传。
func TestBaseInstructions(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		text    string
		current string
		want    string
	}{
		{"空当前内容注入默认", DefaultBaseInstructions, "", DefaultBaseInstructions},
		{"非空当前内容前置拼接", DefaultBaseInstructions, "覆盖", DefaultBaseInstructions + "\n\n覆盖"},
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
