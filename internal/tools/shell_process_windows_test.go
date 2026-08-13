//go:build windows

package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// spawnChildPowerShell 构造"起孙进程（写 pidfile）+ 主进程挂起"的 PowerShell
// 命令：用于验证 job 杀树能把 PowerShell 派生的孙进程一并杀掉（用户痛点：
// 模型前台起 python 服务，超时/Esc 后服务进程残留）。
func spawnChildPowerShell(pidfile string) string {
	return fmt.Sprintf(
		"Start-Process powershell -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 60' -PassThru | Select-Object -ExpandProperty Id | Set-Content '%s'; Start-Sleep -Seconds 60",
		pidfile)
}

// readChildPID 轮询 pidfile 读取孙进程 PID（PowerShell 冷启动 ~1s）。
func readChildPID(t *testing.T, pidfile string) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidfile); err == nil {
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("孙进程未启动（pidfile 无内容）")
	return 0
}

// processAliveWindows 用 tasklist 判定进程存活：有匹配进程时输出含 PID 行；
// 无匹配只输出 "INFO: No tasks are running..."。
func processAliveWindows(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid)).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf("%d", pid))
}

// isProcessAlive Windows 版：tasklist 判定（background_test.go 跨平台用例用）。
func isProcessAlive(pid int) bool { return processAliveWindows(pid) }

// TestWindowsJobKillsTree 直测 job 封装：主进程 + 派生的孙进程在
// killProcessTree 后全部死亡（Windows 上进程入 job 后其新建子进程自动归属
// 同一 job，TerminateJobObject 原子杀全树）。
// job 创建/attach 失败（嵌套 job 限制，CI/IDE 启动器）时 skip——本机正常环境
// 应全绿。
func TestWindowsJobKillsTree(t *testing.T) {
	tree, err := createProcessTree()
	if err != nil {
		t.Skipf("job 不可用（嵌套 job 限制）: %v", err)
	}
	defer closeProcessTree(tree)

	pidfile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", spawnChildPowerShell(pidfile))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cmd.Wait()
	if err := attachProcessTree(tree, cmd.Process); err != nil {
		t.Skipf("attach 失败（嵌套 job 限制）: %v", err)
	}
	childPID := readChildPID(t, pidfile)

	killProcessTree(tree, cmd.Process.Pid)
	time.Sleep(200 * time.Millisecond) // tasklist 刷新窗口
	if isProcessAlive(cmd.Process.Pid) {
		t.Errorf("主进程 %d 仍存活（job 未杀根）", cmd.Process.Pid)
	}
	if isProcessAlive(childPID) {
		t.Errorf("孙进程 %d 仍存活（job 未杀全树）", childPID)
	}
}

// TestShellTimeoutTransfersBackgroundWindows 验证超时转后台（ADR-038 扩展）：
// 超时后整棵树（含孙进程）**存活**（进程移交注册表继续跑，不杀）——此前
// 语义是杀树；Esc 仍是杀树（见 TestShellEscKillsTreeWindows）。退出清理
// （CleanupBackground）与 kill_pid 可终止。
func TestShellTimeoutTransfersBackgroundWindows(t *testing.T) {
	t.Cleanup(CleanupBackground)
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	_, err := call(ShellCommandTool{}, map[string]any{
		"command":    spawnChildPowerShell(pidfile),
		"timeout_ms": 3000, // 孙进程启动 ~1s 后超时，树已成型
	})
	wantRespondToModel(t, err, "timeout transfer")
	if !strings.Contains(err.Error(), "已自动转入后台") {
		t.Fatalf("应提示转后台: %s", err)
	}
	childPID := readChildPID(t, pidfile)
	time.Sleep(200 * time.Millisecond)
	if !isProcessAlive(childPID) {
		t.Errorf("超时转后台后孙进程 %d 应存活（不杀树）", childPID)
	}
	// 退出清理（或 kill_pid）可杀：验证句柄移交注册表生效。
	CleanupBackground()
	time.Sleep(200 * time.Millisecond)
	if isProcessAlive(childPID) {
		t.Errorf("CleanupBackground 后孙进程 %d 应死亡", childPID)
	}
}

// TestShellEscKillsTreeWindows 验证痛点②回归：Esc（ctx 取消）后整棵树
// （含孙进程）被杀，且回填"命令已被中断"提示。
func TestShellEscKillsTreeWindows(t *testing.T) {
	t.Cleanup(CleanupBackground)
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(3 * time.Second) // 等孙进程启动后取消
		cancel()
	}()
	_, err := callWithCtx(ctx, map[string]any{"command": spawnChildPowerShell(pidfile)})
	wantRespondToModel(t, err, "esc tree")
	if !strings.Contains(err.Error(), "命令已被中断") {
		t.Errorf("应回填中断提示: %s", err)
	}
	childPID := readChildPID(t, pidfile)
	time.Sleep(200 * time.Millisecond)
	if isProcessAlive(childPID) {
		t.Errorf("Esc 后孙进程 %d 仍存活（杀树失效）", childPID)
	}
}
