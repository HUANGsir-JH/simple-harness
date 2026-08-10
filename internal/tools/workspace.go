package tools

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-project/harness/internal/middleware"
)

// Workspace 路径解析与边界判定（Bug03，2026-08-10）。
//
// 会话的 workspace = state.CWD（会话启动目录，ADR-028 种的死字段在此复活）。
// 工具把模型给的路径经 ResolveInWorkspace 规范化为绝对路径（相对路径以
// workspace 为基），不再原样交给 os 调用。
//
// 软边界：工具层只规范化、不拒绝越界——越界是否放行交给审批 Decide
// （范围内按 class 规则 / 范围外 Ask；bypass 不受限）。词法校验
// （filepath.Clean），不解析符号链接（symlink 逃逸是 v1 已知局限）。

// ResolvePath 把路径解析为绝对路径：相对路径以 ws 为基（ws 空 = 进程 cwd），
// 绝对路径直接 Clean。纯函数，供审批 Decide 与工具 Handle 共用。
func ResolvePath(ws, path string) string {
	if path == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		base := ws
		if base == "" {
			base, _ = os.Getwd()
		}
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

// InWorkspace 判定 abs 是否位于 ws 内（含 ws 本身；ws 空 = 进程 cwd）。
// 前缀校验：Rel 不以 ".." 开头即视为在范围内。
func InWorkspace(ws, abs string) bool {
	root := ws
	if root == "" {
		root, _ = os.Getwd()
	}
	rel, err := filepath.Rel(filepath.Clean(root), abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// workspaceOf 取工具的 workspace 根：rc.State.CWD（会话启动目录）优先，
// nil 回退进程 cwd（rc 传 nil 的既有测试行为不变）。
func workspaceOf(rc *middleware.RuntimeContext) string {
	if rc != nil && rc.State != nil {
		return rc.State.CWD
	}
	return ""
}

// ResolveInWorkspace 是工具 Handle 用的路径规范化入口：把模型给的路径解析为
// 绝对路径（相对路径以会话 workspace 为基）。软边界——不拒绝越界。
func ResolveInWorkspace(rc *middleware.RuntimeContext, path string) string {
	return ResolvePath(workspaceOf(rc), path)
}
