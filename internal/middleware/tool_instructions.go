package middleware

import (
	"context"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/provider"
)

// applyPatchSyntax 是 apply_patch 语法说明（注入系统提示）。
// 依据 codex 调研：工具 description 太短不够格式敏感工具用，codex 在系统提示
// `# Tool Guidelines` 注入完整语法；我们 anthropic wire 无 freeform grammar
// 机制，语法只能放系统提示。
const applyPatchSyntax = `用 apply_patch 工具编辑文件，格式如下：

*** Begin Patch
*** Add File: path/to/new.txt
+新文件内容行
*** Update File: path/to/old.txt
@@ 定位（可选）
-被删除的行
+新增的行
 上下文行（前导一个空格）
*** Delete File: path/to/old.txt
*** End Patch

要点：
- 必须以 *** Begin Patch 开头、*** End Patch 结尾
- 每个文件操作必须以 *** Add File / *** Update File / *** Delete File 开头
- 新增或修改的行前加 +，删除的行前加 -，上下文行前加一个空格
- 路径必须相对当前工作目录，绝不用绝对路径
- 可在 Update 内用多个 @@ 分段分别定位多处修改`

// todoGuidance 是 update_todo 工具的使用引导（注入系统提示，ADR-027）。
// 与 applyPatchSyntax 同理：工具 description 放语义要点，系统提示放使用纪律
// （何时用、维护习惯）。参照 opencode 的 Task Management 引导段。
const todoGuidance = `# 任务管理
用 update_todo 工具维护一份待办清单，追踪多步骤任务进度：
- 任务需 3+ 个独立步骤时，开工前列出 todos（含验证步骤）
- 完成一步立刻标记 completed，不要攒着批量标
- 同时只保持一个 in_progress
- 全部步骤完成后传空列表清空 todo（不留已完成项）
- 每次调用传完整列表，用 position 维护顺序`

// ToolInstructionsMiddleware 是 onSystemPrompt middleware：在基础指令后追加
// 工具列表、apply_patch 语法说明与任务管理引导（阶段二系统提示动态拼接的
// 第一个实现，阶段四 AGENTS.md 等在此追加）。
type ToolInstructionsMiddleware struct {
	Base
	Tools []provider.ToolSpec
}

// OnSystemPrompt 追加工具说明段。
func (m ToolInstructionsMiddleware) OnSystemPrompt(_ context.Context, _ *RuntimeContext, current string) (string, error) {
	var sb strings.Builder
	sb.WriteString(current)
	sb.WriteString("\n\n# 可用工具\n")
	if len(m.Tools) == 0 {
		sb.WriteString("（当前无可用工具）\n")
	} else {
		for _, t := range m.Tools {
			fmt.Fprintf(&sb, "- %s: %s\n", t.Name, t.Description)
		}
	}
	sb.WriteString("\n# apply_patch 语法\n")
	sb.WriteString(applyPatchSyntax)
	if hasTool(m.Tools, "update_todo") {
		sb.WriteString("\n\n" + todoGuidance)
	}
	return sb.String(), nil
}

// hasTool 判断工具列表是否含指定名工具。
func hasTool(tools []provider.ToolSpec, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
