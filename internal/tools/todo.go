package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// todoMu 保护 rc.State.Todos 的并发读写。并行工具架构（ADR-024）下同一轮
// 多个工具 goroutine 并发 Handle；update_todo 是首个写 AgentState 的工具，
// 加锁消除同轮并发 update_todo（或与未来读 state 的工具）的 data race。
var todoMu sync.Mutex

// UpdateTodoTool 全量替换当前会话的待办清单（AgentScope tasksContext 对位，
// 参考 codex update_plan / opencode todowrite，ADR-027）。
//
// 语义要点：
//   - 全量替换：模型每次传**完整**列表（含已完成的），handler 整体重建，
//     不传则该项消失（天然支持"删除"）
//   - position 由模型维护（有序列表第几行）；存储按 position 稳定排序，
//     不做任何归一化（one in_progress 靠 prompt 约束）
//   - 列表挂 rc.State.Todos，随 SessionMiddleware 落盘 agentstate.json（resume 恢复）
type UpdateTodoTool struct{}

func (UpdateTodoTool) Name() string { return "update_todo" }

func (UpdateTodoTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "update_todo",
		Description: "创建并维护当前会话的结构化待办清单（todo list），用于追踪多步骤任务的进度。\n" +
			"适合在任务需要 3+ 个独立步骤、需要规划、或用户给出了多项任务时主动使用；" +
			"纯查询/单一小改动不需要。\n" +
			"状态：pending（未开始）/ in_progress（进行中）/ completed（已完成）。\n" +
			"规则：完成一步立刻标记 completed，不要攒着批量标；" +
			"同时只保持一个 in_progress；被阻塞时保持 in_progress 并追加一条描述阻塞项的 todo。\n" +
			"每次调用传**完整**列表（全量替换，非增量），用 position 维护顺序。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"todos": {
					"type": "array",
					"description": "完整的待办列表（全量替换）",
					"items": {
						"type": "object",
						"properties": {
							"position": {"type": "integer", "description": "顺序编号（1 基，越小越靠前）"},
							"description": {"type": "string", "description": "任务描述"},
							"status": {"type": "string", "enum": ["pending", "in_progress", "completed"], "description": "任务状态"}
						},
						"required": ["position", "description", "status"]
					}
				}
			},
			"required": ["todos"]
		}`),
	}
}

// todoArgs 是 update_todo 的参数形状（模型可见 schema 的对偶结构）。
type todoArgs struct {
	Todos []struct {
		Position    int    `json:"position"`
		Description string `json:"description"`
		Status      string `json:"status"`
	} `json:"todos"`
}

func (UpdateTodoTool) Handle(_ context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	if rc == nil || rc.State == nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "update_todo: 无会话状态（todo 需挂 AgentState）"}
	}
	var p todoArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "update_todo: 参数解析失败: " + err.Error()}
	}
	items := make([]agentstate.TodoItem, 0, len(p.Todos))
	for _, t := range p.Todos {
		switch t.Status {
		case agentstate.TodoPending, agentstate.TodoInProgress, agentstate.TodoCompleted:
		default:
			return messages.ToolResult{}, &ToolError{RespondToModel: true,
				Message: "update_todo: 非法状态 " + t.Status + "（应为 pending|in_progress|completed）"}
		}
		if strings.TrimSpace(t.Description) == "" {
			return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "update_todo: description 不能为空"}
		}
		items = append(items, agentstate.TodoItem{Position: t.Position, Description: t.Description, Status: t.Status})
	}

	// 写 Todos、读渲染、写提醒基准全部放锁内：并行工具（ADR-024）下同轮多个
	// update_todo 并发，Todos 与 rc.attrs 的 todo 计数键都须串行。
	todoMu.Lock()
	rc.State.ReplaceTodos(items)
	// 记录活动基准：模型已更新 todo，提醒 idle 计数从此清零重计。
	if v, ok := rc.Get("todo_sample_count").(int); ok {
		rc.Set("todo_last_activity", v)
	}
	content := rc.State.RenderTodos()
	todoMu.Unlock()

	return messages.ToolResult{Success: true, Content: content}, nil
}
