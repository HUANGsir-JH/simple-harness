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

// Controller 是 TUI 与 agent 运行时的桥（ADR-030 事件桥）。
// 持 agent + 项目桶 + 配置 + 会话注册表；回合启动/中断/事件转发/命令执行均经它。
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
	runs       sync.WaitGroup                   // 在途 run goroutine 计数（WaitRuns 用，Bug09）

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
		// 懒加载：首条用户消息触发会话创建（失败回 runDoneMsg 走 handleRunDone 报错）。
		if err := c.ensureActive(); err != nil {
			return runDoneMsg{err}
		}
		// 首消息自动命名（codex first_user_message 同款）：name 空时取首行预览，
		// /switch 一眼认出会话。命名落盘（agentstate）。
		if c.active.Name() == "" {
			if name := firstLinePreview(line); name != "" {
				if err := c.active.SetName(name); err != nil {
					return runDoneMsg{err}
				}
			}
		}
		rc := c.active.RuntimeContext()
		rc.Approver = c.approver() // W4 注入 TUIApprover；当前 nil = 自动拒绝
		rc.Emit = c.onEvent        // 压缩开始通知（ADR-037 扩展）：中间件经 rc.Emit 推送
		c.active.AddUser(line)
		runCtx, cancel := context.WithCancel(c.ctx)
		c.setCancel(cancel)
		defer c.clearCancel()
		err := c.a.Run(runCtx, rc, c.onEvent)
		return runDoneMsg{err}
	}
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
// compactor.Run(force=true)（含 LLM 摘要调用，避免阻塞 UI；toast"压缩中…"）。
// 返回 compactDoneMsg 标记完成。压缩成功重写 rc.Messages = [summary user]（指向
// 会话 conversation）+ rc.Segment 切新 transcript 段，TUI 收到后 reloadSession
// 显示摘要占位。摘要失败返回错误（不重写 conversation，历史保留）。
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
		rc := c.active.RuntimeContext()
		runCtx, cancel := context.WithCancel(c.ctx)
		c.setCancel(cancel)
		defer c.clearCancel()
		done, err := compactor.Run(runCtx, rc, true)
		if err != nil {
			return compactDoneMsg{err: err}
		}
		if done {
			// 压缩成功：AgentState 摘要/LastContextTokens 已改（内存）。手动路径
			// 不经 agent.Run → SessionMiddleware onAgent after 不会落盘，这里显式
			// 持久化（resume 恢复 Summary；LastContextTokens 清零防重入）。
			if err := agentstate.SaveFile(c.active.StatePath(), c.active.State()); err != nil {
				return compactDoneMsg{err: fmt.Errorf("压缩后落盘 AgentState: %w", err)}
			}
		}
		return compactDoneMsg{compacted: done}
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
