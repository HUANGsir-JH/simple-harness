# Eval 已知问题与修复记录（2026-08-19 实机调试）

> 记录 Windows 宿主跑 DeepSWE/Terminal-Bench 评测时遇到的全部问题与处置。
> 状态：✅ 已修复 / 🟡 临时方案 / ⏳ 待调查 / ℹ️ 环境注意。
> 目标：下次重装环境/换机器时按此文档快速恢复，避免重复踩坑。

---

## 1. 编排器自身 bug（已修复）

### 1.1 subprocess 没传 env（✅ 已修复）
- **现象**：pier 报 `ValueError: Failed to import module 'runners.deepswe': No module named 'runners'`。
- **原因**：`eval/runners/deepswe.py` 的 `run_task` 构建了 `env`（含 PYTHONPATH）
  但 `subprocess.run()` **漏传 `env=env`**——死代码，PYTHONPATH 从未进子进程。
- **修复**：`subprocess.run(cmd, ..., env=env)`。
- **教训**：写 subprocess 时立刻检查 env 参数；PYTHONPATH 注入依赖它。

### 1.2 dry-run 写 meta.json 崩溃（✅ 已修复）
- **现象**：`run.py --dry-run` 报 `FileNotFoundError: .../results/<run-id>/meta.json`。
- **原因**：写 meta.json 前没建 `results_root` 目录。
- **修复**：`os.makedirs(results_root, exist_ok=True)` 前置。

### 1.3 后台 pwsh 任务默认 workdir = 仓库根（✅ 已改习惯）
- **现象**：多次 `& .\eval\.venv\Scripts\python.exe ...` 后台任务报
  `The term '.\eval\.venv\Scripts\python.exe' is not recognized`。
- **原因**：后台 pwsh 未传 `workdir` 时默认 `D:\agent-project\harness`（仓库根），
  eval 目录在 `simple-harness/` 下。
- **处置**：所有命令显式传 `workdir`。

---

## 2. pier 0.3.1 接口与调研文档的差异（✅ 已适配）

### 2.1 agent 基类位置
- 调研/Harbor 文档说 `pier.agents.base_installed`，实际模块是
  **`pier.agents.installed.base.BaseInstalledAgent`**（pier 0.3.1）。

### 2.2 抽象方法清单（实现 agent 必须全部实现）
```
BaseAgent:        name() / version() / setup() / run()
BaseInstalledAgent: + install_spec() / populate_context_post_run()
```
- 漏 `populate_context_post_run` 报：
  `TypeError: Can't instantiate abstract class HarnessAgent without an
  implementation for abstract method 'populate_context_post_run'`。

### 2.3 `__init__` 位置参数契约（pier 0.3.1 坑）
- 基类签名：`(logs_dir, prompt_template_path=None, version=None, extra_env=None, *args, **kwargs)`。
- 若自定义 `__init__` 按 `(logs_dir, model_name, logger, ...)` 位置透传，会把
  `model_name` 塞进 `prompt_template_path` 槽位、`extra_env` 冲突：
  `TypeError: BaseInstalledAgent.__init__() got multiple values for argument 'extra_env'`。
- **正确姿势**：`__init__(self, logs_dir, <自己的kwargs>, *args, **kwargs)`，
  其余全部经 `*args/**kwargs` 透传。

### 2.4 调用方式（已实机验证）
```
pier run -p <tasks>/<task-id> \
  --agent-import-path runners.deepswe:HarnessAgent \
  --ak harness_bin=<绝对路径> --ak harness_config=<绝对路径> \
  --ak api_host=api.deepseek.com --ak max_turns=100 \
  --env docker -o <jobs-dir> --quiet
```
- `-p` 支持单任务目录（README 确认）；`--ak` 注入 kwargs；
  `-o/--jobs-dir` 指定结果目录（结果解析用）。

---

## 3. pier Windows 宿主问题（核心坑）

### 3.1 egress-proxy 构建的 squid 启动崩溃 —— CRLF（✅ 已修复，补丁在 eval/patches/）
- **现象**：环境启动失败，trial.log：
  `dependency failed to start: container ...-pier-egress-proxy-1 exited (1)`；
  手动复现 squid 日志：`FATAL: Bungled (null) line 0`。
- **原因**：pier 在 Windows 用 `Path.write_text()` 默认文本模式写
  `start-squid.sh`（agent_setup.py `write_docker_proxy_compose`）→ CRLF → 容器内
  heredoc 生成的 `/tmp/squid.conf` 带 `\r` → squid 解析失败。
- **修复**：venv 内 `pier/environments/agent_setup.py` 两处 `write_text` 加
  `newline="\n"`（Dockerfile + start-squid.sh）。重放步骤见
  `eval/patches/pier-windows-crlf.md`。
- **触发条件**：任务禁网（no-network）+ agent 有 network_allowlist → pier 建
  egress 代理（deep-swe 全量任务都触发）。
- **上游建议**：向 datacurve-ai/pier 提 issue（Windows 宿主 CRLF）。

### 3.2 verifier 报 `/tests/test.sh: cannot execute` → RewardFileNotFoundError（✅ 已修复，根因 = CRLF）
- **现象**：trial 完成 agent 阶段后 verifier 报
  `RewardFileNotFoundError`；verifier/test-stdout.txt：
  `bash: line 1: /tests/test.sh: cannot execute: required file not found`；
  trial.log：`Skipping image OS validation for hb__<task>__agent-<fp>: docker inspect returned 1`。
- **根因（2026-08-20 实机定位，推翻"镜像缺失"旧结论）**：Windows 宿主
  `git core.autocrlf=true` 使 checkout 出的 `tests/test.sh` 为 **CRLF** →
  容器内 shebang 变成 `#!/bin/bash\r` → bash 报 `cannot execute: required file
  not found` → 测试根本没跑 → 无 reward.txt/reward.json。
  **与 verifier 镜像无关**：pier 的 verifier（separate 模式）是
  `upload_dir` 把宿主 tests/ 目录**原字节**上传进环境再执行，预构建镜像
  无法修复（镜像里烘焙的 test.sh 也是从宿主 COPY 的 CRLF，或 verifier 根本
  不用镜像里的 tests）。
- **修复**：
  1. `git -C <deep-swe> config core.autocrlf false` + `git config core.eol lf`
     + `git rm --cached -r .` + `git reset --hard <原HEAD>`（工作区全部转 LF，
     **不要提交 .gitattributes**——会让 main 前进、污染 collect 钩子的
     base commit diff）。
  2. 防御层：`eval/runners/deepswe.py` 新增 `_normalize_tests_lf()`——
     run_task 上传前把 tests/ 目录文本文件 CRLF→LF（幂等；未来重新 clone
     再带 CRLF 也不怕）。
- **验证**：修复后 test.sh/grader.py/test.patch CRLF 计数均为 0。
- **注意**：trial.log 中 `git commit` 报 `Command failed` 是**无害的**——
  instruction 要求模型自己 commit（模型已提交），adapter 兜底 commit 因
  nothing-to-commit 失败，collect 钩子 diff 仍正常产出 patch。

### 3.3 agent 环境镜像名含 install fingerprint
- 主环境镜像名 = `hb__<env>__agent-<fingerprint>`（随 install_spec 变化）；
  verifier 镜像 = `hb__<env>`（无 fingerprint，agent_install_spec=None）。
  排查镜像缺失时按此规则找名字。

### 3.4 瞬时网络抖动（ℹ️ 环境注意）
- egress 构建时 `apt-get update` exit 100 一次（手动重构建成功）——Ubuntu 源
  瞬时不可达；重试即可。

---

## 4. DeepSWE 评测链路问题（已修复/待观察）

### 4.1 git commit 失败 → model.patch 为空（✅ 已修复）
- **现象**：collect 钩子 `git diff --binary <base> HEAD > model.patch` 产出
  0 字节 patch → verifier 无 reward。
- **原因**：容器内 git 无 user.name/email，`git commit` 失败
  （"Please tell me who you are"）→ 改动未提交 → diff 为空。
- **修复**：adapter 的 commit 命令加 `-c user.name='simple-harness'
  -c user.email='harness@eval.local'`。

### 4.2 harness 侧观察（ℹ️ 记录）
- `--no-thinking` 下 DeepSeek 端点仍返回 thinking 块（端点忽略 disabled 参数）。
  评测用 max effort（thinking 开），无影响。
- agent 全 shell 工具工作正常（120 次工具调用、真实求解任务）；子 agent/
  等子未在本次任务触发。

### 4.3 pier result.json metrics 无 `mean` 字段（✅ 已修复，2026-08-20）
- **现象**：首个通过任务（reward=1.0, f2p 20/20, p2p 3/3）被编排器标成
  `[error] score=0.0`。
- **原因**：pier 0.3.1 的 `stats.evals.<agent>.metrics[0]` 字段是
  `reward/f2p/p2p/partial`（**没有 `mean`**）；`_pier_job_result` 取
  `metrics[0].get("mean")` 恒为 None → score=None → 判 error。
- **修复**：score 改取 `reward`（缺省 f2p）；DeepSWE 官方 pass 标准 =
  reward 1.0。回放真实 result.json 验证 `(1.0, 0, '')`。

### 4.4 成本恒为 $0.000（⏳ 待补）
- pier result.json 的 `cost_usd` 为 null（pier 不知道我们的定价）；runner
  未从 trajectory 的 usage 事件估算成本。后续在 `deepswe.py` 按 pricing
  表（input/output/cache_read）补估算。

---

## 5. 环境状态备忘（ℹ️）

| 项 | 状态 |
|---|---|
| Docker Desktop | ✅ 运行中（28.5.2，Hyper-V 后端；**WSL 未装但不需要**——pier/harbor 走 Docker API） |
| eval/.venv | ✅ Python 3.12.4（基于 Anaconda），pier 0.3.1 + terminal-bench + pyyaml |
| eval/harness-linux | ✅ 0.14.1 交叉编译（GOOS=linux） |
| eval/harness-config.yaml | ✅ 复制用户配置 + bypass + top_p/temperature（key 不落对话/提交） |
| checkouts | ✅ deep-swe（113 任务）+ NL2RepoBench |
| 待安装 | harbor（`uv pip install harbor` 已装 ✅，CLI 待验证） |

---

## 6. 待办调查项（⏳）

1. ~~**pier verifier 镜像构建失败根因**（3.2）~~ ✅ **已定位**：不是镜像问题，
   是 tests/test.sh CRLF（见 3.2）。重跑验证中。
2. **NL2Repo 评分链路**：post_processor.py 在 `openhands/` 下、无 argparse 入口
   （import 式调用），需按官方 main.py 流程梳理评分调用方式后再接。
3. **Terminal-Bench（Harbor）**：harbor 包已装，CLI 用法待验证。
4. **WSL**：用户要求安装，需管理员权限 + 可能重启；评测不依赖（Docker Hyper-V
   后端可用），重启会打断评测，建议评测出数后再装。
