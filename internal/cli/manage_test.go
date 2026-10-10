package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zz-zhi54/skillscli/internal/config"
)

func cliOK(t *testing.T, args ...string) string {
	t.Helper()
	out, err := cliExecute(args...)
	if err != nil {
		t.Fatalf("%v: %v, output %q", args, err, out)
	}
	t.Logf("%s => %s", strings.Join(args, " "), strings.TrimSpace(out))
	return out
}

func cliReject(t *testing.T, want string, args ...string) {
	t.Helper()
	out, err := cliExecute(args...)
	if err == nil || !strings.Contains(out+err.Error(), want) {
		t.Fatalf("%v: output %q, error %v; want failure %q", args, out, err, want)
	}
}

func cliDeleteFixture(t *testing.T, root, rel string) {
	t.Helper()
	base, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, rel)
	if !strings.HasPrefix(target, base+string(filepath.Separator)) {
		t.Fatalf("unsafe fixture deletion: %s", target)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndLifecycle(t *testing.T) {
	project, home := cliEnvironment(t)
	r := cliRepo(t, "upstream", "alpha", "beta", "obsolete")
	paths := cliPaths(project)
	cliOK(t, "add", r.dir)
	source := cliLoad(t, project).Config.Sources[0]
	if !reflect.DeepEqual(source.Paths, []string{"skills"}) || source.Lock.Source != r.dir || source.Lock.Revision != r.git("rev-parse", "HEAD") || len(source.Lock.Skills) != 3 {
		t.Fatalf("initial source: %#v", source)
	}
	if cliRead(t, filepath.Join(paths.Skills, "alpha", "SKILL.md")) != cliRead(t, filepath.Join(r.dir, "skills", "alpha", "SKILL.md")) {
		t.Fatal("SKILL.md was rewritten")
	}
	before := cliRead(t, paths.Config)
	cliReject(t, "source already exists", "add", r.dir)
	if cliRead(t, paths.Config) != before {
		t.Fatal("duplicate add rewrote YAML")
	}
	if out := cliOK(t, "list"); out != "alpha\tok\tsource 1 (upstream)\nbeta\tok\tsource 1 (upstream)\nobsolete\tok\tsource 1 (upstream)\n" {
		t.Fatalf("list order/status: %q", out)
	}
	cliWrite(t, filepath.Join(r.dir, "skills", "alpha", "data.txt"), "upstream v2")
	r.skill("newcomer")
	cliDeleteFixture(t, r.dir, "skills/obsolete")
	revision := r.commit()
	cliOK(t, "sync")
	cliAbsent(t, filepath.Join(paths.Skills, "obsolete"))
	if cliRead(t, filepath.Join(paths.Skills, "alpha", "data.txt")) != "upstream v2" || cliLoad(t, project).Config.Sources[0].Lock.Revision != revision {
		t.Fatal("upstream update not applied")
	}
	before = cliRead(t, paths.Config)
	cliWrite(t, filepath.Join(paths.Skills, "alpha", "data.txt"), "local edits")
	cliWrite(t, filepath.Join(r.dir, "skills", "beta", "data.txt"), "beta v2")
	r.commit()
	cliReject(t, "local modifications", "sync")
	if cliRead(t, paths.Config) != before || cliRead(t, filepath.Join(paths.Skills, "alpha", "data.txt")) != "local edits" || cliRead(t, filepath.Join(paths.Skills, "beta", "data.txt")) != "upstream" {
		t.Fatal("source protection was not atomic")
	}
	cliDeleteFixture(t, paths.Skills, "newcomer")
	before = cliRead(t, paths.Config)
	t.Run("list is offline and read-only", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		out := cliOK(t, "list")
		if out != "alpha\tmodified\tsource 1 (upstream)\nbeta\tok\tsource 1 (upstream)\nnewcomer\tmissing\tsource 1 (upstream)\n" || cliRead(t, paths.Config) != before {
			t.Fatalf("list statuses or YAML changed: %q", out)
		}
		cliAbsent(t, filepath.Join(paths.Skills, "newcomer"))
		if cliRead(t, filepath.Join(paths.Skills, "alpha", "data.txt")) != "local edits" {
			t.Fatal("list changed files")
		}
	})
	cliOK(t, "sync", "--force")
	if cliRead(t, filepath.Join(paths.Skills, "alpha", "data.txt")) != "upstream v2" || cliRead(t, filepath.Join(paths.Skills, "beta", "data.txt")) != "beta v2" {
		t.Fatal("force did not restore managed contents")
	}
	before = cliRead(t, paths.Config)
	for i := 0; i < 2; i++ {
		if out := cliOK(t, "sync"); out != "- upstream No changes\n" || cliRead(t, paths.Config) != before {
			t.Fatal("sync is not idempotent")
		}
	}
	cliWrite(t, filepath.Join(paths.Skills, "beta", "data.txt"), "beta local edits")
	t.Run("remove only selected Skill offline", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		cliOK(t, "remove", "alpha")
	})
	source = cliLoad(t, project).Config.Sources[0]
	if !reflect.DeepEqual(source.Exclude, []string{"alpha"}) || len(source.Lock.Skills) != 2 || cliRead(t, filepath.Join(paths.Skills, "beta", "data.txt")) != "beta local edits" {
		t.Fatalf("remove touched siblings or failed exclude: %#v", source)
	}
	cliAbsent(t, filepath.Join(paths.Skills, "alpha"))
	cliOK(t, "sync", "--force")
	cliAbsent(t, filepath.Join(paths.Skills, "alpha"))
	cliWrite(t, filepath.Join(paths.Skills, "beta", "data.txt"), "beta modified again")
	before = cliRead(t, paths.Config)
	cliReject(t, "local modifications", "remove", "beta")
	if cliRead(t, paths.Config) != before || cliRead(t, filepath.Join(paths.Skills, "beta", "data.txt")) != "beta modified again" {
		t.Fatal("refused remove changed state")
	}
	cliOK(t, "remove", "beta", "--force")
	cliOK(t, "remove", "newcomer")
	if len(cliLoad(t, project).Config.Sources) != 0 {
		t.Fatal("last remove retained source")
	}
	if out := cliOK(t, "list"); out != "No managed Skills.\n" {
		t.Fatal(out)
	}
	cliOK(t, "sync")
	cliAbsent(t, filepath.Join(paths.Skills, "newcomer"))
	cliAbsent(t, filepath.Join(home, ".agents"))
}

func TestAddPathsAndScopes(t *testing.T) {
	project, home := cliEnvironment(t)
	r := cliRepo(t, "paths", "alpha", "beta", "unused")
	cliWrite(t, filepath.Join(r.dir, "custom", "gamma", "SKILL.md"), "---\nname: gamma\ndescription: Custom path.\n---\n")
	r.commit()
	cliOK(t, "add", r.dir, "--path", "skills/alpha", "--path", "custom")
	p := cliLoad(t, project).Config.Sources[0]
	if !reflect.DeepEqual(p.Paths, []string{"skills/alpha", "custom"}) || len(p.Lock.Skills) != 2 || p.Lock.Skills["gamma"] == "" {
		t.Fatalf("custom paths: %#v", p)
	}
	cliAbsent(t, filepath.Join(cliPaths(project).Skills, "beta"))
	projectConfig := cliRead(t, cliPaths(project).Config)
	cliOK(t, "add", r.dir, "-g")
	if len(cliLoad(t, home).Config.Sources[0].Lock.Skills) != 3 {
		t.Fatal("global add used project selection")
	}
	cliOK(t, "remove", "alpha", "-g")
	if cliRead(t, cliPaths(project).Config) != projectConfig || cliRead(t, filepath.Join(cliPaths(project).Skills, "alpha", "data.txt")) != "upstream" {
		t.Fatal("global remove affected project")
	}
	if out := cliOK(t, "list", "-g"); strings.Contains(out, "alpha") || !strings.Contains(out, "beta\tok") {
		t.Fatal(out)
	}
}

func TestAddFailuresAndUnmanagedProtection(t *testing.T) {
	for _, scenario := range []string{"conflict", "unmanaged", "invalid-path", "no-skills", "invalid-skill", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			project, _ := cliEnvironment(t)
			a := cliRepo(t, "first", "alpha")
			b := cliRepo(t, "second", "alpha", "beta")
			paths := cliPaths(project)
			args := []string{"add", b.dir}
			want := "add failed"
			switch scenario {
			case "conflict":
				cliOK(t, "add", a.dir)
				cliDeleteFixture(t, paths.Skills, "alpha") // 即使缺失仍由旧 lock 占有。
				want = "multiple sources"
			case "unmanaged":
				cliWrite(t, filepath.Join(paths.Skills, "alpha", "user.txt"), "unmanaged")
				want = "unmanaged target exists"
			case "invalid-path":
				args = append(args, "--path", "../skills")
			case "no-skills":
				args = append(args, "--path", "not-present")
				want = "no Skills found"
			case "invalid-skill":
				cliWrite(t, filepath.Join(b.dir, "skills", "beta", "SKILL.md"), "invalid frontmatter")
				b.commit()
			case "unavailable":
				args[1] = filepath.Join(project, "missing-repo")
			}
			before := ""
			if scenario == "conflict" {
				before = cliRead(t, paths.Config)
			}
			cliReject(t, want, args...)
			cliAbsent(t, filepath.Join(paths.Skills, "beta"))
			if before == "" {
				cliAbsent(t, paths.Config)
			} else if cliRead(t, paths.Config) != before {
				t.Fatal("failed add rewrote YAML")
			}
			if scenario == "unmanaged" {
				cliReject(t, "not managed", "remove", "alpha", "--force")
				if cliRead(t, filepath.Join(paths.Skills, "alpha", "user.txt")) != "unmanaged" {
					t.Fatal("unmanaged object overwritten/deleted")
				}
			}
		})
	}
}

func TestEmptyListDoesNotCreateScope(t *testing.T) {
	project, home := cliEnvironment(t)
	for _, args := range [][]string{{"list"}, {"list", "-g"}} {
		if out := cliOK(t, args...); out != "No managed Skills.\n" {
			t.Fatal(out)
		}
	}
	cliAbsent(t, filepath.Join(project, ".agents"))
	cliAbsent(t, filepath.Join(home, ".agents"))
}

func TestEndToEndConfiguredRefs(t *testing.T) {
	for _, refKind := range []string{"branch", "tag", "sha"} {
		t.Run(refKind, func(t *testing.T) {
			project, _ := cliEnvironment(t)
			r := cliRepo(t, "refs", "alpha")
			first := r.git("rev-parse", "HEAD")
			r.git("tag", "-a", "v1", "-m", "version 1")
			cliWrite(t, filepath.Join(r.dir, "skills", "alpha", "data.txt"), "second")
			second := r.commit()
			r.git("branch", "selected", first)
			ref := map[string]string{"branch": "selected", "tag": "v1", "sha": first}[refKind]
			cliConfig(t, project, config.Source{Source: r.dir, Ref: ref})
			cliOK(t, "sync")
			if cliRead(t, filepath.Join(cliPaths(project).Skills, "alpha", "data.txt")) != "upstream" || cliLoad(t, project).Config.Sources[0].Lock.Revision != first {
				t.Fatal("configured ref did not select first commit")
			}
			if refKind == "branch" {
				r.git("branch", "-f", "selected", second)
			}
			cliOK(t, "sync")
			want := first
			if refKind == "branch" {
				want = second
			}
			if cliLoad(t, project).Config.Sources[0].Lock.Revision != want {
				t.Fatal("branch did not track or fixed ref moved")
			}
		})
	}
}

func TestManagementArgumentsAndSafeErrors(t *testing.T) {
	project, _ := cliEnvironment(t)
	for _, args := range [][]string{{"add"}, {"add", "one", "two"}, {"add", "repo", "--force"}, {"remove"}, {"remove", "one", "two"}, {"list", "extra"}, {"list", "--force"}, {"sync", "--upgrade"}} {
		if _, err := cliExecute(args...); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	for _, name := range []string{"add", "remove", "list"} {
		cliOK(t, name, "--help")
	}
	cliAbsent(t, filepath.Join(project, ".agents"))
	secretURL := "https://user:password@example.invalid/repo.git?token=secret"
	out, err := cliExecute("add", secretURL)
	if err == nil || strings.Contains(out+err.Error(), "password") || strings.Contains(out+err.Error(), "token=secret") {
		t.Fatalf("unsafe add diagnostics: %q, %v", out, err)
	}
}
