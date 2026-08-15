package impl

import (
	"context"
	"strings"

	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/skills"
)

// SkillsCatalogMiddleware 是 onSystemPrompt 中间件：把全局技能目录
// （~/.harness/skills，ADR-044）的摘要列表注入系统提示（名称 + 描述 +
// 可选适用场景 + 触发引导），实现渐进式披露——目录只含摘要，完整指令由
// 模型按需经 skill 工具加载。
//
// 仅挂 onSystemPrompt，不参与洋葱 hook。无状态（ADR-026）：只持 SkillsDir
// （app 层注入，防环约定同 AgentsMdMiddleware），共享链可并发。发现失败/
// 空目录原样透传，绝不返回错误——技能目录不存在就是没有技能，不得终止
// 回合（与 agentsmd 同哲学）。ComposeSystemPrompt 每回合执行一次，目录
// 每回合刷新（回合内新增/删除技能下回合可见）。
type SkillsCatalogMiddleware struct {
	middleware.Base
	// SkillsDir 是全局技能根（~/.harness/skills）；空 = 不注入目录段。
	SkillsDir string
}

// OnSystemPrompt 在现有内容后追加技能目录段（有技能时）。
func (m SkillsCatalogMiddleware) OnSystemPrompt(_ context.Context, _ *middleware.RuntimeContext, current string) (string, error) {
	if m.SkillsDir == "" {
		return current, nil
	}
	list := skills.Discover(m.SkillsDir)
	if len(list) == 0 {
		return current, nil
	}
	var sb strings.Builder
	if current != "" {
		sb.WriteString(current)
		sb.WriteString("\n\n")
	}
	sb.WriteString("# Skills（技能）\n")
	sb.WriteString("技能是可复用的任务指令（SKILL.md）。本会话可用技能：\n")
	for _, s := range list {
		sb.WriteString(skills.CatalogLine(s))
		sb.WriteString("\n")
	}
	sb.WriteString("若用户点名某个技能、或任务与某技能的描述明显匹配，先调用 skill 工具加载其完整指令再采取行动；目录仅含摘要，未加载前不要推断技能内容。\n")
	return sb.String(), nil
}
