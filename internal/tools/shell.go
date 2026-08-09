package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
)

// powershellUTF8Prefix 是 PowerShell 脚本的 UTF-8 输出前缀（对标 codex 的
// codex-shell-command）：显式设置输出编码为 UTF-8，避免中文等经 console
// 代码页（默认 GBK）乱码。
const powershellUTF8Prefix = "try { [Console]::OutputEncoding=[System.Text.Encoding]::UTF8 } catch {}\n"

// ShellCommandTool 在 shell 中执行命令。平台分派：Windows 用 PowerShell，
// POSIX 用 sh -c。命令非零退出返回错误文本（可重试）；超时把已收集输出
// 落盘 evictions/（错误带路径，模型可读进度，ADR-028）。
type ShellCommandTool struct{}

func (ShellCommandTool) Name() string { return "shell_command" }

func (ShellCommandTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "shell_command",
		Description: "在 shell 中执行命令并返回输出（stdout+stderr 合并）。Windows 用 PowerShell，POSIX 用 sh -c。命令非零退出或超时返回错误文本（超时输出的完整版会保存到 evictions/ 目录，错误信息含路径）。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "要执行的命令"},
				"workdir": {"type": "string", "description": "工作目录（默认当前目录）"},
				"timeout_ms": {"type": "integer", "description": "超时毫秒（默认 30000）"}
			},
			"required": ["command"]
		}`),
	}
}

func (ShellCommandTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	var p struct {
		Command   string `json:"command"`
		Workdir   string `json:"workdir"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: 参数解析失败: " + err.Error()}
	}
	if p.Command == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: command 不能为空"}
	}
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if p.TimeoutMS <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, err := platformShellCommand(ctx, p.Command, p.Workdir)
	if err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: " + err.Error()}
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		// 超时：已收集输出落盘（错误带路径，模型可用 read_file 读进度，不盲目重试）。
		msg := fmt.Sprintf("shell_command: 命令超时（%v）", timeout)
		if out.Len() > 0 {
			msg += "\n" + impl.EvictContent(rc, out.String())
		}
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}
	}
	if err != nil {
		msg := "shell_command: " + err.Error()
		if out.Len() > 0 {
			msg += "\n" + impl.EvictContent(rc, out.String())
		}
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}
	}
	return messages.ToolResult{Success: true, Content: out.String()}, nil
}

// platformShellCommand 按平台构造 shell 命令。
//
// Windows 用 PowerShell（与 codex 一致）：规避 cmd 的引号转义坑——Go exec 的
// 参数转义用 `\"`，cmd.exe 的引号转义是 `""`（两者不兼容，含引号路径的命令
// 经 exec.Command 传给 cmd 会破坏路径）；PowerShell 走标准命令行解析，无此
// 问题。另加 UTF-8 输出前缀处理中文编码。
func platformShellCommand(ctx context.Context, command, workdir string) (*exec.Cmd, error) {
	if runtime.GOOS == "windows" {
		cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", powershellUTF8Prefix+command)
		if workdir != "" {
			cmd.Dir = workdir
		}
		return cmd, nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if workdir != "" {
		cmd.Dir = workdir
	}
	return cmd, nil
}
