package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/database"
)

func TestServeAndShutdown(t *testing.T) {
	for _, slowClient := range []bool{false, true} {
		t.Run(fmt.Sprint(slowClient), func(t *testing.T) {
			c := config.Defaults()
			c.Database.Path = filepath.Join(t.TempDir(), "test.db")
			c.Server.ShutdownTimeout = 1
			db, err := database.Open(context.Background(), c.Database, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(context.Background(), db); err != nil {
				db.Close()
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				db.Close()
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- serve(ctx, c, db, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("Web SSH")}}, listener, slog.New(slog.NewTextHandler(io.Discard, nil)))
			}()
			client := &http.Client{Timeout: time.Second}
			defer client.CloseIdleConnections()
			resp, err := client.Get("http://" + listener.Addr().String() + "/api/health")
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatal(resp.StatusCode)
			}
			// 保留一条只发部分首部的真实 TCP 连接，验证退出预算到期时强制解除它。
			var slow net.Conn
			if slowClient {
				slow, err = net.Dial("tcp", listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer slow.Close()
				if _, err := slow.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n")); err != nil {
					t.Fatal(err)
				}
			}

			cancel()
			select {
			case err := <-done:
				if !slowClient && err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("退出超出总期限")
			}
			if slowClient {
				slow.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := slow.Read(make([]byte, 1)); err == nil {
					t.Fatal("慢连接未关闭")
				} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
					t.Fatal("只是测试读超时，连接并未关闭")
				}
			}

			if err := db.Ping(); err == nil {
				t.Fatal("数据库仍然开放")
			}
			t.Log("真实 HTTP、半首部连接强制关闭及 SQLite 关闭已验证")

		})
	}
}
