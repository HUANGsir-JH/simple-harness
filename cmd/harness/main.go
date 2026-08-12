package main

import (
	"fmt"
	"os"
)

// version 是 harness 版本号。每次有用户可见变更（功能 → minor、修复 → patch）
// 随提交 bump，`harness version` 输出。
const version = "0.8.1"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
}

// run 分发子命令。配置加载不在本层：需要配置的命令经 app.Load/LoadFrom
// 惰性初始化一次（进程级单例，见 internal/app）。
func run(args []string) error {
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
		return repl(jsonOut) // 直接 harness（无子命令）→ 交互式
	}
	if len(rest) == 1 && rest[0] == "--no-thinking-display" {
		return repl(jsonOut, false)
	}
	switch rest[0] {
	case "version":
		fmt.Printf("harness %s\n", version)
		return nil
	case "run":
		return runCmd(rest[1:], jsonOut)
	case "resume":
		return resumeCmd(rest[1:], jsonOut)
	case "sessions":
		return sessionsCmd(rest[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (try `harness help`)", rest[0])
	}
}

func usage() {
	fmt.Println("harness: minimal agent harness")
	fmt.Println("usage:")
	fmt.Println("  harness                       interactive mode (TUI, multi-turn)")
	fmt.Println("  harness run <prompt>          run a single turn with the configured model")
	fmt.Println("  harness resume <id>|--last    resume a session and continue in TUI")
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
