package subagent

import (
	"fmt"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentsmd"
	"github.com/agent-project/harness/internal/compact"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// buildSubagent 子装配（按 kind，2026-08-16 独立装配——不复用 agent.Build：
// 装配骨架在本包自由组合，提示词按类型精准化）。链首 persona 两分支：
//   - general-purpose：uniform 主 persona（impl.DefaultBaseInstructions，与主
//     装配一致）+ 委托段中间件（deepseek SUBAGENT_DELEGATION_CONTEXT 同款
//     思路：system prompt 跨主/子保持 uniform，委托边界用独立段注入）；
//   - explore：专属只读提示词（opencode explore.txt 风格，简短聚焦）。
//
// 链其余中间件与主装配一致（AgentsMd/技能目录/工具说明/会话/压缩/后台完成
// 注入/提醒/用量/截断/审批）。同 kind 实例缓存共享（无状态 ADR-026，多个子
// agent 并发 Run 同一实例安全；model/effort 覆盖经子会话 rc per-call 生效）。
func (m *Manager) buildSubagent(kind string) (*agent.Agent, error) {
	m.mu.Lock()
	if a, ok := m.agents[kind]; ok {
		m.mu.Unlock()
		return a, nil
	}
	m.mu.Unlock()

	client := m.opts.Client
	if client == nil {
		c, err := provider.NewClient(m.opts.Provider)
		if err != nil {
			return nil, fmt.Errorf("provider: %w", err)
		}
		client = c
	}

	reg := tools.NewRegistry()
	for _, t := range m.toolset(kind) {
		if err := reg.Register(t); err != nil {
			return nil, err
		}
	}

	opts := compact.Options{
		ContextWindow:   int64(m.opts.Provider.ContextWindow),
		Model:           m.opts.Provider.Model,
		MaxOutputTokens: 4096, // codex/opencode 同值，ADR-037
	}
	compactor := compact.NewRunner(compact.NewSummarizer(client, opts), opts)

	var head []middleware.Middleware
	if kind == KindExplore {
		head = []middleware.Middleware{impl.BaseInstructionsMiddleware{Text: exploreInstructions}}
	} else {
		base := m.opts.BaseInstructions
		if base == "" {
			base = impl.DefaultBaseInstructions
		}
		head = []middleware.Middleware{
			impl.BaseInstructionsMiddleware{Text: base},
			DelegationInstructionsMiddleware{},
		}
	}
	mw := middleware.NewChain(append(head,
		impl.AgentsMdMiddleware{Options: agentsmd.Options{GlobalPath: m.opts.GlobalAgentsMD}},
		impl.SkillsCatalogMiddleware{SkillsDir: m.opts.GlobalSkillsDir},
		impl.ToolInstructionsMiddleware{Tools: reg.Specs()},
		impl.SessionMiddleware{},
		impl.CompactMiddleware{Runner: compactor},
		impl.BackgroundCompletionMiddleware{},
		impl.TodoReminderMiddleware{},
		impl.UsageMiddleware{},
		impl.ToolOutputMiddleware{},
		impl.ApprovalMiddleware{DefaultMode: m.opts.DefaultMode},
	)...)

	a := agent.New(client, m.opts.Provider.Model)
	a.SetTools(reg)
	a.SetMiddleware(mw)
	a.SetCompactor(compactor)

	m.mu.Lock()
	if prev, ok := m.agents[kind]; ok { // 竞态：并发装配同 kind
		m.mu.Unlock()
		return prev, nil
	}
	m.agents[kind] = a
	m.mu.Unlock()
	return a, nil
}
