package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// Controller 是 TUI 模式运行时（ADR-030 事件桥）。持 agent + 项目桶 + 配置
// + 会话注册表；回合启动/中断/事件转发/命令执行均经它。
//
// 与装配产物 HarnessAgent（internal/app）的分层（2026-08-14 复查）：两者表面
// 相似的字段（agent/proj/会话/newSession）是 HarnessAgent 移交的零件——tui
// 不能反向依赖 app（成环），故经 Assemble 逐个接收；本结构体只在 TUI 程序
// 运行期间存在（Assemble→Run→Close），持**注册表**而非单会话（/switch 多会话）。
// HarnessAgent = 零件生产者（进程级一次一份、模式无关）；Controller = 零件
// 消费者（TUI 模式运行时）。
//
// agent 完全无状态（ADR-026）：会话状态经 rc 传入，切换会话 = 换 active。
//
// 会话懒加载（2026-08-11）：新入口（repl）不预创建 session，active 起始为
// nil，首条用户消息或状态变更命令经 ensureActive 才创建——避免 /exit 或
// /switch 到旧会话时残留空 session。resume 传入已加载 sess（active 非 nil）。
type Controller struct {
	a          *agent.Agent
	proj       *session.Project
	cfg        config.Config
	active     *session.Session
	open       map[string]*session.Session
	newSession func() (*session.Session, error) // 懒加载创建器（repl 传；resume 不触发传 nil）
	ctx        context.Context                  // 顶层 ctx（回合从它派生；SIGTERM cancel）
	send       func(tea.Msg)                    // program.Send（RunTUI 注入；并发安全）
	// wakeSignal 是后台完成事件的 UI 唤起信号（setSend 后生成，2026-08-13）：
	// 会话完成队列的 OnAppend 指向它——事件到达 → program.Send(completionWakeMsg)
	// → Update → MaybeWake。非 active 会话的事件也发信号，MaybeWake 查 active
	// 的 pending → 空 → 忽略（不打扰当前会话）。
	wakeSignal func()
	runs       sync.WaitGroup // 在途 run goroutine 计数（WaitRuns 用，Bug09）

	mu     sync.Mutex
	cancel context.CancelFunc // 当前回合 cancel（Esc 中断，跨 goroutine 保护）
}

// NewController 构造桥。sess 为已加载会话（resume）或 nil（新入口懒加载）；
// newSession 是懒加载创建器（sess nil 时首动作触发；resume 传 nil 不触发）。
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
	}
}

// setSend 注入 program.Send（bubbletea Program 创建后调用），并生成完成事件
// 唤起信号 + 登记已打开会话的队列回调（2026-08-13）：resume 传入的初始 sess
// 在 NewController 已进 open、早于 wakeSignal 生成——故在此统一补登记；此后
// ensureActive/SwitchTo 打开新会话处各自 registerWake。
// wakeSignal 是命名方法值 c.wake（架构整理 2026-08-14：去匿名闭包，可 grep/
// 可跳转），字段本身保留作 registerWake 的"未生成"哨兵。
func (c *Controller) setSend(send func(tea.Msg)) {
	c.send = send
	c.wakeSignal = c.wake
	for _, s := range c.open {
		c.registerWake(s)
	}
}

// wake 是后台完成事件的 UI 唤起信号（setSend 后经 wakeSignal 方法值挂到
// 会话完成队列的 OnAppend）：事件到达 → program.Send(completionWakeMsg) →
// Update → MaybeWake。非 active 会话的事件也发信号，MaybeWake 查 active 的
// pending → 空 → 忽略（不打扰当前会话）。send 未注入（纯 UI 测试）时 no-op。
func (c *Controller) wake() {
	if c.send != nil {
		c.send(completionWakeMsg{})
	}
}

// registerWake 给会话完成队列挂唤起回调（wakeSignal 未生成时 no-op——
// setSend 会补登记）。重复调用幂等（SetOnAppend 覆盖）。
func (c *Controller) registerWake(s *session.Session) {
	if c.wakeSignal != nil && s != nil {
		s.Completions().SetOnAppend(c.wakeSignal)
	}
}

// setCancel 记录当前回合 cancel（回合启动时设置）。
func (c *Controller) setCancel(f context.CancelFunc) {
	c.mu.Lock()
	c.cancel = f
	c.mu.Unlock()
}

// clearCancel 回合结束时清除。
func (c *Controller) clearCancel() {
	c.mu.Lock()
	c.cancel = nil
	c.mu.Unlock()
}

// cancelRun 取消当前回合（Esc 中断；无回合 no-op）。
func (c *Controller) cancelRun() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

// newRunContext 是单一 UI 域注入点（架构整理 2026-08-14）：会话域默认
// （Session.RuntimeContext：Messages/State/Segment/Completions/AppendUser）+ UI
// 接缝覆写（Approver/Emit）。Run/RunWakeup/RunCompact 都经它——新增接缝只改
// 这里，不再每处登记。
func (c *Controller) newRunContext() *middleware.RuntimeContext {
	rc := c.active.RuntimeContext()
	rc.Approver = c.approver() // TUIApprover；send 未注入时 nil = 自动拒绝
	rc.Emit = c.onEvent        // 压缩开始/系统通知事件桥（ADR-037 扩展）
	return rc
}

// Run 启动一个回合（tea.Cmd：bubbletea 执行 goroutine 里跑 agent，ADR-030
// 事件桥 onEvent → program.Send）。返回 runDoneMsg 标记回合结束。
// runs.Add 在创建 Cmd 时同步执行（program.Run 期间发生），Done 在 goroutine
// 结束时——避免 WaitGroup 的 Add/Wait 并发误用（Bug09）。
func (c *Controller) Run(line string) tea.Cmd {
	c.runs.Add(1)
	return func() tea.Msg {
		defer c.runs.Done()
		// 懒加载：首条用户消息触发会话创建（失败回 runDoneMsg 走 handleRunDone 报错）。
		if err := c.ensureActive(); err != nil {
			return runDoneMsg{err: err}
		}
		// 首消息自动命名（codex first_user_message 同款）：name 空时取首行预览，
		// /switch 一眼认出会话。命名落盘（agentstate）。
		if c.active.Name() == "" {
			if name := firstLinePreview(line); name != "" {
				if err := c.active.SetName(name); err != nil {
					return runDoneMsg{err: err}
				}
			}
		}
		rc := c.newRunContext()
		c.active.AddUser(line)
		runCtx, cancel := context.WithCancel(c.ctx)
		c.setCancel(cancel)
		defer c.clearCancel()
		err := c.a.Run(runCtx, rc, c.onEvent)
		return runDoneMsg{err: err}
	}
}

// isRunning 报告是否有在途回合（锁内读 cancel，复用现有状态零新增字段）。
// 唤醒决策用（2026-08-13）：/compact 期间 cancel 也被占用 → 同样不唤醒。
func (c *Controller) isRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancel != nil
}

// RunWakeup 启动一个"唤醒"回合（后台进程完成自动继续，2026-08-13）：
// Run 去 AddUser 的变体——不往对话写任何东西；通知全文由
// BackgroundCompletionMiddleware 在新 run 首采样前 Drain 注入（唤醒器只
// 负责"启动一个 run"这一个动作）。ctx/cancel 由 MaybeWake 同步创建并抢占
// （防连续两条 wake 消息并发启动两个 run）；active 已由 MaybeWake 判定
// 非 nil，无需 ensureActive。
func (c *Controller) RunWakeup(runCtx context.Context, cancel context.CancelFunc) tea.Cmd {
	c.runs.Add(1)
	return func() tea.Msg {
		defer c.runs.Done()
		defer c.clearCancel()
		// 审查 05（2026-08-14）：MaybeWake 返回 cmd 前已同步抢占 cancel，Esc
		// 可能在本 cmd 开跑前就触发 cancelRun——run 未真正启动即被中断，标记
		// wakeNotStarted，handleRunDone 不写伪中断提示污染 conversation。
		if runCtx.Err() != nil {
			return runDoneMsg{err: runCtx.Err(), wakeNotStarted: true}
		}
		rc := c.newRunContext()
		err := c.a.Run(runCtx, rc, c.onEvent)
		return runDoneMsg{err: err}
	}
}

// MaybeWake 是后台任务完成唤醒的决策入口（2026-08-13）：决策逻辑收敛在
// Controller，Model 只薄转发（"何时启动 run"本就是编排层职责，唤醒只是
// 第二个触发源）。三分支丢弃：active nil（懒加载未触发）、在途 run
// （cancel 非 nil）、无 pending 完成事件（防空跑）；否则拉起唤醒 run。
//
// ⚠ 同步抢占：cancel 必须在返回 cmd **之前**设置——tea.Cmd 由 bubbletea 在
// Update 返回后异步执行，若在 cmd 内才 setCancel，连续两条 wake 消息会在
// 间隙双双通过 isRunning 检查 → 两个 run 并发跑同一 conversation（data race
// + 双倍采样）。Model 层 m.running 闸为第二道兜底。
func (c *Controller) MaybeWake() tea.Cmd {
	if c.active == nil || c.isRunning() || c.active.Completions().PendingCount() == 0 {
		return nil
	}
	runCtx, cancel := context.WithCancel(c.ctx)
	c.setCancel(cancel)
	return c.RunWakeup(runCtx, cancel)
}

// ensureActive 确保有 active 会话（懒加载：新入口首次动作才创建；resume 已
// 预加载则 no-op）。创建后登记 open 供 /switch 进程内复用。
func (c *Controller) ensureActive() error {
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
	c.registerWake(s) // 完成事件唤起回调（2026-08-13）
	return nil
}

// ActiveID 返回当前会话 id（懒加载未创建时空）。
func (c *Controller) ActiveID() string {
	if c.active == nil {
		return ""
	}
	return c.active.ID
}

// ActiveModel 返回当前会话模型（未创建时空）。
func (c *Controller) ActiveModel() string {
	if c.active == nil {
		return ""
	}
	return c.active.Model()
}

// ActiveState 返回当前会话 AgentState（未创建时 nil；弹窗 current 读取用）。
func (c *Controller) ActiveState() *agentstate.AgentState {
	if c.active == nil {
		return nil
	}
	return c.active.State()
}

// AddCommand 记录斜杠命令到当前会话。懒加载下无会话（active nil）则忽略——
// 无 transcript 可写，语义正确（命令不产生空会话）。
func (c *Controller) AddCommand(line string) {
	if c.active == nil {
		return
	}
	c.active.AddCommand(line)
}

// Name 返回当前会话名（未创建时空）。
func (c *Controller) Name() string {
	if c.active == nil {
		return ""
	}
	return c.active.Name()
}

// firstLinePreview 取首条用户消息首行前 ~40 字符做会话默认名（codex
// first_user_message 同款）。空行/空白返回空（不命名）。
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

// WaitRuns 等待所有在途 run goroutine 退出（RunTUI 在 CloseAll 前调用：
// SIGTERM 时 program.Run 已返回但 run goroutine 可能仍在 emit，须等其退出
// 再关 writer，Bug09 治因）。带超时降级：run goroutine 卡死不阻塞退出
// （writer closed 标志已兜底防 panic，Bug06(a) 治症）。
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
// ADR-038 退出 pre-kill：用户补充需求）。正常路径由 SessionMiddleware 每
// 回合进出保存，此处只兜底退出瞬间未落盘的增量（/model 等状态变更即时
// 落盘、回合内变更随回合结束落盘——退出时兜底是廉价保险）。无 active
// （懒加载未触发）no-op。
func (c *Controller) SaveActiveState() error {
	if c.active == nil {
		return nil
	}
	return agentstate.SaveFile(c.active.StatePath(), c.active.State())
}

// RunCompact 手动压缩当前会话（/compact，ADR-037）。tea.Cmd 内构造 rc 跑
// compactor.Run（无条件压缩，判定由调用方决定——手动路径不判定；含 LLM 摘要
// 调用，避免阻塞 UI；toast"压缩中…"）。返回 compactDoneMsg 标记完成。压缩
// 成功重写 rc.Messages = [summary user]（指向会话 conversation）+ rc.Segment
// 切新 transcript 段，TUI 收到后 reloadSession 显示摘要占位。摘要失败返回
// 错误（不重写 conversation，历史保留）。
func (c *Controller) RunCompact() tea.Cmd {
	c.runs.Add(1)
	return func() tea.Msg {
		defer c.runs.Done()
		if err := c.ensureActive(); err != nil {
			return compactDoneMsg{err: err}
		}
		compactor := c.a.Compactor()
		if compactor == nil {
			return compactDoneMsg{err: fmt.Errorf("此装配不支持上下文压缩")}
		}
		rc := c.newRunContext()
		runCtx, cancel := context.WithCancel(c.ctx)
		c.setCancel(cancel)
		defer c.clearCancel()
		if err := compactor.Run(runCtx, rc); err != nil {
			return compactDoneMsg{err: err}
		}
		// 压缩成功：AgentState LastContextTokens/Usage 已改（内存）。手动路径
		// 不经 agent.Run → SessionMiddleware onAgent after 不会落盘，这里显式
		// 持久化（resume 恢复；LastContextTokens 清零防重入）。
		if err := agentstate.SaveFile(c.active.StatePath(), c.active.State()); err != nil {
			return compactDoneMsg{err: fmt.Errorf("压缩后落盘 AgentState: %w", err)}
		}
		return compactDoneMsg{compacted: true}
	}
}

// onEvent 双转发：program.Send（UI 渲染）+ transcript 落盘（块级，ADR-025）。
func (c *Controller) onEvent(ev events.Event) {
	if c.send != nil {
		c.send(agentEventMsg{ev})
	}
	c.active.OnAgentEvent(ev)
}

// approver 返回当前审批交互器（W4 起：TUI 弹窗桥；send 未注入（纯 UI 测试）
// 时返回 nil → ApprovalMiddleware 自动拒绝）。
func (c *Controller) approver() middleware.Approver {
	if c.send == nil {
		return nil
	}
	return &tuiApprover{send: c.send}
}
