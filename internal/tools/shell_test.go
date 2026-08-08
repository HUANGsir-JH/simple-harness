package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

func isWindows() bool { return runtime.GOOS == "windows" }

// callWithRC 用给定 rc（可含 StatePath）执行一次工具调用（测试辅助）。
func callWithRC(t Tool, rc *middleware.RuntimeContext, args map[string]any) (messages.ToolResult, error) {
	b, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return t.Handle(context.Background(), rc, "c1", b)
}

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

// TestShellCommandTimeoutEvictsOutput 验证超时时已收集输出落盘（错误带路径，
// 模型可读进度，ADR-028）。
func TestShellCommandTimeoutEvictsOutput(t *testing.T) {
	rc := middleware.NewRuntimeContext()
	rc.StatePath = filepath.Join(t.TempDir(), "sess", "agentstate.json")
	// 先输出一段再卡住：PowerShell 冷启动约 1s，用较长 timeout 让它先输出。
	// Windows: Write-Output + Start-Sleep；POSIX: echo + sleep。
	var cmd, timeout int
	if isWindows() {
		cmd = 1
		timeout = 1500
	} else {
		cmd = 2
		timeout = 500
	}
	var command string
	switch cmd {
	case 1:
		command = `Write-Output ("slow-output-before-hang"*2000); Start-Sleep -Seconds 3` // >40KB 触发落盘
	case 2:
		command = `yes slow-output-before-hang | head -n 2000; sleep 3`
	}
	_, err := callWithRC(ShellCommandTool{}, rc, map[string]any{"command": command, "timeout_ms": timeout})
	wantRespondToModel(t, err, "命令超时")
	if !strings.Contains(err.Error(), "完整内容已保存到") {
		t.Errorf("timeout error should contain saved path hint, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "slow-output-before-hang") {
		t.Errorf("timeout error should include collected output, got: %s", err.Error())
	}
}

// TestShellCommandQuotedPath 验证含引号路径的命令不被破坏
// （Windows cmd 引号转义坑：exec.Command 的 \" 与 cmd 的 "" 不兼容，走 .bat 修复）。
func TestShellCommandQuotedPath(t *testing.T) {
	dir := t.TempDir()
	r, err := call(ShellCommandTool{}, map[string]any{"command": "echo \"" + dir + "\""})
	if err != nil || !r.Success {
		t.Fatalf("quoted: %v %v", r, err)
	}
	if !strings.Contains(r.Content, dir) {
		t.Errorf("content: got %q", r.Content)
	}
}

// slowCommand 返回一个至少阻塞 1s 的跨平台命令。
func slowCommand() string {
	if isWindows() {
		return "Start-Sleep -Seconds 1"
	}
	return "sleep 1"
}

// TestShellCommandUnicode 验证中文输出不乱码（Windows PowerShell UTF-8 前缀）。
func TestShellCommandUnicode(t *testing.T) {
	r, err := call(ShellCommandTool{}, map[string]any{"command": "echo 中文测试"})
	if err != nil || !r.Success {
		t.Fatalf("unicode: %v %v", r, err)
	}
	if !strings.Contains(r.Content, "中文测试") {
		t.Errorf("content: got %q", r.Content)
	}
}
