//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package sync

import (
	"os"
	"syscall"
)

// lockFile 使用 OS 文件锁串行化同作用域写命令，忙时立即失败而非等待。
// 文件描述符关闭或进程退出即释放锁；该锁只约束遵守同一协议的进程。
func lockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
