package impl

import (
	"context"

	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/tools"
)

// ToolOutputMiddleware 是工具结果统一截断中间件（ADR-028）：挂 onToolCall，
// after 阶段改写本批新增的 tool_result 消息内容（截断 + 落盘 evictions/）。
// 工具自身返回完整结果（职责纯），截断策略在此一处定义、可插拔。
//
// read_file 豁免 evict（ADR-028）：它是"读"工具，模型用 start_line/end_line
// 主动控制粒度，全量就在原文件里；再落盘副本无意义，且会让"读 evictions 文件"
// 闭环二次截断（模型永远读不到全量）。豁免后 read_file 返回完整内容，由
// read 自身的大文件保护（>MaxReadFileBytes 提示分段）兜底。
//
// 注：transcript（会话落盘）记完整结果（审计全量）；conversation（模型
// 上下文）经本中间件截断为 preview + 路径，模型用 read_file/grep 读全量。
type ToolOutputMiddleware struct {
	middleware.Base
}

// OnToolCall 在工具批执行完成后，对本批新增消息（tool_result）统一截断。
//
// 时序契约（C6，2026-08-10）：本中间件的 after 改写发生在 agent.runToolBatch
// 的 emit(EventToolResult) 之后（agent 先 emit 全量结果、再经 onToolCall
// after 截断 conversation）——transcript 因此记全量、conversation 记截断
// （双轨审计，ADR-025）。若把 emit 挪到本 after 之后，transcript 会记录截断
// 内容、审计完整性静默丢失；agent 测试 TestEmitBeforeTruncation 锁定该契约。
func (ToolOutputMiddleware) OnToolCall(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ToolCallInput, next middleware.ToolCallHandler) error {
	if rc == nil || rc.Messages == nil {
		return next(ctx, rc, in)
	}
	// callID → 工具名 映射（判断 read_file 豁免）。工具批并发执行，结果按
	// call_id 回填（ADR-024），这里在 after 统一关联。
	nameByCall := make(map[string]string, len(in.Calls))
	for _, c := range in.Calls {
		if c != nil {
			nameByCall[c.ID] = c.Name
		}
	}
	before := len(rc.Messages.Messages)
	err := next(ctx, rc, in)
	// after：只处理本批新增的消息（before 之前的属历史，resume 全量保留）。
	for _, msg := range rc.Messages.Messages[before:] {
		for i := range msg.ToolResults {
			tr := &msg.ToolResults[i]
			if nameByCall[tr.ToolCallID] == "read_file" {
				continue // 豁免：read 返回完整，模型控制读取粒度
			}
			tr.Content = tools.EvictContent(rc, tr.Content)
		}
	}
	return err
}
