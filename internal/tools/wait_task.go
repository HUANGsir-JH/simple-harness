package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// WaitTaskTool 等待后台 shell 进程退出（子 agent 专属，2026-08-16）：阻塞本轮
// 至进程退出或超时，返回退出码 + 日志尾部。子 agent 无唤醒循环（不做 TUI
// 唤醒器），模型用本工具主动轮询替代"结束回合等通知"（定案 subagent.md 第 14
// 条）。主 agent 有唤醒器（ADR-040），不含本工具。
type WaitTaskTool struct{}

func (WaitTaskTool) Name() string { return "wait_task" }

type waitTaskArgs struct {
	PID       int `json:"pid" jsonschema:"description=后台进程 PID（shell_command background: true 或超时转后台返回的 PID）"`
	TimeoutMS int `json:"timeout_ms" jsonschema:"description=等待超时毫秒（默认 30000）；超时后返回仍在运行，可再次调用或 read_file/grep 轮询日志"`
}

func (WaitTaskTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "wait_task",
		Description: "阻塞等待后台 shell 进程退出（返回退出码 + 日志尾部）。" +
			"适用于需要确认 background 命令结果才能继续的场景；超时返回后进程仍在运行，" +
			"可继续 wait_task 或 read_file 查看日志。",
		Parameters: schemaOf[waitTaskArgs](),
	}
}

func (WaitTaskTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	p, err := parseArgs[waitTaskArgs]("wait_task", args)
	if err != nil {
		return messages.ToolResult{}, err
	}
	if p.PID <= 0 {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "wait_task: pid 必须为正整数"}
	}
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if p.TimeoutMS <= 0 {
		timeout = 30 * time.Second
	}

	v, ok := backgroundProcesses.Load(p.PID)
	if !ok {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"wait_task: 未找到后台进程 %d（可能已退出，或非本会话 background 启动）", p.PID)}
	}
	entry := v.(*bgProcess)

	select {
	case <-entry.done:
		// done close 前 exitCode 已写入（happens-before），读安全。
		code := entry.exitCode
		tail := readLogTail(entry.logPath, 8<<10)
		if code != 0 {
			msg := fmt.Sprintf("wait_task: 后台进程 %d 已退出（exit %d）", p.PID, code)
			if tail != "" {
				msg += "\n日志尾部：\n" + tail
			}
			return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}
		}
		content := fmt.Sprintf("后台进程 %d 已退出（exit 0）", p.PID)
		if tail != "" {
			content += "\n日志尾部：\n" + tail
		}
		return messages.ToolResult{Success: true, Content: content}, nil
	case <-time.After(timeout):
		return messages.ToolResult{Success: true, Content: fmt.Sprintf(
			"wait_task: 后台进程 %d 在 %dms 内仍在运行；可继续 wait_task 等待，或 read_file 查看日志：%s",
			p.PID, timeout.Milliseconds(), entry.logPath)}, nil
	case <-ctx.Done():
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "wait_task: 已中断（Esc）"}
	}
}

// readLogTail 读取日志文件尾部（最长 maxBytes；文件不存在返回空串）。
func readLogTail(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	size := fi.Size()
	off := max(size-maxBytes, 0)
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return ""
	}
	return string(buf)
}
