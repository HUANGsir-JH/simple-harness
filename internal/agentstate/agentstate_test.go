package agentstate

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentstate.json")

	a := New("s1", "claude-sonnet-5", "/work")
	a.ReplaceTodos([]TodoItem{
		{Position: 1, Description: "实现 transcript", Status: TodoPending},
		{Position: 2, Description: "写测试", Status: TodoCompleted},
	})
	if len(a.Todos) != 2 || a.Todos[1].Status != TodoCompleted {
		t.Errorf("ReplaceTodos 未生效: %+v", a.Todos)
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
	if len(got.Todos) != 2 || got.Todos[0].Description != "实现 transcript" || got.Todos[0].Status != TodoPending || got.Todos[1].Position != 2 {
		t.Errorf("todos 往返错误: %+v", got.Todos)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Error("时间戳缺失")
	}
}

// TestReplaceTodosSortsByPosition 验证按 Position 升序稳定排序（重复 position
// 保持传入顺序；非 1 基/带洞位置同样成立）。
func TestReplaceTodosSortsByPosition(t *testing.T) {
	a := New("s1", "m", ".")
	a.ReplaceTodos([]TodoItem{
		{Position: 3, Description: "c"},
		{Position: 1, Description: "a"},
		{Position: 2, Description: "b"},
		{Position: 2, Description: "b2"}, // 与 b 同 position，应保持在 b 之后
	})
	want := []string{"a", "b", "b2", "c"}
	if len(a.Todos) != len(want) {
		t.Fatalf("数量: got %d want %d", len(a.Todos), len(want))
	}
	for i, w := range want {
		if a.Todos[i].Description != w {
			t.Errorf("顺序[%d]: got %s want %s（全: %+v）", i, a.Todos[i].Description, w, a.Todos)
		}
	}
}

// TestRenderTodos 验证渲染格式：pending [ ] / in_progress [~] / completed [x]，
// 重新编号 1..n。
func TestRenderTodos(t *testing.T) {
	a := New("s1", "m", ".")
	a.ReplaceTodos([]TodoItem{
		{Position: 1, Description: "修复登录 bug", Status: TodoInProgress},
		{Position: 2, Description: "写测试", Status: TodoPending},
		{Position: 3, Description: "部署", Status: TodoCompleted},
	})
	want := "1. [~] 修复登录 bug\n2. [ ] 写测试\n3. [x] 部署"
	if got := a.RenderTodos(); got != want {
		t.Errorf("RenderTodos:\ngot:\n%s\nwant:\n%s", got, want)
	}
	empty := New("s1", "m", ".")
	if got := empty.RenderTodos(); got != "（当前无待办）" {
		t.Errorf("空列表渲染: %q", got)
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

// TestAgentStateConcurrentAccess 验证锁下沉（ADR-036 修订）：混合并发读写与
// 序列化不 panic、无 data race（-race 必跑）。修复前 planMu/todoMu 分离时，
// 并发 ReplaceTodos 与 Marshal 会竞态。
func TestAgentStateConcurrentAccess(t *testing.T) {
	a := New("s1", "m", ".")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.ReplaceTodos([]TodoItem{{Position: 1, Description: "x", Status: TodoPending}})
			_ = a.RenderTodos()
			_ = a.TodoCount()
			_ = a.TodoItems()
			a.AddApproved([]string{"git push", "write_file:/ws/a.txt"})
			_ = a.Approved()
			a.SetPlanMode(true)
			_ = a.IsPlanMode()
			a.SetPlanPath("/tmp/plan.md")
			_ = a.PlanPath()
			a.SetPermissionMode("readonly")
			_ = a.PermissionMode()
			_, _ = a.Marshal()
		}()
	}
	wg.Wait()
}

// TestAddUsageAccumulates 验证累计用量跨多轮累加（ADR-037 用量展示）。
func TestAddUsageAccumulates(t *testing.T) {
	a := New("s1", "m", ".")
	a.AddUsage(messages.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 20})
	a.AddUsage(messages.Usage{InputTokens: 30, OutputTokens: 10})
	got := a.UsageTotals()
	if got.InputTokens != 130 || got.OutputTokens != 60 || got.CacheReadInputTokens != 20 {
		t.Errorf("totals: got %+v", got)
	}
	a.SetLastContextTokens(130)
	if a.CurrentContextTokens() != 130 {
		t.Errorf("last context tokens: got %d", a.CurrentContextTokens())
	}
}

// TestUsageFieldsOptional 验证 usage 字段缺省不出现在 json（omitempty）。
func TestUsageFieldsOptional(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentstate.json")
	a := New("s1", "m", ".")
	if err := SaveFile(path, a); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage != nil || got.LastContextTokens != 0 {
		t.Errorf("usage 字段应缺省: %+v", got)
	}
}

// TestAgentStateConcurrentUsage 验证并发累计用量无 data race（-race 必跑）。
func TestAgentStateConcurrentUsage(t *testing.T) {
	a := New("s1", "m", ".")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				a.AddUsage(messages.Usage{InputTokens: 1, OutputTokens: 1})
				a.SetLastContextTokens(int64(j))
			}
			_ = a.UsageTotals()
			_ = a.CurrentContextTokens()
			_, _ = a.Marshal()
		}()
	}
	wg.Wait()
	if got := a.UsageTotals().InputTokens; got != 400 {
		t.Errorf("input total: got %d want 400", got)
	}
}
