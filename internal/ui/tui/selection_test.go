package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// TestScrollbarHiddenWhenContentFits 内容不超视口时滚动条隐藏（ADR-043 §6.2.1）。
func TestScrollbarHiddenWhenContentFits(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.appendMessage(&MessageItem{ID: "u", Role: messages.RoleUser, Content: "hi", Rendered: "hi", Done: true})
	m.refresh(true)
	if sb := m.scrollbarView(); sb != "" {
		t.Fatalf("内容不超视口时滚动条应隐藏，got %q", sb)
	}
}

// TestScrollbarThumbGeometry 拇指高度/位置比例与滚动联动。
func TestScrollbarThumbGeometry(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	for i := 0; i < 60; i++ {
		m.appendSystem(fmt.Sprintf("line %d", i), false)
	}
	m.refresh(true)
	if m.viewport.TotalLineCount() <= m.viewport.Height {
		t.Fatalf("测试前提：内容应超出视口（total=%d vis=%d）", m.viewport.TotalLineCount(), m.viewport.Height)
	}
	sb := m.scrollbarView()
	lines := strings.Split(sb, "\n")
	if len(lines) != m.viewport.Height {
		t.Fatalf("滚动条高度 %d != 视口高度 %d", len(lines), m.viewport.Height)
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w != 1 {
			t.Fatalf("滚动条列宽应为 1，got %d: %q", w, ansi.Strip(l))
		}
	}
	if !strings.Contains(ansi.Strip(sb), "█") {
		t.Fatal("滚动条应有拇指")
	}

	// 顶部 → 底部：拇指下移（refresh(true) 已贴底，先回顶再对比）。
	m.viewport.SetYOffset(0)
	top := m.scrollbarView()
	m.viewport.SetYOffset(m.viewport.TotalLineCount() - m.viewport.Height)
	if after := m.scrollbarView(); after == top {
		t.Fatal("滚动后拇指位置应变化")
	}
}

// TestScrollbarClickJumps 点击滚动条按比例跳转（底部 → max offset）。
func TestScrollbarClickJumps(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	for i := 0; i < 60; i++ {
		m.appendSystem(fmt.Sprintf("line %d", i), false)
	}
	m.refresh(true)

	maxOff := m.viewport.TotalLineCount() - m.viewport.Height
	x := m.width - 1
	y := m.mainTop + m.viewport.Height - 1 // 轨道底部
	nm, _ = m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = nm.(Model)
	if !m.scrollbarDrag {
		t.Fatal("点击滚动条应进入拖拽态")
	}
	if m.viewport.YOffset != maxOff {
		t.Fatalf("点击底部应跳到 maxOffset=%d，got %d", maxOff, m.viewport.YOffset)
	}
	nm, _ = m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = nm.(Model)
	if m.scrollbarDrag {
		t.Fatal("释放后应退出拖拽态")
	}
}

// TestSelectionDragAndCopy 拖拽产生选区 + 复制取纯文本（ADR-043 §6.7）。
func TestSelectionDragAndCopy(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.appendMessage(&MessageItem{ID: "u", Role: messages.RoleUser, Content: "第一行内容", Rendered: "第一行内容", Done: true})
	m.appendMessage(&MessageItem{ID: "a", Role: messages.RoleAssistant, Content: "hello\nworld", Rendered: "hello\nworld", Done: true})
	m.refresh(true)

	// user cell 第 0 行 → assistant 第 2 行（user 1 行 + 分隔 1 空行）。
	nm, _ = m.Update(tea.MouseMsg{X: 0, Y: m.mainTop, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = nm.(Model)
	if !m.selecting {
		t.Fatal("press 后应进入选择拖拽态")
	}
	nm, _ = m.Update(tea.MouseMsg{X: 5, Y: m.mainTop + 2, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = nm.(Model)
	if !m.hasSelection() {
		t.Fatal("拖拽后应存在选区")
	}
	text := m.selectionText()
	if !strings.Contains(text, "第一行内容") || !strings.Contains(text, "hello") {
		t.Fatalf("选区文本应含首末行内容，got %q", text)
	}

	// Esc 清除选区。
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	if m.hasSelection() {
		t.Fatal("Esc 应清除选区")
	}
}

// TestSelectionClickStillToggles 无位移点击不产生选区、保留折叠切换语义。
func TestSelectionClickStillToggles(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	tc := &messages.ToolCall{ID: "t", Name: "shell_command", Args: []byte(`{"command":"echo hi"}`)}
	m.onToolCall(tc)
	m.onToolResult(eventsEvent(tc, "output"))
	m.refresh(true)

	hit := m.hits[0]
	y := m.mainTop + hit.start - m.viewport.YOffset
	nm, _ = m.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	nm, _ = nm.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = nm.(Model)
	if m.hasSelection() {
		t.Fatal("无位移点击不应产生选区")
	}
	if m.tools[0].Collapsed {
		t.Fatal("无位移点击应切换工具块折叠（旧语义保留）")
	}
}

// TestThinkingTitleExtract 首个粗体标题抽取（codex extract_first_bold 同款）。
func TestThinkingTitleExtract(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"**制定计划**\n\n先分析一下", "制定计划"},
		{"plain reasoning without title", ""},
		{"**  多空格标题  **\n正文", "多空格标题"},
		{"", ""},
	}
	for _, c := range cases {
		if got := thinkingTitle(c.in); got != c.want {
			t.Errorf("thinkingTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFormatDuration 耗时格式。
func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   int64 // 毫秒
		want string
	}{
		{0, "0ms"},
		{800, "800ms"},
		{1250, "1.2s"},
		{1260, "1.3s"},
		{59000, "59.0s"},
		{75000, "1m15s"},
		{600000, "10m00s"},
	}
	for _, c := range cases {
		if got := formatDuration(time.Duration(c.in) * time.Millisecond); got != c.want {
			t.Errorf("formatDuration(%dms) = %q, want %q", c.in, got, c.want)
		}
	}
}

// eventsEvent 构造工具结果事件（测试快捷）。
func eventsEvent(tc *messages.ToolCall, content string) events.Event {
	return events.Event{ToolCall: tc, ToolResult: &messages.ToolResult{Success: true, Content: content}}
}
