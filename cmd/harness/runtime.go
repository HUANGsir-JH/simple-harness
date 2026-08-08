package main

import (
	"sync"

	"github.com/agent-project/harness/internal/provider"
)

// Runtime 是进程级共享运行时：配置 + 默认模型解析结果。
// 程序启动时惰性初始化一次（defaultRuntime），所有命令复用——是后续多使用
// 全局变量的统一入口模式（ADR-026）。
type Runtime struct {
	// Config 是完整配置（多 provider）。
	Config provider.Config
	// Resolved 是默认 provider + 默认模型 的解析结果（default model）。
	Resolved *provider.Resolved
}

var (
	rtOnce sync.Once
	rtVal  *Runtime
	rtErr  error
)

// loadRuntime 从显式配置路径构建运行时（run --config 覆盖默认查找）。
func loadRuntime(path string) (*Runtime, error) {
	cfg, err := provider.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	res, err := provider.Resolve(cfg, "")
	if err != nil {
		return nil, err
	}
	return &Runtime{Config: cfg, Resolved: res}, nil
}

// defaultRuntime 惰性加载默认配置并解析默认模型（首次调用，之后进程级共享）。
// 惰性而非包 init()：version/help/sessions 不需要配置；配置缺失的错误须能被
// 命令捕获（init() 无法优雅处理）。对需要配置的命令而言即"启动时初始化一次"。
func defaultRuntime() (*Runtime, error) {
	rtOnce.Do(func() { rtVal, rtErr = loadRuntime("") })
	return rtVal, rtErr
}
