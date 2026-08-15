package main

import (
	"flag"
	"strings"

	"github.com/agent-project/harness/internal/app"
)

// runCmd 执行 `harness run <prompt>`：解析 flags → 声明模式与参数 →
// app.Build/Run。装配与执行全在 Composition Root（架构整理 2026-08-14）。
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
	return appCmd(app.Options{
		Mode:              app.ModeRun,
		ConfigPath:        configPath,
		Model:             modelFlag,
		Effort:            effortFlag,
		Thinking:          thinkingFlag,
		NoThinking:        noThinkingFlag,
		NoThinkingDisplay: noThinkingDisplay,
		JSONOut:           jsonOut,
		Prompt:            strings.Join(fs.Args(), " "),
	})
}
