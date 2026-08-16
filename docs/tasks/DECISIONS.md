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
- **修订（2026-08-09）**：`FindProject` 由"逐级向上匹配已存在桶"改为**精确匹配启动目录（pwd 结果）**。原因：项目根桶先存在时，子目录任务（如 `just-for-test/case03`）被归并进根桶，与"每个启动目录独立记录"的预期不符（实测 case03 会话落进 `D__agent-project_harness` 桶）。桶 = `<workspaces>/<EscapePath(cwd)>/<session-id>`，与 state.CWD 一致；`store_test.go` TestFindProject 相应更新。
- **修订（2026-08-12，ADR-037 第二段：thinking 完整回传）**：第 3 点"thinking **存但不重放**"改为**完整回传**（存且重放）。原因：DeepSeek anthropic 兼容端点实测（Python 原始 HTTP）——返回 thinking 块含 signature；**thinking 块（含 signature）回传 → 200** 且计入 `input_tokens`（99→136）；不带 signature 也 200（DeepSeek 宽松）。1M context_window 足够容纳大 thinking（可达 1.6万~3万字符/轮）。实现：`anthropic_stream` 在 content_block_start 捕获 thinking 块 `Signature`（SDK union `ContentBlockStartEventContentBlockUnion` 已含，v1.61.0）→ `Message.ThinkingSignature` + transcript `Line.Signature`（resume 恢复）→ `toAnthropicAssistantMessage` 首块插入 `ThinkingBlockParam{Signature, Thinking}`；**仅签名非空时重放**（严格端点校验签名，无签名回传 400；DeepSeek 恒返回）；thinking-only assistant（content 空 + 无 tool_calls）由"跳过"改为"带签名则重放"（`toAnthropicMessages` 跳过条件 = content 空 + 无 tool_calls + ThinkingSignature 空）。估算镜像随之纳入 thinking 文本/签名（`internal/compact.EstimateTokens`，阶段 C 兜底）。

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

## ADR-028：工具结果截断中间件 + Esc/Ctrl+C 用户中断 + shell 长任务缓解 + state.CWD 修正（2026-08-09）

- **背景**：三个能力缺口（长工具结果模型无法读全量 / 用户无法主动中断 agent / 模型卡在慢 shell 命令），调研 codex（unified_exec HeadTailBuffer / turn_aborted）+ opencode（truncate.ts 落盘 / ctx.abort）+ 现状分析后定案。用户两点反馈：截断**上收为中间件**、中断用 **Esc 键触发**。
- **选择**：
  1. **工具结果截断中间件**：新建 `middleware.ToolOutputMiddleware` 挂 onToolCall，after 阶段改写 `rc.Messages` 本批新增 tool_result 消息的 Content。工具返回完整结果（职责纯，删工具内 truncate），截断策略一处定义。截断 = head 前 50% + tail 后 50%（各 10KB）+ 中间省略计数 + 全量落盘 `<会话目录>/evictions/` + 绝对路径 + read_file/grep 提示。rc/StatePath 空退化纯截断（测试/非会话）。**transcript 记完整**（审计全量），conversation（模型上下文）记 preview+路径（resume 重建后模型可见全量历史）。
  2. **Esc/Ctrl+C 用户中断**：REPL 改**单一读方事件循环**（`readStdinEvents` goroutine 逐 rune 读 stdin → channel，raw mode 下 Esc(0x1b)/Ctrl+C(0x03) 实时捕获）。中断 → cancel 当前回合 runCtx（下一轮新建 ctx 不受影响）+ `AddUser` 系统提示落盘（resume 可见，对齐 Claude Code）。引入 `golang.org/x/term`（raw mode；stdin 非 TTY 降级普通读行）。runCmd 单轮用 `withEscInterrupt`。
  3. **shell 长任务缓解 A+B**：A = 系统提示追加 `# 长耗时命令` 引导（bash `cmd > log 2>&1 &` / PowerShell `Start-Process` 放后台 + read_file/grep 轮询日志，不盲目重试超时）；B = 超时/非零退出时已收集输出经 `EvictContent` 落盘，错误带路径（模型可读进度）。
  4. **state.CWD 修正**：`Project.Create(model, cwd)` 存**会话启动的进程 cwd**（此前误存 FindProject 项目根 `p.Path`，可能 ≠ 启动目录）。bucket 归属仍由 `Project.Path` 决定——启动目录与项目根解耦。
  5. **描述一致性**：文件工具（read/list_dir/glob/write_file/apply_patch）description 改"相对进程工作目录或绝对路径"（诚实描述现状：按 `os.Getwd()` 解析、接受任意绝对路径；路径边界留阶段三 onActing）。
- **理由**：截断中间件化让工具职责纯、策略一处可插拔（对齐 opencode `Tool.define` wrap 层）；head/tail 双端保留让模型看到输出开头与结尾（日志错误常在尾部），全量落盘供 read_file/grep 读；Esc 中断是 TUI 惯例（Claude Code/opencode），事件循环单一读方避免多 reader 竞争 stdin；transcript 记完整是审计精神（ADR-025），落盘 evictions/ 让"长结果模型自读"（ADR-009 eviction 设计提前落地）。
- **影响 ADR**：ADR-009/023 的 eviction（>80K 落盘）落地为 20K 阈值 + head/tail；ADR-025 transcript 记完整不因截断丢失；ADR-021 `onToolCall` 挂载点新增 ToolOutputMiddleware；tools 包移除 truncate/MaxOutputChars（迁 middleware）。

## ADR-029：工具审批——三档权限 + onActing middleware + 会话级记忆（2026-08-09）

- **背景**：阶段三权限审批开工。调研 codex（Rust：Permission Profile + Approval Policy 两层正交、审批流水线 hook→guardian→user、审批缓存 key 含命令+cwd、命令规范化）+ opencode（TS：工具主动 ask、规则集有序 + 最后匹配胜出、决策粒度=工具名+资源模式、拒绝三分类含 CorrectedError、级联拒绝）后，结合既有 `onActing` 挂载点（ADR-021 预留）+ `AgentState.Permission` 预留字段（ADR-025）落地。
- **选择**：
  1. **三档模式**：`readonly`（只读操作放行，写操作/shell 询问）/ `acceptedit`（只读+编辑放行，shell 询问；默认）/ `bypass`（全部放行）。**config `approval.mode` 是默认权限（可不配置 = acceptedit）**：会话创建时播种进 `AgentState.Permission.Mode`（`Project.Create` 加 mode 参数），运行时 `/permission <mode>` 会话级切换（`Session.SetPermissionMode`，立即落盘）——审批模式完全由会话 state 决定，config 只在创建时播种。
  2. **决策粒度**：工具分类（只读集合 / write_file+apply_patch / update_todo 低风险 / shell_command）+ shell 黑白名单前缀/子串匹配（白名单安全命令 `ls cat git status` 等放行；黑名单危险命令 `rm -rf sudo curl|sh` 等触发审批）。命令先**规范化**（trim + 折叠空白 + 取前 2 token，`git status --porcelain` → `git status`，对齐 opencode arity 理念）。
  3. **审批交互**：`Approver` 接口（middleware 包定义避免循环依赖）+ `channelApprover`（CLI 注入 rc.Approver）。REPL/runCmd 主循环经 channel 协调（单一读方原则，ADR-028）：审批请求发 channel → 主循环 select 打印 UI → 下一行输入路由为答复 y/s/n。**非 TTY / 无 approver → 自动拒绝**（回填模型换思路）。
  4. **拒绝 ≠ Fatal**：审批拒绝返回自定义 `middleware.DeniedError`（非 ToolError），agent 调用层捕获后作为失败结果回填、循环继续（不取消整批）。工具自身错误仍走 ToolError 二分类（acting core 内部处理）。
  5. **会话级记忆**：用户按 s（本会话记住）→ key 记入 `AgentState.Permission.Approved`（shell 用规范化命令前缀，其它工具用工具名），随 AgentState 落盘跨轮生效。**不做**全局 allowlist.json。
- **理由**：三档对齐 IMPLEMENTATION_PLAN 既定设计 + codex SandboxMode；决策粒度按"工具名 + 命令内容"（opencode 已验证）比纯工具名更安全；DeniedError 独立类型让"策略拒绝"与"工具失败"语义分离（比借用 ToolError 更清晰，用户建议）；会话级记忆是用户明确选择（跨会话持久记忆风险高）；channel 协调复用既有单一读方架构（不新增 stdin 读者，避免竞态）。
- **留增强**：级联拒绝（opencode：拒绝时同批 pending 一并拒）、全局 allowlist（持久化"以后允许"）、拒绝反馈（opencode CorrectedError：用户填理由给模型改写）、bash 命令语法解析（tree-sitter/arity 前缀表）、guardian 自动审批、复杂规则集。
- **影响 ADR**：ADR-021 `onActing` 挂载点落地；ADR-006（分层审批 + 拒绝≠Fatal）实现；ADR-025 `AgentState.Permission` 从预留到实现（Mode + Approved）；ADR-003 错误二分类补充 DeniedError 第三路径（策略拒绝，非工具错误）。

## ADR-030：TUI（bubbletea 全屏交互 UI，替代 REPL）（2026-08-09）

- **背景**：REPL 的行式流输出 + 提示符混流影响项目测试（termtest 断言依赖时序且覆盖有限）；用户决定把 TUI 提前到现在（TASKS.md 阶段 6 → 子 agent 之前，阶段 4/5 剩余后置），TUI 上线后 **REPL 整体删除**。调研 codex（`codex-rs/tui`，ratatui 全帧 diff 重绘 + 命令式 App 状态机 + FrameRequester 合并重绘）+ opencode（`packages/tui`，Solid 组件树 + 原生渲染内核 + 增量 store + 16ms 批量合帧）提炼共性：渲染与输入/agent 解耦、事件驱动增量更新、组件化视图、流式 cell 完成合并、UI state 独立 reducer、**无 TTY state-machine 测试**。
- **选择**：
  1. **技术栈**：`charmbracelet/bubbletea` v1.3（elm 架构 Model/Update/Msg，Update/View 纯函数可测）+ `bubbles` v1.0（viewport/textarea/spinner）+ `lipgloss`（排版）+ `glamour` v1.0（markdown→ANSI）+ `hexops/gotextdiff`（write_file 覆盖 diff）。bubbletea 自研终端层（x/term + termenv，无 tcell 重依赖），Windows 支持完善（conpty）。
  2. **入口**：`harness`（无子命令）与 `resume` 进 TUI；**REPL 删除**（runREPL 逻辑删，`repl()` 留薄壳调 `tui.RunTUI`）；`run` **保留**流式非交互（脚本/CI）；非 TTY 下 `harness` 无子命令报错提示用 `run`；`--json` 仅 `run` 支持（TUI 全屏不兼容）。
  3. **队列**：run 期间 textarea 可编辑，Enter → `pending []string` 队列（**不落盘**，队列条显示输入框上方，多行可滚）；`turn_done` 逐条自动连跑；Esc 中断保留队列；审批弹窗与队列互斥；`/exit` 丢弃队列。
  4. **斜杠命令**：`/switch` `/model` `/permission` `/effort` **弹窗选择器**（↑/↓ + Enter + Esc，复用审批弹窗框架），选项列表**实时从配置获取**（provider config / 模型 thinking.efforts），弹窗只显示选项，不显示描述栏；**自动补全首版做**；执行反馈用**系统行**；**命令统一进队列**（消费按 `/` 前缀分派命令 / 普通文本发 agent，运行中切换天然解耦）；**命令落盘**（transcript 新增 `command` 行）**但 load 时不进 conversation**（模型不可见），resume 渲染系统行（对齐 thinking 存但不重放，ADR-025）。
  5. **键鼠**：单焦点 + Tab 切换（输入区↔消息区）；**Ctrl+C = 复制**（textarea 选中，x/exp/clipboard，不可用 no-op）；Esc = 中断回合（ADR-028 保留）；退出 = 仅 `/exit`；输入历史（空输入 ↑，进程内内存）；鼠标首版 = 点击工具块展开/收起 + 滚轮消息滚动 + 点击输入聚焦（WithMouseCellMotion，行号反查 UI 元素）。
  6. **工具展示 = 折叠块 + 按工具分派**（对齐 opencode ToolPart + collapse-tool-output / codex HistoryCell）：消息流内插；默认高 6 行超出折叠、点击/Enter 撑开滚动；失败态红 `[ERR]` + 错误 + 已收集输出。分派表：read_file 折叠态显示单行元信息、展开态显示文件内容；write_file 元信息 + **覆盖时 gotextdiff**（新建可展开内容）；apply_patch 从 args.patch 提取 +/- 行 diff（无库）+ 文件列表；list_dir/glob 前 5 枚举 + 计数（纯名称）；update_todo 完整 checklist；shell_command 完整 command + 输出折叠块（超长落盘提示）。
  7. **thinking**：流式灰显 + 块完成折叠 `[thinking]` 一行，点击展开；历史 thinking 折叠展示（resume 可见）。
  8. **切换 session**：消息区**全量替换**为新 session 历史（重建首屏，非 REPL 增量）；工具区/stream/审批清空、状态栏会话 id/todo 更新；切换时队列清空、输入历史跨 session 共享。
  9. **风格约束**：全程不用 emoji/彩色图标——状态用纯文本或 ASCII + 颜色（成功绿 / 失败红 / 进行中黄），如 `[OK]`/`[ERR]`/`[RUN]`。
  10. **md 渲染**：模型普通输出基本是 markdown；**流式纯文本 → text_done 块完成 glamour 渲染完整 markdown 替换**（对齐 codex streaming cell → 完成合并；每块渲染一次，非逐 token）。
  11. **测试策略**：单测为主 + e2e 尽量全面——T1 Model.Update 无 TTY 单测（消息流/工具状态机/审批/队列/切换/历史/命令消费）；T2 View 关键内容包含断言（非整串快照）；T3 事件桥 + T4 审批桥单测；T5 e2e termtest 尽量全面（prompt→回复/审批 y/n/exit/resume 首屏/switch/队列连跑/thinking/工具块展开/非 TTY 报错/run 保留）；**人工测试清单**（鼠标点击/中文 IME/Ctrl+C 复制/resize/长输出性能）完成后用户实测；T6 既有测试零回归（agent 核心不改）。
- **理由**：TUI 对症 REPL 测试痛点（elm 纯函数可测，无 TTY）；bubbletea 是 Go agent CLI 主流、依赖树小、Windows 完善；删 REPL 避免双交互入口维护、TUI 成唯一交互形态；run 保留脚本化能力；队列/命令统一进队列使"运行中切换"天然解耦（无需禁止/中断/后台三选一）；工具分派对齐两参考项目已验证形态；md 渲染是模型输出刚需（glamour 与 bubbletea 同生态）；无 emoji 是用户明确风格。
- **影响 ADR**：ADR-008 修订——`Output`/Renderer 接口保留给 `run`（TextRenderer/JSONRenderer 流式），TUI 是独立交互 UI 层（`internal/ui/tui`）而非 renderer 插拔实现；ADR-028 Esc 中断语义保留；ADR-025 transcript 增 `command` 行类型（命令落盘，load 不进 conversation）；ADR-026 无状态 agent 是 TUI 换壳零冲击的前提。

## ADR-032：TUI 行号与弹窗宽度的单一来源（2026-08-09）

- **背景**：ADR-031 的两个"由渲染时记录反查"的量各自算了一遍，真实终端出现两类偏移。其一，折叠块点击错配：`renderTimeline` 每个 cell 后只多出一个空分隔行，却按 `+2` 累加行号，第 N 个块整体下移 N-1 行；同时 assistant 消息的点击区间覆盖整个 cell（标题 + thinking + 正文），点正文会切 thinking，thinking 标题行反而落进上一块区间。其二，弹窗选项折行：`modalStyle` 用 `Width(panelWidth-4)` 叠加 `Padding(1,2)`，lipgloss 的 `Width` 含 padding 不含 border，真实内容宽只有 `panelWidth-8`，而调用方按 `panelWidth-4` 排版，每个弹窗正文都宽 4 列。
- **选择**：
  1. 行号会计只在 `appendCell` 一处推进（`line += height + 1`）；hit 区间支持 cell 内局部范围，`renderMessageItem` 返回 `messageCell{body, thinkingStart, thinkingEnd}`，只把 thinking 块本身注册为可点击区间，工具块仍整块可点。
  2. 弹窗几何收敛到 `modalPanelWidth`（外框总宽，按屏宽收敛且不超屏）+ `modalInnerWidth`（可用文本宽 = 外框 - border - padding）两个函数，`modalStyle` 与全部调用方（选择器/审批/帮助）都从它们取值，不再各自算偏移量。
  3. 内容按可用宽度自适应而非硬编码阈值：审批提示一行放不下就竖排；帮助面板按左右两列实际最宽行决定是否并排。
  4. 回归测试断言"渲染结果"而非"内部字段"：`TestHitRangesAlignWithRenderedLines` 断言每个 `hit.start` 行就是该块标题行（`THINKING` / `[OK]`）且相邻区间不重叠；`TestModalsFitPanelWidth` 在 30/40/56/80/120/200 屏宽下断言弹窗每行宽度等于外框宽、且行数不因折行增加。
- **理由**：这两处 bug 同源——同一个几何量在两个地方各算一次，单测又只用内部字段互相验证（旧测试甚至写了 `row++` 去补偿 off-by-one，把 bug 固化成了期望）。把量收敛成一个函数、把断言挪到渲染输出上，才能让"真实终端才复现"的偏移在无 TTY 单测里暴露。已验证：把任一处改回旧算法，两个测试分别失败。

## ADR-031：TUI timeline 与窄屏交互收敛（2026-08-09）

- **背景**：首版 TUI 已满足功能清单，但消息和工具分别渲染，工具块脱离实际事件位置；弹窗与输入区共享底部空间会造成跳屏；队列在 `turn_done` 事件处理时启动下一回合，随后旧回合的 `run_done` 可能把新回合误置为空闲。
- **选择**：
  1. UI 用单一 `timeline []timelineItem` 保存消息、工具调用和系统行；工具调用在 `tool_call` 事件到达时插入，结果只更新对应块。resume 优先按 transcript ordinal 重建，`command` 行与模型消息保持原顺序；旧会话无可读 transcript 时回退 `Conversation`。
  2. `EventTurnDone` 只收尾流式块，不消费队列；队列只在对应 `runDoneMsg` 到达后消费，保证一个 agent goroutine 完整退出后才启动下一条。
  3. 布局采用固定 header/main/auxiliary/composer/footer 分区，resize 时重新计算 viewport；弹窗在 main 区居中，不改变底部 composer 高度。工具和 thinking 的点击区域由渲染时记录的相对行号反查。
  4. Ctrl+C 复制当前 composer 全文到系统剪贴板并给出短暂系统提示；不可用剪贴板时显示失败提示。状态标签、spinner 和边框采用 ASCII，颜色只表达语义。
  5. `resume --no-thinking-display` 与无子命令 `harness --no-thinking-display` 只影响 UI 展示，不修改 transcript 或模型上下文。
- **理由**：timeline 与 transcript 是同一条可审计事件流，能够同时满足历史顺序和工具就地展示；以 goroutine 返回作为队列边界能消除事件桥的竞态；分区布局和 ASCII fallback 对 Windows ConPTY、窄屏和中文宽字符更稳定；复制 composer 是 Bubbles textarea 没有 selection API 时仍可交付的明确行为。

## ADR-033：配置层独立 + 进程级装配根（2026-08-09）

- **背景**：配置加载此前分散在 provider 包（LoadConfig/Resolve/Validate + Config/ProviderConfig 类型）与 cmd/harness（App 惰性单例 + buildAgent + resolveFlags）。agent 装配（buildAgent）挂在 cmd 的 App 上——未来 subagent 需要不同工具集/中间件/提示词装配时，若装配入口在 cmd，internal 的 subagent 定义够不到它，cmd 会退化成堆各 kind 全局的注册中心（依赖方向倒置）。
- **选择**：
  1. 新增 `internal/config`（最底层，只依赖 yaml + stdlib）：`Config/ProviderSpec/Model/Thinking/ApprovalConfig`（YAML 定义）+ `ProviderConfig`（解析后生效的扁平结构）+ `LoadConfig` + `Resolve` + `Validate` + 相关常量（Effort/DefaultAPIKeyEnv/DefaultContextWindow/DefaultEfforts/DefaultThinkingEffort），从 provider 整体迁出（含测试）。`Resolved` 更名 `ProviderConfig`（它就是 ProviderSpec + Model 拍平解析的结果），原 YAML 定义 `ProviderConfig` 更名 `ProviderSpec`（定义 vs 生效）。
  2. provider 回归 ADR-022 的"单 anthropic wire"定位：只留 `ToolSpec/Request/Client/EventStream/Event` + `NewClient(*config.ProviderConfig)`。
  3. 新增 `internal/app`（进程级装配根，惰性单例）：`App{Config, Provider}` + `Load()/LoadFrom()/DefaultApprovalMode()/ResolveFlags()`。它是后续 client/agent/subagent 工厂等进程级共享装配的字段扩展点——config 只是其一。
  4. `buildAgent` 下沉为 `internal/agent.Build(res *config.ProviderConfig, defaultMode string)`（client + 内置工具 + 标准中间件链）。subagent 未来在此之外构造自定义装配，本质同样无状态可共享（ADR-026）。
  5. cmd 薄化为 `rt, _ := app.Load(); a, _ := agent.Build(rt.Provider, rt.DefaultApprovalMode())`；删 `runtime.go/build.go/runtime_test.go`，测试迁 internal。
- **理由**：机制下沉 internal（依赖单向 `config → provider → tools/middleware → agent`、`app → config`）；"1 个 client + N 个 per-kind agent 装配"比"cmd 堆全局"可扩展且方向正确；测试隔离（`app.LoadFrom` 造独立 App = 独立 agent）；保持惰性（version/help/sessions 不碰配置）。
- **影响 ADR**：ADR-026 修订——`defaultApp()` → `app.Load()`，配置加载统一入口从 provider/cmd 改为 internal/config + internal/app。

## ADR-034：thinking 默认开启——删 enabled 配置项 + /thinking 命令（2026-08-10）

- **背景**：架构审查（2026-08-10）证实 `run --model` 装配错参数导致 thinking 配置泄漏（Bug04）：`run.go` 用 `rt.Provider`（默认模型配置）构建 client，请求却发往 `--model` 指定的模型，该模型的 `thinking.enabled/effort` 被忽略。修复讨论中用户提出更简方案：thinking 默认开启，彻底删掉 enabled 配置项，开关改为纯会话级偏好。
- **选择**：
  1. **删 `config.Thinking.Enabled` 与 `ProviderConfig.ThinkingEnabled`**：thinking 默认开启（client 侧恒 true，`anthropicClient.thinkingEnabled=true`），配置不再能声明"某模型默认关思考"。
  2. **开关降级为会话级偏好**：`AgentState.ThinkingEnabled`（nil = 默认开启）持久化，新增 TUI `/thinking` 命令（on/off 弹窗选择器，对齐 /permission /effort）运行时切换；`--thinking/--no-thinking`（run）保留。
  3. **`/model` 一致性**：切换模型只同步 effort（新模型默认，原有行为），enabled 保持会话用户设置（无配置可循，天然一致）。
  4. **Bug04 根修**：`run.go` `agent.Build(res, ...)`（res = ResolveFlags 覆盖后的生效配置），client 与请求模型同源。
- **理由**：enabled 从"模型能力声明"降级为"纯会话偏好"，语义清晰（thinking 是 anthropic 原生能力，默认开启符合 agent 常规）；/model 一致性自动解决；config 少一个项。破坏性：现有 config 的 `enabled: false` 失效（宽松解析忽略）→ 该模型默认开启 thinking，用 `--no-thinking` 或 `/thinking off` 替代。
- **影响 ADR**：ADR-025 修订——`AgentState.ThinkingEnabled` 语义从"继承 client 默认（配置）"改为"nil = 默认开启"；ADR-030 命令集新增 `/thinking`；ADR-026 per-call 覆盖（rc.ThinkingEnabled）不变。

## ADR-035：workspace 边界（软）+ Decide 参数感知 + 多路径审批记忆（2026-08-10）

- **背景**：架构审查 Bug03——5 个文件工具（read_file/list_dir/glob/write_file/apply_patch）把模型给的路径原样交给 os，无规范化/前缀校验/`..`穿越检查，`state.CWD`（会话启动目录）是死字段；叠加审批后 `Decide` 对 classRead 任何模式无条件 Allow（最严 readonly 下仍可无审批读 /etc/passwd、~/.ssh/id_rsa）。另：审批粒度只看工具名（`ApprovalKey` 对非 shell 工具返回工具名，批准一次 write_file 后本会话写任何路径不再询问；Decide 无法表达"允许读项目内、不允许读项目外"）。
- **参考**：opencode 用 patterns（相对 worktree 路径）+ Wildcard 匹配，apply_patch 提取全部文件路径逐个判定；codex 内置文件工具边界靠 OS sandbox 硬边界（审批不覆盖），不采纳（无 sandbox 基础设施）。
- **选择**：
  1. **软边界**：`resolveInWorkspace` 只把相对路径规范化为绝对（以 state.CWD 为基），**不拒绝越界**——越界判定交审批（范围内按 class 规则 / 范围外 Ask，bypass 不受限）。词法校验（filepath.Clean），不解析符号链接（symlink 逃逸 v1 已知局限）。工具层与审批层同源（都读 rc.State.CWD），"相对路径基准"一致。
  2. **Decide 参数感知**：`action{class, targets}` + ws 参数（ApprovalMiddleware 从 rc.State.CWD 读入）。classRead 范围内 Allow / 越界 Ask；classEdit 越界 Ask（软边界优先）/ 范围内按模式；apply_patch 提取 patch 内全部文件路径判定，任一越界 → Ask。shell/todo/未知按原规则。
  3. **ApprovalKey 多路径**：文件工具 key = `<tool>:<绝对路径>`（apply_patch 每个文件路径一条），Decide 判定时**全部命中 approved 才 Allow**；"本会话记住"（AllowSession）把全部 key 写入 AgentState.Permission.Approved。shell 保持 NormalizeCommand 命令粒度。
  4. **EvictContent/MaxOutputChars 下沉 tools 包**：断 tools→impl 反向依赖环（Bug03 需 impl→tools 报错暴露）。
- **理由**："workspace 划定默认范围，审批负责范围之外交给人判断"（审查报告结论）——硬边界要么全允许要么全拒绝，用户无法参与；软边界让越界读/写进审批，合法越界（读外部配置、写 /tmp 输出）可批准，记忆粒度对齐 opencode multi-pattern。
- **影响 ADR**：ADR-025——`AgentState.Permission.Approved` 的 key 语义从"工具名/命令前缀"扩展为"`<工具>:<绝对路径>` 多 key"；ADR-029——`Decide` 签名加 ws 参数、`ApprovalKey` 拆多 key `approvalKeys`；ADR-028——`EvictContent` 从 impl 移到 tools。

## ADR-036：Plan Mode——模式切换 + plan 文件 + plan_done 交接（2026-08-11）

- **背景**：用户要求加入 plan mode 规划模式（先规划、批准后再执行）。调研三个参照源：
  - **codex**（`ModeKind::Plan` 协作模式）：会话内模式切换；独立 developer instructions + 可选不同 model/effort（`plan_mode_reasoning_effort`）；`update_plan`（TODO 工具）**禁止**（plan 不是 todo，产物是对话文本）；无用户输入的后台/空闲回合被拒；`request_user_input` 阻塞式 HITL；退出 = 用户手动切回。
  - **opencode**（独立 plan agent）：`plan-mode.txt` 专门 system prompt + READ-ONLY 硬约束（唯一例外 = 编辑 plan 文件）；`plan_enter`/`plan_exit` 工具；`plan_exit` 弹 HITL 确认 → 批准后**注入合成消息** "plan 已批准，执行它" 并切回 build agent；产物 = 一个 plan 文件（handoff 工件）。
  - **AgentScope**（此前调研吸收）：Plan Mode = 只读阶段 + plan_write + HITL 退出；`todo_write` 独立（plan 模式可用）。
  经四个决策点问答确认：
  1. **形态 = 模式切换**（codex 路线，而非独立 plan agent）
  2. **产物 = plan 文件 + plan_done 交接**（opencode/AgentScope 完整版）
  3. **提问 = 纯文本轮次**（`ask_user` 工具列入后续待办）
  4. **只读强制 = onModelCall 过滤 + onActing `Decide` plan 分支兜底**
- **选择**：
  1. **`AgentState.PlanMode bool`**：`/plan` 命令切换（同 `/permission` 弹窗选择器），SessionMiddleware 落盘、resume 恢复；与 `Permission.Mode` **正交**——plan = 整轮只读规划，权限 = 逐操作审批；plan 激活时 plan 分支优先于权限模式（bypass 也先 /plan 关掉才能写）。plan 文件 `<会话>/plans/plan.md`（复用 `AgentState.Plan.Path`，写入指针），**单文件全量替换**。
  2. **两个新工具**：
     - `write_plan {content}`：全量替换 plan.md（同 update_todo 全量语义，模型每次传完整内容）；**plan 模式下唯一允许的写工具**（非 plan 模式调用 → 拒绝回填）；结果回填渲染（用户可读）。
     - `plan_done`：规划完成信号 → **复用 Approver channel 弹 HITL 确认**（`DecisionAllow` = 批准执行 / `DecisionDeny` = 继续规划，`AllowSession` 归并 Allow）→ 批准：`rc.State.PlanMode=false`（随 SessionMiddleware 落盘）+ `rc.Messages.Add(NewUserMessage("plan 已批准，执行它"))` 注入合成用户消息 + 返回结果、循环继续——下一轮采样 onModelCall/onSystemPrompt 自动读到新状态（无状态 agent ADR-026 的零成本切换）；拒绝：返回"用户要继续规划"，模式不变循环继续。
  3. **新 `PlanModeMiddleware`**（onSystemPrompt + onModelCall）：
     - onSystemPrompt：PlanMode 激活时追加 plan-mode 指令段（只读约束 + 工作流：调研 → 文本提问 → write_plan 写 plan → plan_done；opencode plan-mode.txt 精简版）。
     - onModelCall：PlanMode 激活时过滤 `in.Tools`——**摘掉 `write_file`/`apply_patch`**（模型看不见、省无效调用）；保留 read 三件套 + `update_todo`（AgentScope 路线，规划阶段可记步骤）+ `write_plan` + `shell_command`（只读 shell 仍需要）。
  4. **`Decide` plan 分支**（onActing 兜底；纯函数加 plan 参数）：plan 激活时 classEdit → **Deny**、classShell → 仅 `isSafe` 放行否则 **Deny**、classRead/classTodo/write_plan → Allow、未知 → **Deny**（整轮强只读，非 Ask 打断用户）。
  5. **TUI**：`/plan` 弹窗（on/off）+ 状态栏 `[PLAN]` 标记 + plan 路径系统行。
  6. **不做（后续待办）**：`ask_user` 提问工具、plan 多版本文件、plan 文件 UI 预览面板、plan 模式独立 model/effort（codex `plan_mode_reasoning_effort`）。
- **理由**：
  - 模式切换契合无状态 agent 架构（ADR-026）：PlanMode 进 AgentState，middleware 读 rc.State 适配，无 agent 层改动、无新装配线；子 agent 机制（阶段 5）未做，独立 plan agent 概念过重。
  - plan 文件 + plan_done 让规划有独立可审阅文档 + 明确执行交接点（对齐 opencode/AgentScope 的 HITL 一等公民）；合成消息注入复用既有 `messages.NewUserMessage`，零新基础设施。
  - 过滤 + 拒绝双保险：模型看不见写工具（防无效调用），幻觉调用被 Decide 拒绝（安全兜底）。
  - 纯文本提问渐进式（先跑通闭环，ask_user 后置）；update_todo 保留让规划阶段可维护步骤清单。
- **影响 ADR**：ADR-025——`AgentState.Plan` 从预留到实现（新增 `PlanMode`）；ADR-029——`Decide` 签名加 plan 参数、审批分类新增 plan 工具类；ADR-030——命令集新增 `/plan`、状态栏新增 plan 标记；ADR-021——onSystemPrompt/onModelCall 挂载点新增 `PlanModeMiddleware`。

- **修订（2026-08-11，规划期与用户逐点确认后）**：
  1. **只读强制 = 可见但拒绝**（不做工具过滤）：codex 不过滤（靠 sandbox），opencode 权限 deny 不隐藏。我们没 sandbox，靠 `Decide` plan 分支直接 Deny——工具全量可见，被拒有明确反馈（"plan 模式下禁止写文件"）。**删除 `PlanModeMiddleware` 的工具过滤职责**。
  2. **plan 指令 = 进入点持久化单次注入**（不做 per-round 注入 middleware，用户强调）：`/plan on` 经 `session.AddUser(tools.PlanInstructions)` 写 conversation+transcript；`plan_enter` 批准后完整指令放 tool_result（自然落盘）。后续轮次从历史可见，前缀缓存不受影响。
  3. **Approver 增 `Ask` 方法**（用户指出"原本的 approver 不可以用吗"）：不新开 Asker 接口/rc 字段——`middleware.Approver` 加 `Ask(ctx, AskRequest) (AskResult, error)`（选项单选/多选 + Other 自定义文本，参照 codex request_user_input / opencode question）。复用 `rc.Approver` 字段、TUI `c.send` 桥、run `ChannelApprover`。**删除 `rc.Asker`**。
  4. **退出 plan mode 必须询问用户，bypass 也不例外**：plan_done 恒走 `rc.Approver.Ask`（独立于权限模式）。
  5. **`plan_done` Other 自定义 = 拒绝 + 反馈回填**（opencode CorrectedError 语义提前落地）：用户输入文本 → ToolResult 回填"用户未批准，反馈：<text>，请据此修订计划"，PlanMode 保持 true。**不注入合成消息**（anthropic tool_use→tool_result 邻接约束，ADR-024）。
  6. **`plan_enter` 工具**（模型自主进 plan 模式）+ **`ask_user` 工具**（通用提问，两模式可用），共 4 个 plan 相关工具（plan_enter/write_plan/plan_done/ask_user）。
  7. **`isPlanReadonlyShell`**：plan 模式 shell 放宽管道（原 isSafe 拒绝元字符，不适用规划期只读管道如 `grep foo | head`）；保留危险黑名单 + 拒绝 `>` 写重定向，按 `| && ;` 拆段逐段校验只读白名单。
  8. **plan 文件感知**：write_plan/plan_done 工具结果显式携带绝对路径 + 全文。
  9. **`/plan` 纯切换**（非弹窗）+ **`/plan view`** 查看计划文件。

- **修订（2026-08-12，审查报告 plan-mode-review-2026-08-12 四项缺陷修复）**：
  1. **只读强制 = 写黑名单反向判定 + 纯 Deny 失败模式**（替代点 7 的 `isPlanReadonlyShell` 只读白名单前缀匹配）。同根因缺陷 01/02：前缀匹配猜只读既过松（`sed -i`/`sort -o`/`env sh` 真实写盘）又过严（`python --version` 等 38/56 误拒）。改为**不查是否只读、而查是否写**（三个参考源都不用命令白名单）：命中写黑名单（写命令名/解释器跳板/包管理/系统服务 + git/go/cargo 写子命令 + sed/sort/find/gofmt/awk/tar 写参数 + `>`/`$(`/反引号/换行写形态）→ **Deny**；只读 flag 豁免（`--version`/`-v`/`--help` 等纯探查）+ 写子命令只读例外表（`git status`/`go list`/`npm ls`/`make -n` 等）→ **Allow**；未命中写黑名单也非明确只读（unknown）→ **Deny**（纯 Deny 失败模式，理由区分"检测到写"与"无法确认只读"，回填模型换思路；**不做 review 建议的 Ask 降级**——plan = 整轮强只读，不打断用户）。`isPlanReadonlyShell`/`planSafeExtra` 删除，`isDangerous`/`isSafe` 等非 plan 判定不受影响。
  2. **AgentState 锁下沉**（替代 tools 包 `planMu`/`todoMu` 两把锁保护同一数据）：AgentState 内置 `mu sync.Mutex`（`json:"-"`）+ 带锁方法（`SetPlanMode`/`SetPlanPath`/`SetPermissionMode`/`AddApproved`/`ReplaceTodos`/`RenderTodos`/`Marshal` 等），`SaveFile` 内部经 `Marshal` 加锁。覆盖缺陷 04 + review 未点名的同类 race（`rememberApproved` 无锁写、`refreshStatus` run 期间并发读、`todo.go` attrs map 并发读写）。AgentState **不得按值复制**（go vet copylocks 抓）。
  3. **TUI 待决请求队列**（缺陷 03）：`approvalRequestMsg`/`askRequestMsg` 走 `openOverlay` 守卫，已有覆盖层未决时**入队**而非静默覆盖（原直接赋值 `m.ovl` 导致并发请求互相覆盖 → 审批静默消失 + 工具 goroutine 永久阻塞）；统一 `closeOverlay` 关当前 + 自动弹出下一个（FIFO）；Esc 中断/`reloadSession` 清空 pending（ctx cancel 释放阻塞 goroutine）。
  4. **AllowCustom 在 TUI 侧落地**：`handleAskKey`/`finishAsk` 校验 `AllowCustom`（对齐 run 模式 `ParseAskAnswer`），view 仅在允许时渲染 Custom 输入行。
  - **已知局限（纯 Deny 的代价，接受）**：未知只读命令（`jq`/`yq`/`bat`/`exa` 等已入 allowlist；`tsc --noEmit`/`ruff`/`mypy`/`shellcheck` 有写模式未入）会 Deny，可继续补 `planReadonlyCommands`；无 shell 语法解析（引号内 `;`/`>` 误判，与旧实现一致）；`git config <key>` 单 key 读会误判 write（`--get`/`--list` 已覆盖主流）。

## ADR-037：用量展示 + thinking 回传 + 上下文压缩方案（2026-08-12）

- **背景**：阶段 4 剩余（用量展示 + 上下文压缩）。规划期调研 codex/opencode + **DeepSeek anthropic 端点实测**（Python 原始 HTTP），与用户逐点确认决策。
- **实测结论（DeepSeek）**：返回 thinking 块且**含 signature**，usage 含 input/output/cache；**thinking 块（含 signature）回传 → 200 且计入 input_tokens**（99→136）；不带 signature 也 200（宽松）；thinking + tool_use 同消息回传 200；`include: reasoning.encrypted_content` 参数 200。context_window 1M 足够容纳大 thinking。
- **决策**：
  1. **用量展示**（本 ADR 已实现）：provider `anthropicStream` 捕获 usage（message_start 的 input/cache + 最后 message_delta 的累计 output）随 `EventDone` 发出（不新增 provider 事件类型，`stream_test.go` 严格 switch 免改）；统一类型 `messages.Usage`（provider/events/agentstate 复用）；agent 每轮采样后透出 `events.EventUsage`（非零才发）；`AgentState.Usage`（累计，/usage 命令展示）+ `AgentState.LastContextTokens`（最近请求 input_tokens = 当前上下文占用，footer 与压缩触发）；`impl.UsageMiddleware`（onReasoning after，agent.sample 存 rc.attrs["round_usage"]，中间件累计，agent 核心不碰 state ADR-021）。TUI footer 右侧 `ctx 128k/1.0M`（`LastContextTokens`/`Controller.ActiveContextWindow()`=config.Resolve 的 context_window）+ `/usage` 命令系统行（input/cache_read/output + 当前 ctx）。**不做**回合结束 toast、run --json usage 事件（用户未选）。
  2. **thinking 完整回传**（ADR-025 修订决议，阶段 B 实现）：原"存但不重放"改为**存且重放**——`anthropicStream` 在 `content_block_start` 捕获 thinking 的 signature（SDK union 已确认），`Message.ThinkingSignature` 存储（transcript Line 加 Signature 供 resume 恢复），`toAnthropicAssistantMessage` 重放 `ThinkingBlockParam{Signature, Thinking}` 作首块（**仅 signature 非空时重放**，严格端点兼容；DeepSeek 恒返回 signature）；thinking-only assistant 不再跳过。理由：codex/opencode 都回传 reasoning（`include: reasoning.encrypted_content` + `reasoning_summary`，走加密/服务端摘要），我们完整回传实测可行。**会计澄清**：input_tokens 永远只统计实际发送内容，剥离 thinking 则 input 变小是剥离目的（省 token），非 mismatch；估算必须镜像实际发送。
  3. **压缩 = 直接 LLM 摘要式**（不做 v1 TokenBudget——基础设施已就绪，v1 是需废弃的中间产物；阶段 C 实现）：**触发 = context_window 的 85%**（硬编码常量，暂不提供设置；实际 usage 驱动 + 估算兜底）；`compact.Run` 复用 provider.Client 单独采样生成摘要（**codex 方式发送**：完整 conversation 经 toAnthropicMessages + 摘要 prompt 作为最后一条 user 消息，无工具，max_tokens 4096；prompt = opencode 结构化模板 Objective/Important Details/Work State(Completed/Active/Blocked)/Next Move/Relevant Files + 显式指令保留最新 user 请求原文与当前工作状态 + previous summary 更新式）；压缩后 conversation = **单一 summary user 消息（纯占位，不保留最近消息）**，"保留最新信息"由摘要 prompt 交给 LLM；`AgentState.Summary` 存摘要 + `SetLastContextTokens(0)` 防重入；`RuntimeContext.Segment` 钩子（session 注入 `NewSegment` + seed 行，与 rc.Approver 同模式防环）；**摘要失败 = 跳过压缩 + 终止本次 agent run**（下轮用户消息再触发或手动 /compact；**Esc 中断压缩 = 摘要失败同处理**）；自动（onReasoning before）+ 手动 `/compact`。**不做** "You have N tokens left" 注入（footer 已实时显示）、config.compact 配置段。
  4. **Esc 中断部分落盘**：维持现状**不落盘**（流式 delta 本就不落盘，块完成才落盘；中断时部分 thinking/text 丢弃，只落中断提示）。
- **落地状态（2026-08-12）**：三阶段全部完成——阶段 A 用量展示（版本 0.7.1）✅ → 阶段 B thinking 完整回传（版本 0.7.2）✅ → 阶段 C LLM 摘要压缩（版本 0.8.0）✅。实现细节：`internal/compact` 包（EstimateTokens/ShouldCompact(85%)/BuildSummaryPrompt(previous-summary 更新式)/Summarizer(codex 方式)/Runner.Run）；`RuntimeContext.Segment` 钩子（session 注入 NewSegment + seed 落盘 + Flush）；`impl.CompactMiddleware`（onReasoning before，失败终止 Run）；`events.EventCompacted` + TUI 系统行；`/compact` 手动（Controller.RunCompact，成功显式落盘 AgentState——手动路径不经 SessionMiddleware）。**摘要双重计数坑**：Summarizer 只收 `EventTextDone`（整块），delta+done 都收会翻倍。
- **勘误（2026-08-12，ADR-037 修订）**：`LastContextTokens` 口径修正——原实现只记单轮 `input_tokens`，但 DeepSeek 等端点 `input_tokens` **只统计未命中缓存的新增输入**，历史上下文在 `cache_read_input_tokens`（缓存命中省计费、不省窗口）。只记 input 会把上下文占用低估十几倍：TUI footer 显示 `ctx 0k/1.0M`（实测真实占用 25 万/1M 却显示 0k）、压缩触发（ShouldCompact 读 LastContextTokens）形同虚设（真实爆窗也不触发）。**修正为单轮完整占用 = `input_tokens + cache_read_input_tokens + cache_creation_input_tokens + output_tokens`**（对齐 opencode `tokens.total` 口径 + anthropic Total input tokens 官方定义 + output）。关键认识：单轮 `cache_read` 即"当前历史大小"（缓存前缀 = 历史全量），总和随会话单调增长；**累计值不能相加**（跨轮重复累加会虚高到"好多 M 的错觉"）。`fmtTokens` 同步修复：<1000 显示原值而非 `0k`。压缩成功后 `SetLastContextTokens(0)` 防重入逻辑不变。
- **勘误（2026-08-13，usage-compact-review 修复轮）**：审查 12 项逐点决策，修复 11 项（11 待 12 修复后实测再定），全部回归测试锁定：
  1. **thinking 签名捕获失效**（严重，阶段 B 实际未生效）：DeepSeek 流式 `content_block_start` 处 signature 是**空串**，签名经 `signature_delta` delta 事件下发（SDK union 含 Signature 字段）而 `anthropic_stream` 无此分支 → 签名全丢、thinking 从未回传。补 `signature_delta` 分支（挂 pendingBlock 随 thinking_done 发出）后回传生效。
  2. **压缩后同轮采样仍发旧上下文**（严重）：CompactMiddleware 压缩成功后未把重写后的 conversation 传回 `in.Messages` → 触发压缩的那轮仍以完整旧上下文采样（该轮防爆窗失效），且该轮 usage 抬高 LastContextTokens → 下轮重复压缩。修复 = 成功时 `in.Messages = rc.Messages.Messages` + 回归锚点"压缩后采样请求 = [summary]"。**Build 注册顺序调整**：CompactMiddleware 移到 TodoReminder 之前（压缩 = conversation 级变换在外层；提醒 = 请求级装饰在内层，避免其注入被覆盖丢弃）。
  3. **Summarize 忽略 rc.Model**：摘要请求固定用装配默认模型，`/model` 切换后与正常采样不一致。修复 = 与 agent.sample 同规则 rc.Model 优先。
  4. **Usage 累计 → 覆盖语义**：`AddUsage` 跨轮累加 cache_read（"当前历史全量"非增量）虚高 6.3M，与"累计值不能相加"自相矛盾。改为 `SetUsage` 覆盖——每次 API 返回的 usage 即该次调用的完整账目（与 opencode per-call 跟踪一致），/usage 展示最近一次调用四字段。
  5. **压缩后 Usage 归零**：与 SetLastContextTokens(0) 对称，下轮采样覆盖恢复；摘要请求消耗不单独记账（用户决策）。
  6. **Runner.Run 先落盘后重写**：Segment 失败时内存未动、双轨一致（原顺序 Segment 失败会终止回合但 conversation/state 已改）。
  7. **删除 `AgentState.Summary`**：压缩后 conversation 首条即摘要（纯占位设计），state 副本冗余，且唯一读取方（previous-summary 更新式）消失——`BuildSummaryPrompt` 简化为新建式（旧摘要在 conversation 中 LLM 可见，双份喂送消除）；与 opencode"摘要即消息"模型一致。
  8. **EstimateTokens 低估修正**：注释曾称"更早触发"方向相反（漏系统提示/工具 schema 数 KB）。`Options.SystemPromptTokens`（Build 装配时组合系统提示 + 工具 schema bytes/4 估算传入）补齐缺口；触发滞后一轮（review 05）接受设计，注释记录。
  9. **错误路径补发 EventCompacted**：压缩成功但采样失败时也发"已压缩"系统行（压缩已生效，仅见错误会误判重压/丢历史）。
  10. **杂项**：help 面板补 /usage /compact /thinking /rename；gofmt 全绿（含 CRLF 行尾规范化）。
  **11（footer 非单调）待实测**：12 修复后 thinking 完整回传、上一轮输出计入下一轮输入，footer 应恢复单调；实测确认后再定是否改 `LastContextTokens` 口径（去掉 output）。
- **影响 ADR**：ADR-025——thinking"存且重放"修订 + `AgentState.Usage/LastContextTokens` + transcript Line.Signature；ADR-021——onReasoning 挂载点新增 UsageMiddleware（用量覆盖写）+ CompactMiddleware（压缩）；ADR-026——无状态 agent 下用量覆盖/压缩经 rc 与 rc.Segment 钩子，agent 不碰 state。

## ADR-038：shell 进程树生命周期——Job Object 杀树 + background/kill_pid + 退出 pre-kill（2026-08-13）

- **背景**：用户实测（case07 会话）两个痛点——① 模型用 shell_command 前台启动后端服务，同步阻塞卡死整个会话（Windows 上超时后 PowerShell 本体被杀但子进程残留——`killProcessGroup` 为 no-op，Bug06(b)；且孙进程继承管道句柄导致 `cmd.Run()` 可能永不返回）；② Esc 中断回合但 shell 进程树仍在执行。用户补充需求：harness 退出时 pre-kill 清理（杀全部 background 进程 + 兜底写回 agentstate）。
- **决策（用户逐点拍板）**：
  1. **Windows 杀树 = Job Object**（x/sys/windows v0.38.0 封装，提为直接依赖）：每次 shell 调用创建匿名 job，只设 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`（不设 CPU/内存/UI 限制，不干扰服务进程）；进程 Start 后 `AssignProcessToJobObject`，job 内进程新建的子进程自动归属同一 job → `TerminateJobObject` 原子杀全树。**内核兜底**：句柄关闭即杀树——harness 被 SIGKILL/crash（无任何 defer）时进程销毁 → 句柄内核关闭 → 树死。降级：harness 自身在父 job（CI/IDE 启动器）且 Assign 失败 → 句柄记 0，杀树走 `taskkill /T /F` 尽力（KILL_ON_JOB_CLOSE 兜底失效、Wait 卡住风险复现——已知边界）。POSIX 保持 `Setpgid + kill(-pid, SIGKILL)`（killProcessTree 防御 pid<=0：kill(-0) 会杀 harness 自身进程组）。
  2. **前台 Esc/超时杀树时机 = ctx 取消瞬间**（`context.AfterFunc`），不等 `Wait` 返回——旧实现杀树在 Run 返回后的超时分支，而 Run 可能因孙进程继承管道句柄永不返回（"卡住 → 杀不到"死锁）；杀树成功后树内进程全亡、管道写端随进程终止关闭 → Wait 必返回。Esc 回填"命令已被中断（Esc），进程树已终止"（模型可见，opencode "User aborted the command" 同款）；超时消息加"进程树已终止"。成功路径 `stop()` 防 PID 复用误杀。
  3. **background 参数 + kill_pid 参数**（长任务/服务启动的结构化解）：`background: true` → 工具用 **Go 直接启动**（exec.Command + 文件重定向，不用 &/nohup/Start-Process 等 shell 语法——跨平台统一且模型手写易出错）；日志目录 `<会话>/background/<pid>.log`（工具惰性建，仿 evictions 模式；StatePath 空退化 os.TempDir；rename 失败保留临时名、结果显式返回实际路径）；立即返回 PID+日志路径，模型用 read_file/grep 轮询。`kill_pid` 终止：**仅接受进程级注册表内 PID**（防误杀系统进程的安全边界；进程级 `sync.Map`，与无状态 agent ADR-026 兼容——工具实例无状态、注册表是进程级全局并发安全）。**Esc 语义区分**：Esc 只杀前台进程树（"正在执行的工具调用"）；background 进程不绑定回合 ctx、不受 Esc 影响（会话级资源），仅由 kill_pid 与退出清理终止。
  4. **退出 pre-kill**：`cmd/harness run()` defer `tools.CleanupBackground()`（run/resume/TUI 全部子命令统一覆盖）——background 进程生命周期 ≤ harness 进程寿命（用户拍板：不提供"退出后存活"语义）；TUI `RunTUI` 在 CloseAll 前兜底 `SaveActiveState`（agentstate 写回，正常路径由 SessionMiddleware 每回合保存，兜底是廉价保险）。清理顺序：WaitRuns → SaveActiveState → CloseAll（transcript flush）→ CleanupBackground。
  5. **审批适配**：`ApprovalKey` kill 模式显式派生 `"kill <pid>"`——command 为空时 `NormalizeCommand("")=""` 空 key 若被记住会命中任意空命令调用（放行风险）；`SummaryOf` kill 模式显示 "shell_command: kill <pid>"；`Decide` 不改（空命令 → Ask，杀进程需审批；bypass 放行）；plan 模式空命令 classifyPlanShell → unknown → Deny（强只读语义正确：plan 不该杀进程）。
  6. **schema 与提示词**：`command` 从 required 改 omitempty（kill_pid 模式可省略），Handle 校验二者至少其一；`shellLongTaskGuidance` 改写为显式 background/kill_pid 引导（"不要用 shell 语法自己放后台——工具不追踪，超时/Esc/退出都无法正确终止"）；TUI 工具块：kill 模式块头拼 PID、background 成功结果原文展示（不拼 "exit 0" 前缀——background 返回的是 PID+日志路径而非命令完成态）。
  7. 默认超时保持 30s；不做输出流式化（阶段 3，可选后续）。
- **边界（风险记录）**：job 嵌套降级路径（KILL_ON_JOB_CLOSE 失效）；Attach 竞态窗口（Start 与 Assign 间微秒级，PowerShell 解释执行需数百 ms，实际无孙进程逃逸）；POSIX PID 复用（kill 前可探测，Windows job 句柄精确定位无此风险）；注册表条目进程自然退出后残留至 kill/退出清理（开销可忽略）。
- **影响 ADR**：ADR-028——shell 长任务缓解从"提示词引导手写后台语法"升级为工具参数化（background/kill_pid）+ Esc/超时杀树；ADR-021——工具层职责（非 middleware：进程树管理是 shell 工具自身生命周期，非链式切面）；ADR-026——注册表进程级全局与无状态 agent 兼容。

### ADR-038 勘误（2026-08-13 扩展，超时转后台）

- **修订**：前台命令超时语义从"杀树"改为**自动转后台托管**（原决策第 2 点）。用户讨论后拍板：起服务（长活）与长构建（>timeout 但会完成）场景下杀树前功尽弃——超时后进程继续跑、输出无缝续写日志文件、进程树句柄移交注册表、返回"已自动转入后台：PID+日志路径（不要重试——它仍在运行）"；模型轮询日志、kill_pid 终止。**Esc 语义不变**（用户主动说"停"→杀树+回填"已中断"）。
- **实现要点**：前台输出从内存 buffer 改为写临时日志文件（`.fg_<nano>.log`，转后台时 rename `<pid>.log`——无缝续写无撕裂）；AfterFunc 杀树回调加 `ctx.Err() == context.Canceled` 判断（只 Esc 杀，超时不杀）；Wait 拆到 goroutine（`go func() { err := cmd.Wait(); f.Close(); done <- err }()`——转后台后进程长活时 f 由该 goroutine 在进程死后收尾），Handle 用 select 三路：完成 / Esc（杀树后等 done + 5s 安全网）/ 超时（transferToBackground：rename + 注册表 + tree 置零跳过 defer close）。竞态无害：超时瞬间进程恰好完成 → 注册已死进程（杀空 job 无副作用）；完成瞬间恰逢 Esc → 报中断语义。
- **边界**：僵尸积累（模型反复跑卡死命令）由退出清理 + 提示词引导 kill_pid 缓解；此语义超出 opencode/codex 参考源（两者超时都杀），为用户自创决策。

### ADR-038 二次勘误（2026-08-13，审查修复轮：正常退出不杀派生进程 + 自然退出注销 + 降级路径补实现）

- **背景**：`docs/reviews/shell-process-tree-review-2026-08-13.md` 审查发现 5 项缺陷（对照实验/探针证实），核实中另发现 1 项（06 attach 降级）。其中 01/06 属**实现漏掉已定案设计**（决策第 2 点 stop() / 决策第 1 点"句柄记 0"）。
- **修复（6 项，用户逐点拍板）**：
  1. **正常完成路径补 `stop()`（01，严重）**：原实现丢弃 AfterFunc 返回的 stop，`defer cancel()` 使每条前台命令正常返回瞬间 `ctx.Err()==Canceled` → 杀树回调触发，命令派生的后台进程（`npm run dev &`）随进程组/job 被杀（"起了又没了"）。`startForeground` 返回 stop，Handle 完成分支（非 Esc）与超时分支调用；Esc 分支不调用（杀树就是目的）。超时分支调用是 no-op 防御（回调已在超时瞬间触发且不杀）。
  2. **Windows 正常完成补 `preserveProcessTree`（01 延伸，审查报告未覆盖）**：Windows 上仅 stop() 不够——defer 顺序（LIFO）使 `closeProcessTree` 先于 `cancel` 执行，KILL_ON_JOB_CLOSE 句柄关闭仍由内核兜底杀树。新平台函数：完成分支**清除 KILL_ON_JOB_CLOSE 限制**再关句柄（codex `JobObject.preserve_descendants` 同款）——派生进程完全释放，与 POSIX stop() 后语义对齐（**终端式：前台正常退出不杀派生进程，派生进程退出 harness 后仍存活**；模型经 shell 语法放后台的进程不被注册表追踪、不能用 kill_pid，与决策第 6 点引导一致）；Esc/超时路径不调用（Esc 要杀、超时转后台要移交句柄继续受控）；crash 兜底不受影响（崩溃时本函数未执行，句柄随进程消亡关闭仍杀树）。
  3. **background 自然退出自动注销（02）**：Wait goroutine 补 `unregisterBackground(pid)`——消除边界"注册表条目进程自然退出后残留至 kill/退出清理"，POSIX PID 复用后 `kill_pid` 误杀无关进程组的风险随之关闭（Windows 因 job 句柄精确定位本无此风险）。
  4. **attach 失败降级补实现（06，报告外）**：原实现丢弃 attach 错误且句柄未置零——有效空 job 走 `TerminateJobObject` 分支杀空气、taskkill 兜底不可达。修复：attach 失败 → close + 置零（startForeground/startBackground 两处）。
  5. **Start 失败返回零值句柄（05）**：消除 Windows 上错误分支二次 `CloseHandle`（关闭被内核复用句柄值的未定义行为）。
  6. **测试修复（03/04）**：POSIX 判活拆分 `waitForProcessDead`（短超时轮询——僵尸态对 kill -0 假活，"应死亡"断言专用）与瞬时 `isProcessAlive`（"应存活"断言专用）；`TestShellTimeoutKillsProcessGroup` 反转为 `TestShellTimeoutTransfersBackgroundUnix`（超时后进程组存活 + kill_pid 杀整组，对齐 Windows 版）；回归锚点 `TestShellCommandFgSpawnedChildSurvives`（锁 01，跨平台）+ `TestShellCommandBackgroundAutoUnregister`（锁 02）。
- **平台验证状态**：Windows 全量 `go build/vet/test ./...` + tools `-race` + e2e 全绿；linux/darwin 交叉编译绿；POSIX 测试待 macOS 实测——**此前 ADR-038 各记录"全量全绿"均为 Windows 平台验证**（POSIX 侧 3 个测试因僵尸判活假失败、1 个因语义过期真失败）。

### ADR-028 勘误（2026-08-13，移除 shell 工具内截断）

- **背景**：ADR-028 两条款自相矛盾——"截断上收中间件（工具返回完整结果，删工具内 truncate）"与"shell 长任务缓解 B：超时/非零退出经 EvictContent 落盘"。shell 错误分支（非零退出/Esc 中断）工具内调用 EvictContent，与 ToolOutputMiddleware after 双重截断：preview ≈ 20K + 元文本再次超阈值 → 二级截断 + 冗余 eviction 文件（内容是 preview 而非原始输出）；且成功分支不截、错误分支截，行为不一致。历史动机（超时后内存 buffer 输出会丢，需工具内抢先落盘）自 ADR-038 输出写日志文件 + 中间件 after 无条件兜底后已不存在。
- **修订**：移除 shell.go 错误分支三处 EvictContent 调用，错误消息拼原始输出——工具成功/失败一致返回完整结果，截断 + 落盘 + 路径提示统一由 ToolOutputMiddleware（onToolCall after 无条件执行，含 Esc 中断回填）负责。落盘文件内容由"纯输出"变为"错误前缀 + 输出"（无害，更完整）。

## ADR-039：系统提示通道重构——内容通道分类原则 + rc.SystemPrompt + base 中间件化 + 压缩判定实时化（2026-08-13）

- **背景**：两个痛点——① `Agent.instructions` 挂在 agent 结构体上，与 ADR-026 无状态 agent 架构有张力（系统提示本质是会话/运行级内容：阶段四 AGENTS.md 随 cwd 变、阶段五 subagent 不同提示词）；② Build 装配期做兜底 token 估算注入（预组合系统提示 + 工具 schema bytes/4 → `SetSystemPromptTokens`）——装配后写 Runner、阶段四动态注入后固定值失效。
- **内容通道分类原则（用户定案）**：除对话历史（rc.Messages）外，所有内容归位两个通道——**稳定配置 → 系统提示**（onSystemPrompt 管道）、**结构化工具定义 → toolspec 独立字段**（因含 JSON schema 参数，function calling 必需）。与 codex（`instructions` 顶层 + tools + input items）/opencode（`system[]` + tools + messages）完全同构。两类例外：**即时信号**（TodoReminder 偏离提醒）维持临时副本注入消息尾部（非稳定配置，进系统提示语义不对）；**摘要请求**是内部调用（不带系统提示/工具，opencode `system: []` 同款）。未来新内容（AGENTS.md/MEMORY.md/环境信息）自动归位 onSystemPrompt 管道，不再逐项讨论。
- **决策（用户逐点拍板）**：
  1. **rc.SystemPrompt**：`Agent.instructions`/`SetInstructions` 删除——agent 不携带任何提示词文本。rc.SystemPrompt = 本次运行的调用方 per-call 贡献（可空；subagent 覆盖用），agent.Run 经 `ComposeSystemPrompt(ctx, rc)`（去 base 参数，起点 = rc.SystemPrompt）组合后**回写为完整系统提示**（compact 兜底估算读此值）。
  2. **base 中间件化**：基础提示词（"You are a helpful coding agent."）成为标准链的第一个 onSystemPrompt 中间件（`impl.BaseInstructionsMiddleware{Text}`，`DefaultBaseInstructions` 常量）——空起点时由链首注入，无常量无兜底；subagent = 换链换 Text（build.go 既定方向）。
  3. **压缩判定实时化（废弃 SystemPromptTokens）**：兜底估算 = 判定时实时三项（镜像实际发送三通道）——`EstimateTokens(messages)` + `EstimateSystemPrompt(rc.SystemPrompt)`（bytes/4）+ `EstimateTools(in.Tools)`（JSON 序列化 bytes/4，实测 7 内置工具约 7.3KB ≈ 1.8K token）。**判定挪到 CompactMiddleware**（onReasoning 同时持有 rc 与 in.Tools 的唯一位置）→ `Runner.ShouldCompact(rc, tools)`；**Runner 变纯执行器**：`Run(ctx, rc) error`（去 force/bool——手动 /compact 语义不变：无条件压缩，判定由调用方决定）。usage 优先路径不变（API 返回的 input/cache 已含三通道全量）。
- **影响 ADR**：ADR-037——第 8 点 `Options.SystemPromptTokens` 机制废弃（判定时实时估算替代）、`Runner.Run(force)` 签名变更；ADR-026——rc 新增 SystemPrompt 字段（覆盖模式同构：rc 覆盖、链首中间件给默认）；ADR-021——onSystemPrompt 管道新增 BaseInstructionsMiddleware（链首）。
- **边界**：wire 行为零变化（`Request.Instructions` → `params.System` 顶层、`Request.Tools` → `params.Tools`）；BaseInstructions 仅挂 onSystemPrompt，不影响洋葱顺序。

## ADR-040：后台任务完成自动反向通知 + 唤醒器（2026-08-13）

- **背景**：shell 后台进程（`background: true` 与超时转后台）完成时 harness 不主动通知模型——模型被要求"轮询日志"，浪费 token/回合，且 `cmd.Wait()` 的退出码被直接丢弃。参照 AgentScope Java v2（`AsyncToolMiddleware` + `AsyncToolRegistry` + `MessageBus.inbox` + `InboxMiddleware` + `WakeupDispatcher`）实现：后台任务完成 → 落盘完成事件 → 下一次推理开始前注入对话末尾；会话空闲时 → 唤醒 run 自动继续。计划文档：`docs/plans/async-completion-notify-2026-08-13.md`（含三处审查修复，见下）。
- **用户逐点拍板**：
  1. 保留轮询能力，提示词强调"可等通知"，模型自行选择。
  2. **通用 async 通道**：独立 `internal/completion` 包（只依赖 stdlib），阶段 5 子 agent 复用同一链路。
  3. 完成事件落盘（独立 `completions.json`，**不挂 AgentState**——完成通知是"一次性事件"不是"会话状态"）。
  4. 通知角色 = **RoleUser**（复用 LineTypeUser，transcript/load 零改动）。
  5. 唤醒器本轮做：决策逻辑收敛在 `Controller.MaybeWake()`，Model 只薄转发；agent 核心零耦合（"何时启动 run"本就是编排层职责，唤醒只是第二个触发源）。
  6. TUI 事件桥复用（`completionWakeMsg` 走既有 `program.Send`）。
- **数据链路（一个事件 → 两个下游，按会话状态自然分流）**：
  - **生产端**：Wait goroutine 进程自然退出 → `notifyCompletion`（注销注册表条目；nil → kill_pid/CleanupBackground 已注销或前台正常完成 → no-op）→ `Queue.Append`（锁内 append + pid 临时名原子落盘 + 锁外调 `OnAppend`）。**只写 Queue、不碰 conversation**（避开主循环 data race）。
  - **路径 A（注入）**：`BackgroundCompletionMiddleware`（onReasoning before，注册在 Compact 之后、TodoReminder 之前）每次采样前 `Drain()` → `rc.AppendUser(ev.Result)`（session 注入 = `AddUser`，防环同 rc.Segment 模式）→ `in.Messages = rc.Messages.Messages` 同步 → 经 `rc.Emit` 推 `EventNotice`（TUI 系统行可见——否则模型突然回应一条界面上从未出现过的通知）；TUI `handleAgentEvent` 渲染系统行；transcript 不落盘该类型（user 行已由 AddUser 写入）。
  - **路径 B（唤醒）**：`OnAppend → program.Send(completionWakeMsg{})` → Update → `MaybeWake`：`active == nil || isRunning()（cancel != nil）|| PendingCount()==0` → 丢弃；否则 `RunWakeup`（Run 去 AddUser 的变体）拉起新 run——唤醒器只启动 run 不注入内容，新 run 首采样前路径 A 注入，`Drain` 清空后 `PendingCount()==0` 天然防重。resume 双路径恢复：已注入的靠 transcript user 行重建；已完成未注入的靠 completions.json 加载下次采样前补注入。
- **审查修复（计划评审轮，三处竞态/热循环）**：
  1. **双唤醒竞态（不能只靠 bubbletea 单线程）**：tea.Cmd 由 bubbletea 在 Update 返回后异步执行，`cancel` 若在 cmd 内才 `setCancel`，连续两条 wake 消息会在间隙双双通过 `isRunning` → 两个 run 并发跑同一 conversation。修法：`MaybeWake` **返回 cmd 前同步抢占** `setCancel`（第一道闸）+ Model `m.running` 同步闸（第二道兜底）。
  2. **超时转后台竞态窗口**：进程恰在超时瞬间已死时，前台 Wait goroutine 的 notify 先于 `transferToBackground` 注册执行（no-op）→ 通知永久丢失 + 死条目残留。修法：DeadlineExceeded 分支注册后对 `done` 做**非阻塞 receive**（`compensateTransferNotify`）——已有结果补注销+通知，两路恰好一个拿到 entry，天然不双通知。
  3. **唤醒失败热循环**：唤醒 run 首采样前失败 → pending 未清 → `runDoneMsg` 补唤醒 → 再失败无限循环打 API。修法：`handleRunDone` 末尾补 `MaybeWake` **仅当 `err == nil`**（成功 run 必跑过首采样、Drain 必已清空 pending，故 `err==nil && pending>0` 恰好只对应"最后一次采样已过后台完成"的竞态窗口；失败时 pending 留待下一次完成信号/用户消息注入，不丢）。
- **已知局限（记录，不本轮处理）**：`harness run` 单轮模式（主要测试用）无 TUI 唤醒器——"完成会自动通知"只在回合采样期间成立，模型若"结束回合等通知"则通知不会到达（进程最终由 CleanupBackground 清理）；完整承诺仅对 TUI 会话成立。
- **影响 ADR**：ADR-021——rc 新增 `Completions`/`AppendUser` 注入（防环同 rc.Segment 模式）；ADR-026——无状态 agent 零改动（唤醒是编排层第二个触发源）；ADR-038——bgProcess 条目扩展完成通知字段、Wait goroutine 完成时注销+通知合流；ADR-030——TUI 事件桥新增 completionWakeMsg（复用 program.Send）。
- **边界**：Drain 落盘清空与逐条注入之间存在极小崩溃窗口（进程崩溃丢这批事件；不重复注入优先——重复通知比丢失更糟）；bubbletea v1.3.10 `Send` 有 `ctx.Done()` 守卫，退出竞态下 Append+Send 为 no-op 不 panic；完成通知成为 conversation 永久 user 消息（压缩时随摘要收敛）。

## ADR-041：阶段 7 代码架构整理（Composition Root + 接缝方法值化 + ADR-040 审查 03/04/05/06，2026-08-14）

- **背景**：ADR-040 实施后复查代码，用户提出"闭包频繁、装配逻辑散落"（规划文档 `docs/plans/architecture-cleanup-2026-08-13.md`）：命令层三入口（run/resume/repl）各自装配、rc 注入点两处分裂（Session.RuntimeContext 会话域 vs Controller UI 域）、TUI 启停序列隐式、装配根不唯一（agent.Build / app.Load / Controller）。结论：架构方向（无状态 agent + per-call rc + middleware + TUI）本身成立，闭包密集是既定决策（框架强制 / 解耦接缝 / 时序生命周期 / 普通回调）叠加的自然产物——本轮只做低风险可读性整理，**用户拍板提前启动**（原计划阶段 4/5/6 完成后做），为阶段 4 剩余/5（子 agent）/6 铺路。
- **决策**：
  1. **Composition Root 收敛**：`app.Build(Options) → *HarnessAgent`——命令层只声明模式（ModeRun/ModeTUI/ModeResume）与参数（Options 命名字段，零值 = 未指定），全部接线（配置加载/生效配置解析/agent 装配/项目桶/会话创建或恢复/渲染与输入层）收敛在 `internal/app`。产物命名 `HarnessAgent`（用户拍板：避开 `middleware.RuntimeContext` 的 Runtime；内部持有基础 ReAct agent，字段 `reactAgent`）。`HarnessAgent.Run()` 内部按模式创建 signal ctx（run=Interrupt+SIGTERM，TUI/resume=SIGTERM——SIGINT 由 bubbletea 当按键）并执行；`Teardown()` 幂等对称拆除。完整拆除链：`tui.Run`（WaitRuns→SaveActiveState）→ `tui.Close`（CloseAll）→ cmd main defer `CleanupBackground`。resume 错误优先级保持历史行为（会话解析先于配置加载）。
  2. **TUI 显式三阶段**：`tui.Assemble → (*tui.App).Run → (*tui.App).Close` 取代 RunTUI 单函数隐式顺序（RunTUI 留薄壳兼容）：Assemble 只接线不运行（接线完整性可单测断言）；setSend 补偿登记（bubbletea 构造鸡生蛋：Program 需初始 Model）收敛进 Assemble 注释记录，不推翻。
  3. **rc 注入分层成型**：session 域（`Session.RuntimeContext`）给全量默认；`Controller.newRunContext` 是**唯一** UI 覆写点（Approver/Emit），Run/RunWakeup/RunCompact 三处共用；run 单轮模式在 `HarnessAgent.runOnce` 单点注入 channelApprover。新增接缝只改一处，不再多处登记。
  4. **注入闭包方法值化**（行为零变化，可 grep/可跳转）：`rc.AppendUser = s.AddUser`、`rc.Segment = s.writeSegment`（seed 落盘抽命名方法）、`c.wakeSignal = c.wake`（字段保留作 registerWake 哨兵）、repl `newSession` 闭包 → `HarnessAgent.defaultNewSession` 方法值。既定取舍线不动：单方法、单实现、只用一次 → 函数字段；`Approver` 多方法多实现 → 接口。普通回调（run onEvent 双转发 / context.AfterFunc / Wait goroutine）按分类保留，捕获语义注释补强。
  5. **ADR-040 审查待办修复**：
     - **03**：`BackgroundCompletionMiddleware` 补 `rc.Messages != nil` 守卫——非会话构造 rc 挂 Completions 且 drain 非空时会解引用 nil panic；防御性跳过（不 Drain，pending 保留），生产路径恒非 nil 零影响。
     - **04**：前台 Wait goroutine 的 `notifyCompletion` 加 `transferred` 门控（atomic.Bool，仅超时转后台分支置 true）——纯前台完成路径不再按 pid 查全局注册表，"pid 复用命中刚死未注销旧后台条目发错通知"的理论窗口消除；抽 `waitForeground` 命名函数使门控可单测；`compensateTransferNotify` 补偿不变（两路仍恰好一个拿到 entry，不双通知）。
     - **05**：`runDoneMsg.wakeNotStarted` 标记——唤醒 run 的 cancel 被 MaybeWake 同步抢占、cmd 尚未真正开跑即被 Esc 打断时，`handleRunDone` 不写"Turn interrupted"系统行与中断提示 AddUser（run 未启动，无事发生，避免污染 conversation），pending 保留待下一次信号。
     - **06 测试补齐**：Esc 打断已启动唤醒 run（正常中断语义，05 对照锚点）/ 非 active 会话完成事件（信号发出但 MaybeWake 三分支丢弃、pending 保留）/ 退出后 Send 安全（bubbletea v1.3.10 Send 的 ctx.Done 守卫 + "已终止 no-op"语义锚点）/ text+json 渲染器忽略 EventNotice（无输出不 panic，run 模式通知可见性仅靠 transcript 为已知局限）。
     - **另议项落地**：`handleCompactDone` 成功路径（含 !compacted）末尾补 `maybeStartWake`——对称 handleRunDone 的 err==nil 补唤醒，compact 期间被 m.running 闸丢弃的 pending 立即补跑（延迟不丢）；err 路径不补（防热循环）。
     - 测试基架：`testCompletionRC` 去 rc.attrs 走私断言数据（直接返回注入记录切片；rc.attrs 只承载生产键）。
  6. **附带**：`session.ProjectForCWD()`（cmd findProject 下沉 session 包，Build 与 sessions 命令共用）；e2e `TestSessionPersistenceE2E` 解析符号链接（macOS `/var→/private/var` 物理/逻辑路径分桶错位——CLI 子进程 getcwd 返回物理路径、测试用逻辑路径；HEAD 即存在，与本次改动无关的测试侧修复）。
- **影响 ADR**：ADR-030——RunTUI 拆三阶段（薄壳保留兼容）；ADR-026/021——rc 注入分层（session 默认 + Controller 单一覆写点）+ 闭包方法值化；ADR-040——审查 03/04/05 修复、06 测试补齐、compact 补唤醒；版本 0.10.0。
- **边界**：行为零变化（03/04/05 防御性修复除外，均已在审查记录）；`harness run` 单轮模式无唤醒器（ADR-040 已知局限）维持；阶段 5 子 agent 不实现——`HarnessAgent`/`Options` 为其装配变体留扩展位（届时在 Build 参数化或新装配工厂派生）；`agent.Build` 仍为 agent 域子工厂（域内工厂，非装配根）。

## ADR-042：后台日志分配竞态修复——临时文件唯一命名（2026-08-14）

- **背景**：TUI 实测发现并发启动 background 任务时日志文件分配竞态（问题文档 `docs/problems/background-log-race-2026-08-14.md`，实测 5/8 轮命中）：临时日志名用 `time.Now().UnixNano()` 合成，而本机墙上时钟 tick 粒度粗（实测连续调用 93% 相同值、8 并发同刻放行 7~8 个相同）——并行 tool call（ADR-024 errgroup goroutine）同刻启动时 `os.Create` 共享同一 inode：输出同偏移互覆、等长行整体丢失；先 rename 者胜、后 rename 者 ENOENT 降级到已不存在的 `.bg` 路径（完成通知路径不可读）。四种症状（.bg 共享 / .bg 消失 / 内容串扰 / 输出丢失）全部复现。
- **决策**：**临时文件命名机制统一为 `os.CreateTemp`（随机后缀 + O_EXCL，EEXIST 自动换新）**，取代"时间戳合成名 + os.Create 截断打开"——唯一性由内核 O_EXCL 保证而非时间戳概率保证，从构造上消除共享 inode。三处同源一并修复：`background.go`（`.bg_*.log`）、`shell.go`（`.fg_*.log`，超时转后台同源 rename 竞态）、`evict.go`（`tool_*.txt`，并发 eviction 后写者 O_TRUNC 覆盖先写者）。rename 降级语义保留（平台差异兜底）：tmp 由 O_EXCL 唯一创建、他人不可见，降级路径必然存在。
- **影响 ADR**：ADR-038——日志文件分配实现修正（进程生命周期/注册表/通知链路不变，仅临时命名机制）；ADR-028——eviction 落盘文件命名机制。
- **回归锚点**：`TestShellCommandBackgroundConcurrentUniqueLogs`（12 轮 × 8 任务屏障并发直调 `startBackground`；断言路径唯一且为 `<pid>.log`、文件可读、内容仅含本任务、无 `.bg` 残留；修复前一次运行即复现全部四症状）。
- **验证**：`go build/vet/test ./...` 全绿；`tools -race` 绿；linux/windows/darwin 交叉编译绿。版本 0.11.1。

## ADR-043：AGENTS.md 注入 + 基础提示词增强（2026-08-15，阶段 4 剩余）

- **背景**：阶段 4 剩余两项——① `agentsmd`（AGENTS.md 注入）一直待办（ADR-011 原定"注入 developer 消息"，ADR-039 内容通道分类后改为系统提示管道）；② 基础提示词过薄（`DefaultBaseInstructions = "You are a helpful coding agent."` 单行），模型缺"身份/工作方式/交流语气"上下文。调研 codex（`prompt.md` 275 行：身份/AGENTS.md 语义/响应/计划/任务执行/验证/收尾）+ opencode（`default.txt`：身份/语气/主动性/约定）+ deepseek-harness（persona 注入 `{{model}}`/`{{cwd}}`）后落定。
- **决策**：
  1. **agentsmd 挂 onSystemPrompt**（修订 ADR-011 的"developer 消息"）：`internal/agentsmd` 纯逻辑包（发现+拼接+截断，只依赖 stdlib）+ `impl.AgentsMdMiddleware` 注入；装配顺序 = 基础 persona（BaseInstructions）→ 项目上下文（AgentsMd）→ 工具引导（ToolInstructions）。
  2. **发现规则（codex 对齐）**：从 cwd 向上找最近含 `.git`（文件或目录，兼容 worktree/submodule）的目录为项目根；收集根→cwd（含两端）每层一个文件、根→cwd 顺序；**文件名回退 `AGENTS.md` → `CLAUDE.md`**（用户拍板，使 simple-harness 自身的 CLAUDE.md 可自举）；全局 persona `~/.harness/agents.md` 恒在最前；**预算 200KB 硬编码**；**读失败/空文件跳过、非致命**。
  3. **基础提示词中文 + 动态上下文**（用户拍板）：`DefaultBaseInstructions` 改为中文模板（身份 + `# 环境`/`{{cwd}}`/`{{model}}` + 工作方式 + 交流），`BaseInstructionsMiddleware` 增 `render` 注入动态上下文（cwd 取 `rc.State.CWD` 空回退 Getwd、model 取 `rc.Model` 空回退"默认"）；**只承担身份/通用工作方式/交流，不重复 ToolInstructions 的工具纪律**。
  4. **路径注入防环**：`session.GlobalAgentsMDPath()` 由 `app.buildAgent` 解析后传入 `agent.Build(res, mode, globalAgentsMD)`（三模式共用），保持"session 知中间件、反之不成立"约定（impl 不反向依赖 session）。
- **影响 ADR**：ADR-011——"注入 developer 消息"修订为"注入 onSystemPrompt 管道"；ADR-039——onSystemPrompt 管道新增 AgentsMd 中间件、BaseInstructionsMiddleware 增占位符渲染；ADR-033——`agent.Build` 签名 +1 参数（globalAgentsMD）。
- **验证**：`go build/vet/test ./... -count=1` 全绿（含 e2e）；`internal/agentsmd` 发现/拼接/截断/多字节边界 + `impl` 注入/渲染回归锚点锁定。

## ADR-044：全局 Skill 支持——SKILL.md 目录 + skill 工具渐进式披露（2026-08-15，阶段 4 剩余收尾）

- **背景**：用户要求给 harness 加 skill 支持（仅全局）。调研三个参考源：codex（`codex-rs/skills`：全局 `$CODEX_HOME/skills/<name>/SKILL.md` 目录包 + YAML frontmatter，`### Available skills` 目录 + 使用规则，progressive disclosure 模型自读文件；插件/别名/MCP 依赖/动态选择等机制超出"仅全局"范围）、opencode（`~/.config/opencode/skills/**/SKILL.md` 等根扫描，frontmatter 仅 name+description，`<available_skills>` 摘要列表）、deepseek-harness（与 harness 架构最接近：skill 注册表 + filesystem 提供者 + 模型侧 `skill` 工具按需加载，`<skill_content>` 包装，会话目录注入 `<available_skills>` 摘要，kebab-case 名校验，非法文件跳过非致命）后落定。
- **决策**：
  1. **技能格式（三个参考源公共子集）**：技能根 `$HARNESS_HOME/skills`（默认 `~/.harness/skills`）；两种形态——目录包 `<name>/SKILL.md`（可带 references/、scripts/、assets/ 辅助资源，相对路径以该目录为基准）与平铺 `<name>.md`；YAML frontmatter 必填 `name`（kebab-case `/^[a-z0-9]+(?:-[a-z0-9]+)*$/`）与 `description`，可选 `whenToUse`；同名冲突目录包优先（按名排序确定性判重）；正文 = frontmatter 之后 TrimSpace，**加载预算 200KB**（对齐 agentsmd）；无效/读失败文件跳过（发现阶段）或回填错误（加载阶段），**绝不终止回合**。
  2. **模型侧暴露 = 目录摘要 + `skill` 工具（deepseek-harness 模式，非 codex 读文件模式）**：`impl.SkillsCatalogMiddleware` 挂 onSystemPrompt 注入 `# Skills（技能）` 目录段（每行 `- <name>: <description>（适用：<whenToUse>）`，描述截断 200 字符 + 触发引导"用户点名或任务匹配 → 先调用 skill 工具加载完整指令再行动；目录仅摘要，未加载前不要推断技能内容"）；`tools.SkillTool` 按名现读 SKILL.md 全文，以 `<skill_content name="...">`（含资源基目录提示）包装回填——渐进式披露：正文只在模型明确加载时进上下文。选择工具而非读文件：路径不外泄、无 roots 表、与 anthropic wire 的 tool_result 通道自然契合、为未来项目/远程技能留位。定位规则复用 `skills.Discover` 判定（目录包优先/同名去重），工具与中间件单一来源不漂移。
  3. **新包 `internal/skills`（叶子，只依赖 stdlib + yaml.v3）**：`Discover`（扫描 + 校验 + 排序，Content 不加载）/ `Load`（现读 + 校验 + 预算截断）/ `IsSkillName` / `RenderContent` / `CatalogLine`；与 agentsmd 同哲学（读失败非致命、预算截断、多字节安全）。
  4. **装配签名重构（用户拍板：BuildOptions 结构体）**：`agent.Build(res, mode, globalAgentsMD)` → `agent.Build(agent.BuildOptions{Provider, DefaultMode, GlobalAgentsMD, GlobalSkillsDir})`——对齐项目 Options 惯例（app.Options/compact.Options/agentsmd.Options），签名不再随新能力 +1；`session.GlobalSkillsDir()`（镜像 GlobalAgentsMDPath）+ `session.DirSkills`；`app.buildAgent` 三模式共用解析注入（防环约定不动）。
  5. **审批只读分类**：`skill` 归类 classRead（只读工具放行，不打断渐进式披露；plan 模式同放行）；`ToolOutputMiddleware` 豁免 skill 截断（指令语义不能被 head/tail 丢中段，全量在原 SKILL.md + 200KB 预算兜底，read_file 同款豁免）。
  6. **基础设施**：`harness init` 与 `session.EnsureDirs` 建 `skills/` 目录 + README.md 占位说明（幂等不覆盖）；TUI 工具块分派（摘要 `skill <name>`、折叠态 `loaded <name> | N`、展开全文、`Loaded skill <name>` 文案）。**无 config 开关**（文件在场即启用，对齐 AGENTS.md 无开关）；**无 AgentState/会话格式变更**。
- **影响 ADR**：ADR-039——onSystemPrompt 管道新增 SkillsCatalog 中间件（顺序：基础 persona → 项目上下文 → 技能目录 → 工具引导）；ADR-033——`agent.Build` 签名重构为 BuildOptions（调用方仅 app/build.go）；ADR-028——ToolOutputMiddleware 豁免清单 +read skill；ADR-029——policy classRead +skill。
- **明确不做**：项目级 skill（.dsh/skills、.agents/skills）、用户 `/name` 手势直呼、远程/市场拉取、config 开关、codex 式插件/别名/MCP 依赖/动态选择、热更新监听（ComposeSystemPrompt 每回合现读足够）。
- **验证**：`go build/vet/test ./... -count=1` 全绿（含 e2e）；`internal/skills` 发现/解析/截断/渲染 + `tools.SkillTool` + `impl.SkillsCatalogMiddleware` + TUI 分派 + policy 只读 + `TestSkillToolE2E`（进程外：系统提示目录行 → skill 调用 → `<skill_content` 回填断言）锁定。版本 0.12.0。



## ADR-045：子 agent——异步 spawn + completion 队列复用 + 按类型装配（2026-08-16，阶段 5）

- **背景**：阶段 5（IMPLEMENTATION_PLAN 待办）：可被主 agent 调用的子 agent——并行执行、独立会话、单向通信、嵌套（深度 ≤2）、中断/resume。功能规划 `docs/plans/subagent.md` 14 条定案 + 实现讨论 12 条新增（计划文档 `docs/plans/subagent-implementation-2026-08-16.md`）。调研参考源：codex（spawn_agent 异步 + mailbox 注入 + wait_agent）、opencode（task 前台阻塞 + task_id resume）、AgentScope Java v2（inbox + wakeup + spawn 深度 3）、deepseek-harness（send_message + interrupt_agent + 深度 3）。
- **决策**：
  1. **spawn 纯异步（用户拍板）**：`spawn_agent` 立即返回 agent_id（工具结果 = id/name/type/depth/status + 引导文案），子 goroutine 跑完 → 完成事件 Append 进**父会话 completion 队列**（复用 ADR-040 通道，零新队列类型）——路径 A 在途采样前注入（user 消息 + EventNotice 系统行）/ 路径 B TUI 唤醒（空闲自动继续）。**无 wait_agent 工具**（注入 + 唤醒天然替代；IMPLEMENTATION_PLAN 简化项落地）。嵌套同构：孙完成 Append 进子的队列（逐层归并）。
  2. **按类型装配 + 实例缓存共享（用户拍板；2026-08-16 修订——装配独立化）**：主 / general-purpose / explore 三个装配变体（工具集 + 提示词不同）。**装配逻辑在 `internal/subagent` 包内**：初版 Manager 直接调 `agent.Build`（BuildOptions +Tools/BaseInstructions/Client 可选字段）；**修订后 `buildSubagent` 完全独立**——不复用 agent.Build（client/registry/compactor/中间件链在 subagent 包自由组合），`agent.Build` 回归纯主装配（`BuildOptions.BaseInstructions` 字段删除，Tools/Client 保留）；依赖方向 subagent→agent→tools 无环、无接口无工厂不变。**提示词精准化（调研 opencode/deepseek 后定稿）**：general-purpose = uniform 主 persona（`impl.DefaultBaseInstructions`，跨主/子一致，对齐 deepseek "system prompt stays uniform across parents and children"）+ **独立 `DelegationInstructionsMiddleware` 追加委托段**（deepseek SUBAGENT_DELEGATION_CONTEXT 同款：权限固定/不重试被拒/任务范围/结论回父/不能问用户/可嵌套/wait_task；做成独立中间件而非 BaseInstructions 加参数——职责分离、不污染 impl 包）；explore = 专属简短提示词（对齐 opencode explore.txt 风格，简短聚焦）。同 kind agent 实例缓存共享（无状态 ADR-026）；spawn 的 model/thinking_effort 覆盖经子 AgentState 播种 → 子会话 rc per-call 生效。
  3. **子会话落盘嵌套 + 血缘**：`<父会话目录>/subagents/<子id>/{historys, agentstate.json, plans, evictions}`（定案）；子 AgentState 新增血缘字段 `ParentID/AgentType/Depth/Status` + 复用 `Name`（spawn 可选 name，默认 `<type>-<短id>`）；`session.CreateIn(dir, st)`（Create 重构委托）+ `ResumeAt(dir)`（resume 续接原 jsonl 段，不新开）。
  4. **工具集（定案第 8 条 + 嵌套确认；2026-08-16 修订——explore 加只读 shell）**：general-purpose = 内置 12 − ask_user + 5 控制工具（spawn/send/interrupt/resume/list，实现在 subagent 包）+ `wait_task`（tools 包，访问 bgProcess 注册表轮询后台 shell）；explore = 只读 5（read_file/list_dir/glob/skill + **`ShellCommandTool{Readonly: true}`**——白名单命令强制只读：`tools.IsSafeCommand` 判定（原 impl policy 白名单下沉 tools 包 + 扩充 git grep/show/ls-files、wc/stat/du/sort/uniq；修 --flag=value 与词边界两个漏洞），白名单外命令与 kill_pid 拒绝回填，与审批模式无关的硬边界，bypass 也拦得住；Spec 描述按字段区分明示能力边界）；主 agent = 内置 12 + 5 控制（无 wait_task——有唤醒器）。**嵌套允许**（general-purpose 可再 spawn），深度上限**硬编码 2**（用户拍板不做 config）。
  5. **fork 过滤（用户拍板）**：子会话起点 = 仅 spawn 的 message（不继承父历史）。
  6. **结果不截断完整注入（用户拍板）**：最终答案 = 子会话 conversation 最后一条无 tool_calls 的 assistant 文本（对齐 opencode/dsh/codex 完整返回；父 compact 85% 兜底）；失败/中断通知带"已产出文本"（用户拍板）。
  7. **send_message 仅运行中**（用户拍板）：消息 Append 进子会话 completion 队列（子下轮采样前注入）；子已结束 → 工具错误回填引导 `resume_agent`（仅直属子，磁盘加载继续，多轮委托）。
  8. **interrupt_agent 任意后代**（用户拍板）：不能中断自己/父（沿 ParentID 链上溯检查）；cancel 子 turn ctx（Esc 同款），收尾通知父（带中断前结果）。**父 Esc 不级联子**（子 ctx = WithCancel(Background) 独立）；进程退出 `Manager.Shutdown()`（cancel all + 等收尾 + 通知 Append——父 resume 补注入）。
  9. **权限继承 + 审批归属**：子 AgentState.Permission = 父 Mode + Approved 快照播种；子审批经 `subagentApprover` 包装转发父 Approver（`ApprovalRequest`/`AskRequest` 加 `AgentID` 字段，TUI 弹窗/run 打印带【子 agent <id>】前缀）；inner nil（非 TTY）自动拒绝不变。
  10. **run 模式局限（用户拍板）**：父 turn 结束时未完成的子被进程退出清理（子会话部分落盘，resume 可续）；对齐 ADR-040 run 无唤醒器；e2e 用 mock 内容路由（父拖延循环等注入——"已完成。结果："判定）保证确定性。
  11. **TUI 轻量 + 只读查看（用户拍板）**：默认不打扰（子事件只落子会话 transcript）；`/subagents` 弹窗（Manager 运行态 + 磁盘历史合并，id/name/type/status/depth）→ 只读查看（active 切子会话 + 输入禁用 + 订阅子事件流实时滚动；`/switch` 返回；运行中的子复用 Manager 会话实例避免双 writer，磁盘历史 ResumeAt 退出时 Close）。
  12. **审批策略**：控制工具（spawn/send/interrupt/resume/list/wait_task）归 classControl（无文件系统副作用，readonly/acceptedit/bypass/plan 全放行）。
- **影响 ADR**：ADR-040——`completion.Event.SessionID` 预留字段落地（子→父通知带子会话 id）；ADR-026——共享无状态 agent 被多 goroutine 并发 Run（子并行）；ADR-028——结果注入不截断（user 消息路径，非 tool_result）；ADR-033——buildAgent 返回 (agent, Manager)；ADR-029——ApprovalRequest/AskRequest +AgentID + classControl；ADR-025——session CreateIn/ResumeAt/NewID(prefix)；agentstate +血缘字段（omitempty 向后兼容）。
- **明确不做**：wait_agent/mailbox、用户直连子（第 13 条）、子唤醒循环（子用 wait_task 轮询）、同步 spawn、config subagents 段（深度硬编码）、自定义声明式 subagents/*.md（预留）。
- **验证**：`go build/vet/test ./... -count=1` 全绿（含 e2e + -race）；subagent 包 Manager 生命周期/深度/通知三分支/实例缓存/退订 + session CreateIn/ResumeAt + TUI 查看/输入禁用 + `TestSubagentE2E`（进程外：spawn → 子采样 → 注入父 → 父回复 → 子目录血缘 completed 断言）。版本 0.13.0。**修订 2026-08-16**：装配独立化（buildSubagent + DelegationInstructionsMiddleware + explore 专属提示词）后全量 build/vet/test + -race 全绿（含 `TestBuildSubagentPrompts` 断言 uniform+委托段/explore 专属；FakeClient 加锁修复并发采样 race）。
