# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概览

一个参照 OpenAI Codex CLI（`../codex/codex-rs`，Rust 源码）架构、用 Go 构建的**可真实使用**的极简 agent harness（命令行形式）。定位为通用框架，未来可被其它项目（如 resume-agent）引用。

**当前状态**：阶段 1 进行中（项目骨架已初始化，核心包尚未实现）。规划文档 `IMPLEMENTATION_PLAN.md` 是权威来源，实现前先读它。

## 常用命令

```bash
# 构建（二进制输出到当前目录 ./harness[.exe]）
go build ./...

# 运行
go run ./cmd/harness version

# 测试（全部）
go test ./...

# 测试（单包）
go test ./internal/messages/

# 测试（单个用例，-run 接正则）
go test ./internal/messages/ -run TestMessageJSONL
```

## 代码架构

目标目录结构（`IMPLEMENTATION_PLAN.md` 定义，逐步实现）：

```
cmd/harness/          # CLI 入口（run / resume / version 子命令）
internal/
  agent/              # ★ agent loop：for { stream → tool_call? → 执行 → 回填 }
  provider/           # Provider 接口 + HTTP 客户端（Responses/chat 两 wire）
  messages/           # 统一 Message 模型 + JSONL 序列化（核心层唯一消息类型）
  tools/              # 工具注册表 + shell/file/apply_patch 实现
  approval/           # 三态审批策略 + 危险命令黑名单 + TTY 交互
  session/            # JSONL 会话持久化 + resume 重放
  compact/            # 上下文压缩（TokenBudget 式 v1 → 摘要式 v2）
  ui/                 # Renderer 接口（simple v1 / tui v2 插拔）
  hooks/              # PreToolUse/PermissionRequest/Stop 子进程 hook
  agentsmd/           # AGENTS.md 向上搜索 + 注入 developer 消息
  config/             # YAML 配置加载 + 环境变量合并
docs/tasks/           # 任务跟踪（TASKS/PROGRESS/DECISIONS）
```

## 核心架构约束（来自 ADR，见 docs/tasks/DECISIONS.md）

这些是已定案的设计，实现时**遵循而非重新讨论**：

1. **统一消息模型**：核心层只操作统一 `Message`（role/content/tool_calls/tool_results），provider 适配层负责 ↔ 原生格式转换。JSONL 会话文件直接存统一模型，换后端零迁移。
2. **Provider 无多实现**：多后端 = 一个配置结构体（base_url + env_key + wire_api）+ 一个 HTTP 客户端，Anthropic/Ollama 无需独立实现（参照 codex `ConfiguredModelProvider`）。
3. **错误二分类**：工具错误分 `RespondToModel`（文本回填历史、循环继续）与 `Fatal`（终止 turn）。审批拒绝也是普通错误回填，不中断任务。
4. **并行工具**：errgroup 并发执行全部 tool_call，结果按 call_id 回填历史。
5. **子 agent = 独立 session**：fork 时只继承父的 user 消息 + assistant 最终答案（丢弃工具调用细节）；v1 只做 spawn_agent + 主→子单向消息。
6. **UI 抽象**：`Renderer` 接口（simple v1 / tui v2 插拔）；`--json` 模式是 Renderer 的另一个实现。
7. **分层审批**：三态策略（UnlessTrusted 默认/OnRequest/Never）+ 黑名单启发式 + TTY 交互 + allowlist；无 OS 沙箱，安全靠策略层。

## 工作流约定

- **沟通用中文**；提交信息用 conventional commits（`chore:`/`feat:`/`docs:` 等）。
- **任务跟踪**：每个阶段在 `docs/tasks/TASKS.md` 建条目；每个工作单元完成后在 `PROGRESS.md` 记一笔（含日期）；重要设计决策写 `DECISIONS.md`；阶段完成同步更新 `IMPLEMENTATION_PLAN.md` 状态。
- 时间戳统一 `YYYY-MM-DD`；状态变更必须带日期。
- 规划文档 `IMPLEMENTATION_PLAN.md` 是权威来源，实现前先读它；开发哲学与流程（渐进式、3 次失败即停、决策框架）见全局 `~/.claude/CLAUDE.md`。
