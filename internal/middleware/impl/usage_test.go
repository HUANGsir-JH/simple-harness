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
// （ADR-037 用量展示）。
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
	if st.CurrentContextTokens() != 100 {
		t.Errorf("last context: got %d", st.CurrentContextTokens())
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
