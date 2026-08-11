package tui

import (
	"context"

	"github.com/agent-project/harness/internal/middleware"
	tea "github.com/charmbracelet/bubbletea"
)

// tuiApprover 是审批交互器（注入 rc.Approver，ADR-030）：把审批请求经
// program.Send 发到 Update → 弹窗 + y/s/n 按键回送决策。agent goroutine 的
// Request 阻塞等 respCh（与 REPL channelApprover 同构，经 Msg 桥接）。
type tuiApprover struct {
	send func(tea.Msg)
}

// Request 发送审批请求并等待用户决策（ctx cancel 时自动拒绝）。
func (a *tuiApprover) Request(ctx context.Context, req middleware.ApprovalRequest) (middleware.Decision, error) {
	respCh := make(chan middleware.Decision, 1)
	if a.send == nil {
		return middleware.DecisionDeny, context.Canceled // 无桥（纯测试）→ 拒绝
	}
	a.send(approvalRequestMsg{req: req, respCh: respCh})
	select {
	case d := <-respCh:
		return d, nil
	case <-ctx.Done():
		return middleware.DecisionDeny, ctx.Err()
	}
}

// Ask 发送提问请求并等待用户回答（ADR-036；ask 弹窗在 Model.Update 处理，
// 阶段 B 实现弹窗 UI）。ctx cancel 时返回空结果 + ctx.Err()。
func (a *tuiApprover) Ask(ctx context.Context, req middleware.AskRequest) (middleware.AskResult, error) {
	respCh := make(chan middleware.AskResult, 1)
	if a.send == nil {
		return middleware.AskResult{}, context.Canceled // 无桥（纯测试）→ 取消
	}
	a.send(askRequestMsg{req: req, respCh: respCh})
	select {
	case r := <-respCh:
		return r, nil
	case <-ctx.Done():
		return middleware.AskResult{}, ctx.Err()
	}
}
