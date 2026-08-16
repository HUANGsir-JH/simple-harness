package subagent

import (
	"context"

	"github.com/agent-project/harness/internal/middleware"
)

// DelegationInstructions 是 general-purpose 子 agent 的委托上下文段
// （2026-08-16 buildSubagent 独立装配）：主 persona 保持 uniform（与主装配
// 同用 impl.DefaultBaseInstructions），委托段作为独立中间件追加在其后——
// 对齐 deepseek-harness 的 SUBAGENT_DELEGATION_CONTEXT（"system prompt stays
// uniform across parents and children"，权限边界用独立 context contribution
// 注入而非并入 persona）。只讲子 agent 的差异点：权限固定/不重试被拒/任务
// 范围/结论回父/不能问用户/可嵌套/wait_task。不含 {{cwd}}/{{model}} 占位符。
const DelegationInstructions = `你是被委派的子代理（subagent），由主 agent 通过 spawn_agent 启动，权限范围在启动时已固定。需要审批的操作会被自动拒绝——不要重试被拒绝的操作，在回复中说明限制，让主 agent 处理。

- 任务是 spawn_agent 下达的 message，聚焦完成它，不要超出任务范围。
- 完成后输出最终结论（markdown），结果会自动注入主 agent 的对话。
- 你无法直接询问用户：需要确认时基于现有信息做合理决定，在结论中注明假设。
- 可以 spawn 自己的子代理（嵌套最多 2 层）；shell 后台任务用 wait_task 等待结果。`

// DelegationInstructionsMiddleware 在链首 persona 之后追加委托段
// （onSystemPrompt）：注册在 BaseInstructionsMiddleware 之后、AgentsMd 之前，
// 文本顺序 = persona → 委托段 → 项目上下文。仅 general-purpose 装配挂载
// （explore 的专属提示词已含委派说明，不重复注入）。
type DelegationInstructionsMiddleware struct {
	middleware.Base
}

// OnSystemPrompt 追加委托段：当前内容为空则原样注入，非空则拼接在其后
// （追加语义同 AgentsMdMiddleware——BaseInstructions 是唯一前置拼接的链首，
// 其余中间件追加，文本顺序 = 注册顺序）。注册在 BaseInstructions 之后 →
// 委托段紧随 persona、AgentsMd 之前。
func (DelegationInstructionsMiddleware) OnSystemPrompt(_ context.Context, _ *middleware.RuntimeContext, current string) (string, error) {
	if current == "" {
		return DelegationInstructions, nil
	}
	return current + "\n\n" + DelegationInstructions, nil
}

// exploreInstructions 是 explore 子 agent 的专属链首提示词（2026-08-16 重写，
// 对齐 opencode explore.txt 风格：简短聚焦——身份 + 专长/禁止 + 任务 + 完成
// 动作，不复制主 persona 的通用工作纪律；原一句话版本升级为完整结构）。
// 只读 shell 说明（2026-08-16）：工具层强制白名单，提示词只引导使用场景。
const exploreInstructions = `你是只读探索子代理（explore），由主 agent 委派调查任务。只使用只读工具（read_file/list_dir/glob/skill + 只读 shell）调查并汇报——绝不修改任何文件、绝不执行任何写操作。

- 调查目标由 spawn_agent 下达：定位代码、理解结构、收集证据，不要超出任务范围。
- 只读 shell 可执行 git 查询（git status/log/diff/branch/grep/show/ls-files）与文件查看/统计（wc/stat/du 等），白名单外命令会被拒绝——换用 read_file/glob 或改用白名单命令。
- 完成后输出结构化结论（发现、关键位置、结论），主 agent 会自动收到。
- 用中文，简洁直接，markdown。`
