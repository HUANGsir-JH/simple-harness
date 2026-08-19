package app

import (
	"context"
	"encoding/json"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// runModeSpawnDescription 是 run 模式专属的 spawn_agent 描述：补齐回合末等子
// 语义（A 方案，2026-08-19）。用户拍板：不改 subagent/tools.go 的默认描述
// （TUI 的"可以停下等待"语义成立），run 装配层单独覆盖。
const runModeSpawnDescription = "创建子 agent 异步执行任务（立即返回 agent id，完成后结果自动注入对话通知，无需轮询；期间可继续执行其他任务）。" +
	"回合结束前若有子 agent 仍在运行，harness 会等待其完成并注入结果后再收尾（单轮 run 模式）。" +
	"子 agent 有独立会话与工具集（general-purpose 全套 / explore 只读）。" +
	"可并行创建多个子 agent。之后可用 list_agents 查看状态、send_message 补充指示、interrupt_agent 中断、resume_agent 继续。"

// specOverrideTool 是装配期装饰器：覆盖 inner 工具的 Spec 描述（其余透传）。
// 用于 run 模式对 spawn_agent 描述的模式专属覆盖，不改工具实现本身。
type specOverrideTool struct {
	inner tools.Tool
	spec  provider.ToolSpec
}

func (t specOverrideTool) Name() string { return t.inner.Name() }
func (t specOverrideTool) Spec() provider.ToolSpec { return t.spec }
func (t specOverrideTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, callID string, args json.RawMessage) (messages.ToolResult, error) {
	return t.inner.Handle(ctx, rc, callID, args)
}

// withRunModeSpawnDescription 把控制工具列表中的 spawn_agent 描述替换为
// run 模式版（仅 run 装配调用；TUI/resume 保持 subagent 包默认描述）。
func withRunModeSpawnDescription(ctls []tools.Tool) []tools.Tool {
	out := make([]tools.Tool, 0, len(ctls))
	for _, t := range ctls {
		if t.Name() == "spawn_agent" {
			s := t.Spec()
			s.Description = runModeSpawnDescription
			out = append(out, specOverrideTool{inner: t, spec: s})
			continue
		}
		out = append(out, t)
	}
	return out
}
