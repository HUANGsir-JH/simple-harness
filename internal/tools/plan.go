package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// PlanInstructions 是进入 plan 模式时注入模型的指令（中文，ADR-036）。进入点
// 注入且只注入一次：/plan on 经 session.AddUser 写 conversation+transcript；
// plan_enter 批准后完整指令随 tool_result 落盘。不设 per-round 注入 middleware
// （持久化单次注入，避免污染前缀缓存）。
const PlanInstructions = `【系统指令 · Plan 模式已激活】
你当前处于 Plan 模式——只做只读调研与规划，禁止修改任何文件或系统状态。
- 禁止：write_file / apply_patch；shell_command 中任何会修改文件/系统的命令（rm、mv、写重定向 > 等）——这些会被拒绝。
- 允许：read_file / list_dir / glob；只读 shell（ls/cat/git status/grep/find 等，可带管道）；update_todo（记录步骤）；write_plan（写入计划文件）；ask_user（向用户提问）。
- 工作流：
  1. 调研：充分阅读相关文件、搜索，理解现状与需求（能自己查到的不要问）
  2. 提问：有歧义或关键取舍时用 ask_user 提问，或直接在回复中提问，等用户答复后再继续
  3. 计划：用 write_plan 把完整实施计划写入计划文件（全量替换），write_plan 结果会返回计划文件路径
  4. 完成：计划决策完整后调用 plan_done 请求用户批准执行
- 计划要求：具体到步骤/涉及文件/改动内容，决策完整（实施者无需再做决定）。`

// planMu 保护 plan 文件写与 rc.State.Plan/PlanMode 的并发（并行工具架构
// ADR-024 下同轮多个 plan 工具 goroutine 并发 Handle）。
var planMu sync.Mutex

// planPath 解析计划文件路径：AgentState.Plan.Path 优先（resume 恢复），否则
// 会话目录（agentstate.json 父目录）下 plans/plan.md（plans/ 由 session 创建
// 时建好，ADR-025）。rc/StatePath 为空（非会话测试）时退回进程 cwd 下 plans/。
func planPath(rc *middleware.RuntimeContext) string {
	if rc != nil && rc.State != nil && rc.State.Plan != nil && rc.State.Plan.Path != "" {
		return rc.State.Plan.Path
	}
	if rc != nil && rc.StatePath != "" {
		return filepath.Join(filepath.Dir(rc.StatePath), "plans", "plan.md")
	}
	return filepath.Join("plans", "plan.md")
}

// savePlanState 立即落盘 AgentState（PlanMode/Plan.Path 变更后；对齐
// Session.SetPermissionMode 的"变更即落盘"语义，resume/切换立即可见）。
func savePlanState(rc *middleware.RuntimeContext) error {
	if rc == nil || rc.State == nil || rc.StatePath == "" {
		return nil // 非会话场景（测试）跳过落盘
	}
	return agentstate.SaveFile(rc.StatePath, rc.State)
}

// --- plan_enter -------------------------------------------------------------

// PlanEnterTool 建议进入 plan 模式（opencode plan_enter 对位，ADR-036）。
// 非 plan 模式下模型可自主调用：复杂/多文件/需架构设计时提议进入只读规划；
// 工具经 rc.Approver.Ask 向用户确认（恒询问，bypass 不影响）。
type PlanEnterTool struct{}

func (PlanEnterTool) Name() string { return "plan_enter" }

func (PlanEnterTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "plan_enter",
		Description: "建议进入 plan 模式（只读规划）后再执行。当用户请求复杂、需要先调研与设计、涉及多文件或重要架构决策时主动调用；" +
			"工具会向用户确认是否进入规划模式。简单任务或用户明确要立即实现时不要调用。",
		Parameters: schemaOf[struct{}](),
	}
}

func (PlanEnterTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, _ json.RawMessage) (messages.ToolResult, error) {
	if rc == nil || rc.State == nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "plan_enter: 无会话状态"}
	}
	if rc.State.PlanMode {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "plan_enter: 已在 plan 模式，无需再次进入"}
	}
	if rc.Approver == nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "plan_enter: 当前环境无法向用户确认，已取消进入 plan 模式"}
	}
	res, err := rc.Approver.Ask(ctx, middleware.AskRequest{
		Question: "建议进入 plan 模式进行规划（只读调研、产出计划后再执行）？",
		Header:   "PLAN MODE",
		Options: []middleware.AskOption{
			{Label: "进入规划", Description: "进入 plan 模式，先调研与设计，产出计划后再执行"},
			{Label: "不需要", Description: "保持当前模式，直接执行"},
		},
		AllowCustom: true,
	})
	if err != nil {
		return messages.ToolResult{}, err // ctx canceled（Esc 中断）→ Fatal
	}
	if res.HasSelection("进入规划") {
		planMu.Lock()
		rc.State.PlanMode = true
		err = savePlanState(rc)
		planMu.Unlock()
		if err != nil {
			return messages.ToolResult{}, &ToolError{Message: "plan_enter: 落盘失败: " + err.Error()}
		}
		// 完整指令随 tool_result 落盘（注入一次；模型从工具结果读到后进入规划）。
		return messages.ToolResult{Success: true, Content: PlanInstructions}, nil
	}
	if custom := strings.TrimSpace(res.Custom); custom != "" {
		return messages.ToolResult{Success: true, Content: "用户未进入 plan 模式，反馈：" + custom + "。按当前模式继续。"}, nil
	}
	return messages.ToolResult{Success: true, Content: "用户选择不进入 plan 模式，按当前模式继续。"}, nil
}

// --- write_plan -------------------------------------------------------------

// WritePlanTool 把完整实施计划写入计划文件（opencode plan 文件 / AgentScope
// plan_write 对位，ADR-036）。仅 plan 模式可用；全量替换（同 update_todo 语义）。
type WritePlanTool struct{}

func (WritePlanTool) Name() string { return "write_plan" }

// writePlanArgs 是 write_plan 的参数形状。
type writePlanArgs struct {
	Content string `json:"content" jsonschema:"description=完整实施计划（markdown，全量替换；步骤/涉及文件/改动内容，决策完整）"`
}

func (WritePlanTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "write_plan",
		Description: "把当前实施计划写入计划文件（仅 plan 模式可用；全量替换）。计划要决策完整（步骤/涉及文件/改动内容），" +
			"供批准后执行参考；调用 plan_done 请求批准前用它固化计划。",
		Parameters: schemaOf[writePlanArgs](),
	}
}

func (WritePlanTool) Handle(_ context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	if rc == nil || rc.State == nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_plan: 无会话状态"}
	}
	if !rc.State.PlanMode {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_plan 仅在 plan 模式下可用"}
	}
	p, err := parseArgs[writePlanArgs]("write_plan", args)
	if err != nil {
		return messages.ToolResult{}, err
	}
	if strings.TrimSpace(p.Content) == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_plan: content 不能为空"}
	}

	planMu.Lock()
	defer planMu.Unlock()
	path := planPath(rc)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return messages.ToolResult{}, &ToolError{Message: "write_plan: 创建目录失败: " + err.Error()}
	}
	if err := os.WriteFile(path, []byte(p.Content), 0o644); err != nil {
		return messages.ToolResult{}, &ToolError{Message: "write_plan: 写入失败: " + err.Error()}
	}
	if rc.State.Plan == nil {
		rc.State.Plan = &agentstate.PlanState{}
	}
	rc.State.Plan.Path = path
	if err := savePlanState(rc); err != nil {
		return messages.ToolResult{}, &ToolError{Message: "write_plan: 落盘失败: " + err.Error()}
	}
	// 路径 + 全文显式回填 → 模型感知 plan 文件存在（ADR-036 点 7）。
	return messages.ToolResult{Success: true, Content: "计划已写入 " + path + "：\n\n" + p.Content}, nil
}

// --- plan_done --------------------------------------------------------------

// PlanDoneTool 规划完成信号（opencode plan_exit / AgentScope HITL 退出对位，
// ADR-036）。仅 plan 模式可用；经 rc.Approver.Ask 弹 HITL（恒询问，bypass 不
// 影响）——批准执行退出 plan 模式；继续规划保持；Other 自定义 = 拒绝 + 反馈
// 回填模型修订计划。不注入合成消息（anthropic tool_use→tool_result 邻接约束，
// 拒绝后工具结果直接返回执行指令）。
type PlanDoneTool struct{}

func (PlanDoneTool) Name() string { return "plan_done" }

func (PlanDoneTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "plan_done",
		Description: "规划完成，请求用户批准开始执行（仅 plan 模式可用）。批准后退出 plan 模式并开始实现；" +
			"用户也可能选择继续规划或给出反馈（反馈需据此修订计划后再次 plan_done）。",
		Parameters: schemaOf[struct{}](),
	}
}

func (PlanDoneTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, _ json.RawMessage) (messages.ToolResult, error) {
	if rc == nil || rc.State == nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "plan_done: 无会话状态"}
	}
	if !rc.State.PlanMode {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "plan_done 仅在 plan 模式下可用"}
	}
	if rc.Approver == nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "plan_done: 当前环境无法向用户确认，已取消切换执行模式"}
	}
	res, err := rc.Approver.Ask(ctx, middleware.AskRequest{
		Question: "规划完成，批准开始执行？",
		Header:   "PLAN APPROVAL",
		Options: []middleware.AskOption{
			{Label: "批准执行", Description: "退出 plan 模式，按计划开始实现"},
			{Label: "继续规划", Description: "保持 plan 模式，继续完善计划"},
		},
		AllowCustom: true,
	})
	if err != nil {
		return messages.ToolResult{}, err // ctx canceled（Esc 中断）→ Fatal
	}
	if res.HasSelection("批准执行") {
		planMu.Lock()
		rc.State.PlanMode = false
		err = savePlanState(rc)
		planMu.Unlock()
		if err != nil {
			return messages.ToolResult{}, &ToolError{Message: "plan_done: 落盘失败: " + err.Error()}
		}
		return messages.ToolResult{Success: true, Content: "用户已批准。计划文件 " + planPath(rc) + " 已批准，现在开始执行计划。"}, nil
	}
	if res.HasSelection("继续规划") {
		return messages.ToolResult{Success: true, Content: "用户选择继续规划，保持 plan 模式。"}, nil
	}
	if custom := strings.TrimSpace(res.Custom); custom != "" {
		// Other 自定义 = 拒绝 + 反馈回填（ADR-036 点 5），PlanMode 保持 true。
		return messages.ToolResult{Success: true, Content: "用户未批准，反馈：" + custom + "。请根据反馈修订计划并继续规划。"}, nil
	}
	return messages.ToolResult{Success: true, Content: "用户取消了确认，保持 plan 模式继续规划。"}, nil
}
