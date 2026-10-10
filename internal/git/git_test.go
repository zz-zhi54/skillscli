package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Git 替身是当前测试二进制，不使用 shell；正常命令仍转交真实 Git。
// 仅模拟难以从本地 bare remote 稳定制造的拒绝 SHA、并发推进、失败和取消。
func TestMain(m *testing.M) {
	if os.Getenv("SKILLSCLI_GIT_HELPER") != "" {
		gitHelper()
		return
	}
	os.Exit(m.Run())
}

type invocation struct {
	Args []string
	Dir  string
}

func gitHelper() {
	args := os.Args[1:]
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	log, err := os.OpenFile(os.Getenv("SKILLSCLI_GIT_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(log).Encode(invocation{Args: args, Dir: dir}); err != nil {
		panic(err)
	}
	if err := log.Close(); err != nil {
		panic(err)
	}
	command := commandArgs(args)
	mode := os.Getenv("SKILLSCLI_GIT_HELPER")
	if mode == "fail-all" || mode == "fail-"+command[0] {
		fmt.Fprintln(os.Stderr, "fatal: https://user:super-secret@example.invalid/repo?token=private-token; rewritten-secret")
		os.Exit(23)
	}
	if command[0] == "fetch" {
		switch mode {
		case "cancel-fetch":
			time.Sleep(time.Minute)
			os.Exit(24)
		case "move":
			cmd := exec.Command(os.Getenv("SKILLSCLI_REAL_GIT"), "--git-dir="+os.Getenv("SKILLSCLI_REMOTE"), "update-ref", os.Getenv("SKILLSCLI_MOVE_REF"), os.Getenv("SKILLSCLI_MOVE_SHA"))
			if err := cmd.Run(); err != nil {
				panic(err)
			}
		case "fallback", "fallback-shallow":
			if contains(command, "--depth=1") {
				if mode == "fallback-shallow" {
					// 模拟直接获取失败前已写入浅边界的情况。
					actual := append([]string(nil), args...)
					actual[len(actual)-1] = os.Getenv("SKILLSCLI_TIP_SHA")
					if err := exec.Command(os.Getenv("SKILLSCLI_REAL_GIT"), actual...).Run(); err != nil {
						panic(err)
					}
				}
				fmt.Fprintln(os.Stderr, "server refuses direct SHA fetch")
				os.Exit(128)
			}
		}
	}
	cmd := exec.Command(os.Getenv("SKILLSCLI_REAL_GIT"), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		panic(err)
	}
	os.Exit(0)
}

func commandArgs(args []string) []string {
	for len(args) > 0 {
		switch {
		case args[0] == "-c":
			args = args[2:]
		case args[0] == "--no-replace-objects", strings.HasPrefix(args[0], "--git-dir="), strings.HasPrefix(args[0], "--work-tree="):
			args = args[1:]
		default:
			return args
		}
	}
	panic("missing Git command")
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

type repository struct {
	root, work, remote, source string
	first, tip                 string
	git                        string
	env                        []string
}

func newRepository(t *testing.T, format string) *repository {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("tests require system Git:", err)
	}
	// 不读取用户身份/凭证配置，也不依赖机器上的 hooks 或签名设置。
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("HOME", t.TempDir())
	r := &repository{root: t.TempDir(), git: git, env: os.Environ()}
	r.work, r.remote = filepath.Join(r.root, "upstream"), filepath.Join(r.root, "remote.git")
	r.source = (&url.URL{Scheme: "file", Path: filepath.ToSlash(r.remote)}).String()
	if err := os.Mkdir(r.work, 0700); err != nil {
		t.Fatal(err)
	}
	r.command(t, r.work, "init", "--template=", "--initial-branch=trunk", "--object-format="+format)
	r.command(t, r.work, "config", "user.name", "Test")
	r.command(t, r.work, "config", "user.email", "test@example.invalid")
	r.first = r.commit(t, "first\n")
	r.command(t, r.work, "branch", "develop")
	r.command(t, r.work, "branch", "v9.0.0")
	r.command(t, r.work, "branch", "nested/refs/heads/missing")
	r.command(t, r.work, "tag", "v1.0.0")
	r.command(t, r.work, "tag", "main")
	r.command(t, r.work, "tag", "-a", "v2.0.0", "-m", "annotated")
	r.tip = r.commit(t, "second\n")
	r.command(t, r.work, "branch", "shared")
	r.command(t, r.work, "tag", "shared", r.first)
	r.command(t, r.root, "init", "--bare", "--template=", "--initial-branch=trunk", "--object-format="+format, r.remote)
	r.command(t, r.work, "remote", "add", "origin", r.source)
	r.command(t, r.work, "push", "origin", "--all")
	r.command(t, r.work, "push", "origin", "--tags")
	return r
}

func (r *repository) command(t *testing.T, dir string, args ...string) string {
	t.Helper()
	options := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-C", dir}
	cmd := exec.Command(r.git, append(options, args...)...)
	cmd.Env = r.env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func (r *repository) commit(t *testing.T, text string) string {
	t.Helper()
	writeFile(t, filepath.Join(r.work, "content.txt"), text)
	r.command(t, r.work, "add", "content.txt")
	r.command(t, r.work, "commit", "-m", strings.TrimSpace(text))
	return r.command(t, r.work, "rev-parse", "HEAD")
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func tempRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, dir)
	}
	return dir
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary directories leaked: %v", entries)
	}
}

func useGitHelper(t *testing.T, r *repository, mode string) string {
	t.Helper()
	bin := t.TempDir()
	name := "git"
	if filepath.Ext(r.git) == ".exe" {
		name += ".exe"
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(filepath.Join(bin, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "commands.jsonl")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	t.Setenv("SKILLSCLI_GIT_HELPER", mode)
	t.Setenv("SKILLSCLI_REAL_GIT", r.git)
	t.Setenv("SKILLSCLI_GIT_LOG", log)
	t.Setenv("SKILLSCLI_REMOTE", r.remote)
	t.Setenv("SKILLSCLI_TIP_SHA", r.tip)
	return log
}

func invocations(t *testing.T, path string) []invocation {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(fileText(t, path)))
	var result []invocation
	for {
		var call invocation
		if err := decoder.Decode(&call); errors.Is(err, io.EOF) {
			return result
		} else if err != nil {
			t.Fatal(err)
		}
		result = append(result, call)
	}
}

func assertCheckout(t *testing.T, r *repository, checkout *Checkout, revision, text string) {
	t.Helper()
	if checkout.Revision != revision || fileText(t, filepath.Join(checkout.Dir, "content.txt")) != text {
		t.Fatalf("wrong checkout: revision %q, expected %q", checkout.Revision, revision)
	}
	if actual := r.command(t, checkout.Dir, "rev-parse", "HEAD"); actual != checkout.Revision {
		t.Fatalf("HEAD %s differs from result %s", actual, checkout.Revision)
	}
	if head := fileText(t, filepath.Join(checkout.Dir, ".git", "HEAD")); strings.HasPrefix(head, "ref:") {
		t.Fatal("checkout is not detached")
	}
}

func TestResolveAndFetch(t *testing.T) {
	r := newRepository(t, "sha1")
	cases := []struct {
		name, ref, resolved, revision, text string
	}{
		{"default trunk", "", "refs/heads/trunk", r.tip, "second\n"},
		{"branch", "develop", "refs/heads/develop", r.first, "first\n"},
		{"explicit branch", "refs/heads/trunk", "refs/heads/trunk", r.tip, "second\n"},
		{"lightweight tag", "v1.0.0", "refs/tags/v1.0.0", r.first, "first\n"},
		{"annotated tag", "v2.0.0", "refs/tags/v2.0.0", r.first, "first\n"},
		{"explicit annotated tag", "refs/tags/v2.0.0", "refs/tags/v2.0.0", r.first, "first\n"},
		{"branch with tag-like name", "v9.0.0", "refs/heads/v9.0.0", r.first, "first\n"},
		{"tag with branch-like name", "main", "refs/tags/main", r.first, "first\n"},
		{"ambiguous explicit branch", "refs/heads/shared", "refs/heads/shared", r.tip, "second\n"},
		{"ambiguous explicit tag", "refs/tags/shared", "refs/tags/shared", r.first, "first\n"},
		{"fixed history", r.first, r.first, r.first, "first\n"},
		{"uppercase fixed history", strings.ToUpper(r.first), r.first, r.first, "first\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := tempRoot(t)
			resolved, err := Resolve(context.Background(), r.source, tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if resolved != (Reference{Name: tc.resolved, Revision: tc.revision}) {
				t.Fatalf("unexpected reference: %+v", resolved)
			}
			checkout, err := Fetch(context.Background(), r.source, tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := checkout.Close(); err != nil {
					t.Error(err)
				}
			})
			assertCheckout(t, r, checkout, tc.revision, tc.text)
			if shallow := r.command(t, checkout.Dir, "rev-parse", "--is-shallow-repository"); shallow != "true" {
				t.Fatal("normal fetch should prefer shallow history")
			}
			if err := checkout.Close(); err != nil {
				t.Fatal(err)
			}
			if err := checkout.Close(); err != nil {
				t.Fatal("Close is not idempotent:", err)
			}
			assertEmpty(t, tmp)
		})
	}
}

func TestInvalidReferences(t *testing.T) {
	r := newRepository(t, "sha1")
	for _, tc := range []struct{ ref, message string }{
		{"shared", "ambiguous"},
		{"missing", "does not exist"},
		{"refs/heads/missing", "does not exist"},
		{"refs/tags/missing", "does not exist"},
		{"bad ref", "invalid"},
		{"../trunk", "invalid"},
		{"trunk^{commit}", "invalid"},
		{"trunk:other", "invalid"},
		{"-option", "does not exist"},
		{"refs/remotes/origin/trunk", "unsupported"},
		{r.first[:12], "does not exist"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			tmp := tempRoot(t)
			checkout, err := Fetch(context.Background(), r.source, tc.ref)
			if err == nil || !strings.Contains(err.Error(), tc.message) || checkout != nil {
				t.Fatalf("expected %q error, got %v, %#v", tc.message, err, checkout)
			}
			assertEmpty(t, tmp)
		})
	}
	if _, err := Resolve(context.Background(), " ", "trunk"); err == nil {
		t.Fatal("accepted blank source")
	}
	// bare HEAD 指向不存在的分支时，不悄悄选择别的分支。
	r.command(t, r.remote, "symbolic-ref", "HEAD", "refs/heads/absent")
	if _, err := Resolve(context.Background(), r.source, ""); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Fatalf("missing default branch: %v", err)
	}
}

func TestHistoricalCommitFallback(t *testing.T) {
	r := newRepository(t, "sha1")
	// 让历史提交只由远程默认分支的历史可达，而不是分支或标签的 tip。
	r.command(t, r.remote, "update-ref", "-d", "refs/heads/develop")
	r.command(t, r.remote, "update-ref", "-d", "refs/heads/v9.0.0")
	r.command(t, r.remote, "update-ref", "-d", "refs/heads/nested/refs/heads/missing")
	for _, name := range []string{"v1.0.0", "v2.0.0", "main", "shared"} {
		r.command(t, r.remote, "update-ref", "-d", "refs/tags/"+name)
	}
	for _, mode := range []string{"fallback", "fallback-shallow"} {
		t.Run(mode, func(t *testing.T) {
			log := useGitHelper(t, r, mode)
			tmp := tempRoot(t)
			checkout, err := Fetch(context.Background(), r.source, r.first)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = checkout.Close() })
			assertCheckout(t, r, checkout, r.first, "first\n")
			var fetches [][]string
			for _, call := range invocations(t, log) {
				args := commandArgs(call.Args)
				if args[0] == "fetch" {
					fetches = append(fetches, args)
				}
			}
			if len(fetches) != 2 || !contains(fetches[0], "--depth=1") || fetches[0][len(fetches[0])-1] != r.first {
				t.Fatalf("wrong shallow attempt: %v", fetches)
			}
			fallback := []string{"fetch", "--no-tags", "--no-recurse-submodules"}
			if mode == "fallback-shallow" {
				fallback = append(fallback, "--unshallow")
			}
			fallback = append(fallback, "origin", "+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*")
			if !reflect.DeepEqual(fetches[1], fallback) {
				t.Fatalf("wrong full history fallback: %v", fetches[1])
			}
			if err := checkout.Close(); err != nil {
				t.Fatal(err)
			}
			assertEmpty(t, tmp)
		})
	}
}

func TestFixedCommitMustBeAvailableAndExact(t *testing.T) {
	r := newRepository(t, "sha1")
	blob := r.command(t, r.work, "rev-parse", r.first+":content.txt")
	tagObject := r.command(t, r.work, "rev-parse", "refs/tags/v2.0.0")
	for _, sha := range []string{strings.Repeat("a", 40), blob, tagObject} {
		t.Run(sha, func(t *testing.T) {
			tmp := tempRoot(t)
			checkout, err := Fetch(context.Background(), r.source, sha)
			if err == nil || checkout != nil {
				t.Fatalf("accepted missing or non-commit SHA: %#v, %v", checkout, err)
			}
			assertEmpty(t, tmp)
		})
	}
	// 有效标签如果指向 blob，也必须拒绝，不能把对象 SHA 当作 Commit。
	r.command(t, r.work, "tag", "blob-tag", blob)
	r.command(t, r.work, "push", "origin", "refs/tags/blob-tag")
	tmp := tempRoot(t)
	if checkout, err := Fetch(context.Background(), r.source, "blob-tag"); err == nil || checkout != nil {
		t.Fatalf("accepted tag pointing to non-commit: %#v, %v", checkout, err)
	}
	assertEmpty(t, tmp)
}

func TestRecordsActuallyFetchedCommit(t *testing.T) {
	r := newRepository(t, "sha1")
	newTip := r.commit(t, "third\n")
	r.command(t, r.work, "push", "origin", "trunk")
	// 查询时仍指向旧提交；替身只在 fetch 开始之前推进远程引用。
	for _, ref := range []string{"refs/heads/trunk", "refs/tags/v2.0.0"} {
		t.Run(ref, func(t *testing.T) {
			r.command(t, r.remote, "update-ref", ref, r.tip)
			log := useGitHelper(t, r, "move")
			t.Setenv("SKILLSCLI_MOVE_REF", ref)
			t.Setenv("SKILLSCLI_MOVE_SHA", newTip)
			tmp := tempRoot(t)
			checkout, err := Fetch(context.Background(), r.source, ref)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = checkout.Close() })
			assertCheckout(t, r, checkout, newTip, "third\n")
			for _, call := range invocations(t, log) {
				args := commandArgs(call.Args)
				if args[0] == "fetch" && args[len(args)-1] != ref {
					t.Fatalf("fetch used stale SHA instead of the selected ref: %v", args)
				}
			}
			if err := checkout.Close(); err != nil {
				t.Fatal(err)
			}
			assertEmpty(t, tmp)
		})
	}
	// 分支已经推进，固定历史 SHA 仍必须取得原提交。
	checkout, err := Fetch(context.Background(), r.source, r.first)
	if err != nil {
		t.Fatal(err)
	}
	defer checkout.Close()
	assertCheckout(t, r, checkout, r.first, "first\n")
}

func TestGitFailuresAndCancellation(t *testing.T) {
	r := newRepository(t, "sha1")
	for _, mode := range []string{"fail-all", "fail-ls-remote", "fail-init", "fail-fetch", "fail-checkout"} {
		t.Run(mode, func(t *testing.T) {
			useGitHelper(t, r, mode)
			tmp := tempRoot(t)
			ref := "trunk"
			if mode != "fail-all" && mode != "fail-ls-remote" {
				ref = r.first
			}
			source := "https://user:super-secret@example.invalid/repo?token=private-token"
			if mode == "fail-checkout" {
				// checkout 失败需先成功获取，故使用本地来源。
				source = r.source
			}
			checkout, err := Fetch(context.Background(), source, ref)
			var exit *exec.ExitError
			if checkout != nil || !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("execution error not preserved: %#v, %v", checkout, err)
			}
			for _, secret := range []string{"super-secret", "private-token", "rewritten-secret", "example.invalid"} {
				if strings.Contains(fmt.Sprintf("%+v", err), secret) || bytes.Contains(exit.Stderr, []byte(secret)) {
					t.Fatalf("error leaks credentials: %v", err)
				}
			}
			assertEmpty(t, tmp)
		})
	}
	t.Run("Git missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		tmp := tempRoot(t)
		if _, err := Fetch(context.Background(), r.source, r.first); err == nil || !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("missing Git error: %v", err)
		}
		assertEmpty(t, tmp)
	})
	t.Run("already canceled", func(t *testing.T) {
		tmp := tempRoot(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Fetch(ctx, r.source, "trunk"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error: %v", err)
		}
		assertEmpty(t, tmp)
	})
	t.Run("cancel executing fetch", func(t *testing.T) {
		log := useGitHelper(t, r, "cancel-fetch")
		tmp := tempRoot(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := Fetch(ctx, r.source, r.first); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("executing cancellation error: %v", err)
		}
		var fetches int
		for _, call := range invocations(t, log) {
			if commandArgs(call.Args)[0] == "fetch" {
				fetches++
			}
		}
		if fetches != 1 {
			t.Fatalf("canceled fetch missing or retried: %d", fetches)
		}
		assertEmpty(t, tmp)
	})
}

func changeDir(t *testing.T, dir string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Error(err)
		}
	})
}

func TestCurrentRepositoryUnaffected(t *testing.T) {
	r := newRepository(t, "sha1")
	writeFile(t, filepath.Join(r.work, "content.txt"), "user staged changes\n")
	r.command(t, r.work, "add", "content.txt")
	writeFile(t, filepath.Join(r.work, "content.txt"), "user unstaged changes\n")
	writeFile(t, filepath.Join(r.work, "untracked.txt"), "untracked\n")
	changeDir(t, r.work)
	for key, value := range map[string]string{
		"GIT_DIR": filepath.Join(r.work, ".git"), "GIT_WORK_TREE": r.work,
		"GIT_COMMON_DIR":       filepath.Join(r.work, ".git"),
		"GIT_INDEX_FILE":       filepath.Join(r.work, ".git", "index"),
		"GIT_OBJECT_DIRECTORY": filepath.Join(r.work, ".git", "objects"),
	} {
		t.Setenv(key, value)
	}
	snapshot := func() []string {
		return []string{
			r.command(t, r.work, "rev-parse", "HEAD"),
			r.command(t, r.work, "status", "--porcelain"),
			r.command(t, r.work, "show-ref"),
			fileText(t, filepath.Join(r.work, ".git", "config")),
			fileText(t, filepath.Join(r.work, ".git", "index")),
			fileText(t, filepath.Join(r.work, "content.txt")),
			fileText(t, filepath.Join(r.work, "untracked.txt")),
		}
	}
	before := snapshot()
	tmp := tempRoot(t)
	checkout, err := Fetch(context.Background(), r.source, "develop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checkout.Close() })
	assertCheckout(t, r, checkout, r.first, "first\n")
	if err := checkout.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(context.Background(), r.source, strings.Repeat("b", 40)); err == nil {
		t.Fatal("expected failure")
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("current repository, index, configuration or user files changed")
	}
	if _, err := os.Stat(filepath.Join(r.work, ".git", "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("fetch touched the current repository: %v", err)
	}
	assertEmpty(t, tmp)
}

func TestRelativeLocalSourceAndCloseOwnership(t *testing.T) {
	r := newRepository(t, "sha1")
	changeDir(t, r.root)
	tmp := tempRoot(t)
	checkout, err := Fetch(context.Background(), "./remote.git", "develop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checkout.Close() })
	assertCheckout(t, r, checkout, r.first, "first\n")
	original := checkout.Dir
	checkout.Dir = r.work
	if err := checkout.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatalf("owned temporary directory not removed: %v", err)
	}
	if fileText(t, filepath.Join(r.work, "content.txt")) != "second\n" {
		t.Fatal("Close removed an unowned directory")
	}
	assertEmpty(t, tmp)
}

func TestNoHooksOrRepositoryScripts(t *testing.T) {
	r := newRepository(t, "sha1")
	marker := filepath.Join(r.root, "script-ran")
	script := "#!/bin/sh\nprintf ran > '" + marker + "'\n"
	writeFile(t, filepath.Join(r.work, "script.sh"), script)
	r.command(t, r.work, "add", "script.sh")
	r.command(t, r.work, "commit", "-m", "repository script")
	r.command(t, r.work, "push", "origin", "trunk")
	template := t.TempDir()
	hooks := filepath.Join(template, "hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"post-checkout", "reference-transaction"} {
		path := filepath.Join(hooks, name)
		writeFile(t, path, script)
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, config, fmt.Sprintf("[core]\n\thooksPath = %q\n[init]\n\ttemplateDir = %q\n[url %q]\n\tinsteadOf = skillscli-fixture://repo\n", filepath.ToSlash(hooks), filepath.ToSlash(template), r.source))
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	before := fileText(t, config)
	// 非网络别名通过用户配置改写到本地 remote，验证没有禁用已有 Git 配置。
	checkout, err := Fetch(context.Background(), "skillscli-fixture://repo", "trunk")
	if err != nil {
		t.Fatal(err)
	}
	defer checkout.Close()
	if fileText(t, filepath.Join(checkout.Dir, "script.sh")) != script {
		t.Fatal("repository script not checked out as an ordinary file")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Git hooks or repository scripts ran: %v", err)
	}
	if fileText(t, config) != before {
		t.Fatal("global configuration changed")
	}
}

func TestCommandParameters(t *testing.T) {
	r := newRepository(t, "sha1")
	for _, tc := range []struct {
		name, ref, selected string
		query               []string
	}{
		{"default", "", "refs/heads/trunk", []string{"HEAD"}},
		{"ordinary", "develop", "refs/heads/develop", []string{"refs/heads/develop", "refs/tags/develop", "refs/tags/develop^{}"}},
		{"explicit tag", "refs/tags/v2.0.0", "refs/tags/v2.0.0", []string{"refs/tags/v2.0.0", "refs/tags/v2.0.0^{}"}},
		{"fixed", r.first, r.first, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := useGitHelper(t, r, "passthrough")
			tmp := tempRoot(t)
			checkout, err := Fetch(context.Background(), r.source, tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = checkout.Close() })
			var queries, fetches int
			for _, call := range invocations(t, log) {
				args := commandArgs(call.Args)
				switch args[0] {
				case "ls-remote":
					queries++
					expected := append([]string{"ls-remote", "--symref", "--", r.source}, tc.query...)
					if !reflect.DeepEqual(args, expected) {
						t.Fatalf("wrong query parameters: %v", args)
					}
				case "init", "remote", "fetch", "rev-parse", "checkout":
					if !contains(call.Args, "--git-dir="+filepath.Join(checkout.Dir, ".git")) || !contains(call.Args, "--work-tree="+checkout.Dir) {
						t.Fatalf("repository command has no isolated paths: %v", call)
					}
					// macOS 的 /var 与 /private/var 可能是同一个路径。
					actual, err := filepath.EvalSymlinks(checkout.Dir)
					if err != nil || call.Dir != actual {
						t.Fatalf("repository command ran outside its checkout: %v, %v", call, err)
					}
				}
				if args[0] == "fetch" {
					fetches++
					expected := []string{"fetch", "--no-tags", "--no-recurse-submodules", "--depth=1", "origin", tc.selected}
					if !reflect.DeepEqual(args, expected) {
						t.Fatalf("wrong fetch parameters: %v", args)
					}
				}
			}
			wantQueries := 1
			if tc.query == nil {
				wantQueries = 0
			}
			if queries != wantQueries || fetches != 1 {
				t.Fatalf("unexpected command counts: queries=%d, fetches=%d", queries, fetches)
			}
			if err := checkout.Close(); err != nil {
				t.Fatal(err)
			}
			assertEmpty(t, tmp)
		})
	}
	t.Run("branch failure is not retried as full history", func(t *testing.T) {
		log := useGitHelper(t, r, "fail-fetch")
		tmp := tempRoot(t)
		if _, err := Fetch(context.Background(), r.source, "develop"); err == nil {
			t.Fatal("expected fetch failure")
		}
		var fetches int
		for _, call := range invocations(t, log) {
			if commandArgs(call.Args)[0] == "fetch" {
				fetches++
			}
		}
		if fetches != 1 {
			t.Fatalf("branch failure incorrectly triggered SHA fallback: %d", fetches)
		}
		assertEmpty(t, tmp)
	})
}

func TestSHA256Repository(t *testing.T) {
	r := newRepository(t, "sha256")
	for _, tc := range []struct{ ref, revision, text string }{
		{"", r.tip, "second\n"},
		{"v2.0.0", r.first, "first\n"},
		{r.first, r.first, "first\n"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			tmp := tempRoot(t)
			checkout, err := Fetch(context.Background(), r.source, tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = checkout.Close() })
			if len(checkout.Revision) != 64 {
				t.Fatal("SHA-256 revision was truncated")
			}
			assertCheckout(t, r, checkout, tc.revision, tc.text)
			if err := checkout.Close(); err != nil {
				t.Fatal(err)
			}
			assertEmpty(t, tmp)
		})
	}
}

func TestParseRemote(t *testing.T) {
	sha := strings.Repeat("a", 40)
	peeled := strings.Repeat("b", 40)
	output := "ref: refs/heads/trunk\tHEAD\n" + sha + "\tHEAD\n" + sha + "\trefs/tags/v1\n" + peeled + "\trefs/tags/v1^{}"
	refs, head, err := parseRemote(output)
	if err != nil || head != "refs/heads/trunk" || refs["refs/tags/v1^{}"] != peeled {
		t.Fatalf("unexpected parsed response: %v, %q, %v", refs, head, err)
	}
	for _, output := range []string{"missing tab", sha + "\t", "not-a-sha\trefs/heads/trunk"} {
		if _, _, err := parseRemote(output); err == nil {
			t.Fatalf("accepted malformed ls-remote response: %q", output)
		}
	}
}
