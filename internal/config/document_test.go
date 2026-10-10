package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture 为每个测试生成独立配置文件，避免不同用例之间共享磁盘状态。
// t.Helper 让辅助函数失败时指向调用它的测试位置；t.TempDir 负责测试结束后的清理。
func fixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skillscli.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fileText 读取保存后的实际文件，供测试检查注释、顺序或失败后的原始内容。
// 不能仅检查内存中的 Config：模型正确并不代表磁盘内容和表示信息也正确。
func fileText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// fingerprint 生成格式有效且可重复的测试指纹，用于验证 lock 的格式和读写。
// 固定的 32 字节只是测试摘要，不代表真实目录内容；这里不提前实现目录指纹计算。
func fingerprint() string {
	return "h1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
}

func TestSameURLSourcesPreserveIndividualDefaults(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		first := "  - source: repo # implicit\n"
		second := "  - source: repo # explicit\n    paths: [skills] # preserve paths\n    ref: '' # preserve ref\n"
		if reverse {
			first, second = second, first
		}
		original := "version: 1\nsources:\n" + first + second
		path := fixture(t, original)
		doc, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := doc.Save(path); err != nil {
			t.Fatal(err)
		}
		if got := fileText(t, path); got != original {
			t.Fatalf("same URL defaults/comments changed:\n%s", got)
		}
		if err := doc.RemoveSource(0); err != nil {
			t.Fatal(err)
		}
		if err := doc.Save(path); err != nil {
			t.Fatal(err)
		}
		if got := fileText(t, path); got != "version: 1\nsources:\n"+second {
			t.Fatalf("wrong source representation survived removal:\n%s", got)
		}
	}
}

// TestSavePreservesExplicitDefaults 验证显式空 ref、空 exclude 及其行内注释不会被删除。
// 这些字段虽然符合 omitempty 的省略条件，但用户原来的说明仍然有价值；
// 因为本例没有业务修改，保存后的文件应与原文本完全相同。
func TestSavePreservesExplicitDefaults(t *testing.T) {
	original := "version: 1\nsources:\n  - source: repo\n    ref: '' # 跟踪默认分支\n    exclude: [] # 不排除任何 Skill\n"
	path := fixture(t, original)
	doc, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	if text := fileText(t, path); text != original {
		t.Fatalf("explicit defaults changed:\n%s", text)
	}
}

// TestSaveRemovesOptionalFields 验证真实删除不会被“保留旧节点”的逻辑撤销。
// 将原来的非空字段清空后，它们应从 YAML 中消失；重新加载时 paths 再恢复默认值，
// 并且磁盘模型与保存成功后的内存模型一致，这就是本测试的往返一致性检查。
func TestSaveRemovesOptionalFields(t *testing.T) {
	path := fixture(t, "version: 1\nsources:\n  - source: repo\n    ref: main\n    paths: [custom]\n    exclude: [skip]\n    lock:\n      source: repo\n      revision: '"+strings.Repeat("a", 40)+"'\n      skills: {}\n")
	doc, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	source := &doc.Config.Sources[0]
	source.Ref, source.Paths, source.Exclude, source.Lock = "", nil, nil, nil
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Config, reloaded.Config) {
		t.Fatal("removed fields did not round trip")
	}
	for _, key := range []string{"ref:", "paths:", "exclude:", "lock:"} {
		if strings.Contains(fileText(t, path), key) {
			t.Fatalf("retained removed field %s", key)
		}
	}
}

// TestLoadEmpty 覆盖空文件、纯空白、只有注释、空映射以及空文档标记等入口情况。
// 它们都应得到版本 1 的空模型，保存后仍可读取；纯注释文件还必须保留注释。
// 文件缺失的子测试额外检查 Load 没有创建父目录，保证读取操作没有写入副作用。
func TestLoadEmpty(t *testing.T) {
	for _, content := range []string{"", " \n", "# 保留这条注释\n", "{}\n", "---\n{}\n", "---\n"} {
		t.Run(content, func(t *testing.T) {
			path := fixture(t, content)
			doc, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Config.Version != Version || len(doc.Config.Sources) != 0 {
				t.Fatalf("unexpected empty config: %#v", doc.Config)
			}
			if err := doc.Save(path); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(content, "# 保留") && !strings.Contains(fileText(t, path), "# 保留这条注释") {
				t.Fatal("comment-only file lost its comment")
			}
			if _, err := Load(path); err != nil {
				t.Fatalf("cannot reload saved empty config: %v", err)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".agents", "skillscli.yaml")
		doc, err := Load(path)
		if err != nil || doc.Config.Version != Version || len(doc.Config.Sources) != 0 {
			t.Fatalf("missing config: %#v, %v", doc, err)
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatalf("Load created directories or returned unexpected error: %v", err)
		}
	})
}

// TestLoadDefaults 验证默认值的两种表示：内存中的 paths 为 ["skills"]，
// 原文件中省略的 paths、ref、exclude 和 lock 保存后仍继续省略。
// 示例使用 Git 的 SSH/SCP 地址形式，确认配置层不会擅自只允许 HTTP URL。
func TestLoadDefaults(t *testing.T) {
	path := fixture(t, "version: 1\nsources:\n  - source: git@example.com:owner/repo.git\n")
	doc, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	source := doc.Config.Sources[0]
	if !reflect.DeepEqual(source.Paths, []string{"skills"}) || source.Ref != "" || source.Lock != nil || len(source.Exclude) != 0 {
		t.Fatalf("unexpected defaults: %#v", source)
	}
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	text := fileText(t, path)
	for _, field := range []string{"paths:", "ref:", "exclude:", "lock:"} {
		if strings.Contains(text, field) {
			t.Fatalf("materialized omitted field %s:\n%s", field, text)
		}
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

// TestLoadInvalid 使用表驱动测试集中覆盖本项目约定的非法配置。
// 这些用例不仅包含 YAML 语法错误，还包括未知字段、版本、严格字段类型、
// 空路径和不完整 lock 等业务约束；目标是验证 Load 不会接受有歧义的配置。
// 每个用例独立落盘并调用真实读取入口，不依赖后续 Git 或安装功能。
func TestLoadInvalid(t *testing.T) {
	base := "version: 1\nsources:\n  - source: repo\n"
	locked := base + "    lock:\n      source: repo\n      revision: '" + strings.Repeat("a", 40) + "'\n"
	cases := map[string]string{
		"missing skills":          locked,
		"null skills":             locked + "      skills: null\n",
		"missing h1 prefix":       locked + "      skills:\n        skill: '" + strings.TrimPrefix(fingerprint(), "h1:") + "'\n",
		"newline fingerprint":     locked + "      skills:\n        skill: \"" + fingerprint() + "\\n\"\n",
		"numeric skill name":      locked + "      skills:\n        123: '" + fingerprint() + "'\n",
		"syntax":                  "version: [\n",
		"scalar root":             "hello\n",
		"null root":               "null\n",
		"list root":               "[]\n",
		"missing version":         "sources: []\n",
		"unsupported version":     "version: 2\n",
		"string version":          "version: '1'\n",
		"unknown root":            "version: 1\nextra: true\n",
		"unknown source":          base + "    branch: main\n",
		"duplicate key":           "version: 1\nversion: 1\n",
		"multiple documents":      "version: 1\n---\nversion: 1\n",
		"empty trailing document": "version: 1\n---\n",
		"null sources":            "version: 1\nsources: null\n",
		"mapping sources":         "version: 1\nsources: {}\n",
		"null source item":        "version: 1\nsources: [null]\n",
		"missing source":          "version: 1\nsources:\n  - ref: main\n",
		"blank source":            "version: 1\nsources:\n  - source: '  '\n",
		"numeric source":          "version: 1\nsources:\n  - source: 123\n",
		"numeric ref":             base + "    ref: 123\n",
		"empty paths":             base + "    paths: []\n",
		"null paths":              base + "    paths: null\n",
		"scalar paths":            base + "    paths: skills\n",
		"empty path":              base + "    paths: ['']\n",
		"numeric path":            base + "    paths: [123]\n",
		"empty exclude":           base + "    exclude: ['']\n",
		"numeric exclude":         base + "    exclude: [123]\n",
		"null lock":               base + "    lock: null\n",
		"empty lock":              base + "    lock: {}\n",
		"missing lock source":     base + "    lock:\n      revision: '" + strings.Repeat("a", 40) + "'\n",
		"short revision":          base + "    lock:\n      source: repo\n      revision: abc\n",
		"nonhex revision":         base + "    lock:\n      source: repo\n      revision: '" + strings.Repeat("z", 40) + "'\n",
		"numeric revision":        base + "    lock:\n      source: repo\n      revision: " + strings.Repeat("1", 40) + "\n",
		"unknown lock field":      base + "    lock:\n      extra: true\n",
		"bad fingerprint":         base + "    lock:\n      source: repo\n      revision: '" + strings.Repeat("a", 40) + "'\n      skills:\n        skill: h1:bad\n",
		"empty skill name":        base + "    lock:\n      source: repo\n      revision: '" + strings.Repeat("a", 40) + "'\n      skills:\n        '': '" + fingerprint() + "'\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(fixture(t, content)); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}

// TestSavePreservesYAML 同时验证值更新与表示信息保留，而不是只验证 YAML 能被解析。
// 原文故意把 sources 放在 version 前、ref 放在 source 前、指纹键 z 放在 a 前，
// 并使用单双引号和行内列表；修改 ref、revision 并新增指纹后，原有顺序和注释应保留。
// 最后重新加载检查模型，再保存一次检查文本稳定性，避免每次保存都产生格式变化。
func TestSavePreservesYAML(t *testing.T) {
	path := fixture(t, "# 文件说明\nsources: # 来源列表\n  # 仓库说明\n  - ref: 'main' # 跟踪分支\n    source: \"repo\" # 来源地址\n    paths: [skills] # 发现目录\n    exclude: [skip] # 排除项\n    lock: # 已安装状态\n      revision: '"+strings.Repeat("a", 40)+"' # 锁定提交\n      source: repo\n      skills:\n        z: '"+fingerprint()+"' # z 的指纹\n        a: '"+fingerprint()+"' # a 的指纹\nversion: 1 # 格式版本\n")
	doc, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	doc.Config.Sources[0].Ref = "next"
	doc.Config.Sources[0].Lock.Revision = strings.Repeat("b", 40)
	doc.Config.Sources[0].Lock.Skills["new"] = fingerprint()
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	text := fileText(t, path)
	for _, part := range []string{
		"# 文件说明", "# 来源列表", "# 仓库说明", "ref: 'next' # 跟踪分支", "source: \"repo\" # 来源地址",
		"paths: [skills] # 发现目录", "exclude: [skip] # 排除项", "# 已安装状态",
		"revision: '" + strings.Repeat("b", 40) + "' # 锁定提交", "# z 的指纹", "# a 的指纹", "version: 1 # 格式版本",
	} {
		if !strings.Contains(text, part) {
			t.Errorf("lost %q:\n%s", part, text)
		}
	}
	if strings.Index(text, "sources:") > strings.Index(text, "version:") || strings.Index(text, "ref:") > strings.Index(text, "source:") || strings.Index(text, "        z:") > strings.Index(text, "        a:") {
		t.Errorf("field order changed:\n%s", text)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Config, reloaded.Config) {
		t.Fatalf("round trip mismatch: %#v != %#v", doc.Config, reloaded.Config)
	}
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	if again := fileText(t, path); again != text {
		t.Fatalf("second save changed formatting:\n%s", again)
	}
}

// TestSaveSourceCommentsFollowIdentity 将来源重排、删除并新增，验证按来源身份匹配旧节点。
// 如果仅按列表下标复用节点，第一、第二个仓库的注释就会错配；
// 被删除来源的说明应随来源一起移除，新来源则正常写入。
func TestSaveSourceCommentsFollowIdentity(t *testing.T) {
	path := fixture(t, "version: 1\nsources:\n  # 第一个仓库\n  - source: first\n  # 第二个仓库\n  - source: second\n  # 待移除的仓库\n  - source: removed\n")
	doc, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	doc.Config.Sources = []Source{doc.Config.Sources[1], doc.Config.Sources[0], {Source: "new"}}
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	text := fileText(t, path)
	for _, part := range []string{"# 第二个仓库\n  - source: second", "# 第一个仓库\n  - source: first", "source: new"} {
		if !strings.Contains(text, part) {
			t.Fatalf("lost comment/identity %q:\n%s", part, text)
		}
	}
	if strings.Contains(text, "removed") || strings.Index(text, "source: second") > strings.Index(text, "source: first") {
		t.Fatalf("source changes not applied:\n%s", text)
	}
}

// TestSaveNewConfigAndExistingPermissions 覆盖首次写入、已有文件替换和临时文件清理。
// 新文件应使用 0600，后续保存应保留人为设置的 0640；父目录最后只应剩下目标配置。
// 本例还使用 64 位提交 SHA，并让期望来源与实际安装来源不同，验证这两种合法状态。
func TestSaveNewConfigAndExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".agents", "skillscli.yaml")
	doc := &Document{Config: Config{Version: Version, Sources: []Source{{
		Source: "new-repo", Ref: "v1.0.0", Exclude: []string{"skip"},
		Lock: &Lock{Source: "installed-repo", Revision: strings.Repeat("a", 64), Skills: map[string]string{"skill": fingerprint()}},
	}}}}
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("new file mode: %v", info.Mode())
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Config, reloaded.Config) {
		t.Fatalf("new config round trip mismatch: %#v != %#v", doc.Config, reloaded.Config)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	doc.Config.Sources[0].Ref = "main"
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("existing permissions not preserved: %v, %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file leaked: %v, %v", entries, err)
	}
}

// TestSaveFailurePreservesOriginal 验证校验失败和文件系统目标错误不会改写原文件。
// 先尝试保存非法版本，再把目录或不可用的父路径作为目标，最后恢复正确路径重试。
// 重试仍应保留原注释并写入新 ref，证明失败的合并过程没有破坏 Document 的原始节点树。
func TestSaveFailurePreservesOriginal(t *testing.T) {
	original := "# 原始配置\nversion: 1\nsources:\n  - source: repo\n"
	path := fixture(t, original)
	doc, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	doc.Config.Version = 2
	if err := doc.Save(path); err == nil {
		t.Fatal("saved invalid config")
	}
	if text := fileText(t, path); text != original {
		t.Fatal("validation failure changed file")
	}
	doc.Config.Version = Version
	doc.Config.Sources[0].Ref = "next"
	if err := doc.Save(filepath.Dir(path)); err == nil {
		t.Fatal("accepted a directory as the config target")
	}
	if text := fileText(t, path); text != original {
		t.Fatal("write failure changed original file")
	}
	blocked := filepath.Join(path, "child", "skillscli.yaml")
	if err := doc.Save(blocked); err == nil {
		t.Fatal("accepted a file as the parent directory")
	}
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	if text := fileText(t, path); !strings.Contains(text, "# 原始配置") || !strings.Contains(text, "ref: next") {
		t.Fatalf("failed save changed retained YAML tree:\n%s", text)
	}
}

// TestSaveRejectsSymlink 验证读取与写入对符号链接采用不同规则。
// Load 可以读取链接指向的配置，但 Save 拒绝用链接路径作为替换目标；
// 失败后既不能改动链接指向的文件，也不能把链接本身替换成普通文件。
func TestSaveRejectsSymlink(t *testing.T) {
	path := fixture(t, "version: 1\n")
	link := filepath.Join(filepath.Dir(path), "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	doc, err := Load(link)
	if err != nil {
		t.Fatal(err)
	}
	doc.Config.Sources = []Source{{Source: "repo"}}
	if err := doc.Save(link); err == nil {
		t.Fatal("overwrote symlink")
	}
	if text := fileText(t, path); text != "version: 1\n" {
		t.Fatal("symlink target changed")
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v, %v", info, err)
	}
}
