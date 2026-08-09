package tui

import "github.com/agent-project/harness/internal/agent"

// 内部消息类型（bubbletea Msg；跨 goroutine 经 program.Send 传入 Update）。
type (
	// agentEventMsg 是 agent 回合级事件桥（onEvent → program.Send，ADR-030）。
	agentEventMsg struct{ ev agent.Event }

	// runDoneMsg 标记一个回合结束（含错误）。
	runDoneMsg struct{ err error }

	// submitMsg 是用户提交的一行输入（Enter；run 期间进队列，W4 扩展）。
	submitMsg struct{ line string }
)
