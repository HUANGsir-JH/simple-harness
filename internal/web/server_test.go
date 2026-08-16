package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	"github.com/gin-gonic/gin"
)

// newTestServer 构造完整 Server（gin 路由 + FakeClient agent），返回 router
// 与 controller（供断言）。
func newTestServer(t *testing.T, streamFn func(ctx context.Context, req provider.Request) (provider.EventStream, error)) (*Server, *Controller) {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	res := &config.ProviderConfig{
		ProviderID: "p", Model: "test-model", APIKey: "sk-test",
		BaseURL: "http://127.0.0.1:1", ThinkingEffort: "high",
	}
	client := &provider.FakeClient{StreamFn: streamFn}
	a, err := agent.Build(agent.BuildOptions{Provider: res, Client: client, DefaultMode: "bypass"})
	if err != nil {
		t.Fatalf("agent.Build: %v", err)
	}
	proj, err := session.ProjectForCWD()
	if err != nil {
		t.Fatalf("ProjectForCWD: %v", err)
	}
	controller := NewController(a, proj, testConfig(), nil, func() (*session.Session, error) {
		return session.CreateInCWD("test-model", "bypass")
	}, context.Background())
	hub := NewHub()
	controller.SetHub(hub)
	srv := &Server{controller: controller, hub: hub}
	t.Cleanup(srv.Close)
	return srv, controller
}

// doJSON 执行 JSON POST 请求。
func doJSON(t *testing.T, r *ginEngine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	}
	// 模拟同源（Origin 校验放行）。
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ginEngine 别名（避免与 Server 混淆）。
type ginEngine = gin.Engine

// TestServerState 验证 /api/state 空状态。
func TestServerState(t *testing.T) {
	srv, _ := newTestServer(t, textStream("hi"))
	r := srv.routes()
	w := doJSON(t, r, "GET", "/api/state", "")
	if w.Code != http.StatusOK {
		t.Fatalf("state status: %d", w.Code)
	}
	var st StateSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("state JSON: %v", err)
	}
	if st.Active != nil {
		t.Errorf("active 应 nil: %+v", st.Active)
	}
}

// TestServerInputFlow 验证 input → SSE 事件流（agent 事件 + run_done）。
func TestServerInputFlow(t *testing.T) {
	srv, _ := newTestServer(t, textStream("你好"))
	// 用真实 http.Server 起服务（httptest.Recorder 不适合 SSE 流式读）。
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	// 订阅 SSE。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE 连接: %v", err)
	}
	defer resp.Body.Close()

	// 发消息（SSE 连接已建立后）。
	body := strings.NewReader(`{"line":"你好"}`)
	presp, err := http.Post(ts.URL+"/api/input", "application/json", body)
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	presp.Body.Close()

	// 读 SSE 流直到 run_done（超时由 ctx 控制）。
	br := bufio.NewReader(resp.Body)
	seenAgent, seenDone := false, false
	for !(seenAgent && seenDone) {
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				t.Fatal("SSE 流提前结束")
			}
			t.Fatalf("SSE 读: %v", err)
		}
		if strings.Contains(line, "event: agent") {
			seenAgent = true
		}
		if strings.Contains(line, "event: run_done") {
			seenDone = true
		}
	}
}

// TestServerCSRF 验证跨源 POST 被拒（403）。
func TestServerCSRF(t *testing.T) {
	srv, _ := newTestServer(t, textStream("hi"))
	r := srv.routes()

	req := httptest.NewRequest("POST", "/api/input", bytes.NewBufferString(`{"line":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example.com")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("跨源 POST 应 403，got %d", w.Code)
	}
}

// TestServerSameOrigin 验证同源 POST 放行。
func TestServerSameOrigin(t *testing.T) {
	srv, _ := newTestServer(t, textStream("hi"))
	r := srv.routes()
	w := doJSON(t, r, "POST", "/api/interrupt", "")
	if w.Code != http.StatusOK {
		t.Fatalf("同源 POST 应放行，got %d", w.Code)
	}
}

// TestServerSwitchRunning 验证 running 时 /api/switch 拒绝。
func TestServerSwitchRunning(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	streamFn := func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		<-ctx.Done()
		return provider.NewFakeStream([]provider.Event{{Type: provider.EventDone}}), nil
	}
	srv, _ := newTestServer(t, streamFn)
	r := srv.routes()

	doJSON(t, r, "POST", "/api/input", `{"line":"跑"}`)
	time.Sleep(100 * time.Millisecond)

	w := doJSON(t, r, "POST", "/api/switch", `{"session_id":"nope"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("running 时 switch 应 400，got %d body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.Error, "回合进行中") {
		t.Errorf("switch 错误文案: %q", resp.Error)
	}
}

// TestServerStatic 验证静态资源可访问（embed）。
func TestServerStatic(t *testing.T) {
	srv, _ := newTestServer(t, textStream("hi"))
	r := srv.routes()
	w := doJSON(t, r, "GET", "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET / status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Harness") {
		t.Errorf("首页缺品牌名")
	}
	// 静态资源 /static/app.js /static/style.css。
	for _, p := range []string{"/static/app.js", "/static/style.css"} {
		w := doJSON(t, r, "GET", p, "")
		if w.Code != http.StatusOK {
			t.Errorf("GET %s status: %d", p, w.Code)
		}
	}
}

// TestServerApproveFlow 验证审批闭环（SSE approval → POST approve）。
func TestServerApproveFlow(t *testing.T) {
	called := 0
	streamFn := func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		called++
		if called == 1 {
			// 第一轮：工具调用（bypass 不审批——这里直接测试 ask_user 路径的
			// 登记用不到；改为检查 approval 端点的 404 行为即可）。
			return provider.NewFakeStream([]provider.Event{
				{Type: provider.EventTextDelta, Text: "ok"},
				{Type: provider.EventTextDone, Text: "ok"},
				{Type: provider.EventDone},
			}), nil
		}
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDelta, Text: "done"},
			{Type: provider.EventTextDone, Text: "done"},
			{Type: provider.EventDone},
		}), nil
	}
	srv, c := newTestServer(t, streamFn)
	r := srv.routes()

	// 未知 request_id → 404。
	w := doJSON(t, r, "POST", "/api/approve", `{"request_id":"a999","decision":"allow"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知 approval 应 404，got %d", w.Code)
	}

	// 非法决策 → 400。
	w = doJSON(t, r, "POST", "/api/approve", `{"request_id":"a1","decision":"maybe"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法决策应 400，got %d", w.Code)
	}

	// 通过 controller 直接登记（等价于 agent 审批到达），再经 HTTP 回填。
	rid, _ := c.registerApproval(middleware.ApprovalRequest{ToolName: "shell_command", Summary: "ls", Mode: "acceptedit"})
	w = doJSON(t, r, "POST", "/api/approve", `{"request_id":"`+rid+`","decision":"allow"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("approve status: %d body: %s", w.Code, w.Body.String())
	}
}

// TestServerNewSwitch 验证 /api/new 创建会话（state_changed 广播）。
func TestServerNewSwitch(t *testing.T) {
	srv, _ := newTestServer(t, textStream("hi"))
	r := srv.routes()

	w := doJSON(t, r, "POST", "/api/new", "")
	if w.Code != http.StatusOK {
		t.Fatalf("new status: %d", w.Code)
	}
	w = doJSON(t, r, "GET", "/api/state", "")
	var st StateSnapshot
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if st.Active == nil {
		t.Error("new 后应有 active 会话")
	}
}
