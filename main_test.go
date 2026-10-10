package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuiltVersion(t *testing.T) {
	data, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "skillscli")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	// 从没有 VERSION、配置或 Git 的目录运行，验证版本已经包含在程序中。
	runDir := t.TempDir()
	for _, flag := range []string{"-v", "--version"} {
		t.Run(flag, func(t *testing.T) {
			cmd := exec.Command(binary, flag)
			cmd.Dir = runDir
			cmd.Env = append(os.Environ(), "PATH=", "HOME="+runDir, "USERPROFILE="+runDir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("version: %v\n%s", err, out)
			}
			if want := strings.TrimSpace(string(data)) + "\n"; string(out) != want {
				t.Fatalf("got %q, want %q", out, want)
			}
		})
	}
	entries, err := os.ReadDir(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("version command wrote runtime files: %v", entries)
	}
}
