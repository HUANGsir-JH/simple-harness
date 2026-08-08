package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
)

// newTodoRC 构造带新 AgentState 的 rc。
func newTodoRC(t *testing.T) *middleware.RuntimeContext {
	t.Helper()
	rc := middleware.NewRuntimeContext()
	rc.State = agentstate.New("s1", "m", ".")
	return rc
}

func callTodo(t *testing.T, rc *middleware.RuntimeContext, raw string) (bool, string) {
	t.Helper()
	res, err := UpdateTodoTool{}.Handle(context.Background(), rc, "call", json.RawMessage(raw))
	if err != nil {
		var te *ToolError
		if errors.As(err, &te) {
			return false, te.Message
		}
		t.Fatalf("Handle 非工具错误: %v", err)
	}
	return res.Success, res.Content
}

// TestUpdateTodoFullReplace 验证全量替换：第二次调用整体覆盖第一次。
func TestUpdateTodoFullReplace(t *testing.T) {
	rc := newTodoRC(t)
	ok, _ := callTodo(t, rc, `{"todos":[{"position":1,"description":"a","status":"pending"}]}`)
	if !ok {
		t.Fatal("第一次调用应成功")
	}
	// 第二次传不同列表：a 消失，b 新增。
	ok, _ = callTodo(t, rc, `{"todos":[{"position":1,"description":"b","status":"in_progress"}]}`)
	if !ok {
		t.Fatal("第二次调用应成功")
	}
	if len(rc.State.Todos) != 1 || rc.State.Todos[0].Description != "b" {
		t.Errorf("全量替换失败: %+v", rc.State.Todos)
	}
	// 空列表清空。
	callTodo(t, rc, `{"todos":[]}`)
	if len(rc.State.Todos) != 0 {
		t.Errorf("空列表应清空: %+v", rc.State.Todos)
	}
}

// TestUpdateTodoSortsByPosition 验证按 position 排序（含重复 position 稳定）。
func TestUpdateTodoSortsByPosition(t *testing.T) {
	rc := newTodoRC(t)
	ok, _ := callTodo(t, rc, `{"todos":[
		{"position":3,"description":"c","status":"pending"},
		{"position":1,"description":"a","status":"pending"},
		{"position":2,"description":"b","status":"pending"},
		{"position":2,"description":"b2","status":"pending"}]}`)
	if !ok {
		t.Fatal("调用应成功")
	}
	want := []string{"a", "b", "b2", "c"}
	for i, w := range want {
		if rc.State.Todos[i].Description != w {
			t.Errorf("顺序[%d]: got %s want %s（全: %+v）", i, rc.State.Todos[i].Description, w, rc.State.Todos)
		}
	}
}

// TestUpdateTodoInvalidStatus 验证非法 status 报 RespondToModel 错误且 state 不变。
func TestUpdateTodoInvalidStatus(t *testing.T) {
	rc := newTodoRC(t)
	callTodo(t, rc, `{"todos":[{"position":1,"description":"keep","status":"pending"}]}`)

	ok, msg := callTodo(t, rc, `{"todos":[{"position":1,"description":"x","status":"done"}]}`)
	if ok {
		t.Fatal("非法 status 应失败")
	}
	if !strings.Contains(msg, "非法状态") {
		t.Errorf("错误消息应指明非法状态: %s", msg)
	}
	// state 未被破坏。
	if len(rc.State.Todos) != 1 || rc.State.Todos[0].Description != "keep" {
		t.Errorf("非法调用不应改 state: %+v", rc.State.Todos)
	}
}

// TestUpdateTodoEmptyDescription 验证空 description 报错。
func TestUpdateTodoEmptyDescription(t *testing.T) {
	rc := newTodoRC(t)
	ok, msg := callTodo(t, rc, `{"todos":[{"position":1,"description":"  ","status":"pending"}]}`)
	if ok {
		t.Fatal("空 description 应失败")
	}
	if !strings.Contains(msg, "description") {
		t.Errorf("错误消息应指明 description: %s", msg)
	}
}

// TestUpdateTodoNilState 验证 rc.State 为空时防御性报错。
func TestUpdateTodoNilState(t *testing.T) {
	rc := middleware.NewRuntimeContext() // State 未设置
	ok, msg := callTodo(t, rc, `{"todos":[{"position":1,"description":"x","status":"pending"}]}`)
	if ok {
		t.Fatal("无 state 应失败")
	}
	if !strings.Contains(msg, "会话状态") {
		t.Errorf("错误消息应说明无会话状态: %s", msg)
	}
}

// TestUpdateTodoResultRenders 验证工具结果回填为渲染后的 checklist。
func TestUpdateTodoResultRenders(t *testing.T) {
	rc := newTodoRC(t)
	ok, content := callTodo(t, rc, `{"todos":[
		{"position":2,"description":"写测试","status":"pending"},
		{"position":1,"description":"修复 bug","status":"in_progress"}]}`)
	if !ok {
		t.Fatal("调用应成功")
	}
	want := "1. [~] 修复 bug\n2. [ ] 写测试"
	if content != want {
		t.Errorf("结果渲染:\ngot:\n%s\nwant:\n%s", content, want)
	}
}

// TestUpdateTodoRecordsLastActivity 验证 handler 记录活动基准（模型更新 todo 后
// 提醒 idle 计数清零重计；TodoReminderMiddleware 依赖）。
func TestUpdateTodoRecordsLastActivity(t *testing.T) {
	rc := newTodoRC(t)
	rc.Set("todo_sample_count", 5)
	callTodo(t, rc, `{"todos":[{"position":1,"description":"a","status":"pending"}]}`)
	last, ok := rc.Get("todo_last_activity").(int)
	if !ok || last != 5 {
		t.Errorf("todo_last_activity: got %v want 5", rc.Get("todo_last_activity"))
	}
}

// TestUpdateTodoConcurrent 验证并发写 state 无 race（todoMu 保护）。
func TestUpdateTodoConcurrent(t *testing.T) {
	rc := newTodoRC(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw := fmt.Sprintf(`{"todos":[{"position":1,"description":"t%d","status":"pending"}]}`, i)
			_, _ = UpdateTodoTool{}.Handle(context.Background(), rc, "call", json.RawMessage(raw))
		}(i)
	}
	wg.Wait()
	// 只需不 panic/不 race；最终状态由最后一次胜出。
	if len(rc.State.Todos) != 1 {
		t.Errorf("并发后应剩 1 项: %+v", rc.State.Todos)
	}
}
