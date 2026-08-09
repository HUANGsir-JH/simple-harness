package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type itemKind uint8

const (
	itemMessage itemKind = iota
	itemTool
)

type focusMode uint8

const (
	focusComposer focusMode = iota
	focusTimeline
)

type hitKind uint8

const (
	hitTool hitKind = iota
	hitThinking
)

// MessageItem is one conversational cell in the timeline.
type MessageItem struct {
	ID               string
	Role             messages.Role
	Content          string
	Rendered         string
	Thinking         string
	ThinkingExpanded bool
	Done             bool
	Err              bool
}

// StreamState contains the currently streaming assistant block.
type StreamState struct {
	MsgID    string
	Text     string
	Thinking string
}

// StatusBar is a rendering snapshot of the active session state.
type StatusBar struct {
	Model          string
	SessionID      string
	Permission     string
	ThinkingEffort string
	TodoCount      int
	Todos          []agentstate.TodoItem
}

type timelineItem struct {
	kind itemKind
	msg  *MessageItem
	tool *ToolStatus
}

type hitTarget struct {
	kind    hitKind
	message *MessageItem
	tool    *ToolStatus
	start   int
	end     int
}

type approvalPopup struct {
	req    middleware.ApprovalRequest
	respCh chan middleware.Decision
}

// Model owns only UI state. Controller contains runtime side effects.
type Model struct {
	c *Controller

	input    textarea.Model
	viewport viewport.Model
	sp       spinner.Model

	items  []timelineItem
	msgs   []*MessageItem // kept as a direct index for state tests and lookup
	tools  []*ToolStatus  // kept as a direct index for tool result lookup
	stream *StreamState
	queue  []string

	running     bool
	turnDone    bool
	interrupted bool
	eventError  bool
	focus       focusMode

	inputHistory []string
	historyPos   int
	draft        string
	completion   int

	status       StatusBar
	appr         *approvalPopup
	sel          *selectPopup
	help         bool
	toast        string
	showThinking bool

	hits         []hitTarget
	selectedHit  int
	autoScroll   bool
	width        int
	height       int
	mainTop      int
	composerTop  int
	contentWidth int
	renderWidth  int
}

func New(c *Controller) Model {
	ta := textarea.New()
	ta.Placeholder = "Ask anything or type / for commands"
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 5
	ta.SetWidth(76)
	ta.SetHeight(3)
	ta.FocusedStyle.CursorLine = textarea.Style{}.CursorLine
	ta.BlurredStyle = ta.FocusedStyle
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Line
	sp.Style = styleRunning

	vp := viewport.New(80, 12)
	vp.MouseWheelEnabled = false // mouse routing is centralized in Model
	vp.MouseWheelDelta = 3

	return Model{
		c:            c,
		input:        ta,
		viewport:     vp,
		sp:           sp,
		focus:        focusComposer,
		historyPos:   -1,
		completion:   -1,
		selectedHit:  -1,
		autoScroll:   true,
		showThinking: true,
		width:        80,
		height:       24,
	}
}

func (m Model) Init() tea.Cmd { return textarea.Blink }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.refresh(true)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case agentEventMsg:
		return m.handleAgentEvent(msg.ev)
	case approvalRequestMsg:
		m.appr = &approvalPopup{req: msg.req, respCh: msg.respCh}
		m.input.Blur()
		m.refresh(false)
		return m, nil
	case runDoneMsg:
		return m.handleRunDone(msg.err)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		if m.running {
			return m, cmd
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.appr != nil {
		return m.handleApprovalKey(msg)
	}
	if m.sel != nil {
		return m.handlePopupKey(msg)
	}
	if m.help {
		if msg.String() == "esc" || msg.String() == "enter" || msg.String() == "/" {
			m.help = false
			m.restoreFocus()
			m.refresh(false)
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		if value := m.input.Value(); value != "" {
			if err := clipboard.WriteAll(value); err == nil {
				m.toast = "Composer copied"
			} else {
				m.toast = "Clipboard unavailable"
			}
			m.refresh(false)
		}
		return m, nil
	case "esc":
		if m.running && m.c != nil {
			m.requestInterrupt()
		}
		return m, nil
	case "tab":
		if m.completionVisible() {
			m.acceptCompletion()
			return m, nil
		}
		m.toggleFocus()
		m.refresh(false)
		return m, nil
	case "pgup":
		m.viewport.PageUp()
		m.autoScroll = false
		return m, nil
	case "pgdown":
		m.viewport.PageDown()
		m.autoScroll = m.viewport.AtBottom()
		return m, nil
	case "home":
		if m.focus == focusTimeline {
			m.viewport.GotoTop()
			m.autoScroll = false
			return m, nil
		}
	case "end":
		if m.focus == focusTimeline {
			m.viewport.GotoBottom()
			m.autoScroll = true
			return m, nil
		}
	}

	if m.focus == focusTimeline {
		return m.handleTimelineKey(msg)
	}
	return m.handleComposerKey(msg)
}

func (m Model) handleComposerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.completionVisible() {
		switch msg.String() {
		case "up":
			m.moveCompletion(-1)
			return m, nil
		case "down":
			m.moveCompletion(1)
			return m, nil
		}
	}
	if m.input.Value() == "" {
		switch msg.String() {
		case "up":
			m.recallHistory(-1)
			return m, nil
		case "down":
			m.recallHistory(1)
			return m, nil
		}
	}
	if msg.String() == "shift+enter" || msg.String() == "alt+enter" {
		m.input.InsertRune('\n')
		m.refresh(false)
		return m, nil
	}
	if msg.Type == tea.KeyEnter && !msg.Alt {
		if m.completionVisible() {
			m.acceptCompletion()
			return m, nil
		}
		return m.submit()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.historyPos = -1
	m.completion = normalizeCompletion(m.input.Value(), m.completion)
	m.layout()
	m.refresh(false)
	return m, cmd
}

func (m Model) handleTimelineKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.moveSelectedHit(-1)
	case "down", "j":
		m.moveSelectedHit(1)
	case "enter", "space":
		m.toggleSelectedHit()
	}
	return m, nil
}

func (m *Model) moveSelectedHit(delta int) {
	if len(m.hits) == 0 {
		return
	}
	if m.selectedHit < 0 || m.selectedHit >= len(m.hits) {
		m.selectedHit = 0
	}
	m.selectedHit = (m.selectedHit + delta + len(m.hits)) % len(m.hits)
	hit := m.hits[m.selectedHit]
	if hit.start < m.viewport.YOffset {
		m.viewport.SetYOffset(hit.start)
		m.autoScroll = false
	} else if hit.end >= m.viewport.YOffset+m.viewport.Height {
		m.viewport.SetYOffset(hit.end - m.viewport.Height + 1)
		m.autoScroll = m.viewport.AtBottom()
	}
	m.refresh(false)
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	ev := tea.MouseEvent(msg)
	switch ev.Button {
	case tea.MouseButtonWheelUp:
		m.viewport.ScrollUp(3)
		m.autoScroll = false
		return m, nil
	case tea.MouseButtonWheelDown:
		m.viewport.ScrollDown(3)
		m.autoScroll = m.viewport.AtBottom()
		return m, nil
	}
	if ev.Button != tea.MouseButtonLeft || ev.Action != tea.MouseActionPress {
		return m, nil
	}
	if ev.Y >= m.composerTop {
		m.setFocus(focusComposer)
		m.refresh(false)
		return m, nil
	}
	if ev.Y >= m.mainTop && ev.Y < m.mainTop+m.viewport.Height && m.appr == nil && m.sel == nil && !m.help {
		m.setFocus(focusTimeline)
		contentY := m.viewport.YOffset + ev.Y - m.mainTop
		for i, hit := range m.hits {
			if contentY >= hit.start && contentY <= hit.end {
				m.selectedHit = i
				m.toggleHit(hit)
				break
			}
		}
		m.refresh(false)
	}
	return m, nil
}

func (m Model) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var decision middleware.Decision
	switch strings.ToLower(msg.String()) {
	case "y":
		decision = middleware.DecisionAllow
	case "s":
		decision = middleware.DecisionAllowSession
	case "n":
		decision = middleware.DecisionDeny
	case "esc":
		decision = middleware.DecisionDeny
		if m.running && m.c != nil {
			m.requestInterrupt()
		}
	default:
		return m, nil
	}
	m.appr.respCh <- decision
	m.appr = nil
	m.restoreFocus()
	m.refresh(false)
	return m, nil
}

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
		message, err := m.confirmPopup()
		m.sel = nil
		m.restoreFocus()
		if err != nil {
			return m.sysErr(err), nil
		}
		return m.sysOK(message), nil
	case "esc":
		m.sel = nil
		m.restoreFocus()
	}
	m.refresh(false)
	return m, nil
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	if line == "" {
		return m, nil
	}
	m.input.Reset()
	m.completion = -1
	m.historyPos = -1
	m.draft = ""
	m.inputHistory = append(m.inputHistory, line)
	if line == "/exit" && !m.running {
		if m.c != nil {
			m.c.active.AddCommand(line)
		}
		m.queue = nil
		return m, tea.Quit
	}
	if m.running {
		m.queue = append(m.queue, line)
		m.toast = "Queued"
		m.layout()
		m.refresh(false)
		return m, nil
	}
	return m.handleInput(line)
}

func (m Model) handleInput(line string) (tea.Model, tea.Cmd) {
	if cmd, ok := parseCommandLine(line); ok {
		if m.c != nil {
			m.c.active.AddCommand(line)
		}
		return m.runCommand(cmd)
	}
	return m.startRun(line)
}

func (m Model) startRun(line string) (tea.Model, tea.Cmd) {
	m.appendMessage(&MessageItem{Role: messages.RoleUser, Content: line, Rendered: line, Done: true})
	m.running = true
	m.turnDone = false
	m.eventError = false
	m.toast = ""
	m.refresh(true)
	if m.c == nil {
		return m, nil
	}
	return m, m.c.Run(line)
}

func (m Model) handleAgentEvent(ev agent.Event) (tea.Model, tea.Cmd) {
	switch ev.Type {
	case agent.EventTurnStart:
		m.running = true
		m.turnDone = false
		m.ensureStream(ev.MsgID)
		return m, m.sp.Tick
	case agent.EventThinkingDelta:
		m.ensureStream(ev.MsgID)
		m.stream.Thinking += ev.Text
	case agent.EventTextDelta:
		m.ensureStream(ev.MsgID)
		m.stream.Text += ev.Text
	case agent.EventThinkingDone:
		m.ensureStream(ev.MsgID)
		m.stream.Thinking = ev.Text
	case agent.EventTextDone:
		m.ensureStream(ev.MsgID)
		if ev.Text != "" {
			m.stream.Text = ev.Text
		}
		m.flushStream()
	case agent.EventToolCall:
		m.flushStream()
		m.onToolCall(ev.ToolCall)
	case agent.EventToolResult:
		m.onToolResult(ev)
	case agent.EventTurnDone:
		m.flushStream()
		m.turnDone = true
	case agent.EventError:
		m.eventError = true
		if ev.Err != nil {
			m.appendSystem("Error: "+ev.Err.Error(), true)
		}
	}
	m.refresh(true)
	return m, nil
}

func (m Model) handleRunDone(err error) (tea.Model, tea.Cmd) {
	m.running = false
	m.turnDone = false
	if errors.Is(err, context.Canceled) {
		if !m.interrupted {
			m.appendSystem("Turn interrupted", false)
		}
	} else if err != nil && !m.eventError {
		m.appendSystem("Error: "+err.Error(), true)
	}
	m.interrupted = false
	m.eventError = false
	m.refresh(true)
	if len(m.queue) == 0 {
		return m, nil
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	if next == "/exit" {
		if m.c != nil {
			m.c.active.AddCommand(next)
		}
		m.queue = nil
		return m, tea.Quit
	}
	return m.handleInput(next)
}

func (m *Model) ensureStream(msgID string) {
	if m.stream == nil {
		m.stream = &StreamState{MsgID: msgID}
	}
}

func (m *Model) flushStream() {
	if m.stream == nil {
		return
	}
	if m.stream.Text != "" || m.stream.Thinking != "" {
		m.appendMessage(&MessageItem{
			ID:       m.stream.MsgID,
			Role:     messages.RoleAssistant,
			Content:  m.stream.Text,
			Rendered: renderMarkdown(m.stream.Text, m.contentWidth),
			Thinking: m.stream.Thinking,
			Done:     true,
		})
	}
	m.stream = nil
}

func (m *Model) appendMessage(msg *MessageItem) {
	m.msgs = append(m.msgs, msg)
	m.items = append(m.items, timelineItem{kind: itemMessage, msg: msg})
}

func (m *Model) appendSystem(content string, isErr bool) {
	m.appendMessage(&MessageItem{Content: content, Rendered: content, Done: true, Err: isErr})
}

func (m *Model) onToolCall(tc *messages.ToolCall) {
	if tc == nil {
		return
	}
	ts := &ToolStatus{ID: tc.ID, Name: tc.Name, Args: tc.Args, Summary: toolCallSummary(tc.Name, tc.Args), Collapsed: true}
	prepareTool(ts)
	m.tools = append(m.tools, ts)
	m.items = append(m.items, timelineItem{kind: itemTool, tool: ts})
}

func (m *Model) onToolResult(ev agent.Event) {
	if ev.ToolCall == nil || ev.ToolResult == nil {
		return
	}
	for _, ts := range m.tools {
		if ts.ID == ev.ToolCall.ID {
			applyToolResult(ts, ev.ToolResult)
			return
		}
	}
}

func (m *Model) refresh(follow ...bool) {
	shouldFollow := len(follow) > 0 && follow[0]
	wasBottom := m.viewport.AtBottom() || m.viewport.TotalLineCount() == 0
	m.refreshStatus()
	m.layout()
	if m.renderWidth != m.contentWidth {
		for _, message := range m.msgs {
			if message.Role == messages.RoleAssistant && message.Content != "" && message.Done {
				message.Rendered = renderMarkdown(message.Content, m.contentWidth)
			}
		}
		m.renderWidth = m.contentWidth
	}
	content, hits := renderTimeline(m)
	m.hits = hits
	m.viewport.SetContent(content)
	if shouldFollow && (m.autoScroll || wasBottom) {
		m.viewport.GotoBottom()
		m.autoScroll = true
	}
	if len(m.hits) == 0 {
		m.selectedHit = -1
	} else if m.selectedHit < 0 || m.selectedHit >= len(m.hits) {
		m.selectedHit = len(m.hits) - 1
	}
}

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
	m.status.ThinkingEffort = st.ThinkingEffort
	m.status.TodoCount = len(st.Todos)
	m.status.Todos = sortTodos(st.Todos)
}

func (m *Model) toggleFocus() {
	if m.focus == focusComposer {
		m.setFocus(focusTimeline)
	} else {
		m.setFocus(focusComposer)
	}
}

func (m *Model) setFocus(f focusMode) {
	m.focus = f
	if f == focusComposer {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
}

func (m *Model) restoreFocus() { m.setFocus(m.focus) }

func (m *Model) toggleSelectedHit() {
	if m.selectedHit >= 0 && m.selectedHit < len(m.hits) {
		m.toggleHit(m.hits[m.selectedHit])
		m.refresh(false)
	}
}

func (m *Model) toggleHit(hit hitTarget) {
	switch hit.kind {
	case hitTool:
		if hit.tool != nil && hit.tool.Expandable() {
			hit.tool.Collapsed = !hit.tool.Collapsed
		}
	case hitThinking:
		if hit.message != nil {
			hit.message.ThinkingExpanded = !hit.message.ThinkingExpanded
		}
	}
}

func (m *Model) requestInterrupt() {
	if m.interrupted {
		return
	}
	m.interrupted = true
	m.c.cancelRun()
	m.c.active.AddUser("(System: the previous agent turn was interrupted by the user. Continue unfinished work if needed; background processes may still be running.)")
	m.appendSystem("Interrupt requested", false)
	m.toast = "Interrupting current turn"
	m.refresh(false)
}
