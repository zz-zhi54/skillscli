package git

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Reference 是解析后的完整分支/标签引用或固定提交 SHA。
// Name 用作 fetch 的目标；Revision 是查询时的对象 SHA，附注标签优先使用剥离结果。
// 分支/标签在下载时仍可能变化，实际提交以 Checkout.Revision 为准。
// 查询不保证对象一定是提交；固定 SHA 的远程可获取性与最终提交类型在 Fetch 时验证。
type Reference struct {
	Name     string
	Revision string
}

// Resolve 解析 source 的 ref：空值查询远程默认分支，普通名称同时查询分支与标签，不猜测名称类型。
// 完整 refs/heads/ 或 refs/tags/ 指定唯一命名空间；普通名称同时命中分支和标签则报歧义错误。
// 完整 SHA 直接规范化为小写固定版本，不查询远程；其他 refs 命名空间及 revision 表达式不受支持。
// 成功返回查询结果，失败返回零值 Reference；远程对象的可获取性与提交类型仍需 Fetch 验证。
func Resolve(ctx context.Context, source, ref string) (Reference, error) {
	if err := ctx.Err(); err != nil {
		return Reference{}, err
	}
	if strings.TrimSpace(source) == "" {
		return Reference{}, errors.New("Git source is required")
	}
	if fullSHA(ref) {
		sha := strings.ToLower(ref)
		return Reference{Name: sha, Revision: sha}, nil
	}
	source = absoluteSource(source)
	if ref == "" {
		output, err := run(ctx, "", "ls-remote", "--symref", "--", source, "HEAD")
		if err != nil {
			return Reference{}, err
		}
		refs, head, err := parseRemote(output)
		if err != nil {
			return Reference{}, err
		}
		if !strings.HasPrefix(head, "refs/heads/") || !fullSHA(refs["HEAD"]) {
			return Reference{}, errors.New("remote default branch is missing or has no commit")
		}
		if _, err := run(ctx, "", "check-ref-format", head); err != nil {
			return Reference{}, fmt.Errorf("validate remote default branch: %w", err)
		}
		return Reference{Name: head, Revision: refs["HEAD"]}, nil
	}

	var names []string
	switch {
	case strings.HasPrefix(ref, "refs/heads/"), strings.HasPrefix(ref, "refs/tags/"):
		names = []string{ref}
	case strings.HasPrefix(ref, "refs/"):
		return Reference{}, errors.New("unsupported ref: use refs/heads/, refs/tags/, or a full commit SHA")
	default:
		names = []string{"refs/heads/" + ref, "refs/tags/" + ref}
	}
	for _, name := range names {
		if _, err := run(ctx, "", "check-ref-format", name); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				return Reference{}, errors.New("invalid Git ref")
			}
			return Reference{}, err
		}
	}
	args := []string{"ls-remote", "--symref", "--", source}
	for _, name := range names {
		args = append(args, name)
		if strings.HasPrefix(name, "refs/tags/") {
			args = append(args, name+"^{}")
		}
	}
	output, err := run(ctx, "", args...)
	if err != nil {
		return Reference{}, err
	}
	refs, _, err := parseRemote(output)
	if err != nil {
		return Reference{}, err
	}
	var result Reference
	for _, name := range names {
		// map 的双返回值区分“没有这个完整引用”和值为空，避免接受 pattern 的其他匹配项。
		sha, exists := refs[name]
		if !exists {
			continue
		}
		if result.Name != "" {
			return Reference{}, errors.New("ambiguous Git ref: branch and tag share the name; use refs/heads/<name> or refs/tags/<name>")
		}
		if peeled, exists := refs[name+"^{}"]; exists {
			sha = peeled
		}
		result = Reference{Name: name, Revision: sha}
	}
	if result.Name == "" {
		return Reference{}, errors.New("Git ref does not exist on remote")
	}
	return result, nil
}

// fullSHA 只检查完整 SHA-1/SHA-256 的长度及十六进制编码，不证明对应对象存在或是提交。
func fullSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// parseRemote 将 ls-remote 输出拆成引用到对象 SHA 的映射，以及 HEAD 的符号引用目标。
// 调用方只使用完全相等的引用键，因为 ls-remote 的 pattern 可能匹配其他路径后缀。
// 附注标签的 ^{} 是剥离后的对象，最终提交类型仍由下载后的 ^{commit} 检查。
func parseRemote(output string) (map[string]string, string, error) {
	refs := make(map[string]string)
	var head string
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		value, name, ok := strings.Cut(line, "\t")
		if !ok || name == "" {
			return nil, "", errors.New("invalid git ls-remote response")
		}
		if target, symbolic := strings.CutPrefix(value, "ref: "); symbolic {
			if name == "HEAD" {
				head = target
			}
			continue
		}
		if !fullSHA(value) {
			return nil, "", errors.New("invalid commit SHA in git ls-remote response")
		}
		refs[name] = strings.ToLower(value)
	}
	return refs, head, nil
}
