package main

import (
	"flag"
	"fmt"

	"github.com/agent-project/harness/internal/app"
)

// webCmd 执行 `harness web`：启动本地 Web UI 服务（承载 TUI 全部功能）。
// 解析 flags → 声明模式与参数 → app.Build/Run（装配与执行全在 Composition
// Root，架构整理 2026-08-14）。
func webCmd(args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	host := fs.String("host", "127.0.0.1", "bind address (default 127.0.0.1)")
	port := fs.Int("port", 8080, "listen port (default 8080)")
	noThinkingDisplay := fs.Bool("no-thinking-display", false, "do not show thinking text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("web: 不接受位置参数（用法：harness web [--host <addr>] [--port <n>]）")
	}
	return appCmd(app.Options{
		Mode:              app.ModeWeb,
		WebHost:           *host,
		WebPort:           *port,
		NoThinkingDisplay: *noThinkingDisplay,
	})
}
