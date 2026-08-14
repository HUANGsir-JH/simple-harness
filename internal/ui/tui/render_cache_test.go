package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestMessageCellCacheInvalidation 消息 cell 缓存 key 正确性（ADR-043 Phase 5）：
// 折叠态/选中态/宽度任一变化都必须失效，恢复原状态应命中原渲染。
func TestMessageCellCacheInvalidation(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.appendMessage(&MessageItem{ID: "m", Role: messages.RoleAssistant,
		Thinking: "private reasoning", Content: "answer", Rendered: "answer", Done: true})
	m.refresh(true)

	item := &m.items[0]
	if item.cache.body == "" || item.cache.width != m.contentWidth {
		t.Fatalf("首次渲染应填充缓存（body=%q width=%d）", item.cache.body, item.cache.width)
	}
	first, _ := renderTimeline(&m)
	if !strings.Contains(first, "answer") {
		t.Fatalf("渲染应含正文，got %q", first)
	}

	// 展开 thinking → 失效（expanded 进 key）。
	m.msgs[0].ThinkingExpanded = true
	expanded, _ := renderTimeline(&m)
	if !strings.Contains(ansi.Strip(expanded), "private reasoning") {
		t.Fatalf("展开后应渲染 thinking 全文，got %q", ansi.Strip(expanded))
	}
	// 折叠回来 → 渲染恢复（缓存命中）。
	m.msgs[0].ThinkingExpanded = false
	back, _ := renderTimeline(&m)
	if back != first {
		t.Fatal("折叠恢复后渲染应与首次一致")
	}

	// 宽度变化 → 失效。
	m.contentWidth = 40
	if _, _ = renderTimeline(&m); item.cache.width != 40 {
		t.Fatalf("宽度变化后缓存应更新，got %d", item.cache.width)
	}
}

// TestToolCellCache 工具 cell 缓存：折叠切换失效、恢复命中；运行中不缓存
// （耗时实时变化）。
func TestToolCellCache(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	tc := &messages.ToolCall{ID: "c", Name: "shell_command", Args: []byte(`{"command":"printf 'a\nb\nc\nd\ne\nf\ng\nh'"}`)}
	m.onToolCall(tc)
	m.onToolResult(events.Event{ToolCall: tc, ToolResult: &messages.ToolResult{
		Success: true,
		Content: "a\nb\nc\nd\ne\nf\ng\nh",
	}})
	m.refresh(true)

	item := &m.items[0]
	if item.cache.body == "" {
		t.Fatal("首次渲染应填充缓存")
	}
	collapsed := item.cache.body

	// 展开 → 失效（含 args 段）。
	m.tools[0].Collapsed = false
	renderTimeline(&m)
	if item.cache.body == collapsed || !strings.Contains(ansi.Strip(item.cache.body), "args") {
		t.Fatalf("展开后缓存应更新且含 args 段")
	}
	// 折叠恢复 → 命中初始渲染。
	m.tools[0].Collapsed = true
	renderTimeline(&m)
	if item.cache.body != collapsed {
		t.Fatal("折叠恢复后应命中初始缓存")
	}

	// 运行中：不命中缓存（Started 提前 2s，渲染应含 2.0s）。
	started := time.Now().Add(-2 * time.Second)
	runTool := &ToolStatus{Name: "shell_command", Args: []byte(`{"command":"x"}`),
		Summary: "shell_command: x", Started: started, Collapsed: true}
	m2 := New(nil)
	nm, _ = m2.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 = nm.(Model)
	m2.tools = []*ToolStatus{runTool}
	m2.items = []timelineItem{{kind: itemTool, tool: runTool}}
	m2.refresh(false)
	r1, _ := renderTimeline(&m2)
	if !strings.Contains(ansi.Strip(r1), "2.0s") {
		t.Fatalf("运行中应实时渲染耗时，got %q", ansi.Strip(r1))
	}
	// 完成 → 缓存定格耗时。
	runTool.Done = true
	runTool.Duration = time.Second
	r2, _ := renderTimeline(&m2)
	if !strings.Contains(ansi.Strip(r2), "1.0s") {
		t.Fatalf("完成后应定格耗时，got %q", ansi.Strip(r2))
	}
	cached := m2.items[0].cache.body
	renderTimeline(&m2)
	if m2.items[0].cache.body != cached {
		t.Fatal("Done 后渲染应命中缓存")
	}
}
