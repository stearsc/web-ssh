package maintenance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stearsc/web-ssh/backend/internal/maintenance"
)

func TestMaintenanceLockDatabaseAliases(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing_database", true: "existing_database"}[exists], func(t *testing.T) {
			root := t.TempDir()
			realDir := filepath.Join(root, "real")
			if err := os.Mkdir(realDir, 0700); err != nil {
				t.Fatal(err)
			}
			database := filepath.Join(realDir, "app.db")
			if exists {
				if err := os.WriteFile(database, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			parentAlias := filepath.Join(root, "parent-alias")
			fileAlias := filepath.Join(root, "file-alias.db")
			if err := os.Symlink(realDir, parentAlias); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("real/app.db", fileAlias); err != nil {
				t.Fatal(err)
			}
			paths := []string{database, filepath.Join(parentAlias, "app.db"), fileAlias}
			// 每种入口轮流先持锁，其他入口均须争抢同一把锁。
			for _, first := range paths {
				lock, err := maintenance.Acquire(first)
				if err != nil {
					t.Fatal(err)
				}
				for _, other := range paths {
					if second, err := maintenance.Acquire(other); err == nil {
						second.Close()
						t.Errorf("别名取得第二把锁: %s -> %s", first, other)
					}
				}
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMaintenanceLockMissingNestedDirectoryAndSymlinkDotDot(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "parent")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "parent", "new", "app.db")
	lock, err := maintenance.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := maintenance.Acquire(filepath.Join(real, "new", "app.db")); err == nil {
		second.Close()
		t.Error("新建父目录未统一身份")
	}
	lock.Close()
	// 链接内容先经过 nested 的符号链接再处理 ..，不得提前 Clean。
	if err := os.Symlink(filepath.Join(real, "nested"), filepath.Join(root, "jump")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.db")
	if err := os.Symlink("jump/../missing.db", alias); err != nil {
		t.Fatal(err)
	}
	lock, err = maintenance.Acquire(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if second, err := maintenance.Acquire(filepath.Join(real, "missing.db")); err == nil {
		second.Close()
		t.Fatal("链接中的 .. 导致锁身份偏离实际数据库")
	}
}

func TestMaintenanceLockRejectsUnsafeIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(root, "hard.db")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(root, "loop.db")
	if err := os.Symlink("loop.db", loop); err != nil {
		t.Fatal(err)
	}
	missingParent := filepath.Join(root, "dangling.db")
	if err := os.Symlink("absent/app.db", missingParent); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{path, hardlink, loop, missingParent, root} {
		if lock, err := maintenance.Acquire(unsafe); err == nil {
			lock.Close()
			t.Errorf("未拒绝不安全身份: %s", unsafe)
		}
	}
}
