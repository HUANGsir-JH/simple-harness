package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/agent-project/harness/internal/middleware"
)

// ApprovalPrompt 是主循环与审批者之间的审批请求（channel 协调，ADR-029）。
// agent goroutine 里的 ChannelApprover.Request 把请求发到 reqCh，REPL/runCmd
// 主循环消费并打印审批 UI，把下一行输入解析为决策经 resp 送回。
type ApprovalPrompt struct {
	Req  middleware.ApprovalRequest
	Resp chan middleware.Decision
}

// AskPrompt 是主循环与审批者之间的提问请求（ADR-036，Ask 方法）。
// ChannelApprover.Ask 把请求发到 askCh，主循环打印问题/选项，把下一行输入
// 解析为回答（选项编号或自定义文本）经 resp 送回。
type AskPrompt struct {
	Req  middleware.AskRequest
	Resp chan middleware.AskResult
}

// ChannelApprover 是审批交互器（CLI 注入 rc.Approver，ADR-029/036）：把审批
// 请求（Request）与提问请求（Ask）都转发给主循环。单一读方原则（ADR-028）：
// stdin 由 readStdinEvents 独占读取，交互不直接读 stdin，而是经 channel 与
// 主循环协调。
//
// ctx canceled（用户 Esc 中断回合）时 Request 返回 Deny + ctx.Err()、
// Ask 返回 error，ApprovalMiddleware / 工具按其 Fatal 处理终止当前 turn。
type ChannelApprover struct {
	reqCh chan *ApprovalPrompt
	askCh chan *AskPrompt
}

// NewChannelApprover 创建审批者。reqCh/askCh 由调用方持有（REPL/runCmd 主循环
// select 消费；nil = 不启用审批交互，非 TTY 场景自动拒绝）。
func NewChannelApprover(reqCh chan *ApprovalPrompt, askCh chan *AskPrompt) *ChannelApprover {
	return &ChannelApprover{reqCh: reqCh, askCh: askCh}
}

func (a *ChannelApprover) Request(ctx context.Context, req middleware.ApprovalRequest) (middleware.Decision, error) {
	ar := &ApprovalPrompt{Req: req, Resp: make(chan middleware.Decision, 1)}
	select {
	case a.reqCh <- ar:
	case <-ctx.Done():
		return middleware.DecisionDeny, ctx.Err()
	}
	select {
	case d := <-ar.Resp:
		return d, nil
	case <-ctx.Done():
		return middleware.DecisionDeny, ctx.Err()
	}
}

func (a *ChannelApprover) Ask(ctx context.Context, req middleware.AskRequest) (middleware.AskResult, error) {
	ar := &AskPrompt{Req: req, Resp: make(chan middleware.AskResult, 1)}
	select {
	case a.askCh <- ar:
	case <-ctx.Done():
		return middleware.AskResult{}, ctx.Err()
	}
	select {
	case r := <-ar.Resp:
		return r, nil
	case <-ctx.Done():
		return middleware.AskResult{}, ctx.Err()
	}
}

// ParseApprovalDecision 解析审批输入行（y/s/n；支持中文别名）。
// 非法输入返回 ok=false（调用方重提示）。
func ParseApprovalDecision(line string) (middleware.Decision, bool) {
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

// PrintApprovalUI 打印审批提示（文本渲染器）。
func PrintApprovalUI(req middleware.ApprovalRequest) {
	fmt.Printf("\n%s[审批] %s%s", ansiYellow, req.ToolName, ansiReset)
	if req.Summary != "" {
		fmt.Printf(" %s", req.Summary)
	}
	fmt.Printf("\n  模式 %s ｜ 允许(y) / 本会话记住(s) / 拒绝(n) > ", req.Mode)
}

// PrintAskUI 打印提问提示（文本渲染器，run 模式；ADR-036）。
func PrintAskUI(req middleware.AskRequest) {
	header := req.Header
	if header == "" {
		header = "提问"
	}
	fmt.Printf("\n%s[%s]%s %s\n", ansiYellow, header, ansiReset, req.Question)
	for i, o := range req.Options {
		fmt.Printf("  %d. %s", i+1, o.Label)
		if o.Description != "" {
			fmt.Printf(" - %s", o.Description)
		}
		fmt.Println()
	}
	fmt.Printf("  输入选项编号，或直接输入自定义文本 > ")
}

// ParseAskAnswer 解析提问输入行（run 模式）：数字命中选项 → Selection；
// 否则非空文本 → Custom（AllowCustom=false 时非编号输入非法）。
func ParseAskAnswer(line string, req middleware.AskRequest) (middleware.AskResult, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return middleware.AskResult{}, false
	}
	if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(req.Options) {
		return middleware.AskResult{Selection: []string{req.Options[n-1].Label}}, true
	}
	if !req.AllowCustom {
		return middleware.AskResult{}, false
	}
	return middleware.AskResult{Custom: line}, true
}

// EmitApprovalJSON 输出审批请求的 JSON 事件（--json 模式排障用）。
func EmitApprovalJSON(req middleware.ApprovalRequest) {
	emitJSON(map[string]any{"type": "approval_request", "tool": req.ToolName, "summary": req.Summary, "mode": req.Mode})
}
