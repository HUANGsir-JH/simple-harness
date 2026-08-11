# 开发日志

> 按日期追加，最新在上。记录：进展、阻塞、问题、经验。

## 2026-08-11

### Bug10：Esc 中断后发消息 400（tool_use 无紧跟 tool_result）✅

- **用户报告**：TUI 里工具执行中按 Esc 中断，再发消息报 `POST .../messages: 400 invalid_request_error`——`tool_use` 无对应 `tool_result`。
- **根因**（双处叠加）：
  1. **agent 层不补全**：`runToolBatch` 在工具批被中断（ctx canceled）时 `results` 为空 → `blocks` 为空 → 不追加 tool_result。assistant 已带 tool_calls 落盘，conversation/transcript 留下未配对 `tool_use`，下一轮采样违反 anthropic 邻接约束（ADR-024）。
  2. **TUI 时机错**：`requestInterrupt` 在 Esc 瞬间立即 `AddUser(中断提示)`——在 Run goroutine 补 tool_result 之前就插入 user 消息（且并发写无锁 conversation），`tool_use` 与 `tool_result` 之间夹了 user，即使补全顺序也错。
- **修复**：
  - **agent `runToolBatch` 补全配对**：为 results 缺失的调用回填 `"工具未执行（回合被中断）"` 块（conversation + emit 落盘 transcript 双轨一致，C6 时序）。
  - **TUI 挪中断提示时机**：`requestInterrupt` 不再 AddUser，改在 `handleRunDone`（Run 返回后，tool_result 已补全）AddUser——顺序 `tool_use → tool_result → user(System)` 合法，且消除并发写竞态。
  - **采样前自愈兜底**：`agent.Run` 每轮采样前 `repairDanglingToolUse`——覆盖**存量损坏会话**（resume 修复前中断产生的会话也能恢复）。
- **二修（用户续报，切会话后仍 400）**：存量 transcript 里 `user(中断提示)` 被旧版插进 tool_result **中间**（transcript 实测：tool_result(call_01) → user → tool_result(call_00)），resume 重建后 assistant 的 result 被 user 隔开、散成多条 tool 消息。首版 `repairDanglingToolUse` 把缺失块**新建一条 tool 消息**插到前面，导致紧邻消息缺 call_01 → 仍 400（报 `call_01 without tool_result immediately after`）。**改进**：收集 assistant 后全部 result（含被 user 隔开的散落 result，去重），缺失补"未执行"，重建为 `assistant → 一条完整 tool（全部 result，保留真实值）→ after 非 tool 消息（原顺序）`；完整紧邻配对时 no-op。
- **测试**：agent（`TestRunToolBatchInterruptedRepairsPairing` 中断补全 / `TestRepairDanglingToolUse` / `TestRepairDanglingInterleaved` 交错重建 / `TestRunRepairsDanglingToolUse` 存量自愈）+ TUI（`TestEscInterrupt` 改断言 + `TestInterruptPromptAddedOnRunDone`）。`go build/vet/test ./...` + `-race` 全绿。
- **经验**：anthropic 邻接约束（tool_use 后下一条消息必须含全部 tool_result，ADR-024）在中断/错误路径上也会被违反——凡 assistant 带 tool_calls 落盘，必须保证 tool_result **紧邻且完整**（缺一块、或隔了 user、或拆成多条 tool 消息都不行）；中断提示这类 AddUser 不能抢在工具批回填之前插消息。修复"缺 result"时不能新建 tool 消息插到已有 tool 消息之前——会拆散紧邻的完整配对，正确做法是合并进紧邻消息。

### Plan Mode（规划模式）✅ 版本 0.7.0（ADR-036）

- **功能**：会话级 plan 模式——先只读调研、产出计划文件、批准后执行。4 工具：`plan_enter`（模型自主提议进规划，HITL 确认）/ `write_plan`（写 `<会话>/plans/plan.md`，路径+全文回填）/ `plan_done`（弹 HITL 交接：批准执行 / 继续规划 / **Other=拒绝+反馈回填**，bypass 也询问）/ `ask_user`（通用提问，选项+Other+单选多选）。
- **设计要点**（规划期与用户 5 轮逐点确认）：
  - **复用 Approver 扩展 `Ask`**：`middleware.Approver` 增 `Ask(ctx, AskRequest) (AskResult, error)`——不新开接口/rc 字段（用户指出"原本的 approver 不可以用吗"）；TUI `c.send` 桥 / run `ChannelApprover` 补 Ask。
  - **只读强制 = 可见但拒绝**（不做工具过滤）：codex 不过滤（靠 sandbox）、opencode 权限 deny；我们 `Decide` plan 分支直接 Deny，被拒有明确反馈。
  - **plan 指令进入点持久化单次注入**：`/plan on` 经 `session.AddUser(PlanInstructions)`、`plan_enter` 批准后随 tool_result——均落盘、只注入一次，无 per-round middleware（前缀缓存）。
  - **`isPlanReadonlyShell`**：plan 模式 shell 放宽管道（`grep foo | head` 放行），保留危险黑名单 + 拒 `>` 写重定向，按 `| && ;` 拆段逐段校验只读白名单。
  - **不做合成消息**（anthropic tool_use→tool_result 邻接约束，ADR-024）。
- **TUI**：`/plan` 纯切换 + `/plan view` 读计划文件 + 状态栏 `[PLAN]` + ask 弹窗（↑/↓ 选项、Space 多选、打字=Other 自定义、Enter 提交、Esc 取消）。
- **验证**：单测（4 工具行为 / Decide plan 分支 / isPlanReadonlyShell / ask 弹窗 / /plan 命令 / agent 集成闭环）+ e2e（plan 模式闭环）；`go build/vet/test ./...` 全绿；已 `go install`。
- **经验**：tea.KeySpace 的 `String()` 返回 `" "` 而非 `"space"`（bubbletea 特例，handleTimelineKey 的 `case "space"` 是既有 latent bug）；`Approve` 接口扩展迫使所有实现（2 生产 + 3 mock）补 Ask——有界 ripple。

### TUI 输入框修复：粘贴拆条排查 + 高度动态（1→5 行 + 滚动）

- **用户报告**：复制多行文字粘贴进输入框，被拆成多条消息塞进 queue。
- **根因排查**：bubbletea v1.3.10 **无 `tea.PasteMsg` 类型**（v1.x 移除），bracketed paste 解析为单个 `Key{Type:KeyRunes, Paste:true}`（key_sequences.go），整段含 \n 进 textarea——**代码路径正确**（验证测试 `TestPasteMultiLineSingleMessage` 证明多行粘贴是一条完整消息）。拆条只发生在 **bracketed paste 失效** 的终端（VSCode 集成终端 + conpty 输入转发对 \x1b[200~ 标记透传不稳定）→ 粘贴的 \n 被当 Enter 键 → `handleComposerKey` 每个 Enter submit → 拆条。保持 Enter=提交 下无代码可修（终端环境限制），治本需改 Enter 语义（用户嫌 Ctrl+Enter 别扭，暂不改）。
- **高度修复（已做）**：`updateComposerHeight()` 让 textarea 高度随内容行数动态增长——默认 1 行（minHeight=1 可行）、每显式换行高一行、至多 5 行（超出 textarea 内部跟随光标滚动）；layout() 不再固定 SetHeight(2/3)；换行/键入/粘贴/历史/补全/submit Reset 后都更新高度。测试 `TestComposerHeightGrows`。
- **验证**：`go build/vet/test ./...` 全绿（含 e2e）；已 `go install`。

### A2：session→agent 依赖倒置（agent.Event 下沉 internal/events）✅

- **问题**：`session/transcript.go`、`session/session.go` import `internal/agent` 只为了拿 `agent.Event` 落盘——存储层反向依赖编排层（agent 的传递闭包含 provider/tools/middleware）。非运行 bug、无环，是分层倒置：transcript 无法持久化非 agent 来源事件；agent 新增事件类型时 session 的 switch 靠 default 静默丢弃（C2 同款风险的类型层版本）。
- **决策**（用户逐点确认）：新建 `internal/events` 包（事件是回合生命周期概念，非消息数据，不进 messages）；全仓迁移 `events.*`（不保留 agent 别名）。
- **`internal/events` 新建**：`EventType/Event`（含 `*messages.ToolCall/ToolResult`）+ 9 常量 + `OnEvent`，从 agent.go 整体搬移。
- **全仓 12 文件迁移**：agent（agent.go + 2 测试）、session（transcript/session + 测试）、ui/render、ui/tui（controller + events + model + 3 测试）、cmd/run。`agent.Event*` → `events.Event*`；`agent.OnEvent` → `events.OnEvent`（Run/sample/runToolBatch 签名）。仅 controller/run 仍保留 `agent` import（用 `*agent.Agent`/`agent.Build`），其余文件 agent import 全删。
- **验证**：`go build/vet/test ./...` 全绿（含 e2e）；grep 确认 session 无 agent 依赖、agent 无 session 依赖（无环）；无残留 `agent.Event`。

### Session name + 重命名 & 懒加载（2026-08-11）

- **背景**（用户提出两个优化）：① 会话只有时间戳-hex ID，/switch 弹窗裸列 ID、sessions 列 `ID model= updated=`，无法区分；② `repl()` 在 RunTUI 前 eager `CreateInCWD`——用户 /exit 或进入后 /switch 到旧会话 → 残留空 session（resume 用 proj.Resume 加载从不创建，已确认）。
- **决策**（用户逐点确认）：默认名 = 首条用户消息前 ~40 字（codex first_user_message 同款，空则短 ID 兜底）；状态命令（/model 等）无 active 时也触发创建；只防增量（存量空会话不清理）；`/rename <名称>` 带参命令（不做弹窗——改名是自由文本，现有弹窗全是选项列表，无文本输入弹窗组件）。
- **name 落点 = AgentState**（可变小快照，`Set* + SaveFile` 模式现成；不放 transcript meta 行——追加式文件原地改名麻烦）：`agentstate` 加 `Name`；`Session.Name()/SetName()`；`SessionInfo` 加 `Name/Model/UpdatedAt`，`Sessions()` 遍历时一次 LoadFile 填充（~200B/文件、N 个微秒级，顺带消掉 sessionsCmd 二次读）。
- **懒加载**：`repl()` 删 CreateInCWD 改传 factory；`RunTUI/NewController` 加 `newSession` 参数（sess 可 nil = 新入口 / 非 nil = resume）；`Controller.ensureActive()` 首条消息或状态命令时创建；`Controller.Run` 首消息自动命名（SetName 落盘）；nil-safe helpers（ActiveID/ActiveModel/ActiveState/AddCommand 无会话 no-op）；弹窗 current 读取改走 helpers（不因弹窗打开就创建，创建发生在 confirm）。
- **TUI 展示**：header 右端展示优先级 name（截 24）→ 短 ID → 灰色"新会话"占位（懒加载未创建）；/switch 弹窗 label = name || 短 ID；`sessions` 命令显示 name 列。
- **测试**：lazy_test.go 8 例（进入无会话/首消息创建/首消息命名落盘/rename 落盘/空名拒绝/状态命令触发/switchItems name/firstLinePreview 截断）+ session SetName/Sessions 填充。
- **验证**：`go build/vet/test ./...` 全绿（含 e2e）；已 `go install ./cmd/harness`。

### C4 + B4：工具 schema 单一来源（jsonschema 生成）+ parseArgs 泛型 ✅

- **背景**：7 个工具每份 schema 以手写 JSON 字符串声明在 `Spec()`，与 Handle 的 Go struct 双份靠 review 同步（改字段漏改 schema 不报错）；`anthropic_messages.go:114` 静默吞 schema 语法错（坏 schema 发空 properties 给模型）。
- **决策**（用户逐点确认）：引入 `invopop/jsonschema`（v0.14.0 已是 SDK 间接依赖，提升为直接依赖）从 Go struct 注解生成 schema 消双份；同批合并 B4（`parseArgs[T]` 泛型）；缓存用泛型 helper `schemaOf[T]()`（sync.Map 按类型，Reflector.Reflect 每次本地 definitions 并发安全）；`:114` 吞错改运行时 hard error。
- **`tools/args.go` 新建**：`schemaOf[T]()` + `parseArgs[T]()`。`Reflector{DoNotReference, Anonymous, AllowAdditionalProperties}`——DoNotReference 让顶层直接输出 properties/required（不包 `$ref/$defs`，正好是 anthropic `toAnthropicInputSchema` 从顶层读的形状；**默认输出 `$ref` 包裹会变空 schema，必须开**）；Anonymous 去包路径派生的 `$id`；AllowAdditionalProperties 保持与手写 schema 一致（不新增 additionalProperties:false）。
- **7 工具 struct 具名**：readFileArgs/writeFileArgs/listDirArgs/globArgs/applyPatchArgs/shellCommandArgs/updateTodoArgs（原 todoArgs 改名对齐）+ todoItem 具名；字段加 `jsonschema` 标签（description/enum），可选字段靠 `json omitempty`（jsonschema 默认非 omitempty=required，与手写 required 完全对齐）。Handle 全换 `parseArgs[T]("tool", args)`，每处 -7 行样板。
- **provider 修复**：`toAnthropicTools` 返回 error、`Stream` 传播——自定义工具手写坏 schema 采样时报错（fail-loud）而非静默发空。
- **测试**：TestParseArgs / TestBuiltinSchemasValid（7 工具 required 断言）/ TestTodoStatusEnum / TestSchemaOfCaches。
- **验证**：`go build/vet/test ./...` 全绿（含 e2e）；已 `go install ./cmd/harness`。

## 2026-08-10

### 架构审查 10 项缺陷修复（ADR-034/035，Bug01-09 ✅）

- **来源**：`docs/reviews/architecture-review-2026-08-10.html`（13,147 行全仓审查，10 项经可执行探针证实）。逐 bug 与用户讨论修复方向，每个单独提交。
- **已完成**：Bug01 e2e SendLine→sendKeys(CR)；Bug02 shell 白名单前拒元字符 + find 危险参数；Bug04 删 thinking.enabled 默认开启 + /thinking 命令（ADR-034）；Bug05 Models() 只列当前 provider；Bug06 writer 写后关 panic + shell 杀进程组（POSIX Setpgid，Windows no-op）；Bug07 apply_patch 歧义检测 + @@ 定位 + 两阶段事务；Bug08 resume 读侧跳过坏行；Bug09 RunTUI 等 run goroutine 退出。
- **Bug03（最大块，ADR-035）**：
  - `tools/workspace.go` 新增 `ResolvePath/InWorkspace/ResolveInWorkspace`：相对路径以 `state.CWD`（会话启动目录）为基解析为绝对，**软边界**（只规范化不拒绝，越界交审批）。5 个文件工具接入，`state.CWD` 死字段复活；`applyPatch` 加 ws 参数逐路径解析。
  - `Decide` 参数感知：加 ws 参数，`action{class, targets}`；classRead 范围内 Allow/越界 Ask、classEdit 越界 Ask/范围内按模式、apply_patch 提取全部文件路径（任一越界 Ask）；bypass 不受限（用户显式确认）。
  - `ApprovalKey` 拆多 key：文件工具 `<tool>:<绝对路径>`，全部命中 approved 才 Allow（对齐 opencode multi-pattern）；批准"本会话记住"全部写入 AgentState。
  - `EvictContent/MaxOutputChars` 下沉 tools 包：断 tools→impl 反向依赖环（#105 前置项）。
- **验证**：`go build/vet/test ./...` 全绿（含 e2e/TUI 强制重跑）；新增 policy 边界测试 20+ 用例、workspace 单测、工具 CWD 解析测试；shell 超时落盘测试修 flaky（PowerShell 冷启动放宽 timeout）。
- **Bug10（overlay tagged union）**：TUI 三个覆盖层字段 appr/sel/help 可并存（审批未决时 runDone 消费队列命令叠开第二层弹窗，help 被遮、答完浮现）。收成 `ovl *overlay` 单字段（kind=approval/select/help）+ `openOverlay` 叠开守卫，非法组合类型层面不可表达；新增 `TestOverlayMutuallyExclusive` 回归。已 install。
- **C1-C6（审查"其余已核实问题"批量，2026-08-10）**：
  - C1 AgentState 原子写临时名带 pid + fsync（两进程 resume 互踩、断电丢内容）
  - C2 transcript 行类型抽 `LineType*` 常量统一三处（写/读/TUI 渲染），load 的 switch 加 default 跳过未知类型
  - C3 resume 复用段续接 ordinal/turn，新增 `TestResumeContinuesOrdinal`
  - C5 thinking 不重放测试（`TestToAnthropicMessagesStripsThinking`）
  - C6 双轨审计时序契约：agent.go/tool_output.go 注释声明 + `TestEmitBeforeTruncation` 锁定
  - **C4 完成**（2026-08-11，见顶部段）：工具 schema 双份声明 → `invopop/jsonschema` 从 Go struct 注解生成 schema 消双份；`:114` 静默吞错改 hard error。

## 2026-08-09

### 配置层独立 + 进程级装配根 ✅（ADR-033）

- **配置域拆出 provider**：新增 `internal/config`（最底层，只依赖 yaml+stdlib）——`Config/ProviderSpec/Model/Thinking/ApprovalConfig`（YAML 定义）+ `ProviderConfig`（解析后生效扁平结构，`Resolved` 更名）+ `LoadConfig` + `Resolve` + `Validate` + Effort/Default* 常量，从 provider 整体迁出（含 config_load/resolve/validate 测试）。provider 回归 ADR-022 的"单 anthropic wire"定位（ToolSpec/Request/Client/Event + `NewClient(*config.ProviderConfig)`）。
- **新增 `internal/app`**（进程级装配根，惰性单例）：`App{Config, Provider}` + `Load()/LoadFrom()/DefaultApprovalMode()/ResolveFlags()`，替代 cmd 的 `defaultApp/loadApp/buildAgent/resolveFlags`；未来 client/agent/subagent 工厂作为字段挂这（config 只是其一）。
- **buildAgent 下沉**：`internal/agent.Build(res *config.ProviderConfig, defaultMode string)`（client + 内置工具 + 标准中间件链），cmd 薄化为 `app.Load()` + `agent.Build()`。为 subagent 提供不同装配铺路（无状态可共享，ADR-026）。
- **改动**：删 `cmd/harness/runtime.go/build.go/runtime_test.go`；tui/cmd/provider 引用 `provider.Config/Resolved` → `config.*`（Resolved → ProviderConfig、原 ProviderConfig → ProviderSpec）；agent 测试 `provider.EffortMax` → `config.EffortMax`。
- **验证**：`go build ./...` + `go vet ./...` + `go test ./...`（含 e2e 全通过）；新 app/config 包测试固化 Load 单例契约与 ResolveFlags 校验。

### TUI 折叠块点击与弹窗宽度修复 ✅（ADR-032）

- **折叠块点击错配**（用户实测反馈）：根因是 `renderTimeline` 行号会计 off-by-one（cell 后只有一个空分隔行却按 +2 累加，块越多偏移越大）+ assistant 消息 hit 区间覆盖整个 cell。改为 `line += height + 1`，并让 `renderMessageItem` 返回 `messageCell{body, thinkingStart, thinkingEnd}`，只注册 thinking 块本身为点击区间。
- **弹窗正文折行**（用户截图：`/model` 选项被拆成两行）：根因是 `modalStyle` 的 `Width` 含 padding 不含 border，实际内容宽比调用方假设的少 4 列。新增 `modalPanelWidth`/`modalInnerWidth` 作为唯一来源，选择器/审批/帮助三个弹窗统一取值；审批提示与帮助双列改为按可用宽度自适应。
- **测试**：`TestHitRangesAlignWithRenderedLines`（断言 hit.start 行即块标题行、相邻区间不重叠）、`TestModalsFitPanelWidth`（6 种屏宽下断言弹窗每行宽度与总行数）；旧测试里补偿 off-by-one 的 `row++` 已删除。两个测试均已验证能在还原旧算法时失败。
- **验证**：`go build ./...`、`go test ./...`、`go test -race ./internal/ui/tui`、`go vet ./...`、TUI e2e 四例、`go install ./cmd/harness` 全通过。

### TUI redesign pass（`feat/tui-redesign`）

- 从 `57eefe0` 建立独立分支，保留已验证的 agent/controller 协议，重做 UI 状态组织与视觉层。
- 消息、工具、系统行统一为 timeline；resume 优先按 transcript ordinal 重建 command/thinking/tool 顺序，旧会话保留 conversation fallback。
- 固定 header/main/auxiliary/composer/footer 布局，ASCII 状态与边框、颜色语义、窄屏自适应；选择器、审批、帮助为居中 modal。
- 完成焦点切换、输入历史、斜杠补全、composer 复制、滚轮滚动、工具/思考点击展开、Esc 中断和 `runDone` 队列边界修复。
- 新增响应式 View、鼠标工具展开和 `turn_done` 竞态回归测试；包级 race、全仓测试、PTY e2e、vet、build 已验证。

### TUI 阶段完成 ✅（ADR-030，替代 REPL）

- **交付**（W1-W5）：bubbletea 全屏聊天式 TUI 替代 REPL——消息流式 + md 渲染（glamour 块完成渲染）、底部多行输入 + 队列（回合中排队、turn_done 逐条连跑）、工具折叠块按工具分派（read/write 单行元信息、write 覆盖 gotextdiff、apply_patch diff、list_dir/glob 枚举、update_todo checklist、shell 完整 command+输出）、审批弹窗（tuiApprover 桥 + y/s/n + Esc 拒绝中断）、斜杠命令弹窗选择器（/switch /model /effort /permission，选项实时从配置获取 + 右侧说明）、todo 常驻条（输入框上方、进行中-待办-完成排序 ≤5 项 + 统计）+ 队列条、/switch 消息区全量替换、命令落盘 transcript command 行（resume 呈现、不发模型）、Ctrl+C 复制语义、无 emoji 风格、单焦点 Tab 键鼠模型。
- **REPL 删除**：runREPL + SessionManager 移除（session_mgr.go 删除）；`repl()` 留薄壳调 tui.RunTUI；`run` 保留流式非交互 + TTY 审批；`resume` 迁 TUI（历史首屏）。
- **测试**：tui 包 30+ 单测（事件桥/消息流/工具状态机+分派/审批弹窗/命令弹窗/队列/中断/落盘/todo 条）+ termtest e2e 全面覆盖（TUI 起/交互闭环/审批 y/resume 首屏 + run 保留用例）。TranscriptWriter.Close 幂等（sync.Once）+ Flush 同步点（命令落盘）。
- **版本**：0.5.0 → **0.6.0**。
- **人工测试清单（termtest 难覆盖，需实测）**：鼠标点击工具块展开/收起、中文 IME 输入、Ctrl+C 复制（剪贴板）、终端 resize、长输出滚动性能、并行工具多审批排队、真实模型回合。

### TUI 阶段启动：决策落盘 + 任务拆分 ✅（ADR-030）

- **背景**：用户指 REPL 的交互式行为（行式流输出 + 提示符混流）影响项目测试，决定把 TUI 提前到现在（TASKS.md 阶段 6 → 子 agent 之前），TUI 上线后 **REPL 整体删除**。
- **调研**：两轮 Explore 深挖 codex（`codex-rs/tui`：ratatui 全帧 diff + 命令式 App 状态机 + FrameRequester 合并重绘 + 无 TTY state-machine 测试）+ opencode（`packages/tui`：Solid 组件树 + 原生渲染内核 + 16ms 批量合帧 + 增量 store reducer + 无 pty 帧快照测试）。提炼共性 6 条：渲染/输入/agent 解耦、事件驱动增量、组件化视图、流式 cell 完成合并、UI state 独立 reducer、无 TTY 测试。
- **决策（ADR-030 落盘）**：bubbletea v1.3 + bubbles + lipgloss + glamour（md 渲染）+ gotextdiff（write diff）；REPL 删除、run 保留；队列（run 期间输入 + 队列条 + turn_done 连跑）；斜杠命令弹窗选择器 + 实时配置列表 + 自动补全 + 命令落盘 command 行（不发给模型）；单焦点 Tab 键鼠 + Ctrl+C 复制 + Esc 中断 + 仅 /exit 退出；工具折叠块按工具分派（read/write 单行元信息、write/apply diff、list_dir/glob 枚举、todo checklist、shell 完整 command+输出）；thinking 流式折叠；切换全量替换；无 emoji 风格；单测为主 + e2e 全面。
- **落盘**：DECISIONS.md 加 ADR-030；TASKS.md 建阶段 TUI + W0-W5 子任务，阶段 4/5 标后置；task list 建 #82-87。
- **下一步**：W1 依赖 + TUI 骨架。

### workspace 桶精确建桶修复 ✅（FindProject 去向上归并）

- **现象**：用户报 case03 任务未在全局目录建 workspace 记录。排查发现**记录存在**但落进 `D__agent-project_harness`（项目根桶）：会话 `20260809T125025-74dce164`（agentstate.cwd = `D:\agent-project\harness\just-for-test\case03`，任务正常执行）。
- **根因**：`FindProject` 从 cwd 逐级向上匹配**已存在的桶**（ADR-025 设计）；项目根桶 8月8 已建立，case03 上探命中它 → 被归并。case02（8月8 上午）时根桶未建，故有独立桶。
- **修复**：`FindProject` 改为**精确匹配启动目录（pwd 结果）**，不做向上探测——桶 = `<workspaces>/<EscapePath(cwd)>/<session-id>`，与 state.CWD 一致。ADR-025 追加修订记录；`store_test.go` TestFindProject 更新（新增"上级桶已存在仍不归并"断言）。
- **既有数据**：项目根桶的 3 个历史会话保留不动；进行中的 case03 会话仍在项目根桶，待进程退出后可把 `20260809T125025-74dce164` 移入 case03 桶（或保留）。

### cmd/harness 瘦身：ui 交互层下沉 + session_mgr 拆分 ✅

- **背景**：用户指 cmd/harness 主入口包过重（9 生产文件 ~1080 行，repl.go 323 行），关切代码结构与长期可维护性。
- **判断**：臃肿本质不是文件多，而是 main 包承载了不属于应用层的 UI 机制（终端输入/渲染/审批展示）——renderer/approver/input 三文件只依赖 internal 类型、零 cmd 专属依赖，本该在 internal。
- **拆分 1**：repl.go（323 行）拆出 `session_mgr.go`（SessionManager/switchTo/switchLast/replCommand/parseCommand/handleCommand），repl.go 只剩 repl + runREPL；`commands_test.go` 改名 `session_mgr_test.go`（其测试本就全是 SessionManager）。
- **下沉 ui**：新建 `internal/ui`（用户交互层）——cmd 的 input.go/renderer.go/approver.go 整体移入：`readStdinEvents`→`ui.ReadStdinEvents`、`output`→`ui.Output`、`textRenderer`/`jsonRenderer`→`ui.TextRenderer`/`ui.JSONRenderer`、`approvalRequest`/`channelApprover`→`ui.ApprovalPrompt`/`ui.ChannelApprover`；接口方法与字段导出（Start/Event/Esc/Line/Req/Resp）；ANSI 常量/argsSummary/emitJSON 成 ui 包私有。两个测试随迁。
- **结果**：cmd/harness 生产文件 9→7（main/runtime/build/run/resume/repl/session_mgr），全部是"装配 + 子命令编排 + REPL 业务循环"，无一条终端交互机制；`internal/ui` ~250 行独立交互层，阶段 6 TUI 在此扩展。依赖 ui→{middleware, agent, messages} 单向无环。
- **验证**：全量 `go build && go vet && go test ./...` 绿 + `go test -race ./...` 绿。

### 项目结构可维护性重构 ✅（middleware core/impl 分层 + 文件拆分）

- **背景**：用户评估结构可维护性。要点——① `cmd/harness/commands.go` 651 行混 5 关注点（run/REPL/输入/resume）；② `provider/anthropic.go` 341 行混 client/消息转换/流适配；③ `session/transcript.go` 写侧（异步 writer）与读侧（resume 重建）混；④ `middleware/approval.go`（跨包契约）与审批实现分散，用户指其"与中间件无关"；⑤ `messages/jsonl.go` 死代码（ADR-025 后无生产调用）；⑥ `agent/util.go` 单函数文件。
- **决策**：采纳用户"中间件 core + 接口实现分离"——框架层只留机制，具体中间件实现集中 **`internal/middleware/impl`**：`internal/approval` 包整体并入（policy + ApprovalMiddleware）、`session.SessionMiddleware` 搬入、原 middleware 三个通用中间件（tool_instructions/todo_reminder/tool_output）迁入。契约文件 `approval.go` → `contract.go` 留在框架层（agent 调用层捕获 DeniedError 只认 middleware 类型；契约若随实现走 impl 会与签名用 rc 互引成环）。**RuntimeContext 留在框架 core**：它是 handler 签名三件套（ctx + rc + Input）的载体、与 chain 强耦合；独立成包需连带迁走 Approver 契约、改 108 处引用，收益仅是命名纯度——不做。
- **文件拆分**：commands.go → run.go/repl.go/input.go/resume.go（input_test.go 正好对上）；anthropic.go → anthropic_messages.go（toAnthropic* 转换）/ anthropic_stream.go（SSE 块事件适配）；transcript.go → load.go（读侧；currentSegment/historyPath 等共用辅助留写侧）；删 messages/jsonl.go + 5 个 JSONL 测试；agent/util.go 并入 agent.go。
- **依赖方向**：middleware（框架）→ {messages, provider, agentstate} 纯数据层；impl → {middleware, messages, provider, agentstate}；tools → impl（EvictContent 复用，shell 超时落盘）。`internal/approval` 包删除，`internal/middleware` 框架层只剩 chain/runtime_context/contract 三文件。
- **验证**：全量 `go build && go vet && go test ./...` 绿 + `go test -race ./...` 绿。已知 flaky（既有，非本次引入）：`TestShellCommandTimeoutEvictsOutput` 依赖 PowerShell 冷启动 ~1.5s 时序，完整套件并行负载高时偶发无输出，单独跑稳定通过。

### 工具审批（阶段三权限）✅（ADR-029）

- **背景**：阶段三开工。调研 codex（Rust：Profile+Policy 两层正交、审批流水线 hook→guardian→user、缓存 key 含命令+cwd、命令规范化）+ opencode（TS：工具主动 ask、规则集最后匹配胜出、决策粒度=工具+资源模式、拒绝三分类、级联拒绝）。结合既有 `onActing` 挂载点（ADR-021 预留）+ `AgentState.Permission` 预留字段落地。用户三点决策：readonly 写操作**询问**、记忆**仅会话级**、非 TTY **自动拒绝**。
- **approval 包**（`internal/approval/`）：
  - **Policy**（纯函数 `Decide(call, mode, approved)`）：三档模式 `readonly/acceptedit/bypass`；工具分类（只读集合 / write_file+apply_patch / update_todo 低风险 / shell_command）+ shell 黑白名单前缀/子串匹配（白名单 `ls cat git status` 等放行；黑名单 `rm -rf sudo curl|sh` 等触发审批）。命令**规范化**（trim+折叠空白+取前 2 token，`git status --porcelain`→`git status`，对齐 opencode arity）。
  - **ApprovalMiddleware**（onActing before）：Decide → Allow 放行 / Ask 经 `rc.Approver` 询问 / Deny 拒绝。**拒绝返回自定义 `middleware.DeniedError`**（用户建议：独立错误类型 vs 借用 ToolError——语义分离），agent 调用层捕获后回填失败结果、循环继续（拒绝≠Fatal，不取消整批）。
  - **会话级记忆**：按 s → key 记入 `AgentState.Permission.Approved`（shell 规范化命令前缀 / 其它工具名），随 AgentState 落盘跨轮生效。
- **CLI 接线**（最难部分）：REPL 单一读方原则下，审批交互经 **channel 协调**——`channelApprover.Request` 发请求到 reqCh，主循环 select 消费 + 打印审批 UI + 下一行输入路由为答复（y/s/n / Esc）。`runCmd` 从 `withEscInterrupt`（独立 Esc goroutine 与审批读行竞争 stdin）改为与 REPL 统一的 `readStdinEvents + select` 骨架（withEscInterrupt 删除）。非 TTY / MakeRaw 失败 → 不设 rc.Approver → 自动拒绝。`--json` 发 `approval_request` 事件。
- **契约类型**：`middleware.Approver / ApprovalRequest / Decision / DeniedError` 定义在 middleware 包（approval→middleware 依赖已存在，避免循环；provider 无法 import approval，approval.mode 校验硬编码合法值）。
- **验证**：全量 `go build && go vet && go test ./...` 绿；approval 包 17 测试（三档/黑白名单/记忆/规范化/摘要）；agent 集成 3 测试（拒绝回填不 Fatal + 工具不执行 + 回合继续）；provider validate 2 测试；approver 3 测试（解析/channel 往返/ctx cancel）；**e2e 新增 TestApprovalE2E：termtest 真实 TTY 下 write_file 触发审批 UI → SendLine y → 工具执行写盘 → 次轮回复 → 退出码 0**。版本 0.3.0 → 0.4.0。
- **补充（同日）**：采纳用户设计——config `approval.mode` 为默认权限（可不配置 = acceptedit），**会话创建时播种**进 `AgentState.Permission.Mode`（`Project.Create(model, cwd, mode)` 加参数，`CreateInCWD` 透传 `App.defaultApprovalMode()`）；新增 **`/permission <mode>` 会话级切换**（`Session.SetPermissionMode` 立即落盘，对齐 `/model`/`/effort` 模式）。审批模式完全由会话 state 决定（resume 恢复），config 只在创建时播种。测试：TestCreatePermissionMode/TestSetPermissionMode/TestHandleCommandPermission(+Invalid)。版本 0.4.0 → 0.5.0。
- **补充 2（同日）**：① 用户全局配置 `~/.harness/config.yaml` 配 `approval.mode: bypass`（用户本机默认不审批，代码默认仍 acceptedit）；② **阶段 3 目标里"错误重试"拆出**——与审批无关（历史规划残留）：429 重试依赖 SDK 内置退避（ADR-012，无需自研），流中断恢复未做、独立待办；IMPLEMENTATION_PLAN/TASKS 已标注。
- **留增强**：级联拒绝、全局 allowlist、拒绝反馈（CorrectedError）、bash 语法解析（tree-sitter/arity）、guardian 自动审批、复杂规则集。

### 工具结果截断中间件 + Esc 用户中断 + shell 长任务缓解 + state.CWD 修正 ✅（ADR-028）

- **背景**：用户提出三个能力缺口——长工具结果模型无法读全量（现 `truncate` 保留前 20K 直接丢）；用户无法主动中断 agent（REPL 共用 ctx 一旦 cancel 永久失效）；模型卡在慢 shell 命令（同步阻塞）。调研 codex（unified_exec HeadTailBuffer / turn_aborted）+ opencode（truncate.ts 落盘 / ctx.abort）。用户反馈定案：**截断上收为中间件**、**Esc 键触发中断**。
- **ToolOutputMiddleware**（onToolCall after）：改写本批 tool_result 消息 Content。工具返回完整结果（删 fs/shell 内 truncate），策略一处定义。截断 = head 前 50% + tail 后 50%（各 10KB）+ 省略计数 + 全量落盘 `<会话>/evictions/tool_<ts>.txt` + 绝对路径 + read_file/grep 提示。rc/StatePath 空退化纯截断。**transcript 记完整**（审计），conversation 记 preview+路径。
- **Esc/Ctrl+C 中断**：REPL 改单一读方事件循环（`readStdinEvents` goroutine 逐 rune 读 stdin → channel：Esc/Ctrl+C→esc、回车→行、退格→删行尾+回显、Ctrl+D→EOF、中文 ReadRune）。raw mode（`golang.org/x/term`，非 TTY 降级）。中断 → cancel 本轮 runCtx（下一轮不受影响）+ `AddUser` 系统提示落盘（resume 可见）。runCmd 单轮 `withEscInterrupt`。
- **shell 缓解 A+B**：A = 系统提示 `# 长耗时命令` 引导（放后台 + 日志轮询 + 不盲目重试）；B = 超时/非零退出已收集输出落盘，错误带路径。
- **state.CWD 修正**：`Project.Create(model, cwd)` 存**会话启动目录**（此前误存 FindProject 项目根，可能 ≠ 启动目录）；bucket 归属与启动目录解耦。
- **描述一致性**：文件工具描述改"相对进程工作目录或绝对路径"（诚实：按 os.Getwd() 解析、接受任意绝对路径；边界留阶段三 onActing）。
- **验证**：全量 `go build && go vet && go test ./...` 绿；新增 tool_output_test（head/tail/落盘/退化/仅改写本批）、input_test（readStdinEvents 行/Esc/Ctrl+C/退格/中文/Ctrl+D）、shell 超时落盘、session CWD；**e2e 三用例重跑绿（termtest 真实 TTY + raw mode 兼容性验证通过）**。版本 0.2.0 → 0.3.0。
- **踩坑**：`go get golang.org/x/term@latest` 把 go directive 升到 1.25 → 降回 1.24.2 并用旧版 x/term v0.29.0 + x/sys v0.30.0（兼容 go 1.18）。shell 超时落盘测试需 >20K 输出触发（PowerShell 冷启动 ~1.5s，用 `("s"*2000)` 长输出 + Start-Sleep 卡住）。

## 2026-08-08

### todo 工具（update_todo + 跨轮偏离提醒）✅（ADR-027）

- **背景**：进入阶段三（权限）前第一个功能。用户要求先参考开源实现：调研 codex `update_plan`（事件型只转发前端、全量替换、explanation 可选）+ opencode `todowrite`（持久化 SQLite `(session_id, position)` 无 id、纯工具回填可见性、**prompt 引导写最好**）+ AgentScope tasksContext（todo 进 state，已选路线）。
- **用户决策**：不做 priority/cancelled；**模型显式填 position** 维护顺序（对齐"md 有序列表"心智）删 `TodoItem.ID`；无 explanation 参数；prompt 详尽引导；可见性 = 工具结果回填 + 提醒机制（非空 todo 连续 ≥10 次 model call 未更新 → 注入提醒）；无权限/无 handler 归一化。
- **TodoItem 结构**：`{Position, Description, Status}`（删 ID）；`ReplaceTodos`（按 position 稳定排序全量替换）替代 `AddTodo/UpdateTodoStatus`；`RenderTodos`（`1. [~]` / `[ ]` / `[x]` 渲染，工具结果 + 提醒共用）。
- **update_todo 工具**：`{todos:[{position,description,status}]}` 全量替换 → `rc.State.ReplaceTodos` → 记录 `todo_last_update` 基准 → 返回渲染 checklist 回填。tools 包级 `todoMu` 保护 Todos 与 attrs 写（并行工具同轮并发）。
- **TodoReminderMiddleware**（onReasoning）：每轮采样计数（rc.attrs per-Run）；todo 非空且 `cnt-last >= 10` 时 **copy 请求消息副本** 在尾部注入提醒 user 消息——不写 conversation（不落盘、resume 不重放，一次性注意力拉回）。
- **系统提示**：`ToolInstructionsMiddleware` 追加 `# 任务管理` 引导段（含 update_todo 时）；工具 description 抄 opencode 风格（When to use / Rules / 状态）。
- **验证**：全量 `go test -race ./...` 绿（含并发写锁、提醒注入/重置/不污染 conversation 测试）；e2e 重跑绿；**真实 API 冒烟 ✅**——模型正确建 3 步清单、标记第一步 in_progress、工具结果回填渲染；agentstate.json 落盘 position/description/status。
- **顺手修**：agent 测试 `eventRecorder` 并行 emit 无锁 race（runToolBatch 并行工具既有设计暴露的测试缺陷）。

### 架构重构：无状态 agent + 运行时切换（会话/模型/档位）+ 配置统一 init ✅（ADR-026）

- **背景**：用户要求优化代码结构（配置加载重复、main.go 初始化堆积），并明确未来需求——进程内 resume 切换 session、多个 agent 并行、运行时切换模型与推理强度。用户对齐 AgentScope：**无状态 agent，其余全由 RuntimeContext + AgentState 承载**。
- **架构翻转**：agent 完全无状态（`Run(ctx, rc, onEvent)` 去掉 thread 参数，消息序列从 `rc.Messages` 取）；rc 承载会话（新增 Messages/StatePath/Model/ThinkingEffort/ThinkingEnabled，`Session.RuntimeContext()` 每轮新建）；**一个共享 agent + 共享 chain 可被多 goroutine 并发 Run**（并行架构可扩展的基石）；切换会话 = 换 active，下一轮取新 rc。
- **配置统一 init**：`loadConfig/configCandidates` 迁入 `provider.LoadConfig`（cmd 去掉 yaml 依赖）；cmd 层 `Runtime{Config, Resolved}` 经 `defaultRuntime()`（sync.Once 惰性单例）复用，Resolved = 默认模型全局。惰性而非包 init()（version/help/sessions 不需配置）。
- **运行时切换**：`Request` 加 `ThinkingEnabled/ThinkingEffort`（三态覆盖：nil/空 = 继承 client 默认；`Model` 字段本就存在但 anthropic 适配器忽略，补上尊重）；`AgentState` 加同名字段持久化；REPL `/switch <id>|--last`、`/model <name>`、`/effort <level>`（都落会话 state，随 SessionMiddleware 落盘，resume 恢复）。
- **SessionMiddleware 无状态化**：去掉 Sess 字段，从 rc.StatePath 读写（取代 StateMiddleware）。
- **修复 rc-drop bug**：`sample()` 内 `wrapped(ctx, nil, ...)` 导致 onModelCall 中间件拿到 nil rc（读 rc 会解引用错误）→ 改 `wrapped(ctx, rc, ...)`。
- **main.go 拆分**：main（dispatch）/ runtime / build（共享 agent + resolveFlags）/ commands（run/repl/resume/sessions + REPL 命令）。
- **验证**：全量 go test 绿（agent Run 签名机械改 + 新增 rc→Request 覆盖、rc-drop、RuntimeContext、SessionMiddleware 无状态、/switch 进程内切换等测试）；e2e 三用例重跑绿；**真实 API 冒烟 ✅**（`run` 默认 / `--effort max` 落盘 `thinking_effort=max` / REPL `/model deepseek-v4-pro` 重置 effort high / `/effort max` / `/switch --last` resume / `resume --last` 恢复）。
- **踩坑**：REPL 测试 `/switch` 预建会话未关 writer 导致 Windows 文件锁（需先 Close 释放再 resume）；`rc.Thread` 命名与并发线程撞词 → 改 `rc.Messages`（用户指出）。

### bug 修复：max_tokens=4096 + budget=1024 导致 thinking 截断无 text ✅

- **现象**：模型"思考总是中断"——thinking 到一半停住，无 text 回复，回合结束
- **根因**：硬编码 `max_tokens=4096` + `budget_tokens=1024` 太小。DeepSeek 长思考（15928+ 字符）把 4096 占满 → `stop_reason=max_tokens`，text 无输出（实测：max=4096 → thinking 16597 无 text；max=8192 → thinking 31710 无 text）。且 DeepSeek 对 budget_tokens 的处理与 effort 相关（budget=1024 对 effort=high 太小导致 thinking 无界）
- **用户方案验证**：DeepSeek 兼容端点**不传 thinking/budget 完全正常**（默认开启 thinking + 自行管理长度，`end_turn` + text）；`max_tokens` 有效范围 `[1, 393216]`，传 0 报错；SDK 强制 max_tokens required（`api:"required"`）不能省略
- **修复**：`defaultMaxTokens = 393216`（端点允许的最大值——用户约束"不设小上限"，超长任务不截断）；**默认不传 thinking/budget_tokens**（DeepSeek 默认开 thinking 且自行管理，传小 budget 反而截断）；effort 独立传 `output_config`；仅 `--no-thinking` 传 thinking disabled
- **验证**：provider 单测更新（enabled 时不传 thinking + 传 effort）；真实 API 冒烟：长思考任务 `has_text=1277 + thinking + turn_done` ✅

### bug 修复：thinking-only 的 assistant 消息重放 400 ✅

- **现象**：真实会话中模型只产出 thinking（无 text、无 tool_call）→ 回合结束无回复；用户发"继续任务" → anthropic 400 `messages.N: all messages must have non-empty content`
- **根因**：ADR-025 决策 thinking 剥离不重放 → thinking-only 的 assistant 消息重放时 `Content=""` + 无 ToolCalls → `toAnthropicAssistantMessage` 生成**空 blocks** → anthropic 要求 assistant 至少一个内容块 → 400
- **修复**：重放时**跳过空 assistant 消息**（`toAnthropicMessages` 对 content 空 + 无 tool_calls 的 assistant `continue`），前后 user 消息相邻（连续 user 是合法格式，tool_result 即以 user 发送）。**先试占位文本块方案被用户否决**——占位会伪造 assistant 内容污染模型上下文，与 thinking 剥离的初衷（不污染上下文）自相矛盾 → 弃用改跳过
- **澄清**：thinking 剥离发生在每次采样请求重放历史时（`toAnthropicAssistantMessage`），**不只 resume**——同一回合二次采样/下一轮/resume 全部剥离，thinking 从不进模型上下文
- **验证**：provider 单测（空 assistant 被跳过、正常消息不受影响）；**真实 API resume 出错会话重放不 400** ✅

### 阶段四拆出：Workspace + AgentState + 会话落盘/resume ✅（ADR-025，提前）

- **背景**：用户要求把阶段四的 workspace + AgentState 提前到阶段三（权限）之前——todo 工具可挂 state 持久化、每次运行记录落盘。参考 codex + AgentScope + 用户自定义项目分桶结构。
- **目录结构（项目分桶，ADR-025）**：`~/.harness/workspaces/<项目转义>/<session-id>/{historys, plans, agentstate.json}` + 全局 `agents.md`/`config.yaml`；扩展目录三层（全局/项目/会话）。转义 `D:\a\b → D__a_b`（保留盘符）；session-id = `<时间戳>-<8hex>`（目录名即 id）。
- **块级 transcript + 异步 writer**：thinking/text/tool_use/tool_result 各一行（每行 ordinal）；单后台 goroutine 消费 channel（FIFO 保序）+ ordinal 在 writer 内自增（resume 按序加载）——解决异步写盘顺序/并发安全；压缩切新文件（`NewSegment` API 预留，触发留 compact 包）；resume 只读最大序号文件。**thinking 存但不重放**（Message.Thinking 存审计，provider 重放剥离）。
- **AgentState 注入机制**：独立 `agentstate` 包（middleware/session 只依赖它，防环）；`RuntimeContext.State` 字段；`session.StateMiddleware` 挂 onAgent（before 加载/after 保存，AgentScope call() 语义）；**工具 Handle 签名加 rc**（todo 后续挂 rc.State.Todos）。
- **provider/agent 块完成事件**：anthropicStream 按 block index 追踪类型，content_block_stop 发 `EventThinkingDone/EventTextDone`（完整块文本）+ tool_call；agent 事件加 `MsgID`（块归属 assistant 消息）+ 新增 thinking_done/text_done。
- **CLI**：run/repl 接入 Session（meta/user 行 + 事件双转发 renderer+writer）；新增 `harness resume --last|<id>`（重建 thread + 恢复 state → REPL 继续）、`harness sessions`（列表）；`buildAgent` 返回 chain + model，runCmd 挂 StateMiddleware。
- **验证**：全量 go test 绿（新增 session 包 13 测试：escape/Store/FindProject/AgentState/StateMiddleware/writer 顺序+并发/切分/重建/Create-Resume）；e2e 三用例（单轮/交互式/落盘+sessions，HARNESS_HOME 隔离）；**真实 API 冒烟 ✅**（`harness run` 落盘 `~/.harness/workspaces/D__agent-project_harness_simple-harness/<sid>/{historys/history-1.jsonl, agentstate.json, plans/}`，块级行完整：meta/user/turn_start/thinking/text/turn_end）。
- **踩坑**：EscapePath 测试 `\x00` 用 raw string 是字面非 NUL（改双引号）；sed 插入 import 误加 `t ` 别名（gofmt 解析，sed 修正）；StateMiddleware 需嵌入 `middleware.Base` 补齐 5 个空 hook（否则不满足 Middleware 接口）；tool_use 参数若 start 时 input 写入累积 builder 会与 input_json_delta 分片重复（改 initial 兜底）。

## 2026-08-07

### middleware 可读性重构 + onAgent 接线 ✅

- **类型别名降噪**：`chain.go` 五个洋葱 hook 签名加 `Handler` 类型别名（AgentHandler/ReasoningHandler/...，用 `=` alias 与原签名完全等价 → Base/测试零改动），接口与 Wrap* 不再被长签名糊眼
- **详细注释**：WrapAgent 集中解释组装 vs 执行两阶段、倒序循环原因（注册顺序外层在前）、`inner := next` 快照（闭包捕获变量非取值）、空链零开销透传；其余 Wrap* 简注引用；ComposeSystemPrompt 注明与洋葱相反是正序流水线
- **onAgent 接线**：此前 5 个洋葱 hook 中 onAgent 是死 hook（Run 未调用 WrapAgent）→ 已把 Run 主体包进 WrapAgent，before 先于 turn_start、after 后于 turn_done（回合级准备/收尾）；rc/thread 判空提到最外层
- **测试**：新增 TestRunOnAgentWrapsTurn（agentSpy 记录 before/after + 事件序列，断言 before<turn_start<turn_done<after）；全量 go test 绿
- **现状**：6 hook 全部真实接线（onAgent/onReasoning/onToolCall/onActing/onModelCall/onSystemPrompt），阶段三权限只需实现 onActing before 即可挂载

### 阶段二：工具系统 + 并发执行 + 终端渲染 + middleware 骨架 ✅

- **交付**：`harness run <prompt>` 单轮 + `harness` 交互式 REPL，工具闭环 + 终端渲染（thinking 灰显 + 工具调用展示 + --json 事件）完整可跑 —— **可用的简单终端 CLI agent 循环**（阶段二定位达成）
- **tools 包**（2.1-2.2）：Tool 接口 + 注册表（有序）+ ToolError 二分类（RespondToModel/Fatal）；6 内置工具（read_file/list_dir/glob/write_file/shell_command/apply_patch）。shell 平台分派（Windows cmd /C / POSIX sh -c）；apply_patch v1 支持 codex 格式子集（Begin/End Patch + Add/Update/Delete + @@ hunk）；write_file 整文件覆盖写（补 apply_patch 无法整文件重写的缺口，真实 API 验证多行创建）
- **middleware 链**（2.3）：RuntimeContext（单用户无 UserID）+ 6 hook（onAgent/onReasoning/onToolCall/onActing/onModelCall 洋葱 + onSystemPrompt transformer），**hook 贯穿 context.Context**（执行链需要，ADR-024）；Base 空实现；内置 ToolInstructionsMiddleware（工具说明注入系统提示，codex 调研依据）
- **agent 纯 loop**（2.4）：Run(ctx, rc, thread, onEvent) 多轮 采样→工具→回填；回合级事件 7 类（turn_start/thinking_delta/text_delta/tool_call/tool_result/turn_done/error），turn_done 为测试锚点；provider Event 加 EventThinkingDelta（anthropic thinking_delta 流式）
- **关键修复**：多工具调用时多条独立 tool_result 消息触发 anthropic 400（"tool_use 无紧随 tool_result"）→ 一批结果合并成一条多块消息（messages.ToolResults），ADR-024
- **关键修复（补）**：真实 API 暴露 anthropic **工具参数流式**——content_block_start 时 tool_use 的 input 为空（`{}`），参数经 input_json_delta 分片到达。原实现只读 start 的 input → **工具参数全空**（apply_patch/shell_command 报"参数为空"）。修复：按 content block index 累积 input_json_delta，content_block_stop 时输出完整参数（+ input_json_delta 累积单测）。**验证：apply_patch 创建/修改/删除 test.txt + read_file 回读，文件增删改读全闭环 ✅**（此前真实 API 测试都用了无参工具 list_dir，未暴露）
- **渲染 + CLI**（2.5-2.6）：renderer 订阅 agent 事件（text/thinking/tool/边界）；--json 输出完整事件流（含 turn_done）；`harness` 交互式 REPL（复用 thread 会话延续）、`harness run <prompt>` 单轮
- **测试**：单测全绿（tools/middleware/agent/provider/messages/cmd）；**termtest 进程外 e2e 跑通**（mock HTTP：单轮 turn_done 锚点 + 交互式工具闭环，Windows ConPTY 集成 demo ✅）；真实 API 冒烟 ✅（读取目录/计数 .md/交互式）
- **踩坑**：Go flag 顺序（flag 在 prompt 前）复现 ADR-018；交互式入口暂不支持 --config（用默认 config.local.yaml 查找）；**Windows shell 引号坑**——Go exec 的 `\"` 转义与 cmd 的 `""` 引号转义不兼容（含引号路径命令被破坏）。对照 codex 调研：codex Windows 用 **PowerShell**（非 cmd）+ UTF-8 输出前缀 → shell_command 改为 **PowerShell `-Command` + UTF-8 前缀**（引号/中文均正常，无临时文件），真实 API 复验通过

### 移除 openai wire，provider 收敛为单 anthropic ✅

- **背景**：AgentScope 调研后用户决策——抛弃 openai wire（Responses 与 Chat Completions 都不要），只留 anthropic Messages（ADR-022）。理由：单 wire = 最大 simple，thinking/事件形状唯一；接入面收窄的代价（DeepSeek openai 格式、阿里 qwen 失联）可接受。
- **改动**：
  - provider 包：删 `openai.go`、`Provider` 接口、`WireAPI` 类型/常量、`ProviderConfig.WireAPI` 字段、`factory.go` 的 switch 分派；`DefaultEnvKey(w)` → `DefaultAPIKeyEnv` 常量（ANTHROPIC_API_KEY）；`NewClient` 直接构造 anthropic；`Resolved` 去 `WireAPI` 字段
  - **保留** `Client`/`EventStream`/`Event` 接口 + `FakeClient`（agent 与 SDK 的可测边界，阶段二测试主力）
  - 测试：删 5 个 openai 用例（含 sseEvent helper）+ wire_api 校验用例，适配新签名
  - config：`config.example.yaml` 改单 anthropic 结构；`config.local.yaml` 移除 deepseek(openai)/qwen(openai)，保留 deepseek-claude/qwen-claude，`default_provider` → deepseek-claude（用 Go + yaml.v3 脚本处理，key 未进对话/记忆）
  - go.mod：`go mod tidy` 移除 openai-go
- **验证**：`go build`/`go vet`/`go test` 全绿；真实 API 复验 deepseek-claude：默认 / `--effort max` / `--no-thinking` 三路径流式回复 + assistant_message 正常，无 error 事件
- **结论**：provider 层保留"接口边界"（可测性），砍掉"多 wire 抽象"（单 wire 下的负担）——这才是移除 openai 消化的真正复杂度

## 2026-08-06

### thinking 推理模式支持 ✅

- **需求**：DeepSeek V4 支持 thinking（默认启用，档位 low/high/max）。框架需默认启用 + 多档位 + 运行时修改。**用户约束：不能变成 DS 特化格式，配置与传递都按通用语义 / 各 wire 标准参数**
- **配置**（model 级）：`thinking: {enabled, efforts}` —— enabled 默认 true；efforts 是模型支持档位集（默认 [low, high, max]）；当前档位默认 high（openai/anthropic 两协议一致）
- **CLI 运行时覆盖**（优先于配置）：`--effort <low|high|max>`（须在模型 efforts 内否则报错）+ `--thinking` / `--no-thinking`（互斥 bool）
- **传递**（各 wire SDK 标准参数）：
  - openai（Responses）：`reasoning: {effort: low|high|max}`；关闭传 `effort: none`
  - anthropic（Messages）：`thinking: {type: enabled, budget_tokens}` + SDK `output_config: {effort}`；关闭传 `thinking: {type: disabled}`
- **关键坑**：DeepSeek **默认开启 thinking** → `--no-thinking` 若不显式传关闭表达（effort none / thinking disabled）根本关不掉
- **真实 API 验证**（双 wire 全路径）：deepseek（openai wire）high / max / none 全通过；deepseek-claude（anthropic wire）enabled+high / max / disabled 全通过，无 400
- **测试**：resolve 默认值 + efforts 解析 + YAML 解析、validate 白名单、wire 请求体参数断言（4 个新测试）、CLI flag 互斥与校验（3 个新测试），全绿
- 文档：ADR-020；config.example.yaml 更新 thinking 示例；config.local.yaml deepseek 模型加 thinking 配置

## 2026-08-05（续）

### anthropic wire 401 根因修复 ✅（重要调试）

- **现象**：deepseek-claude（anthropic wire）调用 401 "Authentication Fails, Your api key: ****AGED is invalid"，但 key 对 openai wire 完全有效，curl 也 200
- **排查过程**（多步对照实验）：
  1. curl 直接测：`x-api-key` 和 `Authorization: Bearer` 对 DeepSeek anthropic 端点都 200 → key 有效、端点接受两种头
  2. SDK 代理调试：发现 SDK 请求头里有 `Authorization: Bearer PROXY_MANAGED`（非 SDK 代码注入，是系统代理/全局软件在出站请求注入的）
  3. 纯 Go `http.Client` 直连 + `x-api-key: 真实key` → **200**；再加 `Authorization: Bearer PROXY_MANAGED` → **401**
  4. **根因确认**：DeepSeek 端点**优先读 Authorization 头**。系统注入的 `Bearer PROXY_MANAGED` 覆盖了正确的 `X-Api-Key` → 401。key 本身完全有效
- **修复**：anthropic 适配层 `newAnthropicClient` 增加 `option.WithAuthToken(res.APIKey)` —— 显式设置正确的 `Authorization: Bearer 真实key`，覆盖系统注入的假头（与 X-Api-Key 双保险）
- **验证**：`harness run`（deepseek-claude）正常回复 ✅
- **教训**：OpenAI wire 无此问题（其 Authorization 本就是真实 key）；这是 anthropic wire 特有的坑
- ADR-019：anthropic wire 必须显式设置 Authorization 头

## 2026-08-05

### 双 provider 真实测试 + yaml 校验 ✅

- 用户更新 config.local.yaml 为真实内容：两个 provider（deepseek / deepseek-claude，各自 api_key）
- **测试结果**：
  - ✅ deepseek（openai wire）：`harness run` 正常回复
  - ⚠️ deepseek-claude（anthropic wire）：代码路径正确（请求发到 /anthropic/v1/messages），但 **401 invalid key**——用户的 key 对 anthropic 端点无效（DeepSeek anthropic 兼容端点鉴权独立）
- **踩坑 1（测试假象）**：`--model deepseek-v4-flash` 只在当前选中 provider 的 models 里查；两 provider 有同名模型，导致"测试 2"实际还走 openai wire，没测到 anthropic。**正确做法：临时改 default_provider 再测**
- **踩坑 2（flag 顺序）**：`run "hi" --config x` 中 flag 在 prompt 后，Go flag 包停止解析，--config 被当 prompt 发给模型（浪费一次 API 调用）。flag 必须放 prompt 前
- **yaml 校验**（用户要求，加载时）：`Config.Validate()` —— providers 非空、default_provider 存在、wire_api 枚举、models 非空、context_window >= 0、key 来源（api_key/env_key）非空；一次返回全部错误。接入 loadConfig。8 个单测全过
- ADR-017：yaml 校验时机与内容

## 2026-08-04（深夜 2）

### 多模型配置系统重构 ✅

- **需求**：context_window 进 YAML + 支持多模型 + 按 provider 分组（用户明确结构）
- **配置结构**（ADR-015）：`default_provider` + `providers.<名>{wire_api, base_url, api_key/env_key, models.<模型>{context_window}}`
- **实现**：
  - `interface.go`：Config/ProviderConfig/Model 新结构（ProviderConfig 避免与 Provider 接口重名）
  - `resolve.go`（新）：Resolve(cfg, modelFlag) → Resolved{ProviderID, WireAPI, BaseURL, APIKey, Model, ContextWindow}；选择优先级 `--model > default_provider > 排序第一个`
  - `factory.go`：NewClient(res *Resolved)；providerBase 增加 contextWindow 字段（替代 ContextWindowFor 查表）
  - `models.go` **删除**（硬编码表移除，窗口完全来自配置）
  - CLI：`--model` flag + loadConfig 适配新结构（移除环境变量 fallback——多 provider 结构无法用 env 表达）
- **真实 API 验证**：
  - 默认：`harness run "你好"` → deepseek-v4-pro 回复成功
  - 切换：`harness run --model deepseek-v4-flash "..."` → 生效
  - 错误：`--model nonexistent` → `models: "nonexistent" not found in this provider`
- **踩坑（ADR-016）**：默认模型取排序第一个 → `deepseek-v4` 不存在（DeepSeek 只支持 v4-flash/v4-pro）→ 400。修复：config.local.yaml 改为真实可用的 `deepseek-v4-pro`。教训：配置作者需保证第一个模型可用，或用 --model 指定。

## 2026-08-04（深夜）

### 配置系统改造：项目级 config.local.yaml ✅

- 用户提供了 `.env`（DeepSeek 兼容端点：apikey + openai_base_url + openai_model + anthropic 备用）
- **决策**：`.env` 直接转成项目级 `config.local.yaml`（不入 git），作为本项目后续调用的配置来源；不再需要手动 export 环境变量
- 改动：
  - `provider.Config` 增加 `APIKey` 字段（`api_key` in YAML）；key 解析顺序：配置文件 api_key → env（env_key / 默认变量名）
  - `loadConfig` 查找顺序：显式路径 → **项目级 `config.local.yaml`** → `~/.harness/config.yaml` → 环境变量 fallback
  - `config.example.yaml` 模板更新（说明三种 key 提供方式）
- 验证：`harness run "你好..."` 无任何 export 直接调用成功，模型身份确认是 DeepSeek
- 测试：新增 TestLoadConfigProjectLocal（chdir 到临时目录验证项目级优先）；TestLoadConfigFromFile 增加 api_key 断言
- 安全：`.env` 和 `config.local.yaml` 均在 .gitignore（`git status --ignored` 确认 `!!` 忽略）

## 2026-08-04（晚）

### 阶段一 完成 ✅（1.4 ~ 1.8）

**1.4 provider 接口**：`internal/provider/` interface.go（Provider/Client/EventStream/Event）+ models.go（模型窗口表）+ factory.go（Config → NewClient）。`go vet`/`gofmt` 干净。

**1.5/1.6 OpenAI + Anthropic 适配**：两个 SDK 适配层，统一消息 ↔ 原生格式转换 + SSE 流事件 → 统一 Event。
- **关键 SDK API 探测**（openai-go v1.12.0 / anthropic-sdk-go v1.61.0）：
  - 流式：`NewStreaming(ctx, params) → *ssestream.Stream[T]`，迭代 `Next()/Current()/Err()/Close()`（两 SDK 一致）
  - openai 事件：`output_text.delta`（`ev.Delta.OfString`）、`output_item.done`（`ev.Item` 含 function_call: `Arguments/CallID/Name`）、`response.completed`
  - anthropic 事件：`content_block_start`（tool_use 在 `ev.ContentBlock`）、`content_block_delta`（文本在 `ev.Delta.Text`）、`message_stop`
  - openai helper：`openai.String/Bool/Int`（**没有 Int64**）；anthropic helper：`anthropic.String`
  - openai 输入项：`ResponseInputItemUnionParam{OfMessage: &EasyInputMessageParam{...}}`；工具：`ToolUnionParam{OfFunction: &FunctionToolParam{Name, Description, Parameters}}`
  - anthropic 工具 schema：`ToolInputSchemaParam{Properties, Required}` + `ExtraFields` 兜底；工具结果：`ToolResultBlockParamContentUnion{OfText}`（**没有 OfString**）
- **踩坑（重要）**：Anthropic SDK 的 `Stream.Next()` 按 **SSE 的 `event:` 字段**路由事件（不是 data 里的 type），mock server 必须同时带 `event: message_start` 和 data 顶层 `type`，否则事件被静默丢弃（3 个测试因此失败）。**openai 版不需要 event: 字段**（两 SDK 解析逻辑不同！）
- 测试：mock SSE server（httptest）各 3 个用例全过

**1.7 agent 单次采样**：`internal/agent/agent.go` RunOnce（文本拼装/delta 回调/error 传播/done 结束）+ 5 个单测。FakeClient/FakeStream 移到 `provider/mock.go`（非 _test 文件）供 agent 包复用。

**1.8 CLI run**：`cmd/harness/` 重构——run/version/help 子命令 + `--json` 模式 + config 加载（YAML 文件 → 环境变量 fallback）+ SIGINT 取消。config.example.yaml 模板。6 个 CLI 单测。

**真实 API 端到端验证 ✅（DeepSeek 兼容端点）**：
- `.env`：apikey + openai_base_url（https://api.deepseek.com/）+ openai_model（deepseek-v4-flash）
- `harness run "你好..."` → 流式回复成功（exit 0）
- `harness --json run "说一个字"` → `{"type":"thread_start",...}` `{"type":"text_delta","text":"好"}` `{"type":"assistant_message",...}` —— 结构化事件正常
- 验证了"OpenAI 兼容端点"设计路径：base_url 覆盖即可，无需新实现

## 2026-08-04（早）

### 阶段一 1.3 messages 包 ✅

- 完成 `internal/messages`：统一 Message/ToolCall/ToolResult/Thread 模型 + JSONL 序列化（SaveJSONL/LoadThreadJSONL/Read/Write）
- 7 个单测全过：JSONL 往返、tool result 序列化、文件往返、缺 id 补全、坏行报错、AppendToolResult
- 踩坑：测试初始断言 thread ID 往返相等——实际 thread ID 是会话元数据（来自文件名），不在 JSONL 消息行里持久化，改为验证消息序列；gofmt 对齐（json.RawMessage 字段注释缩进）
- 另：gopls 因模块不在 workspace 报 undefined 误报，`go build`/`go test` 实际正常；后续 IDE 如需消除可建 go.work

## 2026-08-04（早期记录）

### 项目初始化 ✅

- 完成 Go 模块初始化：`github.com/agent-project/harness`（go 1.24.2）
- 完成入口骨架：`cmd/harness/main.go`，`version` 子命令可用
- 创建 git 仓库（main 分支），初始 commit：`chore: initialize harness project skeleton`
- 建立文档跟踪目录 `docs/tasks/`（TASKS / PROGRESS / DECISIONS）
- 实施计划落盘 `IMPLEMENTATION_PLAN.md`（基于 codex 源码两轮调研 + 用户决策）

### 前置调研

- 两轮 Explore 完成 codex-rs 源码调研（agent loop / tools / approvals / compaction / thread-store / provider / subagent / AGENTS.md / hooks / 沙箱）
- 关键技术结论：
  - Provider 抽象 = 配置结构体 + 单一 HTTP 客户端（codex `ConfiguredModelProvider` 模式）
  - 子 agent = 独立 session + fork 过滤（只继承 user 消息 + 最终答案）
  - 错误二分类 `RespondToModel` / `Fatal` 是容错核心
  - 压缩触发 = token 超限，TokenBudget 式可先行
