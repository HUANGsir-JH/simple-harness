"""ale.py — Agent Last Exam（rdi-berkeley）适配器：官方 Deployer 类（sandbox-CLI）。

接入方式（调研结论，见 docs/eval/BENCHMARKS.md §6）：
- 官方 runner：`uv run python -m ale_run run <experiment>.yaml`
- 自定义 agent = 实现 Deployer Python 类（install()/launch(prompt)->
  AgentRunResult/parse_artifacts()）+ `configs/agents/<name>.yaml` preset
- 我们 = **In-sandbox CLI 形态**：harness 二进制注入沙箱 VM，prompt 经 stdin
  （prompt.txt）传入，stdout 落 transcript.jsonl，产物直接写沙箱（按任务描述
  写指定路径，如 output/result.txt）
- 评分：沙箱内本地确定性 evaluate()；无统一 final answer 文件
- 环境：本地 Docker 子集（ALE-CLI 105 个 Linux 任务，无需 GPU）；HF gated
  数据集需申请

本文件双角色：
1. 被 ALE runner 以 Deployer 加载（configs/agents/simple_harness.yaml →
   harness: 本类 import path）；
2. 被编排器以 `runners.ale` 加载 → list_tasks / run_task（调 ale_run run）。

⚠️ 状态：骨架实现；Deployer 接口签名按调研文档，待阶段 2 装齐
agent-last-exam 后实机验证（TODO(env) 标记处）。
"""
from __future__ import annotations

import shutil
import subprocess
import time
from pathlib import Path

try:
    from ale_run.agents.deployer import Deployer, AgentRunResult  # type: ignore
except ImportError:  # TODO(env): agent-last-exam 未安装时的降级占位
    Deployer = object  # type: ignore


class SimpleHarnessDeployer(Deployer):  # type: ignore[misc]
    """In-sandbox CLI 形态：沙箱内执行 `harness run "<prompt>"`。

    - install：把 harness 二进制 + HARNESS_HOME/config.yaml 拷进沙箱
    - launch：stdin=DEVNULL 防挂起；stdout → transcript.jsonl；stderr → stderr.log
    - 产物：agent 按任务描述写指定路径（沙箱内）；parse_artifacts 拷贝回 host
    """

    name = "simple_harness"
    supported_executors = ["sandbox"]

    def install(self, executor):
        # TODO(env): 按 ALE Deployer 实际 API 调整：拷 harness 二进制与
        #   评测配置（provider + bypass）进沙箱；版本探测/固定可参考官方
        #   claude_code preset（版本 pin）。
        return super().install(executor)

    def launch(self, prompt):
        # TODO(env): 按 AgentRunResult 实际字段构造；stdin=DEVNULL；
        #   prompt 写入沙箱 prompt.txt 后以参数传入（或 stdin 管道）。
        #   超时 5h 上限由 ALE runner 外部控制。
        return AgentRunResult()

    def parse_artifacts(self, executor, run_dir):
        # TODO(env): 把沙箱内产物（按任务描述写的 output/* 等）拷回 host。
        return super().parse_artifacts(executor, run_dir)


# --- 编排器接口 ---------------------------------------------------------------

def list_tasks(cfg: dict) -> list[dict]:
    """列 ALE-CLI 任务（105 个 Linux 任务）。TODO(env): 从 checkout 的
    selected_tasks/ale_cli.txt 解析真实任务 id（任务 id 形如 task-<name>）。"""
    extra = cfg.get("benchmarks", {}).get("ale", {}).get("extra", {})
    checkout = Path(extra.get("checkout", ""))
    tasks_file = checkout / "selected_tasks" / "ale_cli.txt"
    if tasks_file.is_file():
        ids = [ln.strip() for ln in tasks_file.read_text(encoding="utf-8").splitlines()
               if ln.strip() and not ln.startswith("#")]
        if ids:
            return [{"id": i, "title": i} for i in ids]
    # 骨架兜底：前 10 个占位（真实 id 待 checkout 就绪）
    return [{"id": f"task-{i}", "title": f"ale-cli-{i}"} for i in range(10)]


def run_task(task: dict, cfg: dict, harness_bin: str, home: str, workdir: str,
             traj_path: str) -> dict:
    """单任务：`ale_run run` 该任务（官方建沙箱 → Deployer → 本地 evaluate）。"""
    extra = cfg.get("benchmarks", {}).get("ale", {}).get("extra", {})
    checkout = extra.get("checkout")
    if not checkout:
        return {"status": "error", "failure": "env_error",
                "detail": "ale: 缺 extra.checkout（agent-last-exam 仓库路径）"}
    started = time.time()
    # 实验配置（YAML）骨架：TODO(env) 按官方 experiment schema 生成，指向
    #   configs/agents/simple_harness.yaml（harness: SimpleHarnessDeployer）。
    exp = Path(workdir) / f"experiment-{task['id']}.yaml"
    exp.write_text(
        f"agent: simple_harness\ntask: {task['id']}\nenvironment: docker\n"
        f"output_dir: {workdir}/results\n", encoding="utf-8")
    try:
        proc = subprocess.run(
            ["uv", "run", "python", "-m", "ale_run", "run", str(exp)],
            cwd=checkout, capture_output=True, text=True, timeout=3600)
    except subprocess.TimeoutExpired:
        return {"status": "error", "failure": "timeout", "score": 0.0,
                "wall_seconds": round(time.time() - started, 2)}
    out = proc.stdout + proc.stderr
    # TODO(env): 从 ALE 输出/产物解析 evaluate() 分数（[0,1]）；骨架粗判。
    ok = proc.returncode == 0
    return {
        "status": "pass" if ok else "error",
        "score": 1.0 if ok else 0.0,
        "failure": "" if ok else f"ale_run exit {proc.returncode}",
        "exit_code": proc.returncode,
        "detail": out[-2000:],
        "wall_seconds": round(time.time() - started, 2),
    }
