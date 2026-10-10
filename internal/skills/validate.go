package skills

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Validate 先检查 dir 的完整文件树安全性，再读取普通文件 SKILL.md 并验证必要 Frontmatter 字段。
// 成功返回与目录名一致的 Skill 名称，失败返回空名称及错误；目录在校验和读取期间须保持不变。
// 可选/扩展字段交给 YAML 解析，不使用配置文件的未知字段拒绝策略。
// 不要求正文长度、标题或其他推荐但非必需的格式，也不改写源文件。
func Validate(dir string) (string, error) {
	dir, err := checkedRoot(dir)
	if err != nil {
		return "", err
	}
	if err := checkTree(dir); err != nil {
		return "", err
	}
	found, err := hasSkillFile(dir)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("Skill %q: missing SKILL.md", dir)
	}
	content, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return "", err
	}
	name, err := validateFrontmatter(content, filepath.Base(dir))
	if err != nil {
		return "", fmt.Errorf("Skill %q: SKILL.md: %w", dir, err)
	}
	return name, nil
}

// validateFrontmatter 从 UTF-8 原始内容提取单个 YAML Frontmatter，验证名称、描述及目录名一致性。
// BOM、开头空白和 CRLF 只在解析视图中处理，正文不参与格式校验，也不回写原始字节。
func validateFrontmatter(content []byte, directoryName string) (string, error) {
	if !utf8.Valid(content) {
		return "", fmt.Errorf("must contain valid UTF-8")
	}
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	lines := strings.Split(strings.TrimLeft(string(content), " \t\r\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", fmt.Errorf("missing YAML frontmatter opening delimiter ---")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		// YAML 块标量中的缩进 --- 是字段内容，不能当作 Frontmatter 结束。
		if strings.TrimRight(lines[i], " \t\r") == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", fmt.Errorf("missing YAML frontmatter closing delimiter ---")
	}
	var fields map[string]any
	decoder := yaml.NewDecoder(strings.NewReader(strings.Join(lines[1:end], "\n")))
	if err := decoder.Decode(&fields); err != nil && err != io.EOF {
		return "", fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	// 解码一次成功不代表输入已耗尽；再次 Decode 必须是 EOF，禁止尾随另一份 YAML 文档。
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return "", fmt.Errorf("invalid YAML frontmatter: %w", err)
		}
		return "", fmt.Errorf("frontmatter must contain a single YAML document")
	}
	// map[string]any 保留 YAML 值类型；comma-ok 类型断言要求真正的字符串，不将数字或 null 转成文本。
	name, ok := fields["name"].(string)
	if !ok || name == "" {
		return "", fmt.Errorf("name must be a non-empty string")
	}
	// 长度按 Unicode 码点而非 UTF-8 字节计算，和下面 range 逐 rune 的名称规则保持一致。
	if utf8.RuneCountInString(name) > 64 {
		return "", fmt.Errorf("name must be 1-64 characters")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return "", fmt.Errorf("name must not start/end with a hyphen or contain consecutive hyphens")
	}
	// 官方规范和参考校验器支持 Unicode 字母/数字，不额外限制为 ASCII。
	for _, r := range name {
		if r != '-' && ((!unicode.IsLetter(r) && !unicode.IsNumber(r)) || unicode.ToLower(r) != r) {
			return "", fmt.Errorf("name must contain only lowercase letters, numbers and hyphens")
		}
	}
	if name != directoryName {
		return "", fmt.Errorf("name %q must match directory name %q", name, directoryName)
	}
	description, ok := fields["description"].(string)
	if !ok || strings.TrimSpace(description) == "" {
		return "", fmt.Errorf("description must be a non-empty string")
	}
	if utf8.RuneCountInString(description) > 1024 {
		return "", fmt.Errorf("description must be 1-1024 characters")
	}
	return name, nil
}
