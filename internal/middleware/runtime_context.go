package middleware

import (
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/events"
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

	// Messages 是当前会话的消息序列（*messages.Conversation，替代 Run 的消息
	// 序列参数）。命名对齐 provider.Request.Messages，避免与并发线程混淆。
	// agent.Run 在此读写消息（追加 assistant / tool_result）。
	Messages *messages.Conversation

	// State 是当前会话的 AgentState（注入机制 ADR-025）：
	// impl.SessionMiddleware 挂 onAgent，before 加载、after 保存；
	// 工具经 Handle 的 rc 参数读写（如 todo 挂 rc.State.Todos）。
	State *agentstate.AgentState

	// StatePath 是 AgentState 落盘路径（SessionMiddleware 用；空 = 不落盘）。
	StatePath string

	// Model 是 per-call 模型覆盖（ADR-026；空 = agent/client 默认模型）。
	Model string

	// ThinkingEffort 是 per-call 推理档位覆盖（空 = client 默认档位）。
	ThinkingEffort string

	// ThinkingEnabled 是 per-call thinking 开关覆盖（nil = 默认开启；client 恒
	// 默认开，2026-08-10 删配置 enabled）。
	ThinkingEnabled *bool

	// Approver 是审批交互器（阶段三权限，ADR-029）。CLI 注入实现
	// （channel 协调 / 直接读行）；nil = 自动拒绝（非 TTY 场景）。
	// ApprovalMiddleware 在 onActing 询问时从 rc 读取。
	Approver Approver

	// Segment 是压缩落盘钩子（ADR-037）：中间件重写 conversation 为纯占位后
	// 调用，切新 transcript 段（writer.NewSegment）并以 seed 消息（摘要 user
	// 行）开头，resume 从新段重建。session 注入闭包（与 rc.Approver 同模式：
	// session 知道中间件、反之不成立，防环）；nil = 不落盘（非会话场景）。

	// seed 是压缩后要落盘的消息序列（通常为 [summary user 消息]）。
	Segment func(seed []*messages.Message) error

	// Emit 是事件出口（ADR-037 扩展）：中间件在阻塞调用（如 Summarize）前向
	// UI 推送事件——agent.Run 的 emit 闭包够不到中间件内部，须 per-call 注入。
	// Controller.Run 注入 c.onEvent（与 rc.Approver 同模式）；nil = 不发出
	// （非交互场景；现有测试 rc 未注入，天然兼容）。
	Emit func(events.Event)

	attrs map[string]any
}

// CompactedKey 是 rc.attrs 的压缩完成标记键：compact.Run 成功后置 true，
// agent.Run 读取并发出 events.EventCompacted（TUI 系统行"上下文已压缩"）。
// 放 middleware 包（compact 与 agent 都已依赖它，避免新增依赖边）。
const CompactedKey = "compacted"

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
