package session

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
)

// TestSessionMiddlewareLoadSave 验证无状态 SessionMiddleware（ADR-026）：
// before 从 rc.StatePath 加载（缺失 → 空 state）、after 保存。
func TestSessionMiddlewareLoadSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentstate.json")
	mw := SessionMiddleware{}

	rc := middleware.NewRuntimeContext()
	rc.StatePath = path
	ran := false
	next := func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.AgentInput) error {
		ran = true
		rc.State.ReplaceTodos([]agentstate.TodoItem{{Position: 1, Description: "任务 A", Status: agentstate.TodoPending}}) // 回合内修改 state（模拟 todo 工具）
		return nil
	}
	if err := mw.OnAgent(context.Background(), rc, middleware.AgentInput{}, next); err != nil {
		t.Fatalf("OnAgent: %v", err)
	}
	if !ran {
		t.Fatal("next 未执行")
	}
	if rc.State == nil {
		t.Fatal("rc.State 应为空 state（文件缺失兜底）")
	}
	// after 已落盘：重新加载应含回合内新增的 todo。
	st, err := agentstate.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Todos) != 1 || st.Todos[0].Description != "任务 A" {
		t.Errorf("state 未在 after 保存: %+v", st.Todos)
	}
}

// TestSessionMiddlewarePresetState 验证 rc.State 已预置（Session.RuntimeContext
// 路径）时 before 不重复加载，且 after 保存预置实例。
func TestSessionMiddlewarePresetState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentstate.json")
	mw := SessionMiddleware{}

	rc := middleware.NewRuntimeContext()
	rc.StatePath = path
	rc.State = agentstate.New("preset", "m", ".")
	var seen *agentstate.AgentState
	next := func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.AgentInput) error {
		seen = rc.State
		return nil
	}
	if err := mw.OnAgent(context.Background(), rc, middleware.AgentInput{}, next); err != nil {
		t.Fatalf("OnAgent: %v", err)
	}
	if seen == nil || seen.SessionID != "preset" {
		t.Errorf("应使用预置 state: %+v", seen)
	}
	st, err := agentstate.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionID != "preset" {
		t.Errorf("after 应保存预置 state: %+v", st)
	}
}

// TestSessionMiddlewareNoPath 验证 rc.StatePath 为空时不落盘（非会话场景 no-op）。
func TestSessionMiddlewareNoPath(t *testing.T) {
	mw := SessionMiddleware{}
	rc := middleware.NewRuntimeContext()
	if err := mw.OnAgent(context.Background(), rc, middleware.AgentInput{}, func(context.Context, *middleware.RuntimeContext, middleware.AgentInput) error {
		return nil
	}); err != nil {
		t.Fatalf("OnAgent: %v", err)
	}
}
