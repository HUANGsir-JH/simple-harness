// Package session 管理 workspace（~/.harness）下的会话：目录布局、项目桶、
// AgentState 快照与块级 transcript 落盘/resume（ADR-025）。
//
// 布局（用户确认 + AgentScope/codex 参考）：
//
//	~/.harness/                      # 全局根（$HARNESS_HOME 覆盖）
//	├── config.yaml                  # 全局配置（已有查找逻辑）
//	├── agents.md                    # 全局 persona（总是加载，叠加项目级 AGENTS.md）
//	├── workspaces/<项目转义>/       # 项目分桶
//	│   └── <session-id>/            # 目录名即会话 id（时间戳-随机）
//	│       ├── agentstate.json      # AgentState 快照
//	│       ├── historys/            # 块级 transcript（history-<n>.jsonl，压缩切分）
//	│       └── plans/               # 本会话计划
//	└── subagents/ memory/ logs/     # 预留占位
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// EnvHome 覆盖 workspace 根目录（测试/定制；对标 codex CODEX_HOME）。
const EnvHome = "HARNESS_HOME"

// workspace 布局目录常量。
const (
	DirWorkspaces = "workspaces"
	DirSubagents  = "subagents"
	DirMemory     = "memory"
	DirLogs       = "logs"
	// 会话内子目录。
	DirHistorys = "historys"
	DirPlans    = "plans"
)

// FileAgentsMD 是全局 persona 文件（本阶段建占位；注入逻辑阶段四 onSystemPrompt）。
const FileAgentsMD = "agents.md"

// Store 管理 workspace 根下的目录布局。
type Store struct {
	root string
}

// New 解析 workspace 根：$HARNESS_HOME 优先，否则 ~/.harness。
func New() (*Store, error) {
	if env := os.Getenv(EnvHome); env != "" {
		return &Store{root: env}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("session: resolve home dir: %w", err)
	}
	return &Store{root: filepath.Join(home, ".harness")}, nil
}

// NewAt 使用显式根目录（测试/定制）。
func NewAt(root string) *Store { return &Store{root: root} }

// CreateInCWD 在当前工作目录创建会话：建骨架 + 定位项目桶 + 新建会话。
// CLI 会话创建的入口（运行/REPL 新会话）。mode 是默认审批模式（config
// approval.mode 播种值，ADR-029；空 = 不固化）。
func CreateInCWD(model, mode string) (*Session, error) {
	store, err := New()
	if err != nil {
		return nil, err
	}
	if err := store.EnsureDirs(); err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	proj, err := store.FindProject(cwd)
	if err != nil {
		return nil, err
	}
	return proj.Create(model, cwd, mode)
}

// Root 返回 workspace 根。
func (s *Store) Root() string { return s.root }

// EnsureDirs 创建目录骨架（全局占位 + workspaces），并建 agents.md 占位文件。
// 占位文件已存在时跳过（不覆盖用户编辑）。
func (s *Store) EnsureDirs() error {
	dirs := []string{
		s.root,
		filepath.Join(s.root, DirWorkspaces),
		filepath.Join(s.root, DirSubagents),
		filepath.Join(s.root, DirMemory),
		filepath.Join(s.root, DirLogs),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("session: mkdir %s: %w", d, err)
		}
	}
	return ensurePlaceholder(filepath.Join(s.root, FileAgentsMD),
		"# 全局 persona（总是加载，叠加项目级 AGENTS.md）\n")
}

func ensurePlaceholder(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// Project 是一个工作区（项目目录）桶。
type Project struct {
	Path string // 原始项目路径（如 D:\agent-project\harness）
	Dir  string // 桶目录（如 <root>/workspaces/D__agent-project_harness）
}

// ProjectDir 返回某项目路径的桶目录（不检查存在）。
func (s *Store) ProjectDir(projectPath string) string {
	return filepath.Join(s.root, DirWorkspaces, EscapePath(projectPath))
}

// FindProject 返回 cwd（启动时的 pwd）对应的项目桶。精确匹配：桶 = cwd 路径
// 的转义（Session 创建时惰性建桶），不做向上探测——避免子目录任务被归并进
// 已存在的上级桶（用户实测 case03 任务被吸入项目根桶，2026-08-09 改为精确）。
func (s *Store) FindProject(cwd string) (*Project, error) {
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return nil, fmt.Errorf("session: getwd: %w", err)
		}
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("session: abs %s: %w", cwd, err)
	}
	return &Project{Path: abs, Dir: s.ProjectDir(abs)}, nil
}

// SessionInfo 是会话列表条目。
type SessionInfo struct {
	ID   string // 目录名 = 会话 id（时间戳-随机）
	Path string // 会话目录
}

// Sessions 列出项目桶下的会话（按目录名排序，时间戳前缀天然时间序）。
func (p *Project) Sessions() ([]SessionInfo, error) {
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", p.Dir, err)
	}
	var out []SessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, SessionInfo{ID: e.Name(), Path: filepath.Join(p.Dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Last 返回最新会话（排序最后一个）；无会话时 ok=false。
func (p *Project) Last() (SessionInfo, bool) {
	s, err := p.Sessions()
	if err != nil || len(s) == 0 {
		return SessionInfo{}, false
	}
	return s[len(s)-1], true
}
