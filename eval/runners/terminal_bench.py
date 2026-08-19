"""terminal_bench.py — Terminal-Bench 2.1 适配器：Harbor agent 类。

接入方式（调研结论，见 docs/eval/BENCHMARKS.md §1）：
- TB 2.1 官方评测走 Harbor：`harbor run -d terminal-bench/terminal-bench-2-1`
- 无 --agent-command；自定义 agent = Python 适配器类
  （Harbor `-a path.to.agent:Class` / 老 harness `--agent-import-path`），
  适配器在任务容器内执行 `harness run "<instruction>"`
- 答案不采集；verifier 测试决定 pass/fail（确定性评分）
- **与 DeepSWE 的 Pier 同属 Harbor 框架家族，agent 类接口同形**

本文件双角色：
1. 被 Harbor 以 import path 加载（-a runners.terminal_bench:HarnessAgent）；
2. 被编排器以 `runners.terminal_bench` 加载 → list_tasks / run_task（调 harbor）。

⚠️ 状态：骨架实现；Harbor 包名/接口签名按调研文档 + Pier 同形推断，
待阶段 0/1 装齐 harbor 后实机验证（TODO(env) 标记处）。
"""
from __future__ import annotations

import shlex
import shutil
import subprocess
import time
from pathlib import Path

try:
    from pier.agents.base_installed import BaseInstalledAgent  # type: ignore
except ImportError:
    try:
        from harbor.agents.base_installed import BaseInstalledAgent  # type: ignore
    except ImportError:
        BaseInstalledAgent = None  # type: ignore


class HarnessAgent(BaseInstalledAgent):  # type: ignore[misc]
    """TB 任务容器内执行 `harness run <instruction>`（容器默认开放互联网）。

    install：把 harness 二进制 + HARNESS_HOME/config.yaml 拷进容器；
    run：cd 任务工作目录 → harness run --json "<instruction>"。
    """

    name = "simple-harness"

    async def install(self):
        # TODO(env): 按 Harbor/Pier 实际 API 调整：拷 harness 二进制与评测配置
        #   （provider + approval.mode: bypass + top_p/temperature）进沙箱。
        return await super().install()

    async def run(self, instruction, environment, context):
        cmd = (
            f"cd $HOME && HARNESS_HOME=/harness-home "
            f"/harness/harness run --json --effort max "
            f"{shlex.quote(instruction)}"
        )
        await self.exec_as_agent(environment, command=cmd)

    def populate_context_post_run(self, context):
        return super().populate_context_post_run(context)


# --- 编排器接口 ---------------------------------------------------------------

def list_tasks(cfg: dict) -> list[dict]:
    """列 TB 2.1 任务（89 个）。骨架：固定编号 0..88 + title 占位；
    TODO(env): 用 harbor 的 dataset 元数据（`harbor list`?）或
    terminal-bench 任务清单生成真实 id/标题。"""
    n = cfg.get("benchmarks", {}).get("terminal-bench", {}).get("extra", {}).get("n_tasks", 89)
    return [{"id": str(i), "title": f"tb2.1-task-{i}"} for i in range(int(n))]


def run_task(task: dict, cfg: dict, harness_bin: str, home: str, workdir: str,
             traj_path: str) -> dict:
    """单任务：`harbor run` 该任务（官方建容器 → HarnessAgent → verifier 评分）。

    调研确认（2026-08-19 终版）：
    - pip 包 `terminal-bench`（import terminal_bench）0.2.18，CLI `tb`；2.1 官方
      评测走 Harbor：`uv tool install "harbor[daytona]"` +
      `harbor run -d terminal-bench/terminal-bench-2-1 -a pkg:Agent -k 5`
    - `-k <trials>`：leaderboard 要求 ≥5 trials/任务（89×5=445）；冒烟期用 1
    - instruction 经 shlex.quote 作为命令行参数传给 agent；最终答案不采集，
      verifier（pytest）全过 = resolved（确定性评分）
    """
    harbor = shutil.which("harbor") or shutil.which("pier")
    if not harbor:
        return {"status": "error", "failure": "env_error",
                "detail": "harbor 未安装（uv tool install \"harbor[daytona]\"）"}
    extra = cfg.get("benchmarks", {}).get("terminal-bench", {}).get("extra", {})
    trials = int(extra.get("trials", 1))  # 冒烟 1，正式按 leaderboard 要求 5
    started = time.time()
    try:
        proc = subprocess.run(
            [harbor, "run", "-d", "terminal-bench/terminal-bench-2-1",
             "-a", "runners.terminal_bench:HarnessAgent",
             "-k", str(trials), "--task", str(task["id"])],
            capture_output=True, text=True, timeout=3600,
        )
    except subprocess.TimeoutExpired:
        return {"status": "error", "failure": "timeout", "score": 0.0,
                "wall_seconds": round(time.time() - started, 2)}
    out = proc.stdout + proc.stderr
    # TODO(env): harbor 单任务 flag 与结果输出格式待实机确认
    #   （pass/fail 判读 + 权威分数来源）；骨架以退出码 + 关键字粗判。
    ok = proc.returncode == 0 and ("pass" in out.lower() or "success" in out.lower())
    return {
        "status": "pass" if ok else "error",
        "score": 1.0 if ok else 0.0,
        "failure": "" if ok else f"harbor exit {proc.returncode}",
        "exit_code": proc.returncode,
        "detail": out[-2000:],
        "wall_seconds": round(time.time() - started, 2),
    }
