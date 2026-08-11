package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// TestRunPlanModeClosedLoop 验证 plan 模式闭环（ADR-036）：
// plan 模式（模拟 /plan on）→ write_file 被 plan 分支拒绝 → write_plan 写计划
// 文件 → plan_done 弹 HITL 批准 → 退出 plan 模式 → 写文件放行 → 回合完成。
func TestRunPlanModeClosedLoop(t *testing.T) {
	calls := 0
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		calls++
		switch calls {
		case 1:
			return toolCallStream("write_file", `{"path":"a.txt","content":"x"}`), nil // plan 模式拒绝
		case 2:
			return toolCallStream("write_plan", "{\"content\":\"## 计划\\n1. 改 a.txt\"}"), nil
		case 3:
			return toolCallStream("plan_done", `{}`), nil // 批准执行
		case 4:
			return toolCallStream("write_file", `{"path":"a.txt","content":"y"}`), nil // 退出后放行
		}
		return textStream("完成"), nil
	}}
	reg := tools.NewRegistry()
	for _, tl := range tools.Builtins() {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	a := New(fc, "m")
	a.SetTools(reg)
	a.SetMiddleware(middleware.NewChain(impl.ApprovalMiddleware{DefaultMode: impl.ModeAcceptEdits}))

	dir := t.TempDir()
	conv := newConversation()
	rc := rcFor(conv)
	rc.State = agentstate.New("s1", "m", dir)
	rc.StatePath = filepath.Join(dir, "agentstate.json")
	rc.State.PlanMode = true // 模拟 /plan on（指令注入由 TUI/进入点负责，这里只测策略+工具）
	rc.Approver = allowApprover{}
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rc, rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 工具结果按顺序收集。
	type tr struct {
		success bool
		content string
	}
	var results []tr
	for _, m := range conv.Messages {
		if m.Role == messages.RoleTool {
			for _, r := range m.ToolResults {
				results = append(results, tr{r.Success, r.Content})
			}
		}
	}
	if len(results) != 4 {
		t.Fatalf("工具结果数: %d, want 4", len(results))
	}
	if results[0].success || !strings.Contains(results[0].content, "禁止写文件") {
		t.Errorf("plan 模式 write_file 应拒绝: %+v", results[0])
	}
	if !results[1].success || !strings.Contains(results[1].content, "计划已写入") {
		t.Errorf("write_plan 应成功: %+v", results[1])
	}
	if !results[2].success || !strings.Contains(results[2].content, "已批准") {
		t.Errorf("plan_done 应批准执行: %+v", results[2])
	}
	if !results[3].success {
		t.Errorf("退出 plan 模式后 write_file 应放行: %+v", results[3])
	}

	// plan 文件已写入（write_plan 落盘）。
	planFile := filepath.Join(dir, "plans", "plan.md")
	if rc.State.Plan == nil || rc.State.Plan.Path != planFile {
		t.Errorf("Plan.Path = %+v, want %s", rc.State.Plan, planFile)
	}
	if b, err := os.ReadFile(planFile); err != nil || !strings.Contains(string(b), "改 a.txt") {
		t.Errorf("plan 文件: err=%v\n%s", err, b)
	}

	// 批准后退出 plan 模式（立即落盘）。
	if rc.State.PlanMode {
		t.Error("批准执行后 PlanMode 应为 false")
	}
	if st, err := agentstate.LoadFile(rc.StatePath); err != nil || st.PlanMode {
		t.Errorf("PlanMode=false 应持久化: %v %+v", err, st)
	}
	if !rec.has(events.EventTurnDone) {
		t.Error("应到 turn_done")
	}
}
