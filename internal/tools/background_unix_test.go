//go:build !windows

package tools

import "syscall"

// processAliveUnix 用 kill -0 判定进程存活（信号探测不杀进程）。
func processAliveUnix(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// isProcessAlive POSIX 版：kill -0 判定（background_test.go 跨平台用例用）。
func isProcessAlive(pid int) bool { return processAliveUnix(pid) }
