package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestToolStateMachine tool_call → pending，tool_result → done + 分派内容。
func TestToolStateMachine(t *testing.T) {
	m := New(nil)
	tc := &messages.ToolCall{ID: "c1", Name: "shell_command", Args: []byte(`{"command":"ls"}`)}

	nm, _ := m.Update(agentEventMsg{ev: events.Event{Type: events.EventToolCall, ToolCall: tc}})
	m = nm.(Model)
	if len(m.tools) != 1 || m.tools[0].Done || m.tools[0].Summary != "shell_command: ls" {
		t.Fatalf("tool_call 后 tools = %+v", m.tools)
	}

	res := &messages.ToolResult{Success: true, Content: "ok\n"}
	nm, _ = m.Update(agentEventMsg{ev: events.Event{Type: events.EventToolResult, ToolCall: tc, ToolResult: res}})
	m = nm.(Model)
	if !m.tools[0].Done || m.tools[0].Failed {
		t.Fatalf("tool_result 后 tools[0] = %+v", m.tools[0])
	}
	if !strings.Contains(m.tools[0].Content, "exit 0") {
		t.Fatalf("shell compact state should contain exit 0, got %q", m.tools[0].Content)
	}
}

func TestReadFileToolCanExpand(t *testing.T) {
	m := New(nil)
	tc := &messages.ToolCall{ID: "read-expand", Name: "read_file", Args: []byte(`{"path":"README.md"}`)}
	m.onToolCall(tc)
	m.onToolResult(events.Event{ToolCall: tc, ToolResult: &messages.ToolResult{Success: true, Content: "first\nsecond\nthird"}})
	if !m.tools[0].Expandable() {
		t.Fatal("read_file should retain full output for expansion")
	}
	if !strings.Contains(m.tools[0].Full, "second") {
		t.Fatalf("read_file full output = %q", m.tools[0].Full)
	}
}

func TestExpandedToolShowsCompleteArgumentsAndResult(t *testing.T) {
	ts := &ToolStatus{
		Name:      "shell_command",
		Summary:   "shell_command: go test ./...",
		Args:      []byte(`{"command":"go test ./...","timeout_ms":120000,"background":false}`),
		Done:      true,
		Content:   "exit 0",
		Full:      "all packages passed",
		Collapsed: false,
	}
	view := ansi.Strip(renderToolBlock(ts, 120, false))
	for _, want := range []string{"Arguments", `"command": "go test ./..."`, `"timeout_ms": 120000`, `"background": false`, "Result", "all packages passed"} {
		if !strings.Contains(view, want) {
			t.Errorf("expanded tool missing %q:\n%s", want, view)
		}
	}
}

func TestExpandedToolCompactsResultSpacing(t *testing.T) {
	ts := &ToolStatus{
		Name:      "shell_command",
		Summary:   "shell_command: test",
		Args:      []byte(`{"command":"test"}`),
		Done:      true,
		Full:      "\r\nfirst\r\n\r\nsecond\n \nthird\n\n",
		Collapsed: false,
	}
	view := ansi.Strip(expandedToolContent(ts))
	if strings.Contains(view, "Result\n\n") {
		t.Fatalf("Result label should be adjacent to output:\n%s", view)
	}
	if !strings.Contains(view, "first\n\nsecond\n \nthird") {
		t.Fatalf("expanded result should preserve internal blank lines:\n%s", view)
	}
}

func TestFormatToolArgsPreservesHTMLCharacters(t *testing.T) {
	formatted := formatToolArgs([]byte(`{"command":"echo a&b","query":"<tag>","quote":"a'b"}`))
	for _, escaped := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(formatted, escaped) {
			t.Fatalf("formatted args should not contain HTML escapes %q: %s", escaped, formatted)
		}
	}
	for _, want := range []string{"a&b", "<tag>", "a'b"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted args missing %q: %s", want, formatted)
		}
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

// TestToolCallSummaryKillAndBackground 验证 shell 块头对 kill/background 模式
// 的展示（ADR-038）：kill 显示目标 PID，background 加 & 后缀。
func TestToolCallSummaryKillAndBackground(t *testing.T) {
	if got := toolCallSummary("shell_command", []byte(`{"kill_pid":123}`)); got != "shell_command: kill 123" {
		t.Errorf("kill summary = %q", got)
	}
	if got := toolCallSummary("shell_command", []byte(`{"command":"python server.py","background":true}`)); got != "shell_command: python server.py &" {
		t.Errorf("background summary = %q", got)
	}
}

// TestToolBackgroundResult 验证 background 成功结果原文展示（不拼 "exit 0"——
// background 返回的是 PID+日志路径，不是命令输出完成态）。
func TestToolBackgroundResult(t *testing.T) {
	ts := &ToolStatus{Name: "shell_command", Args: []byte(`{"command":"sleep 5","background":true}`), Collapsed: true}
	res := &messages.ToolResult{Success: true, Content: "已后台启动 PID 42，日志：x.log"}
	applyToolResult(ts, res)
	if strings.Contains(ts.Content, "exit 0") {
		t.Fatalf("background 结果不应拼 exit 0: %q", ts.Content)
	}
	if !strings.Contains(ts.Content, "已后台启动 PID 42") {
		t.Fatalf("应原文展示: %q", ts.Content)
	}
}

// TestToolFailed 失败态：红色 × + 错误信息。
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

// TestEscInterrupt Esc 中断：cancel 被调 + 中断系统行显示。中断提示的 AddUser
// 已挪到 handleRunDone（Run 返回后，tool_result 配对补全后再插入 user，Bug10），
// 故 Esc 瞬间 conversation 不立即新增消息。
func TestEscInterrupt(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.running = true
	before := len(c.active.Conversation().Messages)

	nm2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm2.(Model)
	if !m.interrupted {
		t.Fatal("Esc 后应置 interrupted")
	}
	if n := len(c.active.Conversation().Messages); n != before {
		t.Fatalf("Esc 瞬间不应写 conversation（挪到 handleRunDone），before=%d after=%d", before, n)
	}
	if !strings.Contains(m.View(), "Interrupt requested") {
		t.Fatalf("View 应含中断系统行")
	}
}

// TestInterruptPromptAddedOnRunDone 中断回合结束后（runDoneMsg 处理）AddUser 中断
// 提示：conversation 末尾是 user(System) 提示，紧跟 assistant(tool_use)+tool_result
// 之后（顺序合法，Bug10）。
func TestInterruptPromptAddedOnRunDone(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.running = true

	// 模拟 Esc → 中断。
	nm2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm2.(Model)
	// 回合结束（ctx canceled）→ handleRunDone。
	nm3, _ := m.handleRunDone(runDoneMsg{err: context.Canceled})
	m = nm3.(Model)
	if len(c.active.Conversation().Messages) == 0 {
		t.Fatal("中断提示应写入 conversation")
	}
	last := c.active.Conversation().Messages[len(c.active.Conversation().Messages)-1]
	if last.Role != messages.RoleUser || !strings.Contains(last.Content, "interrupted") {
		t.Fatalf("最后一条应为中断提示，got %+v", last)
	}
	// 下一轮可继续（无未配对 tool_use）：conversation 末尾合法。
	if m.running {
		t.Fatal("handleRunDone 后应停止 running")
	}
}
