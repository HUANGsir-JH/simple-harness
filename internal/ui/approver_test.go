package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/middleware"
)

// TestParseApprovalDecision 验证审批输入解析（y/s/n + 中文别名；非法 false）。
func TestParseApprovalDecision(t *testing.T) {
	cases := []struct {
		in   string
		want middleware.Decision
		ok   bool
	}{
		{"y", middleware.DecisionAllow, true},
		{"Y", middleware.DecisionAllow, true},
		{"yes", middleware.DecisionAllow, true},
		{"允许", middleware.DecisionAllow, true},
		{"s", middleware.DecisionAllowSession, true},
		{"session", middleware.DecisionAllowSession, true},
		{"记住", middleware.DecisionAllowSession, true},
		{"n", middleware.DecisionDeny, true},
		{"N", middleware.DecisionDeny, true},
		{"no", middleware.DecisionDeny, true},
		{"拒绝", middleware.DecisionDeny, true},
		{"", middleware.DecisionDeny, false},
		{"maybe", middleware.DecisionDeny, false},
		{"  y  ", middleware.DecisionAllow, true}, // trim
	}
	for _, c := range cases {
		got, ok := ParseApprovalDecision(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parse(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestChannelApproverRoundTrip 验证请求→主循环答复→决策回传的完整协调。
func TestChannelApproverRoundTrip(t *testing.T) {
	reqCh := make(chan *ApprovalPrompt, 1)
	askCh := make(chan *AskPrompt, 1)
	appr := NewChannelApprover(reqCh, askCh)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// agent goroutine 发起审批请求。
	resCh := make(chan middleware.Decision, 1)
	errCh := make(chan error, 1)
	go func() {
		d, err := appr.Request(ctx, middleware.ApprovalRequest{ToolName: "shell_command", Summary: "git push", Mode: "acceptedit"})
		if err != nil {
			errCh <- err
			return
		}
		resCh <- d
	}()

	// 主循环收到请求。
	var ar *ApprovalPrompt
	select {
	case ar = <-reqCh:
	case <-ctx.Done():
		t.Fatal("请求未到达主循环")
	}
	if ar.Req.ToolName != "shell_command" || ar.Req.Summary != "git push" {
		t.Errorf("请求内容: %+v", ar.Req)
	}
	// 模拟用户输入 "s" → AllowSession。
	ar.Resp <- middleware.DecisionAllowSession
	select {
	case d := <-resCh:
		if d != middleware.DecisionAllowSession {
			t.Errorf("决策: got %v", d)
		}
	case err := <-errCh:
		t.Fatalf("Request 错误: %v", err)
	case <-ctx.Done():
		t.Fatal("决策未回传")
	}
}

// TestChannelApproverCtxCancel 验证 ctx canceled（Esc 中断）时返回 Deny + err。
func TestChannelApproverCtxCancel(t *testing.T) {
	reqCh := make(chan *ApprovalPrompt, 1)
	askCh := make(chan *AskPrompt, 1)
	appr := NewChannelApprover(reqCh, askCh)
	ctx, cancel := context.WithCancel(context.Background())

	resCh := make(chan struct{})
	go func() {
		// 请求已发出但主循环不答复 → 只等 ctx.Done。
		d, err := appr.Request(ctx, middleware.ApprovalRequest{ToolName: "write_file", Summary: "x", Mode: "readonly"})
		if d != middleware.DecisionDeny || !errors.Is(err, context.Canceled) {
			t.Errorf("ctx cancel: got (%v, %v), want (deny, Canceled)", d, err)
		}
		close(resCh)
	}()
	<-reqCh  // 请求到达主循环
	cancel() // 用户 Esc 中断
	<-resCh
}

// TestParseAskAnswer 验证提问输入解析（编号选选项 / 自定义文本 / 非法）。
func TestParseAskAnswer(t *testing.T) {
	req := middleware.AskRequest{
		Question:    "选哪个？",
		Options:     []middleware.AskOption{{Label: "A"}, {Label: "B"}},
		AllowCustom: true,
	}
	cases := []struct {
		in   string
		want middleware.AskResult
		ok   bool
	}{
		{"1", middleware.AskResult{Selection: []string{"A"}}, true},
		{"2", middleware.AskResult{Selection: []string{"B"}}, true},
		{"自定义答案", middleware.AskResult{Custom: "自定义答案"}, true},
		{"", middleware.AskResult{}, false},
		{"3", middleware.AskResult{Custom: "3"}, true}, // 越界编号 → AllowCustom 时视为自定义文本
	}
	for _, c := range cases {
		got, ok := ParseAskAnswer(c.in, req)
		if ok != c.ok {
			t.Errorf("parse(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && (got.Custom != c.want.Custom || !slicesEqual(got.Selection, c.want.Selection)) {
			t.Errorf("parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	// AllowCustom=false 时自定义文本非法。
	if _, ok := ParseAskAnswer("x", middleware.AskRequest{Options: []middleware.AskOption{{Label: "A"}}}); ok {
		t.Error("AllowCustom=false 时应拒绝自定义文本")
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestChannelApproverAskRoundTrip 验证 Ask 请求→回答回传的协调。
func TestChannelApproverAskRoundTrip(t *testing.T) {
	reqCh := make(chan *ApprovalPrompt, 1)
	askCh := make(chan *AskPrompt, 1)
	appr := NewChannelApprover(reqCh, askCh)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resCh := make(chan middleware.AskResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := appr.Ask(ctx, middleware.AskRequest{Question: "Q?", Options: []middleware.AskOption{{Label: "A"}}})
		if err != nil {
			errCh <- err
			return
		}
		resCh <- r
	}()

	var ar *AskPrompt
	select {
	case ar = <-askCh:
	case <-ctx.Done():
		t.Fatal("Ask 请求未到达主循环")
	}
	if ar.Req.Question != "Q?" {
		t.Errorf("问题: %+v", ar.Req)
	}
	ar.Resp <- middleware.AskResult{Selection: []string{"A"}}
	select {
	case r := <-resCh:
		if len(r.Selection) != 1 || r.Selection[0] != "A" {
			t.Errorf("回答: %+v", r)
		}
	case err := <-errCh:
		t.Fatalf("Ask 错误: %v", err)
	case <-ctx.Done():
		t.Fatal("回答未回传")
	}
}
