package app

import (
	"fmt"
	"os"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/subagent"
	"github.com/agent-project/harness/internal/tools"
	"golang.org/x/term"
)

// Mode 是 harness 运行模式：命令层唯一需要回答的问题（架构整理 2026-08-14，
// Composition Root 收敛——命令层只声明模式与参数，装配全在 Build）。
type Mode uint8

const (
	// ModeRun 是单轮流式非交互模式（harness run <prompt>；TTY 时可选审批交互）。
	ModeRun Mode = iota
	// ModeTUI 是交互式新会话模式（harness 无子命令；会话懒加载）。
	ModeTUI
	// ModeResume 是恢复会话进 TUI 模式（harness resume <id>|--last）。
	ModeResume
)

// Options 是命令层对一次运行的声明：模式 + 参数。字段零值 = 未指定。
// 阶段 5 子 agent 的装配变体届时在此参数化（或新装配工厂），不再开散装入口。
type Options struct {
	Mode Mode

	// 配置与运行时覆盖（run 用；其余模式忽略）。
	ConfigPath string // run --config；空 = 默认查找
	Model      string // run --model
	Effort     string // run --effort
	Thinking   bool   // run --thinking
	NoThinking bool   // run --no-thinking
	MaxTurns   int    // run --max-turns（回合上限，0 = 不限；评测用）

	// 展示（TUI/run 共用）。
	NoThinkingDisplay bool
	JSONOut           bool

	// run 模式输入。
	Prompt string

	// resume 模式会话选择。
	ResumeID   string
	ResumeLast bool
}

// Build 是唯一装配入口（Composition Root）：把"模式+参数"接线成可运行的
// HarnessAgent。命令层不再直接接触 agent/session/tui 的构造；新接缝
// （AGENTS.md 注入/子 agent）只改这里。
func Build(o Options) (*HarnessAgent, error) {
	switch o.Mode {
	case ModeRun:
		return buildRun(o)
	case ModeTUI:
		return buildTUI(o)
	case ModeResume:
		return buildResume(o)
	default:
		return nil, fmt.Errorf("app: unknown mode %d", o.Mode)
	}
}

// buildAgent 解析全局 persona 与技能目录路径、创建子 agent Manager 并装配共享
// ReAct agent（AGENTS.md 注入 ADR-043 + 全局 skill 支持 ADR-044 + 子 agent
// 控制工具 ADR-045）。三模式共用：路径由 app 层解析后经 agent.BuildOptions
// 注入 agent.Build，保持"session 知中间件、反之不成立"的防环约定（impl 不
// 反向依赖 session）。主装配 = 内置工具 + 控制工具（不含 wait_task）。
// maxTurns 仅 run 模式传入（评测 --max-turns）；TUI/resume 传 0（不限）。
func buildAgent(res *config.ProviderConfig, defaultMode string, maxTurns int) (*agent.Agent, *subagent.Manager, error) {
	agentsPath, err := session.GlobalAgentsMDPath()
	if err != nil {
		return nil, nil, err
	}
	skillsDir, err := session.GlobalSkillsDir()
	if err != nil {
		return nil, nil, err
	}
	m := subagent.NewManager(subagent.Options{
		Provider:        res,
		DefaultMode:     defaultMode,
		GlobalAgentsMD:  agentsPath,
		GlobalSkillsDir: skillsDir,
	})
	a, err := agent.Build(agent.BuildOptions{
		Provider:        res,
		DefaultMode:     defaultMode,
		GlobalAgentsMD:  agentsPath,
		GlobalSkillsDir: skillsDir,
		Tools:           append(tools.Builtins(skillsDir), subagent.ControlTools(m)...),
		MaxTurns:        maxTurns,
	})
	if err != nil {
		return nil, nil, err
	}
	return a, m, nil
}

// buildRun 装配单轮模式：配置 → 生效配置（flags 覆盖）→ agent → 会话 →
// flags→会话 state 覆盖。prompt 校验前置（原 runCmd 文案）。
func buildRun(o Options) (*HarnessAgent, error) {
	if o.Prompt == "" {
		return nil, fmt.Errorf("run: prompt is required (harness run \"your prompt\"; 不带参数运行 `harness` 进入交互式)")
	}
	var rt *App
	var err error
	if o.ConfigPath != "" {
		rt, err = LoadFrom(o.ConfigPath)
	} else {
		rt, err = Load()
	}
	if err != nil {
		return nil, err
	}
	// 用 res（--model/--effort 覆盖后的生效配置）装配 agent：client 的 thinking
	// effort 与请求模型必须同源，否则 --model 指定的模型会带上默认模型的配置
	// （Bug04，2026-08-10 审查证实 thinking 泄漏）。
	res, err := rt.ResolveFlags(o.Model, o.Effort, o.Thinking, o.NoThinking)
	if err != nil {
		return nil, err
	}
	a, subMgr, err := buildAgent(res, rt.DefaultApprovalMode(), o.MaxTurns)
	if err != nil {
		return nil, err
	}
	sess, err := session.CreateInCWD(res.Model, rt.DefaultApprovalMode())
	if err != nil {
		return nil, err
	}
	h := &HarnessAgent{
		mode:         ModeRun,
		cfg:          rt,
		reactAgent:   a,
		subagents:    subMgr,
		sess:         sess,
		prompt:       o.Prompt,
		jsonOut:      o.JSONOut,
		showThinking: !o.NoThinkingDisplay,
	}
	// flags → 会话 state（随 SessionMiddleware 落盘，resume 可恢复）；失败先
	// 拆除（关会话）再返回。
	if o.Thinking {
		if err := sess.SetThinkingEnabled(boolPtr(true)); err != nil {
			h.Teardown()
			return nil, err
		}
	}
	if o.NoThinking {
		if err := sess.SetThinkingEnabled(boolPtr(false)); err != nil {
			h.Teardown()
			return nil, err
		}
	}
	if o.Effort != "" {
		if err := sess.SetThinkingEffort(o.Effort); err != nil {
			h.Teardown()
			return nil, err
		}
	}
	return h, nil
}

// buildTUI 装配交互式新会话模式（懒加载：会话由 defaultNewSession 在首动作
// 时创建，避免 /exit 或 /switch 残留空会话，2026-08-11 既定）。
func buildTUI(o Options) (*HarnessAgent, error) {
	if o.JSONOut {
		return nil, fmt.Errorf("交互模式不支持 --json（请用 `harness --json run <prompt>`）")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("交互模式需要终端（TUI 全屏），请用 `harness run <prompt>`（非交互单轮）")
	}
	rt, err := Load()
	if err != nil {
		return nil, err
	}
	a, subMgr, err := buildAgent(rt.Provider, rt.DefaultApprovalMode(), 0)
	if err != nil {
		return nil, err
	}
	proj, err := session.ProjectForCWD()
	if err != nil {
		return nil, err
	}
	h := &HarnessAgent{
		mode:         ModeTUI,
		cfg:          rt,
		reactAgent:   a,
		subagents:    subMgr,
		proj:         proj,
		showThinking: !o.NoThinkingDisplay,
	}
	h.newSession = h.defaultNewSession
	return h, nil
}

// buildResume 装配恢复会话模式。错误优先级与历史行为逐字一致：会话解析
// （findProject→info→Resume）先于配置加载。
func buildResume(o Options) (*HarnessAgent, error) {
	if o.JSONOut {
		return nil, fmt.Errorf("resume 交互模式不支持 --json（请用 `harness --json run <prompt>`）")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("resume requires a terminal (full-screen TUI); use `harness run <prompt>` for non-interactive work")
	}
	proj, err := session.ProjectForCWD()
	if err != nil {
		return nil, err
	}

	var info session.SessionInfo
	if o.ResumeLast {
		var ok bool
		if info, ok = proj.Last(); !ok {
			return nil, fmt.Errorf("resume: 本项目暂无会话（先 `harness run`）")
		}
	} else {
		if o.ResumeID == "" {
			return nil, fmt.Errorf("resume: 需要会话 id 或 --last（`harness sessions` 查看）")
		}
		list, _ := proj.Sessions()
		found := false
		for _, s := range list {
			if s.ID == o.ResumeID {
				info = s
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("resume: 会话 %q 不存在（`harness sessions` 查看）", o.ResumeID)
		}
	}

	sess, err := proj.Resume(info)
	if err != nil {
		return nil, err
	}

	rt, err := Load()
	if err != nil {
		return nil, err
	}
	a, subMgr, err := buildAgent(rt.Provider, rt.DefaultApprovalMode(), 0)
	if err != nil {
		return nil, err
	}
	return &HarnessAgent{
		mode:         ModeResume,
		cfg:          rt,
		reactAgent:   a,
		subagents:    subMgr,
		proj:         proj,
		sess:         sess,
		showThinking: !o.NoThinkingDisplay,
	}, nil
}

// boolPtr 构造 bool 指针（--thinking/--no-thinking 覆盖会话 state 用）。
func boolPtr(b bool) *bool { return &b }
