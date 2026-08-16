package web

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
)

// testConfig 是测试配置（含 provider 与模型 efforts）。
func testConfig() config.Config {
	return config.Config{
		DefaultProvider: "p",
		Providers: map[string]config.ProviderSpec{
			"p": {
				APIKey: "sk-test",
				Models: map[string]config.Model{
					"test-model": {Thinking: &config.Thinking{Efforts: []string{"low", "high", "max"}}},
				},
			},
		},
	}
}

// testController 构造测试用 Controller：隔离 HARNESS_HOME + FakeClient agent +
// Hub。streamFn 为 LLM 响应脚本；mode 为审批默认模式。返回 controller 与 hub。
func testController(t *testing.T, mode string, streamFn func(ctx context.Context, req provider.Request) (provider.EventStream, error)) (*Controller, *Hub) {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	res := &config.ProviderConfig{
		ProviderID: "p", Model: "test-model", APIKey: "sk-test",
		BaseURL: "http://127.0.0.1:1", ThinkingEffort: "high",
		ThinkingEfforts: []string{"low", "high", "max"},
	}
	client := &provider.FakeClient{StreamFn: streamFn}
	a, err := agent.Build(agent.BuildOptions{Provider: res, Client: client, DefaultMode: mode})
	if err != nil {
		t.Fatalf("agent.Build: %v", err)
	}
	proj, err := session.ProjectForCWD()
	if err != nil {
		t.Fatalf("ProjectForCWD: %v", err)
	}
	hub := NewHub()
	c := NewController(a, proj, testConfig(), nil, func() (*session.Session, error) {
		return session.CreateInCWD("test-model", mode)
	}, context.Background())
	c.SetHub(hub)
	t.Cleanup(c.CloseAll)
	return c, hub
}

// textStream 构造一个输出固定文本的脚本化流。
func textStream(text string) func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
	return func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDelta, Text: text},
			{Type: provider.EventTextDone, Text: text},
			{Type: provider.EventDone, Usage: &messages.Usage{InputTokens: 10, OutputTokens: 5}},
		}), nil
	}
}

// sseMsg 解析 hub 通道的一条 SSE 消息（"event: x\ndata: {...}\n\n"）。
func sseMsg(t *testing.T, msg []byte) (eventType string, data map[string]any) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(msg)), "\n")
	for _, line := range lines {
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			_ = json.Unmarshal([]byte(v), &data)
		}
	}
	return
}

// waitSSE 等待指定事件类型（返回其 data）。
func waitSSE(t *testing.T, ch <-chan []byte, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				t.Fatalf("hub 通道关闭（等 %s）", want)
			}
			ev, data := sseMsg(t, msg)
			if ev == want {
				return data
			}
		case <-deadline:
			t.Fatalf("超时：未收到事件 %s", want)
		}
	}
}

// --- 输入路由 -----------------------------------------------------------------

// TestHandleInputMessageStarted 验证普通消息启动回合（started 响应 + SSE
// 事件流含 run_done）。
func TestHandleInputMessageStarted(t *testing.T) {
	c, hub := testController(t, "bypass", textStream("你好"))
	res := c.HandleInput("你好")
	if !res.OK || res.Kind != "started" {
		t.Fatalf("响应: %+v", res)
	}
	ch, unsub := hub.Subscribe()
	defer unsub()
	data := waitSSE(t, ch, "run_done", 3*time.Second)
	if data["error"] != "" {
		t.Errorf("run_done error: %v", data["error"])
	}
}

// TestHandleInputQueued 验证 running 时输入入队（含命令）。
func TestHandleInputQueued(t *testing.T) {
	var n atomic.Int32
	streamFn := func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if n.Add(1) == 1 {
			// 第一轮阻塞（回合保持运行 → 后续输入入队）。
			<-ctx.Done()
			return provider.NewFakeStream([]provider.Event{{Type: provider.EventDone}}), nil
		}
		// 后续轮正常结束（队列消费后 idle）。
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventTextDone, Text: "ok"},
			{Type: provider.EventDone},
		}), nil
	}
	c, _ := testController(t, "bypass", streamFn)
	res := c.HandleInput("第一条")
	if res.Kind != "started" {
		t.Fatalf("第一条: %+v", res)
	}
	// 确保第一条已进入运行态（goroutine 启动需要时间）。
	time.Sleep(100 * time.Millisecond)
	res = c.HandleInput("第二条")
	if !res.OK || res.Kind != "queued" || res.QueueLen != 1 {
		t.Fatalf("第二条应入队: %+v", res)
	}
	res = c.HandleInput("/model other")
	if !res.OK || res.Kind != "queued" {
		t.Fatalf("命令也应入队: %+v", res)
	}
	c.Interrupt()
	waitIdle(t, c)
}

// --- webApprover（直测：不走 agent） ------------------------------------------

// TestWebApproverRequest 验证审批登记 → 广播 approval → 回填决策。
func TestWebApproverRequest(t *testing.T) {
	c, hub := testController(t, "bypass", textStream("hi"))
	// 先创建会话（approval 广播带 session_id）。
	c.HandleInput("/help")
	waitIdle(t, c)

	ch, unsub := hub.Subscribe()
	defer unsub()

	ap := &webApprover{c: c}
	done := make(chan struct{})
	var decision middleware.Decision
	var reqErr error
	go func() {
		defer close(done)
		decision, reqErr = ap.Request(context.Background(), middleware.ApprovalRequest{
			ToolName: "shell_command", Summary: "rm -rf /tmp/x", Mode: "acceptedit",
		})
	}()
	data := waitSSE(t, ch, "approval", 2*time.Second)
	if data["request_id"] == "" {
		t.Fatalf("approval 缺 request_id: %v", data)
	}
	rid := data["request_id"].(string)
	if err := c.Approve(rid, "allow"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	<-done
	if reqErr != nil || decision != middleware.DecisionAllow {
		t.Errorf("决策: %v err: %v", decision, reqErr)
	}
	// 重复审批 404。
	if err := c.Approve(rid, "allow"); err == nil {
		t.Error("重复审批应失败")
	}
	// 表已清空。
	c.mu.Lock()
	n := len(c.approvals)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("审批后表未清空: %d", n)
	}
}

// TestWebApproverAsk 验证提问登记 → 广播 ask → 回填回答。
func TestWebApproverAsk(t *testing.T) {
	c, hub := testController(t, "bypass", textStream("hi"))
	c.HandleInput("/help")
	waitIdle(t, c)

	ch, unsub := hub.Subscribe()
	defer unsub()

	ap := &webApprover{c: c}
	done := make(chan struct{})
	var res middleware.AskResult
	var askErr error
	go func() {
		defer close(done)
		res, askErr = ap.Ask(context.Background(), middleware.AskRequest{
			Question: "选一个", Options: []middleware.AskOption{{Label: "A"}, {Label: "B"}},
		})
	}()
	data := waitSSE(t, ch, "ask", 2*time.Second)
	rid := data["request_id"].(string)
	if err := c.AnswerAsk(rid, middleware.AskResult{Selection: []string{"A"}}); err != nil {
		t.Fatalf("AnswerAsk: %v", err)
	}
	<-done
	if askErr != nil || !res.HasSelection("A") {
		t.Errorf("回答: %+v err: %v", res, askErr)
	}
}

// TestWebApproverNoSubscriber 验证无订阅者时自动拒绝（页面全关防卡死）。
func TestWebApproverNoSubscriber(t *testing.T) {
	c, _ := testController(t, "bypass", textStream("hi"))
	c.HandleInput("/help")
	waitIdle(t, c)
	// hub 无订阅者。
	ap := &webApprover{c: c}
	_, err := ap.Request(context.Background(), middleware.ApprovalRequest{ToolName: "x"})
	if err == nil {
		t.Fatal("无订阅者应自动拒绝（返回错误）")
	}
	// 表应无残留。
	c.mu.Lock()
	n := len(c.approvals)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("无订阅者路径残留 %d", n)
	}
}

// TestWebApproverInterruptCleanup 验证中断清空 pending 表 + Request 释放。
func TestWebApproverInterruptCleanup(t *testing.T) {
	c, hub := testController(t, "bypass", textStream("hi"))
	c.HandleInput("/help")
	waitIdle(t, c)

	ch, unsub := hub.Subscribe()
	defer unsub()

	ap := &webApprover{c: c}
	done := make(chan struct{})
	var reqErr error
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	go func() {
		defer close(done)
		// 真实场景 ctx 是 run ctx：Interrupt → cancelRun → ctx.Done 释放。
		_, reqErr = ap.Request(reqCtx, middleware.ApprovalRequest{ToolName: "x"})
	}()
	waitSSE(t, ch, "approval", 2*time.Second)
	// 表有 1 条。
	c.mu.Lock()
	n := len(c.approvals)
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("审批未登记: %d", n)
	}
	c.Interrupt()
	reqCancel() // 模拟 run ctx 随中断取消
	<-done
	if reqErr == nil {
		t.Error("中断后 Request 应返回错误")
	}
	c.mu.Lock()
	n = len(c.approvals)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("中断后表应清空: %d", n)
	}
}

// --- 命令分发 -----------------------------------------------------------------

// TestCommandModel 验证 /model 带参执行成功 + 无参 select。
func TestCommandModel(t *testing.T) {
	c, _ := testController(t, "bypass", textStream("hi"))
	res := c.HandleInput("/model test-model")
	if !res.OK || res.Kind != "ok" {
		t.Fatalf("/model: %+v", res)
	}
	// 无参 → select（models 含 test-model）。
	res = c.HandleInput("/model")
	if !res.OK || res.Kind != "select" || res.Title != "Models" {
		t.Fatalf("/model 无参: %+v", res)
	}
	if len(res.Items) != 1 || res.Items[0].Value != "test-model" {
		t.Errorf("select items: %+v", res.Items)
	}
	// 带参提交路径：模拟前端选中后提交。
	res = c.HandleInput("/model test-model")
	if !res.OK || res.Kind != "ok" {
		t.Fatalf("/model 带参: %+v", res)
	}
}

// TestCommandEffort 验证 /effort select 与带参执行。
func TestCommandEffort(t *testing.T) {
	c, _ := testController(t, "bypass", textStream("hi"))
	res := c.HandleInput("/effort high")
	if !res.OK || res.Kind != "ok" {
		t.Fatalf("/effort high: %+v", res)
	}
	res = c.HandleInput("/effort maxx")
	if res.OK {
		t.Fatalf("非法 effort 应失败: %+v", res)
	}
}

// TestCommandSwitchLast 验证 /switch --last 无会话时错误。
func TestCommandSwitchLast(t *testing.T) {
	c, _ := testController(t, "bypass", textStream("hi"))
	res := c.HandleInput("/switch --last")
	if res.OK {
		t.Fatalf("无会话时 --last 应失败: %+v", res)
	}
}

// TestCommandUnknown 验证未知命令报错。
func TestCommandUnknown(t *testing.T) {
	c, _ := testController(t, "bypass", textStream("hi"))
	res := c.HandleInput("/bogus")
	if res.OK {
		t.Fatalf("未知命令应失败: %+v", res)
	}
}

// TestCommandExitHelp 验证 /exit 与 /help。
func TestCommandExitHelp(t *testing.T) {
	c, _ := testController(t, "bypass", textStream("hi"))
	if res := c.HandleInput("/exit"); !res.OK || res.Kind != "exit" {
		t.Fatalf("/exit: %+v", res)
	}
	if res := c.HandleInput("/help"); !res.OK || res.Kind != "help" {
		t.Fatalf("/help: %+v", res)
	}
}

// TestCommandPlan 验证 /plan toggle 广播 state_changed{status}。
func TestCommandPlan(t *testing.T) {
	c, hub := testController(t, "bypass", textStream("hi"))
	ch, unsub := hub.Subscribe()
	defer unsub()
	res := c.HandleInput("/plan")
	if !res.OK || res.Kind != "ok" {
		t.Fatalf("/plan: %+v", res)
	}
	data := waitSSE(t, ch, "state_changed", 2*time.Second)
	if data["reason"] != "status" {
		t.Errorf("state_changed reason: %v", data["reason"])
	}
	// 已开启。
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil || !active.State().IsPlanMode() {
		t.Error("plan 模式应已开启")
	}
}

// --- 状态快照 -----------------------------------------------------------------

// TestState 验证无会话时 state 快照。
func TestState(t *testing.T) {
	c, _ := testController(t, "bypass", textStream("hi"))
	s := c.State()
	if s.Active != nil {
		t.Errorf("未创建会话时 active 应 nil: %+v", s.Active)
	}
	if len(s.Timeline) != 0 || len(s.Pending) != 0 {
		t.Errorf("未创建会话时 timeline/pending 应空")
	}
}

// TestStateAfterRun 验证回合后 state 包含会话与 timeline + 首消息自动命名。
func TestStateAfterRun(t *testing.T) {
	c, _ := testController(t, "bypass", textStream("你好世界"))
	c.HandleInput("你好")
	waitIdle(t, c)
	s := c.State()
	if s.Active == nil {
		t.Fatal("回合后应创建会话")
	}
	if len(s.Timeline) == 0 {
		t.Error("回合后 timeline 应非空")
	}
	if s.Active.Name == "" {
		t.Error("首消息应自动命名会话")
	}
}

// waitIdle 等待 controller 空闲（running=false）。
func waitIdle(t *testing.T, c *Controller) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		c.mu.Lock()
		running := c.running
		c.mu.Unlock()
		if !running {
			return
		}
		select {
		case <-deadline:
			t.Fatal("controller 未空闲")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestErrResult 验证错误消息透传。
func TestErrResult(t *testing.T) {
	c, _ := testController(t, "bypass", textStream("hi"))
	err := errors.New("模型 %q 不支持 effort")
	res := c.errResult(err)
	if res.Error != "模型 %q 不支持 effort" {
		t.Errorf("错误文本: %q", res.Error)
	}
}
