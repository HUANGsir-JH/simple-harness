//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestShellTimeoutTransfersBackgroundUnix 验证超时转后台（ADR-038 勘误）：
// 超时后进程组**存活**（不杀树），kill_pid 可杀整组（含派生孙进程）。仅
// POSIX（Windows 对应 TestShellTimeoutTransfersBackgroundWindows）。
//
// 历史（审查报告 04）：本测试原名 TestShellTimeoutKillsProcessGroup，断言
// "超时杀进程组"——勘误把超时语义改为转后台后 Windows 侧已反转，POSIX 侧
// 漏了；此处补齐。前台 sleep 用 30s 而非 5s：sh 存活期间注册表条目才在，
// kill_pid 才有可杀对象（进程自然退出后条目自动注销，见 02 修复）。
func TestShellTimeoutTransfersBackgroundUnix(t *testing.T) {
	t.Cleanup(CleanupBackground)
	pidFile := filepath.Join(t.TempDir(), "bg.pid")
	// 后台 sleep 100 记录 PID + 前台 sleep 30 使命令挂起至超时（300ms）。
	command := fmt.Sprintf("nohup sleep 100 & echo $! > %s; sleep 30", pidFile)
	b, _ := json.Marshal(map[string]any{"command": command, "timeout_ms": 300})
	_, err := ShellCommandTool{}.Handle(context.Background(), middleware.NewRuntimeContext(), "c1", b)
	wantRespondToModel(t, err, "timeout transfer")
	msg := err.Error()
	if !strings.Contains(msg, "已自动转入后台") {
		t.Fatalf("应提示转后台: %s", msg)
	}
	var pid int
	if _, scanErr := fmt.Sscanf(msg, "shell_command: 命令运行超过 300ms，已自动转入后台：PID %d", &pid); scanErr != nil || pid <= 0 {
		t.Fatalf("无法从消息提取 PID: %q", msg)
	}
	// 超时后派生进程应存活（不杀树）。
	child := readSpawnedPID(t, pidFile)
	if !isProcessAlive(child) {
		t.Errorf("超时转后台后派生进程 %d 应存活", child)
	}
	// kill_pid 杀整组（sh + nohup sleep）。
	r, err := call(ShellCommandTool{}, map[string]any{"kill_pid": pid})
	if err != nil || !r.Success {
		t.Fatalf("kill_pid: %v %v", r, err)
	}
	if !waitForProcessDead(child, 2*time.Second) {
		t.Errorf("kill_pid 后派生进程 %d 应死亡", child)
	}
}
