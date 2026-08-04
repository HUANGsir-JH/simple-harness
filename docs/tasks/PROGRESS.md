# 开发日志

> 按日期追加，最新在上。记录：进展、阻塞、问题、经验。

## 2026-08-04

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
