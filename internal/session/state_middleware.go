package session

import (
	"context"
	"fmt"
	"time"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
)

// StateMiddleware 把 AgentState 注入/保存 agent.Run（挂 onAgent，ADR-025）：
//   - before：加载 agentstate.json → rc.State（rc.State 已设置则跳过，CLI
//     resume 路径预置）
//   - after：保存 rc.State → agentstate.json（整体重写，每次 Run 结束）
//
// 对应 AgentScope 无状态引擎每次 call() 的 load/save：agent 核心 loop 不碰
// state，落盘/恢复完全在中间件层。
type StateMiddleware struct {
	middleware.Base // 嵌入空实现，只需覆写 OnAgent
	// Path 是 agentstate.json 路径（会话目录下）。
	Path string
}

func (m *StateMiddleware) OnAgent(ctx context.Context, rc *middleware.RuntimeContext, in middleware.AgentInput, next middleware.AgentHandler) error {
	if rc.State == nil {
		st, err := agentstate.LoadFile(m.Path)
		if err != nil {
			return fmt.Errorf("session: load agentstate: %w", err)
		}
		rc.State = st
	}
	if rc.State.CreatedAt == "" {
		// 新会话兜底补全（正常路径 CLI 已初始化）。
		rc.State.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	err := next(ctx, rc, in)

	if rc.State != nil {
		if serr := agentstate.SaveFile(m.Path, rc.State); serr != nil {
			if err != nil {
				return fmt.Errorf("%w; 保存 state 也失败: %v", err, serr)
			}
			return fmt.Errorf("session: save agentstate: %w", serr)
		}
	}
	return err
}
