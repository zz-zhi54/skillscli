package sync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zz-zhi54/skillscli/internal/config"
	"github.com/zz-zhi54/skillscli/internal/skills"
)

type repository struct {
	dir string
	t   *testing.T
}

func newRepository(t *testing.T, names ...string) *repository {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("tests require system Git:", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	r := &repository{dir: t.TempDir(), t: t}
	r.git("init", "--template=", "--initial-branch=main")
	r.git("config", "user.name", "Test")
	r.git("config", "user.email", "test@example.invalid")
	// 即使删除最后一个 Skill，发现路径本身仍存在。
	writeFile(t, filepath.Join(r.dir, "skills", "README.md"), "fixture")
	for _, name := range names {
		r.skill("skills/" + name)
	}
	r.commit()
	return r
}

func (r *repository) git(args ...string) string {
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
		r.t.Fatalf("fixture git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repository) skill(rel string) {
	r.t.Helper()
	dir := filepath.Join(r.dir, filepath.FromSlash(rel))
	writeFile(r.t, filepath.Join(dir, "SKILL.md"), "---\nname: "+filepath.Base(dir)+"\ndescription: Test skill.\n---\n# Instructions\n")
	writeFile(r.t, filepath.Join(dir, "assets", "data.txt"), "original")
}

func (r *repository) commit() string {
	r.t.Helper()
	r.git("add", ".")
	r.git("commit", "-m", "fixture")
	return r.git("rev-parse", "HEAD")
}

func writeFile(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func hash(t *testing.T, dir string) string {
	t.Helper()
	got, err := skills.Hash(dir)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (r *repository) lockedSource() config.Source {
	r.t.Helper()
	found, err := skills.Discover(r.dir, config.Source{})
	if err != nil {
		r.t.Fatal(err)
	}
	lock := &config.Lock{Source: r.dir, Revision: r.git("rev-parse", "HEAD"), Skills: make(map[string]string)}
	for _, skill := range found {
		lock.Skills[skill.Name] = skill.Hash
	}
	return config.Source{Source: r.dir, Lock: lock}
}

// 仅用于构造已安装测试数据，不是部署实现。
func installFixture(t *testing.T, r *repository, root string) {
	t.Helper()
	found, err := skills.Discover(r.dir, config.Source{})
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range found {
		target := filepath.Join(root, skill.Name)
		err := filepath.WalkDir(skill.Dir, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(skill.Dir, file)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.MkdirAll(filepath.Join(target, rel), 0700)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(target, rel), data, 0600)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func removeFixture(t *testing.T, root, rel string) {
	t.Helper()
	// 只删除明确位于本测试临时目录内的 fixture，不接受未检查的计算路径。
	absolute, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	base, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(absolute, base+string(filepath.Separator)) {
		t.Fatalf("unsafe fixture removal: %q", absolute)
	}
	if err := os.RemoveAll(absolute); err != nil {
		t.Fatal(err)
	}
}

func build(t *testing.T, root string, force bool, sources ...config.Source) *Plan {
	t.Helper()
	plan, err := Build(context.Background(), config.Config{Version: config.Version, Sources: sources}, root, Options{Force: force})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := plan.Close(); err != nil {
			t.Error(err)
		}
	})
	return plan
}

func requireActions(t *testing.T, sp SourcePlan, want map[string]ActionKind) {
	t.Helper()
	got := make(map[string]ActionKind)
	last := ""
	for _, action := range sp.Actions {
		if action.Name <= last {
			t.Fatalf("actions not sorted: %#v", sp.Actions)
		}
		last = action.Name
		got[action.Name] = action.Kind
		if !filepath.IsAbs(action.Dir) {
			t.Fatalf("non-absolute destination: %#v", action)
		}
		if action.TargetHash != "" {
			if action.FromDir == "" || action.Path == "" || hash(t, action.FromDir) != action.TargetHash {
				t.Fatalf("unusable target snapshot: %#v", action)
			}
		} else if action.FromDir != "" || action.Path != "" {
			t.Fatalf("absent target has a source path: %#v", action)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %v, want %v (status %s, err %v)", got, want, sp.Status, sp.Err)
	}
}

func requireStatus(t *testing.T, sp SourcePlan, status SourceStatus) {
	t.Helper()
	if sp.Status != status || sp.Err != nil {
		t.Fatalf("source status = %s, err = %v, want %s; conflicts %#v", sp.Status, sp.Err, status, sp.Conflicts)
	}
}

func TestFirstInstallAndUnmanagedCollision(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, existing := range []string{"absent", "identical", "empty-directory", "file", "symlink"} {
			t.Run(fmt.Sprintf("force=%t/%s", force, existing), func(t *testing.T) {
				r := newRepository(t, "alpha")
				root := filepath.Join(t.TempDir(), "not-created")
				switch existing {
				case "identical":
					installFixture(t, r, root)
				case "empty-directory":
					if err := os.MkdirAll(filepath.Join(root, "alpha"), 0700); err != nil {
						t.Fatal(err)
					}
				case "file":
					writeFile(t, filepath.Join(root, "alpha"), "not managed")
				case "symlink":
					if err := os.MkdirAll(root, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "alpha")); err != nil {
						t.Fatal(err)
					}
				}
				plan := build(t, root, force, config.Source{Source: r.dir})
				sp := plan.Sources[0]
				requireActions(t, sp, map[string]ActionKind{"alpha": Add})
				if existing == "absent" {
					requireStatus(t, sp, Ready)
					if _, err := os.Lstat(root); !os.IsNotExist(err) {
						t.Fatalf("planning created installation root: %v", err)
					}
					if len(plan.Conflicts) != 0 || sp.CurrentRevision != "" {
						t.Fatalf("unexpected initial state: %#v", sp)
					}
				} else {
					requireStatus(t, sp, Skipped)
					if len(plan.Conflicts) != 1 || plan.Conflicts[0].Kind != UnmanagedName || sp.Actions[0].Local != Unmanaged {
						t.Fatalf("unmanaged collision not reported: %#v", plan.Conflicts)
					}
				}
			})
		}
	}
}

func TestManagedLocalStateAndForce(t *testing.T) {
	for _, remoteUpdated := range []bool{false, true} {
		for _, force := range []bool{false, true} {
			for _, state := range []string{"unmodified", "changed", "missing", "partial", "missing-marker", "added-file"} {
				t.Run(fmt.Sprintf("remote=%t/force=%t/%s", remoteUpdated, force, state), func(t *testing.T) {
					r := newRepository(t, "alpha", "beta")
					source := r.lockedSource()
					root := t.TempDir()
					installFixture(t, r, root)
					switch state {
					case "changed":
						writeFile(t, filepath.Join(root, "alpha", "assets", "data.txt"), "local edit")
					case "missing":
						removeFixture(t, root, "alpha")
					case "partial":
						removeFixture(t, root, "alpha/assets/data.txt")
					case "missing-marker":
						removeFixture(t, root, "alpha/SKILL.md")
					case "added-file":
						writeFile(t, filepath.Join(root, "alpha", "local.txt"), "local")
					}
					if remoteUpdated {
						for _, name := range []string{"alpha", "beta"} {
							writeFile(t, filepath.Join(r.dir, "skills", name, "assets", "data.txt"), "upstream")
						}
						r.commit()
					}
					before := hash(t, root)
					plan := build(t, root, force, source)
					sp := plan.Sources[0]
					wantAlpha, wantBeta := Keep, Keep
					wantStatus := Unchanged
					if remoteUpdated {
						wantAlpha, wantBeta, wantStatus = Update, Update, Ready
					}
					modified := state != "unmodified" && state != "missing"
					if state == "missing" {
						wantAlpha, wantStatus = Add, Ready
					} else if modified {
						wantAlpha, wantStatus = Update, Ready
						if !force {
							wantStatus = Skipped
						}
					}
					requireStatus(t, sp, wantStatus)
					requireActions(t, sp, map[string]ActionKind{"alpha": wantAlpha, "beta": wantBeta})
					wantLocal := Unmodified
					if modified {
						wantLocal = Modified
					} else if state == "missing" {
						wantLocal = Missing
					}
					if sp.Actions[0].Local != wantLocal {
						t.Fatalf("local = %s, want %s", sp.Actions[0].Local, wantLocal)
					}
					if modified && !force {
						if len(sp.Conflicts) != 1 || sp.Conflicts[0].Kind != LocalModification {
							t.Fatalf("missing local conflict: %#v", sp.Conflicts)
						}
					} else if len(plan.Conflicts) != 0 {
						t.Fatalf("unexpected conflicts: %#v", plan.Conflicts)
					}
					if sp.CurrentRevision != source.Lock.Revision || sp.TargetRevision != r.git("rev-parse", "HEAD") {
						t.Fatalf("wrong revisions: %#v", sp)
					}
					if after := hash(t, root); after != before {
						t.Fatal("planning changed installed files")
					}
				})
			}
		}
	}
}

func TestUpstreamAddUpdateDeleteKeep(t *testing.T) {
	r := newRepository(t, "alpha", "beta", "delta")
	source := r.lockedSource()
	root := t.TempDir()
	installFixture(t, r, root)
	writeFile(t, filepath.Join(r.dir, "skills", "alpha", "assets", "data.txt"), "new")
	removeFixture(t, r.dir, "skills/beta")
	r.skill("skills/gamma")
	targetRevision := r.commit()
	plan := build(t, root, false, source)
	sp := plan.Sources[0]
	requireStatus(t, sp, Ready)
	requireActions(t, sp, map[string]ActionKind{"alpha": Update, "beta": Delete, "delta": Keep, "gamma": Add})
	if sp.TargetRevision != targetRevision || len(sp.TargetSkills) != 3 || len(plan.checkouts) != 1 {
		t.Fatalf("not a single complete source snapshot: %#v", sp)
	}
}

func TestConfigurationChangesRecomputeSelection(t *testing.T) {
	for _, change := range []string{"paths", "exclude", "ref", "fixed-commit", "source", "source-identical", "empty-selection", "missing-removed"} {
		t.Run(change, func(t *testing.T) {
			r := newRepository(t, "alpha", "beta")
			r.skill("extra/gamma")
			r.commit()
			source := r.lockedSource()
			root := t.TempDir()
			installFixture(t, r, root)
			want := map[string]ActionKind{"alpha": Keep, "beta": Keep}
			wantStatus := Ready
			wantRevision := source.Lock.Revision
			switch change {
			case "paths":
				source.Paths = []string{"extra"}
				want = map[string]ActionKind{"alpha": Delete, "beta": Delete, "gamma": Add}
			case "exclude":
				source.Exclude = []string{"alpha"}
				want["alpha"] = Delete
			case "empty-selection":
				source.Exclude = []string{"alpha", "beta"}
				want = map[string]ActionKind{"alpha": Delete, "beta": Delete}
			case "missing-removed":
				removeFixture(t, root, "alpha")
				source.Exclude = []string{"alpha"}
				// 不删除不存在的目录，但目标 lock 集合已改变。
				want["alpha"] = Keep
			case "ref":
				r.git("checkout", "-b", "next")
				writeFile(t, filepath.Join(r.dir, "skills", "alpha", "assets", "data.txt"), "next")
				wantRevision = r.commit()
				r.git("checkout", "main")
				source.Ref = "next"
				want["alpha"] = Update
			case "fixed-commit":
				source.Ref = source.Lock.Revision
				writeFile(t, filepath.Join(r.dir, "skills", "alpha", "assets", "data.txt"), "later")
				r.commit()
				wantStatus = Unchanged
			case "source":
				other := newRepository(t, "gamma")
				source.Source = other.dir
				wantRevision = other.git("rev-parse", "HEAD")
				want = map[string]ActionKind{"alpha": Delete, "beta": Delete, "gamma": Add}
			case "source-identical":
				// 相同 commit 和文件，来源文本变化仍要维护未来的 lock.source。
				source.Source = "file://" + filepath.ToSlash(r.dir)
			}
			plan := build(t, root, false, source)
			sp := plan.Sources[0]
			requireStatus(t, sp, wantStatus)
			requireActions(t, sp, want)
			if sp.TargetRevision != wantRevision || sp.SourceChanged != strings.HasPrefix(change, "source") {
				t.Fatalf("configuration/revision change not reflected: %#v", sp)
			}
		})
	}
}

func TestNewDeviceUsesCurrentSourceRefNotOldLock(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprint(collision), func(t *testing.T) {
			old := newRepository(t, "alpha", "removed")
			source := old.lockedSource()
			current := newRepository(t, "alpha", "new-skill")
			source.Source = current.dir
			// 旧 revision 语法合法但根本不可获取；不能尝试恢复它。
			source.Lock.Revision = strings.Repeat("a", 40)
			root := filepath.Join(t.TempDir(), "skills")
			if collision {
				writeFile(t, filepath.Join(root, "new-skill", "notes.txt"), "unmanaged")
			}
			plan := build(t, root, true, source)
			sp := plan.Sources[0]
			status := Ready
			if collision {
				status = Skipped
			}
			requireStatus(t, sp, status)
			requireActions(t, sp, map[string]ActionKind{"alpha": Add, "new-skill": Add, "removed": Keep})
			if sp.TargetRevision != current.git("rev-parse", "HEAD") {
				t.Fatal("did not use current source/ref")
			}
			for _, conflict := range plan.Conflicts {
				if conflict.Kind != UnmanagedName {
					t.Fatalf("missing installations treated as modifications: %#v", conflict)
				}
			}
		})
	}
}

func TestMissingDiscoveryDirectoriesAreEmptySyncSelections(t *testing.T) {
	for _, scenario := range []string{"last-upstream-skill-deleted", "paths-to-missing", "single-skill-path-deleted", "one-of-multiple-paths-missing"} {
		t.Run(scenario, func(t *testing.T) {
			r := newRepository(t, "alpha")
			source := r.lockedSource()
			root := t.TempDir()
			installFixture(t, r, root)
			want := map[string]ActionKind{"alpha": Delete}
			switch scenario {
			case "last-upstream-skill-deleted":
				removeFixture(t, r.dir, "skills")
				r.commit()
			case "paths-to-missing":
				source.Paths = []string{"absent/subdirectory"}
			case "single-skill-path-deleted":
				source.Paths = []string{"skills/alpha"}
				removeFixture(t, r.dir, "skills/alpha")
				r.commit()
			case "one-of-multiple-paths-missing":
				source.Paths = []string{"missing", "skills"}
				want["alpha"] = Keep
			}
			plan := build(t, root, false, source)
			status := Ready
			if scenario == "one-of-multiple-paths-missing" {
				status = Unchanged
			}
			requireStatus(t, plan.Sources[0], status)
			requireActions(t, plan.Sources[0], want)
		})
	}
}

func TestLoadNewDeviceWithOnlyYAMLAndOldLock(t *testing.T) {
	r := newRepository(t, "alpha")
	source := r.lockedSource()
	writeFile(t, filepath.Join(r.dir, "skills", "alpha", "assets", "data.txt"), "current upstream")
	target := r.commit()
	project := t.TempDir()
	paths := config.Paths{Config: filepath.Join(project, "skillscli.yaml"), Skills: filepath.Join(project, "skills")}
	original := fmt.Sprintf("version: 1\nsources:\n  - source: %q\n    lock:\n      source: %q\n      revision: '%s'\n      skills:\n        alpha: %q\n", filepath.ToSlash(r.dir), filepath.ToSlash(r.dir), strings.Repeat("b", 40), source.Lock.Skills["alpha"])
	writeFile(t, paths.Config, original)
	before := hash(t, project)
	plan, err := Load(context.Background(), paths, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := plan.Close(); err != nil {
			t.Error(err)
		}
	})
	requireStatus(t, plan.Sources[0], Ready)
	requireActions(t, plan.Sources[0], map[string]ActionKind{"alpha": Add})
	if plan.Sources[0].TargetRevision != target || len(plan.Conflicts) != 0 {
		t.Fatalf("new device did not use current target: %#v", plan.Sources[0])
	}
	if hash(t, project) != before {
		t.Fatal("new-device planning modified YAML or created installation directory")
	}
}

func TestModifiedRemovalOrSourceChangeSkipsWholeSource(t *testing.T) {
	for _, change := range []string{"removed", "exclude", "source"} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%t", change, force), func(t *testing.T) {
				r := newRepository(t, "alpha", "beta")
				source := r.lockedSource()
				root := t.TempDir()
				installFixture(t, r, root)
				writeFile(t, filepath.Join(root, "alpha", "local.txt"), "local")
				want := map[string]ActionKind{"alpha": Delete, "beta": Keep}
				switch change {
				case "removed":
					removeFixture(t, r.dir, "skills/alpha")
					r.skill("skills/gamma")
					r.commit()
					want["gamma"] = Add
				case "exclude":
					source.Exclude = []string{"alpha"}
				case "source":
					other := newRepository(t, "gamma")
					source.Source = other.dir
					want["beta"], want["gamma"] = Delete, Add
				}
				plan := build(t, root, force, source)
				sp := plan.Sources[0]
				status := Skipped
				if force {
					status = Ready
				}
				requireStatus(t, sp, status)
				requireActions(t, sp, want)
			})
		}
	}
}

func TestSourceConflictDoesNotBlockOtherSources(t *testing.T) {
	r := newRepository(t, "alpha", "beta")
	source := r.lockedSource()
	root := t.TempDir()
	installFixture(t, r, root)
	writeFile(t, filepath.Join(root, "alpha", "local.txt"), "edit")
	writeFile(t, filepath.Join(r.dir, "skills", "beta", "assets", "data.txt"), "update")
	r.skill("skills/gamma")
	r.commit()
	other := newRepository(t, "delta")
	plan := build(t, root, false, source, config.Source{Source: other.dir})
	requireStatus(t, plan.Sources[0], Skipped)
	requireActions(t, plan.Sources[0], map[string]ActionKind{"alpha": Update, "beta": Update, "gamma": Add})
	requireStatus(t, plan.Sources[1], Ready)
	requireActions(t, plan.Sources[1], map[string]ActionKind{"delta": Add})
}

func TestCrossSourceNamesAndStableOutput(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			first := newRepository(t, "shared", "zeta", "alpha")
			second := newRepository(t, "shared", "alpha")
			third := newRepository(t, "shared")
			independent := newRepository(t, "delta")
			sources := []config.Source{{Source: first.dir}, {Source: second.dir}, {Source: third.dir}, {Source: independent.dir}}
			root := t.TempDir()
			plan := build(t, root, force, sources...)
			for i := 0; i < 3; i++ {
				requireStatus(t, plan.Sources[i], Skipped)
			}
			requireStatus(t, plan.Sources[3], Ready)
			want := []Conflict{
				{Kind: CrossSourceName, Name: "alpha", SourceIndexes: []int{0, 1}, Reason: "Skill name is claimed by multiple sources (lock or target)"},
				{Kind: CrossSourceName, Name: "shared", SourceIndexes: []int{0, 1, 2}, Reason: "Skill name is claimed by multiple sources (lock or target)"},
			}
			if !reflect.DeepEqual(plan.Conflicts, want) {
				t.Fatalf("conflicts = %#v, want %#v", plan.Conflicts, want)
			}
			secondPlan := build(t, root, force, sources...)
			// 临时目录名本来就唯一；排除这些运行期路径，比较全部决策与排序。
			normalize := func(p *Plan) []SourcePlan {
				result := make([]SourcePlan, len(p.Sources))
				for i, sp := range p.Sources {
					sp.Actions = append([]SkillAction(nil), sp.Actions...)
					sp.TargetSkills = append([]skills.Skill(nil), sp.TargetSkills...)
					for j := range sp.Actions {
						sp.Actions[j].FromDir = ""
					}
					for j := range sp.TargetSkills {
						sp.TargetSkills[j].Dir = ""
					}
					result[i] = sp
				}
				return result
			}
			if !reflect.DeepEqual(normalize(plan), normalize(secondPlan)) || !reflect.DeepEqual(plan.Conflicts, secondPlan.Conflicts) {
				t.Fatal("planning decisions depend on map traversal")
			}
		})
	}
}

func TestOldOwnershipAlsoBlocksCrossSourceTakeover(t *testing.T) {
	for _, scenario := range []string{"deleting", "fetch-failed", "duplicate-lock"} {
		t.Run(scenario, func(t *testing.T) {
			old := newRepository(t, "shared")
			oldSource := old.lockedSource()
			incoming := newRepository(t, "shared")
			incomingSource := config.Source{Source: incoming.dir}
			switch scenario {
			case "deleting":
				oldSource.Exclude = []string{"shared"}
			case "fetch-failed":
				oldSource.Ref = "does-not-exist"
			case "duplicate-lock":
				incomingSource = incoming.lockedSource()
			}
			// 本地完全缺失也不能在不同来源之间自动移交旧所有权。
			plan := build(t, t.TempDir(), true, oldSource, incomingSource)
			if scenario == "fetch-failed" {
				if plan.Sources[0].Status != Failed || plan.Sources[0].Err == nil {
					t.Fatal("expected isolated Git failure")
				}
			} else {
				requireStatus(t, plan.Sources[0], Skipped)
			}
			requireStatus(t, plan.Sources[1], Skipped)
			if len(plan.Conflicts) != 1 || plan.Conflicts[0].Kind != CrossSourceName {
				t.Fatalf("old ownership not protected: %#v", plan.Conflicts)
			}
		})
	}
}

func TestSameRevisionChecksChangedLocalStateEveryTime(t *testing.T) {
	r := newRepository(t, "alpha")
	source := r.lockedSource()
	root := t.TempDir()
	installFixture(t, r, root)
	requireStatus(t, build(t, root, false, source).Sources[0], Unchanged)
	writeFile(t, filepath.Join(root, "alpha", "assets", "data.txt"), "edit")
	requireStatus(t, build(t, root, false, source).Sources[0], Skipped)
	requireStatus(t, build(t, root, true, source).Sources[0], Ready)
	removeFixture(t, root, "alpha")
	plan := build(t, root, false, source)
	requireStatus(t, plan.Sources[0], Ready)
	requireActions(t, plan.Sources[0], map[string]ActionKind{"alpha": Add})
}

func TestRevisionOnlyChangeRequiresFutureLockMaintenance(t *testing.T) {
	r := newRepository(t, "alpha")
	source := r.lockedSource()
	root := t.TempDir()
	installFixture(t, r, root)
	writeFile(t, filepath.Join(r.dir, "README.md"), "outside skill")
	r.commit()
	plan := build(t, root, false, source)
	requireStatus(t, plan.Sources[0], Ready)
	requireActions(t, plan.Sources[0], map[string]ActionKind{"alpha": Keep})
}

func TestUnsafeManagedPathsCannotBeForced(t *testing.T) {
	for _, kind := range []string{"file", "directory-link", "nested-link"} {
		t.Run(kind, func(t *testing.T) {
			r := newRepository(t, "alpha")
			source := r.lockedSource()
			root := t.TempDir()
			installFixture(t, r, root)
			outside := t.TempDir()
			writeFile(t, filepath.Join(outside, "sentinel"), "untouched")
			before := hash(t, outside)
			switch kind {
			case "file":
				removeFixture(t, root, "alpha")
				writeFile(t, filepath.Join(root, "alpha"), "not a directory")
			case "directory-link":
				removeFixture(t, root, "alpha")
				if err := os.Symlink(outside, filepath.Join(root, "alpha")); err != nil {
					t.Fatal(err)
				}
			case "nested-link":
				if err := os.Symlink(filepath.Join(outside, "sentinel"), filepath.Join(root, "alpha", "link")); err != nil {
					t.Fatal(err)
				}
			}
			plan := build(t, root, true, source)
			requireStatus(t, plan.Sources[0], Skipped)
			if len(plan.Conflicts) != 1 || plan.Conflicts[0].Kind != UnsafeLocal || plan.Sources[0].Actions[0].Local != Unsafe {
				t.Fatalf("unsafe tree bypassed with force: %#v", plan.Conflicts)
			}
			if hash(t, outside) != before {
				t.Fatal("planning changed symlink target")
			}
		})
	}
}

func TestInvalidLockNamesDoNotEscapeInstallationRoot(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "..", ".", ".git", `a\b`, "C:drive", "a\x00b"} {
		t.Run(name, func(t *testing.T) {
			r := newRepository(t, "alpha")
			source := r.lockedSource()
			source.Lock.Skills = map[string]string{name: source.Lock.Skills["alpha"]}
			plan := build(t, t.TempDir(), true, source)
			if plan.Sources[0].Status != Failed || plan.Sources[0].Err == nil || len(plan.checkouts) != 0 {
				t.Fatalf("invalid lock path used: %#v", plan.Sources[0])
			}
		})
	}
}

func TestLoadIsReadOnlyAndClosesAllTemporaryCheckouts(t *testing.T) {
	r := newRepository(t, "alpha", "beta")
	project := t.TempDir()
	paths := config.Paths{Config: filepath.Join(project, ".agents", "skillscli.yaml"), Skills: filepath.Join(project, ".agents", "skills")}
	installFixture(t, r, paths.Skills)
	source := r.lockedSource()
	original := fmt.Sprintf("# preserve this YAML exactly\nversion: 1\nsources:\n  - source: %q\n    lock:\n      source: %q\n      revision: '%s'\n      skills:\n        beta: %q\n        alpha: %q\n", filepath.ToSlash(r.dir), filepath.ToSlash(r.dir), source.Lock.Revision, source.Lock.Skills["beta"], source.Lock.Skills["alpha"])
	writeFile(t, paths.Config, original)
	before := hash(t, project)
	// 将 Git 临时资源放到独立系统临时目录，明确验证本层生命周期。
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	plan, err := Load(context.Background(), paths, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := plan.Close(); err != nil {
			t.Error(err)
		}
	})
	requireStatus(t, plan.Sources[0], Unchanged)
	if hash(t, project) != before {
		t.Fatal("planning modified installed files or YAML")
	}
	if len(plan.checkouts) != 1 {
		t.Fatal("expected one source checkout")
	}
	checkoutPath := plan.checkouts[0].Dir
	if err := plan.Close(); err != nil {
		t.Fatal(err)
	}
	if err := plan.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
	if _, err := os.Lstat(checkoutPath); !os.IsNotExist(err) {
		t.Fatalf("checkout remains after Close: %v", err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary resources leaked: %v, %v", entries, err)
	}
	if hash(t, project) != before {
		t.Fatal("Close modified project")
	}
}

func TestSourceFailuresAreIsolatedAndCleaned(t *testing.T) {
	for _, failure := range []string{"fetch", "discover", "duplicate-name"} {
		t.Run(failure, func(t *testing.T) {
			r := newRepository(t, "alpha")
			bad := config.Source{Source: r.dir}
			switch failure {
			case "fetch":
				bad.Ref = "not-a-ref"
			case "discover":
				bad.Paths = []string{"../outside"}
			case "duplicate-name":
				r.skill("other/alpha")
				r.commit()
				bad.Paths = []string{"skills", "other"}
			}
			other := newRepository(t, "beta")
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			t.Setenv("TMP", tmp)
			t.Setenv("TEMP", tmp)
			plan := build(t, t.TempDir(), false, bad, config.Source{Source: other.dir})
			if plan.Sources[0].Status != Failed || plan.Sources[0].Err == nil || len(plan.Sources[0].Actions) != 0 {
				t.Fatalf("expected source failure without partial actions: %#v", plan.Sources[0])
			}
			requireStatus(t, plan.Sources[1], Ready)
			if err := plan.Close(); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(tmp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failure leaked checkouts: %v, %v", entries, err)
			}
		})
	}
}

func TestBuildDoesNotMutateOrAliasConfiguration(t *testing.T) {
	r := newRepository(t, "alpha", "beta")
	source := r.lockedSource()
	source.Paths = []string{"skills"}
	source.Exclude = []string{"beta"}
	before := cloneSource(source)
	cfg := config.Config{Version: config.Version, Sources: []config.Source{source}}
	plan, err := Build(context.Background(), cfg, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := plan.Close(); err != nil {
			t.Error(err)
		}
	})
	if !reflect.DeepEqual(cfg.Sources[0], before) {
		t.Fatal("Build mutated input config")
	}
	plan.Sources[0].Source.Paths[0] = "changed"
	plan.Sources[0].Source.Exclude[0] = "changed"
	plan.Sources[0].Source.Lock.Skills["alpha"] = "changed"
	if !reflect.DeepEqual(cfg.Sources[0], before) {
		t.Fatal("plan aliases mutable config input")
	}
}

func TestEmptyConfigInvalidInputAndCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	plan := build(t, root, false)
	if len(plan.Sources) != 0 || len(plan.Conflicts) != 0 {
		t.Fatalf("nonempty plan for empty config: %#v", plan)
	}
	paths := config.Paths{Config: filepath.Join(t.TempDir(), "missing.yaml"), Skills: root}
	loaded, err := Load(context.Background(), paths, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("empty planning created root: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := Build(ctx, config.Config{Version: config.Version}, root, Options{}); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %#v, %v", got, err)
	}
	if got, err := Build(context.Background(), config.Config{}, root, Options{}); got != nil || err == nil {
		t.Fatalf("invalid config = %#v, %v", got, err)
	}
	writeFile(t, paths.Config, "not: valid config")
	if got, err := Load(context.Background(), paths, Options{}); got != nil || err == nil {
		t.Fatalf("invalid YAML = %#v, %v", got, err)
	}
	writeFile(t, root, "not a directory")
	if got, err := Build(context.Background(), config.Config{Version: config.Version}, root, Options{}); got != nil || err == nil {
		t.Fatalf("file installation root accepted: %#v, %v", got, err)
	}
	link := filepath.Join(t.TempDir(), "skills")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if got, err := Build(context.Background(), config.Config{Version: config.Version}, link, Options{}); got != nil || err == nil {
		t.Fatalf("symlink installation root accepted: %#v, %v", got, err)
	}
}
