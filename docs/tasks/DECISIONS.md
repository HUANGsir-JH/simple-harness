# 架构决策记录 (ADR)

> 每条决策：背景 → 选择 → 理由。写下来即不再回头讨论，除非有新信息推翻。

## ADR-001：多后端 Provider 用配置结构体 + 单一 HTTP 客户端

- **背景**：需要支持 OpenAI / Anthropic / OpenAI-兼容后端，最初设想是每后端一个实现目录。
- **选择**：不写多 provider 实现。`Provider` 接口只暴露 `BaseURL / APIKey / WireAPI / ContextWindow`，一个 HTTP 客户端根据 `WireAPI`（responses/chat）切换请求格式。
- **理由**：codex 源码中 Anthropic/Ollama/LM Studio 全部走 `ConfiguredModelProvider`（base_url + env_key + wire_api 配置），没有独立实现。这是多后端最简且验证过的模式。

## ADR-002：内部统一消息模型，provider 适配转换

- **背景**：换后端时若用各 provider 原生消息格式，会话存储/回放逻辑全部要重写。
- **选择**：核心层只操作统一 `Message`（role/content/tool_calls/tool_results），provider 适配层负责 ↔ 原生格式转换；JSONL 会话文件直接存统一模型。
- **理由**：多后端切换零迁移；JSONL 可读可调试；测试可用统一模型 mock。

## ADR-003：agent loop 错误二分类（RespondToModel / Fatal）

- **背景**：工具执行失败的处理方式决定 agent 容错能力。
- **选择**：`RespondToModel`（错误文本回填历史，循环继续）/ `Fatal`（终止 turn）。审批拒绝也是普通错误回填。
- **理由**：codex 最重要的容错语义——模型看到失败后可自行换思路重试，而不是杀掉整个任务。

## ADR-004：工具并发执行 + call_id 回填

- **背景**：模型可能一次返回多个 tool_call。
- **选择**：errgroup 并发执行全部，任一失败只影响自己；结果按 call_id 排序回填历史。
- **理由**：codex 主路径语义（FuturesOrdered 按完成顺序合并）；多文件修改显著加速。

## ADR-005：子 agent = 独立 session + fork 过滤 + 单向通信

- **背景**：需要子任务委托能力，又不想引入 mailbox/队列复杂度。
- **选择**：子 agent 是独立 session（goroutine 跑自己的 turn 循环）；fork 时只继承父的 user 消息 + assistant 最终答案（丢弃工具调用细节）；v1 只做 spawn_agent + 主→子单向 send_message。
- **理由**：codex `keep_forked_rollout_item` 的语义——子 agent 只需要"结论"不需要"过程"；单向通信覆盖主要委托场景。

## ADR-006：分层审批 + 黑白名单启发式 + 拒绝≠Fatal

- **背景**：无 OS 沙箱（Windows 实现成本极高），安全只能靠策略层。
- **选择**：三态策略（UnlessTrusted 默认/OnRequest/Never）；搬 codex `BANNED_PREFIX_SUGGESTIONS` 黑名单 + 只读安全命令白名单；TTY 弹 Y/n/remember，非 TTY 自动拒绝；拒绝结果回填模型。
- **理由**：黑名单启发式已验证；拒绝回填让模型换思路，不中断任务。

## ADR-007：会话存储 = 每会话一个 JSONL 追加写

- **背景**：需要持久化 + resume + 可调试。
- **选择**：`~/.harness/sessions/<timestamp>-<id>.jsonl`，存统一 Message 模型，`os.O_APPEND` 追加写。
- **理由**：JSONL 可读、流式持久化、resume 零成本（读回重放）；不做 SQLite/zstd（v1 无此需求）。

## ADR-008：UI 抽象为 Renderer 接口（simple 先行，TUI 后置）

- **背景**：CLI 交互体验要求（流式渲染），但 TUI 复杂度高。
- **选择**：`Renderer` 接口（Start/WriteText/WriteToolCall/WriteApprovalRequest/...），v1 实现 simple 渲染器（ANSI 彩色 + 文本流式），v2 实现 tui 渲染器插拔替换；`--json` 模式 = Renderer 的另一个实现。
- **理由**：接口先行，UI 演进不影响核心循环；--json 排障复用同一接口。

## ADR-009：压缩先 TokenBudget 式，后摘要式

- **背景**：长会话必然超窗，压缩是刚需；但摘要式需要额外 LLM 调用。
- **选择**：v1 TokenBudget 式（清空历史保留系统提示 + 最近 N 条 + 占位），v2 摘要式（单独 LLM 摘要 + 保留最近用户消息）。
- **理由**：codex 两种都有；TokenBudget 式 10 行可跑通，摘要式质量更好但成本高，分阶段合理。
- **修订（2026-08-07，ADR-023）**：增加**大工具结果 eviction**（>80K 落盘 + head/tail preview + read_file 指针，治"宽"）；**不做 overflow 安全网**（eviction 撑住宽度后模型超限概率大降，砍被动抢救）。TokenBudget 仅作 v1 保底，主路径为摘要式 + eviction。

## ADR-010：配置用 YAML + 环境变量覆盖

- **背景**：多后端/多模型需要可读配置；敏感信息（API key）不能进文件。
- **选择**：`~/.harness/config.yaml` + 项目级 `.harness.yaml`，YAML 定义 provider/model/approval 策略；API key 走环境变量（`OPENAI_API_KEY` 等）。
- **理由**：YAML 可读性好（用户选择）；key 不入文件安全；双层级（用户/项目）符合 codex 分层配置思路。

## ADR-011：AGENTS.md 向上搜索 + 注入 developer 消息

- **背景**：项目级指令注入是 agent 可用性的关键机制。
- **选择**：从 cwd 向上找项目根（.git 等 marker）→ 从根到 cwd 收集拼接 → 注入 developer 消息；200KB 截断。
- **理由**：codex 已验证的机制，~50 行实现，价值极高。

## ADR-012：错误重试依赖 SDK 内置，不自研

- **背景**：规划时计划自研指数退避重试（参照 codex `responses_retry.rs`）。
- **选择**：openai-go / anthropic-sdk-go 均内置指数退避重试（429/5xx/网络错默认开启），阶段一不做自定义重试；只做 SDK 错误 → `Event{Type: EventError}` 映射。
- **理由**：两 SDK 已实现该能力（探测确认），自研重复造轮子；真实验证时 DeepSeek 端点无重试问题。若后续发现 SDK 重试不够（如流中断恢复），再在 provider 层补。

## ADR-013：FakeClient/FakeStream 放非测试文件（provider/mock.go）

- **背景**：agent 包测试需要复用 provider 的 mock 客户端，但 `_test.go` 文件对其它包不可见。
- **选择**：将 FakeClient/FakeStream 放在 `internal/provider/mock.go`（非 _test 文件，带导出注释，var _ 接口断言）。
- **理由**：Go 测试辅助跨包复用的标准做法；文件小（~50 行）且明确标注测试用途，不影响生产代码体积。

## ADR-014：Anthropic SSE 测试 mock 必须带 event: 字段

- **背景**：Anthropic mock SSE 测试最初只发 `data:` 行，事件被 SDK 静默丢弃（3 个测试失败）。
- **选择**：anthropic 的 mock 事件同时带 `event: <type>` 字段 + data 顶层 `type` 字段（`anthropicSSE` helper）；openai 版不需要。
- **理由**：探测确认 anthropic-sdk-go 的 `Stream.Next()` 按 **SSE 的 event: 字段**路由（switch 匹配），openai-go 则按 data 内 type。这是两 SDK 的解析差异，写测试时必须区分。

## ADR-015：多 provider 多模型配置结构（default_provider + providers 分组）

- **背景**：阶段一配置是单模型结构（一个 provider/model/base_url/api_key），无法支持多个模型切换。
- **选择**：
  - 配置结构：`default_provider`（默认供应商）+ `providers: map<名> -> {wire_api, base_url, api_key/env_key, models: map<模型> -> {context_window}}`
  - **provider 名自定义**，API 类型由显式 `wire_api` 字段决定（openai/anthropic，默认 openai）
  - **context_window 每模型一个**，进 YAML；未配置回退 `DefaultContextWindow`(128k)
  - 模型选择优先级：`--model <名>` > default_provider 的 models 第一个
  - provider 选择：default_provider > providers 排序第一个
  - **删除 models.go 硬编码窗口表**，窗口完全来自配置
- **理由**：用户明确要求按 provider 分组 + 自定义供应商名；参照 codex 的 `model`/`model_provider` 分层设计；硬编码表在配置化后失去意义。

## ADR-016：默认模型语义修正——按 provider 排序取第一个而非字母序

- **背景**：`--model` 未指定时取 models map 排序第一个。真实 API 验证暴露问题：DeepSeek 只支持 `deepseek-v4-flash`/`deepseek-v4-pro`，但排序第一个是 `deepseek-v4`（不存在的模型）导致 400 错误。
- **选择**：默认取"配置里 models 的排序第一个"（实现不变），但**配置作者需确保第一个模型真实可用**（把可用的模型名放第一位，或用 --model 指定）。已把 config.local.yaml 中 `deepseek-v4` 改为真实可用的 `deepseek-v4-pro`。
- **理由**：codex 同款行为（catalog 自动默认取第一个）；配置化后模型可用性由配置负责，代码无法校验远端模型名。真实使用中 `--model` 是最可靠的指定方式。

## ADR-017：yaml 配置校验——加载时执行，一次返回全部错误

- **背景**：多 provider 配置化后，错误配置（无 models、非法 wire_api、缺 key）会导致运行时才失败，难以排查。
- **选择**：`Config.Validate()` 在 loadConfig 后立即执行（加载时校验，不合法直接退出不发起请求）。校验内容：providers 非空、default_provider 存在、wire_api 枚举（openai/anthropic）、models 非空、模型名非空、context_window >= 0、key 来源（api_key 或 env_key）非空。**一次返回全部错误**（多行），便于一次修完。
- **理由**：加载时校验发现早、错误信息全；比 Resolve 时校验更贴近用户（配置问题应配置层解决）。

## ADR-018：测试多 provider 必须切换 default_provider，不能依赖 --model

- **背景**：`--model <名>` 只在当前选中 provider 的 models 里查。两个 provider 有同名模型时，`--model` 仍命中第一个 provider，造成"测了 anthropic wire"的假象（实际还是 openai wire）。
- **选择**：测非默认 provider 时，临时改 `default_provider`（或提供 --provider flag）后再测；测试脚本做好配置备份与还原。
- **理由**：`--model` 的选择域是"当前 provider 内"，跨 provider 测试必须从 provider 选择层切入。

## ADR-019：anthropic wire 必须显式设置 Authorization: Bearer 头

- **背景**：deepseek-claude（anthropic wire）调用持续 401，但 key 对 openai wire 有效、curl 也 200。对照实验（纯 Go http.Client 直连 ± Authorization 头）确认：**系统代理/全局软件会在所有出站请求注入 `Authorization: Bearer PROXY_MANAGED` 头**，而 DeepSeek 等兼容端点**优先读 Authorization 头**，导致正确的 `X-Api-Key` 被无视 → 401。
- **选择**：anthropic 适配层在 `WithAPIKey`（X-Api-Key 头）之外，**追加 `option.WithAuthToken(apiKey)`** 显式设置正确的 `Authorization: Bearer 真实key`，覆盖系统注入的假头（双保险）。
- **理由**：key 有效、端点兼容，问题在鉴权头被污染；显式设置正确头是最小且通用的修复。OpenAI wire 无此问题（其 Authorization 本就是真实 key）。

## ADR-020：thinking 推理模式——模型级配置 + 按协议标准参数传递

- **背景**：DeepSeek V4 等模型支持 thinking（推理）模式，默认启用、档位 low/high/max。框架需默认启用、支持多档位、运行时可修改。
- **选择**：
  - **配置**（model 级）：`thinking: {enabled, efforts}`。`enabled` 默认 true；`efforts` 是模型支持的档位集（默认 `[low, high, max]`），覆盖默认集，未配置回退默认。
  - **当前档位默认 high**（openai/anthropic 两协议一致）；high 不在 efforts 内时取 efforts 第一个。efforts 做白名单校验。
  - **运行时覆盖**（CLI 优先于配置）：`--effort <档位>`（须在模型 efforts 内，否则报错）、`--thinking` / `--no-thinking`（互斥 bool）。
  - **传递**（各 wire 的 SDK 标准参数，非后端特化字符串）：
    - anthropic（Messages）→ `thinking: {type: enabled, budget_tokens}` + SDK `output_config: {effort}`；关闭传 `thinking: {type: disabled}`
    - ~~openai（Responses）→ `reasoning: {effort: low|high|max}`；关闭传 `effort: "none"`~~（openai wire 已于 2026-08-07 移除，见 ADR-022）
  - **关闭必须显式传关闭表达**：DeepSeek 等兼容端点默认开启 thinking，"不传参数"无法关闭（不传 = 走模型默认 = 开）。
- **理由**：配置与传递都是通用语义 / 协议标准字段，DeepSeek 只是恰好兼容，不写任何后端特化。efforts 列表让模型粒度声明支持档位，运行时 --effort 在集内校验。anthropic 的 `output_config.effort` 是 SDK 官方字段（DeepSeek 兼容端点支持）。
- **注意**：openai wire 关闭时的 `effort: "none"` 是 DeepSeek 等兼容端点在 OpenAI 格式内的关闭约定；标准 OpenAI o 系列 effort 仅 low/medium/high，若后续接入需按官方语义适配关闭。

## ADR-022：移除 openai wire，只保留 anthropic Messages（单 wire）

- **背景**：阶段一实现 openai（Responses）/ anthropic（Messages）双 wire。AgentScope 调研后，用户决策：**抛弃 openai wire——Responses 与 Chat Completions 都不要**（曾短暂评估迁移到 Chat Completions，用户进一步明确 openai 生态整体不要）。
- **选择**：移除 `internal/provider/openai.go` 及 openai 相关测试；`WireOpenAI` 枚举、`DefaultEnvKey`、config 校验 / loadConfig 相应调整；provider 只保留 anthropic Messages 一个 wire。DeepSeek 等兼容端点只能走其 anthropic 兼容端点。
- **理由**：
  - provider 单一 wire = 最大 simple：thinking 一种传递方式、事件一种形状、适配层一个，无双 wire 归一双重逻辑。
  - 用户判断：与本项目最终目标（架构设计——middleware / loop / 事件 / 权限——的实践与复用）不背离；多后端接入是加分项非必需，接入面收窄的代价可接受。
- **代价（知情）**：阿里 qwen（仅 openai 兼容）、DeepSeek openai 格式不再支持；config.local.yaml 中 qwen / deepseek（openai wire）provider 需移除或停用；DeepSeek 只能走 deepseek-claude（anthropic wire，ADR-019 的 401 坑仍需 WithAuthToken 覆盖）。
- **影响 ADR**：ADR-020 的 openai 传递部分作废；ADR-001 / ADR-015 中"两 wire"相关描述以本文为准。

## ADR-024：middleware 贯穿 ctx + 工具说明注入 + tool_result 多块合并

- **背景**：阶段二实现工具闭环时确认三处设计细节。
- **选择**：
  1. **middleware hook 贯穿 `context.Context`**：执行链需要 ctx（取消/超时），而 RuntimeContext 是 per-call 元数据（SessionID + attrs，单用户无 UserID）不含 ctx → 各 hook 签名 `(ctx context.Context, rc *RuntimeContext, in X, next)`。
  2. **工具说明注入系统提示**（onSystemPrompt middleware）：codex 调研证实 apply_patch 工具 description 只有一句，语法靠 freeform grammar 字段（anthropic wire 无此机制）；codex 在系统提示 `# Tool Guidelines` 注入完整语法 → 阶段二用 ToolInstructionsMiddleware 注入"工具列表 + apply_patch 语法"（阶段四 AGENTS.md 等在此追加）。
  3. **tool_result 多块合并**：anthropic 要求 tool_use 后的**下一条消息**含全部对应 tool_result；多工具调用回填多条独立 tool_result 消息触发 400（真实 API 暴露）→ 一批工具结果合并成一条消息（`messages.Message.ToolResults []ToolResultBlock`），provider 转多块。
- **理由**：ctx 贯穿是执行硬需求；工具说明必须注入否则格式敏感工具不可用；多块合并满足 anthropic 协议约束（不按完成顺序，按调用顺序回填）。

## ADR-023：Workspace 统一目录 + Compaction 范围 + 子 Agent 形态（AgentScope 调研第三轮）

- **背景**：AgentScope 调研（ADR-021）确认 middleware / loop / 状态快照 / 权限后，继续确认 workspace / compaction / 子 agent 三点。
- **选择**：
  1. **Workspace = `~/.harness/` 统一目录**，作为 agent 数据唯一事实源：`sessions/`（JSONL + AgentState 快照）、`subagents/*.md`（预留）、`tools.json`（工具 allow/deny）、`memory/`、`plans/`（后续）；AGENTS.md 保持项目级向上搜索（两源拼接注入）。
  2. **Compaction**：TokenBudget v1（保底）+ 摘要式（主）+ 大工具结果 eviction（>80K 落盘 + head/tail preview + read_file 指针，治"宽"）；**不做 overflow 安全网**。
  3. **子 agent**：**内置几个**（general-purpose 等）+ **允许并行** + **状态跟踪**（pending / running / completed）；**自定义声明式预留**（subagents/*.md 留扩展点，不实现）；保留 fork 过滤 + 主→子单向。实现细节阶段五探讨。
- **理由**：
  - workspace：一个目录 = 一个 agent 的全部数据，好理解 / 好备份 / 好调试；不引入多租户与 filesystem 抽象（本地目录约定即可）。
  - compaction：eviction 治宽、摘要治深，两个主动手段够用；砍 overflow 被动抢救，避免阶段四膨胀。
  - 子 agent：内置几个覆盖主要委托场景，免自定义注册复杂度；并行 + 状态跟踪是"真并行可观测"的核心；声明式仅预留扩展点。

## ADR-021：架构修订——进程内 Middleware + 纯 loop + AgentState 轻量快照 + 三档权限（AgentScope 调研）

- **背景**：阶段一完成、阶段二开始前，用户提供 AgentScope Java v2 文档（`agent-scope-llms.txt`），要求模仿其功能/实现完善 simple-harness 的设计。通读其核心文档后，经问答确认 4 项架构修订。
- **选择**：
  1. **扩展机制：进程内 middleware 为核心**。新增 `internal/middleware` 包，定义 5 个 hook：`onAgent` / `onReasoning` / `onActing` / `onModelCall`（onion：包 next、可观察事件流）+ `onSystemPrompt`（transformer 链）。原计划的子进程 hooks（PreToolUse/PermissionRequest/Stop）**降级为远期**（作为 middleware 的一种外部实现）。
  2. **agent 纯 loop + 挂载点**：agent 核心循环只做 采样→工具→回填；压缩/权限/记忆/AGENTS.md 注入全部作为 middleware 挂载。**阶段二即搭挂载点骨架**，避免工程能力揉进 agent.go。挂载映射：`onActing` = 权限扩展点（阶段三 approval）；`onSystemPrompt` = 系统提示词拼接 + AGENTS.md 注入（阶段四 agentsmd）；`onReasoning` = 压缩（阶段四 compact）。
  3. **会话 = JSONL + 轻量 AgentState 快照**：消息仍追加 JSONL（保留换后端零迁移与可读性）；另存一份可序列化运行时状态快照（权限规则/todo/plan 指针/摘要），`resume` 恢复完整会话（不只消息）。
  4. **权限保持三档**：阶段三只做 readonly / acceptedit / bypass + 黑白名单 + TTY 交互 + allowlist，以 onActing middleware 挂载；AgentScope 的复杂规则匹配引擎（Rules + Mode + 不可绕过 Checks、suggested-rules 记忆）**不强做**，由 middleware 挂载点天然承载后续演进。
- **理由**：
  - middleware 化让工程能力可单测、可插拔、不污染核心循环（AgentScope 第一支柱："capabilities 叠加在 loop 上，不揉进 loop"）。
  - 会话快照解决"消息流无法表达权限/todo/plan 等非消息状态"的 resume 缺口，又保留 JSONL 的零迁移/可读性（换后端无需迁移）。
  - 权限三档符合 simple 定位：复杂规则匹配是增强不是刚需，middleware 机制先行即可，不超前设计。
- **参考**：AgentScope Java v2 文档（`agent-scope-llms.txt`；重点篇目：architecture / middleware / message-and-event / context / permission-system / workspace / compaction / memory / subagent）。

## ADR-025：Workspace 项目分桶 + AgentState 注入机制 + 块级 transcript（2026-08-08，提前自阶段四）

- **背景**：用户要求把阶段四的 workspace + AgentState 提前到阶段三（权限）之前，理由：todo 工具可挂 AgentState 持久化、每次运行记录可落盘。参考 codex（目录即元数据 + 异步 writer）+ AgentScope（AgentState 独立快照、todo 进 state、双轨 transcript）+ **用户自定义项目分桶结构**，修订 ADR-023 的扁平 `sessions/` 布局。
- **选择**：
  1. **目录结构（项目分桶）**：`~/.harness/workspaces/<项目路径转义>/<session-id>/{historys, plans, agentstate.json}` + 全局 `agents.md`（persona，总是加载，阶段四注入）+ `config.yaml`。扩展目录三层：全局（subagents/tools.json/memory/logs）/ 项目（allowlist/subagents）/ 会话（plans/evictions）。转义 `D:\a\b → D__a_b`（保留盘符）；session-id = `<时间戳>-<8hex>`（目录名即 id）。
  2. **historys 多文件**：`history-<n>.jsonl`，**压缩切新文件**，新文件以"摘要+保留最近"开头；resume 只读**最大序号文件**（最新文件即有效历史），旧文件纯审计。
  3. **块级 transcript + 异步 writer**：thinking 结束 / text 结束 / 工具调用结束 / 工具结果 各写一行（每行带 ordinal）；**单后台 goroutine 消费 channel（FIFO 保序）+ ordinal 在 writer 内自增**（resume 按序加载兜底）——解决异步写盘的顺序与并发安全；用户消息输入即写、meta 首行。thinking **存但不重放**（`Message.Thinking` 存审计，provider 重放 assistant 时忽略，免 anthropic 格式适配）。
  4. **AgentState 注入机制**：独立 `internal/agentstate` 包（middleware 与 session 都只依赖它，避免循环引用）；`RuntimeContext.State *agentstate.AgentState` 字段；`session.StateMiddleware` 挂 onAgent（before 加载 / after 保存，对应 AgentScope call() load/save）；**工具 Handle 签名加 `rc *middleware.RuntimeContext`**（todo 等经 rc.State 读写）。
- **理由**：项目分桶贴合"我在哪个项目用 harness"（resume 从 cwd 定位，FindProject 逐级向上）；块级事件流使中断零丢失、resume 渲染逐块回放；AgentState 独立包解循环依赖；todo 挂 state 是 AgentScope tasksContext 对位，为 todo 工具铺路。
- **影响 ADR**：ADR-023 的 `sessions/` 扁平布局以本文为准；ADR-021 第 3 点"JSONL + AgentState 快照"细化为块级 transcript + 注入机制。

## ADR-026：无状态 agent 架构 + 运行时切换（会话/模型/推理强度）（2026-08-08）

- **背景**：进入 todo 工具阶段前，用户要求优化代码结构，并明确未来需求——**进程内 resume 切换 session、多个 agent 并行**、运行时切换模型与推理强度。用户提出对齐 AgentScope：**无状态的 harness agent，其余全部由 RuntimeContext 和 AgentState 承载**。推演并行场景后发现"每会话一个 agent/chain（SessionApp）"方案过度：无状态化后一个共享 agent + 一个共享 chain（全部中间件无状态）即可被多 goroutine 并发 Run。
- **选择**：
  1. **agent 完全无状态**：`agent.Run(ctx, rc, onEvent)` 去掉 thread 参数，消息序列从 `rc.Messages`（*messages.Thread，命名对齐 `provider.Request.Messages` 避免与并发线程混淆）读写。agent 不持有任何会话状态；每次 Run 传独立 rc → **切换会话 = 换 rc，并行 = 每 goroutine 一个 rc**（零共享可变状态）。
  2. **RuntimeContext 承载会话**：新增字段 `Messages / StatePath / Model / ThinkingEffort / ThinkingEnabled`。`session.Session.RuntimeContext()` 从会话一次填满（每轮新建）。**修复了此前 `sample()` 内 `wrapped(ctx, nil, ...)` 丢 rc 的 bug**（onModelCall 中间件拿到 nil rc，读 rc 会解引用错误）。
  3. **SessionMiddleware 无状态化**：去掉 `Sess` 字段，改从 `rc.StatePath` 读 load / 写 save（rc.State 预置则跳过）。零共享可变状态 → **共享 chain 可被多个 goroutine 并发 Run**（并行 agent 架构可扩展的基石；阶段五落地）。
  4. **per-call 模型/档位覆盖**：`provider.Request` 增加 `ThinkingEnabled *bool / ThinkingEffort string`（`Model` 字段本就存在但 anthropic 适配器忽略，补上尊重）。三态语义：`nil / 空 = 继承 client 默认`。`AgentState` 增加 `ThinkingEnabled *bool / ThinkingEffort string` 持久化（会话级模型/档位，resume 恢复）。`anthropicClient.Stream` 按 `req.*` 优先、client 默认兜底。
  5. **配置统一 init（Runtime 惰性单例）**：`loadConfig/configCandidates` 从 cmd 迁入 `provider.LoadConfig`；cmd 层 `Runtime{Config, Resolved}`（Resolved = 默认模型）经 `defaultRuntime()`（sync.Once）惰性加载一次，所有命令复用。惰性而非包 `init()`：version/help/sessions 不需要配置，配置错误须能被命令捕获。
  6. **REPL 会话注册表 + 命令**：`replCtx{open map[string]*Session, active}`；`/switch <id>|--last`（未开 → proj.Resume 加入，已开 → 复用）、`/model <name>`（Resolve 校验 + 重置 effort 为模型默认）、`/effort <level>`（Resolve 校验 efforts 白名单）。CLI flags（--model/--effort/--thinking/--no-thinking）经 `(*Runtime).resolveFlags` 校验后落到会话 state（随 SessionMiddleware 落盘）。
- **理由**：无状态 agent 是并行/切换的最简解（无每会话重建开销、无共享可变状态）；会话=运行时隔离单元、模型/档位归 AgentState（持久 + resume 恢复）对齐 AgentScope "状态经 context + AgentState 承载"；Runtime 惰性单例是后续多使用全局变量的统一入口模式。
- **影响 ADR**：ADR-025 的 StateMiddleware（持 Path）以本 ADR 的无状态 SessionMiddleware（rc 驱动）为准；ADR-021 第 2 点"agent 纯 loop"进一步明确为"完全无状态"。

## ADR-027：todo 工具（update_todo 全量替换 + 跨轮偏离提醒）（2026-08-08）

- **背景**：进入阶段三（权限）前的第一个功能。用户要求先参考开源实现再定设计。调研三个参照源：codex `update_plan`（事件型，只转发前端展示、零存储；全量替换；explanation 可选）、opencode `todowrite`（持久化 SQLite `(session_id, position)` 主键、无 id；纯工具回填可见性；prompt 引导写最好；权限一等公民）、AgentScope tasksContext（todo 进 AgentState 快照——我们已选路线）。
- **选择**：
  1. **全量替换语义**：`update_todo {todos:[{position, description, status}]}`，模型每次传**完整**列表整体重建（对齐 codex/opencode；原子、防漂移、天然支持"删除"）。**不做增量 add/update/remove**。
  2. **模型显式填 `position`** 维护顺序（对齐"md 有序列表"心智）；`TodoItem` 改 `{Position, Description, Status}`，**删 `ID`**（opencode 同样无 id，位置即身份）。`ReplaceTodos` 按 position 稳定排序。**不做 handler 归一化**（one in_progress 靠 prompt 约束，模型传几个存几个——codex/opencode 都如此）。
  3. **可见性 = 工具结果回填 + TodoReminderMiddleware**：工具返回渲染后的 checklist 回填历史（基础）；另参照 Claude Code 的 system-reminder，todo 非空但模型连续 ≥10 次 model call 未更新时，在**请求消息尾部**注入提醒段。提醒注入**临时副本**，不写 conversation（不落盘、resume 不重放，一次性注意力拉回）。
  4. **prompt 引导详尽**：工具 description 抄 opencode `todowrite.txt` 风格（When to use 3+ 步骤 / When NOT / 状态 / Rules：完成即标 completed 不攒、同时一个 in_progress、被阻塞保持 in_progress 并加 follow-up）；系统提示 `ToolInstructionsMiddleware` 追加 `# 任务管理` 引导段。
  5. **不做**：priority/cancelled 维度（全量替换天然支持删除）；`explanation` 参数（前端面板才有用，无前端即删）；权限（留阶段三 onActing）；handler 归一化。
  6. **并发**：tools 包级 mutex 保护 `rc.State.Todos` 与 rc.attrs todo 计数键写（并行工具同轮并发 update_todo）。
- **理由**：全量替换 + position 自维护是三个参照实现里最简单且跑得通的组合（opencode 靠它生产运行）；提醒机制补上"纯回填可见性"防漂移的缺口，且不引入新概念（走 rc.attrs + onReasoning middleware）；todo 挂 rc.State.Todos 随 SessionMiddleware after 无条件落盘（Fatal 也不丢），resume 恢复零新代码。
- **影响 ADR**：ADR-025 第 4 点"todo 挂 state"落地；ADR-021 挂载映射中 `onReasoning` = 压缩，现补充 TodoReminder 也挂 onReasoning（不同中间件叠加）。
