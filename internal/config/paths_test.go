package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolvePathsScopes 验证项目和全局作用域各自使用正确根目录，且解析路径不创建目录。
// 当前工作目录和环境变量属于进程共享状态，所以这些测试不调用 t.Parallel；
// 工作目录用 Cleanup 恢复，HOME 则由 t.Setenv 自动恢复，避免影响其他测试。
func TestResolvePathsScopes(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Error(err)
		}
	})
	// 临时目录路径可能包含系统符号链接，例如 macOS 的 /var。
	// 切换目录后重新读取工作目录，使用与生产代码一致的实际路径进行比较，
	// 避免把 /var 与 /private/var 的表示差异误判为作用域解析错误。
	project, err = os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for _, tc := range []struct {
		name   string
		global bool
		root   string
	}{
		{name: "project", root: project},
		{name: "global", global: true, root: home},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, err := ResolvePaths(tc.global)
			if err != nil {
				t.Fatal(err)
			}
			if paths.Config != filepath.Join(tc.root, ".agents", "skillscli.yaml") || paths.Skills != filepath.Join(tc.root, ".agents", "skills") {
				t.Fatalf("unexpected scope paths: %#v", paths)
			}
			if _, err := os.Stat(filepath.Join(tc.root, ".agents")); !os.IsNotExist(err) {
				t.Fatalf("path resolution created files: %v", err)
			}
		})
	}
}

// TestResolveGlobalWithoutHome 验证全局作用域缺少 HOME 时应报错，而不是退回项目目录。
// 同时确认项目作用域不依赖 HOME，两个作用域只读取各自需要的运行环境。
func TestResolveGlobalWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := ResolvePaths(true); err == nil {
		t.Fatal("resolved global scope without a home directory")
	}
	if _, err := ResolvePaths(false); err != nil {
		t.Fatalf("project scope should not need HOME: %v", err)
	}
}

// TestScopeConfigsAreIndependent 将路径解析和配置读写组合起来测试。
// 两个作用域分别写入不同来源再重新加载，验证不会相互覆盖；同时检查只写配置
// 不会创建 Skill 安装目录，保持第一阶段配置操作与后续安装操作的职责边界。
func TestScopeConfigsAreIndependent(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("HOME", home)
	projectPaths, err := ResolvePaths(false)
	if err != nil {
		t.Fatal(err)
	}
	globalPaths, err := ResolvePaths(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		source string
	}{
		{projectPaths.Config, "project-repo"},
		{globalPaths.Config, "global-repo"},
	} {
		doc, err := Load(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		doc.Config.Sources = []Source{{Source: tc.source}}
		if err := doc.Save(tc.path); err != nil {
			t.Fatal(err)
		}
	}
	projectDoc, err := Load(projectPaths.Config)
	if err != nil {
		t.Fatal(err)
	}
	globalDoc, err := Load(globalPaths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if projectDoc.Config.Sources[0].Source != "project-repo" || globalDoc.Config.Sources[0].Source != "global-repo" {
		t.Fatalf("scope configs interfere: %#v, %#v", projectDoc.Config, globalDoc.Config)
	}
	for _, paths := range []Paths{projectPaths, globalPaths} {
		if _, err := os.Stat(paths.Skills); !os.IsNotExist(err) {
			t.Fatalf("config write created a Skill installation directory: %v", err)
		}
	}
}
