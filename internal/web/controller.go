package web

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/subagent"
)

// Controller 是 Web UI 模式运行时（对位 tui.Controller，去掉 bubbletea 依赖：
// 事件经 Hub 广播而非 tea.Msg，输入经 HTTP handler 而非键盘事件）。持 agent +
// 项目桶 + 配置 + 会话注册表；回合启动/中断/事件转发/命令执行均经它。
//
// ⚠ 并发契约（web 多 handler/goroutine，TUI 单线程主循环天然串行，此处必须
// 显式加锁）：以下字段的读写全部在 c.mu 内——running / queue / cancel /
// interrupted / approvals / asks / seq / active / open / viewSubID。
//   - 队列消费（防双 run）：run 收尾在**同一锁临界区**内完成 running=false →
//     取下一条 → running=true + setCancel，再启动 goroutine（对齐
//     MaybeWake 同步抢占警告：绝不在 cmd/goroutine 内才置位）
//   - switch/new 在 running 时拒绝（不切 active，防事件落盘错会话）
//   - onEvent 锁内取 active 快照再广播/落盘
type Controller struct {
	a          *agent.Agent
	proj       *session.Project
	cfg        config.Config
	subagents  *subagent.Manager // 子 agent 管理器（/subagents 查看）
	active     *session.Session
	open       map[string]*session.Session
	newSession func() (*session.Session, error) // 懒加载创建器
	ctx        context.Context                  // 顶层 ctx（回合从它派生；信号 cancel）
	hub        *Hub

	mu          sync.Mutex
	running     bool
	queue       []string
	cancel      context.CancelFunc // 当前回合 cancel（中断）
	interrupted bool               // 用户中断标志（Interrupt 置位，run 收尾消费复位）
	approvals   map[string]*pendingApproval
	asks        map[string]*pendingAsk
	seq         int                      // request_id 自增
	toolCalls   map[string]*toolCallInfo // call_id → tool 调用信息（tool_result 分派用）

	// 子 agent 只读查看（对位 tui.Controller）：active 切到子会话、输入禁用；
	// viewSubFn 是 Manager.Subscribe 订阅 → Hub 广播桥（带子 id，前端过滤）。
	parentSess *session.Session
	viewSubID  string
	viewSubFn  func(events.Event)
	viewOwned  bool // 子会话由 ResumeAt 自建（退出查看时 Close）

	runs sync.WaitGroup // 在途 run goroutine 计数（WaitRuns 用）
}

// NewController 构造桥。sess 为已加载会话（resume）或 nil（懒加载）；
// newSession 是懒加载创建器（sess nil 时首动作触发）。
func NewController(a *agent.Agent, proj *session.Project, cfg config.Config, sess *session.Session, newSession func() (*session.Session, error), ctx context.Context) *Controller {
	open := map[string]*session.Session{}
	if sess != nil {
		open[sess.ID] = sess
	}
	return &Controller{
		a:          a,
		proj:       proj,
		cfg:        cfg,
		active:     sess,
		open:       open,
		newSession: newSession,
		ctx:        ctx,
		approvals:  map[string]*pendingApproval{},
		asks:       map[string]*pendingAsk{},
		toolCalls:  map[string]*toolCallInfo{},
	}
}

// SetSubagents 注入子 agent 管理器（Assemble 装配；测试可省）。
func (c *Controller) SetSubagents(m *subagent.Manager) { c.subagents = m }

// SetHub 注入事件广播 Hub（Server 装配时调用；纯单元测试可省——无 Hub 时
// 广播 no-op，onEvent 仍落盘）。
func (c *Controller) SetHub(h *Hub) { c.hub = h }

// --- 会话/输入路由 ---------------------------------------------------------

// ensureActive 确保有 active 会话（懒加载：首动作才创建；resume 已预加载则
// no-op）。创建后登记 open 供 /switch 复用 + 挂完成事件唤起回调。
func (c *Controller) ensureActive() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return nil
	}
	if c.newSession == nil {
		return fmt.Errorf("无会话且未配置创建器")
	}
	s, err := c.newSession()
	if err != nil {
		return fmt.Errorf("创建会话: %w", err)
	}
	c.open[s.ID] = s
	c.active = s
	return nil
}

// HandleInput 处理用户输入行（POST /api/input）：running 时一切输入（含
// 命令）入队（对位 TUI submit()）；空闲时命令分发 / 消息启动回合。
func (c *Controller) HandleInput(line string) InputResult {
	line = strings.TrimSpace(line)
	if line == "" {
		return InputResult{OK: false, Error: "输入为空"}
	}
	c.mu.Lock()
	running := c.running
	if running {
		c.queue = append(c.queue, line)
		n := len(c.queue)
		c.mu.Unlock()
		return InputResult{OK: true, Kind: "queued", QueueLen: n}
	}
	c.mu.Unlock()
	return c.handleLine(line)
}

// handleLine 空闲路径：命令分发或启动回合。子 agent 查看中拒绝输入
// （/switch 除外——退出查看路径，对位 TUI）。
func (c *Controller) handleLine(line string) InputResult {
	if cmd, ok := parseCommandLine(line); ok {
		c.addCommand(line) // 命令落盘（transcript command 行）
		if c.IsViewingSubagent() && cmd.name != "switch" {
			return InputResult{OK: false, Error: "只读查看子 agent，输入已禁用"}
		}
		return c.runCommand(cmd)
	}
	if c.IsViewingSubagent() {
		return InputResult{OK: false, Error: "只读查看子 agent，输入已禁用"}
	}
	c.startRun(line)
	return InputResult{OK: true, Kind: "started"}
}

// addCommand 记录斜杠命令到当前会话（无会话忽略，对位 tui.Controller）。
func (c *Controller) addCommand(line string) {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active != nil {
		active.AddCommand(line)
	}
}

// --- 回合编排 ---------------------------------------------------------------

// startRun 空闲路径启动回合：锁内**同步**置 running + setCancel（H2 防双
// run——绝不在 goroutine 内才置位），再启动 goroutine。
func (c *Controller) startRun(line string) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(c.ctx)
	c.running = true
	c.cancel = cancel
	c.mu.Unlock()
	c.goRun(runCtx, line, false)
}

// maybeWake 是后台任务完成唤醒的决策入口（completion OnAppend 回调 +
// run 收尾补唤醒共用）：三分支丢弃（active nil / 在途 run / 无 pending），
// 否则同步抢占 cancel 后启动唤醒 run（RunWakeup 对位）。唤醒器只启动 run
// 不注入内容——通知全文由 BackgroundCompletionMiddleware 首采样前 Drain 注入。
func (c *Controller) maybeWake() {
	c.mu.Lock()
	if c.active == nil || c.running || c.active.Completions().PendingCount() == 0 {
		c.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(c.ctx)
	c.running = true
	c.cancel = cancel
	c.mu.Unlock()
	c.broadcastSystem("后台进程完成，继续执行…", false)
	c.goRun(runCtx, "", true)
}

// goRun 启动 run goroutine（running 已由调用方锁内置位）。wake = 唤醒 run
// （不 AddUser；line 空）。审查 05 对位：runCtx 已被 cancel（唤醒 run 未开跑
// 即被中断）→ wakeNotStarted，收尾不写伪中断提示。
func (c *Controller) goRun(runCtx context.Context, line string, wake bool) {
	c.runs.Add(1)
	go func() {
		defer c.runs.Done()
		if runCtx.Err() != nil {
			c.finishRun(runCtx, runCtx.Err(), true) // wakeNotStarted
			return
		}
		if err := c.ensureActive(); err != nil {
			c.finishRun(runCtx, err, false)
			return
		}
		// 首消息自动命名（codex first_user_message 同款）。
		if !wake && c.active.Name() == "" {
			if name := firstLinePreview(line); name != "" {
				if err := c.active.SetName(name); err != nil {
					c.finishRun(runCtx, err, false)
					return
				}
				c.broadcastStateChanged("rename")
			}
		}
		rc := c.newRunContext()
		if !wake {
			c.active.AddUser(line)
		}
		err := c.a.Run(runCtx, rc, c.onEvent)
		c.finishRun(runCtx, err, false)
	}()
}

// runCompact 手动压缩当前会话（/compact，对位 tui.Controller.RunCompact）。
// 调用方（命令分发）已同步置 running + setCancel。goroutine 内构造 rc 跑
// compactor.Run（含 LLM 摘要调用，不阻塞 HTTP handler）；完成广播
// state_changed{compact}（前端重拉显示摘要占位）。压缩成功重写 rc.Messages
// = [summary user] + rc.Segment 切新 transcript 段；手动路径不经
// SessionMiddleware，需显式落盘 AgentState。
func (c *Controller) runCompact() {
	c.runs.Add(1)
	go func() {
		defer c.runs.Done()
		err := func() error {
			if err := c.ensureActive(); err != nil {
				return err
			}
			compactor := c.a.Compactor()
			if compactor == nil {
				return fmt.Errorf("此装配不支持上下文压缩")
			}
			rc := c.newRunContext()
			runCtx, cancel := context.WithCancel(c.ctx)
			c.setCancel(cancel)
			defer c.clearCancel()
			if err := compactor.Run(runCtx, rc); err != nil {
				return err
			}
			if err := agentstate.SaveFile(c.active.StatePath(), c.active.State()); err != nil {
				return fmt.Errorf("压缩后落盘 AgentState: %w", err)
			}
			return nil
		}()
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		c.mu.Unlock()
		if err != nil {
			c.broadcastSystem("压缩失败: "+err.Error(), true)
		} else {
			c.broadcastSystem("上下文已压缩", false)
		}
		c.broadcastStateChanged("compact")
	}()
}

// finishRun 是 run goroutine 收尾（含队列消费，锁内串行）：
//  1. 中断提示落盘（Bug10 顺序：agent.Run 已返回、tool_result 已补全，
//     AddUser 此时插入合法；wakeNotStarted 不写伪中断）
//  2. 锁内 running=false → 取 queue 下一条 → running=true + setCancel
//     （同一临界区，防双 run）
//  3. 锁外广播 run_done（带 interrupted/queue_len）
//  4. 队列空且 err==nil → maybeWake 补唤醒（M9 竞态窗口；err!=nil 跳过防
//     热循环）
func (c *Controller) finishRun(runCtx context.Context, err error, wakeNotStarted bool) {
	c.mu.Lock()
	interrupted := c.interrupted
	c.interrupted = false
	c.mu.Unlock()

	if err != nil && errors.Is(err, context.Canceled) && !wakeNotStarted {
		// 中断提示在 Run 返回后 AddUser（Bug10）：tool_use → tool_result
		// （agent 补全）→ user(System) 顺序合法。wakeNotStarted 不写。
		c.mu.Lock()
		active := c.active
		c.mu.Unlock()
		if active != nil {
			active.AddUser("(System: the previous agent turn was interrupted by the user. Continue unfinished work if needed; background processes may still be running.)")
		}
	}

	c.mu.Lock()
	c.running = false
	c.cancel = nil
	var next string
	if len(c.queue) > 0 {
		next = c.queue[0]
		c.queue = c.queue[1:]
	}
	queueLen := len(c.queue)
	var nextCtx context.Context
	var nextCancel context.CancelFunc
	if next != "" {
		// 同一临界区内为下一条置位（防"双通过 isRunning 检查"竞态）。
		nextCtx, nextCancel = context.WithCancel(c.ctx)
		c.running = true
		c.cancel = nextCancel
	}
	c.mu.Unlock()

	errText := ""
	if err != nil && !errors.Is(err, context.Canceled) {
		errText = err.Error()
	}
	c.hubBroadcast("run_done", map[string]any{
		"session_id":  c.activeID(),
		"error":       errText,
		"interrupted": interrupted && errors.Is(err, context.Canceled),
		"queue_len":   queueLen,
	})

	if next != "" {
		c.goRun(nextCtx, next, false)
		return
	}
	// 队列空：补唤醒（err==nil 才补——成功 run 必跑过首采样、Drain 已清
	// pending；err!=nil 补唤醒会形成"唤醒失败 → 再唤醒"热循环）。
	if err == nil {
		c.maybeWake()
	}
}

// setCancel/clearCancel 记录当前回合 cancel（中断用；runCompact 复用）。
func (c *Controller) setCancel(f context.CancelFunc) {
	c.mu.Lock()
	c.cancel = f
	c.mu.Unlock()
}

func (c *Controller) clearCancel() {
	c.mu.Lock()
	c.cancel = nil
	c.mu.Unlock()
}

// isRunning 报告是否有在途 run/compact（锁内读 cancel）。
func (c *Controller) isRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancel != nil
}

// Interrupt 中断当前回合（POST /api/interrupt，对位 TUI Esc）：
// interrupted 置位 + cancelRun + 清空 approvals/asks pending 表（孤儿请求
// 防复活——其 goroutine 随 run ctx 释放，respCh 无消费者）。无回合 no-op。
func (c *Controller) Interrupt() {
	c.mu.Lock()
	hadRun := c.cancel != nil
	hadPending := len(c.approvals) > 0 || len(c.asks) > 0
	if hadRun {
		c.cancel()
		c.interrupted = true
	}
	// 清 pending 表（孤儿审批/提问；广播通知前端关闭弹窗）。
	for id := range c.approvals {
		delete(c.approvals, id)
	}
	for id := range c.asks {
		delete(c.asks, id)
	}
	c.mu.Unlock()
	if hadRun || hadPending {
		c.broadcastStateChanged("status")
	}
}

// newRunContext 是单一 UI 域注入点（对位 tui.Controller）：会话域默认
// （Session.RuntimeContext）+ UI 接缝覆写（Approver）。Emit 走 onEvent（Hub
// 广播 + 落盘）——压缩开始/系统通知事件对位 ADR-037。
func (c *Controller) newRunContext() *middleware.RuntimeContext {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil {
		return middleware.NewRuntimeContext()
	}
	rc := active.RuntimeContext()
	rc.Approver = c.webApprover() // 无 Hub 时 nil = 自动拒绝
	rc.Emit = c.onEvent
	return rc
}

// onEvent 双转发：Hub 广播（带 session_id + 附加渲染字段）+ transcript 落盘
// （块级，ADR-025）。锁内取 active 快照（run 期间 active 稳定——switch/new
// running 时拒绝、子查看命令入队）。
func (c *Controller) onEvent(ev events.Event) {
	toolView := c.toolEvent(ev)
	c.mu.Lock()
	active := c.active
	var sid string
	if active != nil {
		sid = active.ID
	}
	c.mu.Unlock()
	if active != nil {
		active.OnAgentEvent(ev) // 落盘（writer 自带锁）
	}
	if sid != "" {
		payload := c.agentEventPayload(sid, ev)
		if toolView != nil {
			payload["tool"] = toolView
		}
		c.hubBroadcast("agent", payload)
	}
}

// toolEvent 处理工具事件的调用信息缓存与分派：tool_call 保存 info（write_file
// 预读旧文件），tool_result 消费生成 ToolView（返回 nil = 无分派数据）。
// 主会话 onEvent 与子查看 viewSubFn 共用（call_id 模型侧全局唯一，共享 map
// 无冲突）。
func (c *Controller) toolEvent(ev events.Event) *ToolView {
	switch ev.Type {
	case events.EventToolCall:
		if ev.ToolCall != nil {
			info := prepareToolCall(ev.ToolCall.Name, ev.ToolCall.Args)
			c.mu.Lock()
			c.toolCalls[ev.ToolCall.ID] = info
			c.mu.Unlock()
		}
	case events.EventToolResult:
		if ev.ToolCall == nil || ev.ToolResult == nil {
			return nil
		}
		c.mu.Lock()
		info, ok := c.toolCalls[ev.ToolCall.ID]
		if ok {
			delete(c.toolCalls, ev.ToolCall.ID)
		}
		c.mu.Unlock()
		if ok {
			v := applyToolResult(info, ev.ToolResult)
			v.Name = info.name
			v.Summary = toolCallSummary(info.name, info.args)
			return &v
		}
	}
	return nil
}

// agentEventPayload 构造 SSE agent 事件载荷：events.Event 序列化 + 附加字段
// （html 仅 text_done/thinking_done；summary/tool 仅 tool_call/tool_result；
// err_text 仅 error——events.Event.Err 是 error 接口无法 JSON 序列化）。
func (c *Controller) agentEventPayload(sid string, ev events.Event) map[string]any {
	payload := map[string]any{
		"session_id": sid,
		"ev":         ev,
	}
	switch ev.Type {
	case events.EventTextDone:
		payload["html"] = renderHTML(ev.Text)
	case events.EventThinkingDone:
		payload["html"] = renderHTML(ev.Text)
	case events.EventToolCall:
		if ev.ToolCall != nil {
			payload["summary"] = toolCallSummary(ev.ToolCall.Name, ev.ToolCall.Args)
		}
	case events.EventError:
		if ev.Err != nil {
			payload["err_text"] = ev.Err.Error()
		}
	}
	return payload
}

// hubBroadcast 向 Hub 广播（Hub 未装配时 no-op——单元测试/纯逻辑场景）。
func (c *Controller) hubBroadcast(eventType string, payload any) {
	if c.hub != nil {
		c.hub.Broadcast(eventType, payload)
	}
}

// broadcastSystem 广播系统行（前端追加居中系统行）。
func (c *Controller) broadcastSystem(text string, isErr bool) {
	c.hubBroadcast("system", map[string]any{"text": text, "error": isErr})
}

// broadcastStateChanged 广播会话/状态变更（前端重拉 /api/state）。
func (c *Controller) broadcastStateChanged(reason string) {
	c.hubBroadcast("state_changed", map[string]any{"reason": reason})
}

// activeID 返回当前 active 会话 id（锁内读；空 = 未创建）。
func (c *Controller) activeID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return ""
	}
	return c.active.ID
}

// --- 子 agent 查看（对位 tui.Controller） -----------------------------------

// ListSubagents 列出当前会话的直属子（运行态 + 磁盘历史；/subagents 弹窗）。
func (c *Controller) ListSubagents() []subagent.EntryView {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if c.subagents == nil || active == nil {
		return nil
	}
	return c.subagents.List(active.RuntimeContext())
}

// ViewSubagent 进入只读查看模式（/subagents 选中）：active 切到子会话 +
// 订阅子事件流（Manager.Subscribe → Hub 广播桥，带子 id——前端按 active
// 过滤）。parentSess 记录查看前会话，退出时切回。会话来源：Manager 注册表
// 命中（运行中/刚收尾）→ 复用实例；磁盘历史 → ResumeAt（viewOwned，退出
// 时 Close，避免双 writer）。
func (c *Controller) ViewSubagent(id string) error {
	if c.subagents == nil {
		return fmt.Errorf("此装配不支持子 agent")
	}
	c.mu.Lock()
	parent := c.active
	c.mu.Unlock()
	if parent == nil {
		return fmt.Errorf("无会话")
	}
	var sub *session.Session
	var owned bool
	if s, ok := c.subagents.Session(id); ok && s != nil {
		sub = s
	} else {
		dir := filepath.Join(parent.Dir(), session.DirSubagents, id)
		s, err := session.ResumeAt(dir)
		if err != nil {
			return fmt.Errorf("加载子会话 %s: %w", id, err)
		}
		sub = s
		owned = true
	}
	c.ExitSubagentView() // 幂等退出旧查看（换看/重复进入）
	c.mu.Lock()
	c.parentSess = c.active
	c.viewOwned = owned
	c.viewSubID = id
	c.mu.Unlock()
	// 子事件流订阅：落盘由子 onEvent 固定做，订阅者只转发渲染（实时滚动）。
	c.viewSubFn = func(ev events.Event) {
		toolView := c.toolEvent(ev)
		c.mu.Lock()
		still := c.viewSubID == id
		c.mu.Unlock()
		if still {
			payload := c.agentEventPayload(id, ev)
			if toolView != nil {
				payload["tool"] = toolView
			}
			c.hubBroadcast("agent", payload)
		}
	}
	c.subagents.Subscribe(id, c.viewSubFn)
	c.mu.Lock()
	c.active = sub
	c.open[id] = sub
	c.mu.Unlock()
	c.broadcastStateChanged("subagent_view")
	return nil
}

// ExitSubagentView 退出只读查看（幂等）：退订子事件流 + active 切回父会话
// + Close 自建的磁盘会话。非查看模式 no-op。
func (c *Controller) ExitSubagentView() {
	c.mu.Lock()
	id := c.viewSubID
	fn := c.viewSubFn
	c.viewSubID = ""
	c.viewSubFn = nil
	c.mu.Unlock()
	if id == "" {
		return
	}
	if c.subagents != nil && fn != nil {
		c.subagents.Unsubscribe(id, fn)
	}
	c.mu.Lock()
	if c.viewOwned && c.active != nil {
		_ = c.active.Close()
	}
	c.viewOwned = false
	if c.parentSess != nil {
		c.active = c.parentSess
	}
	c.parentSess = nil
	c.mu.Unlock()
	c.broadcastStateChanged("subagent_exit")
}

// IsViewingSubagent 报告当前是否处于子 agent 只读查看模式（输入禁用）。
func (c *Controller) IsViewingSubagent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.viewSubID != ""
}

// --- 收尾三件套（对位 tui.Controller） --------------------------------------

// WaitRuns 等待所有在途 run goroutine 退出（Server 收尾在 CloseAll 前调用：
// run goroutine 可能仍在 emit，须等其退出再关 writer）。带超时降级。
func (c *Controller) WaitRuns() {
	done := make(chan struct{})
	go func() {
		c.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// SaveActiveState 兜底写回 active 会话的 AgentState（进程退出前调用，
// ADR-038 退出 pre-kill）。无 active no-op。
func (c *Controller) SaveActiveState() error {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil {
		return nil
	}
	return agentstate.SaveFile(active.StatePath(), active.State())
}

// CloseAll flush 所有打开会话 transcript。
func (c *Controller) CloseAll() {
	c.mu.Lock()
	open := make([]*session.Session, 0, len(c.open))
	for _, s := range c.open {
		open = append(open, s)
	}
	c.mu.Unlock()
	for _, s := range open {
		_ = s.Close()
	}
}

// firstLinePreview 取首条用户消息首行前 ~40 字符做会话默认名（对位 tui）。
func firstLinePreview(line string) string {
	line = strings.TrimSpace(strings.SplitN(line, "\n", 2)[0])
	if line == "" {
		return ""
	}
	runes := []rune(line)
	if len(runes) > 40 {
		return string(runes[:40]) + "..."
	}
	return line
}
