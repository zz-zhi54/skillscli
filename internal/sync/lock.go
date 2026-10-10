package sync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// acquireScope 获取非阻塞的作用域排他锁，成功后由调用方 Close 文件释放。
// 锁文件不删除：删除会使不同进程锁住不同 inode。
// 操作系统在进程退出时释放锁，不需要 PID 检测或陈旧锁抢占。
func acquireScope(pathsDir string) (*os.File, error) {
	path := filepath.Join(pathsDir, ".skillscli.lock")
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("scope lock is not a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	// 对比已打开句柄与当前路径，避免检查后路径被换成另一对象或链接。
	actual, err := file.Stat()
	current, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !current.Mode().IsRegular() || !os.SameFile(actual, current) {
		file.Close()
		return nil, fmt.Errorf("scope lock changed while opening")
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("scope is busy or cannot be locked: %w", err)
	}
	return file, nil
}
