// Package app 是进程级装配根（Composition Root，架构整理 2026-08-14）：
//   - App：进程级共享配置装配（惰性单例，Config + 解析后生效的 Provider）；
//   - Build/Options/HarnessAgent：唯一装配入口——命令层只声明模式与参数，
//     全部接线（配置/agent/session/TUI）收敛在 Build，产物 HarnessAgent
//     提供对称的 Run/Teardown。
//
// ⚠️ 与 middleware.RuntimeContext 区分：
//   - App：**进程级**，一次进程一份（配置/默认模型）
//   - HarnessAgent：**一次装配**一份（App + ReAct agent + 会话/TUI 实例）
//   - RuntimeContext：**per-call**，每次 agent.Run 新建（会话/消息/状态），
//     贯穿 middleware 与工具
package app

import (
	"fmt"
	"slices"
	"sync"

	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/middleware/impl"
)

// App 是进程级共享装配：配置 + 解析后生效的 provider 配置。
// 程序启动时惰性初始化一次（Load），所有命令复用。
type App struct {
	// Config 是完整配置（多 provider 的 YAML 定义）。
	Config config.Config
	// Provider 是解析后生效的 provider 配置（选定 provider + 默认模型）。
	Provider *config.ProviderConfig
}

var (
	once sync.Once
	val  *App
	err  error
)

// LoadFrom 从显式配置路径构建运行时（run --config 覆盖默认查找）。
func LoadFrom(path string) (*App, error) {
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	res, err := config.Resolve(cfg, "")
	if err != nil {
		return nil, err
	}
	return &App{Config: cfg, Provider: res}, nil
}

// Load 惰性加载默认配置并解析默认模型（首次调用，之后进程级共享）。
// 惰性而非包 init()：version/help/sessions 不需要配置；配置缺失的错误须能被
// 命令捕获（init() 无法优雅处理）。对需要配置的命令而言即"启动时初始化一次"。
func Load() (*App, error) {
	once.Do(func() { val, err = LoadFrom("") })
	return val, err
}

// DefaultApprovalMode 返回审批默认模式（config approval.mode；未配置回退
// impl.DefaultMode，ADR-029）。会话创建时播种到 AgentState.Permission.Mode
// （之后 /permission 切换改会话 state），审批 middleware 的 DefaultMode 同源。
func (a *App) DefaultApprovalMode() string {
	if a.Config.Approval != nil && a.Config.Approval.Mode != "" {
		return a.Config.Approval.Mode
	}
	return impl.DefaultMode
}

// ResolveFlags 校验 CLI 运行时覆盖（--model / --effort / --thinking / --no-thinking），
// 返回新会话的默认 ProviderConfig（模型 + 默认档位 + efforts）；无 flags 时返回默认。
func (a *App) ResolveFlags(modelFlag, effortFlag string, thinking, noThinking bool) (*config.ProviderConfig, error) {
	if thinking && noThinking {
		return nil, fmt.Errorf("--thinking and --no-thinking are mutually exclusive")
	}
	res := a.Provider
	if modelFlag != "" {
		var err error
		res, err = config.Resolve(a.Config, modelFlag)
		if err != nil {
			return nil, fmt.Errorf("resolve: %w", err)
		}
	}
	if effortFlag != "" {
		if !slices.Contains(res.ThinkingEfforts, effortFlag) {
			return nil, fmt.Errorf("--effort %q not supported by model %q (supported: %v)", effortFlag, res.Model, res.ThinkingEfforts)
		}
	}
	return res, nil
}
