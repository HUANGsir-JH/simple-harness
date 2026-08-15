package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/app"
	"github.com/agent-project/harness/internal/session"
)

// resumeCmd 恢复会话（--last 或 <id>）并进入 TUI 继续（ADR-030）：解析
// flags → 声明模式与参数 → app.Build/Run（架构整理 2026-08-14）。
func resumeCmd(args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	last := fs.Bool("last", false, "resume the most recent session for this project")
	noThinkingDisplay := fs.Bool("no-thinking-display", false, "do not show thinking text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return appCmd(app.Options{
		Mode:              app.ModeResume,
		ResumeID:          strings.Join(fs.Args(), " "),
		ResumeLast:        *last,
		NoThinkingDisplay: *noThinkingDisplay,
		JSONOut:           jsonOut,
	})
}

// sessionsCmd 列出当前项目的会话（只读列表，不需要装配）。
func sessionsCmd(args []string) error {
	proj, err := session.ProjectForCWD()
	if err != nil {
		return err
	}
	list, err := proj.Sessions()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("（本项目暂无会话，先 `harness run`）")
		return nil
	}
	for _, s := range list {
		fmt.Printf("%s  name=%s  model=%s  updated=%s\n", s.ID, s.Name, s.Model, s.UpdatedAt)
	}
	return nil
}
