# 开发日志

> 按日期追加，最新在上。记录：进展、阻塞、问题、经验。

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
