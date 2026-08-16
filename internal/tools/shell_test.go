package tools

import (
	"context"
	"encoding/json"
	"os"
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

// TestShellCommandTimeoutTransfersBackground 验证超时转后台（ADR-038 扩展）：
// 超时不杀树——进程继续跑，已收集输出无缝续写到日志文件，消息含 PID+日志
// 路径+"不要重试"。Esc 仍是杀树（见 TestShellCommandEscInterrupt）。
func TestShellCommandTimeoutTransfersBackground(t *testing.T) {
	rc := middleware.NewRuntimeContext()
	rc.StatePath = filepath.Join(t.TempDir(), "sess", "agentstate.json")
	t.Cleanup(CleanupBackground)
	// 先输出一段再挂起：PowerShell 冷启动慢（约 1s），timeout 放宽到 5s 让输出
	// 稳定发生在超时前；sleep 远大于 timeout 保证必超时。POSIX 启动快用 1.5s。
	var cmd, timeout int
	if isWindows() {
		cmd = 1
		timeout = 5000
	} else {
		cmd = 2
		timeout = 1500
	}
	var command string
	switch cmd {
	case 1:
		command = `Write-Output ("slow-output-before-hang"*2000); Start-Sleep -Seconds 12` // >40KB 输出
	case 2:
		command = `yes slow-output-before-hang | head -n 2000; sleep 12`
	}
	_, err := callWithRC(ShellCommandTool{}, rc, map[string]any{"command": command, "timeout_ms": timeout})
	wantRespondToModel(t, err, "timeout transfer")
	if !strings.Contains(err.Error(), "已自动转入后台") {
		t.Errorf("应提示已转后台: %s", err)
	}
	if !strings.Contains(err.Error(), "不要重试") {
		t.Errorf("应提示不要重试: %s", err)
	}
	// 日志路径从消息提取，含已收集输出（不再走 evictions 路径——日志文件
	// 本身就是完整输出载体）。
	logPath := ""
	for _, line := range strings.Split(err.Error(), "\n") {
		if i := strings.Index(line, "日志："); i >= 0 {
			logPath = strings.TrimSpace(line[i+len("日志："):])
		}
	}
	if logPath == "" {
		t.Fatalf("消息应含日志路径: %s", err)
	}
	if data, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(data), "slow-output-before-hang") {
		t.Errorf("日志应含已收集输出: err=%v len=%d", err, len(data))
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

// TestShellCommandReadonly 验证只读模式强制判定（explore 装配，2026-08-16）：
// 白名单命令放行执行；白名单外命令与 kill_pid 拒绝回填（RespondToModel）。
func TestShellCommandReadonly(t *testing.T) {
	tool := ShellCommandTool{Readonly: true}

	// 白名单命令执行（pwd 跨平台）。
	r, err := call(tool, map[string]any{"command": "pwd"})
	if err != nil || !r.Success {
		t.Fatalf("白名单命令应执行: %v %v", r, err)
	}

	// 白名单外命令拒绝回填（不执行）。
	_, err = call(tool, map[string]any{"command": "rm -rf x"})
	te, ok := err.(*ToolError)
	if !ok || !te.RespondToModel {
		t.Fatalf("白名单外应回填拒绝: %v", err)
	}
	if !strings.Contains(te.Message, "只读") {
		t.Errorf("拒绝文案: %q", te.Message)
	}

	// kill_pid 拒绝（终止操作非只读）。
	_, err = call(tool, map[string]any{"kill_pid": 123})
	te2, ok := err.(*ToolError)
	if !ok || !te2.RespondToModel || !strings.Contains(te2.Message, "kill_pid") {
		t.Fatalf("kill_pid 应拒绝: %v", err)
	}
}
