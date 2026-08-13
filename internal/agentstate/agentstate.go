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
	"sync"
	"time"

	"github.com/agent-project/harness/internal/messages"
)

// AgentState 是会话的运行时状态快照。
//
// 并发安全（ADR-036 修订，2026-08-12）：内置互斥锁，全部共享字段的写、整体
// 序列化、关键读都经带锁方法——tools 包（update_todo/write_plan）与
// middleware（审批记忆）在并行工具批（ADR-024）内共享同一 *AgentState 指针，
// 锁下沉到数据自身避免 planMu/todoMu 两把锁保护同一份数据的 data race
// （plan-mode-review-2026-08-12 缺陷 04）。
//
// ⚠️ 含 sync.Mutex，不得按值复制 AgentState（go vet copylocks 会抓）；全仓库
// 只以指针使用（New/LoadFile/rc.State）。
type AgentState struct {
	mu sync.Mutex `json:"-"` // 不序列化；New/LoadFile 反序列化后零值即可用

	SessionID         string           `json:"session_id"`
	Name              string           `json:"name,omitempty"`             // 会话名（首消息自动命名或 /rename；空 = 未命名）
	Model             string           `json:"model,omitempty"`            // 会话使用的模型（resume 恢复）
	ThinkingEnabled   *bool            `json:"thinking_enabled,omitempty"` // nil = 默认开启（/thinking 切换后显式 true/false）
	ThinkingEffort    string           `json:"thinking_effort,omitempty"`  // 推理档位；空 = 继承 client 默认
	CWD               string           `json:"cwd,omitempty"`
	CreatedAt         string           `json:"created_at"`
	UpdatedAt         string           `json:"updated_at"`
	Todos             []TodoItem       `json:"todos,omitempty"`               // todo 工具挂这
	Permission        *PermissionState `json:"permission,omitempty"`          // 审批状态（ADR-029）
	PlanMode          bool             `json:"plan_mode,omitempty"`           // plan 模式开关（/plan 或 plan_enter/plan_done，ADR-036）
	Plan              *PlanState       `json:"plan,omitempty"`                // plan 文件指针（ADR-036）
	Usage             *messages.Usage  `json:"usage,omitempty"`               // 最近一次 API 调用的 token 用量（覆盖语义，/usage 展示；ADR-037）
	LastContextTokens int64            `json:"last_context_tokens,omitempty"` // 最近一次请求的完整上下文占用（单轮 input+cache+output，footer 与压缩触发，ADR-037）
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
	a.mu.Lock()
	defer a.mu.Unlock()
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
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.renderTodosLocked()
}

// renderTodosLocked 是 RenderTodos 的持锁内部实现（方法内不调用其它带锁方法）。
func (a *AgentState) renderTodosLocked() string {
	if len(a.Todos) == 0 {
		return "（当前无待办）"
	}
	var sb strings.Builder
	for i, t := range a.Todos {
		fmt.Fprintf(&sb, "%d. %s %s\n", i+1, statusMark(t.Status), t.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// --- 带锁访问方法（ADR-036 修订：并行工具批共享同一 *AgentState，全部共享
// 字段读写经方法加锁，替代 tools 包的 planMu/todoMu 分离锁）----

// SetPlanMode 设置 plan 模式开关（/plan、plan_enter/plan_done，ADR-036）。
func (a *AgentState) SetPlanMode(on bool) {
	a.mu.Lock()
	a.PlanMode = on
	a.mu.Unlock()
}

// SetPlanPath 设置计划文件路径（write_plan；Plan 为 nil 时先建）。
func (a *AgentState) SetPlanPath(path string) {
	a.mu.Lock()
	if a.Plan == nil {
		a.Plan = &PlanState{}
	}
	a.Plan.Path = path
	a.mu.Unlock()
}

// SetName 设置会话名（/rename；首消息自动命名）。
func (a *AgentState) SetName(name string) {
	a.mu.Lock()
	a.Name = name
	a.mu.Unlock()
}

// SetModel 设置会话模型（/model 运行时切换）。
func (a *AgentState) SetModel(model string) {
	a.mu.Lock()
	a.Model = model
	a.mu.Unlock()
}

// SetThinkingEnabled 设置 thinking 开关（--thinking/--no-thinking；nil = 默认
// 开启）。拷贝指针值，避免调用方后续改 *enabled 影响已存状态。
func (a *AgentState) SetThinkingEnabled(enabled *bool) {
	a.mu.Lock()
	if enabled != nil {
		v := *enabled
		a.ThinkingEnabled = &v
	} else {
		a.ThinkingEnabled = nil
	}
	a.mu.Unlock()
}

// SetThinkingEffort 设置推理档位（/effort 运行时切换）。
func (a *AgentState) SetThinkingEffort(effort string) {
	a.mu.Lock()
	a.ThinkingEffort = effort
	a.mu.Unlock()
}

// SetPermissionMode 设置审批模式（/permission、config 播种；Permission 为 nil
// 时先建，ADR-029）。
func (a *AgentState) SetPermissionMode(mode string) {
	a.mu.Lock()
	if a.Permission == nil {
		a.Permission = &PermissionState{}
	}
	a.Permission.Mode = mode
	a.mu.Unlock()
}

// AddApproved 把审批 key 记入会话级记忆（去重，原 impl.rememberApproved 逻辑
// 下沉；ADR-029）。Permission 为 nil 时先建。
func (a *AgentState) AddApproved(keys []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(keys) == 0 {
		return
	}
	if a.Permission == nil {
		a.Permission = &PermissionState{}
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		dup := false
		for _, k := range a.Permission.Approved {
			if k == key {
				dup = true
				break
			}
		}
		if !dup {
			a.Permission.Approved = append(a.Permission.Approved, key)
		}
	}
}

// SetUsage 记录最近一次 API 调用的 token 用量（**覆盖语义**，ADR-037 勘误
// 2026-08-13：每次采样返回的 usage 就是该次调用的完整账目，cache_read 是
// "当前历史全量"而非增量，跨轮累加会虚高到"好多 M"；覆盖语义显示最近一次
// 调用，与 opencode per-call 跟踪一致）。Usage 并行工具批/多轮采样共享同一
// *AgentState，带锁。
func (a *AgentState) SetUsage(u messages.Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Usage = &u
}

// SetLastContextTokens 记录最近一次请求的**完整上下文占用**（单轮
// input + cache_read + cache_creation + output，opencode tokens.total 口径，
// ADR-037 勘误）。footer 实时展示与压缩触发（ShouldCompact）用；压缩后重置
// 为 0 防重入。注意：不能只记 input_tokens——端点只统计未缓存新增，历史在
// cache_read，只记 input 会低估十几倍（footer 显示 0k + 压缩永不触发）。
func (a *AgentState) SetLastContextTokens(n int64) {
	a.mu.Lock()
	a.LastContextTokens = n
	a.mu.Unlock()
}

// UsageTotals 返回最近一次调用用量的防御性拷贝（/usage 展示；不受并发写影响）。
func (a *AgentState) UsageTotals() messages.Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Usage == nil {
		return messages.Usage{}
	}
	u := *a.Usage
	return u
}

// CurrentContextTokens 读取最近一次请求的 input_tokens（无数据返回 0）。
func (a *AgentState) CurrentContextTokens() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.LastContextTokens
}

// IsPlanMode 读取 plan 模式开关（方法名避开字段 PlanMode，Go 不允许同名）。
func (a *AgentState) IsPlanMode() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.PlanMode
}

// PlanPath 读取计划文件路径（无 Plan 返回 ""）。
func (a *AgentState) PlanPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Plan == nil {
		return ""
	}
	return a.Plan.Path
}

// PermissionMode 读取审批模式（无 Permission 返回 ""）。
func (a *AgentState) PermissionMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Permission == nil {
		return ""
	}
	return a.Permission.Mode
}

// Approved 返回审批记忆的防御性拷贝（调用方可安全遍历，不受并发写影响）。
func (a *AgentState) Approved() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Permission == nil {
		return nil
	}
	out := make([]string, len(a.Permission.Approved))
	copy(out, a.Permission.Approved)
	return out
}

// TodoCount 返回 todo 条数（避免整体拷贝的读路径）。
func (a *AgentState) TodoCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.Todos)
}

// TodoItems 返回 todo 列表的防御性拷贝（方法名避开字段 Todos）。
func (a *AgentState) TodoItems() []TodoItem {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]TodoItem, len(a.Todos))
	copy(out, a.Todos)
	return out
}

// Marshal 序列化 state（加锁 + 刷新 UpdatedAt）。SaveFile 与任何需要整体编码
// 的调用都经它，保证并发落盘不与其他字段读写竞态。
func (a *AgentState) Marshal() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("agentstate: marshal: %w", err)
	}
	return data, nil
}

// SaveFile 将 state 整体 JSON 写入 path（原子写：进程唯一临时文件 + fsync +
// rename，避免半截文件与断电丢内容）。序列化经 a.Marshal()（加锁刷新
// UpdatedAt），落盘本身跨进程互踩由 pid 临时名防。
func SaveFile(path string, a *AgentState) error {
	data, err := a.Marshal()
	if err != nil {
		return err
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
