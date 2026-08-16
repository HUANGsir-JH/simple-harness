package main

import (
	"fmt"
	"os"

	"github.com/agent-project/harness/internal/app"
	"github.com/agent-project/harness/internal/tools"
)

// version 是 harness 版本号。每次有用户可见变更（功能 → minor、修复 → patch）
// 随提交 bump，`harness version` 输出。
const version = "0.13.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
}

// run 分发子命令。装配不在本层：各命令只解析 flags → 声明 app.Options →
// appCmd（app.Build → HarnessAgent.Run，Composition Root 见 internal/app，
// 架构整理 2026-08-14）。
// defer CleanupBackground：进程退出前杀光全部 background shell 进程树
// （ADR-038 退出 pre-kill；Windows 另有 KILL_ON_JOB_CLOSE 内核兜底，
// SIGKILL/crash 也能清树）——覆盖 run/resume/TUI 全部子命令。
func run(args []string) error {
	defer tools.CleanupBackground()
	// 在子命令分发前预先扫描全局 --json 参数，使
	// `harness --json run` 与 `harness run --json` 都可用。
	jsonOut := false
	rest := args
	for i, a := range args {
		if a == "--json" {
			jsonOut = true
			rest = append(rest[:i], rest[i+1:]...)
			break
		}
	}

	if len(rest) == 0 {
		return appCmd(app.Options{Mode: app.ModeTUI}) // 直接 harness（无子命令）→ 交互式
	}
	if len(rest) == 1 && rest[0] == "--no-thinking-display" {
		return appCmd(app.Options{Mode: app.ModeTUI, NoThinkingDisplay: true})
	}
	switch rest[0] {
	case "version":
		fmt.Printf("harness %s\n", version)
		return nil
	case "run":
		return runCmd(rest[1:], jsonOut)
	case "resume":
		return resumeCmd(rest[1:], jsonOut)
	case "init":
		return initCmd(rest[1:])
	case "sessions":
		return sessionsCmd(rest[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (try `harness help`)", rest[0])
	}
}

// appCmd 执行一个已声明模式的命令：装配与执行全在 app.Build/Run 内
// （Composition Root，架构整理 2026-08-14）。
func appCmd(o app.Options) error {
	h, err := app.Build(o)
	if err != nil {
		return err
	}
	return h.Run()
}

func usage() {
	fmt.Println("harness: minimal agent harness")
	fmt.Println("usage:")
	fmt.Println("  harness                       interactive mode (TUI, multi-turn)")
	fmt.Println("  harness run <prompt>          run a single turn with the configured model")
	fmt.Println("  harness resume <id>|--last    resume a session and continue in TUI")
	fmt.Println("  harness init                  initialize ~/.harness workspace (skeleton + config template)")
	fmt.Println("  harness sessions              list sessions for this project")
	fmt.Println("  harness version               print version")
	fmt.Println("  harness help                  show this help")
	fmt.Println()
	fmt.Println("flags:")
	fmt.Println("  --json                       emit machine-readable events as JSONL (run only)")
	fmt.Println("  --model <name>               model to use (default: first model of default provider)")
	fmt.Println("  --effort <low|high|max>      reasoning effort override (must be in the model's thinking.efforts)")
	fmt.Println("  --thinking                   force enable thinking (default: model config)")
	fmt.Println("  --no-thinking                force disable thinking (default: model config)")
	fmt.Println("  --no-thinking-display        do not show thinking text")
	fmt.Println()
	fmt.Println("TUI commands:")
	fmt.Println("  /switch /model /effort /permission /thinking    popup pickers (real-time config lists)")
	fmt.Println("  /compact                              compress context (LLM summary)")
	fmt.Println("  /help /exit")
	fmt.Println()
	fmt.Println("config: project config.local.yaml or ~/.harness/config.yaml")
	fmt.Println("  default_provider + providers.<name>.{base_url, api_key, models} (anthropic wire)")
}
