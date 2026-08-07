# 开发日志

> 按日期追加，最新在上。记录：进展、阻塞、问题、经验。

## 2026-08-07

### 阶段二：工具系统 + 并发执行 + 终端渲染 + middleware 骨架 ✅

- **交付**：`harness run <prompt>` 单轮 + `harness` 交互式 REPL，工具闭环 + 终端渲染（thinking 灰显 + 工具调用展示 + --json 事件）完整可跑 —— **可用的简单终端 CLI agent 循环**（阶段二定位达成）
- **tools 包**（2.1-2.2）：Tool 接口 + 注册表（有序）+ ToolError 二分类（RespondToModel/Fatal）；6 内置工具（read_file/list_dir/glob/write_file/shell_command/apply_patch）。shell 平台分派（Windows cmd /C / POSIX sh -c）；apply_patch v1 支持 codex 格式子集（Begin/End Patch + Add/Update/Delete + @@ hunk）；write_file 整文件覆盖写（补 apply_patch 无法整文件重写的缺口，真实 API 验证多行创建）
- **middleware 链**（2.3）：RuntimeContext（单用户无 UserID）+ 6 hook（onAgent/onReasoning/onToolCall/onActing/onModelCall 洋葱 + onSystemPrompt transformer），**hook 贯穿 context.Context**（执行链需要，ADR-024）；Base 空实现；内置 ToolInstructionsMiddleware（工具说明注入系统提示，codex 调研依据）
- **agent 纯 loop**（2.4）：Run(ctx, rc, thread, onEvent) 多轮 采样→工具→回填；回合级事件 7 类（turn_start/thinking_delta/text_delta/tool_call/tool_result/turn_done/error），turn_done 为测试锚点；provider Event 加 EventThinkingDelta（anthropic thinking_delta 流式）
- **关键修复**：多工具调用时多条独立 tool_result 消息触发 anthropic 400（"tool_use 无紧随 tool_result"）→ 一批结果合并成一条多块消息（messages.ToolResults），ADR-024
- **关键修复（补）**：真实 API 暴露 anthropic **工具参数流式**——content_block_start 时 tool_use 的 input 为空（`{}`），参数经 input_json_delta 分片到达。原实现只读 start 的 input → **工具参数全空**（apply_patch/shell_command 报"参数为空"）。修复：按 content block index 累积 input_json_delta，content_block_stop 时输出完整参数（+ input_json_delta 累积单测）。**验证：apply_patch 创建/修改/删除 test.txt + read_file 回读，文件增删改读全闭环 ✅**（此前真实 API 测试都用了无参工具 list_dir，未暴露）
- **渲染 + CLI**（2.5-2.6）：renderer 订阅 agent 事件（text/thinking/tool/边界）；--json 输出完整事件流（含 turn_done）；`harness` 交互式 REPL（复用 thread 会话延续）、`harness run <prompt>` 单轮
- **测试**：单测全绿（tools/middleware/agent/provider/messages/cmd）；**termtest 进程外 e2e 跑通**（mock HTTP：单轮 turn_done 锚点 + 交互式工具闭环，Windows ConPTY 集成 demo ✅）；真实 API 冒烟 ✅（读取目录/计数 .md/交互式）
- **踩坑**：Go flag 顺序（flag 在 prompt 前）复现 ADR-018；交互式入口暂不支持 --config（用默认 config.local.yaml 查找）

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
