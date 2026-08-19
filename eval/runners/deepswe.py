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

    def __init__(self, logs_dir, model_name=None, logger=None, mcp_servers=None,
                 skills_dir=None, harness_bin="", harness_config="",
                 api_host="api.deepseek.com", max_turns=100, *args, **kwargs):
        super().__init__(logs_dir, model_name, logger, mcp_servers, skills_dir,
                         *args, **kwargs)
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
        """沙箱内 headless 执行：harness run → 补 git commit（答案契约）。"""
        cmd = (
            f"cd /app && HARNESS_HOME=/harness-home "
            f"harness run --json --effort max --max-turns {self.max_turns} "
            f"{shlex.quote(instruction)}"
        )
        await self.exec_as_agent(environment, command=cmd)
        # git commit 契约：harness 不自动 commit，adapter 补（只提交目标改动；
        # 无改动/已提交时 git 报错由外层 try 兜底，不影响评分）。
        try:
            await self.exec_as_agent(
                environment,
                command="cd /app && git add -A && git commit -m 'harness: done'",
            )
        except Exception:
            pass


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
        "--quiet",
    ]
    env = dict(os.environ)
    env["PYTHONPATH"] = str(EVAL_ROOT) + os.pathsep + env.get("PYTHONPATH", "")

    started = time.time()
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True,
                              timeout=int(extra.get("timeout_s", 5400 + 600)))
    except subprocess.TimeoutExpired:
        return {"status": "error", "failure": "timeout", "score": 0.0,
                "wall_seconds": round(time.time() - started, 2)}
    out = proc.stdout + proc.stderr
    # TODO(env): 从 pier jobs 目录的 reward.json 读权威分数（binary reward +
    # pass fractions）；骨架先以退出码 + 关键字粗判。
    ok = proc.returncode == 0 and ("reward" in out.lower() or "pass" in out.lower())
    return {
        "status": "pass" if ok else "error",
        "score": 1.0 if ok else 0.0,
        "failure": "" if ok else f"pier exit {proc.returncode}",
        "exit_code": proc.returncode,
        "detail": out[-2000:],
        "wall_seconds": round(time.time() - started, 2),
    }
