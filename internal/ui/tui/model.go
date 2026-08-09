package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// MessageItem 是消息区的一个渲染项。
type MessageItem struct {
	Role     messages.Role
	Content  string // 原始文本（assistant = markdown）
	Rendered string // md 渲染后 ANSI（Done 后；流式中 = Content）
	Thinking string // 关联 thinking（块完成折叠，W3 展开）
	Done     bool
	Err      bool
}

// StreamState 是当前流式块（delta 累积，块完成 flush 成 MessageItem）。
type StreamState struct {
	Text     string
	Thinking string
}

// StatusBar 是底部状态栏缓存（refresh 时从 session 读，View 只读缓存）。
type StatusBar struct {
	Model      string
	SessionID  string
	Permission string
	TodoCount  int
	Todos      []agentstate.TodoItem // 排序后（进行中-待办-完成），todo 常驻条用
}

// approvalPopup 是审批弹窗（输入区上方；y/s/n 直击键，Esc 拒绝 + 中断）。
type approvalPopup struct {
	req    middleware.ApprovalRequest
	respCh chan middleware.Decision
}

// Model 是 TUI 根组件（bubbletea elm：Update/View 纯函数）。
// W4：审批弹窗 + 斜杠命令弹窗 + 队列条 + 自动补全。
type Model struct {
	c       *Controller
	input   textarea.Model
	msgs    []*MessageItem
	tools   []*ToolStatus // 本批工具折叠块（消息流内插，ADR-030）
	stream  *StreamState
	queue   []string // 用户输入队列（prompt + /命令统一排队，W4）
	running bool     // 是否有回合在跑

	sp     spinner.Model
	status StatusBar
	appr   *approvalPopup // 非 nil = 审批弹窗（输入接管 y/s/n）
	sel    *selectPopup   // 非 nil = 斜杠命令弹窗选择器（↑/↓ + Enter/Esc）

	viewport viewport.Model // 消息区（滚动）
	width    int
	height   int
}

// New 构造根 Model。c 为 agent 桥（RunTUI 注入；nil 时仅 UI 壳，供测试）。
func New(c *Controller) Model {
	ta := textarea.New()
	ta.Placeholder = "输入消息…（Enter 提交 · Alt+Enter 换行）   /help 查看命令"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Line
	sp.Style = styleDim

	return Model{
		c:        c,
		input:    ta,
		sp:       sp,
		viewport: viewport.New(0, 0),
	}
}

// Init 返回初始 Cmd。
func (m Model) Init() tea.Cmd { return nil }

// Update 是纯函数 reducer：消息 → 新 state + 副作用 Cmd。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(msg.Width - 4)
		m.viewport.Width = msg.Width - 4
		m.viewport.Height = msg.Height - 7
		m.refresh()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		return m.handleAgentEvent(msg.ev)

	case approvalRequestMsg:
		m.appr = &approvalPopup{req: msg.req, respCh: msg.respCh}
		m.refresh()
		return m, nil

	case runDoneMsg:
		return m.handleRunDone(msg.err)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		if m.running {
			return m, cmd // 回合进行中继续转
		}
		return m, nil

	default:
		return m, nil
	}
}

// handleKey 处理键盘事件（焦点模型：审批弹窗 → 命令弹窗 → 普通）。
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.appr != nil {
		return m.handleApprovalKey(msg)
	}
	if m.sel != nil {
		return m.handlePopupKey(msg)
	}

	switch msg.String() {
	case "ctrl+c":
		// Ctrl+C = 复制语义（ADR-030）；剪贴板接入前 no-op。
		return m, nil
	case "esc":
		if m.running && m.c != nil {
			m.c.cancelRun()
			// 中断提示落盘（ADR-028）：resume 后模型可见，对齐 Claude Code。
			m.c.active.AddUser("（系统：上一轮 agent 运行被用户中断。如有未完成的工作，请继续；后台进程可能仍在运行。）")
			m.msgs = append(m.msgs, &MessageItem{Role: "", Content: "[系统] 已中断当前回合", Rendered: "[系统] 已中断当前回合", Done: true})
			m.refresh()
		}
		return m, nil
	}

	if msg.Type == tea.KeyEnter && !msg.Alt {
		return m.submit()
	}

	// 其余按键交给 textarea（Alt+Enter 换行、退格、光标移动）。
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handlePopupKey 处理弹窗选择器按键（↑/↓ 移动，Enter 确认，Esc 取消）。
func (m Model) handlePopupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.sel.cursor > 0 {
			m.sel.cursor--
		}
	case "down", "j":
		if m.sel.cursor < len(m.sel.items)-1 {
			m.sel.cursor++
		}
	case "enter":
		msg, err := m.confirmPopup()
		m.sel = nil
		if err != nil {
			return m.sysErr(err), nil
		}
		return m.sysOK(msg), nil
	case "esc":
		m.sel = nil
	}
	m.refresh()
	return m, nil
}

// handleApprovalKey 处理审批弹窗按键（y 允许 / s 本会话记住 / n 拒绝 / Esc 拒绝+中断）。
func (m Model) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var dec middleware.Decision
	switch strings.ToLower(msg.String()) {
	case "y":
		dec = middleware.DecisionAllow
	case "s":
		dec = middleware.DecisionAllowSession
	case "n":
		dec = middleware.DecisionDeny
	case "esc":
		// Esc：拒绝 + 中断当前回合。
		m.appr.respCh <- middleware.DecisionDeny
		m.appr = nil
		if m.running && m.c != nil {
			m.c.cancelRun()
		}
		m.refresh()
		return m, nil
	default:
		return m, nil // 弹窗接管，其它键忽略
	}
	m.appr.respCh <- dec
	m.appr = nil
	m.refresh()
	return m, nil
}

// submit 处理 Enter 提交：/exit 立即退出；回合中进队列；空闲按内容分派
// （/ 命令 → 命令处理 / 普通文本 → 启动回合，ADR-030 统一输入队列）。
func (m Model) submit() (tea.Model, tea.Cmd) {
	line := m.input.Value()
	m.input.Reset()
	if strings.TrimSpace(line) == "" {
		return m, nil
	}
	if line == "/exit" {
		return m, tea.Quit
	}
	if m.running {
		m.queue = append(m.queue, line)
		m.refresh()
		return m, nil
	}
	return m.handleInput(line)
}

// handleInput 空闲输入分派：/ 命令 → 命令处理（弹窗，落盘）；普通 → 启动回合。
func (m Model) handleInput(line string) (tea.Model, tea.Cmd) {
	if cmd, ok := parseCommandLine(line); ok {
		if m.c != nil {
			m.c.active.AddCommand(line) // 命令落盘（ADR-030：resume 呈现、不发模型）
		}
		return m.runCommand(cmd)
	}
	return m.startRun(line)
}

// startRun 启动一个回合。
func (m Model) startRun(line string) (tea.Model, tea.Cmd) {
	m.running = true
	m.refresh()
	if m.c == nil {
		return m, nil // 纯 UI 壳（测试）
	}
	return m, m.c.Run(line)
}

// handleAgentEvent 处理 agent 事件（消息增量 reducer，对齐 opencode data.tsx）。
func (m Model) handleAgentEvent(ev agent.Event) (tea.Model, tea.Cmd) {
	switch ev.Type {
	case agent.EventTurnStart:
		m.running = true
		if m.stream == nil {
			m.stream = &StreamState{}
		}
		return m, m.sp.Tick
	case agent.EventThinkingDelta:
		if m.stream == nil {
			m.stream = &StreamState{}
		}
		m.stream.Thinking += ev.Text
	case agent.EventTextDelta:
		if m.stream == nil {
			m.stream = &StreamState{}
		}
		m.stream.Text += ev.Text
	case agent.EventThinkingDone:
		if m.stream == nil {
			m.stream = &StreamState{}
		}
		m.stream.Thinking = ev.Text
	case agent.EventTextDone:
		m.flushStream()
	case agent.EventToolCall:
		m.onToolCall(ev.ToolCall)
	case agent.EventToolResult:
		m.onToolResult(ev)
	case agent.EventTurnDone:
		m.flushStream()
		m.running = false
		m.refresh()
		// 队列逐条连跑（ADR-030）：回合完成自动发下一条（/ 命令走命令处理 + 落盘）。
		if len(m.queue) > 0 && m.c != nil {
			next := m.queue[0]
			m.queue = m.queue[1:]
			if cmd, ok := parseCommandLine(next); ok {
				m.c.active.AddCommand(next)
				return m.runCommand(cmd)
			}
			return m, m.c.Run(next)
		}
		return m, nil
	case agent.EventError:
		m.running = false
		if ev.Err != nil {
			m.msgs = append(m.msgs, &MessageItem{Role: "", Content: "[错误] " + ev.Err.Error(), Rendered: "[错误] " + ev.Err.Error(), Done: true, Err: true})
		}
	}
	m.refresh()
	return m, nil
}

// onToolCall 建工具折叠块（pending）。
func (m *Model) onToolCall(tc *messages.ToolCall) {
	if tc == nil {
		return
	}
	ts := &ToolStatus{ID: tc.ID, Name: tc.Name, Args: tc.Args, Summary: toolCallSummary(tc.Name, tc.Args), Collapsed: true}
	prepareTool(ts)
	m.tools = append(m.tools, ts)
}

// onToolResult 关联 ToolCall 填充结果（按 ID；审批拒绝也发 ToolResult，ADR-029）。
func (m *Model) onToolResult(ev agent.Event) {
	if ev.ToolCall == nil {
		return
	}
	for _, ts := range m.tools {
		if ts.ID == ev.ToolCall.ID {
			if ev.ToolResult != nil {
				applyToolResult(ts, ev.ToolResult)
			}
			return
		}
	}
}

// handleRunDone 回合结束（含中断/错误）。
func (m Model) handleRunDone(err error) (tea.Model, tea.Cmd) {
	m.running = false
	if err != nil && !errors.Is(err, context.Canceled) {
		m.msgs = append(m.msgs, &MessageItem{Role: "", Content: "[错误] " + err.Error(), Rendered: "[错误] " + err.Error(), Done: true, Err: true})
	}
	m.refresh()
	return m, nil
}

// flushStream 把流式块收尾成完整消息（text_done 时 md 渲染；ADR-030 块完成渲染）。
func (m *Model) flushStream() {
	if m.stream == nil {
		return
	}
	if m.stream.Text != "" || m.stream.Thinking != "" {
		rendered := renderMarkdown(m.stream.Text, m.width)
		m.msgs = append(m.msgs, &MessageItem{
			Role:     messages.RoleAssistant,
			Content:  m.stream.Text,
			Rendered: rendered,
			Thinking: m.stream.Thinking,
			Done:     true,
		})
	}
	m.stream = nil
}

// refresh 重建消息区 + 状态栏并滚到底（Update 内调用；View 只读）。
func (m *Model) refresh() {
	m.refreshStatus()
	m.viewport.SetContent(renderMessages(m))
	m.viewport.GotoBottom()
}

// refreshStatus 从会话读状态栏缓存（View 只读缓存，保持纯函数）。
func (m *Model) refreshStatus() {
	if m.c == nil || m.c.active == nil {
		return
	}
	st := m.c.active.State()
	m.status.Model = m.c.active.Model()
	m.status.SessionID = m.c.active.ID
	m.status.Permission = ""
	if st.Permission != nil {
		m.status.Permission = st.Permission.Mode
	}
	m.status.TodoCount = len(st.Todos)
	m.status.Todos = sortTodos(st.Todos)
}
