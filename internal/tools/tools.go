// Package tools 定义 agent 可调用的工具系统：Tool 接口、注册表与错误二分类。
// 工具执行遵循 ADR-003 错误二分类：RespondToModel（错误回填历史、循环继续）
// / Fatal（终止 turn）。
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// Tool 是 agent 可调用的能力。Name 唯一；Spec 描述给模型（JSON Schema）；
// Handle 执行实际逻辑并返回统一结果。
type Tool interface {
	// Name 返回工具名（模型可见，须唯一）。
	Name() string
	// Spec 返回工具定义（name + description + parameters JSON Schema），
	// 采样时传给模型。
	Spec() provider.ToolSpec
	// Handle 执行一次工具调用。callID 用于关联（结果回填历史）。
	// 返回统一 ToolResult；执行失败时返回 error（*ToolError 携带二分类语义）。
	Handle(ctx context.Context, callID string, args json.RawMessage) (messages.ToolResult, error)
}

// ToolError 是工具错误的二分类容器。
// RespondToModel=true：错误文本回填历史（success=false），agent 循环继续，
// 模型看到失败可换思路重试；false：Fatal，终止当前 turn。
type ToolError struct {
	RespondToModel bool
	Message        string
}

func (e *ToolError) Error() string { return e.Message }

// MaxOutputChars 是工具结果文本的最大长度（超出截断，避免撑爆上下文）。
const MaxOutputChars = 20000

// truncate 将文本截断到 MaxOutputChars，超长加省略号标记。
func truncate(s string) string {
	if len(s) <= MaxOutputChars {
		return s
	}
	return s[:MaxOutputChars] + "\n… [truncated]"
}

// Registry 是工具注册表：保持注册顺序（模型可见顺序稳定），按名查找。
type Registry struct {
	order []string
	tools map[string]Tool
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register 注册一个工具；重名或空名返回错误。
func (r *Registry) Register(t Tool) error {
	if t == nil || t.Name() == "" {
		return fmt.Errorf("tools: nil tool or empty name")
	}
	if _, ok := r.tools[t.Name()]; ok {
		return fmt.Errorf("tools: %q already registered", t.Name())
	}
	r.tools[t.Name()] = t
	r.order = append(r.order, t.Name())
	return nil
}

// Specs 返回按注册顺序排列的工具定义（供采样传给模型，顺序稳定）。
func (r *Registry) Specs() []provider.ToolSpec {
	out := make([]provider.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name].Spec())
	}
	return out
}

// Get 按名查找工具。
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Has 判断工具是否已注册。
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}
