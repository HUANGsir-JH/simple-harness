package tui

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/tools"
)

// command 是一条斜杠命令（/switch /model /effort /permission /help /exit）。
type command struct {
	name string
	arg  string
}

// parseCommandLine 解析输入行；非 / 开头返回 ok=false。
func parseCommandLine(line string) (command, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return command{}, false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return command{}, false
	}
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

// Models 模型列表（/model 弹窗数据源；只列当前 provider（default_provider）的
// 模型——config.Resolve 只在该 provider 内查找，列出跨 provider 模型会选不中，
// Bug05，2026-08-10）。
func (c *Controller) Models() []string {
	names, err := config.ProviderModels(c.cfg)
	if err != nil {
		return nil
	}
	return names
}

// ActiveContextWindow 返回当前会话模型的上下文窗口（token；ADR-037 footer
// 实时上下文占用用）。无 active / 解析失败返回 0。纯计算（config.Resolve），
// 每次调用可接受；模型经 /model 切换后自然取新值。
func (c *Controller) ActiveContextWindow() int {
	if c.active == nil {
		return 0
	}
	res, err := config.Resolve(c.cfg, c.active.Model())
	if err != nil {
		return 0
	}
	return res.ContextWindow
}

// SetModel 切换会话模型 + 重置档位为模型默认（ADR-026 运行时切换）。
// 懒加载：无 active 时先创建会话（用户决策：状态命令也触发创建）。
func (c *Controller) SetModel(name string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	res, err := config.Resolve(c.cfg, name)
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
	res, err := config.Resolve(c.cfg, c.active.Model())
	if err != nil {
		return nil
	}
	return res.ThinkingEfforts
}

// SetEffort 切换推理档位（校验在模型 efforts 内）。懒加载：无 active 先创建。
func (c *Controller) SetEffort(level string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	cur, err := config.Resolve(c.cfg, c.active.Model())
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
// 懒加载：无 active 先创建。
func (c *Controller) SetPermission(mode string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	if !slices.Contains(impl.Modes, mode) {
		return fmt.Errorf("未知模式 %q（支持: %v）", mode, impl.Modes)
	}
	return c.active.SetPermissionMode(mode)
}

// SetThinking 切换会话 thinking 开关（/thinking，持久化 AgentState；
// 2026-08-10 删配置 enabled，开关纯会话级，默认开启）。懒加载：无 active 先创建。
func (c *Controller) SetThinking(enabled bool) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	return c.active.SetThinkingEnabled(&enabled)
}

// SetPlanMode 切换会话 plan 模式（/plan，ADR-036）。开启时注入一次 plan 指令
// （持久化到 conversation + transcript，进入点注入；off→on 才注入，避免重复）。
// 懒加载：无 active 先创建。
func (c *Controller) SetPlanMode(on bool) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	wasOn := c.active.State().IsPlanMode()
	if err := c.active.SetPlanMode(on); err != nil {
		return err
	}
	if on && !wasOn {
		c.active.AddUser(tools.PlanInstructions)
	}
	return nil
}

// PlanContent 返回当前会话计划文件内容（/plan view；无计划文件返回空串 nil）。
func (c *Controller) PlanContent() (string, error) {
	if c.active == nil {
		return "", nil
	}
	b, err := os.ReadFile(c.active.PlanFile())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// Rename 重命名当前会话（/rename <名称>；懒加载：未创建则先创建再命名）。
func (c *Controller) Rename(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("/rename 需要名称（用法：/rename <名称>）")
	}
	if err := c.ensureActive(); err != nil {
		return err
	}
	return c.active.SetName(name)
}

// CloseAll flush 所有打开的会话 transcript。
func (c *Controller) CloseAll() {
	for _, s := range c.open {
		_ = s.Close()
	}
}
