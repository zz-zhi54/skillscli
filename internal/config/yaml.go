package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// validateTypes 检查 YAML 节点中真实的字段类型，弥补结构体解码的隐式转换。
// yaml.v3 可以将数字或布尔标量解码到 Go string，但配置中的 source、ref、
// 路径和指纹必须确实是 YAML 字符串，不能因为转换成功就接受错误类型。
// 此处只检查已经出现的字段；必填性、默认值和业务含义由模型层处理。
func validateTypes(root *yaml.Node) error {
	if err := requireTag(root, "!!map", "config"); err != nil {
		return err
	}
	if version := mappingValue(root, "version"); version != nil {
		if err := requireTag(version, "!!int", "version"); err != nil {
			return err
		}
	}
	sources := mappingValue(root, "sources")
	if sources == nil {
		return nil
	}
	if err := requireTag(sources, "!!seq", "sources"); err != nil {
		return err
	}
	for i, source := range sources.Content {
		// 为嵌套字段建立可定位的名称，例如 sources[0].paths。
		prefix := fmt.Sprintf("sources[%d]", i)
		if err := requireTag(source, "!!map", prefix); err != nil {
			return err
		}
		for _, key := range []string{"source", "ref"} {
			if value := mappingValue(source, key); value != nil {
				if err := requireTag(value, "!!str", prefix+"."+key); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"paths", "exclude"} {
			if value := mappingValue(source, key); value != nil {
				// 不仅要求字段是列表，还要求列表中的每一项都是字符串。
				if err := requireTag(value, "!!seq", prefix+"."+key); err != nil {
					return err
				}
				for _, item := range value.Content {
					if err := requireTag(item, "!!str", prefix+"."+key); err != nil {
						return err
					}
				}
			}
		}
		lock := mappingValue(source, "lock")
		if lock == nil {
			continue
		}
		if err := requireTag(lock, "!!map", prefix+".lock"); err != nil {
			return err
		}
		for _, key := range []string{"source", "revision"} {
			if value := mappingValue(lock, key); value != nil {
				if err := requireTag(value, "!!str", prefix+".lock."+key); err != nil {
					return err
				}
			}
		}
		if skills := mappingValue(lock, "skills"); skills != nil {
			if err := requireTag(skills, "!!map", prefix+".lock.skills"); err != nil {
				return err
			}
			// 映射的 Content 交替存放键和值，所以遍历整个切片会同时检查
			// Skill 名称和指纹，避免数字名称被隐式转换成字符串。
			for _, item := range skills.Content {
				if err := requireTag(item, "!!str", prefix+".lock.skills"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// requireTag 检查节点的 YAML 类型标签，并在错误中带上字段名和原文件行号。
// Kind 表示节点形态，如标量或映射；Tag 进一步区分字符串、整数等数据类型。
func requireTag(node *yaml.Node, tag, field string) error {
	if node.Tag != tag {
		return fmt.Errorf("%s must be %s (line %d)", field, tag, node.Line)
	}
	return nil
}

// mappingValue 按字段名取得映射中的值节点，找不到时返回 nil。
// yaml.Node 的映射不是 Go map：Content 的布局为 [键, 值, 键, 值, ...]，
// 因此下标每次增加 2，才能跳到下一对键值，同时保留原有字段顺序。
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// cloneNode 复制节点及其 Content 子树，避免合并时修改原树中的共享子节点。
// 单纯执行 copy := *node 只会复制切片头，子节点指针仍指向旧节点；
// 递归复制 Content 后，Save 才能在失败时保留原来的节点内容。
func cloneNode(node *yaml.Node) *yaml.Node {
	// *node 在表达式中表示解引用；&copy 返回副本的地址。
	// Go 会保证返回的局部变量仍有效，但浅复制不会自动复制它引用的子树。
	copy := *node
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = cloneNode(child)
	}
	return &copy
}

// mergeNode 将 desired 的配置值合并到 current，尽量复用旧节点的表示信息。
// desired 决定哪些字段或列表项存在，current 提供注释、字段顺序和标量样式。
// 因此删除字段会真正删除，不会为了保留旧注释而恢复已移除的业务数据。
func mergeNode(current, desired *yaml.Node) {
	if current.Kind != desired.Kind || current.Tag != desired.Tag {
		// 类型改变后不能继续沿用旧内容或旧样式，直接替换节点结构；
		// 但保留该位置的头部、行内和尾部注释，减少不必要的信息损失。
		replacement := *cloneNode(desired)
		replacement.HeadComment = current.HeadComment
		replacement.LineComment = current.LineComment
		replacement.FootComment = current.FootComment
		*current = replacement
		return
	}
	switch current.Kind {
	case yaml.MappingNode:
		var content []*yaml.Node
		seen := make(map[string]bool)
		// 先按旧映射的顺序处理仍然存在的字段，保留键节点及其注释。
		// desired 中已经没有的字段不加入新内容，相当于执行删除。
		for i := 0; i < len(current.Content); i += 2 {
			key, value := current.Content[i], current.Content[i+1]
			if next := mappingValue(desired, key.Value); next != nil {
				mergeNode(value, next)
				content = append(content, key, value)
				seen[key.Value] = true
			}
		}
		// 再按期望节点的顺序追加新字段；已有字段不重复添加。
		// 这样结构体编码产生的字段顺序不会打乱用户原来排列的字段。
		for i := 0; i < len(desired.Content); i += 2 {
			if !seen[desired.Content[i].Value] {
				content = append(content, cloneNode(desired.Content[i]), cloneNode(desired.Content[i+1]))
			}
		}
		current.Content = content
	case yaml.SequenceNode:
		var content []*yaml.Node
		used := make(map[int]bool)
		// 列表顺序由 desired 决定，但复用旧项时按身份匹配，而不是按下标匹配。
		// 例如来源列表重排后，仓库说明应跟随对应来源，而不是留在原来的第几项。
		for _, next := range desired.Content {
			match := -1
			for i, old := range current.Content {
				if !used[i] && nodeIdentity(old) == nodeIdentity(next) {
					match = i
					break
				}
			}
			if match >= 0 {
				// 每个旧项最多复用一次，避免重复内容造成多个新项共享一个节点。
				used[match] = true
				old := current.Content[match]
				mergeNode(old, next)
				content = append(content, old)
			} else {
				content = append(content, cloneNode(next))
			}
		}
		current.Content = content
	default:
		// 同类型的标量只更新值，保留原来的引号、字面量样式和注释。
		current.Value = desired.Value
	}
}

// nodeIdentity 为当前配置中的列表项提供匹配依据，不是通用的 YAML 身份规则。
// 来源项使用 source 地址；paths、exclude 等标量列表使用标量值。
// 当来源地址本身改变时会被视为新项，不保证继承旧来源项的注释。
func nodeIdentity(node *yaml.Node) string {
	if source := mappingValue(node, "source"); source != nil {
		return source.Value
	}
	return node.Value
}

// preserveDefaults 修正普通结构体编码与原始 YAML 在默认值表示上的差异。
// 读取时补齐的默认 paths 不应自动变成新字段；原来显式写出的空 ref 或
// 空 exclude 可能带有说明，也不应仅因 omitempty 就在保存时消失。
func preserveDefaults(original, desired *yaml.Node) {
	if len(original.Content) == 0 {
		// 新配置没有可参考的原始写法，直接使用模型的编码结果。
		return
	}
	oldSources := mappingValue(original.Content[0], "sources")
	newSources := mappingValue(desired, "sources")
	if newSources == nil {
		return
	}
	if oldSources == nil {
		// 原来省略了 sources 且现在仍然没有来源，无需强行补上空列表。
		if len(newSources.Content) == 0 {
			deleteMappingKey(desired, "sources")
		}
		return
	}
	used := make(map[int]bool)
	for _, next := range newSources.Content {
		for i, old := range oldSources.Content {
			if used[i] || nodeIdentity(old) != nodeIdentity(next) {
				continue
			}
			used[i] = true
			paths := mappingValue(next, "paths")
			// 只有旧字段不存在且新值恰好是默认路径时才继续省略。
			// 用户指定了其他路径时必须写入，不能把真实修改误当成默认值。
			if mappingValue(old, "paths") == nil && paths != nil && len(paths.Content) == 1 && paths.Content[0].Value == "skills" {
				deleteMappingKey(next, "paths")
			}
			for _, key := range []string{"ref", "exclude"} {
				value := mappingValue(old, key)
				// 仅恢复旧配置中的空值节点；非空字段被调用方清空时仍应删除。
				// 追加到 desired 后，mergeNode 会按旧字段顺序放回，保留其注释。
				if value != nil && value.Value == "" && len(value.Content) == 0 && mappingValue(next, key) == nil {
					next.Content = append(next.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, cloneNode(value))
				}
			}
			break
		}
	}
}

// deleteMappingKey 从映射中同时移除键节点和值节点，其他字段的顺序不变。
// 调用方传入的是已知映射；每次删掉两个元素，保持 Content 的键值配对结构。
func deleteMappingKey(node *yaml.Node, key string) {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}
