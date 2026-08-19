# Benchmark 调研矩阵（Eval Suite）

> 调研日期 2026-08-19，调研人 = 后台子代理（web 检索，来源 URL 见各节）。
> 用途：评测接入方案的事实依据。随接入进度更新（✅ 已确认 / ⏳ 调研中）。

## 官方基线（统一出处）

**全部 7 个分数出自 [DeepSeek API 更新日志 2026-07-31「DeepSeek-V4-Flash Update」](https://api-docs.deepseek.com/updates/)**：

> Terminal Bench 2.1: 82.7 / NL2Repo: 54.2 / Cybergym: 76.7 / DeepSWE: 54.4 /
> Toolathlon verified: 70.3 / Agent Last Exam: 25.2 / Automation Bench (Public): 25.1

**Note 1 原文**（评测协议）：*For the Code Agent tasks in the public benchmark
sets, the official DeepSeek-V4-Flash was tested using the DeepSeek Harness
minimal mode (to be released soon) as the framework, with the **max effort
level, topp=0.95, and temperature=1.0***。

- 官方极简模式配置 = `examples/jsonrpc-agent/minimal.cordis.yml`：系统提示固定
  `You are a helpful software engineer assistant.`，工具仅 2 个（owner-scoped
  持久 `bash` + `str_replace_editor`），每任务独立 workspace/session。
- 2026-08-13 V4-Pro GA 同表（87.9/61.5/83.3/62.7/74.1/25.7/31.8），可作后续扩展基线。

**对我们评测协议的约束**：
1. 模型固定 `deepseek-v4-flash`；
2. 采样参数对齐：`max effort`（我们 = `--effort max`）+ `top_p=0.95` +
   `temperature=1.0`（⚠️ 我们 provider/config 目前无 top_p/temperature 配置，需改造）；
3. 全自动无审批交互（官方极简模式无 approval UI）；
4. 每任务独立工作区/会话。

---

## 1. Terminal-Bench 2.1 ✅ 已确认

- **官方**：tbench（terminal-bench.org / github.com/tbench-ai）；TB 2.1 官方评测
  走 **Harbor 框架**：`harbor run -d terminal-bench/terminal-bench-2-1`。
- **自定义 agent 接入**：**没有 `--agent-command`**；官方方式 = 写一个小 Python
  适配器类（Harbor `-a path.to.agent:Class`，老 harness `--agent-import-path`），
  适配器在任务容器内执行 `harness run "<instruction>"`。
  **与 DeepSWE 的 Pier 同属 Harbor 框架家族，适配器模式可复用**。
- **答案采集**：不采集答案文本；由 verifier 测试决定 pass/fail（确定性评分）。
- **任务集**：89 个任务；确定性评分；不需要 GPU；容器默认开放互联网。
- **参考分注意**：82.7 = DeepSeek 官方更新日志（V4-Flash @ Harness 极简模式）；
  另有来源把 TB 2.1 的 82.7 归给 Claude Code + Opus 4.8（tbench.ai 原生 harness，
  AICoderScope 2026-06 引述；官方榜该组合已更新为 78.88%±1.31）——**同分属巧合，
  引用需注明出处与日期**。

## 2. NL2Repo ✅ 已确认

- **官方仓库**：[multimodal-art-projection/NL2RepoBench](https://github.com/multimodal-art-projection/NL2RepoBench)
  （无 pip 包/无 CLI/无版本号；`git clone` + `pip install -r requirements.txt`，
  入口 `python main.py` 读 `config.json`）；论文 [arXiv:2512.12730](https://arxiv.org/abs/2512.12730)
- **自定义 agent 接入**：官方只支持 OpenHands headless（`moduleName/baseUrl/sk`
  注入 config.toml，OpenHands 自驱 loop）。**无 `--agent-command`、无结果文件契约**。
  我们二选一：
  - (a) 改 `openhands_app.py` 的容器启动段 → 换成 `docker run` 我们的
    `harness run "<start.md 内容>"`，复用官方评分；
  - (b) 只复用 `test_files/` + `post_processor.py`，自建 runner（先例：
    elizaOS `code_agent_matrix.py`）。
- **评分**：104 个 Python 库任务；**真实 pytest 执行**（非 LLM judge），
  `post_processor.py` 跑 `test_commands.json`；指标 = 平均测试通过率
  `min(passed/total, 1)` + Pass@1 计数；Easy/Medium/Hard 分层。
- **环境**：Docker + `openhands:0.56` + `runtime:0.56-nikolaik` + 每任务基准镜像
  `ghcr.io/multimodal-art-projection/nl2repobench/<task>:1.0`（amd64）；无 GPU；
  104 任务磁盘数十 GB 量级。
- **成本/耗时**：官方未公布；论文 ~180 轮/任务 ≈ 9 万 token 历史，产出代码
  10–50K token；V4-Flash $0.14/$0.28 per M 下粗估 **~$0.02–0.03/任务**。
- **坑**：必须全自动无审批（GPT-5 因 13.4% 提前终止等输入而低分）；评分前删除
  包管理文件与已知测试文件；[issue #12](https://github.com/multimodal-art-projection/NL2RepoBench/issues/12)
  部分任务测试文件清理不彻底会泄漏；[issue #13/#14](https://github.com/multimodal-art-projection/NL2RepoBench/issues/13)
  7/104 任务 test_commands 含 shell 语法须用 `/bin/sh -lc` 执行；提示词硬编码引用 start.md。

## 3. Cybergym ✅ 已确认

- **官方仓库**：[sunblaze-ucb/cybergym](https://github.com/sunblaze-ucb/cybergym)
  （⚠️ `google/cybergym` 不存在）；pip 包 `cybergym`（Python ≥3.12 + Docker，
  `pip3 install -e '.[dev,server]'`）；**无 `cybergym run` CLI**，模块入口
  `python3 -m cybergym.server` 等。
- **自定义 agent 接入**：官方方式 = 照抄 [cybergym-agent-examples](https://github.com/sunblaze-ucb/cybergym-agent-examples)
  （codex/openhands/enigma/cybench）的 run.py 包装器：`generate_task()` 生成任务
  目录（description.txt/README.md/repo-vul.tar.gz/submit.sh）→ 挂载进容器 →
  以 `codex --full-auto --quiet --model ... "<PROMPT>"` 方式调用 agent →
  agent 用 `bash submit.sh /poc` 提交 PoC 到本地 server（vul 崩溃 + fix 干净 =
  成功），`verify_agent_result.py` 按 agent_id 验证。
  **我们的 `harness run "<PROMPT>"` 直接兼容此模式**（S2 自研驱动）。
- **评分**：PoC 复现成功率；答案 = 最后一次提交（非 stdout/flag 文件）。
- **环境**：Docker；无 GPU（LLM 走 API）；磁盘：HF 数据 ~240GB + server 数据
  binary-only ~130GB / full ~10TB（⚠️ 大）。
- **任务集**：1,507 实例 / 188 个 C/C++ 项目（OSS-Fuzz 漏洞），arvo/oss-fuzz/
  oss-fuzz-latest 三类，level0~3。
- **成本/耗时**：论文 v3 ~$2.0/任务（GPT-4.1）；Whitzard 实测 ~33 分钟/任务、
  ~12.9M tokens/任务、~$0.47/任务（缓存命中高时）。
- **坑**：必须全自动非交互；只给 pre-patch 版本；删 `/src/**/.git` 和 `/tmp/poc`
  防泄漏；server 绑 Docker 网关不可公网暴露；`--mask-map` 打码 task_id；
  先跑 10 任务 subset；2026-08 后按 SUBMISSION.md 报成本。

## 4. DeepSWE ✅ 已确认

- **官方**：[datacurve-ai/deep-swe](https://github.com/datacurve-ai/deep-swe)（Datacurve
  2026，**113 个长程工程任务**，91 仓库、5 语言：TS 35/Python 34/**Go 34**/JS 5/Rust 5；
  ⚠️ 不是旧版"深度学习仓库" benchmark，旧版已消失）；官网
  [deepswe.datacurve.ai](https://deepswe.datacurve.ai/)；论文
  [arXiv:2607.07946](https://arxiv.org/abs/2607.07946)；HF
  [datacurve/deep-swe](https://huggingface.co/datasets/datacurve/deep-swe)。
- **评测框架 = Pier**：`pip install datacurve-pier`（CLI `pier`，v0.3.1，
  Python ≥3.12；v1.1 评分要求 >0.3.0）。
- **自定义 agent 接入**：实现 `BaseInstalledAgent`（`install()` 装进沙箱 /
  `run(instruction, environment, context)` headless 执行 /
  `populate_context_post_run()`），`pier run -p deep-swe/tasks --agent path.to.agent:SomeAgent`。
  官方文档示例 `exec_as_agent(environment, command=f"my-agent run {shlex.quote(instruction)}")`
  **与我们的 `harness run "<task>"` 形态完全一致**。instruction = 任务
  `instruction.md` 全文（平均 2158 字符）。
- **答案契约 = git commit**（非答案文件）：instruction 要求新分支 + 全部 commit；
  verifier 的 collect 钩子执行 `git diff --binary <base_commit> HEAD > model.patch`，
  在**独立纯净容器**应用 patch 跑手写 verifier → `reward.json`（binary reward +
  pass fractions）。
- **评分**：非 SWE-bench fail-to-pass；每任务手写 verifier 验证行为；
  pass@1 = 按任务宏平均；context-window 失败与 agent 超时计失败；
  provider/verifier/network 错误剔除。
- **环境**：Linux + Docker（或 Modal）；每任务 2 CPU/8GB/20GB、**无 GPU**；
  agent 超时 5400s、verifier 1800s；沙箱 **no-network**（per-agent network
  allowlist 放行 LLM API 域名）。
- **成本/耗时**：中位 $3–16/任务、15–55 分钟/任务；全量 113 × 4 rollouts =
  452 trials/配置。
- **坑**：必须非交互无审批；**必须 git commit**（harness 不自动 commit 时
  adapter 需补 `git add -A && git commit`）；只提交目标改动、别把 harness
  自身文件带进仓库；canary 防泄漏；deepseek-v4-flash 同名 7/30→8/1 分数
  7.3→54.4（按模型名固定评测会得到过期结论）。

## 5. Toolathlon ✅ 已确认

- **官方**：[hkust-nlp/Toolathlon](https://github.com/hkust-nlp/Toolathlon)（HKUST-NLP，
  ICLR 2026，[arXiv:2510.25726](https://arxiv.org/abs/2510.25726)，官网
  toolathlon.xyz）。**非 pip 包**：git clone + uv（Python 3.12.11、Node≥22）；
  评测客户端 = 仓库内 `python eval_client.py run|check|status|cancel`（v1.1）。
- **"verified" 含义**：**Toolathlon-Verified 是 2026-06-30 发布的"最终校准版"**
  （任务提示词/ground truth/评测脚本人工复查对齐定稿），**不是人工/LLM 验证流程**。
- **自定义 agent 接入**：官方服务 = 平台制 + OpenAI 兼容 API（服务端固定
  scaffold 跑 ReAct loop），**无 --agent-command**。唯一 CLI 路径 = **本地部署 +
  Decoupled Agent Loop 模式**（`DECOUPLED_AGENT_LOOP.md`）：容器 preprocess →
  MCP gateway 以 **SSE 端点**暴露该任务 MCP 工具 → host 侧 agent loop 连
  gateway → 容器 `container_eval.py` 读轨迹 JSONL + `--agent_exit_code` 评分。
  **⚠️ 硬门槛：harness 必须支持 MCP over SSE 客户端**（32 MCP server / 604 工具），
  或写适配器把 MCP 工具翻译成 shell 调用（官方基础设施是 MCP 制）。
- **评分**：每任务容器内**确定性 eval 脚本**（比对静态 ground truth / 动态实时
  数据）；跑 3 次报 pass@1（±std）、pass@3、pass^3、平均 turn 数。
- **环境**：Linux + Docker/Podman，每任务独立容器；无 GPU；16 核/64GB 参考配置、
  10 并行约 70 分钟跑完 108 任务；需注册 32 个应用账号凭据 +
  `deploy_containers.sh` 部署本地应用（Canvas/Poste.io/WooCommerce/k8s）。
- **任务集**：108 任务、7 类（Research/Campus/Finance/Tech/Business/Daily/E-com）、
  每任务平均暴露 69.9 个工具（28–128）；67% 带初始状态；单任务上限 5400s、
  默认 max turns 100。
- **成本**：大多数模型 <$1/任务（开 prompt caching）；平均 20–27 turn/任务。
- **参考分**：70.3 = DeepSeek-V4-Flash 官方（2026-07-31 更新日志）；
  官方 Note 只提"Code Agent 任务用 Harness 极简模式"，**Toolathlon 是否 harness
  驱动未明确**（如实标注歧义）。
- **坑**：MCP-SSE 客户端能力是硬门槛；终止契约（无工具调用即结束 / claim_done）；
  15–35% 轨迹有超长工具输出需自行截断/摘要；任务指令故意模糊；轨迹格式须按
  Toolathlon dump 格式；评测脚本/ground truth 有 task_artifact_guard 防作弊；
  公共服务限流（180min/24h、3 次/24h）。

## 6. Agent Last Exam ✅ 已确认

- **官方**：[rdi-berkeley/agents-last-exam](https://github.com/rdi-berkeley/agents-last-exam)
  （⚠️ **UC Berkeley RDI × RDI Foundation**，与 Aleph Alpha 无关）；官网
  [agents-last-exam.org](https://agents-last-exam.org/)；论文
  [arXiv:2606.05405](https://arxiv.org/abs/2606.05405)；包 `agent-last-exam`
  0.1.0；CLI：`uv run python -m ale_run run <experiment>.yaml`。
- **评测流程**：provision 沙箱 → stage 任务输入 → 跑 agent → **隐藏 reference
  事后注入** → 本地确定性 `evaluate()` 评分。任务在仓库 `tasks/`（main.py +
  task_card.json），输入数据走 gated HF 数据集 / GCS。
- **自定义 agent 接入**：官方支持，方式是实现 **Deployer Python 类**
  （`install()` / `launch(prompt) -> AgentRunResult` / `parse_artifacts()`）+
  `configs/agents/<name>.yaml` preset。两种形态：
  - **In-sandbox CLI**（Claude Code / Codex 形态）：CLI 二进制注入沙箱 VM，
    prompt 经 stdin（prompt.txt）传入，stdout 落 transcript.jsonl，产物直接写沙箱。
  - Out-of-sandbox host harness（ALE-Claw 形态，MCP bridge）。
  **我们 = In-sandbox CLI 形态：`harness run "<task>"`**。
- **评分**：每任务 `evaluate()` 返回 [0,1]；exact/hash 匹配、数值容差、行为状态、
  少数 vision-LLM 探针；**无人类评委、尽量不用 LLM judge**；无统一 final answer
  文件——按任务描述写指定产物路径（如 `output/result.txt`）。
- **环境**：本地 Docker 子集 99 题（rootless Docker、镜像 ~105GB 解压/42GB 压缩、
  仅 Ubuntu amd64）；ALE-CLI 子集 **105 个 Linux 任务**（`selected_tasks/ale_cli.txt`，
  无需 GPU）；框架需 Linux 宿主；HF gated 数据集需申请。
- **成本/耗时**：$3–15/任务、十几分钟~数小时；5 小时硬上限；官方参考
  Codex(GPT-5.5) ALE-CLI overall 23.3%（论文）/博客 25.2%。
- **坑**：headless 必须禁交互/审批（官方 claude_code preset 用
  `dangerously_skip_permissions: true` + disallowedTools 禁 ask 类工具）；
  stdin 必须 DEVNULL（防挂起）；最常见失败 = "宣称完成但产物缺失"；
  版本 pin；web_search 默认禁用。

## 7. Automation Bench (Public) ✅ 已确认

- **身份**：Zapier [AutomationBench](https://github.com/zapier/AutomationBench)
  （官方仓库），"(Public)" = 仓库发布的 600 任务公开集（官方排行榜用私有集）。
  论文 [arXiv:2604.18934](https://arxiv.org/abs/2604.18934)；发布博客
  [zapier.com/blog/introducing-automationbench](https://zapier.com/blog/introducing-automationbench/)。
- **安装/CLI**：Python ≥3.13（uv）；包 `automation-bench` 1.0.6；CLI
  `auto-bench`（`uv run auto-bench --model ...`）。
- **评测流程**：任务随仓库代码提供，runner **进程内**初始化模拟业务环境（47 个
  模拟 SaaS 工具、~500 REST 端点），function-calling 暴露工具；
  **不采集答案文本，对最终 WorldState 跑断言**。
- **自定义 agent 接入**：**不支持**。只有 OpenAI 兼容 API（Chat/Responses）/
  Anthropic Messages 两种驱动（`--base-url/--api-key/--max-steps/--num-examples/...`）。
  无 `--agent-command`。
- **评分**：**确定性断言，无 LLM judge**（exact string match + 结构检查）；
  指标 = `task_completed_correctly` 均值（pass rate，官方口径），另有
  `partial_credit`。
- **环境**：无 Docker、无 GPU；纯 Python；需要模型 API key。
- **成本**：frontier 模型单任务 $0.4–1.3（官方私有集口径）；600 任务全量数百美元
  量级，建议先 `--num-examples` 冒烟。
- **⚠️ 对 harness 评测的关键结论**：官方 runner 自己驱动 agent loop，环境是
  进程内 Python 状态——**纯 shell/文件工具的 CLI agent 无法直接参与**；25.1 分
  大概率也是 API 直驱（非 Harness）跑出来的。接入我们的 harness 需要自建
  OpenAI 兼容网关把 benchmark 工具集暴露给 agent（复杂），或走 EvalScope
  Bridge（兼容性未确认）。**建议**：Automation Bench 不作为 harness 对比项
  （或仅作"API 模式 + 网关"扩展项，报告中明确标注口径差异）。

---

## 汇总表

| Benchmark | 接入方式 | 任务量 | 评分 | 环境 | 参考分 |
|---|---|---|---|---|---|
| Terminal-Bench 2.1 | S2（Harbor 适配器类，容器内跑 harness run） | 89 | 低 | Docker，无 GPU | 确定性 verifier | 82.7 |
| NL2Repo | S2 自研驱动（复用官方评分） | 104 | pytest 真实执行 | Docker | 54.2 |
| Cybergym | S2 官方示例模式（submit.sh） | 1507 | PoC 复现 | Docker + 240GB 数据 | 76.7 |
| DeepSWE | S2（官方 Pier InstalledAgent，git commit 契约） | 113（×4 rollouts） | $3–16 | Docker，无 GPU | 手写 verifier（patch 应用） | 54.4 |
| Toolathlon | S2（本地 Decoupled 模式；⚠️ 需 MCP-SSE 客户端能力） | 108（×3） | <$1 | Docker，无 GPU，需账号凭据 | 容器内确定性 eval | 70.3 |
| Agent Last Exam | S2（官方 Deployer 类，sandbox-CLI 形态） | 105 (ALE-CLI) / 152 (public) | $3–15 | Docker（~105GB 镜像） | 本地确定性 evaluate() | 25.2 |
| Automation Bench | ⚠️ 不推荐（无 CLI 接入路径） | 600(public) | 确定性断言 | 纯 Python | 25.1 |
