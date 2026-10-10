package sync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zz-zhi54/skillscli/internal/config"
)

func TestRecoveryStopsWhenOriginalAndBackupAreMissing(t *testing.T) {
	for _, kind := range []ActionKind{Delete, Update} {
		t.Run(string(kind), func(t *testing.T) {
			_, paths, _, _ := changedTransaction(t)
			txn := crashTransaction(t, paths, "saved")
			name := "z" // changedTransaction 的上游删除项。
			if kind == Update {
				name = "a"
				// 尚有 staged new，但正式目录和旧备份均缺失。
				if err := renameAbsent(filepath.Join(paths.Skills, name), filepath.Join(txn, "new", name)); err != nil {
					t.Fatal(err)
				}
			}
			removeFixture(t, txn, "old/"+name)
			after := readFile(t, paths.Config)
			err := recoverTransactions(paths)
			var recovery *RecoveryError
			if !errors.As(err, &recovery) || recovery.Dir != txn {
				t.Fatalf("lost original was accepted as restored: %v", err)
			}
			if !bytes.Equal(after, readFile(t, paths.Config)) {
				t.Fatal("incomplete recovery rewrote YAML")
			}
			if _, err := os.Lstat(filepath.Join(txn, "journal.json")); err != nil {
				t.Fatal("recovery evidence removed", err)
			}
		})
	}
}

func TestRecoveryPreservesOnlyBackup(t *testing.T) {
	for _, change := range []string{"installed-content", "configuration", "missing-journal"} {
		t.Run(change, func(t *testing.T) {
			_, paths, before, old := changedTransaction(t)
			txn := crashTransaction(t, paths, "saved")
			after := readFile(t, paths.Config)
			switch change {
			case "installed-content":
				writeFile(t, filepath.Join(paths.Skills, "a", "assets", "data.txt"), "edits after crash")
			case "configuration":
				writeFile(t, paths.Config, string(after)+"# user edits after crash\n")
			case "missing-journal":
				removeFixture(t, txn, "journal.json")
			}
			results, err := Run(context.Background(), paths, Options{Force: true})
			var recovery *RecoveryError
			if !errors.As(err, &recovery) || recovery.Dir != txn || len(results) != 0 {
				t.Fatalf("unsafe recovery proceeded: %#v %v", results, err)
			}
			if hash(t, filepath.Join(txn, "old", "a")) != old["a"] {
				t.Fatal("unique original backup was deleted")
			}
			if change == "missing-journal" {
				return
			}
			if change == "installed-content" {
				if string(readFile(t, filepath.Join(paths.Skills, "a", "assets", "data.txt"))) != "edits after crash" {
					t.Fatal("recovery discarded user changes")
				}
				writeFile(t, filepath.Join(paths.Skills, "a", "assets", "data.txt"), "updated")
			} else {
				if !bytes.HasSuffix(readFile(t, paths.Config), []byte("# user edits after crash\n")) {
					t.Fatal("recovery discarded edited config")
				}
				writeFile(t, paths.Config, string(after))
			}
			if err := recoverTransactions(paths); err != nil {
				t.Fatal(err)
			}
			assertRestored(t, paths, before, old)
			requireClean(t, paths)
		})
	}
}

func TestRecoveryCanResumePartialRollback(t *testing.T) {
	_, paths, before, old := changedTransaction(t)
	txn := crashTransaction(t, paths, "saved")
	// 模拟回滚搬回 a 的新版后终止；其他目录和 YAML 尚未恢复。
	if err := renameAbsent(filepath.Join(paths.Skills, "a"), filepath.Join(txn, "new", "a")); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransactions(paths); err != nil {
		t.Fatal(err)
	}
	assertRestored(t, paths, before, old)
	requireClean(t, paths)
	if err := recoverTransactions(paths); err != nil {
		t.Fatal("recovery not idempotent:", err)
	}
}

func TestCommittedAndTerminalCleanupRecovery(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "partial-cleanup"}[partial], func(t *testing.T) {
			_, paths, _, _ := changedTransaction(t)
			txn := crashTransaction(t, paths, "saved")
			after := readFile(t, paths.Config)
			if err := config.WriteAtomic(filepath.Join(txn, "committed"), []byte("committed\n")); err != nil {
				t.Fatal(err)
			}
			if partial {
				garbage := filepath.Join(filepath.Dir(txn), ".skillscli-cleanup-"+strings.TrimPrefix(filepath.Base(txn), ".skillscli-txn-"))
				if err := renameAbsent(txn, garbage); err != nil {
					t.Fatal(err)
				}
				removeFixture(t, garbage, "config.before")
				removeFixture(t, garbage, "journal.json")
			}
			if err := recoverTransactions(paths); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, readFile(t, paths.Config)) {
				t.Fatal("committed YAML rolled back")
			}
			assertInstalled(t, paths, 0, "a", "b")
			requireClean(t, paths)
		})
	}
}

func TestInitialPreparationCleanupRecovery(t *testing.T) {
	paths := setupDeployment(t)
	txn, err := os.MkdirTemp(filepath.Dir(paths.Config), ".skillscli-txn-")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(txn, "new", "a", "partial-file"), "not yet validated")
	if err := recoverTransactions(paths); err != nil {
		t.Fatal(err)
	}
	requireClean(t, paths)
}

func TestForceRepairFailureKeepsRawYAMLForNextSource(t *testing.T) {
	a := newRepository(t, "a")
	b := newRepository(t, "b")
	paths := setupDeployment(t, config.Source{Source: a.dir}, config.Source{Source: b.dir})
	runDeployment(t, paths, false)
	doc := loadDoc(t, paths)
	// 使用流式格式（编码会变化）但模型/所有权相同。
	bytes, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, paths.Config, "---\n"+string(bytes))
	original := readFile(t, paths.Config)
	writeFile(t, filepath.Join(paths.Skills, "a", "assets", "data.txt"), "local force repair")
	writeFile(t, filepath.Join(b.dir, "skills", "b", "assets", "data.txt"), "b upstream update")
	b.commit()
	doc = loadDoc(t, paths)
	plan := build(t, paths.Skills, true, doc.Config.Sources...)
	results, err := execute(context.Background(), paths, doc, plan, original, func(stage, name string) error {
		if stage == "prepare" && name == "a" {
			return errors.New("force repair preparation failed")
		}
		return nil
	})
	if err == nil || results[0].Status != Failed || results[1].Status != Ready {
		t.Fatalf("raw baseline broke isolation: %#v %v", results, err)
	}
	if string(readFile(t, filepath.Join(paths.Skills, "a", "assets", "data.txt"))) != "local force repair" {
		t.Fatal("failed repair modified local content")
	}
	assertInstalled(t, paths, 1, "b")
	requireClean(t, paths)
}

func TestRestoredDocumentSupportsNextSource(t *testing.T) {
	for _, stage := range []string{"saved", "invalid-version"} {
		t.Run(stage, func(t *testing.T) {
			a := newRepository(t, "a")
			b := newRepository(t, "b")
			paths := setupDeployment(t, config.Source{Source: a.dir}, config.Source{Source: b.dir})
			runDeployment(t, paths, false)
			doc := loadDoc(t, paths)
			oldHash := doc.Config.Sources[0].Lock.Skills["a"]
			writeFile(t, paths.Config, strings.Replace(string(readFile(t, paths.Config)), oldHash, oldHash+" # preserve-a-lock-comment", 1))
			before := readFile(t, paths.Config)
			removeFixture(t, a.dir, "skills/a")
			a.commit()
			writeFile(t, filepath.Join(b.dir, "skills", "b", "assets", "data.txt"), "b updated")
			b.commit()
			doc = loadDoc(t, paths)
			plan := build(t, paths.Skills, false, doc.Config.Sources...)
			first := true
			results, err := execute(context.Background(), paths, doc, plan, before, func(boundary, name string) error {
				if first && stage == "saved" && boundary == "saved" {
					first = false
					return errors.New("failure after YAML save")
				}
				if first && stage == "invalid-version" && boundary == "save" {
					first = false
					doc.Config.Version = 99
				}
				return nil
			})
			if err == nil || len(results) != 2 || results[0].Status != Failed || results[1].Status != Ready {
				t.Fatalf("restored document affected next source: %#v %v", results, err)
			}
			if !bytes.Contains(readFile(t, paths.Config), []byte("# preserve-a-lock-comment")) {
				t.Fatal("rollback document lost original lock comment")
			}
			assertInstalled(t, paths, 0, "a")
			assertInstalled(t, paths, 1, "b")
			requireClean(t, paths)
		})
	}
}

func TestReadonlyBackupCleanup(t *testing.T) {
	repo := newRepository(t, "a")
	paths := setupDeployment(t, config.Source{Source: repo.dir})
	runDeployment(t, paths, false)
	old := filepath.Join(paths.Skills, "a", "assets")
	if err := os.Chmod(old, 0555); err != nil {
		t.Fatal(err)
	}
	// 如果测试在同步前失败，仍确保 fixture 可清理。
	t.Cleanup(func() { _ = os.Chmod(old, 0700) })
	writeFile(t, filepath.Join(repo.dir, "skills", "a", "assets", "data.txt"), "update")
	repo.commit()
	runDeployment(t, paths, false)
	assertInstalled(t, paths, 0, "a")
}

func TestActualYAMLWriteFailureRollsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions fault fixture")
	}
	_, paths, before, old := changedTransaction(t)
	agents := filepath.Dir(paths.Config)
	probe := filepath.Join(agents, "permission-probe")
	if err := os.Chmod(agents, 0500); err != nil {
		t.Fatal(err)
	}
	err := os.WriteFile(probe, []byte("probe"), 0600)
	if chmodErr := os.Chmod(agents, 0700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil {
		removeFixture(t, agents, "permission-probe")
		t.Skip("privileged process bypasses filesystem permissions")
	}
	doc := loadDoc(t, paths)
	plan := build(t, paths.Skills, false, doc.Config.Sources...)
	t.Cleanup(func() { _ = os.Chmod(agents, 0700) })
	results, err := execute(context.Background(), paths, doc, plan, before, func(stage, name string) error {
		if stage == "save" {
			return os.Chmod(agents, 0500)
		}
		return nil
	})
	if err == nil || results[0].Status != Failed {
		t.Fatalf("YAML I/O failure not reported: %#v %v", results, err)
	}
	if err := os.Chmod(agents, 0700); err != nil {
		t.Fatal(err)
	}
	assertRestored(t, paths, before, old)
	if err := recoverTransactions(paths); err != nil {
		t.Fatal(err)
	}
	requireClean(t, paths)
}

func TestCommitMarkerWriteFailureRollsBack(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires unprivileged Unix directory permissions")
	}
	_, paths, before, old := changedTransaction(t)
	doc := loadDoc(t, paths)
	plan := build(t, paths.Skills, false, doc.Config.Sources...)
	results, err := execute(context.Background(), paths, doc, plan, before, func(stage, name string) error {
		if stage != "saved" {
			return nil
		}
		entries, err := os.ReadDir(filepath.Dir(paths.Config))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".skillscli-txn-") {
				return os.Chmod(filepath.Join(filepath.Dir(paths.Config), entry.Name()), 0500)
			}
		}
		return errors.New("transaction missing")
	})
	if err == nil || results[0].Status != Failed {
		t.Fatalf("commit marker I/O failure not reported: %#v %v", results, err)
	}
	assertRestored(t, paths, before, old)
	requireClean(t, paths)
	if err := recoverTransactions(paths); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCleanupFailurePreservesLocationAndIsolation(t *testing.T) {
	a := newRepository(t, "a")
	b := newRepository(t, "b")
	paths := setupDeployment(t, config.Source{Source: a.dir}, config.Source{Source: b.dir})
	doc := loadDoc(t, paths)
	plan := build(t, paths.Skills, false, doc.Config.Sources...)
	before := readFile(t, paths.Config)
	external := filepath.Join(t.TempDir(), "user-file")
	writeFile(t, external, "must not change")
	injected := false
	results, err := execute(context.Background(), paths, doc, plan, before, func(stage, name string) error {
		if stage != "saved" || injected {
			return nil
		}
		injected = true
		entries, err := os.ReadDir(filepath.Dir(paths.Config))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".skillscli-txn-") {
				if err := os.Symlink(external, filepath.Join(filepath.Dir(paths.Config), entry.Name(), "unsafe-cleanup-link")); err != nil {
					t.Skip("symlink fixture unavailable:", err)
				}
				return nil
			}
		}
		return errors.New("transaction missing")
	})
	var recovery *RecoveryError
	if !errors.As(err, &recovery) || !strings.HasPrefix(filepath.Base(recovery.Dir), ".skillscli-cleanup-") {
		t.Fatalf("missing actual retained location: %v", err)
	}
	if len(results) != 2 || results[0].Status != Failed || results[1].Status != Ready {
		t.Fatalf("terminal cleanup failure stopped independent source: %#v", results)
	}
	if _, err := os.Stat(recovery.Dir); err != nil {
		t.Fatal("reported recovery path does not exist:", err)
	}
	assertInstalled(t, paths, 0, "a")
	assertInstalled(t, paths, 1, "b")
	if string(readFile(t, external)) != "must not change" {
		t.Fatal("cleanup followed symlink")
	}
	removeFixture(t, recovery.Dir, "unsafe-cleanup-link")
	runDeployment(t, paths, false)
	requireClean(t, paths)
}

func TestCanceledDeploymentRollsBack(t *testing.T) {
	_, paths, before, old := changedTransaction(t)
	doc := loadDoc(t, paths)
	plan := build(t, paths.Skills, false, doc.Config.Sources...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results, err := execute(ctx, paths, doc, plan, before, func(stage, name string) error {
		if stage == "install" && name == "b" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || results[0].Status != Failed {
		t.Fatalf("cancellation not propagated: %#v %v", results, err)
	}
	assertRestored(t, paths, before, old)
	requireClean(t, paths)
}

func TestRunClosesGitResources(t *testing.T) {
	repo := newRepository(t, "a")
	missing := filepath.Join(t.TempDir(), "missing.git")
	paths := setupDeployment(t, config.Source{Source: repo.dir}, config.Source{Source: missing})
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	for i := 0; i < 2; i++ {
		results, err := Run(context.Background(), paths, Options{})
		if err == nil || len(results) != 2 || results[1].Status != Failed {
			t.Fatalf("expected independent Git failure: %#v %v", results, err)
		}
		entries, err := os.ReadDir(temp)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "skillscli-git-") {
				t.Fatal("Git checkout leaked:", entry.Name())
			}
		}
		for _, result := range results {
			for _, action := range result.Source.Actions {
				if action.FromDir != "" {
					if _, err := os.Stat(action.FromDir); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("Plan.Close not called:", action.FromDir)
					}
				}
			}
		}
		requireClean(t, paths)
	}
}
