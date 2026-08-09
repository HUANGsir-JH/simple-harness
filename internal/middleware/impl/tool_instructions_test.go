package impl

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// TestToolInstructions 验证工具说明注入（工具列表 + apply_patch 语法）。
func TestToolInstructions(t *testing.T) {
	m := ToolInstructionsMiddleware{Tools: []provider.ToolSpec{
		{Name: "read_file", Description: "读文件"},
		{Name: "apply_patch", Description: "应用补丁"},
	}}
	got, err := m.OnSystemPrompt(context.Background(), middleware.NewRuntimeContext(), "You are a helpful coding agent.")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !strings.Contains(got, "You are a helpful coding agent.") {
		t.Error("应保留基础指令")
	}
	if !strings.Contains(got, "- read_file: 读文件") {
		t.Error("应包含工具列表")
	}
	if !strings.Contains(got, "*** Begin Patch") || !strings.Contains(got, "*** End Patch") {
		t.Error("应包含 apply_patch 语法")
	}
}
