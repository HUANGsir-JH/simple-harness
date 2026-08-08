// Package agentstate 定义 AgentState（会话运行时状态的轻量快照）。
//
// 独立成包的原因（ADR-025）：middleware（RuntimeContext.State 字段）与
// session（StateMiddleware 落盘）都依赖该类型，独立后两者只依赖本包，
// 避免 middleware↔session 循环引用。
//
// 会话的双轨持久化：
//   - 消息流 → transcript（historys/*.jsonl，块级事件）
//   - 非消息状态（todo / 权限 / plan 指针 / 摘要）→ AgentState，每次
//     agent.Run 进出各保存一次（参照 AgentScope 无状态引擎的 call load/save）。
package agentstate

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// AgentState 是会话的运行时状态快照。
type AgentState struct {
	SessionID string     `json:"session_id"`
	Model     string     `json:"model,omitempty"`
	CWD       string     `json:"cwd,omitempty"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
	Todos     []TodoItem `json:"todos,omitempty"`             // todo 工具挂这
	Permission *PermissionState `json:"permission,omitempty"` // 阶段三填，预留
	Plan      *PlanState        `json:"plan,omitempty"`       // plan 文件指针，预留
	Summary   string            `json:"summary,omitempty"`    // 压缩摘要，预留
}

// TodoItem 是单个任务项（AgentScope tasksContext 对位）。
type TodoItem struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Status      string `json:"status"` // pending | in_progress | completed
}

// todo 状态枚举。
const (
	TodoPending    = "pending"
	TodoInProgress = "in_progress"
	TodoCompleted  = "completed"
)

// PermissionState 预留：阶段三三档权限 + 规则（存快照供 resume）。
type PermissionState struct {
	Mode string `json:"mode,omitempty"` // readonly | acceptedit | bypass
}

// PlanState 预留：plan 文件路径指针（内容本体在 plans/，快照只存指针）。
type PlanState struct {
	Path string `json:"path,omitempty"`
}

// New 创建空 state（带时间戳）。
func New(sessionID, model, cwd string) *AgentState {
	now := time.Now().UTC().Format(time.RFC3339)
	return &AgentState{
		SessionID: sessionID,
		Model:     model,
		CWD:       cwd,
		CreatedAt: now,
		UpdatedAt: now,
		Todos:     []TodoItem{},
	}
}

// AddTodo 追加一个 pending 任务并返回。
func (a *AgentState) AddTodo(desc string) TodoItem {
	t := TodoItem{
		ID:          fmt.Sprintf("t_%d", time.Now().UnixNano()),
		Description: desc,
		Status:      TodoPending,
	}
	a.Todos = append(a.Todos, t)
	return t
}

// UpdateTodoStatus 更新任务状态；找不到返回 false。
func (a *AgentState) UpdateTodoStatus(id, status string) bool {
	for i := range a.Todos {
		if a.Todos[i].ID == id {
			a.Todos[i].Status = status
			return true
		}
	}
	return false
}

// SaveFile 将 state 整体 JSON 写入 path（原子写：临时文件 + rename，
// 避免半截文件）。同时刷新 UpdatedAt。
func SaveFile(path string, a *AgentState) error {
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("agentstate: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("agentstate: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("agentstate: rename %s: %w", path, err)
	}
	return nil
}

// LoadFile 从 path 读取 state；文件不存在返回空 state（不报错，供新会话）。
func LoadFile(path string) (*AgentState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AgentState{Todos: []TodoItem{}}, nil
		}
		return nil, fmt.Errorf("agentstate: read %s: %w", path, err)
	}
	var a AgentState
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("agentstate: unmarshal %s: %w", path, err)
	}
	if a.Todos == nil {
		a.Todos = []TodoItem{}
	}
	return &a, nil
}
