package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/session"
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
type HarnessAgent struct {
	mode Mode
	cfg  *App // 配置装配（进程级共享）

	// reactAgent 是基础 ReAct agent（无状态，可被多会话/多 goroutine 共享）。
	reactAgent *agent.Agent

	proj *session.Project
	sess *session.Session // run/resume 已建/已载；ModeTUI 新入口 nil（懒加载）

	// newSession 是 ModeTUI 的懒加载创建器（方法值 defaultNewSession）；
	// ModeResume 传 nil（不触发）。
	newSession func() (*session.Session, error)

	// run 模式执行参数。
	prompt       string
	jsonOut      bool
	showThinking bool

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
	t := tui.Assemble(h.reactAgent, h.proj, h.cfg.Config, h.sess, h.newSession, ctx, h.showThinking)
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
			return err
		}
	}
}

// Teardown 拆除装配产物（幂等；Run 内已调用，外部 defer 兜底）。run：关会话
// transcript；TUI/resume：会话已由 tui.App.Close 的 CloseAll 关闭，此处
// no-op。CleanupBackground 仍由 cmd 的 main defer 承担——完整拆除链：
// WaitRuns→SaveActiveState→CloseAll→CleanupBackground（各段相邻可见）。
func (h *HarnessAgent) Teardown() {
	if h.closed {
		return
	}
	h.closed = true
	if h.mode == ModeRun && h.sess != nil {
		_ = h.sess.Close()
	}
}
