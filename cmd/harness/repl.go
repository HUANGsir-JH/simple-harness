package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/ui"
	"github.com/agent-project/harness/internal/ui/tui"
	"golang.org/x/term"
)

// repl 是交互式模式（`harness` 无子命令）：新会话 + TUI（bubbletea 全屏，ADR-030）。
// 非 TTY / --json 下报错提示用 run（TUI 需要真实终端，--json 仅 run 支持）。
func repl(jsonOut bool) error {
	if jsonOut {
		return fmt.Errorf("交互模式不支持 --json（请用 `harness --json run <prompt>`）")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("交互模式需要终端（TUI 全屏），请用 `harness run <prompt>`（非交互单轮）")
	}

	app, err := defaultApp()
	if err != nil {
		return err
	}
	a, err := app.buildAgent()
	if err != nil {
		return err
	}
	sess, err := session.CreateInCWD(app.Resolved.Model, app.defaultApprovalMode())
	if err != nil {
		return err
	}
	defer sess.Close()

	// SIGTERM 终止进程；SIGINT（Ctrl+C）由 bubbletea 作为按键事件处理（复制语义）。
	// 回合中断用 Esc（ADR-028），顶层 ctx 不被 SIGINT cancel。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	return tui.RunTUI(a, sess, ctx)
}

// runREPL 是交互式 REPL 循环（`harness` / resume 复用）。输入改事件循环：
// 单一读方 goroutine 逐字节读 stdin（raw mode 下 Esc/Ctrl+C 可实时捕获），
// 经 channel 分发；主循环 select 输入事件 / 回合完成。
//
// 中断语义（ADR-028）：Esc(0x1b)/Ctrl+C(0x03) → cancel 当前回合的 runCtx
// （只中断本轮，下一轮新建 ctx 不受影响）；中断后 AddUser 一条系统提示落盘，
// resume 后模型可见"上一轮被中断"。exit/quit 退出。
func runREPL(ctx context.Context, m *SessionManager, renderer ui.Output) error {
	fmt.Println("harness 交互式模式（exit/quit 退出；Esc/Ctrl+C 中断当前回合；/switch <id> /model <name> /effort <level> /permission <readonly|acceptedit|bypass>）")
	renderer.Start(m.active.Conversation())

	// raw mode：让 Esc 作为字节可被实时读取（不依赖行缓冲回车）。非 TTY
	// （重定向/管道）或 MakeRaw 失败 → 降级普通读行（无 Esc 中断，行为同前）。
	fd := int(os.Stdin.Fd())
	var echo io.Writer = io.Discard
	var reqCh chan *ui.ApprovalPrompt // nil = 不启用审批交互（非 TTY → 自动拒绝）
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, old) }()
			echo = os.Stdout // raw mode 无回显，需自行回显用户输入
			reqCh = make(chan *ui.ApprovalPrompt, 8)
		}
	}
	approver := ui.NewChannelApprover(reqCh)
	inputCh := ui.ReadStdinEvents(os.Stdin, echo)
	_, isJSON := renderer.(ui.JSONRenderer)

	var runDone chan error
	var cancelRun context.CancelFunc
	running := false
	var pending *ui.ApprovalPrompt // 非 nil = 审批挂起，下一行输入路由为审批答复

	fmt.Print("> ")
	for {
		select {
		case ev, ok := <-inputCh:
			if !ok {
				return nil // stdin EOF（Ctrl+D）
			}
			// 审批挂起：输入路由为审批答复（y/s/n / Esc），不当作 REPL 命令。
			if pending != nil {
				if ev.Esc {
					// Esc：拒绝当前审批 + 中断当前回合。
					pending.Resp <- middleware.DecisionDeny
					pending = nil
					if running && cancelRun != nil {
						cancelRun()
					}
					continue
				}
				line := strings.TrimSpace(ev.Line)
				if line == "" {
					ui.PrintApprovalUI(pending.Req) // 空行重提示
					continue
				}
				dec, ok := ui.ParseApprovalDecision(line)
				if !ok {
					fmt.Printf("  无效输入（y/s/n）> ")
					continue
				}
				pending.Resp <- dec
				pending = nil
				fmt.Print("> ")
				continue
			}
			if ev.Esc {
				if running && cancelRun != nil {
					cancelRun() // 中断当前回合；结果经 runDone 分支处理
				}
				continue
			}
			line := strings.TrimSpace(ev.Line)
			if line == "" {
				continue
			}
			if line == "exit" || line == "quit" {
				return nil
			}
			if cmd, ok := parseCommand(line); ok {
				if err := m.handleCommand(cmd); err != nil {
					fmt.Fprintf(os.Stderr, "harness: %v\n", err)
				}
				fmt.Print("> ")
				continue
			}
			if running {
				fmt.Println("（上一回合仍在运行，按 Esc 中断）")
				fmt.Print("> ")
				continue
			}
			// 每轮新建 rc（无状态 agent：会话状态经 rc 传入；切换会话下一轮自动生效）。
			rc := m.active.RuntimeContext()
			rc.Approver = approver // 审批交互注入（reqCh nil 时 approver 为 nil → 自动拒绝）
			m.active.AddUser(line)
			runCtx, cancel := context.WithCancel(ctx)
			cancelRun = cancel
			running = true
			runDone = make(chan error, 1)
			onEvent := func(ev agent.Event) {
				renderer.Event(ev)
				m.active.OnAgentEvent(ev)
			}
			go func() { runDone <- m.a.Run(runCtx, rc, onEvent) }()
		case req := <-reqCh:
			// 新审批请求（并行工具可同时多个，channel 缓冲排队逐个处理）。
			pending = req
			ui.PrintApprovalUI(req.Req)
			if isJSON {
				ui.EmitApprovalJSON(req.Req)
			}
		case err := <-runDone:
			running = false
			cancelRun = nil
			runDone = nil
			switch {
			case errors.Is(err, context.Canceled):
				fmt.Println("\n（已中断本轮）")
				// 中断提示落盘（ADR-028）：resume 后模型可见，对齐 Claude Code。
				m.active.AddUser("（系统：上一轮 agent 运行被用户中断。如有未完成的工作，请继续；后台进程可能仍在运行。）")
			case err != nil:
				fmt.Fprintf(os.Stderr, "\nharness: %v\n", err)
			}
			fmt.Print("> ")
		}
	}
}
