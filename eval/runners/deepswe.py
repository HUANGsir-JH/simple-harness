"""deepswe.py — DeepSWE（Datacurve）适配器：官方 Pier 框架的 InstalledAgent。

接入方式（调研结论，见 docs/eval/BENCHMARKS.md §4）：
- 官方评测框架 = Pier（`pip install datacurve-pier`，CLI `pier`，≥0.3.1）
- 自定义 agent = 实现 `BaseInstalledAgent`：
    install() / run(instruction, environment, context) / populate_context_post_run()
- 答案契约 = **git commit**：instruction 要求新分支 + 全部 commit；verifier 的
  collect 钩子取 `git diff --binary <base> HEAD` → 独立容器应用 patch 评分
- 运行：`pier run -p deep-swe/tasks --agent runners.deepswe:HarnessAgent`

本文件双角色：
1. 被 Pier 以 import path 加载（--agent runners.deepswe:HarnessAgent）→ 容器内跑
   `harness run`；
2. 被编排器（orchestrator/run.py）以 `runners.deepswe` 加载 → list_tasks 列任务、
   run_task 调 `pier run` 单任务执行。

⚠️ 状态：骨架实现，接口签名按调研文档；待阶段 0/1 装齐 pier 后实机验证
（TODO(env) 标记处）。
"""
from __future__ import annotations

import json
import os
import shlex
import subprocess
import sys
import time
from pathlib import Path

# --- 角色 1：Pier agent 定义 --------------------------------------------------

try:
    from pier.agents.base_installed import BaseInstalledAgent  # type: ignore
except ImportError:  # TODO(env): pier 未安装时的降级占位（仅编排器角色可用）
    BaseInstalledAgent = None  # type: ignore


class HarnessAgent(BaseInstalledAgent):  # type: ignore[misc]
    """在 DeepSWE 沙箱内执行 `harness run <instruction>` 的 Pier agent。

    沙箱 no-network（LLM API 域名经 Pier 网络 allowlist 放行）；agent 超时
    5400s；最终答案靠 git commit 采集（instruction 约定）。
    """

    name = "simple-harness"

    def install_spec(self):
        """把 harness Linux 二进制打进沙箱镜像（Dockerfile 内联）。"""
        # TODO(env): 按 Pier 实际 API（install_spec/install）调整：
        #   二进制路径经 Pier 配置（kwargs）传入，或在此从 cfg 读取。
        return super().install_spec()

    async def install(self):
        # TODO(env): 若 Pier 用 install() 而非 install_spec()：把 harness 二进制
        #   与评测 config（HARNESS_HOME/config.yaml：provider + bypass）拷进沙箱。
        return await super().install()

    async def run(self, instruction, environment, context):
        """沙箱内 headless 执行：harness run → 补 git commit。"""
        # harness 在工作目录执行（instruction 要求新分支工作 + commit）。
        cmd = (
            f"cd /app && HARNESS_HOME=/harness-home "
            f"/harness/harness run --json --effort max "
            f"{shlex.quote(instruction)}"
        )
        await self.exec_as_agent(environment, command=cmd)
        # 答案契约 = git commit：harness 不自动 commit，adapter 补（只提交目标
        # 改动；失败静默——无改动/已提交时 git 报错不影响评分）。
        await environment.exec("cd /app && git add -A && git commit -m 'harness: done' || true")

    def populate_context_post_run(self, context):
        """解析轨迹/产物（Pier 需要时实现；当前无额外上下文）。"""
        return super().populate_context_post_run(context)


# --- 角色 2：编排器接口 -------------------------------------------------------

def list_tasks(cfg: dict) -> list[dict]:
    """从 DeepSWE 任务目录（config extra.checkout）列任务。

    checkout = deep-swe 仓库本地路径（git clone github.com/datacurve-ai/deep-swe），
    任务在 tasks/ 下（每任务一个子目录，含 instruction.md/task.toml）。
    """
    checkout = Path(cfg.get("benchmarks", {}).get("deepswe", {}).get("extra", {}).get("checkout", ""))
    tasks_dir = checkout / "tasks"
    if not tasks_dir.is_dir():
        raise SystemExit(f"deepswe: checkout 任务目录不存在: {tasks_dir}（git clone 后配置 extra.checkout）")
    tasks = []
    for d in sorted(p for p in tasks_dir.iterdir() if p.is_dir()):
        if (d / "instruction.md").is_file():
            tasks.append({"id": d.name, "title": d.name, "dir": str(d)})
    if not tasks:
        raise SystemExit(f"deepswe: tasks/ 下未找到任务（{tasks_dir}）")
    return tasks


def run_task(task: dict, cfg: dict, harness_bin: str, home: str, workdir: str,
             traj_path: str) -> dict:
    """单任务：调 `pier run`（官方 runner 建沙箱、跑 HarnessAgent、verifier 评分）。"""
    pier = shutil.which("pier")
    if not pier:
        return {"status": "error", "failure": "env_error",
                "detail": "pier 未安装（pip install datacurve-pier，需 ≥0.3.1）"}
    checkout = cfg.get("benchmarks", {}).get("deepswe", {}).get("extra", {}).get("checkout", "")
    tasks_path = str(Path(checkout) / "tasks")
    # 单任务：--task <id>？TODO(env)：确认 pier 单任务 flag（--task/--tasks/--n-tasks）
    # 骨架先按官方示例 `pier run -p <tasks> --agent ... --n-tasks 1 --sample-seed 0`
    # 不可行时改为 `--task <id>`（Pier 0.3.x 支持按任务过滤）。
    started = time.time()
    try:
        proc = subprocess.run(
            [pier, "run", "-p", tasks_path, "--agent", "runners.deepswe:HarnessAgent",
             "--task", str(task["id"]), "--env", "docker"],
            capture_output=True, text=True, timeout=5400 + 600,
        )
    except subprocess.TimeoutExpired:
        return {"status": "error", "failure": "timeout", "score": 0.0,
                "wall_seconds": round(time.time() - started, 2)}
    # Pier 输出 reward.json（binary reward + pass fractions）；骨架先以退出码 +
    # stdout 尾部判读，TODO(env)：解析 reward.json 取权威分数。
    out = proc.stdout + proc.stderr
    ok = proc.returncode == 0 and "reward" in out.lower()
    # TODO(env): 从 pier 产物目录读 reward.json → score/pass
    return {
        "status": "pass" if ok else "error",
        "score": 1.0 if ok else 0.0,
        "failure": "" if ok else f"pier exit {proc.returncode}",
        "exit_code": proc.returncode,
        "detail": out[-2000:],
        "wall_seconds": round(time.time() - started, 2),
    }
