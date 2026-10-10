package sync

import (
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

// lockFile 对固定字节请求非阻塞排他锁；句柄关闭或进程退出即释放。
// Windows 实现由文件名后缀选择，不与 Unix 的 flock 实现同时编译。
func lockFile(file *os.File) error {
	// OVERLAPPED 必须有真实存储供系统调用取指针；这里零值指定偏移为 0 的字节。
	var overlapped syscall.Overlapped
	// LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY，锁住一个固定字节。
	ok, _, err := lockFileEx.Call(file.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		return err
	}
	return nil
}
