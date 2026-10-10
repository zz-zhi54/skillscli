package sync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zz-zhi54/skillscli/internal/config"
)

func TestAddTransactionRollback(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, stage := range []string{"prepare", "install", "save", "saved"} {
			t.Run(stage+map[bool]string{false: "-first", true: "-existing"}[existing], func(t *testing.T) {
				a := newRepository(t, "a", "b")
				root := t.TempDir()
				paths := config.Paths{Config: filepath.Join(root, ".agents", "skillscli.yaml"), Skills: filepath.Join(root, ".agents", "skills")}
				var before []byte
				if existing {
					other := newRepository(t, "other")
					if _, err := AddSource(context.Background(), paths, other.dir, nil); err != nil {
						t.Fatal(err)
					}
					before = readFile(t, paths.Config)
					writeFile(t, filepath.Join(paths.Skills, "other", "assets", "data.txt"), "keep local edits")
				}
				injected := errors.New("injected add transaction failure")
				result, err := addSource(context.Background(), paths, a.dir, nil, func(at, name string) error {
					if at == stage && (at != "prepare" && at != "install" || name == "b") {
						return injected
					}
					return nil
				})
				if !errors.Is(err, injected) || result.Status != Failed {
					t.Fatalf("add error lost: %#v %v", result, err)
				}
				for _, name := range []string{"a", "b"} {
					if _, err := os.Lstat(filepath.Join(paths.Skills, name)); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("half installation left:", name, err)
					}
				}
				if existing {
					if !bytes.Equal(before, readFile(t, paths.Config)) || string(readFile(t, filepath.Join(paths.Skills, "other", "assets", "data.txt"))) != "keep local edits" {
						t.Fatal("failed add changed existing scope")
					}
				} else if _, err := os.Lstat(paths.Config); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed first add left source/lock", err)
				}
				requireClean(t, paths)
			})
		}
	}
}

func TestRemoveTransactionRollback(t *testing.T) {
	for _, last := range []bool{false, true} {
		for _, stage := range []string{"backup", "save", "saved"} {
			t.Run(stage+map[bool]string{false: "-siblings", true: "-last"}[last], func(t *testing.T) {
				names := []string{"a"}
				if !last {
					names = append(names, "b")
				}
				r := newRepository(t, names...)
				paths := setupDeployment(t, config.Source{Source: r.dir})
				runDeployment(t, paths, false)
				before := readFile(t, paths.Config)
				writeFile(t, filepath.Join(paths.Skills, "a", "assets", "data.txt"), "force-remove local content")
				if !last {
					writeFile(t, filepath.Join(paths.Skills, "b", "assets", "data.txt"), "sibling local content")
				}
				injected := errors.New("injected remove transaction failure")
				err := removeSkill(context.Background(), paths, "a", Options{Force: true}, func(at, _ string) error {
					if at == stage {
						return injected
					}
					return nil
				})
				if !errors.Is(err, injected) || !bytes.Equal(before, readFile(t, paths.Config)) || string(readFile(t, filepath.Join(paths.Skills, "a", "assets", "data.txt"))) != "force-remove local content" {
					t.Fatalf("remove did not restore exact YAML and local files: %v", err)
				}
				if !last && string(readFile(t, filepath.Join(paths.Skills, "b", "assets", "data.txt"))) != "sibling local content" {
					t.Fatal("remove affected sibling")
				}
				requireClean(t, paths)
			})
		}
	}
}

func TestRemoveMissingAndExistingExclude(t *testing.T) {
	r := newRepository(t, "a", "b")
	paths := setupDeployment(t, config.Source{Source: r.dir})
	runDeployment(t, paths, false)
	doc := loadDoc(t, paths)
	doc.Config.Sources[0].Exclude = []string{"a", "upstream-only"}
	if err := doc.Save(paths.Config); err != nil {
		t.Fatal(err)
	}
	removeFixture(t, paths.Skills, "a")
	if err := RemoveSkill(context.Background(), paths, "a", Options{}); err != nil {
		t.Fatal(err)
	}
	source := loadDoc(t, paths).Config.Sources[0]
	if !reflect.DeepEqual(source.Exclude, []string{"a", "upstream-only"}) || len(source.Lock.Skills) != 1 {
		t.Fatalf("duplicate exclude or wrong lock: %#v", source)
	}
	runDeployment(t, paths, false)
	if _, err := os.Lstat(filepath.Join(paths.Skills, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("removed missing Skill reinstalled", err)
	}
	requireClean(t, paths)
}

func TestRemoveSameURLSourcePreservesSurvivingConfig(t *testing.T) {
	r := newRepository(t, "a", "b")
	paths := setupDeployment(t,
		config.Source{Source: r.dir, Paths: []string{"skills/a"}},
		config.Source{Source: r.dir, Paths: []string{"skills/b"}},
	)
	runDeployment(t, paths, false)
	text := string(readFile(t, paths.Config))
	text = strings.Replace(text, "- skills/a", "- skills/a # removed selection", 1)
	text = strings.Replace(text, "- skills/b", "- skills/b # surviving selection", 1)
	writeFile(t, paths.Config, text)
	if err := RemoveSkill(context.Background(), paths, "a", Options{}); err != nil {
		t.Fatal(err)
	}
	after := string(readFile(t, paths.Config))
	if strings.Contains(after, "removed selection") || !strings.Contains(after, "surviving selection") {
		t.Fatalf("same URL source comments mismatched: %s", after)
	}
	assertInstalled(t, paths, 0, "b")
}

func TestRemoveRefusesAmbiguousOrUnsafeOwnership(t *testing.T) {
	for _, scenario := range []string{"duplicate-owner", "case-alias", "symlink", "unmanaged", "traversal"} {
		t.Run(scenario, func(t *testing.T) {
			r := newRepository(t, "a")
			paths := setupDeployment(t, config.Source{Source: r.dir})
			runDeployment(t, paths, false)
			doc := loadDoc(t, paths)
			name := "a"
			switch scenario {
			case "duplicate-owner", "case-alias":
				other := cloneSource(doc.Config.Sources[0])
				other.Source = "unavailable source"
				if scenario == "case-alias" {
					other.Lock.Skills["A"] = other.Lock.Skills["a"]
					delete(other.Lock.Skills, "a")
				}
				doc.Config.Sources = append(doc.Config.Sources, other)
			case "symlink":
				removeFixture(t, paths.Skills, "a")
				if err := os.Symlink(filepath.Join(r.dir, "skills", "a"), filepath.Join(paths.Skills, "a")); err != nil {
					t.Skip("fixture requires symlink permission:", err)
				}
			case "unmanaged":
				doc.Config.Sources = nil
			case "traversal":
				name = "../a"
			}
			if err := doc.Save(paths.Config); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, paths.Config)
			if err := RemoveSkill(context.Background(), paths, name, Options{Force: true}); err == nil {
				t.Fatal("unsafe remove accepted")
			}
			if !bytes.Equal(before, readFile(t, paths.Config)) {
				t.Fatal("unsafe remove changed YAML")
			}
			if _, err := os.Lstat(filepath.Join(paths.Skills, "a")); err != nil {
				t.Fatal("unsafe remove deleted target", err)
			}
		})
	}
}

func TestManageCommandsUseScopeLock(t *testing.T) {
	r := newRepository(t, "a")
	paths := setupDeployment(t, config.Source{Source: r.dir})
	runDeployment(t, paths, false)
	lock, err := acquireScope(filepath.Dir(paths.Config))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := AddSource(context.Background(), paths, "not fetched", nil); err == nil {
		t.Fatal("add bypassed scope lock")
	}
	if err := RemoveSkill(context.Background(), paths, "a", Options{}); err == nil {
		t.Fatal("remove bypassed scope lock")
	}
}
