package agent

import (
	"fmt"

	"github.com/agent-project/harness/internal/agentsmd"
	"github.com/agent-project/harness/internal/compact"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// BuildOptions 是 agent.Build 的装配参数（2026-08-16 起收拢为结构体，
// ADR-044：AGENTS.md 路径与全局技能目录同源注入，签名不再随新能力 +1）。
// 字段零值 = 未指定（Provider 必填；路径空 = 对应能力不装配）。
type BuildOptions struct {
	// Provider 是生效 provider 配置（res，--model/--effort 覆盖后）。
	Provider *config.ProviderConfig
	// DefaultMode 是审批默认模式（config approval.mode 播种值，ADR-029）。
	DefaultMode string
	// GlobalAgentsMD 是全局 persona 文件路径（~/.harness/agents.md，
	// $HARNESS_HOME 覆盖，经 session.GlobalAgentsMDPath 解析；空 = 不注入
	// 全局 persona，ADR-043）。
	GlobalAgentsMD string
	// GlobalSkillsDir 是全局技能目录（~/.harness/skills，$HARNESS_HOME
	// 覆盖，经 session.GlobalSkillsDir 解析；空 = 技能不可用，ADR-044）。
	GlobalSkillsDir string
	// Tools 是自定义工具集（阶段 5 子 agent 按类型装配，ADR-045）。
	// 非空时跳过默认内置工具注册（tools.Builtins）；空 = 默认内置 12 工具。
	// agent 保持通用不感知 subagent——装配方负责组合（app 拼主装配、
	// subagent 包拼子装配）。
	Tools []tools.Tool
	// BaseInstructions 覆盖基础提示词（阶段 5：explore 等子 agent 换链首
	// Text，ADR-045）。空 = impl.DefaultBaseInstructions。
	BaseInstructions string
	// Client 覆盖 provider client（测试注入 FakeClient 用；空 = 经
	// provider.NewClient(o.Provider) 创建）。
	Client provider.Client
}

// Build 装配 CLI 标准 agent：共享 client + 内置工具 + 标准中间件链。
//
// agent 完全无状态（ADR-026）：不持有会话/模型/档位，per-call 一切经 rc 传入
// （rc.Messages/rc.Model/rc.ThinkingEffort/rc.ThinkingEnabled）。因此一个 agent
// 可被多个 goroutine 并发 Run（并行 agent 架构可扩展，阶段五落地）。
//
// 未来 subagent = 在此之外构造自定义装配（不同工具集/中间件/提示词，本质同样
// 无状态可共享），buildAgent 从 cmd 下沉到此（2026-08-09）。
func Build(o BuildOptions) (*Agent, error) {
	client := o.Client
	if client == nil {
		c, err := provider.NewClient(o.Provider)
		if err != nil {
			return nil, fmt.Errorf("provider: %w", err)
		}
		client = c
	}

	reg := tools.NewRegistry()
	if len(o.Tools) > 0 {
		// 自定义工具集（子 agent 按类型装配，ADR-045）。
		for _, t := range o.Tools {
			if err := reg.Register(t); err != nil {
				return nil, err
			}
		}
	} else {
		for _, t := range tools.Builtins(o.GlobalSkillsDir) {
			if err := reg.Register(t); err != nil {
				return nil, err
			}
		}
	}
	// 系统提示组合（内容通道分类原则，ADR-037 修订）：BaseInstructions 在链首
	// 注入基础提示词（含 {{cwd}}/{{model}} 动态上下文，调用方 per-call 贡献经
	// rc.SystemPrompt，见 agent.Run）；AgentsMd 注入全局 persona + 项目级 AGENTS.md
	// （阶段四，ADR-043）；SkillsCatalog 注入全局技能目录（ADR-044）；
	// ToolInstructions 追加工具说明。四者仅挂 onSystemPrompt，
	// 不参与洋葱 hook，不影响下列洋葱顺序逻辑。顺序 = 基础 persona → 项目上下文
	// → 技能目录 → 操作型工具引导。
	// SessionMiddleware 无状态，从 rc.StatePath 读写 AgentState。
	// CompactMiddleware 上下文压缩（onReasoning before，ADR-037）：每轮采样前
	// 检查 85% 阈值（实际 usage 驱动 + 估算兜底——兜底由 CompactMiddleware 判定时
	// 用 rc.SystemPrompt（组合后）+ in.Tools 实时估算，不再装配期注入），超则
	// LLM 摘要压缩。注册在 TodoReminder 之前（onion 外层）——压缩是 conversation
	// 级变换、提醒是请求级装饰，外层先压缩重写 in.Messages，内层 TodoReminder
	// 的临时提醒注入才不会被覆盖丢弃；同时仍在 UsageMiddleware 之前，保证压缩
	// 的 before 先于采样、用量的 after 后于采样。
	// TodoReminderMiddleware 在模型连续多轮不更新 todo 时注入偏离提醒。
	// UsageMiddleware 记录每轮采样 token 用量进 AgentState（ADR-037 用量展示：
	// /usage 最近一次调用用量 + LastContextTokens 供 footer 与压缩触发）。
	// ToolOutputMiddleware 统一截断工具结果（超长落盘 evictions/ + head/tail preview，ADR-028）。
	// ApprovalMiddleware 工具审批（onActing，三档模式 + 会话级记忆，ADR-029）；
	// 审批交互器经 rc.Approver 注入（TUI/runCmd 各自 channelApprover，非 TTY 不设）。
	// DefaultMode 与会话创建播种同源（App.DefaultApprovalMode，config approval.mode）。
	opts := compact.Options{
		ContextWindow:   int64(o.Provider.ContextWindow),
		Model:           o.Provider.Model,
		MaxOutputTokens: 4096, // codex/opencode 同值，ADR-037
	}
	compactor := compact.NewRunner(compact.NewSummarizer(client, opts), opts)

	base := impl.DefaultBaseInstructions
	if o.BaseInstructions != "" {
		base = o.BaseInstructions
	}
	mw := middleware.NewChain(
		impl.BaseInstructionsMiddleware{Text: base},
		impl.AgentsMdMiddleware{Options: agentsmd.Options{GlobalPath: o.GlobalAgentsMD}},
		impl.SkillsCatalogMiddleware{SkillsDir: o.GlobalSkillsDir},
		impl.ToolInstructionsMiddleware{Tools: reg.Specs()},
		impl.SessionMiddleware{},
		impl.CompactMiddleware{Runner: compactor},
		// 后台完成通知注入（2026-08-13）：onReasoning before，每次采样前
		// Drain completions 队列以 user 消息注入。注册在 Compact 之后——
		// 压缩重写 conversation 后本中间件再注入、同步 in.Messages；在
		// TodoReminder 之前——注入的通知不会被内层提醒装饰逻辑覆盖。
		impl.BackgroundCompletionMiddleware{},
		impl.TodoReminderMiddleware{},
		impl.UsageMiddleware{},
		impl.ToolOutputMiddleware{},
		impl.ApprovalMiddleware{DefaultMode: o.DefaultMode},
	)

	a := New(client, o.Provider.Model)
	a.SetTools(reg)
	a.SetMiddleware(mw)
	a.SetCompactor(compactor)
	return a, nil
}
