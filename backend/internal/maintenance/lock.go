// Package maintenance 使服务与离线备份/恢复互斥，不提供在线备份。
package maintenance

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Acquire 在打开数据库之前持有，并覆盖数据库关闭及全部文件复制时段。
func Acquire(databasePath string) (*os.File, error) {
	path, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, errors.New("维护锁路径无效")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("维护锁目录不可用")
	}
	fd, err := unix.Open(path+".maintenance.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, errors.New("维护锁不可用")
	}
	file := os.NewFile(uintptr(fd), "maintenance-lock")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		file.Close()
		return nil, errors.New("维护锁权限无效")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("服务或离线维护正在运行，请先停止并等待进程退出")
	}
	return file, nil
}
