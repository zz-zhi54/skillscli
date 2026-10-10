// Package config 负责作用域路径、配置校验及保留 YAML 表示信息的读写。
// 同包文件共享 Config、Document 和未导出的辅助函数，不需要互相 import。
package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Version 是当前程序支持的配置格式版本，不是 skillscli 的发布版本。
// 读取和写入都检查它，避免将不认识的配置格式按当前规则处理。
const Version = 1

// Config 是用于业务操作的配置模型。YAML 标签规定文件中的字段名称；
// 注释、字段顺序和引号样式不存放在结构体中，而由 Document 的节点树保存。
type Config struct {
	Version int      `yaml:"version"` // 格式版本；非空配置必须显式声明为 1。
	Sources []Source `yaml:"sources"` // 一个来源可以包含多个 Skill。
}

// Source 描述用户希望从哪里、哪些目录和哪个版本取得 Skills。
// 带 omitempty 的字段在普通结构体编码时会省略零值；Document.Save
// 会进一步结合原始节点，保留省略的默认值以及显式默认值上的注释。
type Source struct {
	Source  string   `yaml:"source"`            // 必填来源地址；不限制 Git 所支持的地址协议。
	Paths   []string `yaml:"paths,omitempty"`   // 仓库内的发现路径；省略时使用 ["skills"]。
	Ref     string   `yaml:"ref,omitempty"`     // 分支、标签或完整提交 SHA；空值表示远程默认分支。
	Exclude []string `yaml:"exclude,omitempty"` // 不希望安装的 Skill 名称。
	Lock    *Lock    `yaml:"lock,omitempty"`    // 实际安装状态；nil 表示尚无锁定记录。
}

// Lock 记录实际安装结果，与 Source 中的期望配置区分开。
// 例如用户修改了来源地址，但尚未完成下一次安装时，两个 source 可以不同；
// 因此校验不要求 Lock.Source 必须等于 Source.Source。
type Lock struct {
	Source   string            `yaml:"source"`   // 实际安装使用的来源。
	Revision string            `yaml:"revision"` // 完整 Git 提交 SHA，不接受缩写。
	Skills   map[string]string `yaml:"skills"`   // Skill 名称到目录指纹的映射；非 nil，可为空。
}

// applyDefaults 将省略的 paths 转为业务代码可直接使用的默认值。
// nil 与显式空切片有不同含义：nil 表示未指定，[] 表示指定了空列表，后者非法。
// 这里只设置内存默认值；保存时是否补写 paths 由原始 YAML 的写法决定。
func (c *Config) applyDefaults() {
	// *Config 是指针接收者，修改的是调用方的配置；按下标访问真实切片元素。
	// 若改成 for _, source := range，source 会是结构体副本，字段赋值不会更新原项。
	for i := range c.Sources {
		if c.Sources[i].Paths == nil {
			c.Sources[i].Paths = []string{"skills"}
		}
	}
}

// Validate 检查配置模型的业务约束，不修改配置，也不访问 Git 或安装目录。
// YAML 的字段类型由 validateTypes 检查；仓库路径安全、Skill 发现和同名冲突
// 由 skills 和 sync 包负责，不能只因 YAML 合法就认为文件操作安全。
// 错误中的下标用于定位具体来源。
func (c Config) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("unsupported config version %d (expected %d)", c.Version, Version)
	}
	for i, source := range c.Sources {
		// 只用 TrimSpace 判断是否为空，不改写用户的原始来源或路径。
		if strings.TrimSpace(source.Source) == "" {
			return fmt.Errorf("sources[%d].source is required", i)
		}
		if source.Paths != nil && len(source.Paths) == 0 {
			return fmt.Errorf("sources[%d].paths must not be empty", i)
		}
		for _, path := range source.Paths {
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("sources[%d].paths contains an empty path", i)
			}
		}
		for _, name := range source.Exclude {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("sources[%d].exclude contains an empty name", i)
			}
		}
		// lock 整体可省略；但一旦存在，就必须是完整、可解释的安装记录。
		if source.Lock == nil {
			continue
		}
		if strings.TrimSpace(source.Lock.Source) == "" {
			return fmt.Errorf("sources[%d].lock.source is required", i)
		}
		revision := source.Lock.Revision
		// Git 的 SHA-1 提交标识为 40 位十六进制，SHA-256 为 64 位。
		// 先检查完整长度，再检查字符；这里并不验证该提交是否在远程仓库中存在。
		if len(revision) != 40 && len(revision) != 64 {
			return fmt.Errorf("sources[%d].lock.revision must be a full commit SHA", i)
		}
		if _, err := hex.DecodeString(revision); err != nil {
			return fmt.Errorf("sources[%d].lock.revision: %w", i, err)
		}
		if source.Lock.Skills == nil {
			return fmt.Errorf("sources[%d].lock.skills is required", i)
		}
		for name, hash := range source.Lock.Skills {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("sources[%d].lock.skills contains an empty name", i)
			}
			// dirhash 的 h1 指纹由前缀和 Base64 编码的 32 字节 SHA-256 摘要组成。
			// 重新编码并比较，既检查 h1: 前缀，也拒绝换行等非规范写法；
			// 这里只校验格式，实际计算目录指纹留到 Skill 发现阶段。
			digest, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(hash, "h1:"))
			if err != nil || len(digest) != 32 || hash != "h1:"+base64.StdEncoding.EncodeToString(digest) {
				return fmt.Errorf("sources[%d].lock.skills[%q] must be an h1 SHA-256 fingerprint", i, name)
			}
		}
	}
	return nil
}
