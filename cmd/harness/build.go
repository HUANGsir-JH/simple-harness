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
func (r *Runtime) buildAgent() (*agent.Agent, error) {
	client, err := provider.NewClient(r.Resolved)
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
	mw := middleware.NewChain(
		middleware.ToolInstructionsMiddleware{Tools: reg.Specs()},
		session.SessionMiddleware{},
	)

	a := agent.New(client, r.Resolved.Model)
	a.SetTools(reg)
	a.SetMiddleware(mw)
	return a, nil
}

// resolveFlags 校验 CLI 运行时覆盖（--model / --effort / --thinking / --no-thinking），
// 返回新会话的默认 Resolved（模型 + 默认档位 + efforts）；无 flags 时返回默认。
func (r *Runtime) resolveFlags(modelFlag, effortFlag string, thinking, noThinking bool) (*provider.Resolved, error) {
	if thinking && noThinking {
		return nil, fmt.Errorf("--thinking and --no-thinking are mutually exclusive")
	}
	res := r.Resolved
	if modelFlag != "" {
		var err error
		res, err = provider.Resolve(r.Config, modelFlag)
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
