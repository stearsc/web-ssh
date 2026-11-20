package privatekey

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/stearsc/web-ssh/backend/internal/fault"
	"golang.org/x/sys/unix"
)

// 每次从根目录逐层以 O_NOFOLLOW 打开，既不接受父目录符号链接，也不沿旧 fd 扫错卷。
func openDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	for _, name := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, openErr := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
		}
		fd = next
	}
	dir := os.NewFile(uintptr(fd), "private-key-directory")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0777 != 0700 {
		dir.Close()
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	return dir, nil
}

func safeID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, ch := range id {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func openObject(dir *os.File, name string, object Object) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "private-key-object")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink > 2 || !object.Device.Valid || !object.Inode.Valid || int64(stat.Dev) != object.Device.Int64 || int64(stat.Ino) != object.Inode.Int64 {
		file.Close()
		return nil, errors.New("object_changed")
	}
	return file, nil
}

func readObject(dir *os.File, object Object) ([]byte, error) {
	file, err := openObject(dir, object.ID+".key", object)
	if err != nil {
		return nil, fault.Error("PRIVATE_KEY_UNAVAILABLE")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxFileSize+1))
	if err != nil || len(raw) != object.Size || sha256.Sum256(raw) != object.Digest {
		clear(raw)
		return nil, fault.Error("PRIVATE_KEY_UNAVAILABLE")
	}
	return raw, nil
}

// unlink 前保持目录 fd 和对象 fd，最后复核目录项身份，拒绝未知替换文件。
// 同 UID 的恶意进程仍属于宿主信任边界；专用 0700 目录不能共享写权限。
func removeObject(dir *os.File, object Object) error {
	for _, suffix := range []string{".upload", ".key"} {
		name := object.ID + suffix
		file, err := openObject(dir, name, object)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("object_changed")
		}
		var current unix.Stat_t
		err = unix.Fstatat(int(dir.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil && int64(current.Dev) == object.Device.Int64 && int64(current.Ino) == object.Inode.Int64 {
			err = unix.Unlinkat(int(dir.Fd()), name, 0)
		} else if err == nil {
			err = errors.New("object_changed")
		}
		file.Close()
		if err != nil {
			return err
		}
	}
	return dir.Sync()
}
