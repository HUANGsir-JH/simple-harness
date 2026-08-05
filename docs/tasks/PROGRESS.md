# 开发日志

> 按日期追加，最新在上。记录：进展、阻塞、问题、经验。

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
