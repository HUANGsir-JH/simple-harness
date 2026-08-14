package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
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
	// ThinkingDuration 是 thinking 块耗时（首个增量 → 块完成，ADR-043 用户
	// 追加需求，纯 UI 侧计时；历史 resume 无时间戳时为 0 不展示）。
	ThinkingDuration time.Duration
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
	SessionName    string // 会话名（header 展示；空则短 ID 兜底）
	SessionID      string
	Permission     string
	ThinkingEffort string
	PlanMode       bool // [PLAN] 状态栏标记（ADR-036）
	TodoCount      int
	Todos          []agentstate.TodoItem
	// ContextTokens / ContextWindow 是当前上下文占用（token）与模型窗口
	// （ADR-037 用量展示 footer：`ctx 128k/1.0M`）。ContextTokens 来自
	// AgentState.LastContextTokens（UsageMiddleware 每轮更新，实时）。
	ContextTokens int64
	ContextWindow int
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

// askPopup 是提问弹窗状态（ADR-036，Ask 方法）。req 是要展示的问题/选项，
// respCh 回送用户回答（Selection/Custom）。cursor 高亮选项；selected 多选勾选；
// custom 是 Other 自定义文本输入缓冲（用户直接打字追加）。
type askPopup struct {
	req      middleware.AskRequest
	respCh   chan middleware.AskResult
	cursor   int
	selected []bool
	custom   string
}

// overlayKind 是全屏覆盖层的互斥类型（Bug10）。
type overlayKind uint8

const (
	overlayApproval overlayKind = iota
	overlaySelect
	overlayHelp
	overlayAsk
)

// overlay 是当前唯一的全屏覆盖层（审批 / 选择弹窗 / 帮助 / 提问）。nil = 无覆盖层；
// 非 nil 时按 kind 分派，一次只能有一个。原 appr/sel/help 三字段可同时为真
// （审批未决时队列命令可开出第二层弹窗，Bug10），收成单字段后非法组合在
// 类型层面不可表达，渲染与按键分发各自只剩一处判断。
type overlay struct {
	kind overlayKind
	appr *approvalPopup // kind == overlayApproval 时非 nil
	sel  *selectPopup   // kind == overlaySelect 时非 nil
	ask  *askPopup      // kind == overlayAsk 时非 nil
}

// openOverlay 打开新的覆盖层；已有覆盖层未决时拒绝（返回 false，不覆盖）——
// 堵住"审批挂起时队列命令叠开第二层弹窗"的通道（Bug10）。toast 提示被挡。
func (m Model) openOverlay(o *overlay) (Model, bool) {
	if m.ovl != nil {
		m.toast = "Blocked by pending " + overlayName(m.ovl.kind)
		m.refresh(false)
		return m, false
	}
	m.ovl = o
	return m, true
}

// closeOverlay 关闭当前覆盖层并恢复焦点；若有待决请求（并发审批/ask 排队，
// 缺陷 03）自动弹出下一个并保持失焦（Blur，等待用户输入）。全部弹窗关闭点
// 共用，保证排队请求不被静默丢弃（审批不丢 = 安全机制不失效）。
func (m Model) closeOverlay() (Model, tea.Cmd) {
	m.ovl = nil
	m.restoreFocus()
	if len(m.pending) > 0 {
		m.ovl = m.pending[0]
		m.pending = m.pending[1:]
		m.input.Blur()
	}
	m.refresh(false)
	return m, nil
}

func overlayName(k overlayKind) string {
	switch k {
	case overlayApproval:
		return "approval"
	case overlaySelect:
		return "selector"
	case overlayAsk:
		return "ask"
	default:
		return "help"
	}
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
	ovl          *overlay
	pending      []*overlay // 待决请求（FIFO）：当前弹窗关闭后自动弹出下一个（ADR-036 修订，缺陷 03）
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

	// 时长展示（ADR-043 用户追加需求，纯 UI 侧计时）
	turnStarted   *time.Time    // 回合开始（EventTurnStart 打点；footer 实时耗时）
	lastTurn      time.Duration // 上一回合总耗时（footer READY · last Ns）
	thinkingSince *time.Time    // thinking 首个增量打点（折叠行耗时，流式中实时）

	// 滚动条与文本选择（ADR-043 §6.2.1 / §6.7）
	scrollbarDrag bool
	selecting     bool
	selAnchor     selPoint
	selEnd        selPoint
	content       string // 最近一次时间线内容（含选区样式；复制选区取正文）
}

func New(c *Controller) Model {
	ta := textarea.New()
	ta.Placeholder = "Ask anything or type / for commands"
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 5
	ta.SetWidth(76)
	ta.SetHeight(1) // 默认一行，随内容行数增长（updateComposerHeight，至多 5 行）
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
		ovl := &overlay{kind: overlayApproval, appr: &approvalPopup{req: msg.req, respCh: msg.respCh}}
		var ok bool
		m, ok = m.openOverlay(ovl)
		if !ok {
			// 已有覆盖层未决（并发审批/ask 到达，缺陷 03）：入队等待，不静默丢弃。
			// openOverlay 守卫拒绝了叠开，但审批请求不能丢——排队等当前弹窗关闭。
			m.pending = append(m.pending, ovl)
			m.toast = "审批请求已排队（等待当前弹窗关闭）"
			m.refresh(false)
			return m, nil
		}
		m.input.Blur()
		m.refresh(false)
		return m, nil
	case askRequestMsg:
		ovl := &overlay{kind: overlayAsk, ask: &askPopup{
			req:      msg.req,
			respCh:   msg.respCh,
			selected: make([]bool, len(msg.req.Options)),
		}}
		var ok bool
		m, ok = m.openOverlay(ovl)
		if !ok {
			m.pending = append(m.pending, ovl)
			m.toast = "提问已排队（等待当前弹窗关闭）"
			m.refresh(false)
			return m, nil
		}
		m.input.Blur()
		m.refresh(false)
		return m, nil
	case runDoneMsg:
		return m.handleRunDone(msg)
	case completionWakeMsg:
		// 后台任务完成唤起信号（2026-08-13）：m.running 是第二道同步闸
		// （第一道 = MaybeWake 同步抢占 cancel）——兜底 handleRunDone 补唤醒
		// 与 wake 消息间隙的竞态，保证绝不并发启动两个 run。
		if m.running {
			return m, nil
		}
		return m.maybeStartWake()
	case compactDoneMsg:
		return m.handleCompactDone(msg)
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

// handleKey 按弹窗/焦点上下文查键位表分发（ADR-043：keyBindings 为单一事实
// 来源，行为与重构前逐字节等价）。无弹窗时先试全局键，未命中降级到焦点上下文。
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.ovl != nil {
		nm, cmd, _ := dispatchKey(overlayContext(m.ovl.kind), &m, msg)
		return nm, cmd
	}
	if nm, cmd, handled := dispatchKey(ctxGlobal, &m, msg); handled {
		return nm, cmd
	}
	ctx := ctxComposer
	if m.focus == focusTimeline {
		ctx = ctxTimeline
	}
	nm, cmd, _ := dispatchKey(ctx, &m, msg)
	return nm, cmd
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

// handleMouse 鼠标三态状态机（ADR-043 §6.2.1/§6.7）：
//   - 滚轮：滚动（行为不变，保留选区）；
//   - 时间线左键 press→release 无位移 = 点击（工具块/thinking 折叠切换，旧语义）；
//   - press→拖拽 = 文本选择（选区渲染 + Ctrl+C 复制）；
//   - 滚动条列 press/拖拽 = 跳转/连续滚动。
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	ev := tea.MouseEvent(msg)
	if ev.Action == tea.MouseActionMotion {
		return m.handleMouseMotion(ev)
	}
	if ev.Action == tea.MouseActionRelease {
		return m.handleMouseRelease(ev)
	}
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
	return m.handleMousePress(ev)
}

// handleMousePress 左键按下：滚动条 → 跳转并进入拖拽；时间线 → 选区锚点；
// composer → 聚焦并清除选区；其它区域 → 清除选区。
func (m Model) handleMousePress(ev tea.MouseEvent) (tea.Model, tea.Cmd) {
	if m.scrollbarAt(ev) {
		m.scrollbarDrag = true
		m.jumpScrollbar(ev.Y)
		m.refresh(false)
		return m, nil
	}
	if ev.Y >= m.composerTop {
		m.clearSelection()
		m.setFocus(focusComposer)
		m.refresh(false)
		return m, nil
	}
	if ev.Y >= m.mainTop && ev.Y < m.mainTop+m.viewport.Height && m.ovl == nil {
		m.clearSelection()
		m.setFocus(focusTimeline)
		m.selecting = true
		m.selAnchor = m.contentPoint(ev)
		m.selEnd = m.selAnchor
		m.refresh(false)
		return m, nil
	}
	m.clearSelection()
	m.refresh(false)
	return m, nil
}

// handleMouseMotion 拖拽中：滚动条拖拽 → 连续滚动；选区拖拽 → 扩展选区。
func (m Model) handleMouseMotion(ev tea.MouseEvent) (tea.Model, tea.Cmd) {
	if m.scrollbarDrag {
		m.jumpScrollbar(ev.Y)
		m.refresh(false)
		return m, nil
	}
	if m.selecting {
		m.selEnd = m.contentPoint(ev)
		m.refresh(false)
	}
	return m, nil
}

// handleMouseRelease 左键释放：滚动条拖拽结束；选区拖拽结束（零位移 = 点击，
// 保留旧折叠切换语义）。释放坐标直接结算 selEnd——部分终端不保证 Motion 事件，
// 拖拽不能只依赖 Motion 推进。
func (m Model) handleMouseRelease(ev tea.MouseEvent) (tea.Model, tea.Cmd) {
	if m.scrollbarDrag {
		m.scrollbarDrag = false
		return m, nil
	}
	if !m.selecting {
		return m, nil
	}
	m.selecting = false
	m.selEnd = m.contentPoint(ev)
	anchor := m.selAnchor
	if m.selAnchor == m.selEnd {
		// 点击（无位移）：工具块/thinking 折叠切换（旧语义，仅触发点从 press 移到 release）。
		m.clearSelection()
		m.toggleHitAt(anchor)
		m.refresh(false)
	}
	return m, nil
}

// contentPoint 把终端鼠标坐标换算为时间线内容坐标（行 = YOffset + 屏幕行）。
func (m *Model) contentPoint(ev tea.MouseEvent) selPoint {
	return selPoint{
		line: m.viewport.YOffset + ev.Y - m.mainTop,
		col:  ev.X,
	}
}

// toggleHitAt 点击内容行反查命中块并折叠切换（原 press 点击语义）。
func (m *Model) toggleHitAt(p selPoint) {
	for i, hit := range m.hits {
		if p.line >= hit.start && p.line <= hit.end {
			m.selectedHit = i
			m.toggleHit(hit)
			break
		}
	}
}

// hasSelection 报告是否存在有效选区。
func (m *Model) hasSelection() bool { return m.selAnchor != m.selEnd }

// clearSelection 清除选区与拖拽状态（Esc / 点击其它区域）。
func (m *Model) clearSelection() {
	m.selecting = false
	m.selAnchor = selPoint{}
	m.selEnd = selPoint{}
}

// finishAsk 提交 ask 回答并关闭弹窗：自定义文本非空 → Custom；否则单选提交
// 当前高亮 / 多选提交全部勾选项。
func (m Model) finishAsk(ask *askPopup) (tea.Model, tea.Cmd) {
	if custom := strings.TrimSpace(ask.custom); custom != "" && ask.req.AllowCustom {
		ask.respCh <- middleware.AskResult{Custom: custom}
		return m.closeOverlay()
	}
	var selection []string
	if ask.req.Multiple {
		for i, on := range ask.selected {
			if on {
				selection = append(selection, ask.req.Options[i].Label)
			}
		}
	} else if len(ask.req.Options) > 0 {
		selection = []string{ask.req.Options[ask.cursor].Label}
	}
	ask.respCh <- middleware.AskResult{Selection: selection}
	return m.closeOverlay()
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
	m.updateComposerHeight() // Reset 后高度回落一行
	if line == "/exit" && !m.running {
		if m.c != nil {
			m.c.AddCommand(line)
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
			m.c.AddCommand(line)
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

// maybeStartWake 评估唤醒决策（2026-08-13）：MaybeWake 非 nil → 系统行 +
// running 同步置位 + 启动唤醒 run。唤醒器只启动 run 不注入内容——通知全文
// 由 BackgroundCompletionMiddleware 在新 run 首采样前注入，Drain 清空后
// PendingCount()==0 天然防重。
func (m Model) maybeStartWake() (Model, tea.Cmd) {
	if m.c == nil {
		return m, nil
	}
	cmd := m.c.MaybeWake()
	if cmd == nil {
		return m, nil
	}
	m.appendSystem("后台进程完成，继续执行…", false)
	m.running = true
	m.turnDone = false
	m.eventError = false
	m.refresh(true)
	return m, cmd
}

func (m Model) handleAgentEvent(ev events.Event) (tea.Model, tea.Cmd) {
	switch ev.Type {
	case events.EventTurnStart:
		m.running = true
		m.turnDone = false
		if m.turnStarted == nil {
			now := time.Now()
			m.turnStarted = &now // footer 实时耗时打点（ADR-043）
		}
		m.ensureStream(ev.MsgID)
		return m, m.sp.Tick
	case events.EventThinkingDelta:
		m.ensureStream(ev.MsgID)
		if m.thinkingSince == nil {
			now := time.Now()
			m.thinkingSince = &now // thinking 实时耗时打点（ADR-043）
		}
		m.stream.Thinking += ev.Text
	case events.EventTextDelta:
		m.ensureStream(ev.MsgID)
		m.stream.Text += ev.Text
	case events.EventThinkingDone:
		m.ensureStream(ev.MsgID)
		if m.thinkingSince == nil {
			now := time.Now()
			m.thinkingSince = &now
		}
		m.stream.Thinking = ev.Text
	case events.EventTextDone:
		m.ensureStream(ev.MsgID)
		if ev.Text != "" {
			m.stream.Text = ev.Text
		}
		m.flushStream()
	case events.EventToolCall:
		m.flushStream()
		m.onToolCall(ev.ToolCall)
	case events.EventToolResult:
		m.onToolResult(ev)
	case events.EventTurnDone:
		m.flushStream()
		m.turnDone = true
	case events.EventUsage:
		// 用量已由 UsageMiddleware 写入 AgentState.LastContextTokens，尾部
		// refresh 读取并更新 footer（ctx 占用，ADR-037）；此处无需处理。
	case events.EventCompactStart:
		// 自动压缩开始（ADR-037 扩展）：Summarize 阻塞期间先给反馈，与完成行
		// 配对。经 rc.Emit 发出（中间件层无 agent 事件通道）。
		m.appendSystem("正在压缩上下文…", false)
	case events.EventNotice:
		// 后台完成等系统通知的 UI 可见性（2026-08-13）：注入中间件经 rc.Emit
		// 发出，渲染为系统行——内容同时已作为 user 消息进 conversation
		// （transcript 不落盘该类型，user 行由 AddUser 写入）。
		m.appendSystem(ev.Text, false)
	case events.EventCompacted:
		// 自动压缩成功（ADR-037）：conversation 已重写为摘要占位。回合进行中
		// 不能 reloadSession（会清空运行态/队列）；只打系统行，视图保持历史
		// 渲染。手动 /compact 走 compactDoneMsg（可安全 reloadSession）。
		m.appendSystem("上下文已压缩", false)
	case events.EventError:
		m.eventError = true
		if ev.Err != nil {
			m.appendSystem("Error: "+ev.Err.Error(), true)
		}
	}
	m.refresh(true)
	return m, nil
}

func (m Model) handleRunDone(msg runDoneMsg) (tea.Model, tea.Cmd) {
	m.running = false
	m.turnDone = false
	if m.turnStarted != nil {
		m.lastTurn = time.Since(*m.turnStarted)
		m.turnStarted = nil
	}
	if msg.wakeNotStarted {
		// 审查 05（2026-08-14）：唤醒 run 未真正启动（cancel 已抢占但 cmd
		// 未开跑）即被 Esc 打断——无事发生：不写"Turn interrupted"系统行、
		// 不写中断提示 user 消息（避免污染 conversation）；pending 保留待
		// 下一次完成信号/用户消息注入（不补唤醒，防热循环）。
		m.interrupted = false
		m.eventError = false
		m.refresh(true)
		return m, nil
	}
	err := msg.err
	if errors.Is(err, context.Canceled) {
		if !m.interrupted {
			m.appendSystem("Turn interrupted", false)
		}
		m.pending = nil // 兜底（requestInterrupt 已清；幂等），排队孤儿请求随 ctx 释放
		// 中断提示在 Run 返回后 AddUser（Bug10，2026-08-11）：此前在
		// requestInterrupt 立即 AddUser，与 Run goroutine 并发写 conversation，
		// 且在 runToolBatch 补全 tool_result 之前插入 user 消息——tool_use 与
		// tool_result 之间夹了 user，破坏 anthropic 邻接约束，下一轮采样 400。
		// 挪到这里：tool_use → tool_result（agent 补全）→ user(System) 顺序合法。
		if m.c != nil && m.c.active != nil {
			m.c.active.AddUser("(System: the previous agent turn was interrupted by the user. Continue unfinished work if needed; background processes may still be running.)")
		}
	} else if err != nil && !m.eventError {
		m.appendSystem("Error: "+err.Error(), true)
	}
	m.interrupted = false
	m.eventError = false
	m.refresh(true)
	if len(m.queue) == 0 {
		// 竞态窗口补唤醒（2026-08-13）：在途 run 最后一次采样已过后后台完成，
		// 唤醒信号被 isRunning 丢弃、pending 残留——err == nil 时重新评估补
		// 启动 run；err != nil 跳过：pending 未清时补唤醒会形成"唤醒失败 →
		// 再唤醒"热循环（成功 run 必跑过首采样、Drain 必已清空 pending，故
		// err == nil && pending > 0 恰好只对应此竞态窗口）。
		if err == nil {
			return m.maybeStartWake()
		}
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

// handleCompactDone 处理手动 /compact 完成（ADR-037）。空闲时执行（命令不在
// run 队列），可安全 reloadSession 显示压缩后的 transcript（摘要占位）。
func (m Model) handleCompactDone(msg compactDoneMsg) (tea.Model, tea.Cmd) {
	// 审查修复 01（2026-08-14）：复位 /compact 分派时同步置位的 running
	// （Esc 打断压缩也可能置 interrupted，一并复位——同 handleRunDone 语义）。
	m.running = false
	m.interrupted = false
	if msg.err != nil {
		return m.sysErr(msg.err), nil
	}
	if !msg.compacted {
		nm := m.sysOK("上下文未超阈值，无需压缩").(Model)
		// 补唤醒（架构整理另议项，2026-08-14）：compact 期间被 m.running 闸
		// 丢弃的完成事件 pending 在此立即补跑（对称 handleRunDone 的补唤醒），
		// 延迟不丢；无 pending 时 MaybeWake 自然 no-op。
		return nm.maybeStartWake()
	}
	// conversation 已重写为单一摘要占位 + transcript 已切新段 → 重载视图。
	m.reloadSession()
	nm := m.sysOK("上下文已压缩").(Model)
	return nm.maybeStartWake()
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
		thinkingDur := time.Duration(0)
		if m.thinkingSince != nil {
			thinkingDur = time.Since(*m.thinkingSince)
			m.thinkingSince = nil
		}
		m.appendMessage(&MessageItem{
			ID:               m.stream.MsgID,
			Role:             messages.RoleAssistant,
			Content:          m.stream.Text,
			Rendered:         renderMarkdown(m.stream.Text, m.contentWidth),
			Thinking:         m.stream.Thinking,
			ThinkingDuration: thinkingDur,
			Done:             true,
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

func (m *Model) onToolResult(ev events.Event) {
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
	content = m.applySelection(content)
	m.content = content
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
	if m.c == nil {
		return
	}
	// 懒加载：无 active（未创建）→ 清空状态字段，header 显示"新会话"占位。
	m.status.Model = m.c.ActiveModel()
	m.status.SessionName = m.c.Name()
	m.status.SessionID = m.c.ActiveID()
	m.status.ContextWindow = m.c.ActiveContextWindow()
	st := m.c.ActiveState()
	if st == nil {
		m.status.Permission = ""
		m.status.ThinkingEffort = ""
		m.status.PlanMode = false
		m.status.TodoCount = 0
		m.status.Todos = nil
		m.status.ContextTokens = 0
		return
	}
	m.status.Permission = st.PermissionMode()
	m.status.ThinkingEffort = st.ThinkingEffort // /effort 命令路径写，不与 run 并发
	m.status.PlanMode = st.IsPlanMode()
	m.status.TodoCount = st.TodoCount()
	m.status.Todos = sortTodos(st.TodoItems())
	// 当前上下文占用（UsageMiddleware 每轮更新 LastContextTokens；EventUsage
	// 事件触发刷新时已是最新值，ADR-037）。
	m.status.ContextTokens = st.CurrentContextTokens()
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
	// ctx cancel 后所有阻塞的 tuiApprover.Request/Ask goroutine 走 ctx.Done 分支
	// 释放；pending 里的请求成孤儿（respCh 无消费者），必须清空，否则 closeOverlay
	// 会弹出"无人等待"的弹窗（缺陷 03）。
	m.pending = nil
	m.c.cancelRun()
	m.appendSystem("Interrupt requested", false)
	m.toast = "Interrupting current turn"
	m.refresh(false)
}
