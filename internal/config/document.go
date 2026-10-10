package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document 同时保存业务模型和原始 YAML 节点树，两者职责不同：
// Config 方便业务代码读取和修改；node 保留结构体无法表示的注释、顺序和样式。
// 调用方修改 Config 后调用 Save，程序将新值合并回节点树，而不是直接重写结构体。
type Document struct {
	Config Config    // 导出字段供 sync 修改目标模型；不能代表已经提交的磁盘状态。
	node   yaml.Node // 小写字段仅本包可见，防止调用方直接破坏 YAML 表示树。
}

// Load 读取并校验一个配置文件，不创建文件或目录。
// 缺失文件和合法的空配置返回版本 1 的空模型；非空配置必须显式声明版本。
// 成功返回的 Document 可在修改 Config 后继续用于保存，以保留原文件的注释。
func Load(path string) (*Document, error) {
	// := 根据返回值推断 data 为 []byte、err 为 error；Go 用多返回值报告失败。
	// errors.Is 可识别包装后的错误，不能用错误文本判断配置是否缺失。
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// 首次使用时配置尚不存在是正常情况，不需要提前在磁盘上生成空文件。
		return &Document{Config: Config{Version: Version}}, nil
	}
	if err != nil {
		return nil, err
	}
	doc := &Document{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	// 严格解码到结构体，以拒绝拼错的字段名；否则未知字段可能被静默忽略。
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc.Config); err != nil {
		if !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		doc.Config.Version = Version
		// 空文件或只有注释的文件会让解码器返回 EOF，没有可保存的文档节点。
		// 因此手动建立“文档 -> 空映射”的结构，并把原注释放到映射的头部；
		// 这样首次保存时可以补上配置字段，而不会丢掉用户写在空文件中的说明。
		doc.node = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
			Kind: yaml.MappingNode, Tag: "!!map", HeadComment: strings.TrimSpace(string(data)),
		}}}
		return doc, nil
	}
	// YAML 允许用 --- 在一个文件中放多个文档，但 skillscli 只接受一个配置。
	// 第二次解码必须返回 EOF；即使第二份文档是空的，也不能静默忽略。
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		return nil, fmt.Errorf("config %s must contain a single YAML document", path)
	}
	// 再读取节点树是为了保存注释等表示信息，不是重复做业务校验。
	// 直接解码成 Config 无法保留这些信息；仅解码成节点又不能利用 KnownFields。
	if err := yaml.Unmarshal(data, &doc.node); err != nil {
		return nil, err
	}
	// DocumentNode 是外层容器，Content[0] 才是实际配置的根节点。
	root := doc.node.Content[0]
	if root.Tag == "!!null" && root.Value == "" {
		// 只有文档标记 --- 时，yaml.v3 表示为值为空的 null 节点。
		// 将它视为合法空配置；显式写出的 null 或 ~ 不在此范围内。
		root.Kind, root.Tag = yaml.MappingNode, "!!map"
	}
	if err := validateTypes(root); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if len(root.Content) == 0 {
		// 空映射 {} 不必声明版本；非空映射不走这里，仍由 Validate 检查版本。
		doc.Config.Version = Version
	}
	doc.Config.applyDefaults()
	if err := doc.Config.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return doc, nil
}

// Save 将当前 Config 安全写入目标路径，并尽量保留原 YAML 的表示方式。
// 流程为：校验模型 -> 生成期望节点 -> 合并旧节点 -> 在内存中编码 -> 原子写入。
// 编码器可能规范化空白和缩进，因此保留注释及样式不等于逐字节保留整个文件。
func (d *Document) Save(path string) error {
	data, updated, err := d.encode()
	if err != nil {
		return err
	}
	if err := WriteAtomic(path, data); err != nil {
		return err
	}
	d.node = *updated
	d.Config.applyDefaults()
	return nil
}

// RemoveSource 按下标同时删除内存来源和原 YAML 项，避免同 URL 来源错配注释。
// 此方法不写文件；调用方仍须通过 Save 或部署事务提交。
func (d *Document) RemoveSource(index int) error {
	if index < 0 || index >= len(d.Config.Sources) {
		return errors.New("source index out of range")
	}
	if len(d.node.Content) != 0 {
		if sources := mappingValue(d.node.Content[0], "sources"); sources != nil && index < len(sources.Content) {
			sources.Content = append(sources.Content[:index], sources.Content[index+1:]...)
		}
	}
	d.Config.Sources = append(d.Config.Sources[:index], d.Config.Sources[index+1:]...)
	return nil
}

// Bytes 编码当前模型但不写入文件，供部署事务记录预期提交内容。
func (d *Document) Bytes() ([]byte, error) {
	data, _, err := d.encode()
	return data, err
}

// encode 返回完整 YAML 字节和独立的新节点树，供 Bytes 预览或 Save 成功后采用。
// 将编码与落盘分开，使部署器可以先保存 config.after，再开始移动 Skill 目录。
func (d *Document) encode() ([]byte, *yaml.Node, error) {
	// 在创建目录或临时文件之前验证，非法配置不能破坏磁盘上的有效配置。
	if err := d.Config.Validate(); err != nil {
		return nil, nil, err
	}
	var desired yaml.Node
	if err := desired.Encode(d.Config); err != nil {
		return nil, nil, err
	}
	// 内存模型已经补充了默认值，但不应因此把原来省略的字段全部补写到文件。
	// 反过来，显式写出的默认值也可能带有注释，需要保留。
	preserveDefaults(&d.node, &desired)
	// 在旧节点的副本上合并；否则一旦写入失败，Document 会误记尚未落盘的新格式，
	// 下次重试就无法再基于原文件的完整注释和顺序进行更新。
	updated := cloneNode(&d.node)
	if updated.Kind == 0 {
		// 缺失文件返回的 Document 没有旧树，需要为新映射补上文档容器。
		updated = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{&desired}}
	} else {
		mergeNode(updated.Content[0], &desired)
	}
	// 先完整编码到内存，保证编码失败时还没有开始替换目标文件。
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(updated); err != nil {
		return nil, nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), updated, nil
}

// WriteAtomic 使用目标文件所在目录中的临时文件，避免直接截断原配置。
// 事务恢复可用它恢复原始 YAML 字节；调用方负责提供已验证的内容。
// 在支持原子重命名的文件系统上，读者看到的是完整旧文件或完整新文件，
// 而不是写到一半的 YAML。它不是 Skill 部署事务，也不提供并发写入协调；
// 此处同步了临时文件内容，但未同步父目录，不承诺掉电后的重命名持久性。
func WriteAtomic(path string, data []byte) error {
	// 新文件默认只允许当前用户读写；替换已有文件时保持原有权限。
	mode := os.FileMode(0600)
	// Lstat 检查目标本身，而不是跟随符号链接，防止把链接目标当作普通配置替换。
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("config target %s is not a regular file", path)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// 同目录临时文件与目标通常在同一文件系统，避免跨文件系统重命名失败。
	// CreateTemp 生成独立名称，不使用固定临时文件名覆盖其他操作的文件。
	file, err := os.CreateTemp(dir, ".skillscli-*.yaml")
	if err != nil {
		return err
	}
	// defer 按后注册先执行的顺序运行：先关闭，再尝试删除临时路径。
	// 成功重命名后旧临时路径已不存在；失败时仍会清理尚未发布的文件。
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	// Sync 将临时文件的数据交给文件系统同步，再检查 Close 的错误。
	// 必须在这些步骤全部成功后才发布新文件，否则旧配置应继续留在原路径。
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// 重命名是最后的发布步骤；之前的写入、同步或关闭失败都不会替换旧文件。
	return os.Rename(file.Name(), path)
}
