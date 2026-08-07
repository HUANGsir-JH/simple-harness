package tools

// Builtins 返回全部内置工具（按注册顺序，模型可见顺序稳定）。
func Builtins() []Tool {
	return []Tool{
		ReadFileTool{},
		ListDirTool{},
		GlobTool{},
		WriteFileTool{},
		ShellCommandTool{},
		ApplyPatchTool{},
	}
}
