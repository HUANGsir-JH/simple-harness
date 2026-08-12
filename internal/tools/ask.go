package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// AskUserTool 向用户提一个问题并等待回答（opencode question / codex
// request_user_input 对位，ADR-036）。通用工具，两模式可用；经
// rc.Approver.Ask 弹 HITL（选项 + Other 自由文本 + 单选/多选），回答回填
// 模型继续。
type AskUserTool struct{}

func (AskUserTool) Name() string { return "ask_user" }

// askUserArgs 是 ask_user 的参数形状。
type askUserArgs struct {
	Question string      `json:"question" jsonschema:"description=完整问题（向用户展示）"`
	Header   string      `json:"header,omitempty" jsonschema:"description=弹窗短标题（可选，≤30 字符）"`
	Options  []askOption `json:"options,omitempty" jsonschema:"description=选项列表（可选；空 = 纯自由文本提问）"`
	Multiple bool        `json:"multiple,omitempty" jsonschema:"description=允许多选（默认单选）"`
}

// askOption 是单个选项（label + 可选说明）。
type askOption struct {
	Label       string `json:"label" jsonschema:"description=选项显示文本（1-5 词）"`
	Description string `json:"description,omitempty" jsonschema:"description=选项说明（可选）"`
}

func (AskUserTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "ask_user",
		Description: "向用户提一个问题并等待回答。用于存在歧义、关键取舍、或无法从代码/环境推断的信息；" +
			"支持选项 + 自定义文本（用户可输入选项外的回答）+ 单选/多选。回答会回填供继续。能用工具调研/阅读解决的不要问。",
		Parameters: schemaOf[askUserArgs](),
	}
}

func (AskUserTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	if rc == nil || rc.Approver == nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "ask_user: 当前环境无法向用户提问"}
	}
	p, err := parseArgs[askUserArgs]("ask_user", args)
	if err != nil {
		return messages.ToolResult{}, err
	}
	if strings.TrimSpace(p.Question) == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "ask_user: question 不能为空"}
	}
	opts := make([]middleware.AskOption, 0, len(p.Options))
	for _, o := range p.Options {
		opts = append(opts, middleware.AskOption{Label: o.Label, Description: o.Description})
	}
	res, err := rc.Approver.Ask(ctx, middleware.AskRequest{
		Question:    p.Question,
		Header:      p.Header,
		Options:     opts,
		Multiple:    p.Multiple,
		AllowCustom: true, // Other 自定义默认允许（opencode custom 默认 true 对位）
	})
	if err != nil {
		return messages.ToolResult{}, err // ctx canceled（Esc 中断）→ Fatal
	}
	// 格式化回答回填模型（落盘，模型可见）。
	content := "用户回答：" + p.Question
	var parts []string
	if len(res.Selection) > 0 {
		parts = append(parts, strings.Join(res.Selection, "、"))
	}
	if custom := strings.TrimSpace(res.Custom); custom != "" {
		parts = append(parts, "自定义: "+custom)
	}
	if len(parts) == 0 {
		parts = append(parts, "（用户取消了提问）")
	}
	return messages.ToolResult{Success: true, Content: content + " → " + strings.Join(parts, "；")}, nil
}
