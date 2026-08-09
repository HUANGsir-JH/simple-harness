package tui

import (
	"strings"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/messages"
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

// Model 是 TUI 根组件（bubbletea elm：Update/View 纯函数）。
// W2：消息区 + 输入区 + md 渲染 + 回合启动/中断 + 队列基础版。
// W3 起：tools（工具折叠块）/ status（状态栏）；W4：queue 条 UI / 审批 / 命令。
type Model struct {
	c       *Controller
	input   textarea.Model
	msgs    []*MessageItem
	stream  *StreamState
	queue   []string // 用户输入队列（prompt；斜杠命令 W4 统一进队列）
	running bool     // 是否有回合在跑

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
	return Model{
		c:        c,
		input:    ta,
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
		m.viewport.Height = msg.Height - 6
		m.refresh()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		return m.handleAgentEvent(msg.ev)

	case runDoneMsg:
		return m.handleRunDone(msg.err)

	default:
		return m, nil
	}
}

// handleKey 处理键盘事件（焦点模型 W3 扩展：弹窗/消息区）。
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		// Ctrl+C = 复制语义（ADR-030）；剪贴板接入前 no-op。
		return m, nil
	case "esc":
		if m.running && m.c != nil {
			m.c.cancelRun() // 中断当前回合；中断提示 W3 加
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

// submit 处理 Enter 提交：/exit 退出；/命令走命令处理（W4）；其余进队列或启动回合。
func (m Model) submit() (tea.Model, tea.Cmd) {
	line := m.input.Value()
	m.input.Reset()
	if strings.TrimSpace(line) == "" {
		return m, nil
	}
	switch line {
	case "/exit":
		return m, tea.Quit
	case "/help":
		m.msgs = append(m.msgs, &MessageItem{Role: messages.RoleUser, Content: line, Rendered: line, Done: true})
		m.msgs = append(m.msgs, &MessageItem{Role: "", Content: "命令: /switch /model /effort /permission /help /exit（W4 弹窗选择器）", Rendered: "命令: /switch /model /effort /permission /help /exit", Done: true})
		m.refresh()
		return m, nil
	}

	// 回合中 → 进队列（队列条 UI W4）；空闲 → 启动回合。
	if m.running {
		m.queue = append(m.queue, line)
		m.refresh()
		return m, nil
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
		// 工具折叠块 W3 实现；此处仅显示调用行占位。
		m.msgs = append(m.msgs, &MessageItem{Role: messages.RoleAssistant, Content: "[工具] " + toolCallSummary(ev.ToolCall), Rendered: "[工具] " + toolCallSummary(ev.ToolCall), Done: true})
	case agent.EventToolResult:
		// W3 工具块填充。
	case agent.EventTurnDone:
		m.flushStream()
		m.running = false
		// 队列逐条连跑（ADR-030）：回合完成自动发下一条。
		if len(m.queue) > 0 && m.c != nil {
			next := m.queue[0]
			m.queue = m.queue[1:]
			m.refresh()
			return m, m.c.Run(next)
		}
	case agent.EventError:
		m.running = false
		if ev.Err != nil {
			m.msgs = append(m.msgs, &MessageItem{Role: "", Content: "[错误] " + ev.Err.Error(), Rendered: "[错误] " + ev.Err.Error(), Done: true, Err: true})
		}
	}
	m.refresh()
	return m, nil
}

// handleRunDone 回合结束（含中断/错误）。
func (m Model) handleRunDone(err error) (tea.Model, tea.Cmd) {
	m.running = false
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

// refresh 重建消息区内容并滚到底（Update 内调用；View 只读 viewport）。
func (m *Model) refresh() {
	m.viewport.SetContent(renderMessages(m))
	m.viewport.GotoBottom()
}
