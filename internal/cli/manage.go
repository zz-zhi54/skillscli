package cli

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/zz-zhi54/skillscli/internal/config"
	// import 别名只改变本文件的调用写法，明确区别业务同步包与标准库 sync。
	skillsync "github.com/zz-zhi54/skillscli/internal/sync"
)

// commandPaths 统一解析 add/remove/list 的作用域；这里只求路径，不创建作用域。
func commandPaths(cmd *cobra.Command) (config.Paths, error) {
	global, err := cmd.Flags().GetBool("global")
	if err != nil {
		return config.Paths{}, err
	}
	return config.ResolvePaths(global)
}

// newAddCommand 捕获 --path 列表，在真正执行时只规划并安装新来源。
// 来源及 lock 由 AddSource 在部署成功后一起提交，不先写入未完成的来源。
func newAddCommand() *cobra.Command {
	var selectedPaths []string
	cmd := &cobra.Command{
		Use: "add <git-url>", Short: "添加 Git 来源并安装 Skills", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := commandPaths(cmd)
			if err != nil {
				return errors.New("add failed: cannot resolve scope")
			}
			result, runErr := skillsync.AddSource(cmd.Context(), paths, args[0], selectedPaths)
			if result.Source.Source.Source != "" && !errors.Is(runErr, skillsync.ErrNoSkills) {
				if err := printSyncResult(cmd.OutOrStdout(), result); err != nil {
					return errors.New("add failed: cannot write output")
				}
			}
			if runErr != nil {
				return managementError("add", runErr)
			}
			return nil
		},
	}
	// 把变量地址交给 flag 库，解析参数后回调闭包便能读取更新的切片。
	// nil 保留“未指定路径”的含义，让配置层使用默认 skills。
	cmd.Flags().StringArrayVar(&selectedPaths, "path", nil, "仓库内发现路径，可重复指定（默认 skills）")
	return cmd
}

// newRemoveCommand 按 lock 名称删除一个 Skill；RemoveSkill 不获取 Git 或同步兄弟目录。
// force 仅允许删除已管理的本地修改，不改变所有权和路径安全规则。
func newRemoveCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use: "remove <name>", Short: "删除指定的已管理 Skill", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := commandPaths(cmd)
			if err != nil {
				return errors.New("remove failed: cannot resolve scope")
			}
			if err := skillsync.RemoveSkill(cmd.Context(), paths, args[0], skillsync.Options{Force: force}); err != nil {
				return managementError("remove", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Removed: %s\n", terminalName(args[0])); err != nil {
				return errors.New("remove failed: cannot write output")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "删除本地已修改的受管理 Skill（不删除未管理目录）")
	return cmd
}

// newListCommand 仅展示 ListManaged 的离线结果，不持有写锁或恢复未完成事务。
// 业务状态 unmodified 在用户输出中简写为 ok，不能理解成“与远程最新版相同”。
func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use: "list", Short: "只读列出已管理 Skills 和本地状态", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := commandPaths(cmd)
			if err != nil {
				return errors.New("list failed: cannot resolve scope")
			}
			entries, err := skillsync.ListManaged(paths)
			if err != nil {
				return managementError("list", err)
			}
			if len(entries) == 0 {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "No managed Skills."); err != nil {
					return errors.New("list failed: cannot write output")
				}
			}
			for _, entry := range entries {
				state := string(entry.State)
				if entry.State == skillsync.Unmodified {
					state = "ok"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tsource %d (%s)\n", terminalName(entry.Name), state, entry.SourceIndex+1, syncSourceLabel(entry.Source, entry.SourceIndex)); err != nil {
					return errors.New("list failed: cannot write output")
				}
			}
			return nil
		},
	}
}

// managementError 把已知业务错误翻译为固定安全文本；其余错误不回显 YAML 值、URL 或 Git 输出。
func managementError(command string, err error) error {
	for _, known := range []error{skillsync.ErrSourceExists, skillsync.ErrNoSkills, skillsync.ErrNotManaged, skillsync.ErrModified, skillsync.ErrUnsafe, skillsync.ErrOwnership} {
		if errors.Is(err, known) {
			return fmt.Errorf("%s failed: %s", command, known)
		}
	}
	return fmt.Errorf("%s failed: %s", command, syncFailureReason(err, "could not complete operation"))
}

// terminalName 对不适合直接显示的名字加转义，避免控制字符改变终端输出。
func terminalName(name string) string {
	if safeSyncBasename(name) {
		return name
	}
	return strconv.Quote(name)
}
