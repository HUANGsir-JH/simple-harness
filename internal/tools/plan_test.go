package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// stubApprover 是工具测试用的 Ask 桩（Request 恒 Allow，Ask 走回调）。
type stubApprover struct {
	askFn func(middleware.AskRequest) middleware.AskResult
}

func (stubApprover) Request(context.Context, middleware.ApprovalRequest) (middleware.Decision, error) {
	return middleware.DecisionAllow, nil
}

func (s stubApprover) Ask(_ context.Context, req middleware.AskRequest) (middleware.AskResult, error) {
	if s.askFn != nil {
		return s.askFn(req), nil
	}
	return middleware.AskResult{}, nil
}

// newPlanRC 构造带 State + StatePath（临时目录）的 rc，plan 文件可落盘。
func newPlanRC(t *testing.T) *middleware.RuntimeContext {
	t.Helper()
	dir := t.TempDir()
	rc := middleware.NewRuntimeContext()
	rc.State = agentstate.New("s1", "m", dir)
	rc.StatePath = filepath.Join(dir, "agentstate.json")
	return rc
}

// callPlan 调 plan 工具，把工具错误归一到"失败结果"返回（断言用）。
func callPlan(t *testing.T, toolName string, rc *middleware.RuntimeContext, raw string) (bool, string) {
	t.Helper()
	var (
		res messages.ToolResult
		err error
	)
	switch toolName {
	case "plan_enter":
		res, err = PlanEnterTool{}.Handle(context.Background(), rc, "c", json.RawMessage(raw))
	case "write_plan":
		res, err = WritePlanTool{}.Handle(context.Background(), rc, "c", json.RawMessage(raw))
	case "plan_done":
		res, err = PlanDoneTool{}.Handle(context.Background(), rc, "c", json.RawMessage(raw))
	}
	if err != nil {
		var te *ToolError
		if errors.As(err, &te) {
			return false, te.Message
		}
		t.Fatalf("%s: 非工具错误: %v", toolName, err)
	}
	return res.Success, res.Content
}

// --- plan_enter ---

func TestPlanEnterApproved(t *testing.T) {
	rc := newPlanRC(t)
	rc.Approver = stubApprover{askFn: func(middleware.AskRequest) middleware.AskResult {
		return middleware.AskResult{Selection: []string{"进入规划"}}
	}}
	ok, content := callPlan(t, "plan_enter", rc, `{}`)
	if !ok {
		t.Fatalf("进入规划应成功")
	}
	if !rc.State.IsPlanMode() {
		t.Error("批准后 PlanMode 应为 true")
	}
	if !strings.HasPrefix(content, "【系统指令 · Plan 模式已激活】") {
		t.Errorf("tool_result 应携带完整 PlanInstructions，got: %.40s", content)
	}
	// 立即落盘：agentstate.json 存在且 PlanMode=true。
	st, err := agentstate.LoadFile(rc.StatePath)
	if err != nil || !st.PlanMode {
		t.Errorf("落盘失败或 PlanMode 未持久化: %v %+v", err, st)
	}
}

func TestPlanEnterDenied(t *testing.T) {
	rc := newPlanRC(t)
	rc.Approver = stubApprover{askFn: func(middleware.AskRequest) middleware.AskResult {
		return middleware.AskResult{Selection: []string{"不需要"}}
	}}
	ok, content := callPlan(t, "plan_enter", rc, `{}`)
	if !ok || rc.State.IsPlanMode() {
		t.Errorf("拒绝后应保持非 plan 模式，ok=%v", ok)
	}
	if !strings.Contains(content, "不进入 plan 模式") {
		t.Errorf("拒绝内容: %s", content)
	}
}

func TestPlanEnterCustomFeedback(t *testing.T) {
	rc := newPlanRC(t)
	rc.Approver = stubApprover{askFn: func(middleware.AskRequest) middleware.AskResult {
		return middleware.AskResult{Custom: "先别进，解释下"}
	}}
	ok, content := callPlan(t, "plan_enter", rc, `{}`)
	if !ok || rc.State.IsPlanMode() {
		t.Errorf("自定义反馈应保持非 plan 模式")
	}
	if !strings.Contains(content, "先别进，解释下") {
		t.Errorf("应回填自定义反馈，got: %s", content)
	}
}

func TestPlanEnterAlreadyInPlan(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	rc.Approver = stubApprover{}
	ok, msg := callPlan(t, "plan_enter", rc, `{}`)
	if ok || !strings.Contains(msg, "已在 plan 模式") {
		t.Errorf("已在 plan 模式应拒绝，ok=%v msg=%s", ok, msg)
	}
}

func TestPlanEnterNoApprover(t *testing.T) {
	rc := newPlanRC(t)
	ok, msg := callPlan(t, "plan_enter", rc, `{}`)
	if ok || !strings.Contains(msg, "无法向用户确认") {
		t.Errorf("无 approver 应拒绝，ok=%v msg=%s", ok, msg)
	}
}

// --- write_plan ---

func TestWritePlanWritesFile(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	content := "## 计划\n1. 改 a.go\n2. 加测试"
	ok, out := callPlan(t, "write_plan", rc, `{"content":"`+strings.ReplaceAll(content, "\n", `\n`)+`"}`)
	if !ok {
		t.Fatalf("write_plan 应成功")
	}
	// 文件写入 plans/plan.md。
	want := filepath.Join(filepath.Dir(rc.StatePath), "plans", "plan.md")
	if rc.State.PlanPath() != want {
		t.Errorf("Plan.Path = %q, want %s", rc.State.PlanPath(), want)
	}
	got, err := os.ReadFile(want)
	if err != nil || string(got) != content {
		t.Errorf("plan 文件内容: err=%v\n%s", err, got)
	}
	if !strings.Contains(out, want) || !strings.Contains(out, content) {
		t.Errorf("tool_result 应携带路径+全文，got: %.80s", out)
	}
	// 落盘持久化 Plan.Path。
	st, err := agentstate.LoadFile(rc.StatePath)
	if err != nil || st.Plan == nil || st.Plan.Path != want {
		t.Errorf("Plan.Path 未持久化: %v %+v", err, st.Plan)
	}
}

func TestWritePlanReusesPath(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	custom := filepath.Join(t.TempDir(), "my-plan.md")
	rc.State.SetPlanPath(custom)
	ok, _ := callPlan(t, "write_plan", rc, `{"content":"x"}`)
	if !ok {
		t.Fatal("write_plan 应成功")
	}
	if _, err := os.Stat(custom); err != nil {
		t.Errorf("应写入自定义路径 %s: %v", custom, err)
	}
}

func TestWritePlanNotInPlan(t *testing.T) {
	rc := newPlanRC(t)
	ok, msg := callPlan(t, "write_plan", rc, `{"content":"x"}`)
	if ok || !strings.Contains(msg, "仅在 plan 模式") {
		t.Errorf("非 plan 模式应拒绝，ok=%v msg=%s", ok, msg)
	}
}

func TestWritePlanEmptyContent(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	ok, msg := callPlan(t, "write_plan", rc, `{"content":""}`)
	if ok || !strings.Contains(msg, "content 不能为空") {
		t.Errorf("空 content 应拒绝，ok=%v msg=%s", ok, msg)
	}
}

// --- plan_done ---

func TestPlanDoneApproved(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	rc.Approver = stubApprover{askFn: func(middleware.AskRequest) middleware.AskResult {
		return middleware.AskResult{Selection: []string{"批准执行"}}
	}}
	ok, content := callPlan(t, "plan_done", rc, `{}`)
	if !ok {
		t.Fatal("批准执行应成功")
	}
	if rc.State.IsPlanMode() {
		t.Error("批准后 PlanMode 应为 false")
	}
	if !strings.Contains(content, "已批准") || !strings.Contains(content, "现在开始执行") {
		t.Errorf("批准内容应含路径与执行指令，got: %s", content)
	}
	st, _ := agentstate.LoadFile(rc.StatePath)
	if st.PlanMode {
		t.Error("PlanMode=false 应持久化")
	}
}

func TestPlanDoneContinue(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	rc.Approver = stubApprover{askFn: func(middleware.AskRequest) middleware.AskResult {
		return middleware.AskResult{Selection: []string{"继续规划"}}
	}}
	ok, content := callPlan(t, "plan_done", rc, `{}`)
	if !ok || !rc.State.IsPlanMode() {
		t.Errorf("继续规划应保持 plan 模式，ok=%v plan=%v", ok, rc.State.IsPlanMode())
	}
	if !strings.Contains(content, "继续规划") {
		t.Errorf("内容: %s", content)
	}
}

func TestPlanDoneCustomRejectFeedback(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	rc.Approver = stubApprover{askFn: func(middleware.AskRequest) middleware.AskResult {
		return middleware.AskResult{Custom: "方案 A 更好，重写计划"}
	}}
	ok, content := callPlan(t, "plan_done", rc, `{}`)
	if !ok || !rc.State.IsPlanMode() {
		t.Errorf("Other 反馈应保持 plan 模式（拒绝语义），ok=%v plan=%v", ok, rc.State.IsPlanMode())
	}
	if !strings.Contains(content, "未批准") || !strings.Contains(content, "方案 A 更好") {
		t.Errorf("应回填用户反馈，got: %s", content)
	}
}

func TestPlanDoneNotInPlan(t *testing.T) {
	rc := newPlanRC(t)
	ok, msg := callPlan(t, "plan_done", rc, `{}`)
	if ok || !strings.Contains(msg, "仅在 plan 模式") {
		t.Errorf("非 plan 模式应拒绝，ok=%v msg=%s", ok, msg)
	}
}

func TestPlanDoneNoApprover(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	ok, msg := callPlan(t, "plan_done", rc, `{}`)
	if ok || !strings.Contains(msg, "无法向用户确认") {
		t.Errorf("无 approver 应拒绝，ok=%v msg=%s", ok, msg)
	}
}

func TestPlanDoneCancelled(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	rc.Approver = stubApprover{} // 空 AskResult（用户取消）
	ok, content := callPlan(t, "plan_done", rc, `{}`)
	if !ok || !rc.State.IsPlanMode() {
		t.Errorf("取消应保持 plan 模式，ok=%v", ok)
	}
	if !strings.Contains(content, "取消了确认") {
		t.Errorf("内容: %s", content)
	}
}

// TestPlanTodoConcurrentState 验证锁下沉（ADR-036 修订，缺陷 04）：同批并发
// update_todo（写 Todos + 整体 Marshal 落盘）与 write_plan（写 Plan + 整体
// Marshal）与 AddApproved（写 Permission）与 RenderTodos（读 Todos）共享同一
// *AgentState，-race 下必须无竞态。修复前 planMu/todoMu 分离会触发
// Marshal vs ReplaceTodos 的 data race。
func TestPlanTodoConcurrentState(t *testing.T) {
	rc := newPlanRC(t)
	rc.State.SetPlanMode(true)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = UpdateTodoTool{}.Handle(context.Background(), rc, "c", json.RawMessage(`{"todos":[{"position":1,"description":"x","status":"pending"}]}`))
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = WritePlanTool{}.Handle(context.Background(), rc, "c", json.RawMessage(`{"content":"plan"}`))
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc.State.AddApproved([]string{"git push"})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rc.State.RenderTodos()
		}()
	}
	wg.Wait()
}
