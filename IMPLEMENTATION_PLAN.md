# Go Agent Harness — 实施计划

## Context

参照 OpenAI Codex CLI（`../codex/codex-rs`，已做两轮源码调研）的架构，用 Go 构建一个**可真实使用**的极简 agent harness（命令行形式）。定位为**通用框架**：未来可被 resume-agent 等其它项目引用。

目标不是教学 MVP，而是能真实完成开发任务的框架：
多后端（OpenAI / Anthropic / OpenAI-兼容）、会话恢复、并行工具调用、子 agent（含单向通信）、上下文压缩、AGENTS.md 注入、Hooks、分层审批。

## 已确认决策

| 决策点 | 结论 |
|---|---|
| 语言 | Go |
| LLM 后端 | 抽象为多后端；**配置结构体 + 1 个 HTTP 客户端**（codex 的 ConfiguredModelProvider 模式：base_url + env_key + wire_api，Anthropic/Ollama 无需独立实现） |
| 客户端依赖 | 官方 SDK（openai-go、anthropic-sdk-go） |
| 内部消息模型 | **统一 Message 类型**（role/content/tool_calls/tool_results），provider 适配转换 |
| 会话存储 | JSONL 文件（每会话一个，追加写） |
| CLI 交互 | Renderer 接口抽象，v1 简单流式渲染，TUI 后续插拔 |
| 权限审批 | 三档权限（readonly / acceptedit / bypass）+ 规则匹配引擎（保留扩展点）；2026-08-06 调整，替代原三态 |
| 系统提示词 | 动态拼接（AGENTS.md 注入 + 组装）；当前硬编码待替换（见"规划调整"） |
| 工具执行 | 并发执行全部 tool_call，结果按 call_id 回填 |
| 子 agent | spawn_agent + **主→子单向消息传递**（无 mailbox/队列，简化版） |
| 内置工具 | 文件操作 + Shell 执行 + apply_patch（grep/搜索未选，可后续补）；**todo 工具单独开阶段做** |
| 配置 | YAML 文件（~/.harness/config.yaml + 项目级）+ 环境变量覆盖 |
| thinking 推理模式 | 模型级配置（`enabled` + `efforts` 档位集，默认启用/默认 high）；CLI `--effort` / `--thinking` / `--no-thinking` 运行时覆盖；按各 wire 标准参数传递（openai → reasoning.effort；anthropic → thinking + output_config.effort） |
| 定位 | 通用框架（内部包导出、文档完善），项目名暂用 `harness` |

## 架构总览

```
harness/
├── cmd/harness/          # 入口：run / resume / config 子命令
├── internal/
│   ├── agent/            # ★ agent loop（turn 循环 + 事件流）
│   ├── provider/         # Provider 接口 + HTTP 客户端（Responses/chat 两 wire）
│   ├── messages/         # 统一 Message 模型 + JSONL 序列化
│   ├── tools/            # 工具注册表 + shell/file/apply_patch 实现
│   ├── approval/         # 三态审批策略 + 危险命令黑名单 + TTY 交互 + allowlist
│   ├── session/          # 会话管理（JSONL 持久化 + resume 重放）
│   ├── compact/          # 上下文压缩（TokenBudget 式 v1 → 摘要式 v2）
│   ├── ui/               # Renderer 接口：simple（v1）/ tui（v2 插拔）
│   ├── hooks/            # PreToolUse / PermissionRequest / Stop 子进程 hook
│   ├── agentsmd/         # AGENTS.md 向上搜索 + 注入 developer 消息
│   └── config/           # YAML 配置加载 + 环境变量合并
├── go.mod
└── docs/                 # 设计文档
```

## 核心设计

### 1. 统一消息模型（`internal/messages/`）

```go
type Message struct {
    ID       string      `json:"id"`
    Role     string      `json:"role"`   // user | assistant | tool_result | developer
    Content  string      `json:"content,omitempty"`
    ToolCalls []ToolCall `json:"tool_calls,omitempty"` // assistant 消息携带
    ToolCallID string    `json:"tool_call_id,omitempty"` // tool_result 关联
}

type ToolCall struct {
    ID       string          `json:"id"`
    Name     string          `json:"name"`
    Args     json.RawMessage `json:"args"`
    Result   *ToolResult     `json:"result,omitempty"`
}
```

- 核心层只操作统一模型；provider 适配层负责 ↔ 原生格式转换（`openai.go` / `anthropic.go`）
- JSONL 会话文件直接存统一模型，**换后端零迁移**

### 2. Provider 抽象（`internal/provider/`）

```go
type Provider interface {
    BaseURL() string
    APIKey() string          // 从 env 读取
    WireAPI() WireAPI        // "responses" | "chat"
    ContextWindow(model string) int   // 模型目录（硬编码 map，可后续扩展）
}

type LLMClient interface {
    Stream(ctx context.Context, req StreamRequest) (<-chan StreamEvent, error)
}

type StreamEvent struct {
    Type     EventType   // TextDelta | ToolCallDone | TurnComplete | Error
    Text     string
    ToolCall *ToolCall
    Error    error
}
```

- **不写多 provider 实现**：一个配置结构体（base_url / env_key / wire_api）+ 一个 HTTP 客户端
- 重试：指数退避 + 抖动（200ms × 2^n，0.9~1.1 随机），流重试上限 10、请求重试上限 4（对应 codex `responses_retry.rs`）
- 错误分类：可重试（429/5xx）→ 退避重试；ContextWindowExceeded → 触发压缩；不可重试 → 冒泡

### 3. Agent Loop（`internal/agent/`）

```go
// 一次 turn = 从用户消息到 agent 最终回复
for {
    events := llm.Stream(ctx, history, tools, model)
    sawToolCall := false
    for ev := range events {
        switch ev.Type {
        case TextDelta:     renderer.Write(ev.Text)
        case ToolCallDone:  sawToolCall = true; go executeTool(ev.ToolCall) // 并发
        case TurnComplete:  /* 结束判定 */
        }
    }
    if !sawToolCall { break }   // 无 tool_call → turn 结束
    // 工具结果按 call_id 回填历史 → 下一轮 sampling
}
```

- **结束判定**：本轮无 tool_call → 结束（对应 codex `needs_follow_up` 语义）
- **错误二分类**（codex 最重要的语义）：
  - `RespondToModel`（工具报错 → 文本回填历史，循环继续）
  - `Fatal`（终止 turn）
- **并行工具**：errgroup 并发执行全部 tool_call，任一失败只影响自己；结果按 call_id 排序回填（语义等价 codex FuturesOrdered）
- 用户中断：`signal.NotifyContext(SIGINT)` 贯穿全局 ctx
- **无 max_turns 硬限制**（防死循环靠压缩 + 用户中断，同 codex）

### 4. 工具系统（`internal/tools/`）

```go
type Tool interface {
    Name() string
    Spec() ToolSpec          // name + description + parameters JSON Schema
    Handle(ctx context.Context, callID string, args json.RawMessage) (ToolResult, error)
}
```

- 注册表：`map[string]Tool` + 有序列表（模型可见顺序稳定）
- 错误约定：返回 `*ToolError{RespondToModel bool, Message string}` —— false=可回模型继续，true=Fatal
- 结果统一包装为 `{success: bool, text: string}` 文本回填，输出截断（20k 字符）
- 内置工具：
  - `read_file`（路径+行范围）
  - `list_dir` / `glob`
  - `shell_command`（command/workdir/timeout_ms/输出截断）
  - `apply_patch`（diff 语言，语法说明注入系统提示）

### 5. 审批（`internal/approval/`）

- 三态策略（对应 codex `AskForApproval`）：`UnlessTrusted`（默认）/ `OnRequest` / `Never`
- 黑白名单（搬 codex `BANNED_PREFIX_SUGGESTIONS` 表 + 只读安全命令白名单）：
  - 危险：`rm -f`、`sudo`、`curl|sh`、`chmod -R`、`mkfs` 等 → 询问
  - 安全只读：`ls`、`cat`、`git status` 等 → 自动放行
- TTY 检测：非 TTY 自动拒绝；TTY 弹 Y/n/remember 三选
- remember 写入 `~/.harness/allowlist.json`
- **拒绝 ≠ Fatal**：拒绝后作为普通 tool 错误文本回填模型，模型换思路重试

### 6. 会话存储（`internal/session/`）

- 每会话一个 JSONL 文件：`~/.harness/sessions/<timestamp>-<id>.jsonl`
- 追加写 + `os.O_APPEND`，存统一 Message 模型
- resume：读文件反序列化 → 按 provider 格式重发历史
- 子命令：`run` / `resume <id>|--last` / `config`

### 7. 上下文压缩（`internal/compact/`）

- **v1（TokenBudget 式）**：token 估算超限（`min(autoCompactLimit, contextWindow) - 缓冲`）→ 清空历史保留系统提示 + 最近 N 条消息 + 一条 `[对话已压缩，继续任务]` 占位。约 10 行。
- **v2（摘要式）**：单独调 LLM 生成摘要，保留最近用户消息（对应 codex 默认方案）
- 触发时机：turn 开始前（PreTurn）+ 采样后超限（MidTurn）

### 8. 子 Agent（`internal/agent/subagent.go`）

- `spawn_agent` 工具参数：`task_name` + `message`（fork 开关可选）
- 子 agent = 独立 session + 独立历史（goroutine 跑自己的 turn 循环）
- **fork 过滤**（抄 codex `keep_forked_rollout_item`）：只继承父的 user 消息 + assistant 最终答案，**丢弃工具调用细节**
- **单向消息传递**：主 agent 通过 `send_message` 工具向子 agent 发消息，子 agent 把消息作为 user 消息注入其历史继续 turn
- 完成通知：子 agent 完成后，结果作为文本注入父 agent 上下文（模型自己决定是否 spawn 下一个）
- 简化：无 mailbox / wait_agent / 多 agent 并发上限（semaphore 可选）、无昵称/路径树

### 9. AGENTS.md（`internal/agentsmd/`）

- 从 cwd 向上找项目根（`.git` 等 marker）→ 从根到 cwd 收集所有 AGENTS.md → 拼接（`--- project-doc ---` 分隔）→ 注入 developer 消息
- 预算：200KB 截断
- 约 50 行，价值极高

### 10. Hooks（`internal/hooks/`）

- v1 只做 3 个点：
  - **PreToolUse**：工具执行前，stdin 传 `{tool_name, tool_input}`，stdout 读 `{decision: approve|block, reason}`
  - **PermissionRequest**：审批时，输出 `{behavior: allow|deny}`
  - **Stop**：turn 结束时通知
- 子进程模型：`exec.Command` + stdin JSON / stdout JSON + timeout（对应 codex `hook_runtime.rs` 协议）
- 配置：`.harness/hooks.json`

### 11. UI Renderer（`internal/ui/`）

```go
type Renderer interface {
    Start(session *Session)
    WriteText(delta string)
    WriteToolCall(call *ToolCall)
    WriteToolResult(result *ToolResult)
    WriteApprovalRequest(req ApprovalRequest) (ReviewDecision, error)
    WriteError(err error)
    Flush()
}
```

- v1：`simple` 渲染器（ANSI 彩色输出 + 文本流式）
- v2：`tui` 渲染器插拔替换（接口不变）
- `--json` 模式输出事件 JSONL（排障利器，直接复用 Renderer 接口另一实现）

## 规划调整（2026-08-06 用户补充，记录，实现时探讨）

- **阶段二增补**：除工具调用外，一并实现**完整简单的终端渲染**（文本流式 + 工具调用展示）；编写时考虑好**工具权限框架设计**，预留扩展点（为阶段三铺路）
- **阶段三权限/审批改为三档**：`readonly` / `acceptedit` / `bypass`；**规则匹配引擎保留扩展点**即可（替代原 UnlessTrusted/OnRequest/Never 三态）
- **系统提示词动态拼接**：当前 agent.go 硬编码 `Instructions: "You are a helpful coding agent."`，需要动态拼接；新建一个阶段或并入现有阶段（建议并入阶段四，与 AGENTS.md 注入一起做提示词组装）
- **todo 工具**：阶段二之后**单独开一个阶段**实现，不做进工具系统阶段

## 实施阶段

### 阶段 1：骨架 + 统一消息模型 + Provider + 最小 loop ✅ 已完成（2026-08-04）
**目标**：项目初始化（go.mod + 目录结构）、`messages` 包（统一 Message 模型 + JSONL 序列化）、`provider` 包（Provider/LLMClient 接口 + OpenAI Responses/chat 适配 + Anthropic 适配 + 重试）、最小 agent loop（单次采样，无工具）
**成功标准**：`harness run "你好"` 能从真实 API 拿到流式回复 ✅（DeepSeek 兼容端点验证通过）
**测试**：provider 单测（mock HTTP）✅；loop 单测（mock LLMClient 返回固定事件流）✅

> 阶段 1 详细设计见 `docs/tasks/TASKS.md`（单元 1.1~1.9 全部完成）。重试项：两 SDK 内置退避重试（ADR-012）。thinking 推理模式（ADR-020）：模型级配置 + 双 wire 标准参数 + CLI 运行时覆盖，真实 API 双 wire 验证通过。

### 阶段 2：工具系统 + 并发执行
**目标**：`tools` 包（Tool 接口 + 注册表 + 错误二分类）、内置工具（read_file/list_dir/glob/shell_command/apply_patch）、并行执行 + call_id 回填
**成功标准**：`harness run "读取当前目录文件列表并告诉我"` 能触发工具调用并正确回填
**测试**：各工具单测（临时目录）；loop 并发执行测试（mock 3 个 tool_call 验证并发 + 回填顺序）

### 阶段 3：审批 + Hooks + 错误重试
**目标**：`approval` 包（三态策略 + 黑白名单 + TTY 交互 + allowlist）、`hooks` 包（PreToolUse/PermissionRequest/Stop）、错误重试完善
**成功标准**：危险命令触发确认（TTY）/ 自动拒绝（非 TTY）；hook 能拦截工具执行；429 重试生效
**测试**：审批策略单测（黑白名单匹配）；hook 单测（mock 子进程）；重试单测（mock 429 响应）

### 阶段 4：会话 + AGENTS.md + 压缩
**目标**：`session` 包（JSONL 持久化 + resume）、`agentsmd` 包、`compact` 包（TokenBudget v1）
**成功标准**：`harness resume --last` 能恢复历史继续对话；AGENTS.md 注入生效；长会话自动压缩
**测试**：session 单测（写读回放）；agentsmd 单测（临时目录向上搜索）；compact 单测（token 估算超限触发）

### 阶段 5：子 Agent + CLI 完善 + 文档
**目标**：`spawn_agent` + `send_message` 单向通信、Renderer 接口完成（simple 渲染器 + --json 模式）、config 包（YAML 加载）、CLI 子命令完善、docs/ 设计文档
**成功标准**：`harness run "用子 agent 分析这个目录结构"` 端到端跑通；`--json` 输出结构化事件；config 文件可配置多后端
**测试**：子 agent 单测（mock provider 验证 spawn 流程）；CLI 端到端测试；config 单测

### 阶段 6（后续可选）：TUI 渲染器 / 摘要式压缩 / grep 工具 / send_message 双向

## 验证方案

- **单元测试**：每阶段成功标准列出的测试用例（`go test ./...`）
- **端到端（真实 API）**：配置好 provider 后跑：
  1. `harness run "你好"` — 基础对话
  2. `harness run "创建一个 hello.go 并运行"` — 工具闭环（shell + write）
  3. `harness resume --last` — 会话恢复
  4. `harness run --json "..."` — 结构化事件输出
  5. 危险命令触发审批 — 权限验证
- **mock 测试**：agent loop 用 mock LLMClient 验证循环终止、并行、错误回填

## 依赖清单

| 依赖 | 用途 |
|---|---|
| github.com/openai/openai-go | OpenAI Responses API |
| github.com/anthropics/anthropic-sdk-go | Anthropic API |
| github.com/spf13/cobra | CLI 子命令 |
| gopkg.in/yaml.v3 | 配置解析 |
| （可选）github.com/spf13/viper | 配置 + 环境变量合并 |
