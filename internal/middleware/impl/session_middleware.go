package impl

import (
	"context"
	"fmt"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
)

// SessionMiddleware 把 AgentState 注入/保存 agent.Run（onAgent，ADR-025/026）。
//
// 无状态（ADR-026）：**不持有会话对象**，全部信息从 rc 读——会话在调用前把
// StatePath/State 填入 rc（Session.RuntimeContext）。因此共享 chain 可被多个
// goroutine 并发 Run（并行架构可扩展的基石：零共享可变状态）。
//
//   - before：rc.State 为空且 StatePath 非空 → LoadFile（rc.State 已预置则跳过）
//   - after：保存 rc.State → StatePath（整体重写，每次 Run 结束）
//
// 对应 AgentScope 无状态引擎每次 call() 的 load/save：agent 核心 loop 不碰
// state，落盘/恢复完全在中间件层。
type SessionMiddleware struct {
	middleware.Base // 嵌入空实现，只需覆写 OnAgent
}

func (m SessionMiddleware) OnAgent(ctx context.Context, rc *middleware.RuntimeContext, in middleware.AgentInput, next middleware.AgentHandler) error {
	if rc.State == nil && rc.StatePath != "" {
		st, err := agentstate.LoadFile(rc.StatePath)
		if err != nil {
			return fmt.Errorf("session: load agentstate: %w", err)
		}
		rc.State = st
	}

	err := next(ctx, rc, in)

	if rc.State != nil && rc.StatePath != "" {
		if serr := agentstate.SaveFile(rc.StatePath, rc.State); serr != nil {
			if err != nil {
				return fmt.Errorf("%w; 保存 state 也失败: %v", err, serr)
			}
			return fmt.Errorf("session: save agentstate: %w", serr)
		}
	}
	return err
}
