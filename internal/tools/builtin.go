package tools

// Builtins 返回全部内置工具（按注册顺序，模型可见顺序稳定）。
//
// skillsDir 是全局技能目录（~/.harness/skills，app 层注入；ADR-044）——
// 仅 SkillTool 使用（其它工具无参构造）；空 = skill 工具仍注册（Handle
// 回填"技能不可用"）。技能目录随 Builtins 传入而非独立注册，保持"全部
// 工具一个列表"的单一装配入口。
func Builtins(skillsDir string) []Tool {
	return []Tool{
		ReadFileTool{},
		ListDirTool{},
		GlobTool{},
		WriteFileTool{},
		ShellCommandTool{},
		ApplyPatchTool{},
		UpdateTodoTool{},
		AskUserTool{},
		PlanEnterTool{},
		WritePlanTool{},
		PlanDoneTool{},
		SkillTool{SkillsDir: skillsDir},
	}
}
