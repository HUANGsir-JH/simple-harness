package middleware

// RuntimeContext 是一次 call 的 per-call 上下文（参照 AgentScope 的 RuntimeContext）。
// 单用户 CLI 无 UserID；阶段二最小实现：SessionID + 自由键值 attrs。
// 各 middleware hook 与工具共享同一实例，用于在调用内传递状态（不持久化）。
type RuntimeContext struct {
	// SessionID 标识当前会话（交互式/单轮复用）。
	SessionID string

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
