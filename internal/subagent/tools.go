package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// 子 agent 控制工具（阶段 5，ADR-045）。实现放在本包而非 tools 包——它们
// 是子 agent 域的东西，且避免 tools → agent 依赖环（装配在 agent.Build）。

// --- spawn_agent ---

// SpawnAgentTool 创建子 agent（纯异步：立即返回 id，完成自动注入父对话）。
type SpawnAgentTool struct{ Manager *Manager }

func (SpawnAgentTool) Name() string { return "spawn_agent" }

type spawnArgs struct {
	Message        string `json:"message" jsonschema:"description=子 agent 的任务描述（作为其首条用户消息，fork 过滤后子会话起点）"`
	Name           string `json:"name,omitempty" jsonschema:"description=子 agent 名称（可选；默认自动生成）"`
	AgentType      string `json:"agent_type,omitempty" jsonschema:"description=子 agent 类型：general-purpose（默认，全套工具）或 explore（只读）"`
	Model          string `json:"model,omitempty" jsonschema:"description=模型覆盖（可选；默认继承父）"`
	ThinkingEffort string `json:"thinking_effort,omitempty" jsonschema:"description=推理档位覆盖（可选；默认继承父）"`
}

func (SpawnAgentTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "spawn_agent",
		Description: "创建子 agent 异步执行任务（立即返回 agent id，完成后结果自动注入对话通知，无需轮询；期间可继续执行其他任务）。" +
			"回合结束前若有子 agent 仍在运行，harness 会等待其完成并注入结果后再收尾（单轮 run 模式）。" +
			"子 agent 有独立会话与工具集（general-purpose 全套 / explore 只读）。" +
			"可并行创建多个子 agent。之后可用 list_agents 查看状态、send_message 补充指示、interrupt_agent 中断、resume_agent 继续。",
		Parameters: schemaOf[spawnArgs](),
	}
}

func (t SpawnAgentTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	p, err := parseArgs[spawnArgs]("spawn_agent", args)
	if err != nil {
		return messages.ToolResult{}, err
	}
	if p.AgentType != "" && p.AgentType != KindGeneralPurpose && p.AgentType != KindExplore {
		return messages.ToolResult{}, &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"spawn_agent: 未知 agent_type %q（支持 general-purpose / explore）", p.AgentType)}
	}
	id, err := t.Manager.Spawn(rc, SpawnRequest{
		Name: p.Name, Message: p.Message, Type: p.AgentType,
		Model: p.Model, ThinkingEffort: p.ThinkingEffort,
	})
	if err != nil {
		return messages.ToolResult{}, err
	}
	e, _ := t.Manager.get(id)
	return messages.ToolResult{Success: true, Content: fmt.Sprintf(
		"已创建子 agent %s（%s，深度 %d，状态 %s）。任务已异步开始，完成后结果将自动注入对话（系统通知）。"+
			"可用 list_agents 查看状态，send_message 补充指示，interrupt_agent 中断。",
		id, e.Name, e.Depth, t.Manager.statusOf(id))}, nil
}

// --- send_message ---

// SendMessageTool 主→子单向消息（仅运行中的子；子下轮采样前注入）。
type SendMessageTool struct{ Manager *Manager }

func (SendMessageTool) Name() string { return "send_message" }

type sendMessageArgs struct {
	AgentID string `json:"agent_id" jsonschema:"description=目标子 agent id（spawn_agent 返回）"`
	Message string `json:"message" jsonschema:"description=要发送的消息内容（子 agent 下一轮采样前注入其对话）"`
}

func (SendMessageTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "send_message",
		Description: "向运行中的子 agent 发送一条消息（子 agent 下一轮采样前注入其对话，可补充指示或改方向）。" +
			"子 agent 已结束时请用 resume_agent 继续任务。",
		Parameters: schemaOf[sendMessageArgs](),
	}
}

func (t SendMessageTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	p, err := parseArgs[sendMessageArgs]("send_message", args)
	if err != nil {
		return messages.ToolResult{}, err
	}
	if p.AgentID == "" || p.Message == "" {
		return messages.ToolResult{}, &tools.ToolError{RespondToModel: true, Message: "send_message: agent_id 与 message 必填"}
	}
	if err := t.Manager.Send(rc, p.AgentID, p.Message); err != nil {
		return messages.ToolResult{}, err
	}
	return messages.ToolResult{Success: true, Content: fmt.Sprintf("已向子 agent %s 发送消息", p.AgentID)}, nil
}

// --- interrupt_agent ---

// InterruptAgentTool 中断子 agent（任意后代；不能中断自己/父）。
type InterruptAgentTool struct{ Manager *Manager }

func (InterruptAgentTool) Name() string { return "interrupt_agent" }

type interruptArgs struct {
	AgentID string `json:"agent_id" jsonschema:"description=要中断的子 agent id"`
}

func (InterruptAgentTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "interrupt_agent",
		Description: "中断一个运行中的子 agent（停止其当前回合，Esc 同款语义；中断通知会注入对话并附中断前已产出的结果）。" +
			"不能中断自己或父 agent；已结束的子 agent 用 resume_agent 继续。",
		Parameters: schemaOf[interruptArgs](),
	}
}

func (t InterruptAgentTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	p, err := parseArgs[interruptArgs]("interrupt_agent", args)
	if err != nil {
		return messages.ToolResult{}, err
	}
	if p.AgentID == "" {
		return messages.ToolResult{}, &tools.ToolError{RespondToModel: true, Message: "interrupt_agent: agent_id 必填"}
	}
	if err := t.Manager.Interrupt(rc, p.AgentID); err != nil {
		return messages.ToolResult{}, err
	}
	return messages.ToolResult{Success: true, Content: fmt.Sprintf("已请求中断子 agent %s", p.AgentID)}, nil
}

// --- resume_agent ---

// ResumeAgentTool 磁盘加载已落盘的子会话继续新任务（仅直属子）。
type ResumeAgentTool struct{ Manager *Manager }

func (ResumeAgentTool) Name() string { return "resume_agent" }

type resumeArgs struct {
	AgentID string `json:"agent_id" jsonschema:"description=要恢复的子 agent id（list_agents 查看）"`
	Message string `json:"message,omitempty" jsonschema:"description=新的任务指令（可选；追加为子会话新 user 消息）"`
}

func (ResumeAgentTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "resume_agent",
		Description: "恢复一个已结束（完成/失败/中断）的子 agent 继续新任务（仅限直属子；加载其已落盘会话，历史与工具记录保留）。" +
			"完成后结果再次注入对话。",
		Parameters: schemaOf[resumeArgs](),
	}
}

func (t ResumeAgentTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	p, err := parseArgs[resumeArgs]("resume_agent", args)
	if err != nil {
		return messages.ToolResult{}, err
	}
	if p.AgentID == "" {
		return messages.ToolResult{}, &tools.ToolError{RespondToModel: true, Message: "resume_agent: agent_id 必填"}
	}
	if err := t.Manager.Resume(rc, p.AgentID, p.Message); err != nil {
		return messages.ToolResult{}, err
	}
	return messages.ToolResult{Success: true, Content: fmt.Sprintf("已恢复子 agent %s 继续执行", p.AgentID)}, nil
}

// --- list_agents ---

// ListAgentsTool 列出当前会话的子 agent（运行态 + 磁盘历史）。
type ListAgentsTool struct{ Manager *Manager }

func (ListAgentsTool) Name() string { return "list_agents" }

func (ListAgentsTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "list_agents",
		Description: "列出当前会话创建的子 agent（id / 名称 / 类型 / 状态 / 深度）。" +
			"用于查看进度与选择 resume_agent / interrupt_agent 的目标。",
		Parameters: schemaOf[struct{}](),
	}
}

func (t ListAgentsTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	views := t.Manager.List(rc)
	if len(views) == 0 {
		return messages.ToolResult{Success: true, Content: "（当前会话没有子 agent）"}, nil
	}
	var sb strings.Builder
	for _, v := range views {
		fmt.Fprintf(&sb, "- %s %s（%s，深度 %d，%s%s）\n", v.ID, v.Name, v.Type, v.Depth, v.Status,
			map[bool]string{true: "，运行中", false: ""}[v.Running])
	}
	return messages.ToolResult{Success: true, Content: strings.TrimRight(sb.String(), "\n")}, nil
}
