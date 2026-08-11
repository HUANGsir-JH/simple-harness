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
	"sort"
	"strings"
	"time"
)

// AgentState 是会话的运行时状态快照。
type AgentState struct {
	SessionID       string           `json:"session_id"`
	Name            string           `json:"name,omitempty"`             // 会话名（首消息自动命名或 /rename；空 = 未命名）
	Model           string           `json:"model,omitempty"`            // 会话使用的模型（resume 恢复）
	ThinkingEnabled *bool            `json:"thinking_enabled,omitempty"` // nil = 默认开启（/thinking 切换后显式 true/false）
	ThinkingEffort  string           `json:"thinking_effort,omitempty"`  // 推理档位；空 = 继承 client 默认
	CWD             string           `json:"cwd,omitempty"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`
	Todos           []TodoItem       `json:"todos,omitempty"`      // todo 工具挂这
	Permission      *PermissionState `json:"permission,omitempty"` // 审批状态（ADR-029）
	PlanMode        bool             `json:"plan_mode,omitempty"`  // plan 模式开关（/plan 或 plan_enter/plan_done，ADR-036）
	Plan            *PlanState       `json:"plan,omitempty"`       // plan 文件指针（ADR-036）
	Summary         string           `json:"summary,omitempty"`    // 压缩摘要，预留
}

// TodoItem 是单个任务项（AgentScope tasksContext 对位）。对照 codex/opencode
// 调研（ADR-027）：无独立 id，Position 即"有序列表第几行"（模型显式维护）；
// 全量替换语义，工具每次传完整列表整体重建。
type TodoItem struct {
	Position    int    `json:"position"`    // 顺序（模型维护，1 基）
	Description string `json:"description"` // 任务描述
	Status      string `json:"status"`      // pending | in_progress | completed
}

// todo 状态枚举。
const (
	TodoPending    = "pending"
	TodoInProgress = "in_progress"
	TodoCompleted  = "completed"
)

// PermissionState 是会话级审批状态（阶段三权限，ADR-029）：
// 模式 + 会话级记忆，存快照供 resume 恢复（impl.SessionMiddleware 落盘）。
type PermissionState struct {
	Mode     string   `json:"mode,omitempty"`     // readonly | acceptedit | bypass
	Approved []string `json:"approved,omitempty"` // 会话级审批记忆（工具名 / 规范化命令 key）
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

// ReplaceTodos 全量替换 todo 列表（update_todo 工具调用路径）。按 Position
// 升序稳定排序（重复 position 保持传入顺序），模型传什么存什么，不做任何
// 归一化（ADR-027：one in_progress 靠 prompt 约束）。
func (a *AgentState) ReplaceTodos(items []TodoItem) {
	sorted := make([]TodoItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	a.Todos = sorted
}

// statusMark 将状态映射为 checkbox 符号（pending / in_progress / completed）。
func statusMark(status string) string {
	switch status {
	case TodoInProgress:
		return "[~]"
	case TodoCompleted:
		return "[x]"
	default:
		return "[ ]"
	}
}

// RenderTodos 将 todo 列表渲染为 markdown 有序列表（重新编号 1..n）。工具
// 结果回填与偏离提醒共用（ADR-027）。
func (a *AgentState) RenderTodos() string {
	if len(a.Todos) == 0 {
		return "（当前无待办）"
	}
	var sb strings.Builder
	for i, t := range a.Todos {
		fmt.Fprintf(&sb, "%d. %s %s\n", i+1, statusMark(t.Status), t.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// SaveFile 将 state 整体 JSON 写入 path（原子写：进程唯一临时文件 + fsync +
// rename，避免半截文件与断电丢内容）。同时刷新 UpdatedAt。
func SaveFile(path string, a *AgentState) error {
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("agentstate: marshal: %w", err)
	}
	// 临时名带 pid（C1，2026-08-10）：固定 path+".tmp" 会被两个并发进程
	// （resume 同一会话）互踩；写后 fsync 再 rename，断电不丢内容。
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("agentstate: create %s: %w", tmp, err)
	}
	cleanup := func() {
		f.Close()
		os.Remove(tmp)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("agentstate: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("agentstate: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("agentstate: close %s: %w", tmp, err)
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
