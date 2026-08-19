// Package config 定义 LLM 配置域：用户配置结构（YAML）+ 加载 + 解析 + 校验。
// 这是进程最底层的配置层，只依赖 yaml 与标准库，不依赖任何 harness 内部包。
//
// 从 provider 拆出（2026-08-09）：provider 回归 ADR-022 的"单 anthropic wire"
// 定位，配置模型/加载/解析/校验集中于此。依赖方向（底层→上层）：
// config → provider（wire）→ tools/middleware → agent；app（进程级装配根）消费本包。
package config

// DefaultAPIKeyEnv 是 API key 的惯例环境变量名（未配置 env_key 时的回退）。
const DefaultAPIKeyEnv = "ANTHROPIC_API_KEY"

// Config 是面向用户的完整配置（YAML），支持多 provider（多 anthropic 兼容端点）。
// 结构：default_provider 指定默认供应商，providers 按名称分组定义。
type Config struct {
	// DefaultProvider 是默认使用的 provider 名；未指定时取 providers 中
	// 排序后的第一个。
	DefaultProvider string `yaml:"default_provider,omitempty"`
	// Providers 是全部自定义供应商，key 为供应商名。
	Providers map[string]ProviderSpec `yaml:"providers"`
	// Approval 是工具审批配置（阶段三权限，ADR-029）；nil = 默认模式。
	Approval *ApprovalConfig `yaml:"approval,omitempty"`
}

// ApprovalConfig 是工具审批配置。
type ApprovalConfig struct {
	// Mode 是默认审批模式：readonly | acceptedit | bypass（未配置回退
	// acceptedit）。会话级可经 AgentState.Permission.Mode 覆盖（resume 恢复）。
	Mode string `yaml:"mode,omitempty"`
}

// ProviderSpec 描述一个自定义供应商的 YAML 定义（连接 + 鉴权 + 其下模型列表）。
// Resolve 从 Config 选一个 spec，解析出生效的扁平结构 ProviderConfig。
type ProviderSpec struct {
	// BaseURL 覆盖 SDK 默认端点；为空时使用官方默认。
	BaseURL string `yaml:"base_url,omitempty"`
	// EnvKey 是存放 API key 的环境变量名；与 APIKey 二选一。
	EnvKey string `yaml:"env_key,omitempty"`
	// APIKey 是直接写在配置中的 API key（例如由 .env 转换而来）；
	// 优先级高于 EnvKey。为空时从 EnvKey 环境变量读取。
	APIKey string `yaml:"api_key,omitempty"`
	// Models 是该供应商下的模型定义，key 为模型 ID。
	Models map[string]Model `yaml:"models"`
}

// ProviderConfig 是解析后的生效 provider 配置：Resolve 选定 provider + model 后
// 把 ProviderSpec 与 Model 合并拍平的扁平结构。由 Resolve 产出，provider.NewClient
// / agent.Build 直接消费；App 持有一份作为进程级默认。
//
// thinking 默认开启，无 ThinkingEnabled 字段：enabled 是会话级偏好
// （AgentState.ThinkingEnabled，nil = 默认开启，/thinking 切换，ADR-034）。
type ProviderConfig struct {
	ProviderID      string
	BaseURL         string
	APIKey          string
	Model           string
	ContextWindow   int
	ThinkingEffort  string
	ThinkingEfforts []string
	// TopP / Temperature 是模型级采样参数（0 = 未配置，请求不携带）。
	// 官方评测协议对齐（top_p=0.95 / temperature=1.0，2026-08-19）。
	TopP        float64
	Temperature float64
}

// Model 是单个模型定义。
type Model struct {
	// ContextWindow 是该模型的上下文窗口（token 数）；
	// 0 表示使用 DefaultContextWindow。
	ContextWindow int `yaml:"context_window,omitempty"`
	// Thinking 是该模型的 thinking（推理模式）配置；thinking 默认开启，
	// 未配置时档位 high（见 DefaultThinkingEffort）。
	Thinking *Thinking `yaml:"thinking,omitempty"`
	// TopP / Temperature 是采样参数（0 = 未配置，请求不携带；注意 0 无法
	// 表达"显式传 0"，如需 temperature=0 请用极小值）。官方评测协议
	// top_p=0.95 / temperature=1.0（2026-08-19）。
	TopP        float64 `yaml:"top_p,omitempty"`
	Temperature float64 `yaml:"temperature,omitempty"`
}

// Thinking 是模型级 thinking（推理模式）配置。传递按 anthropic Messages
// SDK 标准参数（thinking + output_config.effort），不对具体后端特化。
//
// thinking 默认开启（2026-08-10 删 enabled 配置项）：开关是会话级偏好，
// 用户可在会话运行时用 /thinking 关闭（持久化 AgentState.ThinkingEnabled，
// nil = 默认开启）；模型配置只声明 Efforts（支持的档位集）。
type Thinking struct {
	// Efforts 是模型支持的推理档位集（EffortLow / EffortHigh / EffortMax），
	// 覆盖默认档位集 DefaultEfforts；未配置回退默认。
	// 运行时 --effort 只能在 Efforts 内选择。
	Efforts []string `yaml:"efforts,omitempty"`
}

// thinking 推理档位（通用语义，非某后端特化）。
const (
	EffortLow  = "low"
	EffortHigh = "high"
	EffortMax  = "max"
)

// DefaultThinkingEffort 是未配置 thinking.effort 时的默认档位。
const DefaultThinkingEffort = EffortHigh
