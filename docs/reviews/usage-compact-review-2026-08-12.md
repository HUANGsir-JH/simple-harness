# 用量展示 + 上下文压缩代码审查报告

> 审查对象：`f2cda04`（用量展示，ADR-037 第一段）及其后的功能提交：
> `1a7ee0e`（thinking 完整回传，阶段 B）、`4b9ad06`（LLM 摘要式压缩，阶段 C）、
> `79b62b1`（LastContextTokens 口径勘误）、`2dda0a4`（EventCompactStart 扩展）
> 审查日期：2026-08-12
> 范围：新增/修改 30+ 文件，+1800 行；compact 包、UsageMiddleware、
> RuntimeContext.Segment/Emit 钩子、TUI footer//usage//compact、Session 切段
> 方法：结论经可执行探针证实（探针跑完即删，未入库）；`go build/vet/test ./...`
> 全绿，相关包 `-race` 全绿

> **修复状态（2026-08-13）**：12 项逐点决策后修复 11 项，回归测试锁定，
> 全量测试与 -race 全绿。✅ 01/02/03/04/06/07/08/09/10/12；05 接受设计
> （注释记录）；**11 待实测**——12 修复后 thinking 完整回传，上一轮输出计入
> 下一轮输入，footer 应恢复单调，实测确认后再决定是否改口径（详见
> docs/tasks/DECISIONS.md ADR-037 勘误 2026-08-13）。
> 修复关键点：01 补"压缩后采样请求 = [summary]"回归锚点 + Build 注册顺序
> 交换（Compact 在 TodoReminder 前）；12 补 `signature_delta` 分支；10 按用户
> 决策改覆盖语义（`SetUsage`）；06 压缩后 Usage 归零；07 删除
> `AgentState.Summary`（BuildSummaryPrompt 简化新建式）；03 先落盘后重写；
> 04 `Options.SystemPromptTokens` 补齐估算缺口；08 错误路径补发
> EventCompacted；09 help 补命令 + 全 gofmt。

## 摘要

架构层面是扎实的：`rc.Emit`/`rc.Segment` 沿用 `rc.Approver` 的"session 注入闭包
防环"模式；用量累计与压缩都挂在 onReasoning 上、agent 核心不碰 state（ADR-021）；
`79b62b1` 的勘误口径（单轮完整占用 = input + cache_read + cache_creation +
output）有真实数据锁定（665+239691+100+7900=248356）；Summarizer 只收
`EventTextDone` 防双重计数、失败/取消绝不重写 conversation、手动路径显式落盘
AgentState——这些细节都经测试锁定，质量在线。

但发现 **1 项严重缺陷 + 1 项中等问题**，其余为低风险观察项：

| 编号 | 缺陷 | 级别 | 证据 |
|---|---|---|---|
| 01 | 压缩成功后**同一轮采样仍发送压缩前的旧上下文**（CompactMiddleware 未把重写后的 conversation 传进 `in.Messages`）→ 触发压缩的那轮仍可能爆窗；且该轮 usage 把 LastContextTokens 重新抬高 → footer 显示压缩前占用、下轮/下回合重复压缩（工具循环单回合触发 2 次摘要调用） | 严重 | 探针（采样请求首条仍是旧消息；摘要调用 2 次） |
| 02 | Summarizer 摘要请求固定用装配默认模型（`opts.Model`），忽略 `rc.Model`——`/model` 切换后摘要与正常采样模型不一致 | 中等 | 探针（rc.Model=session-model，摘要请求仍 built-model） |
| 03 | `Runner.Run` 先重写 conversation/state、后调 Segment；Segment 失败时双轨不一致 | 低 | 代码路径（当前注入的 Segment 闭包恒返回 nil，实际不可达） |
| 04 | `EstimateTokens` 兜底低估真实占用（不含系统提示/工具 schema），注释声称"更早触发"方向相反 | 低 | 代码分析 |
| 05 | usage 驱动触发有一轮滞后（回合间新用户消息、上一轮工具结果不计入 LastContextTokens） | 低 | 代码分析（20K 截断缓解） |
| 06 | 摘要请求自身的 token 消耗不记账（/usage 总账缺压缩调用） | 低 | 代码分析 |
| 07 | previous summary 双份喂给 LLM（conversation 首条即旧摘要 + `<previous-summary>`） | 低 | 代码分析 |
| 08 | 压缩成功但随后采样失败时不发 EventCompacted（压缩其实已生效，UI 只见错误） | 低 | 代码分析 |
| 09 | 帮助面板缺 /usage /compact；`gofmt -l` 报 agent_test.go | 低 | 实测 |

> **实测补充（见文末）**：用户实测反馈后经真实 API 请求复核，新增 3 项——
> 10（`AddUsage` 累计 cache_read 虚高，与 ADR-037 勘误自相矛盾，中等）、
> 11（`LastContextTokens` 含 output 致 footer 非单调"变小"，中等）、
> 12（thinking 完整回传从未生效：signature 全空，DeepSeek 流式签名走
> `signature_delta` 事件而代码未处理，严重）。

---

## 已验证缺陷

### 01. 压缩后同一轮采样仍发送压缩前的旧上下文（严重）

`CompactMiddleware.OnReasoning`（`internal/middleware/impl/compact.go`）在
`Runner.Run` 成功后**没有把重写后的 conversation 传回采样输入**：

```go
func (m CompactMiddleware) OnReasoning(...) error {
	if m.Runner != nil && rc != nil {
		if _, err := m.Runner.Run(ctx, rc, false); err != nil {
			return err
		}
	}
	return next(ctx, rc, in) // in.Messages 仍是压缩前的旧切片
}
```

数据流：`agent.Run` 每轮调用 `reasoning(ctx, rc, ReasoningInput{Messages:
conversation.Messages, ...})` 时**捕获快照**；`Runner.Run` 压缩成功重写的是
`rc.Messages.Messages = [summary]`，但 `in.Messages` 仍指向旧切片；采样
（`agent.sample` → `ModelCallInput{Messages: in.Messages}`）发的是**旧消息**。

探针（agent 包内，FakeClient 记录每次采样请求内容）：3 条消息的会话超阈值触发
压缩后，采样请求首条仍是 `"问题1"`（旧会话），而非 `"总结内容"`（摘要占位）。

后果链：

1. **触发压缩的那一轮仍然以完整旧上下文采样**——压缩要防止的爆窗在该轮没被
   防止（真实场景下该轮请求可能逼近/超过 context_window，直接 400）。
2. 该轮采样（旧上下文）的 usage 经 `UsageMiddleware` 把 `LastContextTokens`
   重新写回压缩前的大值 → footer 继续显示 `ctx 950k/1.0M`（实际 conversation
   已是 [summary]）；该值随 SessionMiddleware 落盘 → **下一回合刚启动又触发
   一次压缩**（对已很小的小会话做一次浪费的摘要调用）。
3. 工具循环场景（压缩后采样带 tool_call）：探针证实单回合内摘要调用 **2 次**
   ——第二次由被抬高的 LastContextTokens 触发（"重压缩"），虽然代价小（小会话），
   但每次触发都会先 emit EventCompactStart + 阻塞 Summarize。

**测试盲区**：`TestRunAutoCompact` 只断言 conversation 变成
`[summary, assistant]`，**未断言采样请求本身的内容**——而 FakeClient 恰好按
"末条是否含 anchored summary"分流，采样请求发什么都返回正常流，缺陷被掩盖。
修复方向：`Runner.Run` 成功后在 CompactMiddleware 内
`in.Messages = rc.Messages.Messages`（一行），并补断言"压缩后采样请求 = [summary]"
的回归测试。

### 02. Summarizer 摘要请求忽略会话级模型切换（中等）

`compact.Summarizer.Summarize` 发请求时用 `s.opts.Model`（`agent.Build` 装配时的
默认模型），从不读 `rc.Model`——而正常采样走 `agent.sample` 的
`model := a.model; if rc.Model != "" { model = rc.Model }`。`/model` 切换后：
正常采样用新模型，**压缩摘要仍用旧模型**（探针证实：rc.Model=session-model，
摘要请求 Model=built-model）。摘要质量/档位与当前会话不一致，且用户视角不可见。
修复方向：`Summarize` 内 `model := s.opts.Model; if rc != nil && rc.Model != "" {
model = rc.Model }`，与 sample 同规则。

---

## 低风险观察项

### 03. Segment 失败时的双轨不一致（理论路径）

`Runner.Run` 的顺序是：重写 `rc.Messages` → 更新 State（Summary/清零）→ 调
`rc.Segment`。若 Segment 返回错误，函数返回错误、回合终止——但 conversation 与
state 已改（内存），transcript 仍是旧段：resume 会恢复旧历史 + `LastContextTokens=0`
（估算兜底小）→ 重新压缩旧历史。当前 session 注入的 Segment 闭包（NewSegment +
Write + Flush）恒返回 nil，故实际不可达；但作为契约，先落盘后重写的顺序更稳
（或把 Segment 失败降级为警告而非终止）。

### 04. EstimateTokens 兜底方向与注释相反

`estimate.go` 注释称"估算只覆盖输入侧（不含 output），方向保守（更早触发）"。
实际估算**漏掉系统提示与工具 schema**（这部分随每次请求发送，可能数 KB），
是低估 → `ShouldCompact` 更**晚**触发，与"更早触发"相反。该兜底只在
`LastContextTokens==0` 时生效（首轮/压缩后），且一旦端点不返回 usage（某些
兼容端点），压缩触发点会系统性偏移——建议修正注释并考虑计入系统提示。

### 05. usage 驱动触发的一轮滞后（设计观察）

`ShouldCompact` 读的是**上一轮**请求的 usage：回合间新加的用户消息、上一轮
工具结果都不在其中。单轮增长受 ToolOutputMiddleware 20K 截断约束，实际越过
85% 的幅度有界，可接受；但"新用户消息可能在下一轮采样才计入"意味着触发点
略晚于真实占用，与 04 同向。

### 06. 摘要请求不记账

`/usage` 累计的是正常采样 usage；Summarize 单独采样（完整上下文 + 4096 输出）
的消耗既不进 `AgentState.Usage` 也不进 `LastContextTokens`。计费口径下
`/usage` 偏低。若用户在意账单，可在 Summarize 成功后把其 usage 一并累计。

### 07. previous summary 双份

压缩后 conversation 首条就是旧摘要 user 消息；下轮压缩的摘要请求 = [旧摘要
user, BuildSummaryPrompt(previous=旧摘要)]——同一份摘要既在 conversation 又在
`<previous-summary>` 里，重复喂给 LLM（浪费输入 token，可能轻微干扰"更新式"
判断）。建议压缩时把 `AgentState.Summary` 与 conversation 的关系理清（例如
BuildSummaryPrompt 检测到 conversation 首条即摘要时不再嵌 previous）。

### 08. 压缩成功 + 采样失败 → 无 EventCompacted

`agent.Run` 在 `reasoning()` 返回错误时提前 return，`CompactedKey` 检查（发
EventCompacted）被跳过。压缩已生效（conversation 已重写、transcript 已切段、
state 已更新），但 TUI 只显示错误，用户不知道压缩成功了；下轮重试即正常。
建议错误路径也读标记补发一次。

### 09. 杂项

- TUI 帮助面板（`renderHelp`）未列 `/usage`、`/compact`（命令补全里有）。
- `gofmt -l` 报 `internal/agent/agent_test.go`（2dda0a4 引入 `indexOf` 时少了
  一个空行）。
- 阶段 B 边角（非本次两个特性本身，但影响压缩摘要请求）：多 thinking 块时
  `sample` 只保留**最后一块**签名、`tb` 累加全部文本，重放为单块——严格端点
  校验签名会 400（DeepSeek 宽松无碍）；该问题同时作用于正常采样与摘要请求。

---

## 测试与构建状态

- `go build ./...` / `go vet ./...` 干净；`go test ./...` 全绿
  （含 internal/e2e 5.2s）。
- `go test -race`（agent/compact/middleware/impl/agentstate/session/tui）全绿。
- 覆盖亮点：`TestAnthropicStreamCapturesUsage`（message_start + delta 合成）、
  `TestUsageMiddlewareContextTokensTotal`（真实数据锁定勘误口径）、
  `TestSummarizeRequestShape`（请求形状：完整 conversation + prompt 尾 user、
  无工具、max_tokens）、`TestRunnerRunFailureKeepsHistory` /
  `TestRunnerRunCancelKeepsHistory`（失败/取消不重写）、`TestRunnerRunEmitStart`
  （start 门控三分支）、`TestRunAutoCompact`（事件时序 start < compacted）。
- **覆盖缺口**：没有任何测试断言"压缩后采样请求 = [summary]"（缺陷 01 因此
  漏网）；Summarizer 无"rc.Model 优先"测试（缺陷 02 同理）。

---

## 设计层面

- **触发滞后与口径**：`LastContextTokens` 包含 output_tokens 是 ADR-037 勘误的
  显式决策（opencode tokens.total 口径），虽使"当前占用"略高于下一请求的实际
  输入（上一轮输出会被再次计入），但方向保守，接受。
- **85% 硬编码**：无配置项，与 ADR 一致；触发后靠"下一轮 usage 变小"自然回落，
  无死区问题（除缺陷 01 的重复触发路径）。
- **压缩即回合边界**：压缩失败/Esc 直接终止整轮（ADR-037 决策）——用户消息
  已在 conversation，下轮重试；配合"失败不重写"，历史零丢失，设计自洽。
- 建议在修复缺陷 01/02 后补两条回归（采样请求内容断言 + Summarizer 模型优先），
  并考虑 06/07 的记账与去重（若用户在意计费与 token 成本）。

---

## 实测补充（2026-08-12 用户实测反馈后验证）

用户实测发现两个用量显示问题后，用**真实 API 请求**（从 transcript 重建
conversation 发给 DeepSeek 实测）复核，另发现一个阶段 B 的功能失效。以下
结论全部经真实请求/原始 SSE 证实（探针跑完即删）。

### 10. `AddUsage` 累计 cache_read 造成 /usage 虚高（中等，与 ADR 自相矛盾）

现场 state：`usage.cache_read_input_tokens = 6,294,400`（6.3M）。实测证实
DeepSeek 热缓存下每轮 `cache_read ≈ 当前历史全量`（R3 重复请求 cache_read=
20992/总 21004，turn2 轮 cache_read=123008/总 123064）——它是"当前上下文占用"
而非增量。`AddUsage` 把它跨轮相加 50+ 次，虚高到"好多 M 的错觉"。

**ADR-037 勘误自己写了"累计值不能相加（跨轮重复累加会虚高）"，但勘误只修了
`LastContextTokens` 口径，`AddUsage` 仍在累加 cache_read**——实现与决策矛盾。
且缓存命中不计费，累计它既无账单意义也无上下文意义（上下文占用另有
`LastContextTokens`）。修复方向：`AddUsage` 不累计 `CacheReadInputTokens`
（或 /usage 只展示最近一轮 cache_read）。

### 11. `LastContextTokens` 含 output 导致 footer "多跑一轮反而变小"（中等）

实测：153k 显示轮 = 大 thinking 轮（input+cache≈90k + 单轮 thinking 输出
47.6K 字符 ≈62k tokens）；下一轮 input+cache=123k（上下文实际**变大**）但
output≈0.2k → 显示 123k。footer 值 = input+cache+**output**（ADR-037 勘误的
opencode tokens.total 口径），随单轮输出长短大幅波动、非单调，给用户
"上下文变小"的错觉；且与压缩触发/估算兜底（`EstimateTokens` 只算输入侧）
口径不一致。

修复方向：`SetLastContextTokens` 去掉 output（= input + cache_read +
cache_creation），显示单调且与压缩判定统一。注意：`LastContextTokens` 本身
实测与真实请求吻合（123235 vs 123064，差值仅系统提示/tool schema 计法），
该值用于触发压缩是可靠的。

### 12. thinking 完整回传从未生效——signature 全空（严重，阶段 B 功能失效）

现场 transcript 90/90 条 thinking 行 `signature` 全空。原始 SSE 实测证实
根因：DeepSeek **流式**响应中 `content_block_start` 的 thinking 块 signature
是**空串**（`"signature":""`），签名经独立的 **`signature_delta`** 事件下发
（`{"type":"signature_delta","signature":"<id>"}`）；而
`anthropic_stream.go` 只在 `content_block_start` 读 `cb.Signature`，**没有
`signature_delta` 分支** → 签名全部丢弃 → thinking 只存不重放（实际行为退回
ADR-025 原设计"存但不重放"）。

影响：thinking 完整回传（ADR-025 修订 / ADR-037 第二段）交付的功能实际未生效；
摘要请求同样看不到 thinking。这**不影响** usage 显示的正确性（usage 反映真实
请求），但 `EstimateTokens` 按"含 thinking 回传"估算会高估。修复方向：
`anthropic_stream.go` 补 `signature_delta` 分支（SDK delta union 含
`SignatureDelta` 变体），在 content_block_stop 前把 signature 挂到 pendingBlock。
