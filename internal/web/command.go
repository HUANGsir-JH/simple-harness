package web

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/subagent"
	"github.com/agent-project/harness/internal/tools"
)

// InputResult 是 POST /api/input 的同步响应（命令结果直接返回，前端无状态）。
type InputResult struct {
	OK       bool         `json:"ok"`
	Kind     string       `json:"kind"` // started | queued | ok | select | compact_started | exit | help
	Message  string       `json:"message,omitempty"`
	Error    string       `json:"error,omitempty"`
	QueueLen int          `json:"queue_len,omitempty"`
	Title    string       `json:"title,omitempty"`
	Items    []SelectItem `json:"items,omitempty"`
	Current  string       `json:"current,omitempty"`
	Command  string       `json:"command,omitempty"` // select 选中后提交的带参命令前缀
}

// SelectItem 是选择弹窗选项（对位 tui popupItem）。
type SelectItem struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

// command 是一条斜杠命令（对位 tui parseCommandLine）。
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

// runCommand 分发斜杠命令（空闲路径；running 时已在 HandleInput 入队）。
// 返回同步结果；涉及状态/会话变更时广播 state_changed（前端重拉）。
func (c *Controller) runCommand(cmd command) InputResult {
	switch cmd.name {
	case "exit":
		return InputResult{OK: true, Kind: "exit"}
	case "help":
		return InputResult{OK: true, Kind: "help"}
	case "switch":
		return c.cmdSwitch(cmd.arg)
	case "model":
		return c.cmdModel(cmd.arg)
	case "effort":
		return c.cmdEffort(cmd.arg)
	case "permission":
		return c.cmdPermission(cmd.arg)
	case "thinking":
		return c.cmdThinking(cmd.arg)
	case "plan":
		return c.cmdPlan(cmd.arg)
	case "rename":
		return c.cmdRename(cmd.arg)
	case "subagents":
		return c.cmdSubagents(cmd.arg)
	case "usage":
		return c.cmdUsage()
	case "compact":
		return c.cmdCompact()
	default:
		return InputResult{OK: false, Error: fmt.Sprintf("unknown command /%s（/help 查看命令）", cmd.name)}
	}
}

// okResult 构造成功结果（系统行由前端追加）并广播状态变更。
func (c *Controller) okResult(message string, stateChanged bool, reason string) InputResult {
	if stateChanged {
		c.broadcastStateChanged(reason)
	}
	return InputResult{OK: true, Kind: "ok", Message: message}
}

// errResult 构造失败结果。
func (c *Controller) errResult(err error) InputResult {
	if err == nil {
		return InputResult{OK: true, Kind: "ok"}
	}
	return InputResult{OK: false, Error: err.Error()}
}

// selectResult 构造选择弹窗结果（items 空 → 错误，对位 TUI sysErr）。
func (c *Controller) selectResult(command, title string, items []SelectItem, current string) InputResult {
	if len(items) == 0 {
		return InputResult{OK: false, Error: fmt.Sprintf("%s 无可用选项", strings.ToLower(title))}
	}
	return InputResult{OK: true, Kind: "select", Title: title, Items: items, Current: current, Command: command}
}

// --- /switch -----------------------------------------------------------------

func (c *Controller) cmdSwitch(arg string) InputResult {
	// 子 agent 只读查看：无参直接回父会话（对位 TUI：/switch 退出查看）。
	if c.IsViewingSubagent() {
		if arg == "" {
			c.ExitSubagentView()
			return InputResult{OK: true, Kind: "ok", Message: "已返回父会话"} // ExitSubagentView 已广播
		}
		// 带参：先退出查看再切换（对位 TUI runCommand viewing 分支）。
		c.ExitSubagentView()
	}
	if arg == "--last" {
		if err := c.SwitchLast(); err != nil {
			return c.errResult(err)
		}
		return c.okResult("已切换到最近会话", true, "switch")
	}
	if arg != "" {
		if err := c.SwitchTo(arg); err != nil {
			return c.errResult(err)
		}
		return c.okResult("已切换会话", true, "switch")
	}
	return c.selectResult("/switch", "Sessions", switchItems(c.Sessions()), c.ActiveID())
}

// --- /model ------------------------------------------------------------------

func (c *Controller) cmdModel(arg string) InputResult {
	if arg != "" {
		if err := c.SetModel(arg); err != nil {
			return c.errResult(err)
		}
		return c.okResult("Model set to "+arg, true, "status")
	}
	return c.selectResult("/model", "Models", modelItems(c.Models()), c.ActiveModel())
}

// --- /effort -----------------------------------------------------------------

func (c *Controller) cmdEffort(arg string) InputResult {
	if arg != "" {
		if err := c.SetEffort(arg); err != nil {
			return c.errResult(err)
		}
		return c.okResult("Effort set to "+arg, true, "status")
	}
	current := ""
	if st := c.ActiveState(); st != nil {
		current = st.ThinkingEffort
	}
	return c.selectResult("/effort", "Reasoning effort", effortItems(c.Efforts()), current)
}

// --- /permission --------------------------------------------------------------

func (c *Controller) cmdPermission(arg string) InputResult {
	if arg != "" {
		if err := c.SetPermission(arg); err != nil {
			return c.errResult(err)
		}
		return c.okResult("Permission set to "+arg, true, "status")
	}
	current := ""
	if st := c.ActiveState(); st != nil {
		current = st.PermissionMode()
	}
	return c.selectResult("/permission", "Permission", permissionItems(c.PermissionModes()), current)
}

// --- /thinking ----------------------------------------------------------------

func (c *Controller) cmdThinking(arg string) InputResult {
	if arg != "" {
		switch arg {
		case "on":
			if err := c.SetThinking(true); err != nil {
				return c.errResult(err)
			}
			return c.okResult("Thinking enabled", true, "status")
		case "off":
			if err := c.SetThinking(false); err != nil {
				return c.errResult(err)
			}
			return c.okResult("Thinking disabled", true, "status")
		default:
			return c.errResult(fmt.Errorf("unknown /thinking arg %q（on|off）", arg))
		}
	}
	current := "on"
	if st := c.ActiveState(); st != nil && st.ThinkingEnabled != nil && !*st.ThinkingEnabled {
		current = "off"
	}
	return c.selectResult("/thinking", "Thinking", thinkingItems(), current)
}

// --- /plan ---------------------------------------------------------------------

func (c *Controller) cmdPlan(arg string) InputResult {
	if arg == "view" {
		content, err := c.PlanContent()
		if err != nil {
			return c.errResult(err)
		}
		if content == "" {
			return c.okResult("暂无计划文件（plan 模式下用 write_plan 写入）", false, "")
		}
		return c.okResult("Plan file  "+c.activePlanFile()+"\n"+content, false, "")
	}
	current := false
	if st := c.ActiveState(); st != nil {
		current = st.IsPlanMode()
	}
	on := !current
	switch arg {
	case "on":
		on = true
	case "off":
		on = false
	case "":
	default:
		return c.errResult(fmt.Errorf("unknown /plan arg %q（on|off|view）", arg))
	}
	if err := c.SetPlanMode(on); err != nil {
		return c.errResult(err)
	}
	if on {
		return c.okResult("Plan 模式已开启（只读规划；/plan view 查看计划）", true, "status")
	}
	return c.okResult("Plan 模式已关闭", true, "status")
}

// --- /rename --------------------------------------------------------------------

func (c *Controller) cmdRename(arg string) InputResult {
	if arg == "" {
		return c.errResult(fmt.Errorf("/rename 需要名称（用法：/rename <名称>）"))
	}
	if err := c.Rename(arg); err != nil {
		return c.errResult(err)
	}
	return c.okResult("Renamed to "+strings.TrimSpace(arg), true, "rename")
}

// --- /subagents ------------------------------------------------------------------

func (c *Controller) cmdSubagents(arg string) InputResult {
	if arg != "" {
		if err := c.ViewSubagent(arg); err != nil {
			return c.errResult(err)
		}
		return c.okResult("正在查看子 agent（只读，/switch 返回）", true, "subagent_view")
	}
	views := c.ListSubagents()
	if len(views) == 0 {
		return c.errResult(fmt.Errorf("当前会话没有子 agent"))
	}
	return c.selectResult("/subagents", "Sub agents", subagentItems(views), "")
}

// --- /usage ----------------------------------------------------------------------

func (c *Controller) cmdUsage() InputResult {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil {
		return c.errResult(fmt.Errorf("无会话（先发一条消息）"))
	}
	st := active.State()
	u := st.UsageTotals()
	parts := []string{fmt.Sprintf("input=%s", fmtTokens(u.InputTokens))}
	if u.CacheReadInputTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache_read=%s", fmtTokens(u.CacheReadInputTokens)))
	}
	if u.CacheCreationInputTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache_creation=%s", fmtTokens(u.CacheCreationInputTokens)))
	}
	parts = append(parts, fmt.Sprintf("output=%s", fmtTokens(u.OutputTokens)))
	if cw := c.ActiveContextWindow(); cw > 0 {
		parts = append(parts, fmt.Sprintf("ctx=%s/%s", fmtTokens(st.CurrentContextTokens()), fmtTokens(int64(cw))))
	}
	return c.okResult("Usage  "+strings.Join(parts, "  "), false, "")
}

// --- /compact ----------------------------------------------------------------------

func (c *Controller) cmdCompact() InputResult {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil {
		return c.errResult(fmt.Errorf("无会话（先发一条消息）"))
	}
	// 审查修复 01 对位：分派**同步**置 running——RunCompact 的 setCancel 在
	// goroutine 内，若不置位，分派后、goroutine 执行前的间隙 completionWake
	// 或用户输入会穿过 isRunning 检查并发启动 run（data race）。
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return InputResult{OK: true, Kind: "queued", QueueLen: len(c.queue) + 1}
	}
	c.running = true
	c.mu.Unlock()
	c.runCompact()
	return InputResult{OK: true, Kind: "compact_started"}
}

// --- 会话/状态命令方法（对位 tui.Controller，加锁） -----------------------------

// Sessions 列出项目会话（/switch 弹窗数据源）。
func (c *Controller) Sessions() []session.SessionInfo {
	list, _ := c.proj.Sessions()
	return list
}

// SwitchTo 打开并切换会话（进程内 resume；未开 → proj.Resume 加入）。
// running 时拒绝（防事件落盘错会话 + open/active 并发写，review H1）。
func (c *Controller) SwitchTo(id string) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("回合进行中，请等待完成后再切换会话")
	}
	if s, ok := c.open[id]; ok {
		c.active = s
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
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
	c.mu.Lock()
	c.open[id] = s
	c.active = s
	c.mu.Unlock()
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

// NewSession 新建会话并切换（POST /api/new）。running 时拒绝。
func (c *Controller) NewSession() error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("回合进行中，请等待完成后再新建会话")
	}
	c.mu.Unlock()
	if err := c.ensureActive(); err != nil {
		return err
	}
	// ensureActive 已创建新会话（active 为 nil 时）；若已有 active，建新会话。
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active != nil {
		s, err := c.newSession()
		if err != nil {
			return fmt.Errorf("创建会话: %w", err)
		}
		c.mu.Lock()
		c.open[s.ID] = s
		c.active = s
		c.mu.Unlock()
	}
	return nil
}

// Models 模型列表（/model 弹窗数据源；当前 provider 的模型）。
func (c *Controller) Models() []string {
	names, err := config.ProviderModels(c.cfg)
	if err != nil {
		return nil
	}
	return names
}

// SetModel 切换会话模型 + 重置档位为模型默认。
func (c *Controller) SetModel(name string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	res, err := config.Resolve(c.cfg, name)
	if err != nil {
		return err
	}
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if err := active.SetModel(res.Model); err != nil {
		return err
	}
	return active.SetThinkingEffort(res.ThinkingEffort)
}

// Efforts 当前模型支持的推理档位（/effort 弹窗数据源）。
func (c *Controller) Efforts() []string {
	if err := c.ensureActive(); err != nil {
		return nil
	}
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil {
		return nil
	}
	res, err := config.Resolve(c.cfg, active.Model())
	if err != nil {
		return nil
	}
	return res.ThinkingEfforts
}

// SetEffort 切换推理档位（校验在模型 efforts 内）。
func (c *Controller) SetEffort(level string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	cur, err := config.Resolve(c.cfg, active.Model())
	if err != nil {
		return err
	}
	if !slices.Contains(cur.ThinkingEfforts, level) {
		return fmt.Errorf("模型 %q 不支持 effort %q（支持: %v）", cur.Model, level, cur.ThinkingEfforts)
	}
	return active.SetThinkingEffort(level)
}

// PermissionModes 审批模式列表（/permission 弹窗数据源）。
func (c *Controller) PermissionModes() []string { return impl.Modes }

// SetPermission 切换会话审批模式（落盘 AgentState）。
func (c *Controller) SetPermission(mode string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	if !slices.Contains(impl.Modes, mode) {
		return fmt.Errorf("未知模式 %q（支持: %v）", mode, impl.Modes)
	}
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	return active.SetPermissionMode(mode)
}

// SetThinking 切换会话 thinking 开关（持久化 AgentState）。
func (c *Controller) SetThinking(enabled bool) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	return active.SetThinkingEnabled(&enabled)
}

// SetPlanMode 切换会话 plan 模式（/plan，ADR-036）。开启时注入一次 plan 指令。
func (c *Controller) SetPlanMode(on bool) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	wasOn := active.State().IsPlanMode()
	if err := active.SetPlanMode(on); err != nil {
		return err
	}
	if on && !wasOn {
		active.AddUser(tools.PlanInstructions)
	}
	return nil
}

// PlanContent 返回当前会话计划文件内容（/plan view）。
func (c *Controller) PlanContent() (string, error) {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil {
		return "", nil
	}
	b, err := os.ReadFile(active.PlanFile())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// activePlanFile 返回当前会话计划文件路径（/plan view 展示用）。
func (c *Controller) activePlanFile() string {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil {
		return ""
	}
	return active.PlanFile()
}

// Rename 重命名当前会话（/rename <名称>）。
func (c *Controller) Rename(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("/rename 需要名称（用法：/rename <名称>）")
	}
	if err := c.ensureActive(); err != nil {
		return err
	}
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	return active.SetName(name)
}

// ActiveModel 返回当前会话模型（未创建时空）。
func (c *Controller) ActiveModel() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return ""
	}
	return c.active.Model()
}

// ActiveID 返回当前会话 id（未创建时空）。
func (c *Controller) ActiveID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return ""
	}
	return c.active.ID
}

// ActiveState 返回当前会话 AgentState（未创建时 nil）。
func (c *Controller) ActiveState() *agentstate.AgentState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return nil
	}
	return c.active.State()
}

// ActiveContextWindow 返回当前会话模型的上下文窗口（token；0 = 解析失败）。
func (c *Controller) ActiveContextWindow() int {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil {
		return 0
	}
	res, err := config.Resolve(c.cfg, active.Model())
	if err != nil {
		return 0
	}
	return res.ContextWindow
}

// --- 选项构建（对位 tui popup.go） ---------------------------------------------

func switchItems(sessions []session.SessionInfo) []SelectItem {
	items := make([]SelectItem, 0, len(sessions))
	for i := len(sessions) - 1; i >= 0; i-- { // 最新在前（TUI switchItems 同款）
		s := sessions[i]
		label := s.Name
		if label == "" {
			label = shortID(s.ID)
		}
		items = append(items, SelectItem{Label: label, Value: s.ID, Description: s.Model})
	}
	return items
}

func modelItems(models []string) []SelectItem {
	items := make([]SelectItem, 0, len(models))
	for _, name := range models {
		items = append(items, SelectItem{Label: name, Value: name})
	}
	return items
}

func effortItems(efforts []string) []SelectItem {
	items := make([]SelectItem, 0, len(efforts))
	for _, effort := range efforts {
		items = append(items, SelectItem{Label: effort, Value: effort})
	}
	return items
}

func permissionItems(modes []string) []SelectItem {
	items := make([]SelectItem, 0, len(modes))
	for _, mode := range modes {
		description := "Ask before operations that need approval"
		switch mode {
		case "readonly":
			description = "Allow reads; ask before changes and commands"
		case "acceptedit":
			description = "Allow workspace edits; ask before risky commands"
		case "bypass":
			description = "Allow all supported operations without prompting"
		}
		items = append(items, SelectItem{Label: mode, Value: mode, Description: description})
	}
	return items
}

func thinkingItems() []SelectItem {
	return []SelectItem{
		{Label: "enabled", Value: "on", Description: "Show model reasoning in the timeline"},
		{Label: "disabled", Value: "off", Description: "Hide reasoning from the timeline"},
	}
}

func subagentItems(views []subagent.EntryView) []SelectItem {
	items := make([]SelectItem, 0, len(views))
	for _, v := range views {
		status := v.Status
		if v.Running {
			status += " · running"
		}
		items = append(items, SelectItem{
			Label:       v.Name + "（" + status + "）",
			Value:       v.ID,
			Description: fmt.Sprintf("%s · depth %d · %s", v.Type, v.Depth, v.ID),
		})
	}
	return items
}

// shortID 短会话 id（时间戳前缀，对位 tui shortSession）。
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// fmtTokens 格式化 token 数（对位 tui：1.0M / 128k / 900）。
func fmtTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
