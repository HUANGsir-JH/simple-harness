"""deepswe.py — DeepSWE（Datacurve）适配器：Pier 0.3.1 真实接口（已实机验证）。

接入方式（调研 + pier 0.3.1 源码实机核对，2026-08-19）：
- 官方评测框架 = Pier（pip 包 datacurve-pier，0.3.1；DeepSWE v1.1 要求 >0.3.0）
- 自定义 agent = `BaseInstalledAgent`（pier.agents.installed.base）：
    - `install_spec() -> AgentInstallSpec`（构建期步骤；二进制改 setup() 上传）
    - `network_allowlist() -> NetworkAllowlist`（沙箱 no-network，放行 LLM API 域名）
    - `async setup(environment)`（基类已实现 install_spec 步骤；我们追加 upload_file
      上传 harness 二进制 + 评测配置）
    - `async run(instruction, environment, context)`（沙箱内 headless 执行）
- 调用：`pier run -p deep-swe/tasks/<task-id> --agent-import-path runners.deepswe:HarnessAgent
  --ak harness_bin=<路径> --ak harness_config=<路径> --ak api_host=... --env docker`
  （--ak = agent kwarg；PYTHONPATH 需含 eval/ 目录使 import path 可解析）
- 答案契约 = **git commit**：instruction 要求新分支 + 全部 commit；verifier 的
  collect 钩子取 `git diff --binary <base> HEAD` → 独立容器应用 patch 评分
- 沙箱 no-network；agent 超时 5400s；GPU 不需要

本文件双角色：
1. 被 Pier 以 import path 加载（--agent-import-path runners.deepswe:HarnessAgent）；
2. 被编排器（orchestrator/run.py）以 `runners.deepswe` 加载 → list_tasks / run_task。
"""
from __future__ import annotations

import json
import os
import shlex
import shutil
import subprocess
import sys
import time
from pathlib import Path

try:
    from pier.agents.installed.base import BaseInstalledAgent  # type: ignore
    from pier.environments.base import BaseEnvironment  # type: ignore
    from pier.models.agent.install import AgentInstallSpec, InstallStep  # type: ignore
    from pier.models.agent.network import NetworkAllowlist  # type: ignore
except ImportError:
    BaseInstalledAgent = None  # type: ignore  # 仅编排器角色可用

# --- 角色 1：Pier agent 定义 --------------------------------------------------

HARNESS_VERSION = "0.14.1"


class HarnessAgent(BaseInstalledAgent):  # type: ignore[misc]
    """在 DeepSWE 沙箱内执行 `harness run <instruction>` 的 Pier agent。

    kwargs（经 pier --ak 或 AgentConfig.kwargs 注入）：
      harness_bin   宿主侧 harness Linux 二进制路径（setup 时 upload 进沙箱）
      harness_config 宿主侧 HARNESS_HOME/config.yaml 路径（内联 API key + bypass +
                     top_p/temperature；upload 到 /harness-home/config.yaml）
      api_host      LLM API 域名（network_allowlist 放行；默认 api.deepseek.com）
      max_turns     harness --max-turns（防死循环；默认 100）
    """

    def __init__(self, logs_dir, harness_bin="", harness_config="",
                 api_host="api.deepseek.com", max_turns=100, *args, **kwargs):
        # 只消费自身 kwargs，其余（model_name/extra_env/version/prompt_template_path
        # 等）经 *args/**kwargs 透传给基类——基类签名
        # (logs_dir, prompt_template_path, version, extra_env, *args, **kwargs)，
        # 位置传参会错位（TypeError: multiple values for extra_env）。
        super().__init__(logs_dir, *args, **kwargs)
        self.harness_bin = harness_bin
        self.harness_config = harness_config
        self.api_host = api_host
        self.max_turns = max_turns

    @staticmethod
    def name() -> str:
        return "simple-harness"

    def version(self) -> str | None:
        return HARNESS_VERSION

    def install_spec(self) -> AgentInstallSpec:
        """构建期步骤：仅建目录（二进制经 setup 上传，构建期不可得）。"""
        return AgentInstallSpec(
            agent_name=self.name(),
            version=self.version(),
            steps=[InstallStep(user="root", run="mkdir -p /harness-home /usr/local/bin")],
        )

    def network_allowlist(self) -> NetworkAllowlist:
        """沙箱 no-network：放行 LLM API 域名（评测配置 api_host 覆盖）。"""
        return NetworkAllowlist(domains=[self.api_host])

    async def setup(self, environment: BaseEnvironment) -> None:
        await super().setup(environment)
        # 上传 harness 二进制 + 评测配置（HARNESS_HOME/config.yaml）。
        if self.harness_bin:
            await environment.upload_file(Path(self.harness_bin), "/usr/local/bin/harness")
            await environment.exec(command="chmod +x /usr/local/bin/harness", user="root")
        if self.harness_config:
            await environment.exec(command="mkdir -p /harness-home", user="root")
            await environment.upload_file(Path(self.harness_config), "/harness-home/config.yaml")

    async def run(self, instruction: str, environment: BaseEnvironment, context) -> None:
        """沙箱内 headless 执行：harness run → 保存轨迹 → 补 git commit（答案契约）。"""
        cmd = (
            f"cd /app && HARNESS_HOME=/harness-home "
            f"harness run --json --effort max --max-turns {self.max_turns} "
            f"{shlex.quote(instruction)}"
        )
        result = await self.exec_as_agent(environment, command=cmd)
        # 保存 harness --json 事件流（populate_context_post_run 解析用量/步数）。
        try:
            traj = self.logs_dir / "harness-trajectory.jsonl"
            traj.parent.mkdir(parents=True, exist_ok=True)
            traj.write_text(result.stdout or "", encoding="utf-8")
        except Exception:
            pass
        # git commit 契约：harness 不自动 commit，adapter 补（只提交目标改动；
        # 无改动/已提交时 git 报错由外层 try 兜底，不影响评分）。
        # 显式身份参数：容器内 git 无 user.name/email 时 commit 失败
        # （"Please tell me who you are"）→ 改动未提交 → collect 的
        # git diff base..HEAD 为空 patch（2026-08-19 实测修复）。
        try:
            await self.exec_as_agent(
                environment,
                command=(
                    "cd /app && git -c user.name='simple-harness' "
                    "-c user.email='harness@eval.local' add -A && "
                    "git -c user.name='simple-harness' "
                    "-c user.email='harness@eval.local' commit -m 'harness: done'"
                ),
            )
        except Exception:
            pass

    def populate_context_post_run(self, context) -> None:
        """从 harness-trajectory.jsonl 解析 token 用量/agent 步数填入 context
        （pier 轨迹统计用；解析失败 no-op）。"""
        traj = self.logs_dir / "harness-trajectory.jsonl"
        if not traj.is_file():
            return
        inp = out = cache = steps = peak = 0
        for line in traj.read_text(encoding="utf-8", errors="replace").splitlines():
            try:
                ev = json.loads(line)
            except Exception:
                continue
            t = ev.get("type")
            if t == "usage":
                u = ev.get("usage") or {}
                i = u.get("input_tokens") or 0
                o = u.get("output_tokens") or 0
                c = (u.get("cache_read_input_tokens") or 0) + (u.get("cache_creation_input_tokens") or 0)
                inp += i
                out += o
                cache += c
                if i + o + c > peak:
                    peak = i + o + c
            elif t == "tool_call":
                steps += 1
        context.n_input_tokens = inp
        context.n_output_tokens = out
        context.n_cache_tokens = cache
        context.n_agent_steps = steps
        context.peak_context_tokens = peak


# --- 角色 2：编排器接口 -------------------------------------------------------

EVAL_ROOT = Path(__file__).resolve().parent.parent  # eval/


def list_tasks(cfg: dict) -> list[dict]:
    """从 DeepSWE 任务目录（config extra.checkout）列任务。

    checkout = deep-swe 仓库本地路径（eval/checkouts/deep-swe），任务在 tasks/。
    """
    extra = cfg.get("benchmarks", {}).get("deepswe", {}).get("extra", {})
    checkout = Path(extra.get("checkout", ""))
    tasks_dir = checkout / "tasks"
    if not tasks_dir.is_dir():
        raise SystemExit(
            f"deepswe: 任务目录不存在: {tasks_dir}（git clone datacurve-ai/deep-swe 后配置 extra.checkout）")
    tasks = []
    for d in sorted(p for p in tasks_dir.iterdir() if p.is_dir()):
        if (d / "instruction.md").is_file():
            tasks.append({"id": d.name, "title": d.name, "dir": str(d)})
    if not tasks:
        raise SystemExit(f"deepswe: tasks/ 下未找到任务（{tasks_dir}）")
    return tasks


def _normalize_tests_lf(task_dir: Path) -> None:
    """把任务 tests/ 目录文本文件统一为 LF（幂等）。

    根因（2026-08-20 实机定位）：Windows 宿主 git core.autocrlf=true 使
    checkout 的 tests/test.sh 变成 CRLF → 容器内 shebang 变成 `#!/bin/bash\r`，
    verifier 执行报 `cannot execute: required file not found` → 无 reward.txt
    → RewardFileNotFoundError（与 verifier 镜像无关；预构建镜像也无法修复，
    因为 pier 的 verifier 是 upload_dir 上传宿主 tests 目录的原字节）。
    deep-swe 任务仓库已设 core.autocrlf=false 并重检为 LF；此处为防御层，
    即使未来重新 clone 又带 CRLF，上传前也会被归一化。
    """
    if not task_dir.is_dir():
        return
    text_exts = {".sh", ".py", ".patch", ".json", ".md", ".txt", ".toml",
                 ".yaml", ".yml", ".go", ".c", ".h", ".rs", ".js", ".ts"}
    for p in (task_dir / "tests").rglob("*"):
        if p.is_file() and p.suffix.lower() in text_exts:
            try:
                data = p.read_bytes()
                if b"\r\n" in data:
                    p.write_bytes(data.replace(b"\r\n", b"\n"))
            except OSError:
                pass


def run_task(task: dict, cfg: dict, harness_bin: str, home: str, workdir: str,
             traj_path: str) -> dict:
    """单任务：`pier run -p <task-dir>`（官方 runner 建沙箱 → HarnessAgent → verifier 评分）。

    - agent kwargs 经 --ak 注入（harness_bin/harness_config/api_host/max_turns）
    - PYTHONPATH 注入 eval/ 目录使 `runners.deepswe` import path 可解析
    - harness_config 优先用 cfg 里的预生成配置（内联 API key）；缺省用
      HARNESS_HOME/config.yaml（此时需 --ae 传 DEEPSEEK_API_KEY）
    """
    pier = shutil.which("pier") or str(EVAL_ROOT / ".venv" / "Scripts" / "pier.exe")
    if not Path(pier).exists():
        return {"status": "error", "failure": "env_error",
                "detail": "pier 未安装（uv pip install datacurve-pier，需 ≥0.3.1）"}
    extra = cfg.get("benchmarks", {}).get("deepswe", {}).get("extra", {})
    checkout = Path(extra.get("checkout", ""))
    task_dir = checkout / "tasks" / str(task["id"])
    if not task_dir.is_dir():
        return {"status": "error", "failure": "env_error",
                "detail": f"任务目录不存在: {task_dir}"}
    # CRLF 防御：verifier 上传 tests 目录原字节，Windows checkout 的 CRLF
    # 会让容器内 test.sh 无法执行（2026-08-20 根因定位，见 _normalize_tests_lf）。
    _normalize_tests_lf(task_dir)
    harness_cfg = extra.get("harness_config_path") or str(Path(home) / "config.yaml")
    budget = cfg.get("budget", {})
    max_turns = int(extra.get("max_turns", budget.get("max_turns", 100)))

    cmd = [
        str(pier), "run",
        "-p", str(task_dir),
        "--agent-import-path", "runners.deepswe:HarnessAgent",
        "--ak", f"harness_bin={harness_bin}",
        "--ak", f"harness_config={harness_cfg}",
        "--ak", f"api_host={extra.get('api_host', 'api.deepseek.com')}",
        "--ak", f"max_turns={max_turns}",
        "--env", extra.get("env", "docker"),
        "-o", str(Path(workdir) / "jobs"),
        "--quiet",
    ]
    env = dict(os.environ)
    env["PYTHONPATH"] = str(EVAL_ROOT) + os.pathsep + env.get("PYTHONPATH", "")

    started = time.time()
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, env=env,
                              timeout=int(extra.get("timeout_s", 5400 + 600)))
    except subprocess.TimeoutExpired:
        return {"status": "error", "failure": "timeout", "score": 0.0,
                "wall_seconds": round(time.time() - started, 2)}
    out = proc.stdout + proc.stderr
    # 权威结果：解析 pier job result.json（stats.evals.<agent>.metrics[0] =
    # reward/f2p；exception_stats = trial 异常）。
    score, n_errors, exceptions = _pier_job_result(Path(workdir) / "jobs")
    status = "pass" if score is not None and n_errors == 0 and score >= 1.0 else "error"
    return {
        "status": status,
        "score": score if score is not None else 0.0,
        "failure": "" if status == "pass" else (exceptions or f"pier exit {proc.returncode}"),
        "exit_code": proc.returncode,
        "detail": out[-2000:],
        "cost_usd": _estimate_cost(Path(workdir) / "jobs", cfg.get("pricing", {})),
        "wall_seconds": round(time.time() - started, 2),
    }


def _estimate_cost(jobs_dir: Path, pricing: dict) -> float:
    """从最新 trial 的 harness-trajectory.jsonl 汇总 usage 事件估算成本 USD。

    pier result.json 的 cost_usd 为 null（pier 不知道定价），改从
    harness --json 事件流（usage 行）按 pricing 表估算。无轨迹/无 usage 时返回 0。
    """
    if not jobs_dir.is_dir():
        return 0.0
    try:
        trials = sorted(jobs_dir.glob("*/[!_]*/agent/harness-trajectory.jsonl"),
                        key=lambda p: p.stat().st_mtime)
        if not trials:
            trials = sorted(jobs_dir.glob("*/agent/harness-trajectory.jsonl"),
                            key=lambda p: p.stat().st_mtime)
        if not trials:
            return 0.0
        inp = out = cache = 0.0
        for line in trials[-1].read_text(encoding="utf-8", errors="replace").splitlines():
            try:
                ev = json.loads(line)
            except Exception:
                continue
            if ev.get("type") != "usage":
                continue
            u = ev.get("usage") or {}
            inp += u.get("input_tokens") or 0
            out += u.get("output_tokens") or 0
            cache += (u.get("cache_read_input_tokens") or 0) + (u.get("cache_creation_input_tokens") or 0)
        pi = float(pricing.get("input", 0) or 0) / 1e6
        po = float(pricing.get("output", 0) or 0) / 1e6
        pc = float(pricing.get("cache_read", 0) or 0) / 1e6
        return round(inp * pi + out * po + cache * pc, 4)
    except Exception:
        return 0.0


def _pier_job_result(jobs_dir: Path) -> tuple[float | None, int, str]:
    """解析 pier --jobs-dir 下最新 job 的 result.json，返回 (score, n_errors, exceptions)。

    pier 0.3.1 的 metrics[0] 字段：reward/f2p/p2p/partial（无 mean）。
    score 取 reward（DeepSWE 官方 pass 标准 = reward 1.0）。
    """
    jobs = sorted(jobs_dir.glob("*/result.json"), key=lambda p: p.stat().st_mtime) if jobs_dir.is_dir() else []
    if not jobs:
        return None, 0, ""
    try:
        rj = json.loads(jobs[-1].read_text(encoding="utf-8"))
        evals = (rj.get("stats") or {}).get("evals") or {}
        for e in evals.values():
            metrics = (e.get("metrics") or [{}])
            m = metrics[0] if metrics else {}
            mean = m.get("reward", m.get("f2p"))
            n_err = e.get("n_errors", 0)
            exc = ""
            for k, v in (e.get("exception_stats") or {}).items():
                exc = f"{k}: {v[0] if isinstance(v, list) and v else v}"
            return mean, n_err, exc
    except Exception:
        pass
    return None, 0, ""
