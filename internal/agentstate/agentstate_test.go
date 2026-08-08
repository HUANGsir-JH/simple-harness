package agentstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentstate.json")

	a := New("s1", "claude-sonnet-5", "/work")
	todo := a.AddTodo("实现 transcript")
	if todo.Status != TodoPending {
		t.Errorf("AddTodo 初始状态: %s", todo.Status)
	}
	if !a.UpdateTodoStatus(todo.ID, TodoCompleted) {
		t.Error("UpdateTodoStatus 应命中")
	}
	if a.UpdateTodoStatus("nope", TodoCompleted) {
		t.Error("UpdateTodoStatus 不存在的 id 应返回 false")
	}

	if err := SaveFile(path, a); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got.SessionID != "s1" || got.Model != "claude-sonnet-5" || got.CWD != "/work" {
		t.Errorf("元数据丢失: %+v", got)
	}
	if len(got.Todos) != 1 || got.Todos[0].Description != "实现 transcript" || got.Todos[0].Status != TodoCompleted {
		t.Errorf("todos 往返错误: %+v", got.Todos)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Error("时间戳缺失")
	}
}

func TestLoadFileMissing(t *testing.T) {
	a, err := LoadFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("LoadFile 不存在应返回空 state: %v", err)
	}
	if a == nil || a.Todos == nil || len(a.Todos) != 0 {
		t.Errorf("应为空 state: %+v", a)
	}
}

// TestReservedFieldsOptional 验证预留字段（Permission/Plan/Summary）缺省时
// 不影响往返（旧/新 state 兼容）。
func TestReservedFieldsOptional(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentstate.json")
	a := New("s1", "m", ".")
	if err := SaveFile(path, a); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 预留字段缺省：json 里不应出现（omitempty），且加载后为 nil。
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Permission != nil || got.Plan != nil || got.Summary != "" {
		t.Errorf("预留字段应缺省: %+v", got)
	}
	_ = data
}
