package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// ApprovalMiddleware 是阶段三权限审批（ADR-029），挂 onActing（before = 审批）。
//
// 无状态（ADR-026）：只持 DefaultMode（config 默认模式）；会话级状态
// （Mode/Approved）经 rc.State.Permission 读写，SessionMiddleware 负责落盘
// ——共享 chain 可并发。Approver 从 rc.Approver 读（CLI 注入）；
// nil = 自动拒绝（非 TTY 场景）。
type ApprovalMiddleware struct {
	middleware.Base
	// DefaultMode 是 config 默认模式；rc.State.Permission.Mode 非空时覆盖
	// （会话级，resume 恢复）。
	DefaultMode string
}

// OnActing before = 审批：Policy.Decide → Allow 放行 / Ask 询问 /
// Deny 拒绝（回填模型，循环继续）。
func (m ApprovalMiddleware) OnActing(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ActingInput, next middleware.ActingHandler) error {
	if in.Call == nil {
		return next(ctx, rc, in)
	}
	mode := m.mode(rc)
	ws := workspaceOf(rc)
	outcome, reason := Decide(in.Call, mode, approvedOf(rc), ws, planModeOf(rc))
	switch outcome {
	case OutcomeAllow:
		return next(ctx, rc, in)
	case OutcomeDeny:
		return &middleware.DeniedError{Reason: "审批拒绝：" + reason}
	case OutcomeAsk:
		appr := approverOf(rc)
		if appr == nil {
			// 非 TTY / 无审批者：自动拒绝（回填模型换思路；ADR-006）。
			return &middleware.DeniedError{Reason: "操作需要审批但当前环境无法询问用户，已自动拒绝"}
		}
		req := middleware.ApprovalRequest{
			ToolName: in.Call.Name,
			Summary:  SummaryOf(in.Call),
			Mode:     mode,
		}
		dec, err := appr.Request(ctx, req)
		if err != nil {
			// ctx canceled（Esc 中断等）：按 Fatal 终止（调用层取消整批）。
			return err
		}
		switch dec {
		case middleware.DecisionAllow:
			return next(ctx, rc, in)
		case middleware.DecisionAllowSession:
			rememberApproved(rc, approvalKeys(in.Call, ws))
			return next(ctx, rc, in)
		default: // DecisionDeny
			return &middleware.DeniedError{Reason: fmt.Sprintf("用户拒绝了该操作：%s %s", in.Call.Name, SummaryOf(in.Call))}
		}
	}
	return next(ctx, rc, in)
}

// mode 返回当前审批模式：config 默认（构造时 DefaultMode）→ 会话覆盖
// （rc.State.Permission.Mode）。
func (m ApprovalMiddleware) mode(rc *middleware.RuntimeContext) string {
	mode := m.DefaultMode
	if mode == "" {
		mode = DefaultMode
	}
	if rc != nil && rc.State != nil && rc.State.PermissionMode() != "" {
		mode = rc.State.PermissionMode()
	}
	return mode
}

func approverOf(rc *middleware.RuntimeContext) middleware.Approver {
	if rc == nil {
		return nil
	}
	return rc.Approver
}

func approvedOf(rc *middleware.RuntimeContext) []string {
	if rc == nil || rc.State == nil {
		return nil
	}
	return rc.State.Approved() // 防御性拷贝（AgentState 锁下沉，ADR-036 修订）
}

// planModeOf 读取当前是否 plan 模式（ADR-036）。plan 分支强只读优先于权限模式。
func planModeOf(rc *middleware.RuntimeContext) bool {
	if rc == nil || rc.State == nil {
		return false
	}
	return rc.State.IsPlanMode()
}

// workspaceOf 取审批判定的 workspace 根：rc.State.CWD（会话启动目录，ADR-028）
// 优先，nil 返回空（Decide 内部回退进程 cwd）。与工具层 ResolveInWorkspace
// 同源，审批与工具对"相对路径基准"一致。
func workspaceOf(rc *middleware.RuntimeContext) string {
	if rc != nil && rc.State != nil {
		return rc.State.CWD
	}
	return ""
}

// rememberApproved 把审批 key 记入会话级记忆（去重，逻辑已下沉到
// AgentState.AddApproved），随 AgentState 落盘（SessionMiddleware after），
// resume 后模型可见（ADR-029）。文件工具传入多 key（每个目标路径一条，
// Bug03：批准一次 apply_patch 记住其每个文件）。
func rememberApproved(rc *middleware.RuntimeContext, keys []string) {
	if rc == nil || rc.State == nil {
		return
	}
	rc.State.AddApproved(keys)
}

// SummaryOf 生成工具调用的人类可读摘要（审批 UI 展示）：
// shell → 命令原文 / kill 模式显式展示；write_file → 写入路径；
// apply_patch → 补丁首行；其它 → 参数 JSON 前 80 字符。
func SummaryOf(call *messages.ToolCall) string {
	switch call.Name {
	case "shell_command":
		// kill 模式（ADR-038）：command 为空，直接返回 cmdOf 会得到空摘要
		// （TUI 兜底显示工具名，信息不足），显式展示杀的目标 PID。
		if pid := killPIDOf(call); pid > 0 {
			return fmt.Sprintf("shell_command: kill %d", pid)
		}
		return cmdOf(call)
	case "write_file":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.Args, &p); err == nil && p.Path != "" {
			return "写入文件: " + p.Path
		}
	case "apply_patch":
		var p struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(call.Args, &p); err == nil {
			first := strings.SplitN(p.Patch, "\n", 2)[0]
			if len(first) > 60 {
				first = first[:60] + "…"
			}
			return "应用补丁: " + first
		}
	}
	s := string(call.Args)
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
