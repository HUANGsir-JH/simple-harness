package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
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
		usage()
		return nil
	}
	switch rest[0] {
	case "version":
		fmt.Printf("harness %s\n", version)
		return nil
	case "run":
		return runCmd(rest[1:], jsonOut)
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
	fmt.Println("  harness run <prompt>         run a single turn with the configured model")
	fmt.Println("  harness version              print version")
	fmt.Println("  harness help                 show this help")
	fmt.Println()
	fmt.Println("flags:")
	fmt.Println("  --json                       emit machine-readable events as JSONL")
	fmt.Println()
	fmt.Println("config: ~/.harness/config.yaml (provider, model, base_url, env_key)")
}

func runCmd(args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var configPath string
	fs.StringVar(&configPath, "config", "", "path to config file (default ~/.harness/config.yaml)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		return fmt.Errorf("run: prompt is required (harness run \"your prompt\")")
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	client, err := provider.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("provider: %w\nhint: see config in ~/.harness/config.yaml and set the API key env var", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	thread := messages.NewThread()
	thread.Add(messages.NewUserMessage(prompt))

	a := agent.New(client, cfg.Model)

	var renderer output
	if jsonOut {
		renderer = jsonRenderer{}
	} else {
		renderer = textRenderer{}
	}
	renderer.start(thread)

	msg, err := a.RunOnce(ctx, thread, renderer.delta)
	if err != nil {
		return err
	}
	renderer.finish(msg)
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

// loadConfig 从第一个存在的配置文件中读取 provider 配置；
// 若都不存在则回退到环境变量（HARNESS_PROVIDER / HARNESS_MODEL
// / HARNESS_BASE_URL）。
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
		return cfg, nil
	}

	// 回退：环境变量。
	cfg := provider.Config{
		Provider: os.Getenv("HARNESS_PROVIDER"),
		Model:    os.Getenv("HARNESS_MODEL"),
		BaseURL:  os.Getenv("HARNESS_BASE_URL"),
	}
	if cfg.Provider == "" || cfg.Model == "" {
		return cfg, fmt.Errorf("no config found: create config.local.yaml in this project or ~/.harness/config.yaml (see `harness help`), or set HARNESS_PROVIDER/HARNESS_MODEL")
	}
	return cfg, nil
}
