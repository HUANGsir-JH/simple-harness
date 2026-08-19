package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/subagent"
	"github.com/agent-project/harness/internal/ui"
	"github.com/agent-project/harness/internal/ui/tui"
	"golang.org/x/term"
)

// HarnessAgent 是一次装配的产物（Composition Root 的唯一出口，架构整理
// 2026-08-14）：装配完成的 harness 主体，内部持有基础 ReAct agent（无状态
// 共享，ADR-026）。命名避开 middleware.RuntimeContext 的 Runtime。生命周期：
// Build → Run（内部 Teardown）；Run 幂等由调用方保证（一次装配一次运行）。
// 阶段 5 子 agent = 本体的装配变体（换工具集/中间件/提示词），届时在 Build
// 参数化或新装配工厂派生，不再开散装入口。
//
// 分层（2026-08-14 复查，用户提问"与 Controller 相似"后定稿）：HarnessAgent
// 是**装配产物（零件生产者）**，不是模式运行时。与 TUI 的 Controller 表面
// 相似（同持 agent/proj/会话/创建器）是零件清单跨 app→tui 接缝下传的代价
// （tui 不能反向 import app，防环）——职责边界：本类型进程级一次一份、模式
// 无关、持**单**会话；Controller 是 TUI 模式运行时（会话注册表/回合编排/
// bubbletea 桥，仅在 TUI 程序运行期间存在；run 模式的对位物是 runOnce 函数）。
// Run() 只做分派：把零件交给对应模式运行时。
type HarnessAgent struct {
	mode Mode
	cfg  *App // 配置装配（进程级共享）

	// reactAgent 是基础 ReAct agent（无状态，可被多会话/多 goroutine 共享）。
	reactAgent *agent.Agent

	// subagents 是子 agent 管理器（阶段 5，ADR-045）：进程内注册表 + 生命周期，
	// Teardown 时 Shutdown（cancel 全部 + 等收尾）。
	subagents *subagent.Manager

	proj *session.Project
	sess *session.Session // run/resume 已建/已载；ModeTUI 新入口 nil（懒加载）

	// newSession 是 ModeTUI 的懒加载创建器（方法值 defaultNewSession）；
	// ModeResume 传 nil（不触发）。
	newSession func() (*session.Session, error)

	// run 模式执行参数。
	prompt       string
	jsonOut      bool
	showThinking bool
	// subagentWait 是 run 模式回合末等子超时（--subagent-wait；0 = 不等待，
	// 保持旧行为）。仅 run 模式生效：TUI 子跨回合存活由完成注入 + 唤醒承担。
	subagentWait time.Duration

	closed bool // Teardown 幂等守卫
}

// defaultNewSession 是 ModeTUI 的懒加载创建器（方法值，架构整理 2026-08-14
// 去匿名闭包）：首条消息/状态命令经 Controller.ensureActive 触发，用默认
// provider 配置的模型 + 默认审批模式。
func (h *HarnessAgent) defaultNewSession() (*session.Session, error) {
	return session.CreateInCWD(h.cfg.Provider.Model, h.cfg.DefaultApprovalMode())
}

// Run 执行该模式：内部按模式创建顶层 signal ctx，启动对应入口，收尾 Teardown。
// 命令层拿到 HarnessAgent 后只调 Run——启动/收尾各一段，与 Build 对称。
func (h *HarnessAgent) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), h.signals()...)
	defer stop()
	defer h.Teardown()
	switch h.mode {
	case ModeRun:
		return h.runOnce(ctx)
	default:
		return h.runTUI(ctx)
	}
}

// signals 按模式选择监听信号：run 单轮 Esc/Ctrl+C 中断用 SIGINT（+SIGTERM
// 终止进程）；TUI 的 SIGINT（Ctrl+C）由 bubbletea 作为按键事件处理（复制
// 语义）、回合中断用 Esc（ADR-028），故只监听 SIGTERM。
func (h *HarnessAgent) signals() []os.Signal {
	if h.mode == ModeRun {
		return []os.Signal{os.Interrupt, syscall.SIGTERM}
	}
	return []os.Signal{syscall.SIGTERM}
}

// runTUI 执行 TUI 模式：显式三阶段（Assemble → Run → Close，见 tui 包）。
func (h *HarnessAgent) runTUI(ctx context.Context) error {
	t := tui.AssembleWith(h.reactAgent, h.proj, h.cfg.Config, h.sess, h.newSession, ctx, h.showThinking, h.subagents)
	runErr := t.Run() // program.Run → WaitRuns → SaveActiveState
	t.Close()         // CloseAll（与 Assemble 对称）
	return runErr
}

// runOnce 执行单轮模式：渲染器选择 + 输入层 + 事件循环（原 cmd/harness/run.go
// 后半段原样迁移，2026-08-14）。输入层：TTY 时 raw mode + 单一读方事件循环
// （Esc 中断 + 审批/提问协调，ADR-028/029/036）；非 TTY / MakeRaw 失败 →
// 不启用审批交互（自动拒绝）、无 Esc 中断（跑完即退）。
func (h *HarnessAgent) runOnce(ctx context.Context) error {
	sess := h.sess
	rc := sess.RuntimeContext()
	sess.AddUser(h.prompt)

	var renderer ui.Output
	if h.jsonOut {
		renderer = ui.JSONRenderer{}
	} else {
		renderer = ui.NewTextRenderer(h.showThinking)
	}
	renderer.Start(sess.Conversation())

	// onEvent 双转发（普通回调，规划文档闭包分类第 4 类）：渲染 + 块级实时
	// 落盘。capture 语义：renderer/sess 均为本函数局部且生命周期覆盖整个
	// Run，闭包按引用捕获安全。
	onEvent := func(ev events.Event) {
		renderer.Event(ev)
		sess.OnAgentEvent(ev) // 块级实时落盘
	}

	// 输入层：TTY 时 raw mode + 单一读方事件循环（Esc 中断 + 审批/提问协调，
	// ADR-028/029/036）；非 TTY / MakeRaw 失败 → 不启用审批交互（自动拒绝）、
	// 无 Esc 中断（跑完即退）。
	fd := int(os.Stdin.Fd())
	var inputCh <-chan ui.InputEvent
	var reqCh chan *ui.ApprovalPrompt
	var askCh chan *ui.AskPrompt
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, old) }()
			inputCh = ui.ReadStdinEvents(os.Stdin, os.Stdout)
			reqCh = make(chan *ui.ApprovalPrompt, 8)
			askCh = make(chan *ui.AskPrompt, 8)
			rc.Approver = ui.NewChannelApprover(reqCh, askCh)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- h.reactAgent.Run(runCtx, rc, onEvent) }()

	var pending *ui.ApprovalPrompt
	var pendingAsk *ui.AskPrompt
	for {
		select {
		case ev, ok := <-inputCh:
			if !ok {
				// stdin EOF（Ctrl+D）：取消本轮，等 Run 退出。
				cancel()
				inputCh = nil
				continue
			}
			// 提问挂起：输入路由为提问答复（选项编号 / 自定义文本 / Esc）。
			if pendingAsk != nil {
				if ev.Esc {
					pendingAsk.Resp <- middleware.AskResult{}
					pendingAsk = nil
					continue
				}
				ans, ok := ui.ParseAskAnswer(ev.Line, pendingAsk.Req)
				if !ok {
					ui.PrintAskUI(pendingAsk.Req)
					continue
				}
				pendingAsk.Resp <- ans
				pendingAsk = nil
				continue
			}
			// 审批挂起：输入路由为审批答复（y/s/n / Esc）。
			if pending != nil {
				if ev.Esc {
					pending.Resp <- middleware.DecisionDeny
					pending = nil
					cancel()
					continue
				}
				line := strings.TrimSpace(ev.Line)
				if line == "" {
					ui.PrintApprovalUI(pending.Req)
					continue
				}
				dec, ok := ui.ParseApprovalDecision(line)
				if !ok {
					fmt.Printf("  无效输入（y/s/n）> ")
					continue
				}
				pending.Resp <- dec
				pending = nil
				continue
			}
			if ev.Esc {
				cancel() // 单轮 Esc/Ctrl+C 中断
			}
			// 普通行忽略（单轮模式无 REPL 命令）。
		case req := <-reqCh:
			pending = req
			ui.PrintApprovalUI(req.Req)
			if h.jsonOut {
				ui.EmitApprovalJSON(req.Req)
			}
		case ask := <-askCh:
			pendingAsk = ask
			ui.PrintAskUI(ask.Req)
		case err := <-runDone:
			if errors.Is(err, context.Canceled) {
				fmt.Println("\n（已中断）")
				return nil
			}
			if err != nil {
				return err
			}
			// run 模式回合末等子（2026-08-19 用户拍板 A+D，仅 run 模式）：
			// 父回合正常结束但子 agent 仍在运行 → 等待（有界）→ 注入完成通知
			// → 再跑一轮让父整合结果收尾；超时取消剩余子后同样收尾。
			// 修复"异步 spawn + 完成注入"机制在单轮 run 下的结构性缺口：
			// 此前父结束回合 = 进程退出 = Manager.Shutdown 取消子，结果丢失。
			return h.runDrainSubagents(runCtx, onEvent)
		}
	}
}

// drainRounds 是回合末等子的最大收尾轮数（防模型在收尾轮继续 spawn 造成的
// 病态循环；正常场景：等子完成 → 一轮收尾即结束）。
const drainRounds = 3

// runDrainSubagents 实现 run 模式回合末"等子"（A 方案，2026-08-19）：
//
//	父回合结束后，若子 agent 仍在运行：
//	  1. WaitAll(h.subagentWait) 等待全部完成（ctx 取消/信号可中断）；
//	     超时 → CancelRunning 取消剩余（finish 落"已中断"+部分结果通知）
//	  2. 用新 rc 再跑一轮（SessionMiddleware 重新 load/save，无状态 agent
//	     ADR-026）：BackgroundCompletionMiddleware 在采样前 drain 队列，
//	     完成通知作为 user 消息注入 → 父整合结果产出最终答案
//	  3. 最多 drainRounds 轮（收尾轮若再 spawn，继续等）
//
// 注意：drain 轮不挂 Approver（自动拒绝回填）——事件循环已退出，channelApprover
// 无读方会死锁；bypass（评测）无影响，交互 run 的 drain 轮审批自动拒绝。
// TUI 不经过本函数（runTUI 独立路径，子跨回合存活由注入 + 唤醒承担）。
func (h *HarnessAgent) runDrainSubagents(ctx context.Context, onEvent events.OnEvent) error {
	for round := 0; round < drainRounds; round++ {
		if h.subagents.RunningCount() == 0 {
			return nil
		}
		if h.subagentWait <= 0 {
			return nil // --subagent-wait 0：不等待（保持旧行为）
		}
		fmt.Fprintln(os.Stderr, "（等待子 agent 完成…）")
		if n := h.subagents.WaitAll(ctx, h.subagentWait); n > 0 {
			// 超时：取消剩余子（finish 三分支 Append 中断通知带部分结果），
			// 再跑一轮让父基于部分结果收尾。取消后收尾等待放宽到 5m——
			// 已取消子的流可能被慢网络卡住，30s 不够落"已中断"通知
			// （2026-08-19 用户反馈）。
			h.subagents.CancelRunning()
			h.subagents.WaitAll(ctx, 5*time.Minute)
		}
		rc := h.sess.RuntimeContext()
		if err := h.reactAgent.Run(ctx, rc, onEvent); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
	return nil
}

// Teardown 拆除装配产物（幂等；Run 内已调用，外部 defer 兜底）。run：关会话
// transcript；TUI/resume：会话已由 tui.App.Close 的 CloseAll 关闭，此处
// no-op。子 agent：Shutdown（cancel 全部 + 等收尾，收尾通知 Append 父 Queue
// ——父 resume 后补注入）。CleanupBackground 仍由 cmd 的 main defer 承担——
// 完整拆除链：WaitRuns→SaveActiveState→CloseAll→CleanupBackground（各段相邻
// 可见）。
func (h *HarnessAgent) Teardown() {
	if h.closed {
		return
	}
	h.closed = true
	if h.subagents != nil {
		h.subagents.Shutdown()
	}
	if h.mode == ModeRun && h.sess != nil {
		_ = h.sess.Close()
	}
}
