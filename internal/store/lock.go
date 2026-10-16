package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LockDataDir holds an OS-backed exclusive server lock until the returned file
// is closed (including process exit). Keep the lock file: unlinking it can let
// another process lock a different inode while the original owner is still live.
// Metadata-only commands such as init-token do not run file recovery.
func LockDataDir(dataDir string) (*os.File, error) {
	path := filepath.Join(dataDir, "filepanel.lock")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("数据目录锁必须是普通文件")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("打开数据目录锁失败: %w", err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("无法取得数据目录独占锁，请检查是否已有服务正在使用此目录: %w", err)
	}
	return f, nil
}
