//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/middleware"
)

// TestApplyProcessGroup 验证 POSIX 命令设置了 Setpgid（成为进程组 leader）。
func TestApplyProcessGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "true")
	applyProcessGroup(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("Setpgid 未设置: %+v", cmd.SysProcAttr)
	}
}

// TestShellTimeoutKillsProcessGroup 验证超时杀掉进程组（Bug06(b)）：后台派生
// 进程（nohup & 的 sleep）不残留孤儿。仅 POSIX（Windows PowerShell 无进程组）。
func TestShellTimeoutKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "bg.pid")
	// 后台 sleep 100 记录 PID + 前台 sleep 5 使命令挂起至超时（300ms）。
	command := fmt.Sprintf("nohup sleep 100 & echo $! > %s; sleep 5", pidFile)
	b, _ := json.Marshal(map[string]any{"command": command, "timeout_ms": 300})
	_, err := ShellCommandTool{}.Handle(context.Background(), middleware.NewRuntimeContext(), "c1", b)
	if err == nil {
		t.Fatal("命令应超时")
	}
	time.Sleep(100 * time.Millisecond) // 等 kill 生效
	data, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("读 pid 文件: %v", rerr)
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		t.Fatal("后台进程 PID 未记录")
	}
	// kill -0：进程已被杀则返回非零。
	if cmd := exec.Command("kill", "-0", pid); cmd.Run() == nil {
		t.Errorf("后台子进程残留（进程组未被杀）: pid=%s", pid)
	}
}
