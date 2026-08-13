package impl

import (
	"context"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// TestUsageMiddlewareOverwrites 验证 onReasoning after 覆盖语义（ADR-037 勘误
// 2026-08-13）：agent.sample 存 rc.attrs["round_usage"]，中间件读取后
// SetUsage（覆盖最近一次调用，跨轮累加 cache_read 会虚高）+ SetLastContextTokens
// （ADR-037 用量展示）。SetLastContextTokens 记**单轮完整占用**（input +
// cache_read + output，ADR-037 勘误：不能只记 input——端点只统计未缓存新增）。
func TestUsageMiddlewareOverwrites(t *testing.T) {
	st := agentstate.New("s1", "m", ".")
	rc := middleware.NewRuntimeContext()
	rc.State = st

	m := UsageMiddleware{}
	usages := []*messages.Usage{
		{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 20},
		{InputTokens: 30, OutputTokens: 10, CacheReadInputTokens: 40},
	}
	for i, u := range usages {
		called := false
		err := m.OnReasoning(context.Background(), rc, middleware.ReasoningInput{},
			func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
				called = true
				// 模拟 agent.sample 落 usage 到 rc.attrs。
				rc.Set("round_usage", u)
				return nil
			})
		if err != nil {
			t.Fatalf("OnReasoning[%d]: %v", i, err)
		}
		if !called {
			t.Fatal("next 未被调用")
		}
	}
	totals := st.UsageTotals()
	if totals.InputTokens != 30 || totals.OutputTokens != 10 || totals.CacheReadInputTokens != 40 {
		t.Errorf("覆盖语义应保留最后一次调用: got %+v", totals)
	}
	// 单轮完整占用 = input(30) + cache_read(40) + output(10) = 80。
	if st.CurrentContextTokens() != 80 {
		t.Errorf("last context（完整占用）: got %d, want 80", st.CurrentContextTokens())
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
