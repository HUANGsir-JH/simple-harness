package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/middleware"
)

func callAsk(t *testing.T, rc *middleware.RuntimeContext, raw string) (bool, string) {
	t.Helper()
	res, err := AskUserTool{}.Handle(context.Background(), rc, "c", json.RawMessage(raw))
	if err != nil {
		var te *ToolError
		if errors.As(err, &te) {
			return false, te.Message
		}
		t.Fatalf("ask_user: 非工具错误: %v", err)
	}
	return res.Success, res.Content
}

// TestAskUserSelection 验证选项回答回填（多选逗号连接）。
func TestAskUserSelection(t *testing.T) {
	rc := middleware.NewRuntimeContext()
	rc.Approver = stubApprover{askFn: func(req middleware.AskRequest) middleware.AskResult {
		if req.Question != "选哪个方案？" || len(req.Options) != 2 {
			t.Errorf("AskRequest: %+v", req)
		}
		return middleware.AskResult{Selection: []string{"方案A", "方案B"}}
	}}
	ok, content := callAsk(t, rc, `{"question":"选哪个方案？","options":[{"label":"方案A"},{"label":"方案B"}],"multiple":true}`)
	if !ok {
		t.Fatal("ask_user 应成功")
	}
	if !strings.Contains(content, "选哪个方案？") || !strings.Contains(content, "方案A、方案B") {
		t.Errorf("回答回填: %s", content)
	}
}

// TestAskUserCustom 验证自定义文本回填。
func TestAskUserCustom(t *testing.T) {
	rc := middleware.NewRuntimeContext()
	rc.Approver = stubApprover{askFn: func(middleware.AskRequest) middleware.AskResult {
		return middleware.AskResult{Custom: "两个都要"}
	}}
	ok, content := callAsk(t, rc, `{"question":"选哪个？"}`)
	if !ok {
		t.Fatal("ask_user 应成功")
	}
	if !strings.Contains(content, "自定义: 两个都要") {
		t.Errorf("自定义回答回填: %s", content)
	}
}

// TestAskUserNoApprover 验证无 approver 拒绝。
func TestAskUserNoApprover(t *testing.T) {
	rc := middleware.NewRuntimeContext()
	ok, msg := callAsk(t, rc, `{"question":"x"}`)
	if ok || !strings.Contains(msg, "无法向用户提问") {
		t.Errorf("无 approver 应拒绝，ok=%v msg=%s", ok, msg)
	}
}

// TestAskUserEmptyQuestion 验证空问题拒绝。
func TestAskUserEmptyQuestion(t *testing.T) {
	rc := middleware.NewRuntimeContext()
	rc.Approver = stubApprover{}
	ok, msg := callAsk(t, rc, `{"question":""}`)
	if ok || !strings.Contains(msg, "question 不能为空") {
		t.Errorf("空问题应拒绝，ok=%v msg=%s", ok, msg)
	}
}

// TestAskUserCancelled 验证用户取消（空 AskResult）提示。
func TestAskUserCancelled(t *testing.T) {
	rc := middleware.NewRuntimeContext()
	rc.Approver = stubApprover{}
	ok, content := callAsk(t, rc, `{"question":"x"}`)
	if !ok || !strings.Contains(content, "取消了提问") {
		t.Errorf("取消应成功并提示，ok=%v content=%s", ok, content)
	}
}
