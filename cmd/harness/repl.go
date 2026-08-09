package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	"golang.org/x/term"
)

// repl 是交互式模式（`harness` 无子命令）：新会话 + REPL 循环。
func repl(jsonOut bool) error {
	app, err := defaultApp()
	if err != nil {
		return err
	}
	a, err := app.buildAgent()
	if err != nil {
		return err
	}
	proj, err := findProject()
	if err != nil {
		return err
	}
	sess, err := session.CreateInCWD(app.Resolved.Model, app.defaultApprovalMode())
	if err != nil {
		return err
	}

	// SIGTERM 终止进程；SIGINT（Ctrl+C）作为字节由 raw mode 捕获 → 只中断当前
	// 回合（不终止 REPL）。顶层 ctx 不被 SIGINT cancel，下一轮 Run 不受影响。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	mgr := &SessionManager{
		app:    app,
		a:      a,
		proj:   proj,
		open:   map[string]*session.Session{sess.ID: sess},
		active: sess,
	}
	defer mgr.closeAll()

	var renderer output
	if jsonOut {
		renderer = jsonRenderer{}
	} else {
		renderer = newTextRenderer(true)
	}
	return runREPL(ctx, mgr, renderer)
}

// SessionManager 是 REPL 的会话管理器：开着的会话注册表 + 当前激活会话。
// 无状态 agent（ADR-026）：切换会话 = 换 active（下一轮取新 rc 自动生效）；
// 并行 agent = 每 goroutine 各取一个 rc（阶段五）。
type SessionManager struct {
	app    *App
	a      *agent.Agent
	proj   *session.Project
	open   map[string]*session.Session
	active *session.Session
}

// closeAll flush 所有开着的会话 transcript。
func (m *SessionManager) closeAll() {
	for _, s := range m.open {
		_ = s.Close()
	}
}

// switchTo 打开（若未开）并切换到指定会话 id（进程内 resume 切换）。
func (m *SessionManager) switchTo(id string) error {
	if s, ok := m.open[id]; ok {
		m.active = s
		return nil
	}
	list, err := m.proj.Sessions()
	if err != nil {
		return err
	}
	var info session.SessionInfo
	found := false
	for _, si := range list {
		if si.ID == id {
			info = si
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("会话 %q 不存在（`harness sessions` 查看）", id)
	}
	s, err := m.proj.Resume(info)
	if err != nil {
		return err
	}
	m.open[id] = s
	m.active = s
	return nil
}

// switchLast 打开最新会话并切换。
func (m *SessionManager) switchLast() error {
	info, ok := m.proj.Last()
	if !ok {
		return fmt.Errorf("本项目暂无会话（先 `harness run`）")
	}
	return m.switchTo(info.ID)
}

// replCommand 是一条 REPL 命令（/switch /model /effort）。
type replCommand struct {
	name string
	arg  string
}

// parseCommand 解析 REPL 行；非命令（不以 / 开头）返回 ok=false。
func parseCommand(line string) (replCommand, bool) {
	if !strings.HasPrefix(line, "/") {
		return replCommand{}, false
	}
	fields := strings.Fields(line)
	return replCommand{name: strings.TrimPrefix(fields[0], "/"), arg: strings.Join(fields[1:], " ")}, true
}

// handleCommand 执行一条 REPL 命令。
func (m *SessionManager) handleCommand(cmd replCommand) error {
	switch cmd.name {
	case "switch":
		if cmd.arg == "--last" {
			return m.switchLast()
		}
		if cmd.arg == "" {
			return fmt.Errorf("usage: /switch <id> 或 /switch --last（`harness sessions` 查看 id）")
		}
		return m.switchTo(cmd.arg)
	case "model":
		if cmd.arg == "" {
			return fmt.Errorf("usage: /model <name>")
		}
		res, err := provider.Resolve(m.app.Config, cmd.arg)
		if err != nil {
			return err
		}
		if err := m.active.SetModel(res.Model); err != nil {
			return err
		}
		// 新模型重置档位为模型默认（保证 effort 在新模型 efforts 内）。
		if err := m.active.SetThinkingEffort(res.ThinkingEffort); err != nil {
			return err
		}
		fmt.Printf("已切换模型 %s（effort %s）\n", res.Model, res.ThinkingEffort)
		return nil
	case "effort":
		if cmd.arg == "" {
			return fmt.Errorf("usage: /effort <low|high|max>")
		}
		cur, err := provider.Resolve(m.app.Config, m.active.Model())
		if err != nil {
			return err
		}
		if !slices.Contains(cur.ThinkingEfforts, cmd.arg) {
			return fmt.Errorf("模型 %q 不支持 effort %q（支持: %v）", cur.Model, cmd.arg, cur.ThinkingEfforts)
		}
		if err := m.active.SetThinkingEffort(cmd.arg); err != nil {
			return err
		}
		fmt.Printf("已切换 effort %s\n", cmd.arg)
		return nil
	case "permission":
		if cmd.arg == "" {
			return fmt.Errorf("usage: /permission <readonly|acceptedit|bypass>")
		}
		if !slices.Contains(impl.Modes, cmd.arg) {
			return fmt.Errorf("未知模式 %q（支持: readonly / acceptedit / bypass）", cmd.arg)
		}
		if err := m.active.SetPermissionMode(cmd.arg); err != nil {
			return err
		}
		fmt.Printf("已切换审批模式 %s\n", cmd.arg)
		return nil
	default:
		return fmt.Errorf("未知命令 /%s（支持: /switch /model /effort /permission）", cmd.name)
	}
}

// runREPL 是交互式 REPL 循环（`harness` / resume 复用）。输入改事件循环：
// 单一读方 goroutine 逐字节读 stdin（raw mode 下 Esc/Ctrl+C 可实时捕获），
// 经 channel 分发；主循环 select 输入事件 / 回合完成。
//
// 中断语义（ADR-028）：Esc(0x1b)/Ctrl+C(0x03) → cancel 当前回合的 runCtx
// （只中断本轮，下一轮新建 ctx 不受影响）；中断后 AddUser 一条系统提示落盘，
// resume 后模型可见"上一轮被中断"。exit/quit 退出。
func runREPL(ctx context.Context, m *SessionManager, renderer output) error {
	fmt.Println("harness 交互式模式（exit/quit 退出；Esc/Ctrl+C 中断当前回合；/switch <id> /model <name> /effort <level> /permission <readonly|acceptedit|bypass>）")
	renderer.start(m.active.Conversation())

	// raw mode：让 Esc 作为字节可被实时读取（不依赖行缓冲回车）。非 TTY
	// （重定向/管道）或 MakeRaw 失败 → 降级普通读行（无 Esc 中断，行为同前）。
	fd := int(os.Stdin.Fd())
	var echo io.Writer = io.Discard
	var reqCh chan *approvalRequest // nil = 不启用审批交互（非 TTY → 自动拒绝）
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, old) }()
			echo = os.Stdout // raw mode 无回显，需自行回显用户输入
			reqCh = make(chan *approvalRequest, 8)
		}
	}
	approver := newChannelApprover(reqCh)
	inputCh := readStdinEvents(os.Stdin, echo)
	_, isJSON := renderer.(jsonRenderer)

	var runDone chan error
	var cancelRun context.CancelFunc
	running := false
	var pending *approvalRequest // 非 nil = 审批挂起，下一行输入路由为审批答复

	fmt.Print("> ")
	for {
		select {
		case ev, ok := <-inputCh:
			if !ok {
				return nil // stdin EOF（Ctrl+D）
			}
			// 审批挂起：输入路由为审批答复（y/s/n / Esc），不当作 REPL 命令。
			if pending != nil {
				if ev.esc {
					// Esc：拒绝当前审批 + 中断当前回合。
					pending.resp <- middleware.DecisionDeny
					pending = nil
					if running && cancelRun != nil {
						cancelRun()
					}
					continue
				}
				line := strings.TrimSpace(ev.line)
				if line == "" {
					printApprovalUI(pending.req) // 空行重提示
					continue
				}
				dec, ok := parseApprovalDecision(line)
				if !ok {
					fmt.Printf("  无效输入（y/s/n）> ")
					continue
				}
				pending.resp <- dec
				pending = nil
				fmt.Print("> ")
				continue
			}
			if ev.esc {
				if running && cancelRun != nil {
					cancelRun() // 中断当前回合；结果经 runDone 分支处理
				}
				continue
			}
			line := strings.TrimSpace(ev.line)
			if line == "" {
				continue
			}
			if line == "exit" || line == "quit" {
				return nil
			}
			if cmd, ok := parseCommand(line); ok {
				if err := m.handleCommand(cmd); err != nil {
					fmt.Fprintf(os.Stderr, "harness: %v\n", err)
				}
				fmt.Print("> ")
				continue
			}
			if running {
				fmt.Println("（上一回合仍在运行，按 Esc 中断）")
				fmt.Print("> ")
				continue
			}
			// 每轮新建 rc（无状态 agent：会话状态经 rc 传入；切换会话下一轮自动生效）。
			rc := m.active.RuntimeContext()
			rc.Approver = approver // 审批交互注入（reqCh nil 时 approver 为 nil → 自动拒绝）
			m.active.AddUser(line)
			runCtx, cancel := context.WithCancel(ctx)
			cancelRun = cancel
			running = true
			runDone = make(chan error, 1)
			onEvent := func(ev agent.Event) {
				renderer.event(ev)
				m.active.OnAgentEvent(ev)
			}
			go func() { runDone <- m.a.Run(runCtx, rc, onEvent) }()
		case req := <-reqCh:
			// 新审批请求（并行工具可同时多个，channel 缓冲排队逐个处理）。
			pending = req
			printApprovalUI(req.req)
			if isJSON {
				emitApprovalJSON(req.req)
			}
		case err := <-runDone:
			running = false
			cancelRun = nil
			runDone = nil
			switch {
			case errors.Is(err, context.Canceled):
				fmt.Println("\n（已中断本轮）")
				// 中断提示落盘（ADR-028）：resume 后模型可见，对齐 Claude Code。
				m.active.AddUser("（系统：上一轮 agent 运行被用户中断。如有未完成的工作，请继续；后台进程可能仍在运行。）")
			case err != nil:
				fmt.Fprintf(os.Stderr, "\nharness: %v\n", err)
			}
			fmt.Print("> ")
		}
	}
}
