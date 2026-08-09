package main

import (
	"sync"

	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
)

// App 是进程级共享装配：配置 + 默认模型解析结果 + agent 构建工厂。
// 程序启动时惰性初始化一次（defaultApp），所有命令复用——是后续多使用
// 全局变量的统一入口模式（ADR-026）。
//
// ⚠️ 与 middleware.RuntimeContext 区分：
//   - App：**进程级**，一次进程一份（配置/默认模型），仅 cmd 包使用
//   - RuntimeContext：**per-call**，每次 agent.Run 新建（会话/消息/状态），
//     贯穿 middleware 与工具
type App struct {
	// Config 是完整配置（多 provider）。
	Config provider.Config
	// Resolved 是默认 provider + 默认模型 的解析结果（default model）。
	Resolved *provider.Resolved
}

var (
	appOnce sync.Once
	appVal  *App
	appErr  error
)

// loadApp 从显式配置路径构建运行时（run --config 覆盖默认查找）。
func loadApp(path string) (*App, error) {
	cfg, err := provider.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	res, err := provider.Resolve(cfg, "")
	if err != nil {
		return nil, err
	}
	return &App{Config: cfg, Resolved: res}, nil
}

// defaultApp 惰性加载默认配置并解析默认模型（首次调用，之后进程级共享）。
// 惰性而非包 init()：version/help/sessions 不需要配置；配置缺失的错误须能被
// 命令捕获（init() 无法优雅处理）。对需要配置的命令而言即"启动时初始化一次"。
func defaultApp() (*App, error) {
	appOnce.Do(func() { appVal, appErr = loadApp("") })
	return appVal, appErr
}

// defaultApprovalMode 返回审批默认模式（config approval.mode；未配置回退
// acceptedit，ADR-029）。会话创建时播种到 AgentState.Permission.Mode
// （之后 /permission 切换改会话 state），审批 middleware 的 DefaultMode 同源。
func (app *App) defaultApprovalMode() string {
	if app.Config.Approval != nil && app.Config.Approval.Mode != "" {
		return app.Config.Approval.Mode
	}
	return impl.DefaultMode
}
