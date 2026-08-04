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
	// Pre-scan for the global --json flag before subcommand dispatch so both
	// `harness --json run` and `harness run --json` work.
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

// --- config loading (temporary simplified version; full YAML in phase 4) ----

// loadConfig reads the provider configuration. It accepts an explicit path;
// otherwise it falls back to ~/.harness/config.yaml and finally to pure
// environment variables (HARNESS_PROVIDER / HARNESS_MODEL / HARNESS_BASE_URL).
func loadConfig(path string) (provider.Config, error) {
	cfg := provider.Config{}

	if path == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".harness", "config.yaml")
		}
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("config %s: %w", path, err)
		}
		return cfg, nil
	} else if !os.IsNotExist(err) {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	// Fallback: environment variables.
	cfg = provider.Config{
		Provider: os.Getenv("HARNESS_PROVIDER"),
		Model:    os.Getenv("HARNESS_MODEL"),
		BaseURL:  os.Getenv("HARNESS_BASE_URL"),
	}
	if cfg.Provider == "" || cfg.Model == "" {
		return cfg, fmt.Errorf("no config found: create %s (see `harness help`) or set HARNESS_PROVIDER/HARNESS_MODEL", path)
	}
	return cfg, nil
}
