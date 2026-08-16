package web

import (
	"context"
	"fmt"

	"github.com/agent-project/harness/internal/middleware"
)

// pendingApproval 是待决审批请求（approvals 表条目）：req 展示用、respCh
// 回填决策（缓冲 1，多标签页任一响应即生效，第二个响应 404）。
type pendingApproval struct {
	req    middleware.ApprovalRequest
	respCh chan middleware.Decision
}

// pendingAsk 是待决提问请求（asks 表条目）。
type pendingAsk struct {
	req    middleware.AskRequest
	respCh chan middleware.AskResult
}

// webApprover 是审批交互器（注入 rc.Approver，对位 tui/tuiApprover）：把
// 审批请求（Request）与提问请求（Ask）登记进 Controller 的 pending 表 +
// Hub 广播 approval/ask 事件（带 request_id），前端经 POST /api/approve
// /api/ask 回填。
//
// 生命周期（review 修订）：
//   - 条目创建即登记（锁内），广播后等待 respCh
//   - 决策到达 → Controller.Approve/AnswerAsk 锁内**先删表再回填**（原子，
//     第二个响应 404；缓冲 1 防阻塞）
//   - ctx canceled（中断/切换/服务退出）→ 返回 Deny/空结果 + ctx.Err()，
//     defer delete 清理表条目（无孤儿）
//   - Hub 无订阅者（页面全关）→ 直接 ctx cancel 自释放（对位 TUI
//     send==nil → 自动拒绝，防回合卡死）
type webApprover struct {
	c *Controller
}

// webApprover 返回当前审批交互器（无 Hub（纯逻辑测试）时 nil → 自动拒绝）。
func (c *Controller) webApprover() middleware.Approver {
	if c.hub == nil {
		return nil
	}
	return &webApprover{c: c}
}

// Request 审批一次工具调用并等待用户决策。
func (a *webApprover) Request(ctx context.Context, req middleware.ApprovalRequest) (middleware.Decision, error) {
	if a.c.hub == nil || a.c.hub.Len() == 0 {
		return middleware.DecisionDeny, context.Canceled // 无订阅者 → 自动拒绝
	}
	id, respCh := a.c.registerApproval(req)
	defer a.c.deleteApproval(id)
	a.c.hubBroadcast("approval", map[string]any{
		"session_id": a.c.activeID(),
		"request_id": id,
		"req":        req,
	})
	select {
	case d := <-respCh:
		return d, nil
	case <-ctx.Done():
		return middleware.DecisionDeny, ctx.Err()
	}
}

// Ask 向用户提一个问题并等待回答（ADR-036）。
func (a *webApprover) Ask(ctx context.Context, req middleware.AskRequest) (middleware.AskResult, error) {
	if a.c.hub == nil || a.c.hub.Len() == 0 {
		return middleware.AskResult{}, context.Canceled // 无订阅者 → 取消
	}
	id, respCh := a.c.registerAsk(req)
	defer a.c.deleteAsk(id)
	a.c.hubBroadcast("ask", map[string]any{
		"session_id": a.c.activeID(),
		"request_id": id,
		"req":        req,
	})
	select {
	case r := <-respCh:
		return r, nil
	case <-ctx.Done():
		return middleware.AskResult{}, ctx.Err()
	}
}

// registerApproval 登记审批请求并返回 request_id 与回填通道。
func (c *Controller) registerApproval(req middleware.ApprovalRequest) (string, chan middleware.Decision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := fmt.Sprintf("a%d", c.seq)
	respCh := make(chan middleware.Decision, 1)
	c.approvals[id] = &pendingApproval{req: req, respCh: respCh}
	return id, respCh
}

// registerAsk 登记提问请求并返回 request_id。
func (c *Controller) registerAsk(req middleware.AskRequest) (string, chan middleware.AskResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := fmt.Sprintf("q%d", c.seq)
	respCh := make(chan middleware.AskResult, 1)
	c.asks[id] = &pendingAsk{req: req, respCh: respCh}
	return id, respCh
}

// deleteApproval 删除审批条目（defer 路径：ctx 释放时清理，无孤儿）。
func (c *Controller) deleteApproval(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.approvals, id)
}

// deleteAsk 删除提问条目。
func (c *Controller) deleteAsk(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.asks, id)
}

// Approve 回填审批决策（POST /api/approve）：锁内先删表再回填（原子，第二
// 个响应 404）。未知 request_id 返回错误（handler 转 404）。
func (c *Controller) Approve(id, decision string) error {
	var d middleware.Decision
	switch decision {
	case "allow":
		d = middleware.DecisionAllow
	case "session":
		d = middleware.DecisionAllowSession
	case "deny":
		d = middleware.DecisionDeny
	default:
		return fmt.Errorf("未知决策 %q（allow|session|deny）", decision)
	}
	c.mu.Lock()
	p, ok := c.approvals[id]
	if ok {
		delete(c.approvals, id)
	}
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown approval request")
	}
	p.respCh <- d // 缓冲 1，不阻塞
	return nil
}

// AnswerAsk 回填提问回答（POST /api/ask）：锁内先删表再回填。
func (c *Controller) AnswerAsk(id string, res middleware.AskResult) error {
	c.mu.Lock()
	p, ok := c.asks[id]
	if ok {
		delete(c.asks, id)
	}
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown ask request")
	}
	p.respCh <- res
	return nil
}

// pendingSnapshots 返回当前待决审批/提问快照（state.pending 恢复用，D4/
// H2：重连/刷新后前端据此恢复弹窗）。锁内读表构建。
func (c *Controller) pendingSnapshots() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for id, p := range c.approvals {
		out = append(out, map[string]any{"kind": "approval", "request_id": id, "req": p.req})
	}
	for id, p := range c.asks {
		out = append(out, map[string]any{"kind": "ask", "request_id": id, "req": p.req})
	}
	return out
}
