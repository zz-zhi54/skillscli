// Package skills 从检出目录只读发现、验证和计算 Agent Skills 指纹。
package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/sumdb/dirhash"

	"github.com/zz-zhi54/skillscli/internal/config"
)

// Skill 是一个上游快照；Path 使用仓库内的 / 相对路径，Dir 为本地绝对路径。
// 调用方必须在 Git Checkout.Close 之前完成对 Dir 的使用。
type Skill struct {
	Name string
	Path string
	Dir  string
	Hash string
}

// Discover 只扫描 source.Paths 本身或其直接子目录，不递归发现 Skills。
// checkoutDir 可直接使用 git.Checkout.Dir；省略 Paths 时沿用配置默认值，显式空 Paths 则报错。
// 候选通过目录安全及 Frontmatter 校验后，先检查重名，再应用 Exclude 和计算指纹。
// 成功返回按名称排序的完整快照（可为空）；任何路径、候选或指纹错误均返回 nil，不返回部分结果。
// 配置发现目录缺失也是错误；同步允许上游目录消失时应使用 DiscoverSnapshot。
// 检出目录在调用期间须保持不变，不能并发清理或替换文件。
func Discover(checkoutDir string, source config.Source) ([]Skill, error) {
	return discover(checkoutDir, source, false)
}

// DiscoverSnapshot 用于同步目标集合：不存在的配置目录视为空范围，
// 因为 Git 不保留空目录，上游删除最后一个 Skill 时发现路径也可能消失。
// 仅放宽 resolveDirectory 的不存在错误，其余路径安全和验证规则不变；
// 候选读取或指纹计算期间的缺失仍是错误，不接受不完整快照。
func DiscoverSnapshot(checkoutDir string, source config.Source) ([]Skill, error) {
	return discover(checkoutDir, source, true)
}

// discover 共用严格发现和同步快照的流程，allowMissingPaths 仅控制配置发现目录缺失是否可忽略。
// 不修改 source，也不写入检出树；某个范围失败时丢弃已累积的候选，保持快照完整性。
func discover(checkoutDir string, source config.Source, allowMissingPaths bool) ([]Skill, error) {
	root, err := checkedRoot(checkoutDir)
	if err != nil {
		return nil, err
	}
	paths := source.Paths
	// nil 切片表示配置省略，才应用默认值；非 nil 的零长度切片表示显式空列表，应拒绝。
	if paths == nil {
		paths = []string{"skills"}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}
	excluded := make(map[string]bool, len(source.Exclude))
	for _, name := range source.Exclude {
		excluded[name] = true
	}
	// seen 按仓库相对路径去重重叠范围；byName 另按名称检查不同目录的身份冲突。
	seen := make(map[string]bool)
	byName := make(map[string]string)
	result := make([]Skill, 0)
	// 闭包共享本次发现的去重表和结果，单 Skill 路径与扫描子目录走相同候选处理流程。
	add := func(dir string) error {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			return nil
		}
		seen[rel] = true
		name, err := Validate(dir)
		if err != nil {
			return err
		}
		if previous, exists := byName[name]; exists {
			return fmt.Errorf("duplicate Skill %q in %q and %q", name, previous, rel)
		}
		byName[name] = rel
		// 排除只控制选中集合，不能隐藏无效 Skill 或重名冲突；因此在验证和重名检查之后应用。
		if excluded[name] {
			return nil
		}
		// Validate 已检查整个目录，无须重复遍历；Dir 在调用期间必须保持不变。
		hash, err := hashDir(dir)
		if err != nil {
			return err
		}
		result = append(result, Skill{Name: name, Path: rel, Dir: dir, Hash: hash})
		return nil
	}
	for _, configured := range paths {
		dir, err := resolveDirectory(root, configured)
		if allowMissingPaths && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("path %q: %w", configured, err)
		}
		found, err := hasSkillFile(dir)
		if err != nil {
			return nil, err
		}
		if found {
			if err := add(dir); err != nil {
				return nil, err
			}
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			child := filepath.Join(dir, entry.Name())
			// .git 是检出元数据，不是 Skill。其他链接即使指向仓库内也不支持。
			if entry.Name() == ".git" {
				continue
			}
			if err := safeType(child, entry.Type()); err != nil {
				return nil, err
			}
			if !entry.IsDir() {
				continue
			}
			found, err := hasSkillFile(child)
			if err != nil {
				return nil, err
			}
			if found {
				if err := add(child); err != nil {
					return nil, err
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// Hash 检查整棵目录树后计算 h1 指纹，包含所有普通文件的相对路径与原始内容。
// 它不校验 Frontmatter 或要求存在 SKILL.md，也可用于本地已安装目录和空目录。
// 不记录目录的绝对位置、修改时间、执行权限和空目录；调用期间目录必须保持不变。
// 任一安全检查或文件读取失败均返回空指纹及错误，不能据此将不完整目录当成未修改。
func Hash(dir string) (string, error) {
	dir, err := checkedRoot(dir)
	if err != nil {
		return "", err
	}
	if err := checkTree(dir); err != nil {
		return "", err
	}
	return hashDir(dir)
}

// hashDir 复用 dirhash.Hash1 的稳定目录摘要；调用前须已检查文件树且之后没有并发修改。
// Discover 的 Validate 和独立入口 Hash 分别完成前置安全检查，避免在发现流程重复遍历。
func hashDir(dir string) (string, error) {
	// 空前缀避免把安装位置或 Skill 目录名加入文件的相对路径。
	hash, err := dirhash.HashDir(dir, "", dirhash.Hash1)
	if err != nil {
		return "", fmt.Errorf("hash Skill directory %q: %w", dir, err)
	}
	return hash, nil
}

// checkedRoot 拒绝目录自身的链接，但规范化系统父路径（如 macOS 的 /var）。
// 传入的根目录是调用方的信任边界；仓库内的路径另由 resolveDirectory 检查。
func checkedRoot(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("directory must not be empty")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q must be a directory, not a symlink or special file", absolute)
	}
	return filepath.EvalSymlinks(absolute)
}

// resolveDirectory 将配置的 Git 相对路径映射到可信 root 内的实际目录。
// 除词法边界检查，还逐组件 Lstat 拒绝链接；单独清理路径或检查最终字符串不足以阻止链接逃逸。
func resolveDirectory(root, configured string) (string, error) {
	// Git 路径以 / 分隔；不接受反斜杠、驱动器或 UNC 等平台相关表示。
	// 先检查原始组件，不能让 Clean 吞掉 .. 后再判断安全性。
	if strings.TrimSpace(configured) == "" || strings.ContainsAny(configured, "\\\x00") ||
		path.IsAbs(configured) || filepath.IsAbs(configured) || filepath.VolumeName(configured) != "" ||
		(len(configured) >= 2 && configured[1] == ':') {
		return "", fmt.Errorf("must be a relative Git path using / separators")
	}
	for _, part := range strings.Split(configured, "/") {
		if part == ".." || part == ".git" {
			return "", fmt.Errorf("path traversal and Git metadata paths are not allowed")
		}
	}
	clean := path.Clean(configured)
	dir := filepath.Join(root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path is outside the checkout directory")
	}
	current := root
	for _, part := range strings.Split(clean, "/") {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%q must be a directory, not a symlink or special file", current)
		}
	}
	return dir, nil
}

// hasSkillFile 仅将缺失 SKILL.md 视为非候选；同名目录、链接或特殊文件是错误，不能静默跳过。
func hasSkillFile(dir string) (bool, error) {
	file := filepath.Join(dir, "SKILL.md")
	info, err := os.Lstat(file)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("Skill %q: SKILL.md must be a regular file, not a directory, symlink or special file", dir)
	}
	return true, nil
}

// safeType 只允许普通文件和目录，拒绝链接及 FIFO 等可能逃逸或阻塞读取的特殊对象。
func safeType(file string, mode fs.FileMode) error {
	if mode&os.ModeSymlink != 0 {
		return fmt.Errorf("%q: symlinks are not supported", file)
	}
	if !mode.IsDir() && !mode.IsRegular() {
		return fmt.Errorf("%q: special files are not supported", file)
	}
	return nil
}

// checkTree 在读取 Skill 内容前检查整棵树；不只检查 SKILL.md，以免资源文件绕过安全限制。
// 路径检查和后续读取不是原子操作，调用方须保证文件树不被并发替换。
func checkTree(dir string) error {
	// WalkDir 回调接收遍历错误时 entry 可能为空，必须先返回 err，再读取 entry 的类型。
	// 本回调返回的检查或遍历错误会终止遍历；不跟随链接，也不使用 SkipDir 忽略不安全子树。
	return filepath.WalkDir(dir, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := safeType(file, entry.Type()); err != nil {
			return err
		}
		return nil
	})
}
