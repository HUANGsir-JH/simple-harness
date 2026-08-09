package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	"golang.org/x/term"
)

// boolPtr 构造 bool 指针（--thinking/--no-thinking 覆盖会话 state 用）。
func boolPtr(b bool) *bool { return &b }

// runCmd 执行 `harness run <prompt>`：解析 flags → 建会话 → 应用覆盖 → 单轮 Run。
func runCmd(args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var configPath, modelFlag, effortFlag string
	var thinkingFlag, noThinkingFlag, noThinkingDisplay bool
	fs.StringVar(&configPath, "config", "", "path to config file (default ~/.harness/config.yaml)")
	fs.StringVar(&modelFlag, "model", "", "model to use (must be defined in the selected provider; default: first model)")
	fs.StringVar(&effortFlag, "effort", "", "reasoning effort override (low|high|max; must be in the model's thinking.efforts)")
	fs.BoolVar(&thinkingFlag, "thinking", false, "force enable thinking (default: model config)")
	fs.BoolVar(&noThinkingFlag, "no-thinking", false, "force disable thinking (default: model config)")
	fs.BoolVar(&noThinkingDisplay, "no-thinking-display", false, "do not show thinking text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		return fmt.Errorf("run: prompt is required (harness run \"your prompt\"; 不带参数运行 `harness` 进入交互式)")
	}

	var app *App
	var err error
	if configPath != "" {
		app, err = loadApp(configPath)
	} else {
		app, err = defaultApp()
	}
	if err != nil {
		return err
	}
	res, err := app.resolveFlags(modelFlag, effortFlag, thinkingFlag, noThinkingFlag)
	if err != nil {
		return err
	}
	a, err := app.buildAgent()
	if err != nil {
		return err
	}

	sess, err := session.CreateInCWD(res.Model)
	if err != nil {
		return err
	}
	defer sess.Close()
	// flags → 会话 state（随 SessionMiddleware 落盘，resume 可恢复）。
	if thinkingFlag {
		if err := sess.SetThinkingEnabled(boolPtr(true)); err != nil {
			return err
		}
	}
	if noThinkingFlag {
		if err := sess.SetThinkingEnabled(boolPtr(false)); err != nil {
			return err
		}
	}
	if effortFlag != "" {
		if err := sess.SetThinkingEffort(effortFlag); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rc := sess.RuntimeContext()
	sess.AddUser(prompt)

	var renderer output
	if jsonOut {
		renderer = jsonRenderer{}
	} else {
		renderer = newTextRenderer(!noThinkingDisplay)
	}
	renderer.start(sess.Conversation())

	onEvent := func(ev agent.Event) {
		renderer.event(ev)
		sess.OnAgentEvent(ev) // 块级实时落盘
	}

	// 输入层：TTY 时 raw mode + 单一读方事件循环（Esc 中断 + 审批协调，
	// ADR-028/029）；非 TTY / MakeRaw 失败 → 不启用审批交互（自动拒绝）、
	// 无 Esc 中断（跑完即退）。
	fd := int(os.Stdin.Fd())
	var inputCh <-chan inputEvent
	var reqCh chan *approvalRequest
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, old) }()
			inputCh = readStdinEvents(os.Stdin, os.Stdout)
			reqCh = make(chan *approvalRequest, 8)
			rc.Approver = newChannelApprover(reqCh)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(runCtx, rc, onEvent) }()

	var pending *approvalRequest
	for {
		select {
		case ev, ok := <-inputCh:
			if !ok {
				// stdin EOF（Ctrl+D）：取消本轮，等 Run 退出。
				cancel()
				inputCh = nil
				continue
			}
			// 审批挂起：输入路由为审批答复（y/s/n / Esc）。
			if pending != nil {
				if ev.esc {
					pending.resp <- middleware.DecisionDeny
					pending = nil
					cancel()
					continue
				}
				line := strings.TrimSpace(ev.line)
				if line == "" {
					printApprovalUI(pending.req)
					continue
				}
				dec, ok := parseApprovalDecision(line)
				if !ok {
					fmt.Printf("  无效输入（y/s/n）> ")
					continue
				}
				pending.resp <- dec
				pending = nil
				continue
			}
			if ev.esc {
				cancel() // 单轮 Esc/Ctrl+C 中断
			}
			// 普通行忽略（runCmd 无 REPL 命令）。
		case req := <-reqCh:
			pending = req
			printApprovalUI(req.req)
			if jsonOut {
				emitApprovalJSON(req.req)
			}
		case err := <-runDone:
			if errors.Is(err, context.Canceled) {
				fmt.Println("\n（已中断）")
				return nil
			}
			return err
		}
	}
}

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
	sess, err := session.CreateInCWD(app.Resolved.Model)
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
	default:
		return fmt.Errorf("未知命令 /%s（支持: /switch /model /effort）", cmd.name)
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
	fmt.Println("harness 交互式模式（exit/quit 退出；Esc/Ctrl+C 中断当前回合；/switch <id> /model <name> /effort <level>）")
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

// inputEvent 是 REPL 的 stdin 输入事件。
type inputEvent struct {
	esc  bool   // Esc/Ctrl+C 按下 → 中断当前回合
	line string // 提交的一整行（回车触发）
}

// readStdinEvents 从 reader 逐 rune 读取，产出一致化输入事件（单一读方：REPL
// 主循环与中断监听共用此 channel，避免多个 goroutine 竞争 stdin）。raw mode
// 下终端不回显，经 echo 自行回显（普通字符、退格擦除）。规则：
//
//	Esc(0x1b) / Ctrl+C(0x03) → esc 事件
//	\r 或 \n → 行提交（空行忽略）；Ctrl+D(0x04) → 关闭 channel（EOF）
//	退格(0x7f/0x08) → 删除行尾 + 回显 "\b \b"
//	其它 → 追加当前行 + 回显
func readStdinEvents(reader io.Reader, echo io.Writer) <-chan inputEvent {
	ch := make(chan inputEvent)
	go func() {
		defer close(ch)
		br := bufio.NewReader(reader)
		var line []rune
		for {
			r, _, err := br.ReadRune()
			if err != nil {
				return
			}
			switch r {
			case 0x1b, 0x03: // Esc / Ctrl+C
				line = line[:0]
				ch <- inputEvent{esc: true}
			case '\r', '\n':
				if len(line) > 0 {
					ch <- inputEvent{line: string(line)}
					line = line[:0]
				}
			case 0x7f, 0x08: // 退格
				if len(line) > 0 {
					line = line[:len(line)-1]
					if echo != nil {
						io.WriteString(echo, "\b \b")
					}
				}
			case 0x04: // Ctrl+D：非空行先提交（flush），再 EOF（退出）
				if len(line) > 0 {
					ch <- inputEvent{line: string(line)}
					line = line[:0]
				}
				return
			default:
				line = append(line, r)
				if echo != nil {
					fmt.Fprint(echo, string(r))
				}
			}
		}
	}()
	return ch
}

// resumeCmd 恢复会话（--last 或 <id>）并进入 REPL 继续。
func resumeCmd(args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	last := fs.Bool("last", false, "resume the most recent session for this project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := strings.Join(fs.Args(), " ")

	proj, err := findProject()
	if err != nil {
		return err
	}

	var info session.SessionInfo
	if *last {
		var ok bool
		if info, ok = proj.Last(); !ok {
			return fmt.Errorf("resume: 本项目暂无会话（先 `harness run`）")
		}
	} else {
		if id == "" {
			return fmt.Errorf("resume: 需要会话 id 或 --last（`harness sessions` 查看）")
		}
		list, _ := proj.Sessions()
		found := false
		for _, s := range list {
			if s.ID == id {
				info = s
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("resume: 会话 %q 不存在（`harness sessions` 查看）", id)
		}
	}

	sess, err := proj.Resume(info)
	if err != nil {
		return err
	}

	app, err := defaultApp()
	if err != nil {
		return err
	}
	a, err := app.buildAgent()
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

// sessionsCmd 列出当前项目的会话。
func sessionsCmd(args []string) error {
	proj, err := findProject()
	if err != nil {
		return err
	}
	list, err := proj.Sessions()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("（本项目暂无会话，先 `harness run`）")
		return nil
	}
	for _, s := range list {
		var model, updated string
		if st, err := agentstate.LoadFile(filepath.Join(s.Path, session.FileAgentState)); err == nil {
			model, updated = st.Model, st.UpdatedAt
		}
		fmt.Printf("%s  model=%s  updated=%s\n", s.ID, model, updated)
	}
	return nil
}

// findProject 定位当前项目桶（New + Getwd + FindProject）。
func findProject() (*session.Project, error) {
	store, err := session.New()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return store.FindProject(cwd)
}
