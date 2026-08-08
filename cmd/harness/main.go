package main

import (
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
}

// run 分发子命令。配置加载不在本层：需要配置的命令经 defaultApp/loadApp
// 惰性初始化一次（ADR-026，见 runtime.go）。
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
	fmt.Println("  harness                       interactive mode (REPL, multi-turn)")
	fmt.Println("  harness run <prompt>          run a single turn with the configured model")
	fmt.Println("  harness resume <id>|--last    resume a session and continue in REPL")
	fmt.Println("  harness sessions              list sessions for this project")
	fmt.Println("  harness version               print version")
	fmt.Println("  harness help                  show this help")
	fmt.Println()
	fmt.Println("flags:")
	fmt.Println("  --json                       emit machine-readable events as JSONL")
	fmt.Println("  --model <name>               model to use (default: first model of default provider)")
	fmt.Println("  --effort <low|high|max>      reasoning effort override (must be in the model's thinking.efforts)")
	fmt.Println("  --thinking                   force enable thinking (default: model config)")
	fmt.Println("  --no-thinking                force disable thinking (default: model config)")
	fmt.Println("  --no-thinking-display        do not show thinking text (only affects text renderer)")
	fmt.Println()
	fmt.Println("REPL commands:")
	fmt.Println("  /switch <id>|--last          switch to another session (in-process resume)")
	fmt.Println("  /model <name>                switch model for the active session")
	fmt.Println("  /effort <low|high|max>       switch reasoning effort for the active session")
	fmt.Println()
	fmt.Println("config: project config.local.yaml or ~/.harness/config.yaml")
	fmt.Println("  default_provider + providers.<name>.{base_url, api_key, models} (anthropic wire)")
}
