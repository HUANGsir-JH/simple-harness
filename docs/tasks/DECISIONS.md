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
    - openai（Responses）→ `reasoning: {effort: low|high|max}`；关闭传 `effort: "none"`
    - anthropic（Messages）→ `thinking: {type: enabled, budget_tokens}` + SDK `output_config: {effort}`；关闭传 `thinking: {type: disabled}`
  - **关闭必须显式传关闭表达**：DeepSeek 等兼容端点默认开启 thinking，"不传参数"无法关闭（不传 = 走模型默认 = 开）。
- **理由**：配置与传递都是通用语义 / 协议标准字段，DeepSeek 只是恰好兼容，不写任何后端特化。efforts 列表让模型粒度声明支持档位，运行时 --effort 在集内校验。anthropic 的 `output_config.effort` 是 SDK 官方字段（DeepSeek 兼容端点支持）。
- **注意**：openai wire 关闭时的 `effort: "none"` 是 DeepSeek 等兼容端点在 OpenAI 格式内的关闭约定；标准 OpenAI o 系列 effort 仅 low/medium/high，若后续接入需按官方语义适配关闭。
