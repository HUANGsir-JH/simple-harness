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
	"github.com/muesli/termenv"
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

func TestReadFileToolInline(t *testing.T) {
	m := New(nil)
	tc := &messages.ToolCall{ID: "read-expand", Name: "read_file", Args: []byte(`{"path":"README.md"}`)}
	m.onToolCall(tc)
	m.onToolResult(events.Event{ToolCall: tc, ToolResult: &messages.ToolResult{Success: true, Content: "first\nsecond\nthird"}})
	// ADR-043 双形态：read_file 为 Inline 单行（元信息），不再折叠展开。
	if !m.tools[0].Inline {
		t.Fatal("read_file 应为 Inline 单行形态")
	}
	if m.tools[0].Expandable() {
		t.Fatal("Inline 形态不可展开")
	}
	if !strings.Contains(m.tools[0].InlineBody, "README.md") || !strings.Contains(m.tools[0].InlineBody, "3 lines") {
		t.Fatalf("read_file 单行摘要 = %q", m.tools[0].InlineBody)
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

// TestToolDurationLiveAndDone 调用耗时：live 回合打点（运行中实时、结束后定格）；
// resume 历史重建不设 Started → 不显示耗时（ADR-043 用户追加需求）。
func TestToolDurationLiveAndDone(t *testing.T) {
	m := New(nil)
	m.running = true
	tc := &messages.ToolCall{ID: "dur", Name: "shell_command", Args: []byte(`{"command":"sleep 1"}`)}
	m.onToolCall(tc)
	if m.tools[0].Started.IsZero() {
		t.Fatal("live 回合 ToolCall 应打点 Started")
	}
	if got := toolDuration(m.tools[0]); got == "" {
		t.Fatal("运行中应显示实时耗时")
	}
	m.onToolResult(events.Event{ToolCall: tc, ToolResult: &messages.ToolResult{Success: true, Content: "ok"}})
	if m.tools[0].Duration <= 0 {
		t.Fatal("ToolResult 应结算 Duration")
	}
	if got, want := toolDuration(m.tools[0]), formatDuration(m.tools[0].Duration); got != want {
		t.Fatalf("结束后耗时 = %q, want %q", got, want)
	}

	// 历史重建（running=false）：不打点、不显示。
	m2 := New(nil)
	m2.onToolCall(tc)
	if !m2.tools[0].Started.IsZero() || m2.tools[0].Duration != 0 {
		t.Fatal("历史重建不应打点耗时")
	}
}

// TestExpandedToolShowsFullArgs 展开态完整展示 args（不截断，用户追加需求）：
// 头部摘要截断的块头之外，展开渲染必须包含完整命令原文。
func TestExpandedToolShowsFullArgs(t *testing.T) {
	longCmd := "echo " + strings.Repeat("很长的命令参数", 10)
	ts := &ToolStatus{
		Name:      "shell_command",
		Args:      []byte(`{"command":"` + longCmd + `"}`),
		Summary:   toolCallSummary("shell_command", []byte(`{"command":"`+longCmd+`"}`)),
		Done:      true,
		Content:   "exit 0\noutput",
		Full:      "exit 0\noutput",
		Collapsed: false,
	}
	render := renderToolBlock(ts, 80, false)
	plain := ansiStripForTest(render)
	// args 段会按宽度 Hardwrap（换行+缩进，CJK 可能逐字断行），且块渲染在每
	// 行行首有左竖线边框——归一化需同时去掉边框字符与全部空白。
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, "|", "")), "") }
	if !strings.Contains(norm(plain), norm(longCmd)) {
		t.Fatalf("展开态应含完整命令参数（不截断），render:\n%s", plain)
	}
	if !strings.Contains(plain, "args") {
		t.Fatalf("展开态应有 args 段，render:\n%s", plain)
	}
}

// TestInlineToolRendering Inline 单行形态：徽章 + 摘要 + 无折叠提示。
func TestInlineToolRendering(t *testing.T) {
	ts := &ToolStatus{
		Name: "list_dir", Args: []byte(`{"path":"."}`), Summary: "list_dir .",
		Done: true, Content: "3 items  a b c", Full: "a\nb\nc",
		Inline: true, InlineBody: "3 items  a b c", Collapsed: true,
	}
	render := renderToolBlock(ts, 80, false)
	plain := ansiStripForTest(render)
	if !strings.Contains(plain, "[OK]") || !strings.Contains(plain, "list_dir .") || !strings.Contains(plain, "3 items") {
		t.Fatalf("Inline 渲染应含徽章+摘要，got:\n%s", plain)
	}
	if strings.Contains(plain, "collapsed") || strings.Contains(plain, "+ lines") {
		t.Fatalf("Inline 形态不应有折叠提示，got:\n%s", plain)
	}
}

// ansiStripForTest 测试用 ANSI 剥离。
func ansiStripForTest(s string) string { return ansi.Strip(s) }

// TestPrettyArgs 参数 JSON 缩进排版 + 非 JSON 兜底原文。
func TestPrettyArgs(t *testing.T) {
	got := prettyArgs([]byte(`{"path":"a.txt","command":"ls"}`))
	if !strings.Contains(got, "\n") || !strings.Contains(got, "\"path\"") {
		t.Fatalf("JSON 参数应缩进排版，got %q", got)
	}
	if want := "not-json"; prettyArgs([]byte(want)) != want {
		t.Fatalf("非 JSON 参数应兜底原文")
	}
	if prettyArgs(nil) != "" {
		t.Fatalf("空参数应返回空串")
	}
}

// TestHighlightFileContent 代码高亮输出 ANSI 颜色（256 色档案下非空且含转义）。
func TestHighlightFileContent(t *testing.T) {
	withColorProfile(t, termenv.ANSI256)
	out := highlightFileContent("main.go", "package main\n\nfunc main() {}\n")
	if out == "" {
		t.Fatal("高亮输出不应为空")
	}
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("高亮输出应含 ANSI 颜色序列，got %q", out)
	}
	// 未知语言走 chroma Fallback lexer（仍带默认前景色），内容必须完整保留。
	if got := ansi.Strip(highlightFileContent("unknown.zzz", "plain text")); got != "plain text" {
		t.Fatalf("语言未识别内容应完整保留，got %q", got)
	}
}
