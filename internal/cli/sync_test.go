package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zz-zhi54/skillscli/internal/config"
	skillsync "github.com/zz-zhi54/skillscli/internal/sync"
)

// 这些测试修改 cwd/env，不能使用 t.Parallel。仅允许 file 协议，避免失败用例访问网络。
func cliEnvironment(t *testing.T) (project, home string) {
	t.Helper()
	project, home = t.TempDir(), t.TempDir()
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
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	return project, home
}

func cliExecute(args ...string) (string, error) {
	cmd := NewRootCommand("v0.0.0-test")
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func cliPaths(root string) config.Paths {
	return config.Paths{
		Config: filepath.Join(root, ".agents", "skillscli.yaml"),
		Skills: filepath.Join(root, ".agents", "skills"),
	}
}

func cliWrite(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func cliRead(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func cliConfig(t *testing.T, root string, sources ...config.Source) {
	t.Helper()
	doc := &config.Document{Config: config.Config{Version: config.Version, Sources: sources}}
	if err := doc.Save(cliPaths(root).Config); err != nil {
		t.Fatal(err)
	}
}

func cliLoad(t *testing.T, root string) *config.Document {
	t.Helper()
	doc, err := config.Load(cliPaths(root).Config)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func cliAbsent(t *testing.T, file string) {
	t.Helper()
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected absent %s, got %v", file, err)
	}
}

type cliRepository struct {
	t   *testing.T
	dir string
}

func cliRepo(t *testing.T, label string, names ...string) *cliRepository {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("CLI tests require system Git:", err)
	}
	r := &cliRepository{t: t, dir: filepath.Join(t.TempDir(), label)}
	if err := os.Mkdir(r.dir, 0700); err != nil {
		t.Fatal(err)
	}
	r.git("init", "--template=", "--initial-branch=main")
	r.git("config", "user.name", "CLI Test")
	r.git("config", "user.email", "cli@example.invalid")
	cliWrite(t, filepath.Join(r.dir, "skills", "README.md"), "offline fixture\n")
	for _, name := range names {
		r.skill(name)
	}
	r.commit()
	return r
}

func (r *cliRepository) git(args ...string) string {
	r.t.Helper()
	options := []string{"-C", r.dir, "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}
	cmd := exec.Command("git", append(options, args...)...)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
			"GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_PREFIX", "GIT_SHALLOW_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("fixture Git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *cliRepository) skill(name string) {
	r.t.Helper()
	cliWrite(r.t, filepath.Join(r.dir, "skills", name, "SKILL.md"), "---\nname: "+name+"\ndescription: Offline CLI fixture.\n---\n# Instructions\n")
	cliWrite(r.t, filepath.Join(r.dir, "skills", name, "data.txt"), "upstream")
}

func (r *cliRepository) commit() string {
	r.t.Helper()
	r.git("add", ".")
	r.git("commit", "-m", "offline fixture")
	return r.git("rev-parse", "HEAD")
}

func TestSyncCommandRegistrationAndArguments(t *testing.T) {
	project, home := cliEnvironment(t)
	root := NewRootCommand("v0.0.0-test")
	var commands []string
	for _, cmd := range root.Commands() {
		commands = append(commands, cmd.Name())
	}
	if !reflect.DeepEqual(commands, []string{"add", "list", "remove", "sync"}) {
		t.Fatalf("registered commands = %v", commands)
	}
	for _, args := range [][]string{{"sync", "extra"}, {"sync", "--force", "extra"}, {"upgrade"}, {"add"}, {"--force"}, {"sync", "--unknown"}} {
		if _, err := cliExecute(args...); err == nil {
			t.Fatalf("arguments %v unexpectedly accepted", args)
		}
	}
	for _, args := range [][]string{{}, {"sync", "--help"}, {"-g", "sync", "--help"}, {"sync", "--global", "--help"}} {
		out, err := cliExecute(args...)
		if err != nil || !strings.Contains(out, "sync") {
			t.Fatalf("help %v: output %q, error %v", args, out, err)
		}
	}
	cliAbsent(t, filepath.Join(project, ".agents"))
	cliAbsent(t, filepath.Join(home, ".agents"))
}

func TestSyncFirstIdempotentAndChangedLists(t *testing.T) {
	project, home := cliEnvironment(t)
	r := cliRepo(t, "upstream", "alpha", "obsolete")
	cliConfig(t, project, config.Source{Source: r.dir})
	out, err := cliExecute("sync")
	if err != nil || out != "✓ upstream\n  Added: alpha, obsolete\n" {
		t.Fatalf("first sync: %q, %v", out, err)
	}
	paths := cliPaths(project)
	if got := cliRead(t, filepath.Join(paths.Skills, "alpha", "data.txt")); got != "upstream" {
		t.Fatalf("installed content = %q", got)
	}
	lock := cliLoad(t, project).Config.Sources[0].Lock
	if lock == nil || lock.Source != r.dir || lock.Revision != r.git("rev-parse", "HEAD") || len(lock.Skills) != 2 || lock.Skills["alpha"] == "" {
		t.Fatalf("initial lock = %#v", lock)
	}
	before := cliRead(t, paths.Config)
	out, err = cliExecute("sync")
	if err != nil || out != "- upstream No changes\n" || cliRead(t, paths.Config) != before {
		t.Fatalf("idempotent sync: %q, %v", out, err)
	}
	cliWrite(t, filepath.Join(r.dir, "skills", "alpha", "data.txt"), "updated upstream")
	r.skill("beta")
	revision := r.commit()
	doc := cliLoad(t, project)
	doc.Config.Sources[0].Exclude = []string{"obsolete"}
	if err := doc.Save(paths.Config); err != nil {
		t.Fatal(err)
	}
	out, err = cliExecute("sync")
	if err != nil || out != "✓ upstream\n  Added: beta\n  Updated: alpha\n  Removed: obsolete\n" {
		t.Fatalf("changed sync: %q, %v", out, err)
	}
	if cliRead(t, filepath.Join(paths.Skills, "alpha", "data.txt")) != "updated upstream" {
		t.Fatal("update was not deployed")
	}
	cliAbsent(t, filepath.Join(paths.Skills, "obsolete"))
	lock = cliLoad(t, project).Config.Sources[0].Lock
	if lock.Revision != revision || len(lock.Skills) != 2 || lock.Skills["obsolete"] != "" || lock.Skills["beta"] == "" {
		t.Fatalf("updated lock = %#v", lock)
	}
	cliAbsent(t, filepath.Join(home, ".agents"))
}

func TestSyncForceManagedChanges(t *testing.T) {
	project, _ := cliEnvironment(t)
	r := cliRepo(t, "managed", "alpha")
	cliConfig(t, project, config.Source{Source: r.dir})
	if out, err := cliExecute("sync"); err != nil {
		t.Fatalf("initial sync: %q, %v", out, err)
	}
	paths := cliPaths(project)
	local := filepath.Join(paths.Skills, "alpha", "data.txt")
	cliWrite(t, local, "local edit")
	before := cliRead(t, paths.Config)
	out, err := cliExecute("sync")
	if err == nil || !strings.Contains(out, "! managed Skipped: alpha: local modifications") || !strings.Contains(out, "--force") {
		t.Fatalf("managed conflict: %q, %v", out, err)
	}
	if cliRead(t, local) != "local edit" || cliRead(t, paths.Config) != before {
		t.Fatal("skipped source was changed")
	}
	out, err = cliExecute("sync", "--force")
	if err != nil || out != "✓ managed\n  Updated: alpha\n" || cliRead(t, local) != "upstream" {
		t.Fatalf("force sync: %q, %v", out, err)
	}
}

func TestSyncForceNeverOverwritesUnmanaged(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "force"}[force], func(t *testing.T) {
			project, _ := cliEnvironment(t)
			r := cliRepo(t, "unmanaged", "alpha")
			cliConfig(t, project, config.Source{Source: r.dir})
			paths := cliPaths(project)
			local := filepath.Join(paths.Skills, "alpha", "data.txt")
			cliWrite(t, local, "unmanaged content")
			before := cliRead(t, paths.Config)
			args := []string{"sync"}
			if force {
				args = append(args, "--force")
			}
			out, err := cliExecute(args...)
			if err == nil || out != "! unmanaged Skipped: alpha: unmanaged target exists\n" {
				t.Fatalf("unmanaged conflict: %q, %v", out, err)
			}
			if cliRead(t, local) != "unmanaged content" || cliRead(t, paths.Config) != before || cliLoad(t, project).Config.Sources[0].Lock != nil {
				t.Fatal("unmanaged target or lock was overwritten")
			}
		})
	}
}

func TestSyncGlobalProjectIsolation(t *testing.T) {
	project, home := cliEnvironment(t)
	local := cliRepo(t, "project-repo", "project-skill")
	global := cliRepo(t, "global-repo", "global-skill")
	cliConfig(t, project, config.Source{Source: local.dir})
	cliConfig(t, home, config.Source{Source: global.dir})
	projectBefore := cliRead(t, cliPaths(project).Config)
	out, err := cliExecute("-g", "sync")
	if err != nil || out != "✓ global-repo\n  Added: global-skill\n" {
		t.Fatalf("global sync: %q, %v", out, err)
	}
	if cliRead(t, cliPaths(project).Config) != projectBefore {
		t.Fatal("global sync changed project config")
	}
	cliAbsent(t, cliPaths(project).Skills)
	globalBefore := cliRead(t, cliPaths(home).Config)
	out, err = cliExecute("sync")
	if err != nil || out != "✓ project-repo\n  Added: project-skill\n" {
		t.Fatalf("project sync: %q, %v", out, err)
	}
	if cliRead(t, cliPaths(home).Config) != globalBefore {
		t.Fatal("project sync changed global config")
	}
	cliAbsent(t, filepath.Join(cliPaths(project).Skills, "global-skill"))
	cliAbsent(t, filepath.Join(cliPaths(home).Skills, "project-skill"))
	out, err = cliExecute("sync", "--global")
	if err != nil || out != "- global-repo No changes\n" {
		t.Fatalf("inherited long flag: %q, %v", out, err)
	}
	for _, flag := range []string{"-g", "--global"} {
		localFile := filepath.Join(cliPaths(home).Skills, "global-skill", "data.txt")
		cliWrite(t, localFile, "global local edit")
		projectBefore := cliRead(t, cliPaths(project).Config)
		out, err = cliExecute("sync", flag, "--force")
		if err != nil || out != "✓ global-repo\n  Updated: global-skill\n" || cliRead(t, localFile) != "upstream" {
			t.Fatalf("inherited %s with force: %q, %v", flag, out, err)
		}
		if cliRead(t, cliPaths(project).Config) != projectBefore || cliRead(t, filepath.Join(cliPaths(project).Skills, "project-skill", "data.txt")) != "upstream" {
			t.Fatal("global force changed project state")
		}
	}
}

func TestSyncMultipleSourcesContinueAndSafeErrors(t *testing.T) {
	project, _ := cliEnvironment(t)
	first := cliRepo(t, "first", "alpha")
	blocked := cliRepo(t, "blocked", "blocked-skill")
	last := cliRepo(t, "last", "omega")
	// file URL 指向不存在的本地仓库；userinfo/query 是模拟凭证，不触网。
	secretSource := "file://user:password@localhost" + filepath.ToSlash(filepath.Join(t.TempDir(), "missing.git")) + "?token=topsecret"
	cliConfig(t, project, config.Source{Source: first.dir}, config.Source{Source: secretSource}, config.Source{Source: blocked.dir}, config.Source{Source: last.dir})
	paths := cliPaths(project)
	unmanaged := filepath.Join(paths.Skills, "blocked-skill", "data.txt")
	cliWrite(t, unmanaged, "preserve me")
	out, err := cliExecute("sync")
	want := "✓ first\n  Added: alpha\n! missing Failed: could not synchronize source\n! blocked Skipped: blocked-skill: unmanaged target exists\n✓ last\n  Added: omega\n"
	if err == nil || out != want {
		t.Fatalf("multi-source sync: %q, %v", out, err)
	}
	for _, sensitive := range []string{secretSource, "password", "topsecret", "user:", "fatal:", "file://"} {
		if strings.Contains(out+err.Error(), sensitive) {
			t.Fatalf("sensitive data %q leaked", sensitive)
		}
	}
	sources := cliLoad(t, project).Config.Sources
	if sources[0].Lock == nil || sources[1].Lock != nil || sources[2].Lock != nil || sources[3].Lock == nil {
		t.Fatalf("source isolation failed: %#v", sources)
	}
	if cliRead(t, unmanaged) != "preserve me" || cliRead(t, filepath.Join(paths.Skills, "alpha", "data.txt")) != "upstream" || cliRead(t, filepath.Join(paths.Skills, "omega", "data.txt")) != "upstream" {
		t.Fatal("source deployment isolation failed")
	}
}

func TestSyncStartupRecoveryReportsRetainedDirectory(t *testing.T) {
	project, _ := cliEnvironment(t)
	cliConfig(t, project, config.Source{Source: "https://user:password@example.invalid/repo.git?token=topsecret"})
	paths := cliPaths(project)
	before := cliRead(t, paths.Config)
	txn := filepath.Join(filepath.Dir(paths.Config), ".skillscli-txn-retained")
	if err := os.Mkdir(txn, 0700); err != nil {
		t.Fatal(err)
	}
	// 未完成事务包含不安全对象；恢复必须停止并保留现场，不能跟随链接。
	outside := t.TempDir()
	cliWrite(t, filepath.Join(outside, "marker"), "preserve me")
	if err := os.Symlink(outside, filepath.Join(txn, "unsafe")); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(txn)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cliExecute("sync")
	want := fmt.Sprintf("! sync Failed: automatic recovery failed; recovery data retained at %q: unsafe object in recovery directory %q\n", canonical, filepath.Join(canonical, "unsafe"))
	if err == nil || out != want || err.Error() != "sync failed" {
		t.Fatalf("startup recovery output: %q, %v", out, err)
	}
	for _, sensitive := range []string{"password", "topsecret", "https://", "user:"} {
		if strings.Contains(out+err.Error(), sensitive) {
			t.Fatalf("recovery leaked source data %q", sensitive)
		}
	}
	if cliRead(t, paths.Config) != before || cliRead(t, filepath.Join(outside, "marker")) != "preserve me" {
		t.Fatal("failed recovery changed config or external files")
	}
	if info, err := os.Lstat(filepath.Join(txn, "unsafe")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("recovery data was not retained: %v", err)
	}
}

func TestSyncResultRecoveryReportsOnlyRecoveryError(t *testing.T) {
	project, _ := cliEnvironment(t)
	dir := filepath.Join(project, ".agents", ".skillscli-txn-retained")
	recovery := &skillsync.RecoveryError{Dir: dir, Err: errors.New("original backup is missing")}
	result := skillsync.Result{
		Status: skillsync.Failed,
		Err:    errors.Join(errors.New("https://user:password@example.invalid/repo?token=topsecret"), recovery),
		Source: skillsync.SourcePlan{Source: config.Source{Source: "https://user:password@example.invalid/repo.git?token=topsecret"}},
	}
	var out bytes.Buffer
	if err := printSyncResult(&out, result); err != nil {
		t.Fatal(err)
	}
	if want := "! repo Failed: " + recovery.Error() + "\n"; out.String() != want {
		t.Fatalf("source recovery output: %q, want %q", out.String(), want)
	}
}

func TestSyncConfigErrorDoesNotLeakValues(t *testing.T) {
	project, _ := cliEnvironment(t)
	cliWrite(t, cliPaths(project).Config, "version: 1\nsources:\n  - source: https://user:password@example.invalid/repo.git?token=topsecret\n    ref: [topsecret]\n")
	out, err := cliExecute("sync")
	if err == nil || out != "! sync Failed: could not complete synchronization\n" || err.Error() != "sync failed" {
		t.Fatalf("unsafe config error: %q, %v", out, err)
	}
}

func TestSyncSafeLabelsAndConflictReasons(t *testing.T) {
	for _, test := range []struct{ source, want string }{
		{"https://user:password@example.invalid/org/repo.git?token=secret#fragment", "repo"},
		{"ssh://user:password@example.invalid/org/repo.git", "repo"},
		{"git@example.invalid:org/repo.git", "repo"},
		{"git@example.invalid:repo.git", "repo"},
		{"/tmp/local-repo.git", "local-repo"},
		{"https://user:password@example.invalid?token=secret", "source 3"},
		{"https://example.invalid/%zz?token=secret", "source 3"},
		{"https://example.invalid/bad%0Aname.git?token=secret", "source 3"},
		{"https://example.invalid/..", "source 3"},
		{"/tmp/bad@name.git", "source 3"},
	} {
		if got := syncSourceLabel(test.source, 2); got != test.want {
			t.Errorf("safe label = %q, want %q", got, test.want)
		}
	}
	var out bytes.Buffer
	result := skillsync.Result{
		Status: skillsync.Skipped,
		Err:    errors.New("token=secret"),
		Source: skillsync.SourcePlan{
			Source: config.Source{Source: "https://user:password@example.invalid/repo.git?token=secret"},
			Conflicts: []skillsync.Conflict{
				{Kind: skillsync.UnsafeLocal, Name: "alpha", Reason: "token=secret"},
				{Kind: skillsync.UnsafeLocal, Name: "beta", Reason: "password"},
				{Kind: skillsync.CrossSourceName, Name: "https://user:password@example.invalid/repo?token=secret", Reason: "password"},
			},
		},
	}
	if err := printSyncResult(&out, result); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "! repo Skipped: alpha: unsafe local path; beta: unsafe local path; Skill name is claimed by multiple sources\n" {
		t.Fatalf("safe conflict output = %q", got)
	}
}
