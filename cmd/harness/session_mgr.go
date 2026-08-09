package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
)

// SessionManager 是 REPL 的会话管理器：开着的会话注册表 + 当前激活会话。
// 无状态 agent（ADR-026）：切换会话 = 换 active（下一轮取新 rc 自动生效）；
// 并行 agent = 每 goroutine 各取一个 rc（阶段五）。
type SessionManager struct {
	app    *App
	a      *agent.Agent
	proj   *session.Project
	open   map[string]*session.Session
	active *session.Session
}

// closeAll flush 所有开着的会话 transcript。
func (m *SessionManager) closeAll() {
	for _, s := range m.open {
		_ = s.Close()
	}
}

// switchTo 打开（若未开）并切换到指定会话 id（进程内 resume 切换）。
func (m *SessionManager) switchTo(id string) error {
	if s, ok := m.open[id]; ok {
		m.active = s
		return nil
	}
	list, err := m.proj.Sessions()
	if err != nil {
		return err
	}
	var info session.SessionInfo
	found := false
	for _, si := range list {
		if si.ID == id {
			info = si
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("会话 %q 不存在（`harness sessions` 查看）", id)
	}
	s, err := m.proj.Resume(info)
	if err != nil {
		return err
	}
	m.open[id] = s
	m.active = s
	return nil
}

// switchLast 打开最新会话并切换。
func (m *SessionManager) switchLast() error {
	info, ok := m.proj.Last()
	if !ok {
		return fmt.Errorf("本项目暂无会话（先 `harness run`）")
	}
	return m.switchTo(info.ID)
}

// replCommand 是一条 REPL 命令（/switch /model /effort）。
type replCommand struct {
	name string
	arg  string
}

// parseCommand 解析 REPL 行；非命令（不以 / 开头）返回 ok=false。
func parseCommand(line string) (replCommand, bool) {
	if !strings.HasPrefix(line, "/") {
		return replCommand{}, false
	}
	fields := strings.Fields(line)
	return replCommand{name: strings.TrimPrefix(fields[0], "/"), arg: strings.Join(fields[1:], " ")}, true
}

// handleCommand 执行一条 REPL 命令。
func (m *SessionManager) handleCommand(cmd replCommand) error {
	switch cmd.name {
	case "switch":
		if cmd.arg == "--last" {
			return m.switchLast()
		}
		if cmd.arg == "" {
			return fmt.Errorf("usage: /switch <id> 或 /switch --last（`harness sessions` 查看 id）")
		}
		return m.switchTo(cmd.arg)
	case "model":
		if cmd.arg == "" {
			return fmt.Errorf("usage: /model <name>")
		}
		res, err := provider.Resolve(m.app.Config, cmd.arg)
		if err != nil {
			return err
		}
		if err := m.active.SetModel(res.Model); err != nil {
			return err
		}
		// 新模型重置档位为模型默认（保证 effort 在新模型 efforts 内）。
		if err := m.active.SetThinkingEffort(res.ThinkingEffort); err != nil {
			return err
		}
		fmt.Printf("已切换模型 %s（effort %s）\n", res.Model, res.ThinkingEffort)
		return nil
	case "effort":
		if cmd.arg == "" {
			return fmt.Errorf("usage: /effort <low|high|max>")
		}
		cur, err := provider.Resolve(m.app.Config, m.active.Model())
		if err != nil {
			return err
		}
		if !slices.Contains(cur.ThinkingEfforts, cmd.arg) {
			return fmt.Errorf("模型 %q 不支持 effort %q（支持: %v）", cur.Model, cmd.arg, cur.ThinkingEfforts)
		}
		if err := m.active.SetThinkingEffort(cmd.arg); err != nil {
			return err
		}
		fmt.Printf("已切换 effort %s\n", cmd.arg)
		return nil
	case "permission":
		if cmd.arg == "" {
			return fmt.Errorf("usage: /permission <readonly|acceptedit|bypass>")
		}
		if !slices.Contains(impl.Modes, cmd.arg) {
			return fmt.Errorf("未知模式 %q（支持: readonly / acceptedit / bypass）", cmd.arg)
		}
		if err := m.active.SetPermissionMode(cmd.arg); err != nil {
			return err
		}
		fmt.Printf("已切换审批模式 %s\n", cmd.arg)
		return nil
	default:
		return fmt.Errorf("未知命令 /%s（支持: /switch /model /effort /permission）", cmd.name)
	}
}
