package impl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agentsmd"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
)

// TestAgentsMdMiddleware 验证 AGENTS.md 注入：追加到 current、无内容透传、
// 读 rc.State.CWD。
func TestAgentsMdMiddleware(t *testing.T) {
	root := t.TempDir()
	// 建 .git 标记 + 项目 AGENTS.md。
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("项目 doc"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := AgentsMdMiddleware{Options: agentsmd.Options{}}

	t.Run("追加到 current", func(t *testing.T) {
		rc := middleware.NewRuntimeContext()
		rc.State = agentstate.New("s1", "m", root)
		got, err := m.OnSystemPrompt(context.Background(), rc, "BASE")
		if err != nil {
			t.Fatalf("OnSystemPrompt: %v", err)
		}
		if !strings.HasPrefix(got, "BASE\n\n") || !strings.Contains(got, "项目 doc") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("空 current 原样注入", func(t *testing.T) {
		rc := middleware.NewRuntimeContext()
		rc.State = agentstate.New("s1", "m", root)
		got, err := m.OnSystemPrompt(context.Background(), rc, "")
		if err != nil {
			t.Fatalf("OnSystemPrompt: %v", err)
		}
		if !strings.Contains(got, "项目 doc") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("无内容透传", func(t *testing.T) {
		rc := middleware.NewRuntimeContext()
		rc.State = agentstate.New("s1", "m", t.TempDir())
		got, err := m.OnSystemPrompt(context.Background(), rc, "BASE")
		if err != nil {
			t.Fatalf("OnSystemPrompt: %v", err)
		}
		if got != "BASE" {
			t.Fatalf("got %q want BASE", got)
		}
	})

	t.Run("State 为空回退进程 cwd 不 panic", func(t *testing.T) {
		got, err := m.OnSystemPrompt(context.Background(), middleware.NewRuntimeContext(), "BASE")
		if err != nil {
			t.Fatalf("OnSystemPrompt: %v", err)
		}
		// nil State → workspaceOf 返回 "" → agentsmd 回退 os.Getwd()。
		// 测试进程 cwd 可能含 AGENTS.md/CLAUDE.md，故只断言不 panic 且保留 current 前缀。
		if !strings.HasPrefix(got, "BASE") {
			t.Fatalf("got %q，应保留 BASE 前缀", got)
		}
	})
}
