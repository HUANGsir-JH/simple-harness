package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/ui/tui"
)

// resumeCmd 恢复会话（--last 或 <id>）并进入 TUI 继续（ADR-030）。
func resumeCmd(args []string, jsonOut bool) error {
	if jsonOut {
		return fmt.Errorf("resume 交互模式不支持 --json（请用 `harness --json run <prompt>`）")
	}
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	last := fs.Bool("last", false, "resume the most recent session for this project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := strings.Join(fs.Args(), " ")

	proj, err := findProject()
	if err != nil {
		return err
	}

	var info session.SessionInfo
	if *last {
		var ok bool
		if info, ok = proj.Last(); !ok {
			return fmt.Errorf("resume: 本项目暂无会话（先 `harness run`）")
		}
	} else {
		if id == "" {
			return fmt.Errorf("resume: 需要会话 id 或 --last（`harness sessions` 查看）")
		}
		list, _ := proj.Sessions()
		found := false
		for _, s := range list {
			if s.ID == id {
				info = s
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("resume: 会话 %q 不存在（`harness sessions` 查看）", id)
		}
	}

	sess, err := proj.Resume(info)
	if err != nil {
		return err
	}

	app, err := defaultApp()
	if err != nil {
		return err
	}
	a, err := app.buildAgent()
	if err != nil {
		return err
	}

	// SIGTERM 终止进程；SIGINT（Ctrl+C）由 bubbletea 作为按键事件处理（复制语义）。
	// 回合中断用 Esc（ADR-028），顶层 ctx 不被 SIGINT cancel。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	return tui.RunTUI(a, proj, app.Config, sess, ctx)
}

// sessionsCmd 列出当前项目的会话。
func sessionsCmd(args []string) error {
	proj, err := findProject()
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
		var model, updated string
		if st, err := agentstate.LoadFile(filepath.Join(s.Path, session.FileAgentState)); err == nil {
			model, updated = st.Model, st.UpdatedAt
		}
		fmt.Printf("%s  model=%s  updated=%s\n", s.ID, model, updated)
	}
	return nil
}

// findProject 定位当前项目桶（New + Getwd + FindProject）。
func findProject() (*session.Project, error) {
	store, err := session.New()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return store.FindProject(cwd)
}
