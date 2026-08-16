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
//
// ADR-036 起增 Ask 方法：Approver 同时承担"审批"（Request，y/s/n 枚举决策）与
// "提问"（Ask，选项 + Other 自由文本）两个 HITL 通道。不新开接口/rc 字段——
// 复用同一注入点（rc.Approver）与 TUI send 桥。ask_user/plan_enter/plan_done
// 工具经 Ask 向用户提问。
type Approver interface {
	// Request 审批一次工具调用，返回 y/s/n 决策。
	Request(ctx context.Context, req ApprovalRequest) (Decision, error)
	// Ask 向用户提一个问题，返回选项选择 + 自定义文本（ADR-036）。
	// Options 非空 = 选项选择；AllowCustom 允许用户在选项外输自定义文本；
	// Multiple 允许多选。AskResult.Selection 是选中项 label 列表，Custom 是
	// 用户自定义输入（非空表示选了 Other）。ctx canceled → 返回 error（调用
	// 方按 Fatal 处理）。
	Ask(ctx context.Context, req AskRequest) (AskResult, error)
}

// AskRequest 描述一次向用户的提问（参照 codex request_user_input / opencode
// question，ADR-036）。
type AskRequest struct {
	// Question 是完整问题文本（展示给用户）。
	Question string
	// Header 是弹窗短标题（≤30 字符）。
	Header string
	// Options 是选项列表（label + description）。空 = 纯自由文本提问。
	Options []AskOption
	// Multiple 允许多选（默认单选；opencode multiple 对位）。
	Multiple bool
	// AllowCustom 允许用户在选项外输入自定义文本（默认 true；opencode
	// custom 默认 true 对位）。
	AllowCustom bool
	// AgentID 是子 agent 归属标识（阶段 5，ADR-045）：语义同
	// ApprovalRequest.AgentID；空 = 主会话。
	AgentID string
}

// AskOption 是 Ask 的一个选项（opencode Option{label, description} 对位）。
type AskOption struct {
	Label       string
	Description string
}

// AskResult 是用户对 Ask 的回答。
type AskResult struct {
	// Selection 是选中的选项 label（Multiple 时可多个）。
	Selection []string
	// Custom 是用户自定义输入文本（非空表示用户选了 Other 并输入）。
	Custom string
}

// HasSelection 判断选中了给定 label（大小写敏感）。
func (r AskResult) HasSelection(label string) bool {
	for _, s := range r.Selection {
		if s == label {
			return true
		}
	}
	return false
}

// ApprovalRequest 描述一次待审批的工具调用（展示给用户）。
type ApprovalRequest struct {
	// ToolName 是工具名（shell_command / write_file ...）。
	ToolName string
	// Summary 是人类可读摘要（shell 命令原文 / 文件路径 / 参数前 80 字符）。
	Summary string
	// Mode 是当前审批模式（readonly/acceptedit/bypass，展示用）。
	Mode string
	// AgentID 是子 agent 归属标识（阶段 5，ADR-045）：非空 = 子 agent 发起
	// 的审批，渲染前缀【子 agent <id>】；空 = 主会话（ApprovalMiddleware
	// 构造时不填，subagentApprover 转发时填）。
	AgentID string
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
