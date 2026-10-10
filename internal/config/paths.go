package config

import (
	"os"
	"path/filepath"
)

// Paths 保存同一作用域内的配置文件和 Skill 安装目录的绝对路径。
// 它只描述位置，不代表对应文件或目录已经存在。
type Paths struct {
	Config string // 配置文件：<作用域根目录>/.agents/skillscli.yaml。
	Skills string // 安装目录：<作用域根目录>/.agents/skills。
}

// ResolvePaths 根据 global 选择作用域，并返回配置文件及安装目录的位置。
// global 为 false 时以当前工作目录作为项目根目录，不向上搜索 Git 根目录；
// global 为 true 时以用户主目录作为根目录，不先读取当前工作目录。
// 此函数只解析路径，不创建目录，也不读取配置。
func ResolvePaths(global bool) (Paths, error) {
	// var 声明带类型的变量：string 零值为 ""，error 零值为 nil。
	// 先声明再用 = 赋值，让两个分支共享同一组结果。
	var root string
	var err error
	if global {
		root, err = os.UserHomeDir()
	} else {
		root, err = os.Getwd()
	}
	if err != nil {
		// 无法取得所需作用域的根目录时直接返回原错误，不能悄悄切换到另一作用域。
		return Paths{}, err
	}
	// 统一转为绝对路径，避免后续操作因工作目录改变而把配置写到另一个位置。
	root, err = filepath.Abs(root)
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Config: filepath.Join(root, ".agents", "skillscli.yaml"),
		Skills: filepath.Join(root, ".agents", "skills"),
	}, nil
}
