package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderMarkdownDoesNotHighlightLexerErrors(t *testing.T) {
	markdown := "" +
		"## 目录结构\n\n" +
		"```\n" +
		"cmd/harness/          # CLI 入口：main + run/resume/repl 命令编排\n" +
		"internal/\n" +
		"  config/       配置域（最底层）：Config/ProviderConfig 类型 + YAML 加载/校验\n" +
		"  app/          进程级装配根：App{Config, Provider} 惰性单例\n" +
		"  agent/        无状态 ReAct loop + Build 装配工厂（client+工具+中间件链）\n" +
		"  middleware/   6-hook 扩展机制（onAgent/onReasoning/onToolCall/onActing/onModelCall + onSystemPrompt）+ RuntimeContext\n" +
		"  middleware/impl/  内置中间件：基础提示词、AGENTS.md 注入、技能目录、会话 load-save、todo 提醒、结果截断、三档审批\n" +
		"  provider/     单 anthropic wire（多后端=base_url 覆盖）+ 块事件适配\n" +
		"  messages/     统一 Message 模型（含 thinking）+ JSON 序列化\n" +
		"  tools/        Tool 接口 + 注册表 + 12 个内置工具（read_file/write_file/apply_patch/shell/glob/update_todo/skill 等）\n" +
		"  agentstate/   AgentState 快照（todo/权限/plan/摘要/用量）+ 原子落盘\n" +
		"  compact/      LLM 摘要压缩（85% 阈值自动 + /compact 手动）\n" +
		"  agentsmd/     AGENTS.md/CLAUDE.md 发现与拼接\n" +
		"  skills/       全局 Skill 发现（SKILL.md）+ 渲染\n" +
		"  session/      workspace 项目分桶 + 块级 transcript 异步落盘 + resume\n" +
		"  subagent/     子 agent：Manager + 5 控制工具（spawn/send/interrupt/resume/list）\n" +
		"  ui/           渲染层 + tui/（bubbletea 全屏交互 UI）\n" +
		"  e2e/          进程外端到端测试\n" +
		"```\n"

	rendered := renderMarkdown(markdown, 100)
	assertNoANSIBackground(t, rendered)
	for _, want := range []string{"cmd/harness/", "middleware/", "内置工具"} {
		if !strings.Contains(ansi.Strip(rendered), want) {
			t.Fatalf("rendered markdown lost %q:\n%s", want, ansi.Strip(rendered))
		}
	}
}

func assertNoANSIBackground(t *testing.T, rendered string) {
	t.Helper()
	for _, sequence := range ansiSequences(rendered) {
		if !strings.HasSuffix(sequence, "m") {
			continue
		}
		params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(sequence, "\x1b["), "m"), ";")
		for _, param := range params {
			value, err := strconv.Atoi(param)
			if err != nil {
				continue
			}
			if value == 40 || (value >= 41 && value <= 48) || (value >= 100 && value <= 107) {
				t.Fatalf("markdown renderer emitted background color %q:\n%q", sequence, rendered)
			}
		}
	}
}

func ansiSequences(s string) []string {
	var sequences []string
	for i := 0; i < len(s); {
		if s[i] != '\x1b' {
			i++
			continue
		}
		length := ansiSequenceLength(s[i:])
		sequences = append(sequences, s[i:i+length])
		i += length
	}
	return sequences
}
