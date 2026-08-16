package subagent

import (
	"context"
	"errors"

	"github.com/agent-project/harness/internal/middleware"
)

// subagentApprover 是子 agent 的审批归属包装器（定案第 9 条）：转发父的
// Approver（TUI/run 注入的同一交互器），但请求带上 AgentID 归属标识——UI
// 渲染【子 agent <id>】前缀，用户知道审批来自哪个子 agent。
//
// inner nil（非 TTY 自动拒绝语义）时 Request 直接返回 Deny（与
// ApprovalMiddleware 的 nil 检查等价）、Ask 返回错误。
type subagentApprover struct {
	inner   middleware.Approver
	agentID string
}

func (a *subagentApprover) Request(ctx context.Context, req middleware.ApprovalRequest) (middleware.Decision, error) {
	if a.inner == nil {
		return middleware.DecisionDeny, nil
	}
	req.AgentID = a.agentID
	return a.inner.Request(ctx, req)
}

func (a *subagentApprover) Ask(ctx context.Context, req middleware.AskRequest) (middleware.AskResult, error) {
	if a.inner == nil {
		return middleware.AskResult{}, errors.New("subagent: no approver (non-TTY)")
	}
	req.AgentID = a.agentID
	return a.inner.Ask(ctx, req)
}
