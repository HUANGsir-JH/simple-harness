package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/session"
	"golang.org/x/term"
)

// boolPtr 构造 bool 指针（--thinking/--no-thinking 覆盖会话 state 用）。
func boolPtr(b bool) *bool { return &b }

// runCmd 执行 `harness run <prompt>`：解析 flags → 建会话 → 应用覆盖 → 单轮 Run。
func runCmd(args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var configPath, modelFlag, effortFlag string
	var thinkingFlag, noThinkingFlag, noThinkingDisplay bool
	fs.StringVar(&configPath, "config", "", "path to config file (default ~/.harness/config.yaml)")
	fs.StringVar(&modelFlag, "model", "", "model to use (must be defined in the selected provider; default: first model)")
	fs.StringVar(&effortFlag, "effort", "", "reasoning effort override (low|high|max; must be in the model's thinking.efforts)")
	fs.BoolVar(&thinkingFlag, "thinking", false, "force enable thinking (default: model config)")
	fs.BoolVar(&noThinkingFlag, "no-thinking", false, "force disable thinking (default: model config)")
	fs.BoolVar(&noThinkingDisplay, "no-thinking-display", false, "do not show thinking text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		return fmt.Errorf("run: prompt is required (harness run \"your prompt\"; 不带参数运行 `harness` 进入交互式)")
	}

	var app *App
	var err error
	if configPath != "" {
		app, err = loadApp(configPath)
	} else {
		app, err = defaultApp()
	}
	if err != nil {
		return err
	}
	res, err := app.resolveFlags(modelFlag, effortFlag, thinkingFlag, noThinkingFlag)
	if err != nil {
		return err
	}
	a, err := app.buildAgent()
	if err != nil {
		return err
	}

	sess, err := session.CreateInCWD(res.Model, app.defaultApprovalMode())
	if err != nil {
		return err
	}
	defer sess.Close()
	// flags → 会话 state（随 SessionMiddleware 落盘，resume 可恢复）。
	if thinkingFlag {
		if err := sess.SetThinkingEnabled(boolPtr(true)); err != nil {
			return err
		}
	}
	if noThinkingFlag {
		if err := sess.SetThinkingEnabled(boolPtr(false)); err != nil {
			return err
		}
	}
	if effortFlag != "" {
		if err := sess.SetThinkingEffort(effortFlag); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rc := sess.RuntimeContext()
	sess.AddUser(prompt)

	var renderer output
	if jsonOut {
		renderer = jsonRenderer{}
	} else {
		renderer = newTextRenderer(!noThinkingDisplay)
	}
	renderer.start(sess.Conversation())

	onEvent := func(ev agent.Event) {
		renderer.event(ev)
		sess.OnAgentEvent(ev) // 块级实时落盘
	}

	// 输入层：TTY 时 raw mode + 单一读方事件循环（Esc 中断 + 审批协调，
	// ADR-028/029）；非 TTY / MakeRaw 失败 → 不启用审批交互（自动拒绝）、
	// 无 Esc 中断（跑完即退）。
	fd := int(os.Stdin.Fd())
	var inputCh <-chan inputEvent
	var reqCh chan *approvalRequest
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, old) }()
			inputCh = readStdinEvents(os.Stdin, os.Stdout)
			reqCh = make(chan *approvalRequest, 8)
			rc.Approver = newChannelApprover(reqCh)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(runCtx, rc, onEvent) }()

	var pending *approvalRequest
	for {
		select {
		case ev, ok := <-inputCh:
			if !ok {
				// stdin EOF（Ctrl+D）：取消本轮，等 Run 退出。
				cancel()
				inputCh = nil
				continue
			}
			// 审批挂起：输入路由为审批答复（y/s/n / Esc）。
			if pending != nil {
				if ev.esc {
					pending.resp <- middleware.DecisionDeny
					pending = nil
					cancel()
					continue
				}
				line := strings.TrimSpace(ev.line)
				if line == "" {
					printApprovalUI(pending.req)
					continue
				}
				dec, ok := parseApprovalDecision(line)
				if !ok {
					fmt.Printf("  无效输入（y/s/n）> ")
					continue
				}
				pending.resp <- dec
				pending = nil
				continue
			}
			if ev.esc {
				cancel() // 单轮 Esc/Ctrl+C 中断
			}
			// 普通行忽略（runCmd 无 REPL 命令）。
		case req := <-reqCh:
			pending = req
			printApprovalUI(req.req)
			if jsonOut {
				emitApprovalJSON(req.req)
			}
		case err := <-runDone:
			if errors.Is(err, context.Canceled) {
				fmt.Println("\n（已中断）")
				return nil
			}
			return err
		}
	}
}
