package sync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zz-zhi54/skillscli/internal/config"
)

func setupDeployment(t *testing.T, sources ...config.Source) config.Paths {
	t.Helper()
	root := t.TempDir()
	paths, err := deploymentPaths(config.Paths{Config: filepath.Join(root, ".agents", "skillscli.yaml"), Skills: filepath.Join(root, ".agents", "skills")})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	doc.Config.Sources = sources
	if err := doc.Save(paths.Config); err != nil {
		t.Fatal(err)
	}
	return paths
}

func runDeployment(t *testing.T, paths config.Paths, force bool) []Result {
	t.Helper()
	results, err := Run(context.Background(), paths, Options{Force: force})
	if err != nil {
		t.Fatal(err)
	}
	requireClean(t, paths)
	return results
}

func requireClean(t *testing.T, paths config.Paths) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(paths.Config))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".skillscli-txn-") || strings.HasPrefix(entry.Name(), ".skillscli-cleanup-") || (strings.HasPrefix(entry.Name(), ".skillscli-") && strings.HasSuffix(entry.Name(), ".yaml")) {
			t.Errorf("temporary resource left: %s", entry.Name())
		}
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func loadDoc(t *testing.T, paths config.Paths) *config.Document {
	t.Helper()
	doc, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func assertInstalled(t *testing.T, paths config.Paths, sourceIndex int, names ...string) {
	t.Helper()
	lock := loadDoc(t, paths).Config.Sources[sourceIndex].Lock
	if lock == nil || len(lock.Skills) != len(names) {
		t.Fatalf("wrong lock: %#v", lock)
	}
	for _, name := range names {
		if got := hash(t, filepath.Join(paths.Skills, name)); got != lock.Skills[name] {
			t.Fatalf("installed %s = %s, lock %s", name, got, lock.Skills[name])
		}
	}
}

func TestDeploymentLifecycle(t *testing.T) {
	repo := newRepository(t, "idea", "idea-debug")
	paths := setupDeployment(t, config.Source{Source: repo.dir})
	// 测试用户手写注释/字段样式和省略 paths 的保存行为。
	originalYAML := "# user settings\nversion: 1\nsources:\n  - source: '" + repo.dir + "' # keep\n    ref: main # branch\n"
	writeFile(t, paths.Config, originalYAML)
	first := runDeployment(t, paths, false)
	if first[0].Status != Ready {
		t.Fatalf("first: %#v", first)
	}
	assertInstalled(t, paths, 0, "idea", "idea-debug")
	doc := loadDoc(t, paths)
	if doc.Config.Sources[0].Lock.Revision != repo.git("rev-parse", "HEAD") || doc.Config.Sources[0].Lock.Source != repo.dir {
		t.Fatal("lock does not record actual checkout")
	}
	if !bytes.Equal(readFile(t, filepath.Join(paths.Skills, "idea", "SKILL.md")), readFile(t, filepath.Join(repo.dir, "skills", "idea", "SKILL.md"))) {
		t.Fatal("Skill content changed")
	}
	yaml := readFile(t, paths.Config)
	if !bytes.Contains(yaml, []byte("# user settings")) || !bytes.Contains(yaml, []byte("# keep")) || !bytes.Contains(yaml, []byte("ref: main # branch")) || bytes.Contains(yaml, []byte("paths:")) {
		t.Fatalf("user YAML representation lost: %s", yaml)
	}
	yamlInfo, _ := os.Stat(paths.Config)
	skillInfo, _ := os.Stat(filepath.Join(paths.Skills, "idea", "SKILL.md"))
	second := runDeployment(t, paths, false)
	if second[0].Status != Unchanged {
		t.Fatalf("second: %#v", second)
	}
	yamlAfter, _ := os.Stat(paths.Config)
	skillAfter, _ := os.Stat(filepath.Join(paths.Skills, "idea", "SKILL.md"))
	if !bytes.Equal(yaml, readFile(t, paths.Config)) || !os.SameFile(yamlInfo, yamlAfter) || !yamlInfo.ModTime().Equal(yamlAfter.ModTime()) || !os.SameFile(skillInfo, skillAfter) || !skillInfo.ModTime().Equal(skillAfter.ModTime()) {
		t.Fatal("unchanged sync rewrote files")
	}
	writeFile(t, filepath.Join(repo.dir, "skills", "idea", "assets", "data.txt"), "upgraded")
	repo.skill("skills/idea-new")
	removeFixture(t, repo.dir, "skills/idea-debug")
	revision := repo.commit()
	third := runDeployment(t, paths, false)
	if third[0].Status != Ready {
		t.Fatalf("upgrade: %#v", third)
	}
	assertInstalled(t, paths, 0, "idea", "idea-new")
	if loadDoc(t, paths).Config.Sources[0].Lock.Revision != revision {
		t.Fatal("revision not updated")
	}
	if _, err := os.Stat(filepath.Join(paths.Skills, "idea-debug")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("upstream deletion not applied")
	}
	doc = loadDoc(t, paths)
	doc.Config.Sources[0].Exclude = []string{"idea-new"}
	if err := doc.Save(paths.Config); err != nil {
		t.Fatal(err)
	}
	runDeployment(t, paths, false)
	assertInstalled(t, paths, 0, "idea")
	if _, err := os.Stat(filepath.Join(paths.Skills, "idea-new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("exclude not removed")
	}
	removeFixture(t, paths.Skills, "idea")
	runDeployment(t, paths, false)
	assertInstalled(t, paths, 0, "idea")
	// 只推进元数据，保留完全相同的 Skill 文件。
	info, _ := os.Stat(filepath.Join(paths.Skills, "idea", "SKILL.md"))
	writeFile(t, filepath.Join(repo.dir, "README.md"), "metadata only")
	revision = repo.commit()
	runDeployment(t, paths, false)
	afterInfo, _ := os.Stat(filepath.Join(paths.Skills, "idea", "SKILL.md"))
	if !os.SameFile(info, afterInfo) || loadDoc(t, paths).Config.Sources[0].Lock.Revision != revision {
		t.Fatal("metadata-only plan changed Skill files")
	}
}

func TestDeploymentLocalProtection(t *testing.T) {
	for _, mode := range []string{"modified", "partial-missing", "unmanaged-directory", "unmanaged-file", "unsafe-symlink"} {
		t.Run(mode, func(t *testing.T) {
			repo := newRepository(t, "idea")
			paths := setupDeployment(t, config.Source{Source: repo.dir})
			runDeployment(t, paths, false)
			before := readFile(t, paths.Config)
			data := filepath.Join(paths.Skills, "idea", "assets", "data.txt")
			switch mode {
			case "modified":
				writeFile(t, data, "user edits")
			case "partial-missing":
				removeFixture(t, paths.Skills, "idea/assets/data.txt")
			case "unmanaged-directory", "unmanaged-file":
				repo.skill("skills/idea-new")
				repo.commit()
				if mode == "unmanaged-directory" {
					writeFile(t, filepath.Join(paths.Skills, "idea-new", "user.txt"), "do not overwrite")
				} else {
					writeFile(t, filepath.Join(paths.Skills, "idea-new"), "do not overwrite")
				}
			case "unsafe-symlink":
				removeFixture(t, paths.Skills, "idea/assets/data.txt")
				if err := os.Symlink(filepath.Join(repo.dir, "skills", "idea", "assets", "data.txt"), data); err != nil {
					t.Skip(err)
				}
			}
			results, err := Run(context.Background(), paths, Options{})
			if err == nil || len(results) != 1 || results[0].Status != Skipped {
				t.Fatalf("not protected: %#v %v", results, err)
			}
			if !bytes.Equal(before, readFile(t, paths.Config)) {
				t.Fatal("skipped source changed YAML")
			}
			requireClean(t, paths)
			results, err = Run(context.Background(), paths, Options{Force: true})
			if strings.HasPrefix(mode, "unmanaged") || mode == "unsafe-symlink" {
				if err == nil || results[0].Status != Skipped {
					t.Fatalf("force bypassed protection: %#v %v", results, err)
				}
				if !bytes.Equal(before, readFile(t, paths.Config)) {
					t.Fatal("force conflict changed YAML")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertInstalled(t, paths, 0, "idea")
				if string(readFile(t, data)) != "original" {
					t.Fatal("force did not restore content")
				}
			}
			requireClean(t, paths)
		})
	}
}

func changedTransaction(t *testing.T) (*repository, config.Paths, []byte, map[string]string) {
	t.Helper()
	repo := newRepository(t, "a", "z")
	paths := setupDeployment(t, config.Source{Source: repo.dir})
	runDeployment(t, paths, false)
	before := readFile(t, paths.Config)
	old := map[string]string{"a": hash(t, filepath.Join(paths.Skills, "a")), "z": hash(t, filepath.Join(paths.Skills, "z"))}
	writeFile(t, filepath.Join(repo.dir, "skills", "a", "assets", "data.txt"), "updated")
	repo.skill("skills/b")
	removeFixture(t, repo.dir, "skills/z")
	repo.commit()
	return repo, paths, before, old
}

func assertRestored(t *testing.T, paths config.Paths, before []byte, old map[string]string) {
	t.Helper()
	if !bytes.Equal(before, readFile(t, paths.Config)) {
		t.Fatal("rollback did not restore exact YAML")
	}
	for name, want := range old {
		if got := hash(t, filepath.Join(paths.Skills, name)); got != want {
			t.Fatalf("%s not restored: %s", name, got)
		}
	}
	if _, err := os.Lstat(filepath.Join(paths.Skills, "b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("added Skill remained after rollback")
	}
}

func TestDeploymentRollback(t *testing.T) {
	for _, stage := range []string{"prepare", "backup", "install", "save", "saved", "invalid-yaml"} {
		t.Run(stage, func(t *testing.T) {
			_, paths, before, old := changedTransaction(t)
			doc := loadDoc(t, paths)
			plan := build(t, paths.Skills, false, doc.Config.Sources...)
			injected := errors.New("injected deployment failure")
			hook := func(at, name string) error {
				if at == stage && ((at != "prepare" && at != "backup" && at != "install") || name == "b" || (at == "backup" && name == "z")) {
					return injected
				}
				if stage == "invalid-yaml" && at == "save" {
					doc.Config.Version = 99
				}
				return nil
			}
			results, err := execute(context.Background(), paths, doc, plan, before, hook)
			if err == nil || results[0].Status != Failed {
				t.Fatalf("failure not reported: %#v %v", results, err)
			}
			if stage != "invalid-yaml" && !errors.Is(err, injected) {
				t.Fatalf("lost failure: %v", err)
			}
			assertRestored(t, paths, before, old)
			requireClean(t, paths)
		})
	}
}

func TestStalePlanRejected(t *testing.T) {
	for _, change := range []string{"modified", "missing-replaced", "ownership", "config", "prepared-modified"} {
		t.Run(change, func(t *testing.T) {
			_, paths, before, _ := changedTransaction(t)
			doc := loadDoc(t, paths)
			plan := build(t, paths.Skills, true, doc.Config.Sources...)
			mutate := func() {
				switch change {
				case "modified", "prepared-modified":
					writeFile(t, filepath.Join(paths.Skills, "a", "assets", "data.txt"), "new user changes")
				case "missing-replaced":
					writeFile(t, filepath.Join(paths.Skills, "b", "user.txt"), "unmanaged appeared")
				case "ownership":
					delete(doc.Config.Sources[0].Lock.Skills, "a")
				case "config":
					writeFile(t, paths.Config, string(before)+"# new user comment\n")
				}
			}
			if change != "prepared-modified" {
				mutate()
			}
			hook := func(stage, name string) error {
				if stage == "prepared" && change == "prepared-modified" {
					mutate()
				}
				return nil
			}
			// 使用规划时读取的 YAML 基线，不能在部署时接受过期计划的新基线。
			err := deploySource(context.Background(), paths, doc, plan, plan.Sources[0], before, nil, hook)
			if err == nil {
				t.Fatal("stale plan deployed")
			}
			if change == "modified" || change == "prepared-modified" {
				if string(readFile(t, filepath.Join(paths.Skills, "a", "assets", "data.txt"))) != "new user changes" {
					t.Fatal("new changes overwritten")
				}
			}
			if change != "config" && !bytes.Equal(before, readFile(t, paths.Config)) {
				t.Fatal("stale plan rewrote YAML")
			}
			requireClean(t, paths)
		})
	}
}

func TestSourceFailureIsolation(t *testing.T) {
	a := newRepository(t, "a")
	b := newRepository(t, "b")
	paths := setupDeployment(t, config.Source{Source: a.dir}, config.Source{Source: b.dir})
	runDeployment(t, paths, false)
	oldLock := loadDoc(t, paths).Config.Sources[0].Lock
	writeFile(t, filepath.Join(a.dir, "skills", "a", "assets", "data.txt"), "a updated")
	a.commit()
	writeFile(t, filepath.Join(b.dir, "skills", "b", "assets", "data.txt"), "b updated")
	b.commit()
	doc := loadDoc(t, paths)
	plan := build(t, paths.Skills, false, doc.Config.Sources...)
	results, err := execute(context.Background(), paths, doc, plan, readFile(t, paths.Config), func(stage, name string) error {
		if stage == "install" && name == "a" {
			return errors.New("a install failed")
		}
		return nil
	})
	if err == nil || results[0].Status != Failed || results[1].Status != Ready {
		t.Fatalf("failure isolation: %#v %v", results, err)
	}
	saved := loadDoc(t, paths)
	if !reflect.DeepEqual(saved.Config.Sources[0].Lock, oldLock) || saved.Config.Sources[1].Lock.Revision != b.git("rev-parse", "HEAD") {
		t.Fatal("source locks not isolated")
	}
	assertInstalled(t, paths, 0, "a")
	assertInstalled(t, paths, 1, "b")
	requireClean(t, paths)
	// 后一来源失败时前一来源的成功必须保留。
	writeFile(t, filepath.Join(a.dir, "skills", "a", "assets", "data.txt"), "a updated again")
	a.commit()
	writeFile(t, filepath.Join(b.dir, "skills", "b", "assets", "data.txt"), "b updated again")
	b.commit()
	doc = loadDoc(t, paths)
	oldB := doc.Config.Sources[1].Lock
	plan = build(t, paths.Skills, false, doc.Config.Sources...)
	results, err = execute(context.Background(), paths, doc, plan, readFile(t, paths.Config), func(stage, name string) error {
		if stage == "install" && name == "b" {
			return errors.New("b install failed")
		}
		return nil
	})
	if err == nil || results[0].Status != Ready || results[1].Status != Failed {
		t.Fatal("later failure affected previous source")
	}
	saved = loadDoc(t, paths)
	if saved.Config.Sources[0].Lock.Revision != a.git("rev-parse", "HEAD") || !reflect.DeepEqual(saved.Config.Sources[1].Lock, oldB) {
		t.Fatal("previous successful lock was rolled back")
	}
	requireClean(t, paths)
}

func TestInterruptedTransactionRecovery(t *testing.T) {
	for _, stage := range []string{"prepared", "backup", "install", "saved"} {
		t.Run(stage, func(t *testing.T) {
			_, paths, before, old := changedTransaction(t)
			crashTransaction(t, paths, stage)
			lock, err := acquireScope(filepath.Dir(paths.Config))
			if err != nil {
				t.Fatal("process lock survived crash:", err)
			}
			if err := recoverTransactions(paths); err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			assertRestored(t, paths, before, old)
			requireClean(t, paths)
			runDeployment(t, paths, false)
			assertInstalled(t, paths, 0, "a", "b")
		})
	}
}

func crashTransaction(t *testing.T, paths config.Paths, stage string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTransactionCrashHelper$")
	temp := t.TempDir()
	cmd.Env = append(os.Environ(), "SKILLSCLI_CRASH_CONFIG="+paths.Config, "SKILLSCLI_CRASH_STAGE="+stage, "TMPDIR="+temp, "TMP="+temp, "TEMP="+temp)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 72 {
		t.Fatalf("crash helper: %v %s", err, out)
	}
	entries, err := os.ReadDir(filepath.Dir(paths.Config))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".skillscli-txn-") {
			return filepath.Join(filepath.Dir(paths.Config), entry.Name())
		}
	}
	t.Fatal("crash left no transaction")
	return ""
}

func TestTransactionCrashHelper(t *testing.T) {
	file := os.Getenv("SKILLSCLI_CRASH_CONFIG")
	if file == "" {
		return
	}
	paths := config.Paths{Config: file, Skills: filepath.Join(filepath.Dir(file), "skills")}
	lock, err := acquireScope(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	doc := loadDoc(t, paths)
	plan := build(t, paths.Skills, false, doc.Config.Sources...)
	_, err = execute(context.Background(), paths, doc, plan, readFile(t, paths.Config), func(stage, name string) error {
		if stage == os.Getenv("SKILLSCLI_CRASH_STAGE") && (stage != "backup" && stage != "install" || name == "z" || name == "b") {
			os.Exit(72)
		}
		return nil
	})
	t.Fatalf("did not crash: %v", err)
}

func TestScopeProcessExclusion(t *testing.T) {
	paths := setupDeployment(t)
	lock, err := acquireScope(filepath.Dir(paths.Config))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	other := setupDeployment(t)
	independent, err := acquireScope(filepath.Dir(other.Config))
	if err != nil {
		t.Fatal("independent scope blocked:", err)
	}
	independent.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestScopeLockHelper$")
	cmd.Env = append(os.Environ(), "SKILLSCLI_LOCK_SCOPE="+filepath.Dir(paths.Config))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock helper: %v %s", err, out)
	}
}

func TestScopeLockHelper(t *testing.T) {
	scope := os.Getenv("SKILLSCLI_LOCK_SCOPE")
	if scope == "" {
		return
	}
	lock, err := acquireScope(scope)
	if err == nil {
		lock.Close()
		t.Fatal("second process acquired scope lock")
	}
}
