package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill 写一个技能文件（目录包形态：<root>/<name>/SKILL.md）。
func writeSkill(t *testing.T, root, name, description, whenToUse, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, SkillFile)
	fm := "---\nname: " + name + "\ndescription: \"" + description + "\"\n"
	if whenToUse != "" {
		fm += "whenToUse: \"" + whenToUse + "\"\n"
	}
	fm += "---\n"
	if err := os.WriteFile(p, []byte(fm+body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// writeFlatSkill 写一个平铺技能文件（<root>/<name>.md）。
func writeFlatSkill(t *testing.T, root, name, description, body string) string {
	t.Helper()
	p := filepath.Join(root, name+".md")
	content := "---\nname: " + name + "\ndescription: \"" + description + "\"\n---\n" + body
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func names(skills []Skill) []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Name)
	}
	return out
}

func TestDiscoverDirBundle(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", "做演示用", "", "## 步骤\n1. 执行\n")
	writeSkill(t, root, "alpha", "第一个", "排序时", "body alpha")

	got := Discover(root)
	if len(got) != 2 {
		t.Fatalf("Discover = %d 个技能，want 2", len(got))
	}
	// 按名排序。
	if got[0].Name != "alpha" || got[1].Name != "demo-skill" {
		t.Errorf("排序错误: %v", names(got))
	}
	// 元数据完整、Content 不加载。
	s := got[1]
	if s.Name != "demo-skill" || s.Description != "做演示用" || s.WhenToUse != "" {
		t.Errorf("元数据错误: %+v", s)
	}
	if s.Content != "" {
		t.Errorf("Discover 不应加载正文，got %q", s.Content)
	}
	if !filepath.IsAbs(s.Path) || filepath.Base(s.Path) != SkillFile {
		t.Errorf("Path 应为 SKILL.md 绝对路径: %q", s.Path)
	}
	if filepath.Base(s.Dir) != "demo-skill" {
		t.Errorf("Dir 应为技能目录: %q", s.Dir)
	}
	if got[0].WhenToUse != "排序时" {
		t.Errorf("whenToUse 应保留: %q", got[0].WhenToUse)
	}
}

func TestDiscoverFlatFile(t *testing.T) {
	root := t.TempDir()
	writeFlatSkill(t, root, "flat-skill", "平铺技能", "body flat")

	got := Discover(root)
	if len(got) != 1 {
		t.Fatalf("Discover = %d 个技能，want 1", len(got))
	}
	s := got[0]
	if s.Name != "flat-skill" || s.Description != "平铺技能" {
		t.Errorf("平铺元数据错误: %+v", s)
	}
	if !strings.HasSuffix(s.Path, "flat-skill.md") {
		t.Errorf("Path 应为平铺文件: %q", s.Path)
	}
	if filepath.Clean(s.Dir) != filepath.Clean(root) {
		t.Errorf("平铺 Dir 应为技能根: %q", s.Dir)
	}
}

func TestDiscoverDirBundleBeatsFlat(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "same", "目录包", "", "dir body")
	writeFlatSkill(t, root, "same", "平铺", "flat body")

	got := Discover(root)
	if len(got) != 1 {
		t.Fatalf("Discover = %d 个技能，want 1（目录包优先）", len(got))
	}
	if got[0].Description != "目录包" {
		t.Errorf("目录包应优先: %+v", got[0])
	}
}

func TestDiscoverSkipsInvalid(t *testing.T) {
	root := t.TempDir()
	// 合法技能（基线）。
	writeSkill(t, root, "good", "好的", "", "body")
	// 缺 SKILL.md 的目录。
	if err := os.MkdirAll(filepath.Join(root, "no-file"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 无 frontmatter 的 md。
	if err := os.WriteFile(filepath.Join(root, "no-fm.md"), []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	// name 非法（大写/下划线）。
	writeSkill(t, root, "Bad_Name", "非法名", "", "body")
	// description 空。
	dir := filepath.Join(root, "no-desc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte("---\nname: no-desc\ndescription: \"\"\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 非 md 文件。
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Discover(root)
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("应只剩 good: %v", names(got))
	}
}

func TestDiscoverEmptyOrMissingRoot(t *testing.T) {
	if got := Discover(filepath.Join(t.TempDir(), "nonexistent")); len(got) != 0 {
		t.Errorf("不存在的根应返回空: %v", got)
	}
	if got := Discover(t.TempDir()); len(got) != 0 {
		t.Errorf("空根应返回空: %v", got)
	}
}

func TestDiscoverReadFailureNonFatal(t *testing.T) {
	// 根路径是文件而非目录：ReadDir 失败 → 空（非致命）。
	root := t.TempDir()
	file := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Discover(file); len(got) != 0 {
		t.Errorf("读失败应返回空: %v", got)
	}
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	p := writeSkill(t, root, "demo-skill", "演示", "", "## 正文\n步骤 1\n")

	s, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Name != "demo-skill" || s.Description != "演示" {
		t.Errorf("元数据错误: %+v", s)
	}
	if s.Content != "## 正文\n步骤 1" {
		t.Errorf("正文应保留（TrimSpace 去首尾空白）: %q", s.Content)
	}
	if s.Dir != filepath.Dir(p) {
		t.Errorf("Dir 错误: %q", s.Dir)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.md")); err == nil {
		t.Error("缺失文件应报错")
	}
}

func TestLoadInvalid(t *testing.T) {
	root := t.TempDir()
	// 非法 name。
	badPath := filepath.Join(root, "bad", SkillFile)
	if err := os.MkdirAll(filepath.Dir(badPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte("---\nname: Bad_Name\ndescription: x\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(badPath); err == nil {
		t.Error("非法 name 应报错")
	}
	// 缺 description。
	noDesc := filepath.Join(root, "no-desc", SkillFile)
	if err := os.MkdirAll(filepath.Dir(noDesc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noDesc, []byte("---\nname: ok\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(noDesc); err == nil {
		t.Error("缺 description 应报错")
	}
	// 无 frontmatter。
	plain := filepath.Join(root, "plain.md")
	if err := os.WriteFile(plain, []byte("no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(plain); err == nil {
		t.Error("无 frontmatter 应报错")
	}
}

func TestLoadTruncatesToBudget(t *testing.T) {
	root := t.TempDir()
	// 正文 300KB（远超 200KB 预算）。
	body := strings.Repeat("a", 300*1024)
	p := writeSkill(t, root, "big", "大技能", "", body)
	s, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Content) != DefaultMaxBytes {
		t.Errorf("应截断到 %d 字节，got %d", DefaultMaxBytes, len(s.Content))
	}
}

func TestLoadTruncateUTF8Boundary(t *testing.T) {
	root := t.TempDir()
	// 多字节 rune 恰好跨预算边界：199KB 'a' + 半个中文边界。构造一个
	// 预算内再补多字节内容：把预算边界切在 rune 中间。
	prefix := strings.Repeat("a", DefaultMaxBytes-1) // 预算-1 字节
	body := prefix + "中"                            // +3 字节 → 超预算 2 字节，边界在"中"中间
	p := writeSkill(t, root, "utf8", "多字节", "", body)
	s, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Content) != DefaultMaxBytes-1 {
		t.Errorf("应在 UTF-8 边界截断: len=%d", len(s.Content))
	}
	if !strings.HasSuffix(s.Content, strings.Repeat("a", 1)) {
		t.Errorf("截断不应留下半个 rune")
	}
}

func TestIsSkillName(t *testing.T) {
	valid := []string{"skill", "demo-skill", "a", "a-b-c", "skill1"}
	for _, name := range valid {
		if !IsSkillName(name) {
			t.Errorf("IsSkillName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "Bad", "bad_name", "bad name", "a.b", "-lead", "trail-", "a--b", "a-", "中"}
	for _, name := range invalid {
		if IsSkillName(name) {
			t.Errorf("IsSkillName(%q) = true, want false", name)
		}
	}
}

func TestRenderContent(t *testing.T) {
	s := Skill{Name: "demo", Dir: `C:\skills\demo`, Content: "步骤 1\n步骤 2"}
	got := RenderContent(s)
	want := `<skill_content name="demo">
<skill_resources>
本技能的资源基目录：C:\skills\demo
技能内相对路径（references/、scripts/、assets/ 等）以该目录为基准；仅按需加载所需资源。
</skill_resources>

<skill_instructions>
步骤 1
步骤 2
</skill_instructions>
</skill_content>`
	if got != want {
		t.Errorf("RenderContent 不符:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderContentEscapesName(t *testing.T) {
	// name 语法校验保证不会出现引号/尖括号，但渲染仍转义（防御）。
	s := Skill{Name: `a"<&b`, Content: "body"}
	got := RenderContent(s)
	if !strings.Contains(got, `name="a&quot;&lt;&amp;b"`) {
		t.Errorf("name 应转义: %s", got)
	}
}

func TestCatalogLine(t *testing.T) {
	s := Skill{Name: "demo", Description: "短描述", WhenToUse: "做演示"}
	if got, want := CatalogLine(s), "- demo: 短描述（适用：做演示）"; got != want {
		t.Errorf("CatalogLine = %q, want %q", got, want)
	}
	// 长描述截断。
	long := Skill{Name: "long", Description: strings.Repeat("长", 300)}
	got := CatalogLine(long)
	if len(got) >= len(long.Description) || !strings.HasSuffix(got, "...") {
		t.Errorf("长描述应截断: len=%d", len(got))
	}
	// 无 whenToUse。
	plain := Skill{Name: "p", Description: "x"}
	if got := CatalogLine(plain); got != "- p: x" {
		t.Errorf("CatalogLine = %q", got)
	}
}

func TestDiscoverCRLF(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "crlf-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\r\nname: crlf-skill\r\ndescription: \"CRLF 行尾\"\r\n---\r\nbody\r\n"
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Discover(root)
	if len(got) != 1 || got[0].Name != "crlf-skill" {
		t.Errorf("CRLF frontmatter 应可解析: %v", names(got))
	}
}
