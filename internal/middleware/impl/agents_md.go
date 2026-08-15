package impl

import (
	"context"

	"github.com/agent-project/harness/internal/agentsmd"
	"github.com/agent-project/harness/internal/middleware"
)

// AgentsMdMiddleware 是 onSystemPrompt 中间件：把全局 persona（~/.harness/agents.md）
// 与项目级 AGENTS.md（根→cwd，CLAUDE.md 回退）拼接进系统提示（阶段四，ADR-043）。
//
// 仅挂 onSystemPrompt，不参与洋葱 hook。无状态（ADR-026）：只持 Options
// （GlobalPath/MaxBytes），会话启动目录从 rc.State.CWD 读——共享 chain 可并发。
// 读失败/空文件由 agentsmd.Compose 内部跳过，绝不返回错误（AGENTS.md 不得终止回合）。
type AgentsMdMiddleware struct {
	middleware.Base
	Options agentsmd.Options
}

// OnSystemPrompt 在现有内容后追加 AGENTS.md 指令段。
func (m AgentsMdMiddleware) OnSystemPrompt(_ context.Context, rc *middleware.RuntimeContext, current string) (string, error) {
	added := agentsmd.Compose(m.Options, workspaceOf(rc))
	if added == "" {
		return current, nil
	}
	if current == "" {
		return added, nil
	}
	return current + "\n\n" + added, nil
}
