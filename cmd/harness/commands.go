package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
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

	var rt *Runtime
	var err error
	if configPath != "" {
		rt, err = loadRuntime(configPath)
	} else {
		rt, err = defaultRuntime()
	}
	if err != nil {
		return err
	}
	res, err := rt.resolveFlags(modelFlag, effortFlag, thinkingFlag, noThinkingFlag)
	if err != nil {
		return err
	}
	a, err := rt.buildAgent()
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
	renderer.start(sess.Thread())

	onEvent := func(ev agent.Event) {
		renderer.event(ev)
		sess.OnAgentEvent(ev) // 块级实时落盘
	}
	return a.Run(ctx, rc, onEvent)
}

// repl 是交互式模式（`harness` 无子命令）：新会话 + REPL 循环。
func repl(jsonOut bool) error {
	rt, err := defaultRuntime()
	if err != nil {
		return err
	}
	a, err := rt.buildAgent()
	if err != nil {
		return err
	}
	proj, err := findProject()
	if err != nil {
		return err
	}
	sess, err := session.CreateInCWD(rt.Resolved.Model)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rcx := &replCtx{
		rt:     rt,
		a:      a,
		proj:   proj,
		open:   map[string]*session.Session{sess.ID: sess},
		active: sess,
	}
	defer rcx.closeAll()

	var renderer output
	if jsonOut {
		renderer = jsonRenderer{}
	} else {
		renderer = newTextRenderer(true)
	}
	return runREPL(ctx, rcx, renderer)
}

// replCtx 是 REPL 的会话管理器：开着的会话注册表 + 当前激活会话。
// 无状态 agent（ADR-026）：切换会话 = 换 active（下一轮取新 rc 自动生效）；
// 并行 agent = 每 goroutine 各取一个 rc（阶段五）。
type replCtx struct {
	rt     *Runtime
	a      *agent.Agent
	proj   *session.Project
	open   map[string]*session.Session
	active *session.Session
}

// closeAll flush 所有开着的会话 transcript。
func (c *replCtx) closeAll() {
	for _, s := range c.open {
		_ = s.Close()
	}
}

// switchTo 打开（若未开）并切换到指定会话 id（进程内 resume 切换）。
func (c *replCtx) switchTo(id string) error {
	if s, ok := c.open[id]; ok {
		c.active = s
		return nil
	}
	list, err := c.proj.Sessions()
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
	s, err := c.proj.Resume(info)
	if err != nil {
		return err
	}
	c.open[id] = s
	c.active = s
	return nil
}

// switchLast 打开最新会话并切换。
func (c *replCtx) switchLast() error {
	info, ok := c.proj.Last()
	if !ok {
		return fmt.Errorf("本项目暂无会话（先 `harness run`）")
	}
	return c.switchTo(info.ID)
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
func (c *replCtx) handleCommand(cmd replCommand) error {
	switch cmd.name {
	case "switch":
		if cmd.arg == "--last" {
			return c.switchLast()
		}
		if cmd.arg == "" {
			return fmt.Errorf("usage: /switch <id> 或 /switch --last（`harness sessions` 查看 id）")
		}
		return c.switchTo(cmd.arg)
	case "model":
		if cmd.arg == "" {
			return fmt.Errorf("usage: /model <name>")
		}
		res, err := provider.Resolve(c.rt.Config, cmd.arg)
		if err != nil {
			return err
		}
		if err := c.active.SetModel(res.Model); err != nil {
			return err
		}
		// 新模型重置档位为模型默认（保证 effort 在新模型 efforts 内）。
		if err := c.active.SetThinkingEffort(res.ThinkingEffort); err != nil {
			return err
		}
		fmt.Printf("已切换模型 %s（effort %s）\n", res.Model, res.ThinkingEffort)
		return nil
	case "effort":
		if cmd.arg == "" {
			return fmt.Errorf("usage: /effort <low|high|max>")
		}
		cur, err := provider.Resolve(c.rt.Config, c.active.Model())
		if err != nil {
			return err
		}
		if !slices.Contains(cur.ThinkingEfforts, cmd.arg) {
			return fmt.Errorf("模型 %q 不支持 effort %q（支持: %v）", cur.Model, cmd.arg, cur.ThinkingEfforts)
		}
		if err := c.active.SetThinkingEffort(cmd.arg); err != nil {
			return err
		}
		fmt.Printf("已切换 effort %s\n", cmd.arg)
		return nil
	default:
		return fmt.Errorf("未知命令 /%s（支持: /switch /model /effort）", cmd.name)
	}
}

// runREPL 是交互式 REPL 循环（`harness` / resume 复用）：每轮读输入 →
// 命令处理或 AddUser + agent.Run（渲染 + 落盘双转发）→ 继续。
func runREPL(ctx context.Context, c *replCtx, renderer output) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("harness 交互式模式（exit/quit 退出；/switch <id> /model <name> /effort <level>）")
	renderer.start(c.active.Thread())
	for {
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println()
			return nil // EOF（Ctrl+D）
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return nil
		}
		if cmd, ok := parseCommand(line); ok {
			if err := c.handleCommand(cmd); err != nil {
				fmt.Fprintf(os.Stderr, "harness: %v\n", err)
			}
			continue
		}
		// 每轮新建 rc（无状态 agent：会话状态经 rc 传入；切换会话下一轮自动生效）。
		rc := c.active.RuntimeContext()
		c.active.AddUser(line)
		onEvent := func(ev agent.Event) {
			renderer.event(ev)
			c.active.OnAgentEvent(ev)
		}
		if err := c.a.Run(ctx, rc, onEvent); err != nil {
			fmt.Fprintf(os.Stderr, "harness: %v\n", err)
		}
	}
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

	rt, err := defaultRuntime()
	if err != nil {
		return err
	}
	a, err := rt.buildAgent()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rcx := &replCtx{
		rt:     rt,
		a:      a,
		proj:   proj,
		open:   map[string]*session.Session{sess.ID: sess},
		active: sess,
	}
	defer rcx.closeAll()

	var renderer output
	if jsonOut {
		renderer = jsonRenderer{}
	} else {
		renderer = newTextRenderer(true)
	}
	return runREPL(ctx, rcx, renderer)
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
