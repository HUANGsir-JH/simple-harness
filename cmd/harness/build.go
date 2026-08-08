package main

import (
	"fmt"
	"slices"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/tools"
)

// buildAgent 构建进程级共享 agent（ADR-026）：client + 工具注册表 + middleware
// 链（工具说明注入 + 无状态 SessionMiddleware）。
//
// agent 完全无状态：不持有会话/模型/档位，per-call 一切经 rc 传入（rc.Messages/
// rc.Model/rc.ThinkingEffort/rc.ThinkingEnabled）。因此一个 agent 可被多个
// goroutine 并发 Run（并行 agent 架构可扩展，阶段五落地）。
func (app *App) buildAgent() (*agent.Agent, error) {
	client, err := provider.NewClient(app.Resolved)
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
	// ToolOutputMiddleware 统一截断工具结果（超长落盘 evictions/ + head/tail preview，ADR-028）。
	mw := middleware.NewChain(
		middleware.ToolInstructionsMiddleware{Tools: reg.Specs()},
		session.SessionMiddleware{},
		middleware.TodoReminderMiddleware{},
		middleware.ToolOutputMiddleware{},
	)

	a := agent.New(client, app.Resolved.Model)
	a.SetTools(reg)
	a.SetMiddleware(mw)
	return a, nil
}

// resolveFlags 校验 CLI 运行时覆盖（--model / --effort / --thinking / --no-thinking），
// 返回新会话的默认 Resolved（模型 + 默认档位 + efforts）；无 flags 时返回默认。
func (app *App) resolveFlags(modelFlag, effortFlag string, thinking, noThinking bool) (*provider.Resolved, error) {
	if thinking && noThinking {
		return nil, fmt.Errorf("--thinking and --no-thinking are mutually exclusive")
	}
	res := app.Resolved
	if modelFlag != "" {
		var err error
		res, err = provider.Resolve(app.Config, modelFlag)
		if err != nil {
			return nil, fmt.Errorf("resolve: %w", err)
		}
	}
	if effortFlag != "" {
		if !slices.Contains(res.ThinkingEfforts, effortFlag) {
			return nil, fmt.Errorf("--effort %q not supported by model %q (supported: %v)", effortFlag, res.Model, res.ThinkingEfforts)
		}
	}
	return res, nil
}
