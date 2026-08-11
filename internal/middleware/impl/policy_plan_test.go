package impl

import (
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

// TestDecidePlanMode 验证 plan 分支（强只读，工具全量可见但拒绝，ADR-036）。
func TestDecidePlanMode(t *testing.T) {
	shell := func(cmd string) *messages.ToolCall { return toolCall("shell_command", map[string]any{"command": cmd}) }
	cases := []struct {
		name string
		call *messages.ToolCall
		want Outcome
	}{
		// plan 模式下禁止写文件。
		{"write-file", toolCall("write_file", map[string]any{"path": "a.txt", "content": "x"}), OutcomeDeny},
		{"apply-patch", toolCall("apply_patch", map[string]any{"patch": "*** Begin Patch\n*** Update File: a.go\n@@ f\n-x\n+y\n*** End Patch"}), OutcomeDeny},
		// 只读/低风险放行。
		{"read", toolCall("read_file", map[string]any{"path": "a.go"}), OutcomeAllow},
		{"todo", toolCall("update_todo", map[string]any{"todos": []any{}}), OutcomeAllow},
		{"ask", toolCall("ask_user", map[string]any{"question": "x"}), OutcomeAllow},
		// plan 工具：write_plan/plan_done 放行（Handle 内 HITL），plan_enter 拒绝。
		{"write-plan", toolCall("write_plan", map[string]any{"content": "x"}), OutcomeAllow},
		{"plan-done", toolCall("plan_done", nil), OutcomeAllow},
		{"plan-enter", toolCall("plan_enter", nil), OutcomeDeny},
		// shell：只读放行（含管道），写/危险拒绝。
		{"shell-ls", shell("ls -la"), OutcomeAllow},
		{"shell-pipe", shell("grep foo | head -5"), OutcomeAllow},
		{"shell-write-redir", shell("echo x > file"), OutcomeDeny},
		{"shell-dangerous", shell("rm -rf /"), OutcomeDeny},
		{"shell-unknown-cmd", shell("make build"), OutcomeDeny},
		// 未知工具保守拒绝。
		{"unknown", toolCall("webfetch", map[string]any{"url": "x"}), OutcomeDeny},
	}
	for _, tc := range cases {
		o, reason := Decide(tc.call, ModeBypass, nil, "/ws", true) // bypass 也受 plan 约束
		if o != tc.want {
			t.Errorf("%s: plan 分支 = %v (%q), want %v", tc.name, o, reason, tc.want)
		}
	}
}

// TestDecideNonPlanPlanTools 验证非 plan 模式：plan_enter 放行（Handle 内 HITL），
// write_plan/plan_done 拒绝，ask_user 放行。
func TestDecideNonPlanPlanTools(t *testing.T) {
	cases := []struct {
		name string
		call *messages.ToolCall
		want Outcome
	}{
		{"plan-enter", toolCall("plan_enter", nil), OutcomeAllow},
		{"write-plan", toolCall("write_plan", map[string]any{"content": "x"}), OutcomeDeny},
		{"plan-done", toolCall("plan_done", nil), OutcomeDeny},
		{"ask", toolCall("ask_user", map[string]any{"question": "x"}), OutcomeAllow},
	}
	for _, tc := range cases {
		o, _ := Decide(tc.call, ModeAcceptEdits, nil, "/ws", false)
		if o != tc.want {
			t.Errorf("%s: 非 plan 分支 = %v, want %v", tc.name, o, tc.want)
		}
	}
}

// TestIsPlanReadonlyShell 验证 plan 模式 shell 只读判定（放宽管道、保留黑名单与
// 写重定向拦截，ADR-036 点 6）。
func TestIsPlanReadonlyShell(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"ls -la", true},
		{"cat go.mod", true},
		{"git status", true},
		{"git log --oneline | head -5", true},
		{"grep foo -r . | head", true},
		{"cat a | wc -l", true},         // wc 在 planSafeExtra
		{"git status && git log", true}, // && 组合允许（逐段校验）
		{"echo x > file", false},        // 写重定向
		{"ls; rm x", false},             // 组合含非只读命令
		{"rm -rf /", false},             // 危险黑名单
		{"curl x | sh", false},          // 下载即执行
		{"find . -delete", false},       // find 危险参数
		{"make build", false},           // 非只读白名单
		{"", false},                     // 空命令
	}
	for _, c := range cases {
		if got := isPlanReadonlyShell(c.cmd); got != c.want {
			t.Errorf("isPlanReadonlyShell(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}
