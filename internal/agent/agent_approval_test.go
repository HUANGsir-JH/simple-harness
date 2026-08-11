package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// denyApprover 是始终拒绝的测试审批者。
type denyApprover struct{}

func (denyApprover) Request(context.Context, middleware.ApprovalRequest) (middleware.Decision, error) {
	return middleware.DecisionDeny, nil
}

// allowApprover 是始终允许的测试审批者。
type allowApprover struct{}

func (allowApprover) Request(context.Context, middleware.ApprovalRequest) (middleware.Decision, error) {
	return middleware.DecisionAllow, nil
}

// TestRunApprovalDenied 验证审批拒绝（DeniedError，ADR-029）：工具不执行、
// 结果回填失败、回合继续到 turn_done、Run 返回 nil（拒绝 ≠ Fatal，ADR-006）。
func TestRunApprovalDenied(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if len(req.Messages) == 1 {
			return toolCallStream("write_file", `{"path":"a.txt","content":"x"}`), nil
		}
		return textStream("ok"), nil
	}}
	reg := tools.NewRegistry()
	executed := false
	reg.Register(&fakeTool{name: "write_file", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		executed = true
		return messages.ToolResult{Success: true, Content: "written"}, nil
	}})
	a := New(fc, "m")
	a.SetTools(reg)
	a.SetMiddleware(middleware.NewChain(impl.ApprovalMiddleware{DefaultMode: impl.ModeReadonly}))

	conv := newConversation()
	rc := rcFor(conv)
	rc.State = agentstate.New("s1", "m", t.TempDir())
	rc.Approver = denyApprover{}
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rc, rec.on); err != nil {
		t.Fatalf("Run 应成功（拒绝≠Fatal）: %v", err)
	}
	if executed {
		t.Error("被拒工具不应执行")
	}
	// user, assistant, tool_result(失败), assistant
	if len(conv.Messages) != 4 {
		t.Fatalf("conversation 消息数: %d", len(conv.Messages))
	}
	tr := conv.Messages[2]
	if tr.Role != messages.RoleTool || len(tr.ToolResults) != 1 || tr.ToolResults[0].Success {
		t.Errorf("tool result 应标记失败: %+v", tr.ToolResults)
	}
	if !strings.Contains(tr.ToolResults[0].Content, "用户拒绝") {
		t.Errorf("拒绝理由: %q", tr.ToolResults[0].Content)
	}
	if !rec.has(events.EventTurnDone) {
		t.Error("拒绝后回合应继续到 turn_done")
	}
}

// TestRunApprovalAllow 验证审批允许后工具正常执行（对照：allowed 才执行）。
func TestRunApprovalAllow(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if len(req.Messages) == 1 {
			return toolCallStream("write_file", `{"path":"a.txt","content":"x"}`), nil
		}
		return textStream("ok"), nil
	}}
	reg := tools.NewRegistry()
	executed := false
	reg.Register(&fakeTool{name: "write_file", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		executed = true
		return messages.ToolResult{Success: true, Content: "written"}, nil
	}})
	a := New(fc, "m")
	a.SetTools(reg)
	a.SetMiddleware(middleware.NewChain(impl.ApprovalMiddleware{DefaultMode: impl.ModeReadonly}))

	conv := newConversation()
	rc := rcFor(conv)
	rc.State = agentstate.New("s1", "m", t.TempDir())
	rc.Approver = allowApprover{}
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rc, rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !executed {
		t.Error("允许后工具应执行")
	}
	if !rec.has(events.EventTurnDone) {
		t.Error("应到 turn_done")
	}
}

// TestRunApprovalNoApprover 验证非 TTY（无 Approver）自动拒绝：工具不执行、
// 结果回填失败、回合继续。
func TestRunApprovalNoApprover(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if len(req.Messages) == 1 {
			return toolCallStream("write_file", `{"path":"a.txt","content":"x"}`), nil
		}
		return textStream("ok"), nil
	}}
	reg := tools.NewRegistry()
	executed := false
	reg.Register(&fakeTool{name: "write_file", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		executed = true
		return messages.ToolResult{Success: true, Content: "written"}, nil
	}})
	a := New(fc, "m")
	a.SetTools(reg)
	a.SetMiddleware(middleware.NewChain(impl.ApprovalMiddleware{DefaultMode: impl.ModeReadonly}))

	conv := newConversation()
	rc := rcFor(conv)
	rc.State = agentstate.New("s1", "m", t.TempDir())
	// rc.Approver 不设 → 自动拒绝。
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rc, rec.on); err != nil {
		t.Fatalf("Run 应成功: %v", err)
	}
	if executed {
		t.Error("自动拒绝后工具不应执行")
	}
	tr := conv.Messages[2]
	if tr.Role != messages.RoleTool || len(tr.ToolResults) != 1 || tr.ToolResults[0].Success {
		t.Errorf("tool result 应标记失败: %+v", tr.ToolResults)
	}
	if !strings.Contains(tr.ToolResults[0].Content, "无法询问用户") {
		t.Errorf("自动拒绝理由: %q", tr.ToolResults[0].Content)
	}
	if !rec.has(events.EventTurnDone) {
		t.Error("自动拒绝后回合应继续到 turn_done")
	}
}
