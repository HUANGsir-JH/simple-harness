//go:build !windows

package tools

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// processAliveUnix 用 kill -0 判定进程存活（信号探测不杀进程）。
func processAliveUnix(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// isProcessAlive POSIX 版：kill -0 判定（background_test.go 跨平台用例用）。
func isProcessAlive(pid int) bool { return processAliveUnix(pid) }

// waitForProcessDead 短超时内轮询等待进程死亡。僵尸态修复（审查报告 03）：
// kill -0 对被 cmd.Wait 回收前的僵尸进程仍返回成功（探测的是 PID 表项而非
// 运行状态），瞬时检查会假阳性；轮询直到 kill -0 失败 = 进程被回收。只用于
// "应死亡"断言；"应存活"断言继续用瞬时 isProcessAlive（方向正确，先等死再
// 报活会让短命进程的存活断言变假阳性）。
func waitForProcessDead(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// fgSpawnChildCommand 构造"派生后台进程 + 主命令立即退出"的命令（01 回归
// 锚点：前台命令正常退出后派生进程应存活，终端式语义）。
func fgSpawnChildCommand(pidfile string) string {
	return fmt.Sprintf("nohup sleep 30 & echo $! > %s; echo done", pidfile)
}

// readSpawnedPID 轮询读取派生进程 PID（sh 立即写 pidfile，轮询防慢盘）。
func readSpawnedPID(t *testing.T, pidfile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidfile); err == nil {
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("派生进程未启动（pidfile 无内容）")
	return 0
}

// killPidDirect 直接杀单个进程（01 语义：派生进程不被注册表追踪，测试清理
// 用 syscall 直接杀）。
func killPidDirect(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }
