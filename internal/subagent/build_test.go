package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/provider"
)

// TestBuildSubagentPrompts 验证子装配提示词（2026-08-16 buildSubagent 独立
// 装配）：general-purpose = uniform 主 persona（impl.DefaultBaseInstructions）
// + 委托段（追加在 persona 之后）；explore = 专属只读提示词（不含主 persona）。
func TestBuildSubagentPrompts(t *testing.T) {
	run := func(kind string) string {
		var instructions string
		m, rc, _ := testHarness(t, func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
			instructions = req.Instructions
			return provider.NewFakeStream([]provider.Event{
				{Type: provider.EventTextDone, Text: "ok"},
				{Type: provider.EventDone},
			}), nil
		})
		a, err := m.buildSubagent(kind)
		if err != nil {
			t.Fatalf("buildSubagent(%s): %v", kind, err)
		}
		if err := a.Run(context.Background(), rc, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return instructions
	}

	gp := run(KindGeneralPurpose)
	if !strings.Contains(gp, "运行在用户终端") {
		t.Error("general-purpose 应保留 uniform 主 persona（impl.DefaultBaseInstructions）")
	}
	if !strings.Contains(gp, "被委派的子代理") {
		t.Error("general-purpose 应含委托段（DelegationInstructions）")
	}
	if strings.Index(gp, "运行在用户终端") > strings.Index(gp, "被委派的子代理") {
		t.Error("委托段应追加在 persona 之后")
	}

	ex := run(KindExplore)
	if !strings.Contains(ex, "只读探索子代理") {
		t.Error("explore 应含专属提示词")
	}
	if strings.Contains(ex, "运行在用户终端") {
		t.Error("explore 不应含主 persona（专属提示词替代 uniform）")
	}
	if strings.Contains(ex, "被委派的子代理") {
		t.Error("explore 不应含委托段（提示词已含委派说明）")
	}
}

// TestBuildSubagentBaseInstructionsOverride 验证 Options.BaseInstructions 覆盖
// general-purpose persona（评测 config.base_instructions 透传；空 = 默认，
// explore 不受影响，2026-08-20）。
func TestBuildSubagentBaseInstructionsOverride(t *testing.T) {
	run := func(kind, base string) string {
		var instructions string
		m, rc, _ := testHarness(t, func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
			instructions = req.Instructions
			return provider.NewFakeStream([]provider.Event{
				{Type: provider.EventTextDone, Text: "ok"},
				{Type: provider.EventDone},
			}), nil
		})
		m.opts.BaseInstructions = base
		a, err := m.buildSubagent(kind)
		if err != nil {
			t.Fatalf("buildSubagent(%s): %v", kind, err)
		}
		if err := a.Run(context.Background(), rc, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return instructions
	}

	// 覆盖后 general-purpose 用自定义 persona（不再含默认中文 persona）。
	gp := run(KindGeneralPurpose, "You are a helpful software engineer assistant.")
	if !strings.Contains(gp, "helpful software engineer assistant") {
		t.Errorf("general-purpose 应使用覆盖 persona，got: %q", gp[:min(len(gp), 120)])
	}
	if strings.Contains(gp, "运行在用户终端") {
		t.Error("general-purpose 不应再含默认 persona")
	}
	if !strings.Contains(gp, "被委派的子代理") {
		t.Error("覆盖后委托段仍应保留")
	}

	// explore 固定专属提示词，覆盖不影响。
	ex := run(KindExplore, "You are a helpful software engineer assistant.")
	if !strings.Contains(ex, "只读探索子代理") {
		t.Error("explore 应保持专属提示词")
	}
}

// TestDelegationMiddleware 验证委托段注入语义（追加，同 AgentsMdMiddleware）：
// 空当前内容 → 原样注入；非空 → 拼接在其后（紧随 persona、AgentsMd 之前）。
func TestDelegationMiddleware(t *testing.T) {
	m := DelegationInstructionsMiddleware{}

	out, err := m.OnSystemPrompt(context.Background(), nil, "")
	if err != nil || out != DelegationInstructions {
		t.Errorf("空内容注入: %q, %v", out, err)
	}

	out, err = m.OnSystemPrompt(context.Background(), nil, "PERSONA")
	want := "PERSONA\n\n" + DelegationInstructions
	if err != nil || out != want {
		t.Errorf("追加拼接: %q, %v（want %q）", out, err, want)
	}
}
