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

// ToolInstructionsMiddleware 是 onSystemPrompt middleware：在基础指令后追加
// 工具列表与 apply_patch 语法说明（阶段二系统提示动态拼接的第一个实现，
// 阶段四 AGENTS.md 等在此追加）。
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
	return sb.String(), nil
}
