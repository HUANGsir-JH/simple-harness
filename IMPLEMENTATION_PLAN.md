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
| wire 数量 | **只保留 anthropic Messages 一个 wire**（2026-08-07 决策：Responses 与 Chat Completions 都不要，openai wire 整体移除）——provider 单一 wire 最大 simple；代价：openai 兼容端点（DeepSeek openai 格式 / 阿里 qwen）不再支持，DeepSeek 只能走其 anthropic 兼容端点 |
| 事件模型 | **分层**：provider 层 Event（采样级，4 类 → 扩展 thinking_delta/生命周期，信号基于两 SDK 各自能力）+ agent 层回合级事件（turn_done/tool_result）。不搞 AgentScope start/delta/end 三件套（2026-08-07 探索确认） |
| 内部消息模型 | **统一 Message 类型**（role/content/tool_calls/tool_results），provider 适配转换 |
| 会话存储 | JSONL 文件（每会话一个，追加写）+ 轻量 AgentState 快照，落在 **~/.harness/ 统一 workspace**（见"Workspace"行） |
| CLI 交互 | Renderer 接口抽象，v1 简单流式渲染，TUI 后续插拔 |
| 扩展机制 | **进程内 middleware**（onAgent / onReasoning / onActing / onModelCall / onSystemPrompt 五 hook，洋葱式）+ 链机制为核心扩展点；子进程 hooks 降级远期（2026-08-07 AgentScope 调研修订，替代原子进程 hook 方案） |
| 权限审批 | 三档权限（readonly / acceptedit / bypass）+ 规则匹配引擎（保留扩展点）；**复杂规则匹配不强做**——middleware 挂载点天然承载后续演进（2026-08-07 确认） |
| 系统提示词 | 动态拼接（AGENTS.md 注入 + 组装），作为 middleware 的 **onSystemPrompt** hook 实现 |
| 会话状态 | JSONL 消息流（追加写，换后端零迁移）+ **轻量 AgentState 快照**（权限/todo/plan 指针/摘要 → 完整 resume）（2026-08-07 AgentScope 调研修订） |
| Workspace | **~/.harness/ 统一 workspace**：sessions（JSONL+快照）/subagents/*.md/tools.json/memory/plans 收敛一处；AGENTS.md 保持项目级向上搜索（两源拼接注入）（2026-08-07 确认） |
| Compaction 范围 | TokenBudget v1 保底 + 摘要式 + **大工具结果 eviction**（80K 落盘 + preview + read_file 指针）；**不做 overflow 安全网**（eviction 撑宽度后超限概率低）（2026-08-07 确认） |
| 工具执行 | 并发执行全部 tool_call，结果按 call_id 回填 |
| 子 agent | **内置几个子 agent**（general-purpose 等）+ **允许并行** + **状态跟踪**（pending/running/completed）；自定义声明式预留（不实现）；保留 fork 过滤 + 主→子单向（2026-08-07 确认，细节阶段五探讨） |
| 内置工具 | 文件操作 + Shell 执行 + apply_patch（grep/搜索未选，可后续补）；**todo 工具单独开阶段做** |
| 配置 | YAML 文件（~/.harness/config.yaml + 项目级）+ 环境变量覆盖 |
| thinking 推理模式 | 模型级配置（`enabled` + `efforts` 档位集，默认启用/默认 high）；CLI `--effort` / `--thinking` / `--no-thinking` 运行时覆盖；按各 wire 标准参数传递（openai → reasoning.effort；anthropic → thinking + output_config.effort） |
| 定位 | 通用框架（内部包导出、文档完善），项目名暂用 `harness` |

## 架构总览

```
harness/
├── cmd/harness/          # 入口：run / resume / config 子命令
├── internal/
│   ├── agent/            # ★ 纯 ReAct loop（采样→工具→回填）+ 事件流（不含任何工程能力）
│   ├── middleware/       # ★ Middleware 接口（onAgent/onReasoning/onActing/onModelCall/onSystemPrompt）+ 洋葱链
│   ├── provider/         # Provider 接口 + HTTP 客户端（仅 anthropic Messages wire，2026-08-07 移除 openai wire）
│   ├── messages/         # 统一 Message 模型 + JSONL 序列化
│   ├── tools/            # 工具注册表 + shell/file/apply_patch 实现
│   ├── approval/         # 三档权限（readonly/acceptedit/bypass），作为 onActing middleware 实现
│   ├── session/          # 会话（JSONL 追加写 + 轻量 AgentState 快照 + resume），落 ~/.harness/ workspace
│   ├── compact/          # 上下文压缩（TokenBudget v1 + 摘要式 + 大结果 eviction），作为 onReasoning middleware
│   ├── ui/               # Renderer 接口：simple（v1）/ tui（v2 插拔）
│   ├── agentsmd/         # AGENTS.md 向上搜索 + 注入，作为 onSystemPrompt middleware 实现
│   ├── hooks/            # （远期）子进程 hook，middleware 的一种实现
│   └── config/           # YAML 配置加载 + 环境变量合并
├── go.mod
└── docs/                 # 设计文档
```

## 核心设计

### 0. Middleware（进程内扩展机制 · 2026-08-07 新增）

参照 AgentScope 的第一支柱：**capabilities 叠加在 reasoning loop 上，不揉进 loop 里**。agent 核心循环只做 采样→工具→回填；压缩/权限/记忆/AGENTS.md 注入全部作为 middleware 挂载。

```go
type Middleware interface {
    OnAgent(ctx, in AgentInput, next Next[AgentInput])                          // 包一层完整回复（洋葱）
    OnReasoning(ctx, in ReasoningInput, next Next[ReasoningInput])              // 包一个推理轮（洋葱）
    OnActing(ctx, in ActingInput, next Next[ActingInput])                       // 包一次工具执行（洋葱）→ 权限扩展点
    OnModelCall(ctx, in ModelCallInput, next Next[ModelCallInput])              // 包一次模型调用（洋葱）
    OnSystemPrompt(ctx, current string) string                                  // 组装系统提示（transformer 链）
}
```

- **两种类型**：前四者是 **onion**（`next.apply(input)` 进入内层，前后都可插逻辑、可观察事件流）；`onSystemPrompt` 是 **transformer**（前输出 → 后输入，从左到右）。
- **挂载点用途映射**：`onActing` = 工具权限扩展点（阶段三 approval 挂这）；`onSystemPrompt` = 系统提示词动态拼接 + AGENTS.md 注入（阶段四 agentsmd 挂这）；`onReasoning` = 上下文压缩（阶段四 compact 挂这）。
- **阶段落地**：阶段二只搭**挂载点骨架**（链机制 + 事件流走通，中间件本体为空实现），避免阶段二就把工程能力写进 agent.go。

**事件模型（分层 · 2026-08-07 探索确认）**：不引入 AgentScope 的 start/delta/end 三件套，按两层分工：

- **provider 层 `Event`（采样级，贴近 SDK）**：现有 4 类（text_delta / tool_call / done / error）→ 扩展：
  - `thinking_delta`——推理文本（anthropic 完整流式 thinking_delta）
  - 生命周期 `start` / `done` / `failed` / `incomplete`——信号来源：anthropic message_start/stop + error（失败是阶段四 overflow 安全网的触发信号）
- **agent 层回合级事件（阶段二新建）**：`turn_done`（回合结束 = 测试锚点）、`tool_result`（工具执行结果，provider 不感知执行）等，渲染器 / `--json` / TUI 订阅

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

- **（2026-08-07）AgentScope 调研 → 架构修订**：用户提供 AgentScope Java v2 llms.txt（`agent-scope-llms.txt`），通读其 20 篇核心文档后经问答确认 4 项架构决策（详见决策表 + 核心设计 0 + ADR-021）：
  1. **扩展机制**：进程内 middleware（5 hook，洋葱式）替代子进程 hooks 作为核心扩展点；子进程 hooks 降级远期
  2. **agent 架构**：纯 ReAct loop + middleware 挂载点骨架，capabilities 不揉进 loop（阶段二即搭骨架）
  3. **会话状态**：JSONL 消息流 + 轻量 AgentState 快照（权限/todo/plan 指针/摘要 → 完整 resume）
  4. **权限**：保持三档，复杂规则匹配引擎不强做——middleware 挂载点承载后续演进

- **（2026-08-07 续）移除 openai wire + 事件模型分层**（用户决策 + 两 SDK 能力探索）：
  1. **移除 openai wire（Responses 与 Chat Completions 都不要），只留 anthropic Messages**：provider 单一 wire 最大 simple，thinking/事件形状唯一；代价是 openai 兼容端点（DeepSeek openai 格式 / 阿里 qwen）不再支持，DeepSeek 只能走其 anthropic 兼容端点
  2. **事件模型分层**：provider 层 Event 扩展（thinking_delta + 生命周期 start/done/failed/incomplete，信号源自 anthropic message_start/stop + error）+ agent 层回合级事件（turn_done / tool_result）。不搞 AgentScope start/delta/end 三件套

- **（2026-08-07 续 2）workspace / compaction / 子 agent 三点确认**（AgentScope 调研第三轮）：
  1. **Workspace**：`~/.harness/` 统一目录（sessions/subagents/tools.json/memory/plans 收敛一处），AGENTS.md 项目级向上搜索保留（两源拼接注入）
  2. **Compaction**：TokenBudget v1 + 摘要式 + 大工具结果 eviction；**不做 overflow 安全网**（eviction 撑住宽度后超限概率低，砍被动抢救）
  3. **子 agent**：**内置几个**（general-purpose 等）+ **并行** + **状态跟踪**（pending/running/completed）；自定义声明式（subagents/*.md）预留扩展点；细节阶段五探讨

## 实施阶段

### 阶段 1：骨架 + 统一消息模型 + Provider + 最小 loop ✅ 已完成（2026-08-04）
**目标**：项目初始化（go.mod + 目录结构）、`messages` 包（统一 Message 模型 + JSONL 序列化）、`provider` 包（Provider/LLMClient 接口 + OpenAI Responses/chat 适配 + Anthropic 适配 + 重试）、最小 agent loop（单次采样，无工具）
**成功标准**：`harness run "你好"` 能从真实 API 拿到流式回复 ✅（DeepSeek 兼容端点验证通过）
**测试**：provider 单测（mock HTTP）✅；loop 单测（mock LLMClient 返回固定事件流）✅

> 阶段 1 详细设计见 `docs/tasks/TASKS.md`（单元 1.1~1.9 全部完成）。重试项：两 SDK 内置退避重试（ADR-012）。thinking 推理模式（ADR-020）：模型级配置 + 双 wire 标准参数 + CLI 运行时覆盖，真实 API 双 wire 验证通过。

### 阶段 2：工具系统 + 并发执行 + 终端渲染 + middleware 挂载点骨架 ✅ 已完成（2026-08-07）
**目标**：**移除 openai wire** ✅（删除 openai.go 及相关测试，config/校验/枚举/loadConfig 调整，provider 只留 anthropic；真实 API 复验 deepseek-claude）；`tools` 包（Tool 接口 + 注册表 + 错误二分类）、内置工具（read_file/list_dir/glob/write_file/shell_command/apply_patch）、并行执行 + call_id 回填、完整简单的终端渲染（thinking + 工具调用展示 + --json）；**agent 重构为纯 ReAct loop + middleware 挂载点骨架**（链机制 + 事件流走通，onActing 即预留的工具权限扩展点）；**`harness` 交互式 REPL**
**成功标准**：`harness run "读取当前目录文件列表并告诉我"` 能触发工具调用并正确回填；**多轮工具调用闭环 + 终端渲染完整可跑 —— 阶段二完成 = 一个可用的简单终端 CLI agent 循环**
**测试**：各工具单测（临时目录）；loop 并发执行测试（mock 3 个 tool_call 验证并发 + 回填顺序）

**自动化测试方案（2026-08-06 调研 + 决策）**：
- **分层**：
  1. 进程内逻辑测试：`go test` + FakeClient（agent 循环正确性，主力）
  2. 进程外端到端回归：**ActiveState/termtest** 驱动真实 harness 进程，LLM 端点指向 **mock HTTP server**（确定性，不依赖 LLM 非确定性输出）——回归测试不用真实 LLM 端点
  3. **真实 API 冒烟**（方案 B 决策）：CI 末尾跑 2-3 条真实调用（DeepSeek 便宜），宽松断言（退出码 0、有 assistant_message、无 error 事件），验证协议兼容/参数被接受/链路通
  4. CI 编排（GitHub Actions 或现有 CI），长跑测试全程 timeout 兜底（防 agent 死循环挂死）
- **关键设计**：agent 层输出**回合边界事件**（`--json` 的 `turn_done`），作为自动化测试的断言锚点——这是工具无法替代的设计责任
- **工具选型理由**：termtest = expect 语义（SendLine/Expect/ExpectExitCode）+ vt10x 虚拟终端渲染模拟（匹配"用户看到的内容"）+ **Windows 原生 ConPTY**（本项目 Windows 环境）+ Go 同语言
- **后续可选**：录制回放（首次真实 API 录响应，之后回放），兼得真实性与确定性，实现成本高，后续阶段再评估
- **待验证**：termtest 驱动 harness 的集成 demo（Windows 下跑通 `SendLine → Expect → ExpectExitCode`）

### 阶段 3：审批（三档，作为 onActing middleware）+ 错误重试
**目标**：`approval` 包实现三档权限（readonly / acceptedit / bypass）+ 黑白名单 + TTY 交互 + allowlist，**以 onActing middleware 挂载**（拒绝/确认结果回填，拒绝 ≠ Fatal）；规则匹配引擎保留扩展点（复杂匹配不强做）；错误重试完善（429 依赖 SDK，补充流中断恢复）
**成功标准**：危险命令按权限档位放行/确认/拒绝；middleware 链能拦截工具执行；429 重试生效
**测试**：审批策略单测（黑白名单匹配）；middleware 链单测；重试单测（mock 429 响应）

### 阶段 4：Workspace（~/.harness/）+ 会话（JSONL + AgentState 快照）+ 系统提示词拼接 + 压缩
**目标**：**`~/.harness/` 统一 workspace**（sessions/快照、subagents/*.md 预留、tools.json、memory/）；`session` 包（JSONL 消息流 + **轻量 AgentState 快照** + resume，落 workspace）；`agentsmd` 包（**作为 onSystemPrompt middleware** 注入，配合动态系统提示词拼接，AGENTS.md 项目级向上搜索保留）；`compact` 包（TokenBudget v1 + 摘要式 + **大工具结果 eviction**，作为 onReasoning middleware，**不做 overflow 安全网**）
**成功标准**：`harness resume --last` 能完整恢复（含权限/todo 等非消息状态）；AGENTS.md 注入生效；系统提示词动态组装；长会话自动压缩；超大工具结果落盘 + read_file 指针
**测试**：session 单测（写读回放 + 快照往返）；agentsmd 单测（临时目录向上搜索）；compact 单测（token 估算超限触发 + eviction 阈值触发）

### 阶段 5：子 Agent（内置 + 并行 + 状态）+ CLI 完善 + 文档
**目标**：**内置几个子 agent**（general-purpose 等）+ **并行执行** + **状态跟踪**（pending/running/completed）+ `send_message` 单向通信（fork 过滤保留）；自定义声明式（subagents/*.md）预留扩展点；Renderer 接口完成（simple 渲染器 + --json 模式）、config 包（YAML 加载）、CLI 子命令完善、docs/ 设计文档
**成功标准**：`harness run "用子 agent 分析这个目录结构"` 端到端跑通；并行子 agent 状态可查；`--json` 输出结构化事件；config 文件可配置
**测试**：子 agent 单测（mock provider 验证 spawn 流程 + 状态迁移）；CLI 端到端测试；config 单测

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
