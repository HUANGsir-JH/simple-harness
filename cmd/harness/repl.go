package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/ui/tui"
	"golang.org/x/term"
)

// repl 是交互式模式（`harness` 无子命令）：新会话 + TUI（bubbletea 全屏，ADR-030）。
// 非 TTY / --json 下报错提示用 run（TUI 需要真实终端，--json 仅 run 支持）。
// REPL 已删除（W5，ADR-030）：TUI 是唯一交互模式。
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
	proj, err := findProject()
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

	return tui.RunTUI(a, proj, app.Config, sess, ctx)
}
