package tools

import (
	"runtime"
	"strings"
	"testing"
)

func isWindows() bool { return runtime.GOOS == "windows" }

// TestShellCommandEcho 验证命令输出捕获（平台分派 cmd/sh 都能跑 echo）。
func TestShellCommandEcho(t *testing.T) {
	r, err := call(ShellCommandTool{}, map[string]any{"command": "echo hello"})
	if err != nil || !r.Success {
		t.Fatalf("echo: %v %v", r, err)
	}
	if !strings.Contains(r.Content, "hello") {
		t.Errorf("content: got %q", r.Content)
	}
}

// TestShellCommandError 验证非零退出返回 RespondToModel 错误。
func TestShellCommandError(t *testing.T) {
	_, err := call(ShellCommandTool{}, map[string]any{"command": "exit 1"})
	wantRespondToModel(t, err, "exit 1")
}

// TestShellCommandTimeout 验证超时返回 RespondToModel 错误。
func TestShellCommandTimeout(t *testing.T) {
	// Windows cmd 用 ping 模拟慢命令；POSIX 用 sleep。都取整命令参数。
	_, err := call(ShellCommandTool{}, map[string]any{"command": slowCommand(), "timeout_ms": 100})
	wantRespondToModel(t, err, "slow command")
}

// slowCommand 返回一个至少阻塞 200ms 的跨平台命令。
func slowCommand() string {
	if isWindows() {
		return "ping -n 3 127.0.0.1 >nul"
	}
	return "sleep 0.5"
}
