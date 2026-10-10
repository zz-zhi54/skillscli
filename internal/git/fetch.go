package git

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Checkout 是独立临时仓库中的 detached HEAD 工作树。
// 使用文件完成后，调用方必须调用 Close（通常 defer checkout.Close()）。
// Dir 与 Revision 只描述获取结果；不修改它们或并发调用 Close。
type Checkout struct {
	Dir      string
	Revision string
	tempDir  string
}

// Close 删除本次获取创建的整个临时目录，可重复调用。
// 使用私有路径，避免调用方修改 Dir 后误删其他目录；清理失败可重试。
func (c *Checkout) Close() error {
	if c.tempDir == "" {
		return nil
	}
	if err := os.RemoveAll(c.tempDir); err != nil {
		return err
	}
	c.tempDir = ""
	return nil
}

// Fetch 先通过 Resolve 选择 source 的 ref，再浅获取并检出实际提交。
// ref 为空时选择远程默认分支；分支/标签按下载时的版本获取，完整 SHA 必须精确匹配。
// 返回的 Checkout 包含 detached HEAD 工作树和完整提交 SHA；出错返回 nil，并自动清理已创建的目录。
// 它不修改调用方仓库、不递归获取子模块、不运行 hooks，也不更新任何 lock。
// 成功后调用方须在使用目录完成后调用 Close，并按自身错误策略处理清理失败。
func Fetch(ctx context.Context, source, ref string) (*Checkout, error) {
	source = absoluteSource(source)
	resolved, err := Resolve(ctx, source, ref)
	if err != nil {
		return nil, err
	}
	return fetchResolved(ctx, source, resolved)
}

// fetchResolved 持有本次临时仓库：初始化、浅获取、校验提交，再 detached HEAD 检出。
// 仅固定 SHA 获取失败且未取消时尝试完整历史；其他引用失败直接返回，不换版本兜底。
func fetchResolved(ctx context.Context, source string, ref Reference) (_ *Checkout, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "skillscli-git-*")
	if err != nil {
		return nil, err
	}
	checkout := &Checkout{Dir: dir, tempDir: dir}
	// 命名返回值 err 在 return 后仍可由 defer 修改；将清理错误合并，保留原始失败。
	// 只有失败才清理，成功则把目录所有权交给返回的 Checkout。
	defer func() {
		if err != nil {
			err = errors.Join(err, checkout.Close())
		}
	}()

	format := "sha1"
	if len(ref.Revision) == 64 {
		format = "sha256"
	}
	// 不复制系统/用户模板中的 hooks；仓库本身的脚本只作为文件检出。
	if _, err := run(ctx, dir, "init", "--template=", "--object-format="+format); err != nil {
		return nil, err
	}
	if _, err := run(ctx, dir, "remote", "add", "--", "origin", source); err != nil {
		return nil, err
	}
	_, fetchErr := run(ctx, dir, "fetch", "--no-tags", "--no-recurse-submodules", "--depth=1", "origin", ref.Name)
	fixed := fullSHA(ref.Name)
	if fetchErr != nil {
		if !fixed || ctx.Err() != nil {
			return nil, fetchErr
		}
		// 部分服务器拒绝按历史 SHA 直接获取。只在固定提交失败时回退到
		// 所有公开分支/标签的完整历史，不改用默认分支的版本。
		if err := fetchHistory(ctx, dir); err != nil {
			return nil, errors.Join(fetchErr, err)
		}
	}

	target := "FETCH_HEAD"
	if fixed {
		target = ref.Name
	}
	revision, err := run(ctx, dir, "rev-parse", "--verify", target+"^{commit}")
	if err != nil {
		if fixed {
			return nil, fmt.Errorf("requested commit is unavailable from remote or is not a commit: %w", errors.Join(fetchErr, err))
		}
		return nil, fmt.Errorf("fetched ref does not identify a commit: %w", err)
	}
	if !fullSHA(revision) {
		return nil, errors.New("Git returned an invalid commit SHA")
	}
	if fixed && revision != ref.Revision {
		return nil, errors.New("requested SHA does not identify the exact commit")
	}
	// 分支/标签必须记录 FETCH_HEAD 的实际提交，而不是 ls-remote 时的旧值。
	if _, err := run(ctx, dir, "checkout", "--detach", revision, "--"); err != nil {
		return nil, err
	}
	checkout.Revision = revision
	return checkout, nil
}

// fetchHistory 获取远程公开分支和标签的完整历史，供固定历史 SHA 的直接获取失败后查找。
// 失败的浅获取也可能留下 shallow 边界，须先判断是否需要 --unshallow；这不是通用重试策略。
func fetchHistory(ctx context.Context, dir string) error {
	shallow, err := run(ctx, dir, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return err
	}
	args := []string{"fetch", "--no-tags", "--no-recurse-submodules"}
	if shallow == "true" {
		args = append(args, "--unshallow")
	}
	args = append(args, "origin", "+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*")
	_, err = run(ctx, dir, args...)
	return err
}
