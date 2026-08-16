package subagent

import (
	"context"
	"testing"

	"github.com/agent-project/harness/internal/middleware"
)

// stubApprover 记录收到的请求并返回预设决策。
type stubApprover struct {
	req     middleware.ApprovalRequest
	ask     middleware.AskRequest
	decided middleware.Decision
}

func (s *stubApprover) Request(ctx context.Context, req middleware.ApprovalRequest) (middleware.Decision, error) {
	s.req = req
	return s.decided, nil
}

func (s *stubApprover) Ask(ctx context.Context, req middleware.AskRequest) (middleware.AskResult, error) {
	s.ask = req
	return middleware.AskResult{Selection: []string{"是"}}, nil
}

// TestSubagentApproverFillsAgentID 验证归属包装器：转发时填 AgentID，
// 透传决策；inner nil 时 Request 自动拒绝、Ask 报错。
func TestSubagentApproverFillsAgentID(t *testing.T) {
	inner := &stubApprover{decided: middleware.DecisionAllow}
	a := &subagentApprover{inner: inner, agentID: "sub-123"}

	d, err := a.Request(context.Background(), middleware.ApprovalRequest{ToolName: "shell_command", Summary: "rm -rf x", Mode: "readonly"})
	if err != nil || d != middleware.DecisionAllow {
		t.Fatalf("Request: %v %v", d, err)
	}
	if inner.req.AgentID != "sub-123" {
		t.Errorf("AgentID 应注入: %+v", inner.req)
	}

	res, err := a.Ask(context.Background(), middleware.AskRequest{Question: "继续吗？"})
	if err != nil || !res.HasSelection("是") {
		t.Fatalf("Ask: %+v %v", res, err)
	}
	if inner.ask.AgentID != "sub-123" {
		t.Errorf("Ask AgentID 应注入: %+v", inner.ask)
	}

	// inner nil（非 TTY）：Request 自动拒绝、Ask 报错。
	nilA := &subagentApprover{agentID: "sub-9"}
	d2, err := nilA.Request(context.Background(), middleware.ApprovalRequest{})
	if err != nil || d2 != middleware.DecisionDeny {
		t.Errorf("nil inner Request 应自动拒绝: %v %v", d2, err)
	}
	if _, err := nilA.Ask(context.Background(), middleware.AskRequest{}); err == nil {
		t.Error("nil inner Ask 应报错")
	}
}
