package skills

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zz-zhi54/skillscli/internal/config"
	gitrepo "github.com/zz-zhi54/skillscli/internal/git"
)

// 使用已有配置读取和 Git 获取入口验证离线数据流，不引入安装或业务命令。
func localRepository(t *testing.T) (string, func(...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("tests require system Git:", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	for _, key := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		t.Setenv(key, "")
	}
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		options := []string{"-C", root, "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}
		cmd := exec.Command("git", append(options, args...)...)
		// 空的仓库定位变量也会影响 Git；测试命令不继承它们。
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			switch key {
			case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--template=", "--initial-branch=main")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.invalid")
	return root, run
}

func TestConfigFetchDiscoverReadOnly(t *testing.T) {
	root, git := localRepository(t)
	skillFixture(t, root, "skills/idea")
	skillFixture(t, root, "skills/idea-debug")
	marker := filepath.Join(t.TempDir(), "script-ran")
	writeFile(t, filepath.Join(root, "skills", "idea", "scripts", "do-not-run.sh"), "#!/bin/sh\ntouch '"+marker+"'\n")
	git("add", ".")
	git("commit", "-m", "skills fixture")
	project := t.TempDir()
	configFile := filepath.Join(project, ".agents", "skillscli.yaml")
	original := fmt.Sprintf("version: 1\nsources:\n  - source: %q\n    exclude: [idea-debug]\n", filepath.ToSlash(root))
	writeFile(t, configFile, original)
	doc, err := config.Load(configFile)
	if err != nil {
		t.Fatal(err)
	}
	source := doc.Config.Sources[0]
	checkout, err := gitrepo.Fetch(context.Background(), source.Source, source.Ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := checkout.Close(); err != nil {
			t.Error(err)
		}
	})
	before, err := Hash(checkout.Dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Discover(checkout.Dir, source)
	if err != nil || len(got) != 1 || got[0].Name != "idea" || got[0].Path != "skills/idea" {
		t.Fatalf("discovery = %#v, %v", got, err)
	}
	after, err := Hash(checkout.Dir)
	if err != nil || after != before {
		t.Fatalf("discovery changed checkout contents: %v", err)
	}
	content, err := os.ReadFile(configFile)
	if err != nil || string(content) != original {
		t.Fatalf("discovery changed config: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(project, ".agents", "skills")); !os.IsNotExist(err) {
		t.Fatalf("installation directory was created: %v", err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("repository script ran: %v", err)
	}
}

func TestUninitializedSubmodulesAreNotSkillFiles(t *testing.T) {
	for _, markerGitlink := range []bool{false, true} {
		t.Run(fmt.Sprint(markerGitlink), func(t *testing.T) {
			root, git := localRepository(t)
			skillFixture(t, root, "skills/idea")
			git("add", ".")
			git("commit", "-m", "base")
			revision := git("rev-parse", "HEAD")
			submodule := "skills/uninitialized"
			if markerGitlink {
				submodule = "skills/gitlink-skill/SKILL.md"
			}
			git("update-index", "--add", "--cacheinfo", "160000,"+revision+","+submodule)
			git("commit", "-m", "uninitialized gitlink")
			checkout, err := gitrepo.Fetch(context.Background(), root, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := checkout.Close(); err != nil {
					t.Error(err)
				}
			})
			info, err := os.Lstat(filepath.Join(checkout.Dir, filepath.FromSlash(submodule)))
			if err != nil || !info.IsDir() {
				t.Fatalf("expected an uninitialized submodule directory: %v", err)
			}
			got, err := Discover(checkout.Dir, config.Source{})
			if markerGitlink {
				requireError(t, err, "gitlink-skill", "SKILL.md", "regular file")
				return
			}
			if err != nil || len(got) != 1 || got[0].Name != "idea" {
				t.Fatalf("uninitialized submodule discovered as Skill: %#v, %v", got, err)
			}
		})
	}
}
