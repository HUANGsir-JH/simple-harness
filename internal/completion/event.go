// Package completion 定义后台任务的通用异步完成通知通道（计划
// async-completion-notify-2026-08-13，版本目标 0.9.3）。
//
// 参照 AgentScope Java v2 的 AsyncToolMiddleware + MessageBus.inbox：
// 生产端（如 shell 的 Wait goroutine）在后台任务自然完成时把一条完成事件
// Append 进会话级 Queue（锁内 append + 原子落盘 completions.json + 锁外调
// OnAppend）；注入端（BackgroundCompletionMiddleware）每次采样前 Drain 并
// 以 user 消息注入对话；TUI 唤醒器订阅 OnAppend 唤起空闲会话。完成通知是
// "一次性事件"不是"会话状态"，故独立成文件、不挂 AgentState。
//
// 只依赖 stdlib（最底层包；middleware/session/tools 都依赖它）。
package completion

// Event 是一条后台任务完成事件。
// Result 由生产端拼好的通知全文（注入端直接以 user 消息注入，不再加工）。
// SessionID 为阶段 5 子 agent 跨会话复用预留（单进程内队列即会话级，当前恒
// 与所属会话一致）。
type Event struct {
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Result     string `json:"result"`
	// ExitCode 是进程退出码（exec.ExitError.ExitCode()；signal 杀 = -1）。
	ExitCode *int   `json:"exit_code,omitempty"`
	DoneAt   string `json:"done_at"`
	// SessionID 是事件所属会话（跨会话落盘恢复时校验用）。
	SessionID string `json:"session_id,omitempty"`
}
