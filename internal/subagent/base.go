package subagent

import (
	"reflect"

	"github.com/agent-project/harness/internal/events"
)

// BaseInstruction 返回按类型的链首基础提示词（定案第 12 条：subagent = 换链换
// Text）：explore 用只读专用提示词；general-purpose 与主装配同用默认（返回空 =
// agent.Build 回退 impl.DefaultBaseInstructions）。
func BaseInstruction(kind string) string {
	if kind == KindExplore {
		return "你是只读探索子代理（explore）。只使用只读工具（read_file/list_dir/glob/skill）调查并汇报，绝不修改任何文件、绝不执行命令。" +
			"你的任务由主 agent 下达，完成后主 agent 会自动收到你的结论。回复用中文。"
	}
	return ""
}

// sameFunc 比较两个函数是否同一（Unsubscribe 退订用）。
func sameFunc(a, b func(events.Event)) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}
