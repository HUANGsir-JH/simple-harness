package tui

import (
	"context"
	"sync"
	"time"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// Controller 是 TUI 与 agent 运行时的桥（ADR-030 事件桥）。
// 持 agent + 项目桶 + 配置 + 会话注册表；回合启动/中断/事件转发/命令执行均经它。
// agent 完全无状态（ADR-026）：会话状态经 rc 传入，切换会话 = 换 active。
type Controller struct {
	a      *agent.Agent
	proj   *session.Project
	cfg    config.Config
	active *session.Session
	open   map[string]*session.Session
	ctx    context.Context // 顶层 ctx（回合从它派生；SIGTERM cancel）
	send   func(tea.Msg)   // program.Send（RunTUI 注入；并发安全）
	runs   sync.WaitGroup  // 在途 run goroutine 计数（WaitRuns 用，Bug09）

	mu     sync.Mutex
	cancel context.CancelFunc // 当前回合 cancel（Esc 中断，跨 goroutine 保护）
}

// NewController 构造桥。
func NewController(a *agent.Agent, proj *session.Project, cfg config.Config, sess *session.Session, ctx context.Context) *Controller {
	return &Controller{
		a:      a,
		proj:   proj,
		cfg:    cfg,
		active: sess,
		open:   map[string]*session.Session{sess.ID: sess},
		ctx:    ctx,
	}
}

// setSend 注入 program.Send（bubbletea Program 创建后调用）。
func (c *Controller) setSend(send func(tea.Msg)) { c.send = send }

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

// Run 启动一个回合（tea.Cmd：bubbletea 执行 goroutine 里跑 agent，ADR-030
// 事件桥 onEvent → program.Send）。返回 runDoneMsg 标记回合结束。
// runs.Add 在创建 Cmd 时同步执行（program.Run 期间发生），Done 在 goroutine
// 结束时——避免 WaitGroup 的 Add/Wait 并发误用（Bug09）。
func (c *Controller) Run(line string) tea.Cmd {
	c.runs.Add(1)
	return func() tea.Msg {
		defer c.runs.Done()
		rc := c.active.RuntimeContext()
		rc.Approver = c.approver() // W4 注入 TUIApprover；当前 nil = 自动拒绝
		c.active.AddUser(line)
		runCtx, cancel := context.WithCancel(c.ctx)
		c.setCancel(cancel)
		defer c.clearCancel()
		err := c.a.Run(runCtx, rc, c.onEvent)
		return runDoneMsg{err}
	}
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

// onEvent 双转发：program.Send（UI 渲染）+ transcript 落盘（块级，ADR-025）。
func (c *Controller) onEvent(ev agent.Event) {
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
