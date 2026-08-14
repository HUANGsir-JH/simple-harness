package tui

import (
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/middleware"
)

// 内部消息类型（bubbletea Msg；跨 goroutine 经 program.Send 传入 Update）。
type (
	// agentEventMsg 是 agent 回合级事件桥（onEvent → program.Send，ADR-030）。
	agentEventMsg struct{ ev events.Event }

	// runDoneMsg 标记一个回合结束（含错误）。
	runDoneMsg struct{ err error }

	// compactDoneMsg 标记手动 /compact 完成（compacted = 是否真的压缩；err 非
	// nil = 摘要失败，不重写 conversation）。Update 内 reloadSession + 展示。
	compactDoneMsg struct {
		compacted bool
		err       error
	}

	// submitMsg 是用户提交的一行输入（Enter；run 期间进队列，W4 扩展）。
	submitMsg struct{ line string }

	// approvalRequestMsg 是审批请求桥（tuiApprover.Request → program.Send）。
	// Update 弹窗 + y/s/n 按键后经 respCh 回送决策。
	approvalRequestMsg struct {
		req    middleware.ApprovalRequest
		respCh chan middleware.Decision
	}

	// askRequestMsg 是提问请求桥（tuiApprover.Ask → program.Send，ADR-036）。
	// Update 弹 ask 输入弹窗（选项 + Other）后经 respCh 回送回答。
	askRequestMsg struct {
		req    middleware.AskRequest
		respCh chan middleware.AskResult
	}

	// completionWakeMsg 是后台任务完成唤起信号（2026-08-13）：会话完成队列
	// OnAppend → program.Send；Update 内 MaybeWake 决策（同步抢占 cancel +
	// m.running 双闸防并发 run）。
	completionWakeMsg struct{}
)
