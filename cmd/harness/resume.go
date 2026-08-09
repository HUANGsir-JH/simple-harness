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
	"github.com/agent-project/harness/internal/ui"
)

// resumeCmd 恢复会话（--last 或 <id>）并进入 REPL 继续。
func resumeCmd(args []string, jsonOut bool) error {
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

	// SIGTERM 终止进程；SIGINT（Ctrl+C）作为字节由 raw mode 捕获 → 只中断当前
	// 回合（不终止 REPL）。顶层 ctx 不被 SIGINT cancel，下一轮 Run 不受影响。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	mgr := &SessionManager{
		app:    app,
		a:      a,
		proj:   proj,
		open:   map[string]*session.Session{sess.ID: sess},
		active: sess,
	}
	defer mgr.closeAll()

	var renderer ui.Output
	if jsonOut {
		renderer = ui.JSONRenderer{}
	} else {
		renderer = ui.NewTextRenderer(true)
	}
	return runREPL(ctx, mgr, renderer)
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
