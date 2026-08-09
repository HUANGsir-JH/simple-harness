package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/middleware"
)

// approvalRequest 是主循环与审批者之间的审批请求（channel 协调，ADR-029）。
// agent goroutine 里的 channelApprover.Request 把请求发到 reqCh，REPL/runCmd
// 主循环消费并打印审批 UI，把下一行输入解析为决策经 resp 送回。
type approvalRequest struct {
	req  middleware.ApprovalRequest
	resp chan middleware.Decision
}

// channelApprover 是审批交互器（CLI 注入 rc.Approver）：把审批请求转发给
// 主循环。单一读方原则（ADR-028）：stdin 由 readStdinEvents 独占读取，
// 审批交互不直接读 stdin，而是经 channel 与主循环协调。
//
// ctx canceled（用户 Esc 中断回合）时返回 Deny + ctx.Err()，ApprovalMiddleware
// 按其 Fatal 处理终止当前 turn（回合已被中断）。
type channelApprover struct {
	reqCh chan *approvalRequest
}

// newChannelApprover 创建审批者。reqCh 由调用方持有（REPL/runCmd 主循环
// select 消费；nil = 不启用审批交互，非 TTY 场景自动拒绝）。
func newChannelApprover(reqCh chan *approvalRequest) *channelApprover {
	return &channelApprover{reqCh: reqCh}
}

func (a *channelApprover) Request(ctx context.Context, req middleware.ApprovalRequest) (middleware.Decision, error) {
	ar := &approvalRequest{req: req, resp: make(chan middleware.Decision, 1)}
	select {
	case a.reqCh <- ar:
	case <-ctx.Done():
		return middleware.DecisionDeny, ctx.Err()
	}
	select {
	case d := <-ar.resp:
		return d, nil
	case <-ctx.Done():
		return middleware.DecisionDeny, ctx.Err()
	}
}

// parseApprovalDecision 解析审批输入行（y/s/n；支持中文别名）。
// 非法输入返回 ok=false（调用方重提示）。
func parseApprovalDecision(line string) (middleware.Decision, bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "允许", "是":
		return middleware.DecisionAllow, true
	case "s", "session", "记住":
		return middleware.DecisionAllowSession, true
	case "n", "no", "拒绝", "否":
		return middleware.DecisionDeny, true
	}
	return middleware.DecisionDeny, false
}

// printApprovalUI 打印审批提示（文本渲染器）。
func printApprovalUI(req middleware.ApprovalRequest) {
	fmt.Printf("\n%s[审批] %s%s", ansiYellow, req.ToolName, ansiReset)
	if req.Summary != "" {
		fmt.Printf(" %s", req.Summary)
	}
	fmt.Printf("\n  模式 %s ｜ 允许(y) / 本会话记住(s) / 拒绝(n) > ", req.Mode)
}

// emitApprovalJSON 输出审批请求的 JSON 事件（--json 模式排障用）。
func emitApprovalJSON(req middleware.ApprovalRequest) {
	emitJSON(map[string]any{"type": "approval_request", "tool": req.ToolName, "summary": req.Summary, "mode": req.Mode})
}
