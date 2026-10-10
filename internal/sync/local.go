package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zz-zhi54/skillscli/internal/skills"
)

type localSkill struct {
	state LocalState
	hash  string
	issue string
}

// installationPath 把 Skill 名称限制为单个安全路径组件，再拼接安装目标。
// lock 模型仅校验名称非空，不能直接把其键当作可信路径；跨平台设备名另由 portableName 检查。
func installationPath(root, name string) (string, error) {
	if name == "" || name == "." || name == ".." || name == ".git" ||
		strings.ContainsAny(name, "/\\\x00:") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("invalid installed Skill name %q: must be a single safe path component", name)
	}
	return filepath.Join(root, name), nil
}

// installationRoot 只读地校验并规范化安装根，父路径属于调用方的信任边界。
// 允许可信的系统父目录链接（如 /var），但根目录本身不能是链接或文件。
// 根目录不存在时不创建它。
func installationRoot(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("installation directory must not be empty")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return root, nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("installation root %q must be a directory, not a symlink or special file", root)
	}
	return filepath.EvalSymlinks(root)
}

// inspectLocal 用 Lstat 先区分整目录缺失与存在对象，再只对已管理目录计算安全指纹。
// 不验证 SKILL.md 的语义：目录里标记文件缺失仍应归入内容修改，而非整目录 Missing。
func inspectLocal(dir, lockedHash string, managed bool) (localSkill, error) {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return localSkill{state: Missing}, nil
	}
	if err != nil {
		return localSkill{}, err
	}
	// 未管理对象不读取内容，不论类型或指纹都不可接管。
	if !managed {
		return localSkill{state: Unmanaged}, nil
	}
	if !info.IsDir() {
		return localSkill{state: Unsafe, issue: "managed path is not a directory"}, nil
	}
	hash, err := skills.Hash(dir)
	if err != nil {
		// 无法安全得到指纹的树不能证明可覆盖；force 也不绕过路径安全检查。
		return localSkill{state: Unsafe, issue: err.Error()}, nil
	}
	state := Unmodified
	if hash != lockedHash {
		state = Modified
	}
	return localSkill{state: state, hash: hash}, nil
}
