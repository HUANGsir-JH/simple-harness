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
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/tools"
	"gopkg.in/yaml.v3"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// 在子命令分发前预先扫描全局 --json 参数，使
	// `harness --json run` 与 `harness run --json` 都可用。
	jsonOut := false
	rest := args
	for i, a := range args {
		if a == "--json" {
			jsonOut = true
			rest = append(rest[:i], rest[i+1:]...)
			break
		}
	}

	if len(rest) == 0 {
		return repl(jsonOut) // 直接 harness（无子命令）→ 交互式
	}
	switch rest[0] {
	case "version":
		fmt.Printf("harness %s\n", version)
		return nil
	case "run":
		return runCmd(rest[1:], jsonOut)
	case "resume":
		return resumeCmd(rest[1:], jsonOut)
	case "sessions":
		return sessionsCmd(rest[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (try `harness help`)", rest[0])
	}
}

func usage() {
	fmt.Println("harness: minimal agent harness")
	fmt.Println("usage:")
	fmt.Println("  harness                       interactive mode (REPL, multi-turn)")
	fmt.Println("  harness run <prompt>          run a single turn with the configured model")
	fmt.Println("  harness resume <id>|--last    resume a session and continue in REPL")
	fmt.Println("  harness sessions              list sessions for this project")
	fmt.Println("  harness version               print version")
	fmt.Println("  harness help                  show this help")
	fmt.Println()
	fmt.Println("flags:")
	fmt.Println("  --json                       emit machine-readable events as JSONL")
	fmt.Println("  --model <name>               model to use (default: first model of default provider)")
	fmt.Println("  --effort <low|high|max>      reasoning effort override (must be in the model's thinking.efforts)")
	fmt.Println("  --thinking                   force enable thinking (default: model config)")
	fmt.Println("  --no-thinking                force disable thinking (default: model config)")
	fmt.Println("  --no-thinking-display        do not show thinking text (only affects text renderer)")
	fmt.Println()
	fmt.Println("config: project config.local.yaml or ~/.harness/config.yaml")
	fmt.Println("  default_provider + providers.<name>.{base_url, api_key, models} (anthropic wire)")
}

// buildAgent 从配置装配完整 agent：工具注册表 + middleware 链（工具说明注入）。
// 返回 agent、chain（调用方可追加 StateMiddleware 等会话级中间件）与解析出的模型名。
func buildAgent(cfg provider.Config, modelFlag, effortFlag string, thinking, noThinking bool) (*agent.Agent, *middleware.Chain, string, error) {
	res, err := provider.Resolve(cfg, modelFlag)
	if err != nil {
		return nil, nil, "", fmt.Errorf("resolve: %w", err)
	}
	if thinking && noThinking {
		return nil, nil, "", fmt.Errorf("--thinking and --no-thinking are mutually exclusive")
	}
	if thinking {
		res.ThinkingEnabled = true
	}
	if noThinking {
		res.ThinkingEnabled = false
	}
	if effortFlag != "" {
		if !slices.Contains(res.ThinkingEfforts, effortFlag) {
			return nil, nil, "", fmt.Errorf("--effort %q not supported by model %q (supported: %v)", effortFlag, res.Model, res.ThinkingEfforts)
		}
		res.ThinkingEffort = effortFlag
	}

	client, err := provider.NewClient(res)
	if err != nil {
		return nil, nil, "", fmt.Errorf("provider: %w", err)
	}

	reg := tools.NewRegistry()
	for _, t := range tools.Builtins() {
		if err := reg.Register(t); err != nil {
			return nil, nil, "", err
		}
	}
	// 工具说明注入系统提示（onSystemPrompt middleware；阶段四 AGENTS.md 等在此追加）。
	mw := middleware.NewChain(middleware.ToolInstructionsMiddleware{Tools: reg.Specs()})

	a := agent.New(client, res.Model)
	a.SetTools(reg)
	a.SetMiddleware(mw)
	return a, mw, res.Model, nil
}

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

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	a, chain, model, err := buildAgent(cfg, modelFlag, effortFlag, thinkingFlag, noThinkingFlag)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 会话落盘：workspace 项目桶 + transcript + AgentState（ADR-025）。
	sess, err := newSession(model)
	if err != nil {
		return err
	}
	defer sess.Close()
	chain.Add(&session.StateMiddleware{Path: sess.StatePath()})
	a.SetMiddleware(chain)

	rc := middleware.NewRuntimeContext()
	rc.SessionID = sess.ID
	rc.State = sess.State()
	thread := sess.Thread()
	userMsg := messages.NewUserMessage(prompt)
	thread.Add(userMsg)
	sess.WriteUser(userMsg)

	var renderer output
	if jsonOut {
		renderer = jsonRenderer{}
	} else {
		renderer = newTextRenderer(!noThinkingDisplay)
	}
	renderer.start(thread)

	onEvent := func(ev agent.Event) {
		renderer.event(ev)
		sess.OnAgentEvent(ev) // 块级实时落盘
	}
	if err := a.Run(ctx, rc, thread, onEvent); err != nil {
		return err
	}
	return nil
}

// repl 是交互式模式（`harness` 无子命令）：新会话 + REPL 循环。
func repl(jsonOut bool) error {
	cfg, err := loadConfig("")
	if err != nil {
		return err
	}
	a, chain, model, err := buildAgent(cfg, "", "", false, false)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sess, err := newSession(model)
	if err != nil {
		return err
	}
	defer sess.Close()
	chain.Add(&session.StateMiddleware{Path: sess.StatePath()})
	a.SetMiddleware(chain)

	rc := middleware.NewRuntimeContext()
	rc.SessionID = sess.ID
	rc.State = sess.State()

	var renderer output
	if jsonOut {
		renderer = jsonRenderer{}
	} else {
		renderer = newTextRenderer(true)
	}
	return runREPL(ctx, a, sess, rc, renderer)
}

// runREPL 是共享的 REPL 循环（交互式 / resume 复用）：每轮 读输入 →
// 写 user 行 → agent.Run（渲染 + 落盘双转发）→ 继续。
func runREPL(ctx context.Context, a *agent.Agent, sess *session.Session, rc *middleware.RuntimeContext, renderer output) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("harness 交互式模式（输入 exit/quit 或 Ctrl+C 退出）")
	thread := sess.Thread()
	renderer.start(thread)
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
		userMsg := messages.NewUserMessage(line)
		thread.Add(userMsg)
		sess.WriteUser(userMsg)
		onEvent := func(ev agent.Event) {
			renderer.event(ev)
			sess.OnAgentEvent(ev)
		}
		if err := a.Run(ctx, rc, thread, onEvent); err != nil {
			fmt.Fprintf(os.Stderr, "harness: %v\n", err)
		}
	}
}

// newSession 创建 workspace 骨架 + 定位项目桶 + 新建会话。
func newSession(model string) (*session.Session, error) {
	store, err := session.New()
	if err != nil {
		return nil, err
	}
	if err := store.EnsureDirs(); err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	proj, err := store.FindProject(cwd)
	if err != nil {
		return nil, err
	}
	return proj.Create(model)
}

// resumeCmd 恢复会话（--last 或 <id>）并进入 REPL 继续。
func resumeCmd(args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	last := fs.Bool("last", false, "resume the most recent session for this project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := strings.Join(fs.Args(), " ")

	store, err := session.New()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	proj, err := store.FindProject(cwd)
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
	defer sess.Close()

	cfg, err := loadConfig("")
	if err != nil {
		return err
	}
	a, chain, _, err := buildAgent(cfg, "", "", false, false)
	if err != nil {
		return err
	}
	chain.Add(&session.StateMiddleware{Path: sess.StatePath()})
	a.SetMiddleware(chain)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rc := middleware.NewRuntimeContext()
	rc.SessionID = sess.ID
	rc.State = sess.State()

	var renderer output
	if jsonOut {
		renderer = jsonRenderer{}
	} else {
		renderer = newTextRenderer(true)
	}
	return runREPL(ctx, a, sess, rc, renderer)
}

// sessionsCmd 列出当前项目的会话。
func sessionsCmd(args []string) error {
	store, err := session.New()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	proj, err := store.FindProject(cwd)
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

// --- 配置加载（临时简化版；阶段四做完整 YAML）-------------------------------

// configCandidates 返回配置文件查找顺序：显式路径（若指定）→
// 项目级 config.local.yaml → ~/.harness/config.yaml。
// API key 可放在配置文件（api_key）或环境变量中。
func configCandidates(path string) []string {
	if path != "" {
		return []string{path}
	}
	var out []string
	if cwd, err := os.Getwd(); err == nil {
		local := filepath.Join(cwd, "config.local.yaml")
		if _, err := os.Stat(local); err == nil {
			out = append(out, local)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".harness", "config.yaml"))
	}
	return out
}

// loadConfig 从第一个存在的配置文件中读取配置；
// 都不存在时报错（多 provider 结构必须由配置文件提供）。
func loadConfig(path string) (provider.Config, error) {
	for _, p := range configCandidates(path) {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return provider.Config{}, fmt.Errorf("read config %s: %w", p, err)
		}
		var cfg provider.Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return provider.Config{}, fmt.Errorf("config %s: %w", p, err)
		}
		if err := cfg.Validate(); err != nil {
			return provider.Config{}, fmt.Errorf("config %s: %w", p, err)
		}
		return cfg, nil
	}
	return provider.Config{}, fmt.Errorf("no config found: create config.local.yaml in this project or ~/.harness/config.yaml (see `harness help`)")
}
