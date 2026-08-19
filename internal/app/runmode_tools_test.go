package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// fakeTool 是 runMode 工具装饰测试用的最小工具替身。
type fakeTool struct{ name string }

func (f fakeTool) Name() string { return f.name }
func (f fakeTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{Name: f.name, Description: "默认描述:" + f.name}
}
func (f fakeTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, callID string, args json.RawMessage) (messages.ToolResult, error) {
	return messages.ToolResult{Success: true, Content: "handled:" + f.name}, nil
}

// TestWithRunModeSpawnDescription 验证 run 模式 spawn_agent 描述覆盖（用户拍板
// 2026-08-19：不改 subagent/tools.go 默认描述，run 装配单独覆盖）：
//   - 只替换 spawn_agent 的 Spec 描述（含回合末等子语义），其余控制工具原样
//   - Handle 委托原工具（结果一致）
func TestWithRunModeSpawnDescription(t *testing.T) {
	ctls := []tools.Tool{fakeTool{"spawn_agent"}, fakeTool{"list_agents"}}
	wrapped := withRunModeSpawnDescription(ctls)
	if len(wrapped) != 2 {
		t.Fatalf("包装后工具数 = %d, want 2", len(wrapped))
	}
	for _, w := range wrapped {
		if w.Name() == "spawn_agent" {
			s := w.Spec()
			if !strings.Contains(s.Description, "回合结束前若有子 agent 仍在运行，harness 会等待其完成并注入结果后再收尾") {
				t.Errorf("run 模式 spawn_agent 描述应含回合末等子语义: %q", s.Description)
			}
			if s.Description == "默认描述:spawn_agent" {
				t.Errorf("run 描述应覆盖默认描述")
			}
			// Handle 委托原工具。
			res, err := w.Handle(context.Background(), nil, "c", nil)
			if err != nil || res.Content != "handled:spawn_agent" {
				t.Errorf("Handle 委托失败: %+v %v", res, err)
			}
		} else {
			if s := w.Spec(); s.Description != "默认描述:list_agents" {
				t.Errorf("非 spawn 工具描述应原样: %q", s.Description)
			}
		}
	}
}
