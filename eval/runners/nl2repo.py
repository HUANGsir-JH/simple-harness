"""nl2repo.py — NL2Repo（NL2RepoBench）适配器：自研驱动 + 官方评分复用。

接入方式（调研结论，见 docs/eval/BENCHMARKS.md §2）：
- 官方无 CLI/无自定义 agent：OpenHands headless + config.json 注入
- 我们 = S2 自研驱动：官方任务镜像（ghcr.io/multimodal-art-projection/
  nl2repobench/<task>:1.0）内跑 `harness run <prompt>`，答案 = workspace 里的
  整个仓库；评分复用官方 post_processor.py（真实 pytest）
- 任务描述 = 固定提示词 + 任务 start.md（提示词硬编码引用 start.md）
- 评分坑（issue #12/#13/#14）：评分前删包管理文件与已知测试文件；
  含 shell 语法的 test_commands 用 /bin/sh -lc 执行

本文件被编排器以 `runners.nl2repo` 加载：list_tasks / run_task。

⚠️ 状态：骨架实现；依赖本地 NL2RepoBench checkout（git clone
multimodal-art-projection/NL2RepoBench）+ docker 可用。待阶段 0/1 实机验证
（TODO(env) 标记处）。
"""
from __future__ import annotations

import json
import shutil
import subprocess
import time
from pathlib import Path


def _checkout(cfg: dict) -> Path:
    extra = cfg.get("benchmarks", {}).get("nl2repo", {}).get("extra", {})
    p = Path(extra.get("checkout", ""))
    if not p.is_dir():
        raise SystemExit(
            f"nl2repo: checkout 不存在: {p}（git clone "
            "github.com/multimodal-art-project/NL2RepoBench 后配置 extra.checkout）")
    return p


def list_tasks(cfg: dict) -> list[dict]:
    """从 checkout 的任务清单列任务。TODO(env): 按官方 tasks 元数据（config.json/
    tasks.json）解析任务 id 与 start.md 路径；骨架先列 test_files/ 下的目录。"""
    co = _checkout(cfg)
    # 官方任务元数据位置待实机确认；常见布局：test_files/<task>/ 或 tasks/<task>/
    for cand in ("test_files", "tasks", "testfiles"):
        d = co / cand
        if d.is_dir():
            tasks = [{"id": p.name, "title": p.name, "dir": str(p)}
                     for p in sorted(d.iterdir()) if p.is_dir()]
            if tasks:
                return tasks
    raise SystemExit(f"nl2repo: checkout 中未找到任务目录（{co}）")


def _task_image(task_id: str) -> str:
    return f"ghcr.io/multimodal-art-projection/nl2repobench/{task_id}:1.0"


def run_task(task: dict, cfg: dict, harness_bin: str, home: str, workdir: str,
             traj_path: str) -> dict:
    """单任务：容器内跑 harness → 容器外官方评分（post_processor.py / pytest）。"""
    if not shutil.which("docker"):
        return {"status": "error", "failure": "env_error", "detail": "docker 不可用"}
    co = _checkout(cfg)
    task_id = str(task["id"])
    image = _task_image(task_id)
    prompt = task.get("prompt", "")

    started = time.time()

    # 1) 容器内执行 harness（工作目录 = 容器内挂载的 /workspace）。
    #    prompt 传 start.md 内容（官方提示词模板引用 start.md；TODO(env): 确认
    #    官方 -t 提示词模板原文并复用）。
    if not prompt:
        # 常见布局 test_files/<task>/start.md；找不到时用任务目录内任意 start.md
        start = Path(task.get("dir", "")) / "start.md"
        if start.is_file():
            prompt = start.read_text(encoding="utf-8")
    docker_cmd = [
        "docker", "run", "--rm",
        "-v", f"{workdir}:/workspace",
        "-v", f"{home}:/harness-home",
        "-v", f"{harness_bin}:/harness/harness:ro",
        "-e", "HARNESS_HOME=/harness-home",
        "-e", "DEEPSEEK_API_KEY",
        "--workdir", "/workspace",
        image,
        "bash", "-lc",
        # 任务镜像自带基础仓库（布局待实机确认）；先复制到 /workspace 再跑。
        f"cp -r /root/workspace/* /workspace/ 2>/dev/null || true; "
        f"/harness/harness run --json --effort max {prompt!r}",
    ]
    try:
        proc = subprocess.run(docker_cmd, capture_output=True, text=True,
                              timeout=3600)
    except subprocess.TimeoutExpired:
        return {"status": "error", "failure": "timeout", "score": 0.0,
                "wall_seconds": round(time.time() - started, 2)}
    Path(traj_path).write_text(proc.stdout, encoding="utf-8", errors="replace")

    # 2) 官方评分：post_processor.py（test_commands.json → pytest）。
    #    TODO(env): 官方评分脚本入口/参数待实机确认（post_processor.py 读
    #    test_commands.json，可能需 -t <task> 参数）；评分前删包管理文件与
    #    已知测试文件（防泄漏，issue #12）；shell 语法用 /bin/sh -lc（#13/#14）。
    post = co / "post_processor.py"
    grade_cmd = ["python", str(post)] if post.is_file() else None
    score = 0.0
    status = "error"
    failure = f"harness exit {proc.returncode}" if proc.returncode != 0 else ""
    if grade_cmd:
        g = subprocess.run(grade_cmd, capture_output=True, text=True,
                           timeout=1800, cwd=str(co))
        # TODO(env): 解析 success_rate=min(passed/total,1) 与 Pass@1
        try:
            data = json.loads(g.stdout)
            score = float(data.get("success_rate", 0.0))
            status = "pass" if score >= 1.0 or data.get("pass") else "fail"
        except (json.JSONDecodeError, AttributeError):
            score = 0.0
            status = "fail"
            failure = (failure + " | " if failure else "") + "评分输出解析失败"
    return {
        "status": status,
        "score": score,
        "failure": failure,
        "exit_code": proc.returncode,
        "detail": (proc.stderr or "")[-1000:],
        "wall_seconds": round(time.time() - started, 2),
    }
