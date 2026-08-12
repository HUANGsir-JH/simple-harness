package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// TestUpdateExit 提交 /exit 应返回 Quit。
func TestUpdateExit(t *testing.T) {
	m := New(nil)
	m.input.SetValue("/exit")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("提交 /exit 应返回非 nil cmd（tea.Quit）")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd 应产生 QuitMsg，got %T", cmd())
	}
}

// TestUpdateEnterClearsInput Enter 提交后输入区应清空。
func TestUpdateEnterClearsInput(t *testing.T) {
	m := New(nil)
	m.input.SetValue("hello")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := nm.(Model).input.Value(); v != "" {
		t.Fatalf("提交后输入区应清空，got %q", v)
	}
}

// TestUpdateAltEnterAddsNewline Alt+Enter adds a newline without submitting.
func TestUpdateAltEnterAddsNewline(t *testing.T) {
	m := New(nil)
	m.input.SetValue("line1")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if v := nm.(Model).input.Value(); v != "line1\n" {
		t.Fatalf("Alt+Enter should insert a newline, got %q", v)
	}
}

// TestViewContainsInput View 应渲染输入区（含占位符）。
func TestViewContainsInput(t *testing.T) {
	m := New(nil)
	if !strings.Contains(m.View(), "Ask anything") {
		t.Fatalf("View 应包含输入区占位符")
	}
}

func TestFocusAndInputHistory(t *testing.T) {
	m := New(nil)
	m.inputHistory = []string{"first", "second"}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = nm.(Model)
	if got := m.input.Value(); got != "second" {
		t.Fatalf("history up = %q, want second", got)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(Model)
	if m.focus != focusTimeline || m.input.Focused() {
		t.Fatal("Tab should move focus to timeline")
	}
}

func TestCommandCompletion(t *testing.T) {
	m := New(nil)
	m.input.SetValue("/mo")
	m.completion = 0
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(Model)
	if got := m.input.Value(); got != "/model " {
		t.Fatalf("completion = %q, want /model ", got)
	}
}

func TestEnterAcceptsCommandCompletion(t *testing.T) {
	m := New(nil)
	m.input.SetValue("/mod")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if cmd != nil {
		t.Fatal("Enter on a partial command should accept completion without submitting")
	}
	if got := m.input.Value(); got != "/model " {
		t.Fatalf("completion = %q, want /model ", got)
	}
}

func TestExitCommandIsPersisted(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.input.SetValue("/exit")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/exit should quit")
	}
	m = nm.(Model)
	commands, err := c.active.Commands()
	if err != nil || len(commands) != 1 || commands[0] != "/exit" {
		t.Fatalf("exit command not persisted: commands=%v err=%v", commands, err)
	}
}

func TestResponsiveView(t *testing.T) {
	for _, size := range []struct{ width, height int }{{20, 8}, {40, 12}, {120, 30}} {
		m := New(nil)
		nm, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		m = nm.(Model)
		view := m.View()
		if lipgloss.Width(view) > size.width {
			t.Fatalf("view width %d exceeds terminal width %d", lipgloss.Width(view), size.width)
		}
		if lipgloss.Height(view) > size.height+1 {
			t.Fatalf("view height %d exceeds terminal height %d", lipgloss.Height(view), size.height)
		}
	}
}

func TestMouseTogglesToolBlock(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	tc := &messages.ToolCall{ID: "mouse-tool", Name: "shell_command", Args: []byte(`{"command":"echo hi"}`)}
	m.onToolCall(tc)
	m.onToolResult(events.Event{ToolCall: tc, ToolResult: &messages.ToolResult{Success: true, Content: "one\ntwo\nthree\nfour\nfive\nsix\nseven"}})
	m.refresh(true)
	if len(m.hits) != 1 || !m.tools[0].Collapsed {
		t.Fatalf("expected one collapsed tool hit, hits=%v", m.hits)
	}
	hit := m.hits[0]
	y := m.mainTop + hit.start - m.viewport.YOffset
	nm, _ = m.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = nm.(Model)
	if m.tools[0].Collapsed {
		t.Fatal("mouse click should expand tool block")
	}
}

func TestTimelineEnterTogglesThinkingAndTool(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.appendMessage(&MessageItem{
		ID:       "thinking-msg",
		Role:     messages.RoleAssistant,
		Thinking: "private reasoning",
		Content:  "answer",
		Rendered: "answer",
		Done:     true,
	})
	tc := &messages.ToolCall{ID: "timeline-tool", Name: "shell_command", Args: []byte(`{"command":"printf output"}`)}
	m.onToolCall(tc)
	m.onToolResult(events.Event{ToolCall: tc, ToolResult: &messages.ToolResult{Success: true, Content: "output"}})
	m.refresh(true)

	// The latest hit is the tool. Tab focuses the timeline and Enter expands it.
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.tools[0].Collapsed {
		t.Fatal("timeline Enter should expand the selected tool block")
	}

	// Move to the previous hit and expand the thinking block as well.
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if !m.msgs[0].ThinkingExpanded {
		t.Fatal("timeline Enter should expand the selected thinking block")
	}
}

func TestMouseTargetsExactInterleavedBlock(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 60})
	m = nm.(Model)
	first := &MessageItem{ID: "shared-message", Role: messages.RoleAssistant, Thinking: "first reasoning", Content: "first answer", Rendered: "first answer", Done: true}
	second := &MessageItem{ID: "shared-message", Role: messages.RoleAssistant, Thinking: "second reasoning", Content: "second answer", Rendered: "second answer", Done: true}
	m.appendMessage(first)
	tc := &messages.ToolCall{ID: "between-thinking", Name: "shell_command", Args: []byte(`{"command":"printf output"}`)}
	m.onToolCall(tc)
	m.onToolResult(events.Event{ToolCall: tc, ToolResult: &messages.ToolResult{Success: true, Content: "output"}})
	m.appendMessage(second)
	m.refresh(true)
	m.viewport.GotoTop()

	if len(m.hits) != 3 || m.hits[0].message != first || m.hits[1].tool != m.tools[0] || m.hits[2].message != second {
		t.Fatalf("unexpected interleaved hit targets: %+v", m.hits)
	}
	click := func(index int) {
		// hit 区间现在精确覆盖可点击块本身，无需再跳过标题行。
		hit := m.hits[index]
		y := m.mainTop + hit.start - m.viewport.YOffset
		next, _ := m.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		m = next.(Model)
	}

	click(2)
	if first.ThinkingExpanded || !second.ThinkingExpanded || !m.tools[0].Collapsed {
		t.Fatal("clicking the second thinking block toggled a different timeline item")
	}
	click(1)
	if first.ThinkingExpanded || !second.ThinkingExpanded || m.tools[0].Collapsed {
		t.Fatal("clicking the tool block toggled a thinking block")
	}
	click(0)
	if !first.ThinkingExpanded || !second.ThinkingExpanded || m.tools[0].Collapsed {
		t.Fatal("clicking the first thinking block toggled a different timeline item")
	}
}

// hit 区间必须对齐真实渲染行：hit.start 所在行应当就是该块的标题行。
// 这个断言能捕捉多块堆叠时累积的行号漂移（此前每个 cell 多算一行）。
func TestHitRangesAlignWithRenderedLines(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = nm.(Model)

	for i := 0; i < 4; i++ {
		m.appendMessage(&MessageItem{
			ID:       fmt.Sprintf("msg-%d", i),
			Role:     messages.RoleAssistant,
			Thinking: fmt.Sprintf("reasoning %d\nmore reasoning", i),
			Content:  fmt.Sprintf("answer %d", i),
			Rendered: fmt.Sprintf("answer %d", i),
			Done:     true,
		})
		tc := &messages.ToolCall{
			ID:   fmt.Sprintf("tool-%d", i),
			Name: "shell_command",
			Args: []byte(`{"command":"echo hi"}`),
		}
		m.onToolCall(tc)
		m.onToolResult(events.Event{ToolCall: tc, ToolResult: &messages.ToolResult{
			Success: true,
			Content: "one\ntwo\nthree\nfour\nfive\nsix\nseven",
		}})
	}
	m.refresh(true)

	content, hits := renderTimeline(&m)
	lines := strings.Split(content, "\n")
	if len(hits) != 8 {
		t.Fatalf("expected 8 hits, got %d", len(hits))
	}
	for i, hit := range hits {
		if hit.start < 0 || hit.end >= len(lines) || hit.start > hit.end {
			t.Fatalf("hit %d range [%d,%d] outside content of %d lines", i, hit.start, hit.end, len(lines))
		}
		head := ansi.Strip(lines[hit.start])
		switch hit.kind {
		case hitThinking:
			if !strings.Contains(head, "THINKING") {
				t.Fatalf("hit %d start line %d is %q, want THINKING header", i, hit.start, head)
			}
		case hitTool:
			if !strings.Contains(head, "[OK]") && !strings.Contains(head, "[RUN]") && !strings.Contains(head, "[ERR]") {
				t.Fatalf("hit %d start line %d is %q, want tool header", i, hit.start, head)
			}
		}
	}
	// 相邻 hit 区间不得重叠，否则点击会命中错误的块。
	for i := 1; i < len(hits); i++ {
		if hits[i].start <= hits[i-1].end {
			t.Fatalf("hit %d starts at %d, overlapping previous end %d", i, hits[i].start, hits[i-1].end)
		}
	}
}

// TestEventCompactStartSystemLine 验证压缩开始通知（ADR-037 扩展）：自动压缩
// 开始事件 → 系统行"正在压缩上下文…"（与完成行"上下文已压缩"配对）。
func TestEventCompactStartSystemLine(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)

	nm, _ = m.Update(agentEventMsg{ev: events.Event{Type: events.EventCompactStart}})
	m = nm.(Model)
	if view := m.View(); !strings.Contains(view, "正在压缩上下文…") {
		t.Errorf("View 应含压缩开始系统行，got:\n%s", view)
	}
}
