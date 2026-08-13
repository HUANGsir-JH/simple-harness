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
