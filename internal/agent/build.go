package agent

import (
	"fmt"

	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// Build 装配 CLI 标准 agent：共享 client + 内置工具 + 标准中间件链。
//
// agent 完全无状态（ADR-026）：不持有会话/模型/档位，per-call 一切经 rc 传入
// （rc.Messages/rc.Model/rc.ThinkingEffort/rc.ThinkingEnabled）。因此一个 agent
// 可被多个 goroutine 并发 Run（并行 agent 架构可扩展，阶段五落地）。
// defaultMode 是审批默认模式（config approval.mode 播种值，ADR-029）。
//
// 未来 subagent = 在此之外构造自定义装配（不同工具集/中间件/提示词，本质同样
// 无状态可共享），buildAgent 从 cmd 下沉到此（2026-08-09）。
func Build(res *config.ProviderConfig, defaultMode string) (*Agent, error) {
	client, err := provider.NewClient(res)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}

	reg := tools.NewRegistry()
	for _, t := range tools.Builtins() {
		if err := reg.Register(t); err != nil {
			return nil, err
		}
	}
	// 工具说明注入系统提示（onSystemPrompt middleware；阶段四 AGENTS.md 等在此
	// 追加）。SessionMiddleware 无状态，从 rc.StatePath 读写 AgentState。
	// TodoReminderMiddleware 在模型连续多轮不更新 todo 时注入偏离提醒。
	// UsageMiddleware 累计每轮采样 token 用量进 AgentState（ADR-037 用量展示：
	// /usage 总账 + LastContextTokens 供 footer 与阶段 C 压缩触发）。
	// ToolOutputMiddleware 统一截断工具结果（超长落盘 evictions/ + head/tail preview，ADR-028）。
	// ApprovalMiddleware 工具审批（onActing，三档模式 + 会话级记忆，ADR-029）；
	// 审批交互器经 rc.Approver 注入（TUI/runCmd 各自 channelApprover，非 TTY 不设）。
	// DefaultMode 与会话创建播种同源（App.DefaultApprovalMode，config approval.mode）。
	mw := middleware.NewChain(
		impl.ToolInstructionsMiddleware{Tools: reg.Specs()},
		impl.SessionMiddleware{},
		impl.TodoReminderMiddleware{},
		impl.UsageMiddleware{},
		impl.ToolOutputMiddleware{},
		impl.ApprovalMiddleware{DefaultMode: defaultMode},
	)

	a := New(client, res.Model)
	a.SetTools(reg)
	a.SetMiddleware(mw)
	return a, nil
}
