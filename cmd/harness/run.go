package main

import (
	"flag"
	"strings"
	"time"

	"github.com/agent-project/harness/internal/app"
)

// runCmd 执行 `harness run <prompt>`：解析 flags → 声明模式与参数 →
// app.Build/Run。装配与执行全在 Composition Root（架构整理 2026-08-14）。
func runCmd(args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var configPath, modelFlag, effortFlag string
	var thinkingFlag, noThinkingFlag, noThinkingDisplay bool
	var maxTurns int
	var subagentWait time.Duration
	fs.StringVar(&configPath, "config", "", "path to config file (default ~/.harness/config.yaml)")
	fs.StringVar(&modelFlag, "model", "", "model to use (must be defined in the selected provider; default: first model)")
	fs.StringVar(&effortFlag, "effort", "", "reasoning effort override (low|high|max; must be in the model's thinking.efforts)")
	fs.BoolVar(&thinkingFlag, "thinking", false, "force enable thinking (default: model config)")
	fs.BoolVar(&noThinkingFlag, "no-thinking", false, "force disable thinking (default: model config)")
	fs.BoolVar(&noThinkingDisplay, "no-thinking-display", false, "do not show thinking text")
	fs.IntVar(&maxTurns, "max-turns", 0, "max sampling rounds per turn (0 = unlimited; eval use)")
	fs.DurationVar(&subagentWait, "subagent-wait", 5*time.Minute, "wait for running subagents at turn end (0 = don't wait; run mode)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return appCmd(app.Options{
		Mode:              app.ModeRun,
		ConfigPath:        configPath,
		Model:             modelFlag,
		Effort:            effortFlag,
		Thinking:          thinkingFlag,
		NoThinking:        noThinkingFlag,
		NoThinkingDisplay: noThinkingDisplay,
		MaxTurns:          maxTurns,
		SubagentWait:      subagentWait,
		JSONOut:           jsonOut,
		Prompt:            strings.Join(fs.Args(), " "),
	})
}
