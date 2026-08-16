package impl

import (
	"context"
	"os"
	"strings"

	"github.com/agent-project/harness/internal/middleware"
)

// DefaultBaseInstructions 是框架默认基础提示词（Build 装配标准链时注入）。
// 中文、成体系（阶段四 ADR-043）：身份 + 动态环境（{{cwd}}/{{model}}）+ 通用工作
// 方式 + 交流语气。**不重复** ToolInstructions 已有的工具列表 / apply_patch 语法 /
// todo 纪律 / shell 长任务引导——那些归工具说明段。
const DefaultBaseInstructions = `你是 harness，一个运行在用户终端里的编码 agent，协助用户完成软件工程任务。你与用户在同一台机器、同一个工作区上协作。

# 环境
- 工作目录：{{cwd}}
- 模型：{{model}}

# 工作方式
- 先理解再动手：动手前先读代码、搜索、弄清现状，不要臆测或直接跳到结论。
- 改动精准克制：贴合现有代码风格与既有工具/库，改动最小化、聚焦任务本身，不做无关改动。
- 根因优先：优先修根因而非表面打补丁，避免不必要的复杂度。
- 自主完成：用可用工具把任务做完再交回用户；不要编造或凭空猜答案；工具结果已能说明问题时，不要重复读同一文件或重复跑同一命令。
- 验证：改动后尽量用测试/构建验证，从最贴近改动处开始；不要顺手修无关的坏测试。

# 上下文
- 当你遇到一个全新的项目，可以先寻找 README、CLAUDE.md、AGENT.md等文件，了解项目的背景、目标和约定。

# 交流
- 简洁直接，用 markdown；命令、路径、代码标识符用反引号包裹。
- 除非用户明确要求，不要用 emoji。
- 除非用户明确要求，不要 git commit 或新建分支。
- 已写好的大段文件内容不必回显给用户，引用文件路径即可。`

// BaseInstructionsMiddleware 是标准链的第一个 onSystemPrompt 中间件：
// 在调用方 per-call 贡献（rc.SystemPrompt）之前注入基础提示词。
// subagent 装配可换不同 Text（不同提示词 = 不同装配，build.go 既定方向）。
// Text 支持 {{cwd}} 与 {{model}} 占位符（render 时注入动态上下文，ADR-043）。
// 仅挂 onSystemPrompt，不参与洋葱 hook（顺序无副作用）。
type BaseInstructionsMiddleware struct {
	middleware.Base
	Text string
}

// OnSystemPrompt 前置基础提示词：当前内容为空则原样注入，非空则拼接在
// 调用方贡献之前（基础提示词恒在最前）。
func (m BaseInstructionsMiddleware) OnSystemPrompt(_ context.Context, rc *middleware.RuntimeContext, current string) (string, error) {
	text := m.render(rc)
	if text == "" {
		return current, nil
	}
	if current == "" {
		return text, nil
	}
	return text + "\n\n" + current, nil
}

// render 注入动态上下文：工作目录取 rc.State.CWD（空回退 os.Getwd()），
// 模型取 rc.Model（空回退 "默认"）。Text 为空时替换为空串（透传语义不变）。
func (m BaseInstructionsMiddleware) render(rc *middleware.RuntimeContext) string {
	if m.Text == "" {
		return ""
	}
	cwd, model := "", ""
	if rc != nil {
		if rc.State != nil {
			cwd = rc.State.CWD
		}
		model = rc.Model
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if model == "" {
		model = "默认"
	}
	text := strings.ReplaceAll(m.Text, "{{cwd}}", cwd)
	text = strings.ReplaceAll(text, "{{model}}", model)
	return text
}
