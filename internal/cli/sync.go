package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/zz-zhi54/skillscli/internal/config"
	skillsync "github.com/zz-zhi54/skillscli/internal/sync"
)

// newSyncCommand 把 --force 和作用域交给 Run，再按来源顺序报告结果。
// Run 完成恢复、全局规划和来源级部署；CLI 不执行 skipped/failed 来源的候选操作。
func newSyncCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "同步配置中的 Skills 来源",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			global, err := cmd.Flags().GetBool("global")
			if err != nil {
				return err
			}
			force, err := cmd.Flags().GetBool("force")
			if err != nil {
				return err
			}
			paths, err := config.ResolvePaths(global)
			if err != nil {
				return errors.New("sync failed: cannot resolve scope")
			}
			results, runErr := skillsync.Run(cmd.Context(), paths, skillsync.Options{Force: force})
			failed := false
			for _, result := range results {
				if err := printSyncResult(cmd.OutOrStdout(), result); err != nil {
					return errors.New("sync failed: cannot write output")
				}
				if result.Status == skillsync.Skipped || result.Status == skillsync.Failed || result.Err != nil {
					failed = true
				}
			}
			// 底层错误可能包含配置值、URL 凭证或 Git 输出，只在 CLI 边界翻译为安全文本。
			if runErr != nil && !failed {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "! sync Failed: %s\n", syncFailureReason(runErr, "could not complete synchronization")); err != nil {
					return errors.New("sync failed: cannot write output")
				}
			}
			if runErr != nil || failed {
				return errors.New("sync failed")
			}
			return nil
		},
	}
	cmd.Flags().Bool("force", false, "覆盖已管理 Skill 的本地修改（不覆盖未管理目录）")
	return cmd
}

// printSyncResult 接受 io.Writer 接口：终端和测试用 bytes.Buffer 都有 Write 方法，
// 无需声明 implements 就能作为输出目标。这里只报告结果，不决定或执行文件操作。
func printSyncResult(out io.Writer, result skillsync.Result) error {
	label := syncSourceLabel(result.Source.Source.Source, result.Source.Index)
	switch result.Status {
	case skillsync.Ready:
		if _, err := fmt.Fprintf(out, "✓ %s\n", label); err != nil {
			return err
		}
		for _, group := range []struct {
			kind  skillsync.ActionKind
			label string
		}{{skillsync.Add, "Added"}, {skillsync.Update, "Updated"}, {skillsync.Delete, "Removed"}} {
			var names []string
			for _, action := range result.Source.Actions {
				if action.Kind == group.kind {
					names = append(names, action.Name)
				}
			}
			if len(names) != 0 {
				if _, err := fmt.Fprintf(out, "  %s: %s\n", group.label, strings.Join(names, ", ")); err != nil {
					return err
				}
			}
		}
		return nil
	case skillsync.Unchanged:
		_, err := fmt.Fprintf(out, "- %s No changes\n", label)
		return err
	case skillsync.Skipped:
		// 不回显 Conflict.Reason：路径安全检查产生的理由也可能含源地址或敏感值。
		var reasons []string
		seen := make(map[string]bool)
		for _, conflict := range result.Source.Conflicts {
			var reason string
			switch conflict.Kind {
			case skillsync.LocalModification:
				reason = "local modifications (use --force to overwrite managed Skills)"
			case skillsync.UnmanagedName:
				reason = "unmanaged target exists"
			case skillsync.CrossSourceName:
				reason = "Skill name is claimed by multiple sources"
			case skillsync.UnsafeLocal:
				reason = "unsafe local path"
			default:
				reason = "unresolved conflicts"
			}
			if safeSyncBasename(conflict.Name) {
				reason = conflict.Name + ": " + reason
			}
			if !seen[reason] {
				reasons = append(reasons, reason)
				seen[reason] = true
			}
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "unresolved conflicts")
		}
		_, err := fmt.Fprintf(out, "! %s Skipped: %s\n", label, strings.Join(reasons, "; "))
		return err
	default:
		_, err := fmt.Fprintf(out, "! %s Failed: %s\n", label, syncFailureReason(result.Err, "could not synchronize source"))
		return err
	}
}

// syncFailureReason 只放行人工恢复所需的安全诊断，普通内部错误使用固定替代文本。
func syncFailureReason(err error, fallback string) string {
	// errors.As 需要写回找到的错误指针，所以传 &recovery（即指针的地址）。
	// 与字符串比较不同，它能沿包装/组合错误查找具体错误类型。
	var recovery *skillsync.RecoveryError
	if errors.As(err, &recovery) {
		// 恢复层保证此错误仅包含本地事务路径和安全原因，必须保留人工恢复所需的位置。
		return recovery.Error()
	}
	return fallback
}

// syncSourceLabel 只展示仓库 basename；不展示 URL userinfo、query、fragment 或 SCP 登录信息。
func syncSourceLabel(source string, index int) string {
	fallback := fmt.Sprintf("source %d", index+1)
	var repositoryPath string
	if strings.Contains(source, "://") {
		u, err := url.Parse(source)
		if err != nil {
			return fallback
		}
		repositoryPath = u.Path
	} else {
		repositoryPath, _, _ = strings.Cut(source, "?")
		repositoryPath, _, _ = strings.Cut(repositoryPath, "#")
		// Git 的 SCP 风格 host:path（也允许不带用户名）。
		if host, remotePath, ok := strings.Cut(repositoryPath, ":"); ok && !strings.ContainsAny(host, "/\\") {
			repositoryPath = remotePath
		}
	}
	name := strings.TrimSuffix(path.Base(repositoryPath), ".git")
	if !safeSyncBasename(name) {
		return fallback
	}
	return name
}

// safeSyncBasename 限制可直接显示的标签字符；这不是安装目录名的完整业务校验。
// 对字符串 range 得到的 r 是 Unicode 码点，i 是字节偏移；此处只判断是否首字符。
func safeSyncBasename(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 128 {
		return false
	}
	for i, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && (i == 0 || r != '-' && r != '_' && r != '.') {
			return false
		}
	}
	return true
}
