package approval

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
)

// mockApprover 是测试用审批者（记录请求、返回预设决策）。
type mockApprover struct {
	reqs     []middleware.ApprovalRequest
	decision middleware.Decision
	err      error
}

func (m *mockApprover) Request(_ context.Context, req middleware.ApprovalRequest) (middleware.Decision, error) {
	m.reqs = append(m.reqs, req)
	return m.decision, m.err
}

// newRC 构造带 State + Approver 的 RuntimeContext（测试辅助）。
func newRC(t *testing.T, approver middleware.Approver) *middleware.RuntimeContext {
	t.Helper()
	rc := middleware.NewRuntimeContext()
	rc.State = agentstate.New("s1", "m", t.TempDir())
	rc.Approver = approver
	return rc
}

// TestOnActingAllow 验证放行路径（只读工具直接执行，不询问）。
func TestOnActingAllow(t *testing.T) {
	mw := ApprovalMiddleware{DefaultMode: ModeAcceptEdits}
	appr := &mockApprover{}
	rc := newRC(t, appr)
	executed := false
	core := func(context.Context, *middleware.RuntimeContext, middleware.ActingInput) error {
		executed = true
		return nil
	}
	err := mw.OnActing(context.Background(), rc, middleware.ActingInput{Call: toolCall("read_file", map[string]any{"path": "a"})}, core)
	if err != nil || !executed {
		t.Fatalf("allow: err=%v executed=%v", err, executed)
	}
	if len(appr.reqs) != 0 {
		t.Errorf("allow: should not ask, got %d requests", len(appr.reqs))
	}
}

// TestOnActingAskAllow 验证 Ask → 用户允许 → 执行。
func TestOnActingAskAllow(t *testing.T) {
	mw := ApprovalMiddleware{DefaultMode: ModeAcceptEdits}
	appr := &mockApprover{decision: middleware.DecisionAllow}
	rc := newRC(t, appr)
	executed := false
	core := func(context.Context, *middleware.RuntimeContext, middleware.ActingInput) error {
		executed = true
		return nil
	}
	err := mw.OnActing(context.Background(), rc, middleware.ActingInput{Call: shellCall("git push")}, core)
	if err != nil || !executed {
		t.Fatalf("ask+allow: err=%v executed=%v", err, executed)
	}
	// 请求摘要 = 命令原文。
	if len(appr.reqs) != 1 || appr.reqs[0].Summary != "git push" {
		t.Errorf("ask: got requests %+v", appr.reqs)
	}
}

// TestOnActingAskSession 验证 AllowSession：执行 + 写入会话级记忆。
func TestOnActingAskSession(t *testing.T) {
	mw := ApprovalMiddleware{DefaultMode: ModeReadonly}
	appr := &mockApprover{decision: middleware.DecisionAllowSession}
	rc := newRC(t, appr)
	executed := false
	core := func(context.Context, *middleware.RuntimeContext, middleware.ActingInput) error {
		executed = true
		return nil
	}
	err := mw.OnActing(context.Background(), rc, middleware.ActingInput{Call: shellCall("git push origin main")}, core)
	if err != nil || !executed {
		t.Fatalf("ask+session: err=%v executed=%v", err, executed)
	}
	// 记忆 key = 规范化命令前缀。
	if rc.State.Permission == nil || len(rc.State.Permission.Approved) != 1 || rc.State.Permission.Approved[0] != "git push" {
		t.Errorf("session: approved = %+v", rc.State.Permission)
	}
}

// TestOnActingAskDeny 验证拒绝 → DeniedError（回填模型，不执行工具）。
func TestOnActingAskDeny(t *testing.T) {
	mw := ApprovalMiddleware{DefaultMode: ModeAcceptEdits}
	appr := &mockApprover{decision: middleware.DecisionDeny}
	rc := newRC(t, appr)
	executed := false
	core := func(context.Context, *middleware.RuntimeContext, middleware.ActingInput) error {
		executed = true
		return nil
	}
	err := mw.OnActing(context.Background(), rc, middleware.ActingInput{Call: shellCall("rm -rf x")}, core)
	if executed {
		t.Fatal("deny: tool should not execute")
	}
	var de *middleware.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("deny: want DeniedError, got %v", err)
	}
}

// TestOnActingNoApprover 验证无审批者（非 TTY）→ 自动拒绝。
func TestOnActingNoApprover(t *testing.T) {
	mw := ApprovalMiddleware{DefaultMode: ModeAcceptEdits}
	rc := newRC(t, nil) // Approver nil
	executed := false
	core := func(context.Context, *middleware.RuntimeContext, middleware.ActingInput) error {
		executed = true
		return nil
	}
	err := mw.OnActing(context.Background(), rc, middleware.ActingInput{Call: shellCall("git push")}, core)
	if executed {
		t.Fatal("no approver: tool should not execute")
	}
	var de *middleware.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("no approver: want DeniedError, got %v", err)
	}
}

// TestOnActingSessionModeOverride 验证会话模式覆盖 config 默认。
// 会话 Mode=readonly → 即使 DefaultMode=acceptedit 也询问写操作。
func TestOnActingSessionModeOverride(t *testing.T) {
	mw := ApprovalMiddleware{DefaultMode: ModeAcceptEdits}
	appr := &mockApprover{decision: middleware.DecisionAllow}
	rc := newRC(t, appr)
	rc.State.Permission = &agentstate.PermissionState{Mode: ModeReadonly}
	executed := false
	core := func(context.Context, *middleware.RuntimeContext, middleware.ActingInput) error {
		executed = true
		return nil
	}
	// write_file 在 acceptedit 下应放行，但会话 readonly 覆盖 → 询问。
	err := mw.OnActing(context.Background(), rc, middleware.ActingInput{Call: toolCall("write_file", map[string]any{"path": "a.txt"})}, core)
	if err != nil || !executed {
		t.Fatalf("session override: err=%v executed=%v", err, executed)
	}
	if len(appr.reqs) != 1 {
		t.Errorf("session override: should ask once, got %d", len(appr.reqs))
	}
	if appr.reqs[0].Mode != ModeReadonly {
		t.Errorf("session override: request mode = %q, want readonly", appr.reqs[0].Mode)
	}
}

// TestOnActingNilCall 验证空调用透传（不审批）。
func TestOnActingNilCall(t *testing.T) {
	mw := ApprovalMiddleware{DefaultMode: ModeAcceptEdits}
	rc := newRC(t, nil)
	executed := false
	core := func(context.Context, *middleware.RuntimeContext, middleware.ActingInput) error {
		executed = true
		return nil
	}
	if err := mw.OnActing(context.Background(), rc, middleware.ActingInput{Call: nil}, core); err != nil || !executed {
		t.Fatalf("nil call: err=%v executed=%v", err, executed)
	}
}

// TestDeniedErrorViaChain 验证 DeniedError 经 middleware 链传播时
// errors.As 可识别（agent 调用层捕获用）。
func TestDeniedErrorViaChain(t *testing.T) {
	chain := middleware.NewChain(ApprovalMiddleware{DefaultMode: ModeAcceptEdits})
	appr := &mockApprover{decision: middleware.DecisionDeny}
	rc := newRC(t, appr)
	wrapped := chain.WrapActing(func(context.Context, *middleware.RuntimeContext, middleware.ActingInput) error { return nil })
	err := wrapped(context.Background(), rc, middleware.ActingInput{Call: shellCall("git push")})
	var de *middleware.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("via chain: want DeniedError, got %v", err)
	}
}
