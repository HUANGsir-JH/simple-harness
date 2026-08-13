# 任务列表

> 阶段定义见 `IMPLEMENTATION_PLAN.md`。状态：`未开始 | 进行中 | 已完成`

## 阶段 1：骨架 + 统一消息模型 + Provider + 最小 loop

- **目标**：项目初始化、`messages` 包（统一 Message 模型 + JSONL 序列化）、`provider` 包（Provider/LLMClient 接口 + OpenAI/Anthropic 适配 + 重试）、最小 agent loop（单次采样，无工具）
- **成功标准**：`harness run "你好"` 能从真实 API 拿到流式回复
- **测试**：provider 单测（mock HTTP）；loop 单测（mock LLMClient）
- **状态**：✅ 已完成（2026-08-04）

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| 1.1 | 项目初始化（go.mod + cmd/harness + git） | ✅ 已完成 2026-08-04 |
| 1.2 | 文档跟踪目录（docs/tasks） | ✅ 已完成 2026-08-04 |
| 1.3 | `messages` 包：统一 Message 模型 + JSONL 序列化 | ✅ 已完成 2026-08-04 |
| 1.4 | `provider` 包：Provider/LLMClient 接口 + OpenAI 适配 | ✅ 已完成 2026-08-04 |
| 1.5 | `provider` 包：Anthropic 适配 | ✅ 已完成 2026-08-04 |
| 1.6 | 错误分类 + 重试（指数退避 + 抖动） | ✅ 已完成 2026-08-04（SDK 内置，无需自定义） |
| 1.7 | 最小 agent loop（单次采样）+ CLI run 子命令 | ✅ 已完成 2026-08-04 |
| 1.8 | 真实 API 端到端验证（DeepSeek 兼容端点） | ✅ 已完成 2026-08-04 |
| 1.9 | thinking 推理模式（模型级配置 + 双 wire + CLI 运行时覆盖） | ✅ 已完成 2026-08-06 |

## 阶段 2：工具系统 + 并发执行 + 终端渲染 + middleware 骨架

- **目标**：**移除 openai wire** ✅；`tools` 包（Tool 接口 + 注册表 + 错误二分类）、内置工具（5 个）、并行执行 + call_id 回填、完整简单的终端渲染（thinking + 工具调用 + --json）、**agent 纯 ReAct loop + middleware 挂载点**（onActing 即权限扩展点）、**交互式 REPL**
- **成功标准**：`harness run "读取当前目录文件列表并告诉我"` 触发工具调用并正确回填 ✅；多轮工具调用闭环在终端渲染下完整可跑 ✅；`harness` 交互式多轮 ✅ —— **阶段二完成 = 一个可用的简单终端 CLI agent 循环**
- **测试**：tools 单测（临时目录 + shell 真跑 + apply_patch）；agent loop 单测（FakeClient：无工具/tool 闭环/并行/错误二分类/middleware 链/thinking）；**termtest 进程外 e2e（mock HTTP：单轮 turn_done 锚点 + 交互式）**；真实 API 冒烟 ✅
- **状态**：✅ 已完成（2026-08-07）

### 单元表

| # | 单元 | 状态 |
|---|---|---|
| 2.1 | tools 包（Tool 接口 + 注册表 + 错误二分类） | ✅ 2026-08-07 |
| 2.2 | 内置工具（read_file/list_dir/glob/write_file/shell_command/apply_patch） | ✅ 2026-08-07 |
| 2.3 | middleware 链（RuntimeContext + 6 hook + ToolInstructions） | ✅ 2026-08-07 |
| 2.4 | agent 纯 ReAct loop + 回合级事件（thinking/turn_done） | ✅ 2026-08-07 |
| 2.5 | 终端渲染（thinking + 工具调用展示 + --json 事件） | ✅ 2026-08-07 |
| 2.6 | CLI 装配 + 交互式 REPL | ✅ 2026-08-07 |
| 2.7 | termtest 进程外端到端 + 真实冒烟 | ✅ 2026-08-07 |

## todo 工具阶段（阶段 2 之后单开，编号待定）

- **目标**：todo 工具（任务清单拆解/跟踪）
- **状态**：未开始（2026-08-06 用户指定单独做，不进工具系统阶段）

## 阶段 3：权限/审批（三档）

- **目标**：三档权限（`readonly` / `acceptedit` / `bypass`）**以 onActing middleware 挂载** + 黑白名单 + TTY 交互 + **会话级记忆**（用户决策替代 allowlist，ADR-029；规则匹配引擎保留扩展点，复杂匹配不强做）
- **成功标准**：危险命令按权限档位放行/确认/拒绝；middleware 链能拦截工具执行
- **状态**：**已完成（2026-08-09，ADR-029）**。交付：`internal/middleware/impl/`（Policy 三档 + shell 黑白名单 + ApprovalMiddleware 挂 onActing + `middleware.DeniedError` 拒绝回填）+ channelApprover（REPL/runCmd 单一读方协调，y/s/n）+ `AgentState.Permission`（Mode 会话创建播种 + `/permission` 切换 + Approved 会话级记忆）+ e2e 真实 TTY 审批交互。
- **错误重试（非本阶段交付）**：429 依赖 SDK 内置退避（ADR-012，已承担）；流中断恢复未做，独立待办。
- **Hooks（PreToolUse 子进程）**：降级远期，由 middleware 承载。

## 阶段 4：Workspace（~/.harness/）+ 会话 + 系统提示词拼接 + 压缩

- **目标**：**`~/.harness/` 统一 workspace**（sessions/快照、subagents/*.md 预留、tools.json、memory/）；`session` 包（JSONL 消息流 + **轻量 AgentState 快照** + resume，落 workspace）；`agentsmd` 包（**作为 onSystemPrompt middleware** 注入 + 系统提示词动态拼接，AGENTS.md 项目级向上搜索保留）；`compact` 包（TokenBudget v1 + 摘要式 + **大工具结果 eviction**，作为 onReasoning middleware，**不做 overflow 安全网**）
- **成功标准**：`harness resume --last` 能完整恢复（含权限/todo 等非消息状态）；AGENTS.md 注入生效；系统提示词随上下文动态组装；长会话自动压缩；超大工具结果落盘 + read_file 指针
- **状态**：未开始（2026-08-06 增补系统提示词动态拼接；2026-08-07 确认 workspace/compaction 范围；**2026-08-09 TUI 阶段优先，本阶段剩余 AGENTS.md 注入 + 压缩后置**）

## 阶段 5：子 Agent（内置 + 并行 + 状态）+ CLI 完善 + 文档

- **目标**：**内置几个子 agent**（general-purpose 等）+ **并行执行** + **状态跟踪**（pending/running/completed）+ `send_message` 单向（fork 过滤保留）；自定义声明式（subagents/*.md）预留扩展点；Renderer 接口完成（simple 渲染器 + --json 模式）、config 包（YAML 加载）、CLI 子命令完善、docs/ 设计文档
- **成功标准**：`harness run "用子 agent 分析这个目录结构"` 端到端跑通；并行子 agent 状态可查；`--json` 输出结构化事件；config 文件可配置
- **状态**：未开始（2026-08-07 确认子 agent 形态：内置 + 并行 + 状态，细节阶段五探讨；**2026-08-09 TUI 阶段优先，本阶段后置**）

## 阶段 TUI：bubbletea 全屏交互 UI（提前自阶段 6，子 agent 之前）

- **目标**：bubbletea + bubbles 全屏聊天式 TUI **替代 REPL**（消息列表流式 + md 渲染、底部多行输入 + 队列、工具折叠块 + diff、审批弹窗、斜杠命令弹窗选择器 + 自动补全、thinking 折叠、todo 常驻条、切换全量替换、命令落盘）；TUI 上线后 **REPL 删除**（`repl()` 留薄壳调 `tui.RunTUI`）；`run` 保留流式非交互。
- **成功标准**：`harness` 进 TUI 完整交互（流式回复 / 工具展示 / 审批 y/s/n / 队列连跑 / 斜杠命令 / 切换）；`resume` 历史首屏 + 历史 thinking 折叠；TUI 交互全部无 TTY 单测覆盖；termtest e2e 全面覆盖；版本 0.6.0。
- **测试**：T1 Model.Update 无 TTY 单测（消息流/工具状态机/审批/队列/切换/历史/命令消费）；T2 View 关键内容断言；T3 事件桥 + T4 审批桥单测；T5 termtest e2e 尽量全面 + 人工测试清单（鼠标/IME/剪贴板/resize）。
- **状态**：✅ **已完成（2026-08-09，ADR-030）**——bubbletea TUI 替代 REPL（消息流式 + md 渲染 + 工具折叠块 + 审批弹窗 + 斜杠命令弹窗 + 队列 + todo 常驻条 + 切换全量替换 + 命令落盘 command 行）；REPL 删除（runREPL + SessionManager 移除）；`run` 保留流式非交互；版本 0.6.0。

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| W0 | 决策落盘（ADR-030 + TASKS 条目 + 任务拆分） | ✅ 2026-08-09 |
| W1 | 依赖 + TUI 骨架（bubbletea/bubbles/lipgloss/glamour/gotextdiff + `internal/ui/tui` 空 Model + RunTUI） | ✅ 2026-08-09 |
| W2 | 消息区 + 输入区 + md 渲染（glamour 块完成渲染；textarea + 提交 + 启动回合） | ✅ 2026-08-09 |
| W3 | 回合生命周期（Esc 中断）+ 工具折叠块（分派表 + write/apply diff）+ 状态栏 | ✅ 2026-08-09 |
| W4 | 审批弹窗 + 斜杠命令弹窗选择器 + 自动补全 + 队列 + 命令落盘 command 行 | ✅ 2026-08-09 |
| W5 | 删 REPL + run 整理 + e2e 全面覆盖 + 收尾（版本 0.6.0 + 人工测试清单交付） | ✅ 2026-08-09 |

### TUI redesign pass（ADR-031）

| 单元 | 内容 | 状态 |
|---|---|---|
| R1 | timeline 状态模型与 transcript ordinal 恢复 | 已完成 2026-08-09 |
| R2 | 焦点、队列边界、补全、输入历史、审批/选择 modal | 已完成 2026-08-09 |
| R3 | ASCII/颜色视觉层、窄屏布局、鼠标命中与工具/思考展开 | 已完成 2026-08-09 |
| R4 | 单测、PTY e2e、race/vet/build 收尾 | 已完成 2026-08-09 |

## Plan Mode（规划模式，ADR-036）

- **目标**：会话级 plan 模式——先只读调研、产出计划文件、批准后执行。4 工具（`plan_enter` 自主进 / `write_plan` 写 `<会话>/plans/plan.md` / `plan_done` 弹 HITL 交接 / `ask_user` 通用提问）；`Approver` 增 `Ask` 方法（选项单选/多选 + Other 自定义文本，复用 rc.Approver）；`Decide` plan 分支（可见但拒绝，不做工具过滤）+ `isPlanReadonlyShell`（plan 模式 shell 放宽管道）；plan 指令进入点持久化单次注入；TUI `/plan` 切换 + `/plan view` + 状态栏 `[PLAN]` + ask 弹窗。版本 0.7.0。
- **成功标准**：plan 模式下 write_file 被拒、write_plan 写文件、plan_done 批准后退出并放行写；ask_user 选项/Other 自由文本；bypass 下退出仍询问；e2e 闭环。
- **状态**：✅ **已完成（2026-08-11，ADR-036）**。

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| P1 | 核心闭环：AgentState.PlanMode + Approver.Ask 契约 + 4 工具 + Decide plan 分支 + isPlanReadonlyShell + 注册 + 单测 | ✅ 2026-08-11 |
| P2 | TUI：/plan 切换 + /plan view + 状态栏 [PLAN] + ask 弹窗（选项/Other/单选多选）+ ChannelApprover.Ask + 单测 | ✅ 2026-08-11 |
| P3 | 集成 + e2e（plan 模式闭环）+ 版本 0.7.0 + 文档（ADR-036 修订/IMPLEMENTATION_PLAN/TASKS/PROGRESS） | ✅ 2026-08-11 |
| P4 | 审查修复（plan-mode-review-2026-08-12）：写黑名单反向判定 + 纯 Deny（缺陷 01/02）/ AgentState 锁下沉（缺陷 04）/ TUI 待决请求队列 + AllowCustom（缺陷 03）+ 并发回归测试 + 文档 | ✅ 2026-08-12 |

## 阶段 A（ADR-037）：用量展示 ✅

- **目标**：provider 捕获 token 用量（anthropic message_start/message_delta usage）→ 统一 `messages.Usage` → agent 回合级 `EventUsage` → AgentState 累计（`Usage` + `LastContextTokens`）→ TUI footer 实时上下文占用 + `/usage` 命令。
- **成功标准**：`harness run` / TUI 长对话 footer 显示 `ctx Nk/Mk`；`/usage` 显示会话累计 input/cache_read/output；resume 恢复累计用量。
- **状态**：✅ 已完成（2026-08-12，ADR-037，版本 0.7.1）
- **说明**：本阶段是"用量展示 + 上下文压缩"三阶段的第一段；后续 阶段 B（thinking 完整回传，ADR-025 修订）→ 阶段 C（LLM 摘要压缩）按 ADR-037 决策实施。

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| A1 | provider 捕获 usage 挂 EventDone + `messages.Usage` + `events.EventUsage` | ✅ 2026-08-12 |
| A2 | agent 透出 EventUsage（sampleResult.usage + emit + rc.attrs） | ✅ 2026-08-12 |
| A3 | AgentState.Usage/LastContextTokens + UsageMiddleware | ✅ 2026-08-12 |
| A4 | TUI footer ctx + /usage 命令 | ✅ 2026-08-12 |
| A5 | 测试 + 文档 + 版本 0.7.1 | ✅ 2026-08-12 |

## 阶段 B（ADR-025 修订）：thinking 完整回传 ✅

- **目标**：thinking 由"存审计不重放"改为"完整回传"（DeepSeek anthropic 端点实测可行：thinking 含 signature 回传 200 且计入 input_tokens）。捕获 thinking 块签名 → `Message.ThinkingSignature` + transcript `Line.Signature`（resume 恢复）→ provider 重放 `ThinkingBlockParam{Signature, Thinking}`（仅签名非空）。
- **成功标准**：长 thinking 会话 resume 后回传不 400；thinking-only assistant 带签名不再跳过；估算镜像含 thinking。
- **状态**：✅ 已完成（2026-08-12，ADR-025 修订 + ADR-037 第二段，版本 0.7.2）
- **说明**：阶段 C（LLM 摘要压缩）依赖本阶段——摘要请求经 `toAnthropicMessages` 完整回传 thinking，上下文基线才与正常采样一致。

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| B1 | provider 捕获 thinking signature（content_block_start union + EventThinkingDone.Signature） | ✅ 2026-08-12 |
| B2 | Message.ThinkingSignature + events.Event.Signature + transcript Line.Signature + resume 恢复 + agent.sample 捕获 | ✅ 2026-08-12 |
| B3 | toAnthropicAssistantMessage 重放 thinking 块（首块，仅签名非空）+ thinking-only 不跳过 | ✅ 2026-08-12 |
| B4 | 估算镜像（compact.EstimateTokens 含 thinking）+ 测试 + 文档 + 版本 0.7.2 | ✅ 2026-08-12 |

## 阶段 C（ADR-037）：LLM 摘要式压缩 ✅

- **目标**：上下文超阈值（context_window 85%，实际 usage 驱动 + 估算兜底）→ LLM 生成摘要（codex 方式：完整 conversation + 摘要 prompt 尾 user，无工具，max_tokens 4096；opencode 结构化模板 + previous summary 更新式）→ conversation 重写为单一 summary user（纯占位）+ 切新 transcript 段。自动（onReasoning before）+ 手动 `/compact`。
- **成功标准**：长会话自动压缩后 resume 重建为摘要占位；摘要失败终止 run 且不丢历史；Esc 中断压缩同失败处理；`/compact` 手动压缩并显示结果。
- **状态**：✅ 已完成（2026-08-12，ADR-037 第三段，版本 0.8.0）
- **说明**：三阶段（用量展示 → thinking 回传 → 压缩）全部完成；摘要请求经 `toAnthropicMessages` 完整回传 thinking（阶段 B 依赖），摘要模型看到的上下文与正常采样一致。

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| C1 | compact 核心：ShouldCompact(85%)/Summarizer(codex 方式)/Runner.Run + BuildSummaryPrompt(previous 更新式) | ✅ 2026-08-12 |
| C2 | RuntimeContext.Segment 钩子（session 注入 NewSegment+seed+Flush）+ CompactMiddleware（onReasoning before，失败终止） | ✅ 2026-08-12 |
| C3 | 挂载（build.go）+ agent.Compactor 访问器 + Controller.RunCompact + /compact + EventCompacted + TUI 系统行 | ✅ 2026-08-12 |
| C4 | 测试（compact 纯函数/Summarizer/Runner 失败与取消/agent 集成 EventCompacted/session resume 重建//compact TUI）+ 文档 + 版本 0.8.0 | ✅ 2026-08-12 |

## 阶段 C 修复轮（usage-compact-review-2026-08-12）✅

- **目标**：审查报告 12 项缺陷逐点决策修复——严重：thinking 签名捕获失效（signature_delta 分支）、压缩后同轮采样仍发旧上下文；中等：Summarize 忽略 rc.Model、Usage 累计虚高（改覆盖语义）、压缩后 Usage 归零；低风险：Segment 先落盘后重写、删除 state.Summary、估算固定开销补全、错误路径补发 EventCompacted、help/gofmt。
- **成功标准**：全部回归测试锁定（含"压缩后采样请求 = [summary]"锚点）；`go build/vet/test ./...` 与相关包 `-race` 全绿；ADR-037 勘误记录。
- **状态**：✅ 已完成（2026-08-13，ADR-037 勘误）
- **说明**：11（footer 非单调）待 12 修复后真实 API 实测再定；05（触发滞后）接受设计注释记录；多 thinking 块单签名边角未修（记待办）。

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| R1 | 12：anthropic_stream 补 signature_delta 分支 + 注释修正 + 流式测试 | ✅ 2026-08-13 |
| R2 | 01：CompactMiddleware in.Messages 回传 + Build 顺序交换（Compact 在 TodoReminder 前）+ 采样内容回归锚点 | ✅ 2026-08-13 |
| R3 | 02：Summarize rc.Model 优先 + 测试 | ✅ 2026-08-13 |
| R4 | 10+06：AddUsage→SetUsage 覆盖语义 + 压缩后归零 + /usage 展示 + 测试 | ✅ 2026-08-13 |
| R5 | 03+07：Runner.Run 先落盘后重写 + 删除 AgentState.Summary/BuildSummaryPrompt 更新式路径 + 测试 | ✅ 2026-08-13 |
| R6 | 04+05：EstimateTokens 注释修正 + Options.SystemPromptTokens 估算传入 + 滞后注释 | ✅ 2026-08-13 |
| R7 | 08：CompedKey 检查移到错误判断前 + 测试 | ✅ 2026-08-13 |
| R8 | 09：help 补 /usage /compact /thinking /rename + 全 gofmt | ✅ 2026-08-13 |
| R9 | 文档（DECISIONS ADR-037 勘误/TASKS/PROGRESS/review 更新）+ 全量测试与 -race | ✅ 2026-08-13 |

## 阶段 shell 进程树生命周期（ADR-038）✅

- **目标**：修复 shell 工具两个实测痛点——前台服务命令卡死会话（超时后 Windows 子进程残留 + 管道句柄继承卡 Wait）、Esc 中断不杀进程树；加 background/kill_pid 参数（长任务结构化解）+ 退出 pre-kill 清理。
- **成功标准**：Windows job 杀树测试全绿（孙进程断言死）；Esc/超时回填中断/超时语义；background 立即返回 PID+日志、kill_pid 杀树、退出清理全杀；审批 key/摘要/TUI/提示词适配；版本 0.9.0。
- **状态**：✅ 已完成（2026-08-13，ADR-038）
- **说明**：决策逐点经用户拍板（阶段 1+2 / background+kill_pid 配套 / 30s 超时不变 / Esc 杀树+回填 / 退出 pre-kill）。Esc 只杀前台进程树；background 进程不绑定回合（会话级资源，仅 kill_pid 与退出清理终止）。

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| S1 | 平台杀树层：background_windows.go（x/sys/windows Job Object 四函数 + taskkill 降级）/ background_unix.go（进程组 + pid<=0 防御）+ 前台 AfterFunc 杀树重构 + Esc/超时回填语义 | ✅ 2026-08-13 |
| S2 | background/kill_pid：注册表 sync.Map + startBackground（Go 直接启动 + 日志重定向 + <pid>.log）+ handleKill（仅注册表 PID）+ CleanupBackground + main.go defer | ✅ 2026-08-13 |
| S3 | 审批/schema/TUI/提示词适配：ApprovalKey kill 派生 + SummaryOf + toolCallSummary/applyToolResult + shellLongTaskGuidance 改写 + command 改 omitempty | ✅ 2026-08-13 |
| S4 | 退出兜底（SaveActiveState）+ ADR-038 + 文档 + 版本 0.9.0 + go mod tidy | ✅ 2026-08-13 |
| S5 | 全量验证 + e2e + 真实场景手动验证 + 提交 | ✅ 2026-08-13 |
| S6 | **超时转后台扩展**（ADR-038 勘误）：前台超时不杀树、自动转后台托管（文件输出 + select 三路 + tree 移交注册表）+ 测试语义反转 + 真实 python 服务验证 | ✅ 2026-08-13 |

## 阶段 系统提示通道重构（ADR-039）✅

- **目标**：消除两个痛点——Agent.instructions 与无状态架构的张力、Build 装配期兜底 token 估算注入（固定值阶段四会失效）。落地内容通道分类原则（对话历史=Messages / 稳定配置=系统提示管道 / 工具定义=toolspec / 即时信号=临时消息副本，对齐 codex/opencode）。
- **成功标准**：agent 不含提示词文本（rc.SystemPrompt 承载）；Build 无兜底注入；压缩判定时实时三项估算（messages+system+tools）；Runner 纯执行器 `Run(ctx, rc) error`；全量测试 + e2e 绿。
- **状态**：✅ 已完成（2026-08-13，ADR-039）
- **说明**：决策逐点经用户拍板（通道分类原则 / base 中间件化 / 判定挪 CompactMiddleware + 去 force / toolspec 保留独立字段——实测 7.3KB ≈ 1.8K token）。

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| T1 | rc.SystemPrompt 字段 + ComposeSystemPrompt 去 base 参数（起点 = rc.SystemPrompt）+ BaseInstructionsMiddleware（DefaultBaseInstructions）| ✅ 2026-08-13 |
| T2 | agent 删 instructions/SetInstructions + Run 组合回写 rc.SystemPrompt；build.go 删兜底块 + 链首注册 base | ✅ 2026-08-13 |
| T3 | compact：删 Options.SystemPromptTokens/SetSystemPromptTokens + EstimateSystemPrompt/EstimateTools + ShouldCompact(rc, tools) + Runner.Run error 化 + Runner.ShouldCompact | ✅ 2026-08-13 |
| T4 | CompactMiddleware 判定（in.Tools）+ Controller.RunCompact 手动路径适配 | ✅ 2026-08-13 |
| T5 | 测试改写（compact/chain/session）+ 新增（base_instructions/TestRunSystemPromptCompose 回归锚点/TestRunnerShouldCompact）+ 文档 + 全量验证 | ✅ 2026-08-13 |

## 阶段 6（TUI 后后续可选）：摘要式压缩 / grep 工具 / 双向通信

- **状态**：未开始（TUI 已提前为独立阶段；本阶段为压缩/grep/双向通信剩余项）
