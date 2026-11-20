package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/maintenance"
)

// 使用真实 HTTP Handler 模拟忽略取消的业务任务；通道决定退出时机，
// 任务获准继续后仍访问真实 SQLite，证明数据库没有被提前关闭。
type shutdownTaskFS struct {
	fs.FS
	db      *sql.DB
	entered chan struct{}
	release chan struct{}
	queried chan error
}

func (f *shutdownTaskFS) Open(name string) (fs.File, error) {
	if name == "blocked.txt" {
		close(f.entered)
		<-f.release
		var value int
		err := f.db.QueryRowContext(context.Background(), "SELECT 1").Scan(&value)
		f.queried <- err
	}
	return f.FS.Open(name)
}

func TestMaintenanceLockCoversProcessExit(t *testing.T) {
	for _, mode := range []string{"blocked", "normal"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "service.db")
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMaintenanceLockProcessHelper$")
			cmd.Env = append(os.Environ(), "WEB_SSH_LOCK_HELPER="+mode, "WEB_SSH_LOCK_DATABASE="+path)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { input.Close(); cancel(); cmd.Wait() }()
			lines := make(chan string)
			go func() {
				defer close(lines)
				scanner := bufio.NewScanner(output)
				for scanner.Scan() {
					select {
					case lines <- scanner.Text():
					case <-ctx.Done():
						return
					}
				}
			}()
			read := func(want string) {
				t.Helper()
				select {
				case line := <-lines:
					if line != want {
						t.Fatalf("子进程同步点: got %q want %q", line, want)
					}
				case <-ctx.Done():
					t.Fatal("等待子进程同步点超时", want)
				}
			}
			read("returned")
			// 此时 serve 已返回，子进程由管道阻塞，尚未执行真正的进程退出。
			lock, err := maintenance.Acquire(path)
			if mode == "blocked" {
				if err == nil {
					lock.Close()
					t.Fatal("业务未结束就释放了维护锁")
				}
				if _, err = fmt.Fprintln(input, "release-task"); err != nil {
					t.Fatal(err)
				}
				read("task-queried-database")
				if lock, err = maintenance.Acquire(path); err == nil {
					lock.Close()
					t.Fatal("强制退出前维护锁丢失")
				}
			} else {
				if err != nil {
					t.Fatal("正常退出没有释放维护锁", err)
				}
				lock.Close()
			}
			if _, err = fmt.Fprintln(input, "exit-process"); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if mode == "blocked" {
				var processExit *exec.ExitError
				if !errors.As(err, &processExit) || processExit.ExitCode() != 1 {
					t.Fatalf("强制退出码不符合预期: %v: %s", err, diagnostic.String())
				}
			} else if err != nil {
				t.Fatalf("子进程退出失败: %v: %s", err, diagnostic.String())
			}
			lock, err = maintenance.Acquire(path)
			if err != nil {
				t.Fatal("进程退出后锁未回收", err)
			}
			lock.Close()
		})
	}
}

func TestMaintenanceLockProcessHelper(t *testing.T) {
	mode := os.Getenv("WEB_SSH_LOCK_HELPER")
	if mode == "" {
		return
	}
	cfg := config.Defaults()
	cfg.Database.Path = os.Getenv("WEB_SSH_LOCK_DATABASE")
	cfg.Server.ShutdownTimeout = 1
	lock, err := maintenance.Acquire(cfg.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), cfg.Database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	assets := &shutdownTaskFS{FS: fstest.MapFS{"blocked.txt": {Data: []byte("ok")}}, db: db, entered: make(chan struct{}), release: make(chan struct{}), queried: make(chan error, 1)}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, cfg, db, lock, assets, listener, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	client := &http.Client{Timeout: 10 * time.Second}
	requestDone := make(chan struct{})
	endpoint := "/api/health"
	if mode == "blocked" {
		endpoint = "/blocked.txt"
	}
	go func() {
		defer close(requestDone)
		resp, err := client.Get("http://" + listener.Addr().String() + endpoint)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	if mode == "blocked" {
		<-assets.entered
	} else {
		<-requestDone
	}
	cancel()
	err = <-done
	if mode == "blocked" {
		if err == nil {
			t.Fatal("阻塞任务未触发退出超时")
		}
		if err := db.Ping(); err != nil {
			t.Fatal("业务未结束就关闭了数据库", err)
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Ping(); err == nil {
			t.Fatal("正常退出未关闭数据库")
		}
	}
	// 丢弃 Go 引用并强制 GC，锁仍应由内核保留至进程退出。
	lock = nil
	runtime.GC()
	runtime.GC()
	fmt.Println("returned")
	control := bufio.NewScanner(os.Stdin)
	if !control.Scan() {
		t.Fatal("缺少退出控制信号")
	}
	if mode == "blocked" {
		close(assets.release)
		if err := <-assets.queried; err != nil {
			t.Fatal("超时返回后任务无法访问数据库", err)
		}
		fmt.Println("task-queried-database")
		if !control.Scan() {
			t.Fatal("缺少进程退出信号")
		}
	}
	if mode == "blocked" {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestRunStartupFailureReleasesMaintenanceLock(t *testing.T) {
	for _, stage := range []string{"before_database", "after_database"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("APP_ENCRYPTION_KEY_FILE", "")
			if err := os.Unsetenv("APP_ENCRYPTION_KEY_FILE"); err != nil {
				t.Fatal(err)
			}
			key := "invalid"
			if stage == "after_database" {
				key = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
			}
			t.Setenv("APP_ENCRYPTION_KEY", key)
			t.Setenv("APP_ADMIN_PASSWORD", "short")
			cfg := config.Defaults()
			cfg.Database.Path = filepath.Join(t.TempDir(), "startup.db")
			if err := run(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
				t.Fatal("启动故障未返回")
			}
			lock, err := maintenance.Acquire(cfg.Database.Path)
			if err != nil {
				t.Fatal("启动失败后维护锁未释放", err)
			}
			lock.Close()
			_, err = os.Stat(cfg.Database.Path)
			if stage == "after_database" && err != nil {
				t.Fatal("未覆盖数据库打开后的失败路径", err)
			}
			if stage == "before_database" && !os.IsNotExist(err) {
				t.Fatal("密钥检查前打开了数据库", err)
			}
		})
	}
}
