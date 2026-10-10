// Package cli 将 Cobra 参数和 context 转交给 sync 包，并统一生成安全的终端输出。
// CLI 不自行复制 Skill 或修改 lock，四个命令共享业务层的安全规则。
package cli

import "github.com/spf13/cobra"

// NewRootCommand 创建一次独立的命令树，不使用包级共享命令对象。
// 只注册已实现的业务命令。
func NewRootCommand(version string) *cobra.Command {
	// &cobra.Command{...} 先用字段名初始化结构体，再取得它的指针。
	// RunE 是函数类型字段；匿名函数被 Cobra 在参数校验通过后调用，而非此时执行。
	cmd := &cobra.Command{
		Use:     "skillscli",
		Short:   "管理 Agent Skills",
		Version: version,
		// 根命令没有位置参数，未实现的命令不能被当成成功执行的请求。
		Args: cobra.NoArgs,
		// 错误由 main 统一输出，避免 Cobra 和入口重复打印同一个错误。
		// 执行失败时也不自动附带整段帮助，用户可以显式使用 --help 查看。
		SilenceErrors: true,
		SilenceUsage:  true,
		// _ 是空白标识符：签名保留参数位置，但此回调不使用位置参数。
		RunE: func(cmd *cobra.Command, _ []string) error {
			// 直接运行根命令只显示帮助，
			// 不读取或写入配置，也不创建安装目录。
			return cmd.Help()
		},
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.Flags().BoolP("version", "v", false, "显示版本号")
	// PersistentFlags 注册的是可被子命令继承的参数。
	// false 表示默认项目作用域；--global 和 -g 是同一个布尔参数的长、短写法。
	// 此处只声明参数，具体作用域由业务命令读取并决定。
	cmd.PersistentFlags().BoolP("global", "g", false, "使用全局作用域（默认使用当前项目）")
	// 不让 Cobra 自动添加 completion 命令，保持当前阶段约定的最小命令集合。
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.AddCommand(newAddCommand(), newSyncCommand(), newRemoveCommand(), newListCommand())
	return cmd
}
