# Go Agent Harness — 实施计划

> 本文档是**当前权威状态**（与代码同步）。已定案的架构决策与核心设计写在下面，实现时遵循而非重新讨论；历史决策详情见 `docs/tasks/DECISIONS.md`（ADR-021~029）。实施阶段严格区分 **✅ 已完成** 与 **⏳ 待办**。任务跟踪在 `docs/tasks/{TASKS,PROGRESS}.md`。

## Context

参照 OpenAI Codex CLI（`../codex/codex-rs`，Rust）+ AgentScope Java v2 的架构，用 Go 构建一个**可真实使用**的极简 agent harness（命令行）。定位为**通用框架**，未来可被 resume-agent 等其它项目引用。

**现状（2026-08-14）**：阶段 1（骨架+消息+provider+最小 loop）→ 阶段 2（工具系统+并发+渲染+middleware 骨架+REPL）→ 阶段 2.5（Workspace+AgentState+会话落盘/resume）→ 架构重构（ADR-026 无状态 agent+运行时切换）→ todo 工具（ADR-027）→ 工具结果截断/用户中断/shell 缓解（ADR-028）→ **阶段 3 审批（ADR-029）✅** → 配置层独立 + 装配根 → **Plan Mode（ADR-036）✅ 2026-08-11** → **Plan Mode 审查修复 ✅ 2026-08-12**（写黑名单反向判定 + 纯 Deny / AgentState 锁下沉 / TUI 待决请求队列，见 DECISIONS.md ADR-036 修订）→ **用量展示（ADR-037 第一段）✅ 2026-08-12**（provider 捕获 usage + AgentState 累计 + footer `/usage`，版本 0.7.1）→ **thinking 完整回传（ADR-025 修订，ADR-037 第二段）✅ 2026-08-12**（thinking signature 捕获→存储→重放，版本 0.7.2）→ **LLM 摘要压缩（ADR-037 第三段）✅ 2026-08-12**（compact 包 + Segment 钩子 + CompactMiddleware + /compact，版本 0.8.0）→ **用量+压缩审查修复 ✅ 2026-08-13**（signature_delta 捕获 / 压缩后采样上下文 / Usage 覆盖语义等 11 项，版本 0.8.1）→ **shell 进程树生命周期（ADR-038）✅ 2026-08-13**（Job Object 杀树 + background/kill_pid + 退出 pre-kill，版本 0.9.0）→ **shell 超时转后台托管（ADR-038 勘误）✅ 2026-08-13**（超时不杀树、移交注册表继续运行，版本 0.9.1）→ **系统提示通道重构（ADR-039）✅ 2026-08-13**（内容通道分类 + rc.SystemPrompt + BaseInstructionsMiddleware + 压缩判定实时化）→ **shell 进程树审查修复 ✅ 2026-08-13**（正常退出不杀派生进程 + preserveProcessTree + 自然退出注销 + attach 降级 + POSIX 测试修复，版本 0.9.2）→ **后台任务完成自动反向通知 + 唤醒器（ADR-040）✅ 2026-08-13**（completion 通用 async 通道 + 采样前注入 + TUI 唤醒器，版本 0.9.3）→ **阶段 7 代码架构整理（ADR-041）✅ 2026-08-14**（Composition Root 收敛 `app.Build→HarnessAgent` + TUI 三阶段 + 接缝方法值化 + rc 注入单点 + ADR-040 审查 03/04/05/06，版本 0.10.0）→ **harness init 工具命令 ✅ 2026-08-14**（workspace 骨架 + 全注释 config 模板 + config 查找对齐 HARNESS_HOME，版本 0.11.0）。待办：阶段 4 剩余（**AGENTS.md 注入** + 系统提示词拼接）、阶段 5（子 agent）、阶段 6（可选）。

## 已确认决策（当前生效）

| 决策点 | 结论 |
|---|---|
| 语言 | Go |
| wire | **单 anthropic Messages wire**（ADR-022）：多后端 = 多 anthropic 兼容端点（base_url 覆盖），DeepSeek 走其 anthropic 兼容端点；不写多 provider 实现 |
| 事件模型 | **分层**：provider 采样级（text/thinking **delta + 块完成** text_done/thinking_done + tool_call + done/error，无生命周期事件）+ agent 回合级（turn_start/turn_done/tool_call/tool_result/thinking_done/text_done，**带 MsgID** 关联块归属） |
| 内部消息模型 | **统一 Message**（role/content/**thinking**/tool_calls/**tool_results 多块合并**/is_error），provider 适配转换；thinking **存且重放**（`Message.ThinkingSignature` 非空时重放 `ThinkingBlockParam`，ADR-025 修订 2026-08-12；无签名仍只存不重放） |
| 扩展机制 | **进程内 middleware**（6 hook：onAgent/onReasoning/onToolCall/onActing/onModelCall onion + onSystemPrompt transformer 链）+ 链机制为核心扩展点；**子进程 hooks 降级远期**（ADR-021） |
| 权限审批 | **三档**（readonly/acceptedit/bypass）+ shell 黑白名单 + **会话级记忆**（Approved）+ config 播种 + `/permission` 切换（ADR-029）；复杂规则匹配不强做，middleware 扩展点承载。**plan 只读** = 写黑名单反向判定 + 纯 Deny（ADR-036 修订，2026-08-12） |
| 会话存储 | **双轨**（ADR-025）：消息流 → **块级 transcript**（historys/history-N.jsonl，异步 writer + ordinal，压缩切新文件）；非消息状态 → **AgentState 快照**（模型/档位/todo/权限/CWD/plan/摘要）→ 完整 resume |
| Workspace | **~/.harness/ 项目分桶**（ADR-025）：workspaces/&lt;项目转义&gt;/&lt;session&gt;/{historys, plans, agentstate.json, evictions} + 全局 config.yaml；evictions/ = 超长工具结果落盘（模型 read_file 读全量，ADR-028） |
| 工具执行 | **并发执行**全部 tool_call（errgroup），结果按 call_id 合并成**一条** tool_result 消息回填（anthropic 紧邻要求，ADR-024） |
| 大工具结果 | **20K 阈值截断**（ADR-028）：head/tail 各 10K + 落盘 evictions/ + 路径提示；transcript 记完整、conversation 记 preview；read_file 豁免；**不做 overflow 安全网** |
| 用户中断 | **Esc/Ctrl+C**（raw mode 事件循环 + 单一读方 channel，ADR-028）：cancel 本轮 runCtx + AddUser 提示落盘；非 TTY 降级 |
| todo 工具 | **update_todo**（ADR-027）：全量替换 + 跨轮偏离提醒（TodoReminderMiddleware） |
| 配置 | YAML（~/.harness/config.yaml + 项目级 config.local.yaml），加载/校验统一在 **internal/config** 包；`app.Load()` 惰性单例（ADR-026，2026-08-09 配置层独立） |
| thinking | **默认开启**（ADR-034，2026-08-10 删 enabled 配置项）；模型配置只留 efforts（档位集）+ CLI `--effort/--thinking/--no-thinking` 覆盖 + TUI `/thinking` 会话切换（持久化 AgentState，nil = 默认开启）；按 anthropic 标准参数传递 |
| 内置工具 | 11 个：read_file / list_dir / glob / write_file / shell_command / apply_patch / update_todo + plan 4 个（plan_enter / write_plan / plan_done / ask_user，ADR-036） |
| 压缩 / 子 agent / AGENTS.md / TUI / Hooks | **压缩 ✅ 2026-08-12（ADR-037 第三段）**；子 agent / AGENTS.md 注入规划中，见"待办阶段" |

## 架构总览（当前实际目录）

```
harness/
├── cmd/harness/          # CLI 应用层：main（dispatch: run/resume/init/sessions/version/help）+ 三命令瘦壳（解析 flags → 声明 app.Options → appCmd；装配与执行全在 Composition Root）+ init/sessions 工具命令
├── internal/
│   ├── app/              # ★ 进程级装配根（Composition Root，ADR-041）：App 配置单例 + Build/Options/HarnessAgent（命令层只声明模式与参数，全部接线收敛于此）+ runOnce 单轮事件循环
│   ├── ui/               # ★ 用户交互层：终端输入（raw mode 单一读方事件循环）/ 渲染（text/json）/ 审批交互（ChannelApprover）
│   ├── ui/tui/           # ★ bubbletea 全屏交互 UI：显式三阶段 Assemble/Run/Close + Controller 事件桥/审批桥（ADR-030）
│   ├── agent/            # ★ 无状态 ReAct loop（采样→工具→回填，消息经 rc.Messages；ADR-026）+ 回合级事件 + Build 域内工厂
│   ├── middleware/       # ★ 框架 core：6 hook 扩展机制 + 洋葱链 + RuntimeContext（承载会话）+ 契约类型（Approver/ApprovalRequest/DeniedError）
│   ├── middleware/impl/  # ★ 内置中间件实现（基础提示词/工具说明/会话状态 load-save/todo 提醒/工具截断/审批策略/压缩/后台完成注入）
│   ├── provider/         # 单 anthropic wire + 块事件适配 + per-call 覆盖（Request.Model/ThinkingEnabled/Effort）
│   ├── messages/         # 统一 Message 模型（含 Thinking）+ JSON 序列化
│   ├── tools/            # Tool 接口（Handle 带 rc）+ 注册表 + 11 内置工具
│   ├── agentstate/       # AgentState 快照（模型/档位/todo/权限/CWD/plan/摘要）+ 原子落盘
│   ├── session/          # workspace 项目分桶 + 块级 transcript 异步 writer + resume + ProjectForCWD
│   ├── compact/          # ★ 上下文压缩（ADR-037 第三段）：EstimateTokens/ShouldCompact/Summarizer/Runner
│   ├── completion/       # ★ 后台任务通用 async 完成通道（ADR-040，只依赖 stdlib）
│   ├── config/           # 配置域（只依赖 yaml+stdlib）：Config/ProviderSpec 类型 + YAML 加载/解析/校验
│   ├── e2e/              # 进程外端到端测试（termtest 真实 TTY + mock HTTP）
│   └── # 规划中（未实现）：agentsmd / hooks；子 agent（阶段 5，HarnessAgent 装配变体）
├── config.example.yaml   # 配置示例
└── docs/                 # 设计文档（DECISIONS/TASKS/PROGRESS + DATA_STRUCTURES）
```

## 核心设计

> 已实现部分写**当前实现**（与代码一致）；未实现部分标注"规划"。

### 0. Middleware（进程内扩展机制，ADR-021）

capabilities 叠加在 reasoning loop 上，不揉进 loop：压缩/权限/提醒/AGENTS.md 注入全部作为 middleware 挂载。

```go
// 6 hook。前五者洋葱（next 前 = before、返回后 = after），onSystemPrompt 是
// transformer 链（前输出 → 后输入）。具体签名见 internal/middleware/chain.go。
type Middleware interface {
    OnAgent(ctx, rc, AgentInput, next) error        // 包一整个 agent.Run（回合）
    OnReasoning(ctx, rc, ReasoningInput, next) error // 包一次采样轮
    OnToolCall(ctx, rc, ToolCallInput, next) error   // 包一批工具调用（发起→执行→回填）
    OnActing(ctx, rc, ActingInput, next) error       // 包单个工具执行
    OnModelCall(ctx, rc, ModelCallInput, next) error // 包一次模型 API 调用（最内层）
    OnSystemPrompt(ctx, rc, current string) (string, error) // 组装系统提示（transformer）
}
```

- **挂载点映射**：`onActing` = 工具审批（ApprovalMiddleware，ADR-029）；`onToolCall` = 工具结果截断（ToolOutputMiddleware，ADR-028）；`onReasoning` = todo 偏离提醒（TodoReminder，ADR-027）+ 压缩（规划）；`onSystemPrompt` = 工具说明注入（ToolInstructions）+ AGENTS.md（规划）；`onAgent` = 会话状态 load/save（SessionMiddleware）。**以上内置中间件实现全部在 `internal/middleware/impl`**。
- **注入机制**：`RuntimeContext`（rc）per-call 新建承载会话（Messages/State/StatePath/Model/Thinking*/Approver）；中间件从 rc 读写，**无状态可并发**（共享 chain 多 goroutine 安全，ADR-026）。
- **事件分层**：provider 采样级（delta + 块完成 + tool_call + done/error）→ agent 回合级（带 MsgID 关联块归属）→ 渲染器/transcript 双转发。

### 1. 统一消息模型（internal/messages/）

```go
type Message struct {
    ID          string            `json:"id,omitempty"`
    Role        Role              // user | assistant | tool | developer
    Content     string            // assistant 文本
    Thinking    string            // assistant 推理（存审计不重放，ADR-025）
    ToolCalls   []ToolCall        // assistant 携带
    ToolCallID  string            // tool 消息关联
    ToolResults []ToolResultBlock // tool 消息携带（多块合并，anthropic 紧邻要求）
    IsError     bool              // 单块 tool result 失败标记
}
```

- 核心层只操作统一模型；provider 适配层 ↔ anthropic 原生格式。
- **transcript（磁盘）= 块级事件流**（tool_use / tool_result 各一行，并发结果独立行）；**conversation（模型输入）= 合并消息**（一条 tool 消息多块）。resume 时 appendToolResult 合并（ADR-025）。

### 2. Provider（internal/provider/，ADR-022）

- **单 anthropic wire**：`client.Stream(ctx, Request) (EventStream, error)`；Request 含 Model/Instructions/Messages/Tools/Thinking*/MaxOutputTokens。多后端 = 多 anthropic 兼容端点（base_url + api_key/env_key），配置驱动。
- **块事件适配**：流式 delta → 渲染；块完成（thinking_done/text_done）→ 组装 Message.Thinking/Content（ADR-025）；tool_call → 收集。
- **per-call 覆盖**（ADR-026）：Request.Model/ThinkingEnabled/ThinkingEffort，nil/空 = client 默认；会话级持久化在 AgentState。
- **重试**（ADR-012）：依赖 anthropic SDK 内置退避（429/5xx）；不自研。

### 3. Agent Loop（internal/agent/，ADR-026）

**无状态**：`Run(ctx, rc, onEvent)` 从 rc.Messages 读消息序列并追加；不持有会话，每 Run 新建 rc。

```
Run（onAgent 包裹：SessionMiddleware load/save）
  循环：
    reasoning（onReasoning 包裹）→ sample（onModelCall 包裹 Stream）
      → 收集 thinking/text/tool_call → assistant 消息追加
    toolCalls 为空 → 回合结束（turn_done）
    否则 runToolBatch（onToolCall 包裹）：
      并发执行每个 tool_call（onActing 包裹：审批 before）
      → 结果按 call_id 收集 → 合并成一条 tool_result 消息回填
```

- **错误二分类**：工具错误 `RespondToModel`（回填循环继续）/ `Fatal`（终止）；**审批拒绝 = 独立 `DeniedError`**（回填、不取消整批、循环继续，ADR-029）。
- **用户中断**：Esc/Ctrl+C（raw mode 单一读方）→ cancel 本轮 runCtx → AddUser 提示落盘（ADR-028）。

### 4. 工具系统（internal/tools/）

```go
type Tool interface {
    Name() string
    Spec() provider.ToolSpec      // name + description + parameters JSON Schema
    Handle(ctx, rc, callID string, args json.RawMessage) (ToolResult, error) // rc 供读状态
}
```

- 注册表：有序列表（模型可见顺序稳定）；错误 `*ToolError{RespondToModel, Message}`。
- 内置 7 工具：read_file / list_dir / glob / write_file / shell_command / apply_patch / update_todo。
- **工具结果截断**：工具返回完整结果，截断策略在 ToolOutputMiddleware（onToolCall after，20K head/tail + evictions/ 落盘，ADR-028）。

### 5. 审批（internal/middleware/impl/，ADR-029）

**三层正交设计**（完整版见 `DATA_STRUCTURES.md §3.9`）：

- **状态层**：`AgentState.Permission{Mode, Approved}`（会话级）；config `approval.mode` 创建时播种，`/permission` 运行时切换，`s` 批准记入 Approved，resume 恢复。
- **策略层**：纯函数 `Decide(call, mode, approved)` → Allow/Ask/Deny。判定顺序：bypass → 会话记忆命中 → 只读/todo → 编辑（按模式）→ shell（黑名单 Ask / 白名单 Allow / 其它 Ask）→ 未知 Ask。命令规范化（前 2 token，`git status --porcelain`→`git status`），记忆与黑白名单共用 key。
- **交互层**：`Approver` 接口（CLI 注入 rc.Approver；nil = 非 TTY 自动拒绝）。`channelApprover` 经 reqCh 与 REPL/runCmd 主循环协调（单一读方），打印 UI + 下一行输入路由为 y/s/n。
- **拒绝 ≠ Fatal**：拒绝返回 `middleware.DeniedError`，agent 调用层捕获回填（不取消整批）。

### 6. 会话存储（internal/session/，ADR-025）

- **双轨**：消息流 → 块级 transcript（historys/history-N.jsonl，异步 writer 单 goroutine FIFO + ordinal；压缩切新文件 `NewSegment`）；非消息状态 → agentstate.json（SessionMiddleware onAgent load/save，每次 Run 进出）。
- **项目分桶**：`~/.harness/workspaces/<项目转义>/<session-id>/{historys, plans, agentstate.json, evictions}`；bucket = FindProject 项目根，`state.CWD` = 会话启动目录（ADR-028，两者解耦）。
- **resume**：只读最大序号 transcript 文件逐行重建 + LoadFile 恢复 AgentState。

### 7. 上下文压缩（internal/compact/）✅ 2026-08-12（ADR-037 第三段）

- **实现 = LLM 摘要式**（不做 v1 TokenBudget）：`EstimateTokens`（bytes/4 含 thinking，镜像实际发送，估算兜底）+ `ShouldCompact`（contextSize >= 85%·ContextWindow 硬编码；实际 usage（LastContextTokens）驱动 + 估算兜底）+ `Summarizer`（codex 方式：完整 conversation + 摘要 prompt 尾 user，无工具，max_tokens 4096；opencode 结构化模板 + previous summary 更新式）+ `Runner.Run(ctx, rc)`（纯执行器——判定由调用方 CompactMiddleware 先 `ShouldCompact(rc, in.Tools)`；ADR-039 修订 2026-08-13：兜底 = 判定时实时三项 messages+系统提示+工具 schema，去 force/bool）。
- 触发：onReasoning **before**（CompactMiddleware，每轮采样前）+ 手动 `/compact`（Controller.RunCompact）。
- 压缩后：conversation = **单一 summary user 消息（纯占位）**；`RuntimeContext.Segment` 钩子切新文件（`NewSegment` + seed + Flush），resume 从新段重建；`AgentState.Summary` + `SetLastContextTokens(0)` 防重入。
- 摘要失败/Esc：**跳过 + 终止 run**，绝不重写 conversation。
- 不做 overflow 安全网（eviction 撑宽度，ADR-009/028）。

### 8. 规划：子 Agent（internal/agent/subagent.go）

- 内置几个子 agent（general-purpose 等）+ 并行执行 + 状态跟踪（pending/running/completed）；自定义声明式（subagents/*.md）预留。
- `spawn_agent` 工具；子 agent = 独立 session + 独立历史（goroutine）；fork 过滤（只继承 user + 最终答案）；`send_message` 单向通信；完成结果注入父上下文。
- 简化：无 mailbox / wait_agent / 并发上限（semaphore 可选）/ 昵称/路径树。
- 并行已由无状态 agent + 共享 chain 并发安全支撑（ADR-026）。

### 9. 规划：AGENTS.md 注入（internal/agentsmd/）

- 从 cwd 向上找项目根 → 收集 AGENTS.md → 拼接注入（作为 onSystemPrompt middleware）；预算 200KB 截断。
- 与系统提示词动态拼接（ToolInstructions + 后续组装）同属阶段 4。

### 10. 规划：Hooks（子进程，远期）

- PreToolUse / PermissionRequest / Stop 三点；子进程模型（stdin/stdout JSON + timeout）。**已被进程内 middleware 承载**（ADR-021），仅作为 middleware 的一种实现方式保留。

### 11. UI（现状：internal/ui 交互层；TUI 规划）

- `internal/ui.Output` 接口（text 渲染器 + `--json` JSONL 事件），事件回调双转发（渲染 + transcript 落盘）。审批交互（ChannelApprover/ApprovalPrompt + 审批 UI）、raw mode 输入（ReadStdinEvents）同在 internal/ui。
- 完整 TUI（ratatui 式）留阶段 6。

## 实施阶段（✅ 已完成 / ⏳ 待办，严格区分）

### ✅ 已完成（历史，详见 TASKS.md/PROGRESS.md）

- **阶段 1：骨架 + 统一消息模型 + Provider + 最小 loop**（2026-08-04）：messages / provider / 最小 loop；真实 API `harness run "你好"` 通过。
- **阶段 2：工具系统 + 并发执行 + 终端渲染 + middleware 挂载点骨架**（2026-08-07）：移除 openai wire（ADR-022）；tools 包 + 7 工具（当时 6 个）；并行执行 + call_id 回填；`output` 渲染 + `--json`；REPL；**自动化测试方案落地**（termtest 进程外 e2e + mock HTTP）。
- **阶段 2.5：Workspace + AgentState + 会话落盘/resume**（2026-08-08，ADR-025）：项目分桶 + 块级 transcript 异步 writer + AgentState 注入机制 + 块完成事件 + `resume/sessions` 命令。
- **架构重构：无状态 agent + 运行时切换 + 配置统一 init**（2026-08-08，ADR-026）：`Run(ctx, rc, onEvent)`；`/switch /model /effort`；`defaultApp()` 惰性单例。
- **todo 工具**（2026-08-08，ADR-027）：update_todo 全量替换 + TodoReminderMiddleware 跨轮偏离提醒。
- **工具结果截断 + Esc 用户中断 + shell 长任务缓解 + state.CWD 修正**（2026-08-09，ADR-028）：ToolOutputMiddleware（20K head/tail + evictions/ 落盘）；raw mode 单一读方事件循环；shell 超时输出落盘 + 系统提示引导。
- **阶段 3：审批**（2026-08-09，ADR-029）：approval 包三层设计 + ApprovalMiddleware + DeniedError + 会话级记忆 + config 播种 + `/permission` + channelApprover 协调；e2e 真实 TTY 审批交互。**错误重试非本阶段交付**（429 由 SDK 承担 ADR-012；流中断恢复独立待办）。
- **配置层独立 + 装配根**（2026-08-09）：配置域从 provider 拆出为 `internal/config`（类型 + 加载 + 解析 + 校验），provider 回归单 wire；`internal/app` 进程级装配根（`App{Config, Provider}` 惰性单例，替代 cmd defaultApp）；`agent.Build(res, mode)` 装配工厂（buildAgent 从 cmd 下沉）；cmd 薄化为 `app.Load() + agent.Build()`。为 subagent 提供不同装配铺路。
- **Plan Mode（规划模式）**（2026-08-11，ADR-036）：会话级 `PlanMode` 标记 + 4 工具（plan_enter 自主进 / write_plan 写计划文件 / plan_done 弹 HITL 交接 / ask_user 通用提问）+ `Approver` 增 `Ask` 方法（选项单选/多选 + Other 自定义，复用 rc.Approver）+ `Decide` plan 分支（可见但拒绝，不做工具过滤）+ `isPlanReadonlyShell`（plan 模式 shell 放宽管道）+ plan 指令进入点持久化单次注入 + TUI `/plan` 切换 / `/plan view` / 状态栏 `[PLAN]` / ask 弹窗；版本 0.7.0。plan_done 的 Other = 拒绝 + 反馈回填模型修订计划。
- **shell 进程树生命周期**（2026-08-13，ADR-038）：Windows Job Object（KILL_ON_JOB_CLOSE）杀树 + POSIX 进程组——Esc/超时在 ctx 取消瞬间杀全树（绕开管道句柄继承卡 Wait 死锁）+ 回填"命令已被中断"；`background`/`kill_pid` 参数（Go 直接启动 + 日志重定向 + 进程级注册表 + kill 仅限注册表 PID）；退出 pre-kill（`CleanupBackground` defer + TUI `SaveActiveState` 兜底 + 内核句柄兜底）；审批 key/摘要/TUI 工具块/系统提示适配。版本 0.9.0。

### ⏳ 待办（未完成）

- **阶段 4（剩余）：AGENTS.md 注入 + 系统提示词拼接**
  - **用量展示 ✅ 2026-08-12（ADR-037 第一段）**：provider 捕获 usage → `messages.Usage` → agent `EventUsage` → AgentState `Usage`/`LastContextTokens` → TUI footer `ctx Nk/Mk` + `/usage`。版本 0.7.1。
  - **thinking 完整回传 ✅ 2026-08-12（ADR-025 修订，ADR-037 第二段）**：捕获 thinking signature → `Message.ThinkingSignature` + transcript Line.Signature → `toAnthropicAssistantMessage` 重放 `ThinkingBlockParam`（仅签名非空）；thinking-only assistant 带签名不再跳过；估算镜像 `compact.EstimateTokens` 含 thinking。DeepSeek 实测通过。版本 0.7.2。
  - **LLM 摘要压缩 ✅ 2026-08-12（ADR-037 第三段）**：`internal/compact`（ShouldCompact 85% 硬编码 + Summarizer codex 方式 + Runner.Run）；`RuntimeContext.Segment` 钩子（NewSegment + seed + Flush）；`impl.CompactMiddleware`（onReasoning before，摘要失败终止 run，Esc 同）；`events.EventCompacted` + `/compact` 手动（Controller.RunCompact 成功显式落盘 AgentState）。版本 0.8.0。
  - **系统提示通道重构 ✅ 2026-08-13（ADR-039）**：内容通道分类原则（对话历史=Messages / 稳定配置=系统提示管道 / 工具定义=toolspec / 即时信号=临时副本，对齐 codex/opencode）；`rc.SystemPrompt`（组合后回写）+ base 中间件化（`BaseInstructionsMiddleware` 链首）+ Build 兜底估算删除 + 压缩判定实时三项估算（CompactMiddleware 持 in.Tools）+ Runner 纯执行器。
  - `agentsmd`（onSystemPrompt：项目级 AGENTS.md 向上搜索 + 全局拼接 + 动态系统提示词组装）
  - 注：大工具结果 eviction 已完成（ADR-028），不属于本阶段。
- **阶段 5：子 agent（内置 + 并行 + 状态 + 单向通信）**
  - 内置子 agent + `spawn_agent` + 状态跟踪 + fork 过滤 + `send_message` 单向。
  - 注：Renderer/config/CLI 子命令/docs 已由前述阶段完成，不属于本阶段。
- **阶段 6（可选）：TUI 渲染器 / 摘要式压缩 / grep 工具 / 双向通信**

### 明确不做 / 降级

- 子进程 Hooks → 进程内 middleware 承载（ADR-021）
- 复杂规则匹配引擎 / 全局 allowlist / 级联拒绝 / 拒绝反馈 / guardian 自动审批 → middleware 扩展点或留增强（ADR-029）
- overflow 安全网 → 不做（eviction 撑宽度）
- openai wire → 已移除（ADR-022）
- 多 provider 抽象 → 单 anthropic wire（ADR-022）

## 验证方案

- **单元测试**：各包 `go test ./...`；中间件/策略纯函数优先（approval.Decide、evictContent 等）。
- **进程外 e2e**：`go test ./internal/e2e/ -count=1`（termtest 真实 TTY + mock HTTP，确定性）；锚点 = `turn_done` 事件；审批交互 SendLine y/s/n。
- **真实 API 冒烟**（CI 末尾 2-3 条，宽松断言）：`harness run` 基础对话 / 工具闭环 / `resume --last` / `--json` 结构化事件 / 危险命令触发审批（按 config 模式）。
- **测试隔离**：workspace 相关测试用 `HARNESS_HOME=<临时目录>`。
