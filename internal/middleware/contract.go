package middleware

import "context"

// 审批契约类型（阶段三权限，ADR-029）。
//
// 定义在 middleware 包而非中间件实现包（middleware/impl）：agent 调用层捕获
// DeniedError 回填模型，只认 middleware 包类型；若契约随 ApprovalMiddleware
// 放 impl，则 agent → impl → middleware 与 middleware → impl（签名用 rc）
// 会互引成环。审批策略实现（Policy/黑白名单）与 ApprovalMiddleware 在
// internal/middleware/impl 包；本文件只承载跨包共享的"契约"类型。

// Approver 是审批交互接口。CLI 注入实现（channel 协调 / 直接读行）；
// ApprovalMiddleware 在 onActing 询问时从 rc.Approver 读取。nil = 自动拒绝
// （非 TTY 场景）。
type Approver interface {
	Request(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ApprovalRequest 描述一次待审批的工具调用（展示给用户）。
type ApprovalRequest struct {
	// ToolName 是工具名（shell_command / write_file ...）。
	ToolName string
	// Summary 是人类可读摘要（shell 命令原文 / 文件路径 / 参数前 80 字符）。
	Summary string
	// Mode 是当前审批模式（readonly/acceptedit/bypass，展示用）。
	Mode string
}

// Decision 是审批决策。
type Decision int

const (
	// DecisionDeny 拒绝：作为失败结果回填模型（拒绝 ≠ Fatal，ADR-006）。
	DecisionDeny Decision = iota
	// DecisionAllow 允许本次。
	DecisionAllow
	// DecisionAllowSession 允许本次 + 本会话记住（AgentState.Permission.Approved）。
	DecisionAllowSession
)

// DeniedError 是审批拒绝错误。ApprovalMiddleware 在用户拒绝 / 无法询问时
// 返回它；agent.runToolBatch 捕获后作为失败结果回填（不取消整批），模型
// 看到拒绝可换思路重试。
type DeniedError struct {
	// Reason 是回填给模型的拒绝理由（tool_result 内容）。
	Reason string
}

func (e *DeniedError) Error() string { return e.Reason }
