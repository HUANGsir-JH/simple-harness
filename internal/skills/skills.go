// Package skills 实现全局技能（SKILL.md）的发现、解析与渲染（ADR-044）。
//
// 纯逻辑、只依赖标准库 + yaml（对齐 agentsmd 的叶子包定位）：不依赖
// session / middleware / tools，由 impl.SkillsCatalogMiddleware（目录注入）与
// tools.SkillTool（按需加载）调用。技能根 = $HARNESS_HOME/skills（默认
// ~/.harness/skills，路径由 app 层解析后注入，防环约定同 AGENTS.md）。
//
// 发现规则（deepseek-harness / opencode / codex 公共子集）：技能根下每个
// 子目录的 SKILL.md 是一个技能（目录包，可带 references/、scripts/、assets/
// 辅助资源，相对路径以该目录为基准），根下每个 *.md 文件是平铺技能；
// YAML frontmatter 必填 name（kebab-case）与 description，可选 whenToUse；
// 同名冲突目录包优先；无效/读失败文件跳过（非致命），根不存在视为空目录。
// 加载预算 DefaultMaxBytes（对齐 AGENTS.md 200KB）；技能正文可信本地内容，
// 渲染时仅 name 属性转义。
package skills

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultMaxBytes 是单个技能正文的加载预算（对齐 agentsmd 的 200KB）。
	DefaultMaxBytes = 200 * 1024
	// CatalogDescriptionMax 是目录中 description 的展示上限（截断加省略号，
	// 防目录段撑爆系统提示）。
	CatalogDescriptionMax = 200
	// SkillFile 是目录包形态的指令文件名。
	SkillFile = "SKILL.md"
)

// skillNameRe 是技能名语法（kebab-case，deepseek-harness 同款）。
var skillNameRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Skill 是一个已发现的技能（Load 后 Content 为正文）。
type Skill struct {
	// Name 是 kebab-case 技能名（模型可见，发现与加载唯一标识）。
	Name string
	// Description 是短路由说明（目录展示）。
	Description string
	// WhenToUse 是可选的额外适用场景说明（目录展示，可空）。
	WhenToUse string
	// Path 是 SKILL.md（目录包）或 *.md（平铺）的绝对路径。
	Path string
	// Dir 是资源基目录：目录包 = SKILL.md 所在目录；平铺 = 技能根。
	// 技能内相对路径（references/、scripts/、assets/）以它为基准。
	Dir string
	// Content 是技能正文（frontmatter 之后，TrimSpace；Load 后填充）。
	Content string
}

// IsSkillName 判断 name 是否符合技能名语法（kebab-case）。
func IsSkillName(name string) bool {
	return skillNameRe.MatchString(name)
}

// Discover 扫描技能根（root = ~/.harness/skills），返回按名排序的技能摘要
// （Content 不加载）。root 不存在/不可读返回 nil（非致命——技能目录不存在
// 就是没有技能，绝不终止回合）；单文件/单目录的读失败与格式非法跳过。
// 同名冲突（目录包 foo/SKILL.md 与平铺 foo.md）目录包优先。
func Discover(root string) []Skill {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	// 先收集全部候选（目录包与平铺分开），再按名合并去重（目录包优先）。
	byDir := map[string]Skill{}
	byFlat := map[string]Skill{}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(root, name)
		if e.IsDir() {
			p := filepath.Join(full, SkillFile)
			if s, ok := parseMeta(p, full); ok {
				byDir[s.Name] = s
			}
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue // 非 md 文件跳过（如 README.md 也跳过？）
		}
		if s, ok := parseMeta(full, root); ok {
			byFlat[s.Name] = s
		}
	}
	merged := make([]Skill, 0, len(byDir)+len(byFlat))
	for _, s := range byDir {
		merged = append(merged, s)
	}
	for name, s := range byFlat {
		if _, ok := byDir[name]; ok {
			continue // 目录包优先
		}
		merged = append(merged, s)
	}
	sortSkills(merged)
	return merged
}

// Load 从磁盘现读一个技能文件（工具调用时用，保证新鲜度）：解析 + 校验 +
// 正文预算截断。文件缺失/不可读/格式非法返回错误（调用方回填模型）。
func Load(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	meta, body, err := parseFrontmatter(data)
	if err != nil {
		return Skill{}, err
	}
	if !IsSkillName(meta.Name) {
		return Skill{}, &invalidError{path: path, reason: "name 非法（须 kebab-case）"}
	}
	if meta.Description == "" {
		return Skill{}, &invalidError{path: path, reason: "缺少 description"}
	}
	return Skill{
		Name:        meta.Name,
		Description: meta.Description,
		WhenToUse:   meta.WhenToUse,
		Path:        path,
		Dir:         filepath.Dir(path),
		Content:     truncateUTF8(strings.TrimSpace(body), DefaultMaxBytes),
	}, nil
}

// RenderContent 渲染技能为模型可见的 <skill_content> 块（工具结果与目录
// 使用引导共用同一形状）。name 走属性转义；正文为可信本地内容原样嵌入。
func RenderContent(s Skill) string {
	var sb strings.Builder
	sb.WriteString(`<skill_content name="`)
	sb.WriteString(escapeAttr(s.Name))
	sb.WriteString(`">`)
	sb.WriteString("\n<skill_resources>\n")
	sb.WriteString("本技能的资源基目录：" + s.Dir + "\n")
	sb.WriteString("技能内相对路径（references/、scripts/、assets/ 等）以该目录为基准；仅按需加载所需资源。\n")
	sb.WriteString("</skill_resources>\n\n")
	sb.WriteString("<skill_instructions>\n")
	sb.WriteString(s.Content)
	sb.WriteString("\n</skill_instructions>\n")
	sb.WriteString("</skill_content>")
	return sb.String()
}

// CatalogLine 渲染目录行（name: description[（适用：whenToUse）]，均截断）。
func CatalogLine(s Skill) string {
	desc := truncateTo(s.Description, CatalogDescriptionMax)
	line := "- " + s.Name + ": " + desc
	if s.WhenToUse != "" {
		line += "（适用：" + truncateTo(s.WhenToUse, CatalogDescriptionMax) + "）"
	}
	return line
}

// --- 内部实现 -------------------------------------------------------------

// frontmatter 是 SKILL.md 的 YAML 元数据（name/description 必填，whenToUse 可选）。
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	WhenToUse   string `yaml:"whenToUse"`
}

// invalidError 是格式非法错误（Load 回填模型用）。
type invalidError struct {
	path   string
	reason string
}

func (e *invalidError) Error() string {
	if e.path != "" {
		return "技能文件 " + e.path + " 无效: " + e.reason
	}
	return "技能无效: " + e.reason
}

// parseMeta 读取 path 的 frontmatter 元数据（不加载正文）；失败 ok=false。
// 发现阶段用：无效文件静默跳过（非致命）。
func parseMeta(path, dir string) (Skill, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false
	}
	meta, _, err := parseFrontmatter(data)
	if err != nil {
		return Skill{}, false
	}
	if !IsSkillName(meta.Name) || meta.Description == "" {
		return Skill{}, false
	}
	return Skill{
		Name:        meta.Name,
		Description: meta.Description,
		WhenToUse:   meta.WhenToUse,
		Path:        path,
		Dir:         dir,
	}, true
}

// parseFrontmatter 解析 YAML frontmatter（--- 块），返回元数据与正文。
// 无 frontmatter 或 YAML 解析失败返回错误。
func parseFrontmatter(data []byte) (frontmatter, string, error) {
	text := string(data)
	firstEnd := strings.IndexByte(text, '\n')
	if firstEnd < 0 {
		firstEnd = len(text)
	}
	if strings.TrimSpace(text[:firstEnd]) != "---" {
		return frontmatter{}, "", &invalidError{reason: "缺少 --- frontmatter"}
	}
	// 找第二个 --- 行（正文起点）。
	rest := text[firstEnd+1:]
	closing := findClosingFrontmatter(rest)
	if closing < 0 {
		return frontmatter{}, "", &invalidError{reason: "frontmatter 未闭合"}
	}
	var meta frontmatter
	if err := yaml.Unmarshal([]byte(rest[:closing]), &meta); err != nil {
		return frontmatter{}, "", &invalidError{reason: "frontmatter YAML 解析失败: " + err.Error()}
	}
	return meta, text[firstEnd+1+closing:], nil
}

// findClosingFrontmatter 返回正文起点偏移（第二个 --- 行的行尾）；未闭合 -1。
func findClosingFrontmatter(rest string) int {
	pos := 0
	for {
		end := strings.IndexByte(rest[pos:], '\n')
		var line string
		if end < 0 {
			line = rest[pos:]
		} else {
			line = rest[pos : pos+end]
		}
		if strings.TrimSpace(line) == "---" {
			if end < 0 {
				return len(rest)
			}
			return pos + end + 1
		}
		if end < 0 {
			return -1
		}
		pos += end + 1
	}
}

// sortSkills 按名排序（目录展示顺序稳定）。
func sortSkills(skills []Skill) {
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
}

// truncateTo 截断到最多 max 字节（超长补省略号）。
func truncateTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return truncateUTF8(s, max-3) + "..."
}

// escapeAttr 转义 XML 属性值中的 & " <。
func escapeAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;").Replace(s)
}

// truncateUTF8 截断 s 至最多 max 字节，回退到 UTF-8 边界（不切断多字节 rune）。
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
