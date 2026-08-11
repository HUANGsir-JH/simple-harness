package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
)

// newLazyController 构造 active=nil（懒加载）的桥：新入口不预创建会话，
// newSession factory 首动作触发创建。FakeClient 返回固定纯文本回合。
func newLazyController(t *testing.T) (*Controller, *session.Project) {
	t.Helper()
	root := t.TempDir()
	store := session.NewAt(root)
	proj := &session.Project{Path: root, Dir: store.ProjectDir(root)}
	client := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDelta, Text: "回复"},
			{Type: provider.EventTextDone, Text: "回复"},
			{Type: provider.EventDone},
		}), nil
	}}
	c := NewController(agent.New(client, "test-model"), proj, config.Config{}, nil,
		func() (*session.Session, error) { return proj.Create("test-model", root, "") },
		context.Background())
	t.Cleanup(c.CloseAll)
	return c, proj
}

// TestLazyNoSessionOnEntry 验证懒加载：进入 TUI 不创建 session（disk 无会话）。
func TestLazyNoSessionOnEntry(t *testing.T) {
	c, proj := newLazyController(t)
	if c.active != nil {
		t.Fatal("懒加载：进入时 active 应为 nil")
	}
	if list, _ := proj.Sessions(); len(list) != 0 {
		t.Fatalf("进入时不应创建 session，disk 有 %d 个", len(list))
	}
}

// TestLazyCreateOnFirstMessage 验证首条消息触发创建 + 落盘（解决 /exit 残留
// 空 session：无消息不建会话）。
func TestLazyCreateOnFirstMessage(t *testing.T) {
	c, proj := newLazyController(t)
	msg := c.Run("hello world")()
	if m, ok := msg.(runDoneMsg); !ok || m.err != nil {
		t.Fatalf("Run 结果: %+v", msg)
	}
	if c.active == nil {
		t.Fatal("首消息后应创建 active 会话")
	}
	if list, _ := proj.Sessions(); len(list) != 1 {
		t.Fatalf("首消息后应创建 1 个 session，disk 有 %d 个", len(list))
	}
}

// TestFirstMessageNaming 验证首消息自动命名（codex first_user_message 同款）：
// name = 首行预览，落盘 agentstate。
func TestFirstMessageNaming(t *testing.T) {
	c, _ := newLazyController(t)
	msg := c.Run("修复登录 bug 的用户名验证")()
	if m, ok := msg.(runDoneMsg); !ok || m.err != nil {
		t.Fatalf("Run 结果: %+v", msg)
	}
	if got := c.active.Name(); got != "修复登录 bug 的用户名验证" {
		t.Errorf("首消息命名: got %q", got)
	}
	st, err := agentstate.LoadFile(c.active.StatePath())
	if err != nil || st.Name != "修复登录 bug 的用户名验证" {
		t.Errorf("命名未落盘: %+v err=%v", st, err)
	}
}

// TestRenamePersists 验证 /rename：懒加载下先创建再命名，落盘 agentstate。
func TestRenamePersists(t *testing.T) {
	c, _ := newLazyController(t)
	if c.active != nil {
		t.Fatal("懒加载：进入时 active 应为 nil")
	}
	if err := c.Rename("我的任务"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if c.active.Name() != "我的任务" {
		t.Errorf("active name: %q", c.active.Name())
	}
	st, err := agentstate.LoadFile(c.active.StatePath())
	if err != nil || st.Name != "我的任务" {
		t.Errorf("改名未落盘: %+v err=%v", st, err)
	}
}

// TestRenameEmptyRejected 验证 /rename 空名报错（不创建会话）。
func TestRenameEmptyRejected(t *testing.T) {
	c, proj := newLazyController(t)
	if err := c.Rename("  "); err == nil {
		t.Fatal("空名应报错")
	}
	if c.active != nil {
		t.Error("空名不应触发会话创建")
	}
	if list, _ := proj.Sessions(); len(list) != 0 {
		t.Errorf("空名不应创建 session，disk 有 %d 个", len(list))
	}
}

// TestStateCommandEnsureActive 验证状态命令（/thinking 等）无 active 时先创建
// 会话再执行（用户决策：状态命令也触发创建）。
func TestStateCommandEnsureActive(t *testing.T) {
	c, _ := newLazyController(t)
	if c.active != nil {
		t.Fatal("懒加载：进入时 active 应为 nil")
	}
	if err := c.SetThinking(false); err != nil {
		t.Fatalf("SetThinking: %v", err)
	}
	if c.active == nil {
		t.Fatal("状态命令应触发会话创建")
	}
	st, _ := agentstate.LoadFile(c.active.StatePath())
	if st.ThinkingEnabled == nil || *st.ThinkingEnabled {
		t.Errorf("thinking 切换未落盘: %+v", st.ThinkingEnabled)
	}
}

// TestSwitchItemsName 验证 /switch 弹窗列表：label = name，未命名短 ID 兜底。
func TestSwitchItemsName(t *testing.T) {
	items := switchItems([]session.SessionInfo{
		{ID: "20260808T100001-aaaaaaaa", Name: "修复 bug"},
		{ID: "20260808T100002-bbbbbbbb", Name: ""},
	})
	// switchItems 倒序：items[0] 是最新（未命名），items[1] 是旧的（已命名）。
	if items[0].label != shortSession("20260808T100002-bbbbbbbb") {
		t.Errorf("未命名应短 ID 兜底: got %q", items[0].label)
	}
	if items[0].value != "20260808T100002-bbbbbbbb" {
		t.Errorf("value 应保持 ID: got %q", items[0].value)
	}
	if items[1].label != "修复 bug" {
		t.Errorf("已命名应显示 name: got %q", items[1].label)
	}
	if items[1].value != "20260808T100001-aaaaaaaa" {
		t.Errorf("value 应保持 ID: got %q", items[1].value)
	}
}

// TestFirstLinePreview 验证默认名截断：首行、TrimSpace、>40 rune 截断 + "..."。
func TestFirstLinePreview(t *testing.T) {
	if got := firstLinePreview("  hello\nworld  "); got != "hello" {
		t.Errorf("多行取首行: got %q", got)
	}
	long := strings.Repeat("x", 60)
	if got := firstLinePreview(long); got != strings.Repeat("x", 40)+"..." {
		t.Errorf("长消息应截 40 + ...: got %d runes", len([]rune(got)))
	}
	if got := firstLinePreview("   "); got != "" {
		t.Errorf("纯空白应返回空: got %q", got)
	}
}
