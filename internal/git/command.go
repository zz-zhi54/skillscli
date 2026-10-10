// Package git 使用系统 Git 解析远程引用，并在独立临时仓库获取指定提交。
// 来源地址和认证交给用户既有 Git 配置处理，本包不拼接 shell 命令或维护永久缓存。
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// run 用独立参数调用系统 Git，返回去除首尾空白的 stdout；args 必须以 Git 子命令开头。
// dir 为空时仅查询或验证引用，非空时显式指定临时仓库与工作树。
// 屏蔽调用方的仓库定位环境变量，但保留用户 Git 配置、SSH 和凭证环境。
func run(ctx context.Context, dir string, args ...string) (string, error) {
	options := []string{"--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "submodule.recurse=false"}
	if dir != "" {
		options = append(options, "--git-dir="+filepath.Join(dir, ".git"), "--work-tree="+dir)
	}
	// CommandContext 将取消/超时关联到 Git 进程；切片后的 ... 展开为独立参数，不经过 shell。
	cmd := exec.CommandContext(ctx, "git", append(options, args...)...)
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
			"GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_PREFIX",
			"GIT_SHALLOW_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	// stderr 可能含认证 URL、url.insteadOf 改写结果或凭证助手输出。
	// 不回显参数或原始诊断，保留执行错误及退出码，避免不完整的脱敏规则。
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return strings.TrimSpace(output.String()), nil
}

// absoluteSource 将本机可访问的来源路径转为绝对路径，避免切换到临时仓库后相对路径失效。
// Stat 或绝对化失败时保留原值，由 Git 判断来源；不自行解析或改写 URL、SCP 地址。
func absoluteSource(source string) string {
	if _, err := os.Stat(source); err == nil {
		if absolute, err := filepath.Abs(source); err == nil {
			return absolute
		}
	}
	return source
}
