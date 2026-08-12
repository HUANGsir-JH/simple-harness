package impl

import (
	"context"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// TestUsageMiddlewareAccumulates 验证 onReasoning after 累计：agent.sample 存
// rc.attrs["round_usage"]，中间件读取后 AddUsage + SetLastContextTokens
// （ADR-037 用量展示）。SetLastContextTokens 记**单轮完整占用**（input +
// cache_read + output，ADR-037 勘误：不能只记 input——端点只统计未缓存新增）。
func TestUsageMiddlewareAccumulates(t *testing.T) {
	st := agentstate.New("s1", "m", ".")
	rc := middleware.NewRuntimeContext()
	rc.State = st

	m := UsageMiddleware{}
	called := false
	err := m.OnReasoning(context.Background(), rc, middleware.ReasoningInput{},
		func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
			called = true
			// 模拟 agent.sample 落 usage 到 rc.attrs。
			rc.Set("round_usage", &messages.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 20})
			return nil
		})
	if err != nil {
		t.Fatalf("OnReasoning: %v", err)
	}
	if !called {
		t.Fatal("next 未被调用")
	}
	totals := st.UsageTotals()
	if totals.InputTokens != 100 || totals.OutputTokens != 50 || totals.CacheReadInputTokens != 20 {
		t.Errorf("totals: got %+v", totals)
	}
	// 单轮完整占用 = input(100) + cache_read(20) + output(50) = 170。
	if st.CurrentContextTokens() != 170 {
		t.Errorf("last context（完整占用）: got %d, want 170", st.CurrentContextTokens())
	}
}

// TestUsageMiddlewareContextTokensTotal 验证 LastContextTokens 记录**单轮完整
// 上下文占用**（input + cache_read + cache_creation + output，opencode
// tokens.total 口径，ADR-037 勘误）——不是只记 input_tokens。DeepSeek 等端点
// input_tokens 只统计未命中缓存的新增输入，历史在 cache_read 里；只记 input
// 会把占用低估十几倍（footer 显示 0k + 压缩永不触发）。
func TestUsageMiddlewareContextTokensTotal(t *testing.T) {
	st := agentstate.New("s1", "m", ".")
	rc := middleware.NewRuntimeContext()
	rc.State = st

	m := UsageMiddleware{}
	err := m.OnReasoning(context.Background(), rc, middleware.ReasoningInput{},
		func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
			// 模拟真实 DeepSeek 返回：新增 input 很小，历史占大头的 cache_read。
			rc.Set("round_usage", &messages.Usage{
				InputTokens:              665,
				CacheReadInputTokens:     239_691,
				CacheCreationInputTokens: 100,
				OutputTokens:             7_900,
			})
			return nil
		})
	if err != nil {
		t.Fatalf("OnReasoning: %v", err)
	}
	// 完整占用 = 665 + 239691 + 100 + 7900 = 248356，而非 665。
	if got := st.CurrentContextTokens(); got != 248_356 {
		t.Errorf("CurrentContextTokens = %d, want 248356（input+cache+output 完整占用）", got)
	}
}

// TestUsageMiddlewareSkipsOnError 验证采样出错时不累计（usage 无效）。
func TestUsageMiddlewareSkipsOnError(t *testing.T) {
	st := agentstate.New("s1", "m", ".")
	rc := middleware.NewRuntimeContext()
	rc.State = st

	m := UsageMiddleware{}
	err := m.OnReasoning(context.Background(), rc, middleware.ReasoningInput{},
		func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
			rc.Set("round_usage", &messages.Usage{InputTokens: 10})
			return context.Canceled
		})
	if err == nil {
		t.Fatal("应传播 next 的错误")
	}
	if got := st.UsageTotals().InputTokens; got != 0 {
		t.Errorf("出错时不应累计，got %d", got)
	}
}

// TestUsageMiddlewareNilUsage 验证 round_usage 为 nil 时 no-op。
func TestUsageMiddlewareNilUsage(t *testing.T) {
	st := agentstate.New("s1", "m", ".")
	rc := middleware.NewRuntimeContext()
	rc.State = st

	m := UsageMiddleware{}
	if err := m.OnReasoning(context.Background(), rc, middleware.ReasoningInput{},
		func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
			rc.Set("round_usage", (*messages.Usage)(nil))
			return nil
		}); err != nil {
		t.Fatalf("OnReasoning: %v", err)
	}
	if got := st.UsageTotals().InputTokens; got != 0 {
		t.Errorf("nil usage 不应累计，got %d", got)
	}
}
