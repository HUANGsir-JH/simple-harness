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
	"github.com/agent-project/harness/internal/app"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/ui"
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

	var rt *app.App
	var err error
	if configPath != "" {
		rt, err = app.LoadFrom(configPath)
	} else {
		rt, err = app.Load()
	}
	if err != nil {
		return err
	}
	res, err := rt.ResolveFlags(modelFlag, effortFlag, thinkingFlag, noThinkingFlag)
	if err != nil {
		return err
	}
	// 用 res（--model/--effort 覆盖后的生效配置）装配 agent：client 的 thinking
	// effort 与请求模型必须同源，否则 --model 指定的模型会带上默认模型的配置
	// （Bug04，2026-08-10 审查证实 thinking 泄漏）。
	a, err := agent.Build(res, rt.DefaultApprovalMode())
	if err != nil {
		return err
	}

	sess, err := session.CreateInCWD(res.Model, rt.DefaultApprovalMode())
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

	var renderer ui.Output
	if jsonOut {
		renderer = ui.JSONRenderer{}
	} else {
		renderer = ui.NewTextRenderer(!noThinkingDisplay)
	}
	renderer.Start(sess.Conversation())

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
	go func() { runDone <- a.Run(runCtx, rc, onEvent) }()

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
			// 普通行忽略（runCmd 无 REPL 命令）。
		case req := <-reqCh:
			pending = req
			ui.PrintApprovalUI(req.Req)
			if jsonOut {
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
