// Package maintenance 使服务与离线备份/恢复互斥，不提供在线备份。
package maintenance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Lock 直接持有内核描述符，不安装 GC 终结器。退出超时后即使 Go 对象不可达，
// 锁仍须覆盖所有可能使用 SQLite 的任务，直至进程真正退出。
// 调用方仅在数据库及业务任务全部关闭后显式 Close。
type Lock struct {
	mu sync.Mutex
	fd int
}

// Close 在资源安全关闭后释放锁，重复调用不再操作描述符。
func (l *Lock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fd < 0 {
		return nil
	}
	err := unix.Close(l.fd)
	l.fd = -1
	return err
}

// Acquire 在打开数据库之前持有，并覆盖数据库关闭及全部文件复制时段。
// 只规范化锁身份，不修改传给 SQLite 或离线维护的数据库路径。
func Acquire(databasePath string) (*Lock, error) {
	path, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, errors.New("维护锁路径无效")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("维护锁目录不可用")
	}
	canonical, before, err := databaseIdentity(path)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(canonical+".maintenance.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, errors.New("维护锁不可用")
	}
	lock := &Lock{fd: fd}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		lock.Close()
		return nil, errors.New("维护锁权限无效")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("服务或离线维护正在运行，请先停止并等待进程退出")
	}
	// 申请期间路径或锁目录项发生替换时拒绝继续，不能拿着旧 fd 启动数据库。
	afterPath, after, identityErr := databaseIdentity(path)
	var current unix.Stat_t
	hasSameDatabase := identityErr == nil && afterPath == canonical && sameDatabase(before, after)
	hasSameLock := unix.Lstat(canonical+".maintenance.lock", &current) == nil && current.Dev == stat.Dev && current.Ino == stat.Ino && current.Nlink == 1
	if !hasSameDatabase || !hasSameLock {
		lock.Close()
		return nil, errors.New("维护锁路径身份发生变化")
	}
	return lock, nil
}

// databaseIdentity 解析父目录及数据库符号链接，允许最终数据库文件尚未创建。
// 不存在的父目录、链接循环、非普通文件及无法统一身份的硬链接均拒绝操作。
func databaseIdentity(path string) (string, *unix.Stat_t, error) {
	for links := 0; links < 40; links++ {
		// 保留链接目标中的 ..，让文件系统先解析前面的符号链接；不能先 Clean。
		separator := strings.LastIndexByte(path, os.PathSeparator)
		parent, err := filepath.EvalSymlinks(path[:separator+1])
		if err != nil {
			return "", nil, errors.New("无法确定数据库目录身份")
		}
		name := path[separator+1:]
		if name == "" {
			return "", nil, errors.New("数据库路径不是文件")
		}
		path = parent + string(os.PathSeparator) + name
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return path, nil, nil
		}
		if err != nil {
			return "", nil, errors.New("无法确定数据库文件身份")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return "", nil, errors.New("无法确定数据库链接身份")
			}
			if !filepath.IsAbs(target) {
				target = parent + string(os.PathSeparator) + target
			}
			path = target
			continue
		}
		var stat unix.Stat_t
		if unix.Lstat(path, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
			return "", nil, errors.New("数据库文件身份不安全")
		}
		return path, &stat, nil
	}
	return "", nil, errors.New("数据库链接层数过多")
}

func sameDatabase(before, after *unix.Stat_t) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return before.Dev == after.Dev && before.Ino == after.Ino
}
