package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/messages"
	tea "github.com/charmbracelet/bubbletea"
)

// TestToolStateMachine tool_call → pending，tool_result → done + 分派内容。
func TestToolStateMachine(t *testing.T) {
	m := New(nil)
	tc := &messages.ToolCall{ID: "c1", Name: "shell_command", Args: []byte(`{"command":"ls"}`)}

	nm, _ := m.Update(agentEventMsg{ev: agent.Event{Type: agent.EventToolCall, ToolCall: tc}})
	m = nm.(Model)
	if len(m.tools) != 1 || m.tools[0].Done || m.tools[0].Summary != "shell_command: ls" {
		t.Fatalf("tool_call 后 tools = %+v", m.tools)
	}

	res := &messages.ToolResult{Success: true, Content: "ok\n"}
	nm, _ = m.Update(agentEventMsg{ev: agent.Event{Type: agent.EventToolResult, ToolCall: tc, ToolResult: res}})
	m = nm.(Model)
	if !m.tools[0].Done || m.tools[0].Failed {
		t.Fatalf("tool_result 后 tools[0] = %+v", m.tools[0])
	}
	if !strings.Contains(m.tools[0].Content, "exit 0") {
		t.Fatalf("shell compact state should contain exit 0, got %q", m.tools[0].Content)
	}
}

// TestToolDispatchReadFile read_file 仅元信息（不渲染内容）。
func TestToolDispatchReadFile(t *testing.T) {
	ts := &ToolStatus{Name: "read_file", Args: []byte(`{"path":"a.txt"}`), Collapsed: true}
	res := &messages.ToolResult{Success: true, Content: "l1\nl2\nl3"}
	applyToolResult(ts, res)
	if !strings.Contains(ts.Content, "a.txt") || !strings.Contains(ts.Content, "3 lines") {
		t.Fatalf("read_file 折叠态 = %q", ts.Content)
	}
	if strings.Contains(ts.Content, "l1") {
		t.Fatalf("read_file 不应渲染文件内容: %q", ts.Content)
	}
}

// TestToolWriteFileDiff 覆盖场景显示 gotextdiff（改了什么行）。
func TestToolWriteFileDiff(t *testing.T) {
	ts := &ToolStatus{
		Name:       "write_file",
		Args:       []byte(`{"path":"x.txt","content":"line1\nchanged\n"}`),
		oldContent: "line1\nline2\n",
		Collapsed:  true,
	}
	res := &messages.ToolResult{Success: true, Content: "Write File: x.txt"}
	applyToolResult(ts, res)
	if !strings.Contains(ts.Content, "updated x.txt") {
		t.Fatalf("覆盖场景应标覆盖，got %q", ts.Content)
	}
	if !strings.Contains(ts.Content, "+changed") || !strings.Contains(ts.Content, "-line2") {
		t.Fatalf("覆盖 diff 应含 +/- 行，got %q", ts.Content)
	}
}

// TestToolWriteFileNew 新建无 diff。
func TestToolWriteFileNew(t *testing.T) {
	ts := &ToolStatus{Name: "write_file", Args: []byte(`{"path":"new.txt","content":"hi\n"}`), Collapsed: true}
	res := &messages.ToolResult{Success: true, Content: "Write File: new.txt"}
	applyToolResult(ts, res)
	if !strings.Contains(ts.Content, "created new.txt") || strings.Contains(ts.Content, "+") {
		t.Fatalf("新建应无 diff，got %q", ts.Content)
	}
}

// TestToolApplyPatchDiff apply_patch 折叠态含 patch 提取的 diff 行。
func TestToolApplyPatchDiff(t *testing.T) {
	patch := "@@ -1 +1 @@\n-foo\n+bar\n"
	args, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	ts := &ToolStatus{Name: "apply_patch", Args: args, Collapsed: true}
	res := &messages.ToolResult{Success: true, Content: "Update File: main.go"}
	applyToolResult(ts, res)
	if !strings.Contains(ts.Content, "Update File") || !strings.Contains(ts.Content, "-foo") || !strings.Contains(ts.Content, "+bar") {
		t.Fatalf("apply_patch 块 = %q", ts.Content)
	}
}

// TestToolListDir list_dir 折叠态 = 计数 + 前几项纯名称。
func TestToolListDir(t *testing.T) {
	ts := &ToolStatus{Name: "list_dir", Args: []byte(`{"path":"."}`), Collapsed: true}
	res := &messages.ToolResult{Success: true, Content: "dir\ta\nfile\tb\nfile\tc\n"}
	applyToolResult(ts, res)
	if !strings.Contains(ts.Content, "3 items") || !strings.Contains(ts.Content, "a b c") {
		t.Fatalf("list_dir 折叠态 = %q", ts.Content)
	}
}

// TestToolFailed 失败态：红 [ERR] + 错误信息。
func TestToolFailed(t *testing.T) {
	ts := &ToolStatus{Name: "shell_command", Args: []byte(`{"command":"rm -rf x"}`), Collapsed: true}
	res := &messages.ToolResult{Success: false, Content: "shell_command: 权限拒绝"}
	applyToolResult(ts, res)
	if !ts.Failed || !strings.Contains(ts.Content, "权限拒绝") {
		t.Fatalf("失败态 = %+v", ts)
	}
}

// TestViewToolBlock View 应含工具块头（需先设置终端尺寸，viewport 才有高度）。
func TestViewToolBlock(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	tool := &ToolStatus{ID: "c1", Summary: "shell_command: ls", Done: true, Content: "exit 0\nout", Collapsed: true}
	m.tools = []*ToolStatus{tool}
	m.items = []timelineItem{{kind: itemTool, tool: tool}}
	m.refresh()
	if !strings.Contains(m.View(), "shell_command: ls") {
		t.Fatalf("View 应含工具块头")
	}
}

// TestEscInterrupt Esc 中断：cancel 被调 + 中断提示 AddUser 落盘。
func TestEscInterrupt(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.running = true

	nm2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm2.(Model)
	// 中断提示应已 AddUser（conversation + transcript）。
	if n := len(c.active.Conversation().Messages); n == 0 {
		t.Fatal("中断提示应写入 conversation")
	}
	last := c.active.Conversation().Messages[len(c.active.Conversation().Messages)-1]
	if !strings.Contains(last.Content, "interrupted") {
		t.Fatalf("最后一条应为中断提示，got %q", last.Content)
	}
	if !strings.Contains(m.View(), "Interrupt requested") {
		t.Fatalf("View 应含中断系统行")
	}
}
