// package main 配合 func main 定义可执行程序；其他包通过 import 被入口使用。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/zz-zhi54/skillscli/internal/cli"
)

// main 是程序的进程入口，只负责启动命令和处理最终错误。
// 具体命令逻辑放在 internal/cli，配置逻辑放在 internal/config，
// 这样内部代码不需要直接退出进程，也更容易在测试中复用。
func main() {
	// Execute 让 Cobra 解析参数并执行对应命令。错误统一交给入口输出，
	// 使用标准错误流，避免与命令的正常输出混在一起；退出码 1 表示执行失败。
	// 常见退出信号取消 Git/部署，让来源回滚及 Plan.Close 有机会完成。
	// ctx 沿 Cobra -> sync -> Git/部署传递取消信号；取消不会自动撤销文件操作，
	// 回滚仍由事务代码负责。stop 用于释放信号订阅，正常返回时由 defer 调用。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.NewRootCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		// os.Exit 不执行 main 中的 defer；业务层必须在返回错误前完成回滚和资源关闭。
		os.Exit(1)
	}
}
