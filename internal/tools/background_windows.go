//go:build windows

package tools

import (
	"os"
	"os/exec"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// applyProcessGroup Windows：no-op——进程树管理由 Job Object 承担（ADR-038），
// 无需 POSIX 进程组语义。shell.go 的 newShellCmd 统一调用此函数（POSIX 版设
// Setpgid，shell_process_unix.go）。
func applyProcessGroup(cmd *exec.Cmd) {}

// processTreeHandle Windows 平台 = Job Object 句柄（0 = 降级不可用）。
// 进程树生命周期（ADR-038）：每个 shell 调用创建独立 job（只设
// KILL_ON_JOB_CLOSE），进程 Start 后挂入；job 内进程新建的子进程自动归属
// 同一 job——杀 job = 原子杀全树（PowerShell 派生的 python 服务等孙进程全灭）。
//
// KILL_ON_JOB_CLOSE 的内核兜底：句柄关闭即杀树。即使 harness 被硬杀
// （SIGKILL/crash，无任何 defer 执行），进程销毁时句柄被内核关闭 → 树死——
// 这是退出 pre-kill 的兜底层（用户补充需求）。
type processTreeHandle = windows.Handle

// createProcessTree 创建 KILL_ON_JOB_CLOSE job（匿名，无跨进程可见性需求）。
// 只设 kill-on-close，不设内存/CPU/UI 限制（避免干扰后台服务进程）。
func createProcessTree() (processTreeHandle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// attachProcessTree 把已启动进程挂入 job（Start 后调用：需真实 PID 开进程句柄）。
// 失败降级（调用方忽略错误记 h=0）：harness 自身在父 job（CI/IDE 启动器）且
// 不允许 breakaway 时 Assign 会失败——降级后杀树走 taskkill 尽力兜底。
func attachProcessTree(h processTreeHandle, proc *os.Process) error {
	if h == 0 || proc == nil {
		return nil
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(proc.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(h, ph)
}

// killProcessTree Windows：TerminateJobObject 原子杀全树。h==0（降级，如嵌套
// job 限制）→ taskkill /T /F 尽力杀树（Bug06(b) 曾把 taskkill 定为非交付指
// 主路径；此处仅作 job 不可用时的兜底，主路径是 job）。
func killProcessTree(h processTreeHandle, pid int) {
	if h != 0 {
		_ = windows.TerminateJobObject(h, 1)
		return
	}
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

// closeProcessTree 释放 job 句柄。KILL_ON_JOB_CLOSE 语义下，进程仍活着时
// 关闭句柄 = 内核杀树（正常退出时进程已死，空 job 关闭无害）。
func closeProcessTree(h processTreeHandle) {
	if h != 0 {
		_ = windows.CloseHandle(h)
	}
}
