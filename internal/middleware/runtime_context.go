package middleware

import (
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
)

// RuntimeContext 是一次 call 的 per-call 上下文（参照 AgentScope 的 RuntimeContext）。
// 无状态 agent 架构（ADR-026）：agent 不持有任何会话状态，会话/模型/档位
// 全部经 rc 传入，每次 agent.Run 新建（切换会话 = 换 rc，并行 = 每 goroutine
// 一个 rc）。
//
// 单用户 CLI 无 UserID；会话标识 + 自由键值 attrs。各 middleware hook 与工具
// 共享同一实例，用于在调用内传递状态（不持久化）。
type RuntimeContext struct {
	// SessionID 标识当前会话（交互式/单轮复用）。
	SessionID string

	// Messages 是当前会话的消息序列（*messages.Thread，替代 Run 的 thread 参数）。
	// 命名对齐 provider.Request.Messages，避免与并发线程混淆。agent.Run 在此
	// 读写消息（追加 assistant / tool_result）。
	Messages *messages.Thread

	// State 是当前会话的 AgentState（注入机制 ADR-025）：
	// session.SessionMiddleware 挂 onAgent，before 加载、after 保存；
	// 工具经 Handle 的 rc 参数读写（如 todo 挂 rc.State.Todos）。
	State *agentstate.AgentState

	// StatePath 是 AgentState 落盘路径（SessionMiddleware 用；空 = 不落盘）。
	StatePath string

	// Model 是 per-call 模型覆盖（ADR-026；空 = agent/client 默认模型）。
	Model string

	// ThinkingEffort 是 per-call 推理档位覆盖（空 = client 默认档位）。
	ThinkingEffort string

	// ThinkingEnabled 是 per-call thinking 开关覆盖（nil = client 默认）。
	ThinkingEnabled *bool

	attrs map[string]any
}

// NewRuntimeContext 创建空上下文。
func NewRuntimeContext() *RuntimeContext {
	return &RuntimeContext{attrs: map[string]any{}}
}

// Get 读取自由属性。
func (rc *RuntimeContext) Get(key string) any {
	if rc == nil || rc.attrs == nil {
		return nil
	}
	return rc.attrs[key]
}

// Set 写入自由属性（并发安全由调用方保证；同一 call 内单线程执行）。
func (rc *RuntimeContext) Set(key string, value any) {
	if rc == nil {
		return
	}
	if rc.attrs == nil {
		rc.attrs = map[string]any{}
	}
	rc.attrs[key] = value
}
