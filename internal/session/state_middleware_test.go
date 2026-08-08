package session

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
)

// TestStateMiddlewareLoadSave 验证 onAgent 的 before 加载 / after 保存：
// state 在回合内被修改后，after 落盘生效。
func TestStateMiddlewareLoadSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentstate.json")
	mw := &StateMiddleware{Path: path}

	rc := middleware.NewRuntimeContext()
	ran := false
	next := func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.AgentInput) error {
		ran = true
		// 回合内修改 state（模拟 todo 工具）。
		rc.State.AddTodo("任务 A")
		return nil
	}
	if err := mw.OnAgent(context.Background(), rc, middleware.AgentInput{}, next); err != nil {
		t.Fatalf("OnAgent: %v", err)
	}
	if !ran {
		t.Fatal("next 未执行")
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

// TestStateMiddlewareSkipsLoadWhenPreset 验证 rc.State 已预置（CLI resume
// 路径）时 before 不重复加载。
func TestStateMiddlewareSkipsLoadWhenPreset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentstate.json")
	mw := &StateMiddleware{Path: path}

	rc := middleware.NewRuntimeContext()
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
}

// TestStateMiddlewareNewSession 验证文件不存在时 before 创建空 state 且 after 落盘。
func TestStateMiddlewareNewSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentstate.json")
	mw := &StateMiddleware{Path: path}
	rc := middleware.NewRuntimeContext()
	if err := mw.OnAgent(context.Background(), rc, middleware.AgentInput{}, func(context.Context, *middleware.RuntimeContext, middleware.AgentInput) error {
		return nil
	}); err != nil {
		t.Fatalf("OnAgent: %v", err)
	}
	if rc.State == nil {
		t.Fatal("rc.State 应为空 state")
	}
	if _, err := agentstate.LoadFile(path); err != nil {
		t.Fatalf("after 未落盘: %v", err)
	}
}
