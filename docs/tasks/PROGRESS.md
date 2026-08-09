# 开发日志

> 按日期追加，最新在上。记录：进展、阻塞、问题、经验。

## 2026-08-09

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
