package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zz-zhi54/skillscli/internal/config"
)

func writeFile(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func skillFixture(t *testing.T, root, rel string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel))
	writeFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+filepath.Base(dir)+"\ndescription: Test skill.\n---\n# Instructions\n")
	return dir
}

func requireError(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error %q does not contain %q", err, fragment)
		}
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"skills/idea", "skills/idea-database", "skills/idea-debug", "extra/other", "single", "skills/container/nested"} {
		skillFixture(t, root, rel)
	}
	// 普通文件和没有 SKILL.md 的子目录不是候选；更深层的 Skill 不递归发现。
	writeFile(t, filepath.Join(root, "skills", "README.md"), "not a skill")
	if err := os.MkdirAll(filepath.Join(root, "skills", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		paths   []string
		exclude []string
		want    []string
	}{
		{"default", nil, nil, []string{"skills/idea", "skills/idea-database", "skills/idea-debug"}},
		{"single", []string{"skills/idea"}, nil, []string{"skills/idea"}},
		{"multiple", []string{"extra", "single", "skills"}, nil, []string{"skills/idea", "skills/idea-database", "skills/idea-debug", "extra/other", "single"}},
		{"exclude", nil, []string{"idea-debug", "unknown"}, []string{"skills/idea", "skills/idea-database"}},
		{"deduplicate", []string{"skills", "skills/idea", "./skills/", "skills//idea"}, nil, []string{"skills/idea", "skills/idea-database", "skills/idea-debug"}},
		{"empty", []string{"skills/empty"}, nil, []string{}},
		{"not recursive", []string{"skills/container"}, nil, []string{"skills/container/nested"}},
		{"all excluded", nil, []string{"idea", "idea-database", "idea-debug"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := config.Source{Paths: tc.paths, Exclude: tc.exclude}
			before := config.Source{Paths: append([]string(nil), tc.paths...), Exclude: append([]string(nil), tc.exclude...)}
			got, err := Discover(root, source)
			if err != nil {
				t.Fatal(err)
			}
			paths := make([]string, 0, len(got))
			for _, skill := range got {
				paths = append(paths, skill.Path)
				if skill.Name != filepath.Base(skill.Dir) || !filepath.IsAbs(skill.Dir) {
					t.Fatalf("invalid result: %#v", skill)
				}
				expectedDir, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(skill.Path)))
				if err != nil || skill.Dir != expectedDir {
					t.Fatalf("Dir = %q, expected %q (%v)", skill.Dir, expectedDir, err)
				}
				hash, err := Hash(skill.Dir)
				if err != nil || hash != skill.Hash || !strings.HasPrefix(hash, "h1:") {
					t.Fatalf("Hash = %q, expected %q (%v)", skill.Hash, hash, err)
				}
			}
			if !reflect.DeepEqual(paths, tc.want) {
				t.Fatalf("paths = %v, want %v", paths, tc.want)
			}
			if !reflect.DeepEqual(source, before) {
				t.Fatal("Discover mutated source configuration")
			}
		})
	}
}

func TestDiscoverParentSkillDoesNotScanNestedSkills(t *testing.T) {
	root := t.TempDir()
	skillFixture(t, root, "skills/parent")
	skillFixture(t, root, "skills/parent/nested")
	got, err := Discover(root, config.Source{Paths: []string{"skills/parent"}})
	if err != nil || len(got) != 1 || got[0].Name != "parent" {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestDiscoverDuplicateNames(t *testing.T) {
	root := t.TempDir()
	skillFixture(t, root, "first/idea")
	skillFixture(t, root, "second/idea")
	for _, paths := range [][]string{{"first", "second"}, {"second/idea", "first/idea"}} {
		for _, exclude := range [][]string{nil, {"idea"}} {
			got, err := Discover(root, config.Source{Paths: paths, Exclude: exclude})
			requireError(t, err, "duplicate Skill", "idea", "first/idea", "second/idea")
			if got != nil {
				t.Fatal("returned partial discovery on conflict")
			}
		}
	}
}

func TestDiscoverInvalidPaths(t *testing.T) {
	root := t.TempDir()
	skillFixture(t, root, "skills/idea")
	writeFile(t, filepath.Join(root, "file"), "not a directory")
	for _, configured := range []string{"", " ", "..", "../outside", "skills/../skills", "/skills", "//host/share", "C:/skills", "C:skills", `skills\idea`, `..\outside`, `\\host\share`, "skills\x00/idea", ".git", "skills/.git", "missing", "file", "file/child"} {
		t.Run(configured, func(t *testing.T) {
			got, err := Discover(root, config.Source{Paths: []string{configured}})
			requireError(t, err, "path")
			if got != nil {
				t.Fatal("returned partial result for invalid path")
			}
		})
	}
	_, err := Discover(root, config.Source{Paths: []string{}})
	requireError(t, err, "paths must not be empty")
	_, err = Discover(root, config.Source{Paths: []string{"skills", "missing"}})
	requireError(t, err, "missing")
	_, err = Discover(filepath.Join(root, "missing"), config.Source{})
	requireError(t, err, "missing")
	_, err = Discover(filepath.Join(root, "file"), config.Source{})
	requireError(t, err, "directory")
	_, err = Hash("")
	requireError(t, err, "empty")
}

func TestDiscoverRootPath(t *testing.T) {
	root := t.TempDir()
	skillFixture(t, root, "idea")
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(root, config.Source{Paths: []string{"."}})
	if err != nil || len(got) != 1 || got[0].Path != "idea" {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestDiscoverInvalidSkill(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "broken", "SKILL.md"), "---\nname: another\ndescription: Test\n---\n")
	got, err := Discover(root, config.Source{})
	requireError(t, err, "broken", "SKILL.md", "match directory")
	if got != nil {
		t.Fatal("returned invalid Skill")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name, content, wantError string
	}{
		{"minimal", "---\nname: idea\ndescription: Test\n---", ""},
		{"optional and unknown fields", "---\nname: idea\ndescription: |\n  Multiline description.\nlicense: MIT\ncompatibility: Requires Git\nmetadata:\n  author: test\n  version: '1.0'\nallowed-tools: Read Bash(git:*)\nfuture-option: [one, two]\n---\nAnything", ""},
		{"crlf and bom", "\xef\xbb\xbf---\r\nname: idea\r\ndescription: Test\r\n---\r\n", ""},
		{"delimiter in multiline description", "---\nname: idea\ndescription: |\n  A description.\n  ---\n  More description.\n---\n", ""},
		{"content after yaml document end", "---\nname: idea\ndescription: Test\n...\nname: ignored\n---\n", "invalid YAML"},
		{"maximum description", "---\nname: idea\ndescription: " + strings.Repeat("中", 1024) + "\n---\n", ""},
		{"missing frontmatter", "# Hello", "opening delimiter"},
		{"unclosed frontmatter", "---\nname: idea\ndescription: Test\n", "closing delimiter"},
		{"empty frontmatter", "---\n---\n", "name"},
		{"yaml syntax", "---\nname: [\n---\n", "invalid YAML"},
		{"yaml sequence", "---\n- idea\n---\n", "invalid YAML"},
		{"duplicate field", "---\nname: idea\nname: idea\ndescription: Test\n---\n", "invalid YAML"},
		{"missing name", "---\ndescription: Test\n---\n", "name"},
		{"numeric name", "---\nname: 123\ndescription: Test\n---\n", "name must be a non-empty string"},
		{"null name", "---\nname: null\ndescription: Test\n---\n", "name"},
		{"uppercase", "---\nname: Idea\ndescription: Test\n---\n", "lowercase"},
		{"underscore", "---\nname: idea_tool\ndescription: Test\n---\n", "lowercase"},
		{"space", "---\nname: ' idea'\ndescription: Test\n---\n", "lowercase"},
		{"leading hyphen", "---\nname: -idea\ndescription: Test\n---\n", "hyphen"},
		{"trailing hyphen", "---\nname: idea-\ndescription: Test\n---\n", "hyphen"},
		{"double hyphen", "---\nname: idea--tool\ndescription: Test\n---\n", "hyphen"},
		{"long name", "---\nname: " + strings.Repeat("a", 65) + "\ndescription: Test\n---\n", "1-64"},
		{"mismatched name", "---\nname: other\ndescription: Test\n---\n", "match directory"},
		{"missing description", "---\nname: idea\n---\n", "description"},
		{"numeric description", "---\nname: idea\ndescription: 123\n---\n", "description"},
		{"blank description", "---\nname: idea\ndescription: '   '\n---\n", "description"},
		{"long description", "---\nname: idea\ndescription: " + strings.Repeat("中", 1025) + "\n---\n", "1-1024"},
		{"invalid utf8", "---\nname: idea\ndescription: \xff\n---\n", "UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "idea")
			file := filepath.Join(dir, "SKILL.md")
			writeFile(t, file, tc.content)
			name, err := Validate(dir)
			if tc.wantError != "" {
				requireError(t, err, "idea", "SKILL.md", tc.wantError)
			} else if err != nil || name != "idea" {
				t.Fatalf("name = %q, error = %v", name, err)
			}
			content, readErr := os.ReadFile(file)
			if readErr != nil || string(content) != tc.content {
				t.Fatalf("Validate changed SKILL.md: %v", readErr)
			}
		})
	}
}

func TestValidateNameBoundaries(t *testing.T) {
	for _, name := range []string{"a", "skill-123", "123", strings.Repeat("a", 64), "数据分析", "café", strings.Repeat("中", 64)} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			writeFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: '"+name+"'\ndescription: Test\n---\n")
			got, err := Validate(dir)
			if err != nil || got != name {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestValidateMissingAndNonRegularSkillFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "idea")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := Validate(dir)
	requireError(t, err, "idea", "missing SKILL.md")
	// Gitlink（未检出子模块）也是目录，不能将其当作 SKILL.md 普通文件。
	if err := os.Mkdir(filepath.Join(dir, "SKILL.md"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = Validate(dir)
	requireError(t, err, "idea", "SKILL.md", "regular file")
	_, err = Discover(filepath.Dir(dir), config.Source{Paths: []string{"idea"}})
	requireError(t, err, "SKILL.md", "regular file")
}

func TestHashContentAndRelativePaths(t *testing.T) {
	root := t.TempDir()
	first := skillFixture(t, root, "first/idea")
	second := skillFixture(t, root, "second/idea")
	// 相同文件以不同创建顺序落盘，并使用不同目录位置和修改时间。
	for _, file := range []string{"assets/b.bin", "scripts/a.sh", ".hidden"} {
		writeFile(t, filepath.Join(first, filepath.FromSlash(file)), file+"\x00contents")
	}
	for _, file := range []string{".hidden", "scripts/a.sh", "assets/b.bin"} {
		writeFile(t, filepath.Join(second, filepath.FromSlash(file)), file+"\x00contents")
	}
	original, err := Hash(first)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Hash(second)
	if err != nil || original != other {
		t.Fatalf("location/order changed hash: %q != %q (%v)", original, other, err)
	}
	if err := os.Chtimes(filepath.Join(first, "SKILL.md"), time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	afterTime, err := Hash(first)
	if err != nil || afterTime != original {
		t.Fatalf("mtime changed hash: %q (%v)", afterTime, err)
	}
	last := original
	assertChanged := func() {
		t.Helper()
		got, err := Discover(root, config.Source{Paths: []string{"first/idea"}})
		if err != nil || len(got) != 1 {
			t.Fatalf("Discover = %#v, %v", got, err)
		}
		if got[0].Hash == last {
			t.Fatal("file change did not change discovered fingerprint")
		}
		last = got[0].Hash
	}
	added := filepath.Join(first, "references", "new.md")
	writeFile(t, added, "added")
	assertChanged()
	writeFile(t, added, "modified")
	assertChanged()
	if err := os.Remove(added); err != nil {
		t.Fatal(err)
	}
	assertChanged()
	if last != original {
		t.Fatal("removing added file did not restore original fingerprint")
	}
	// 内容相同但相对文件名变化，也必须改变目录指纹。
	writeFile(t, added, "same content")
	assertChanged()
	if err := os.Rename(added, filepath.Join(first, "references", "renamed.md")); err != nil {
		t.Fatal(err)
	}
	assertChanged()
}

func TestHashEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	hash, err := Hash(dir)
	if err != nil || !strings.HasPrefix(hash, "h1:") {
		t.Fatalf("empty hash = %q, %v", hash, err)
	}
	got, err := Discover(dir, config.Source{Paths: []string{"."}})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty discovery = %#v, %v", got, err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if os.IsPermission(err) {
			t.Skip("symlinks require OS permission:", err)
		}
		t.Fatal(err)
	}
}

func TestSymlinkSafety(t *testing.T) {
	for _, kind := range []string{"skill file", "nested file", "nested directory", "internal link", "dangling link"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := skillFixture(t, root, "skills/idea")
			outside := filepath.Join(t.TempDir(), "outside.txt")
			writeFile(t, outside, "must not be read")
			link, target := filepath.Join(dir, "link"), outside
			switch kind {
			case "skill file":
				link = filepath.Join(dir, "SKILL.md")
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			case "nested file":
				link = filepath.Join(dir, "references", "link")
				if err := os.Mkdir(filepath.Dir(link), 0700); err != nil {
					t.Fatal(err)
				}
			case "nested directory":
				target = filepath.Dir(outside)
			case "internal link":
				target = filepath.Join(dir, "SKILL.md")
			case "dangling link":
				target = filepath.Join(root, "missing")
			}
			symlink(t, target, link)
			_, err := Hash(dir)
			requireError(t, err, "symlink", filepath.Base(link))
			_, err = Validate(dir)
			requireError(t, err, "symlink", filepath.Base(link))
			_, err = Discover(root, config.Source{})
			requireError(t, err, "symlink", filepath.Base(link))
		})
	}
	for _, kind := range []string{"root", "ancestor", "direct child"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			skillFixture(t, outside, "idea")
			link := filepath.Join(root, "skills")
			symlink(t, outside, link)
			source := config.Source{}
			switch kind {
			case "root":
				root = link
				source.Paths = []string{"idea"}
			case "ancestor":
				source.Paths = []string{"skills/idea"}
			case "direct child":
				source.Paths = []string{"."}
			}
			_, err := Discover(root, source)
			requireError(t, err, "symlink")
		})
	}
}
