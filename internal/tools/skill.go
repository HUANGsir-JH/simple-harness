package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/skills"
)

// SkillTool 按名加载一个全局技能的完整指令（ADR-044，渐进式披露）：
// 系统提示只注入目录摘要（SkillsCatalogMiddleware），模型判定"用户点名技能
// 或任务与描述明显匹配"后调用本工具，Handle 从磁盘现读 SKILL.md 全文并以
// <skill_content> 包装回填——正文只在被明确加载时进入上下文。
//
// 无状态（ADR-026）：只持 SkillsDir（app 层注入的全局技能根，防环约定同
// AGENTS.md），不依赖 rc；Handle 纯磁盘读。定位规则（目录包优先、同名
// 去重）与目录展示单一来源——复用 skills.Discover 的判定，避免工具与
// 中间件两套逻辑漂移。
type SkillTool struct {
	// SkillsDir 是全局技能根（~/.harness/skills）；空 = 技能不可用
	// （Handle 回填"未配置"，仍注册工具避免模型侧 404）。
	SkillsDir string
}

// skillArgs 是 skill 工具的参数形状。
type skillArgs struct {
	Name string `json:"name" jsonschema:"description=要加载的技能名（系统提示 Skills 目录中的确切名称）"`
}

func (SkillTool) Name() string { return "skill" }

func (SkillTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "skill",
		Description: "加载一个可用技能的完整指令。若用户点名了某个技能、或当前任务与系统提示 Skills 目录中某个技能" +
			"的描述明显匹配，先调用本工具加载该技能的完整指令再采取行动；目录仅含摘要，加载前不要推断技能内容。",
		Parameters: schemaOf[skillArgs](),
	}
}

func (t SkillTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	p, err := parseArgs[skillArgs]("skill", args)
	if err != nil {
		return messages.ToolResult{}, err
	}
	if !skills.IsSkillName(p.Name) {
		return messages.ToolResult{}, &ToolError{RespondToModel: true,
			Message: "skill: 非法技能名 " + fmt.Sprintf("%q", p.Name) + "（技能名为 kebab-case）"}
	}
	if t.SkillsDir == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true,
			Message: "skill: 全局技能目录未配置（技能不可用）"}
	}
	// 定位复用 Discover 的判定（目录包优先、非法跳过、平铺回退）——与
	// SkillsCatalogMiddleware 展示的目录单一来源，两者不会漂移。
	for _, s := range skills.Discover(t.SkillsDir) {
		if s.Name != p.Name {
			continue
		}
		loaded, err := skills.Load(s.Path)
		if err != nil {
			return messages.ToolResult{}, &ToolError{RespondToModel: true,
				Message: "skill: 加载 " + p.Name + " 失败: " + err.Error()}
		}
		return messages.ToolResult{Success: true, Content: skills.RenderContent(loaded)}, nil
	}
	return messages.ToolResult{}, &ToolError{RespondToModel: true,
		Message: "skill: 未知技能 " + fmt.Sprintf("%q", p.Name) + "（见系统提示 Skills 目录；技能根为 " + filepath.Clean(t.SkillsDir) + "）"}
}
