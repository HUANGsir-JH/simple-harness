//go:build windows

package tools

import "os/exec"

// applyProcessGroup Windows：PowerShell 子进程无 POSIX 进程组语义，no-op。
// 超时由 CommandContext 杀掉 powershell 本身，后台子进程残留风险记录在
// Bug06(b)（taskkill /T 杀树另议，2026-08-10 定为非交付）。
func applyProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup Windows：no-op（进程组不可用）。
func killProcessGroup(cmd *exec.Cmd) {}
