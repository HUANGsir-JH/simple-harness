# pier Windows 宿主 CRLF 补丁（simple-harness eval 环境专用）

**问题**：pier 0.3.1 在 Windows 宿主上 `Path.write_text` 默认文本模式写 CRLF。
`write_docker_proxy_compose` 生成的 `start-squid.sh`（agent_setup.py）带 CRLF →
容器内 heredoc 写出的 `/tmp/squid.conf` 带 `\r` → squid 解析失败
（`FATAL: Bungled (null) line 0`）→ egress 代理容器退出 1 → 环境构建失败
（deep-swe 等 no-network 任务必现）。

**修复**：`eval/.venv/Lib/site-packages/pier/environments/agent_setup.py` 两处
`write_text` 加 `newline="\n"`（Dockerfile + start-squid.sh）。

**重放**（venv 重装后）：

```powershell
# 编辑 eval\.venv\Lib\site-packages\pier\environments\agent_setup.py：
#   1) (proxy_dir / "Dockerfile").write_text(..., newline="\n")
#   2) (proxy_dir / "start-squid.sh").write_text(squid_bootstrap_command(), newline="\n")
# 或直接替换：
$f = "eval\.venv\Lib\site-packages\pier\environments\agent_setup.py"
$c = Get-Content $f -Raw
$c = $c.Replace('(proxy_dir / "start-squid.sh").write_text(squid_bootstrap_command())',
                '(proxy_dir / "start-squid.sh").write_text(squid_bootstrap_command(), newline="\n")')
$c = $c.Replace('        )\n    )\n    (proxy_dir / "start-squid.sh")', ...)  # 见下
Set-Content $f $c -NoNewline
```

**注意**：若任务环境目录（deep-swe/tasks/<id>/environment）里的 Dockerfile/脚本
也被 git 以 CRLF 检出（core.autocrlf），需同样处理（本次 clone 未触发，暂记录）。

**上游**：属 pier Windows 宿主兼容问题，可向 datacurve-ai/pier 提 issue。
