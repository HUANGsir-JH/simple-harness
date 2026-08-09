package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
)

// command 是一条斜杠命令（/switch /model /effort /permission /help /exit）。
type command struct {
	name string
	arg  string
}

// parseCommandLine 解析输入行；非 / 开头返回 ok=false。
func parseCommandLine(line string) (command, bool) {
	if !strings.HasPrefix(line, "/") {
		return command{}, false
	}
	fields := strings.Fields(line)
	return command{name: strings.TrimPrefix(fields[0], "/"), arg: strings.Join(fields[1:], " ")}, true
}

// Sessions 列出项目会话（/switch 弹窗数据源）。
func (c *Controller) Sessions() []session.SessionInfo {
	list, _ := c.proj.Sessions()
	return list
}

// SwitchTo 打开并切换会话（进程内 resume；未开 → proj.Resume 加入）。
func (c *Controller) SwitchTo(id string) error {
	if s, ok := c.open[id]; ok {
		c.active = s
		return nil
	}
	list, err := c.proj.Sessions()
	if err != nil {
		return err
	}
	var info session.SessionInfo
	for _, si := range list {
		if si.ID == id {
			info = si
			break
		}
	}
	if info.ID == "" {
		return fmt.Errorf("会话 %q 不存在", id)
	}
	s, err := c.proj.Resume(info)
	if err != nil {
		return err
	}
	c.open[id] = s
	c.active = s
	return nil
}

// SwitchLast 切换最新会话。
func (c *Controller) SwitchLast() error {
	info, ok := c.proj.Last()
	if !ok {
		return fmt.Errorf("本项目暂无会话（先 `harness run`）")
	}
	return c.SwitchTo(info.ID)
}

// Models 模型列表（/model 弹窗数据源；从配置收集，实时获取非硬编码）。
func (c *Controller) Models() []string {
	seen := map[string]bool{}
	var out []string
	for _, pc := range c.cfg.Providers {
		for name := range pc.Models {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// SetModel 切换会话模型 + 重置档位为模型默认（ADR-026 运行时切换）。
func (c *Controller) SetModel(name string) error {
	res, err := provider.Resolve(c.cfg, name)
	if err != nil {
		return err
	}
	if err := c.active.SetModel(res.Model); err != nil {
		return err
	}
	return c.active.SetThinkingEffort(res.ThinkingEffort)
}

// Efforts 当前模型支持的推理档位（/effort 弹窗数据源，实时解析）。
func (c *Controller) Efforts() []string {
	res, err := provider.Resolve(c.cfg, c.active.Model())
	if err != nil {
		return nil
	}
	return res.ThinkingEfforts
}

// SetEffort 切换推理档位（校验在模型 efforts 内）。
func (c *Controller) SetEffort(level string) error {
	cur, err := provider.Resolve(c.cfg, c.active.Model())
	if err != nil {
		return err
	}
	if !slices.Contains(cur.ThinkingEfforts, level) {
		return fmt.Errorf("模型 %q 不支持 effort %q（支持: %v）", cur.Model, level, cur.ThinkingEfforts)
	}
	return c.active.SetThinkingEffort(level)
}

// PermissionModes 审批模式列表（/permission 弹窗数据源）。
func (c *Controller) PermissionModes() []string { return impl.Modes }

// SetPermission 切换会话审批模式（落盘 AgentState，ADR-029）。
func (c *Controller) SetPermission(mode string) error {
	if !slices.Contains(impl.Modes, mode) {
		return fmt.Errorf("未知模式 %q（支持: %v）", mode, impl.Modes)
	}
	return c.active.SetPermissionMode(mode)
}

// CloseAll flush 所有打开的会话 transcript。
func (c *Controller) CloseAll() {
	for _, s := range c.open {
		_ = s.Close()
	}
}
