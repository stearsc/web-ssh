package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/httpapi"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
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

func TestShutdownCancelsSSHBeforeDatabaseClose(t *testing.T) {
	db := testutil.DB(t)
	cfg := config.Defaults()
	cfg.Server.ShutdownTimeout = 2
	cfg.Auth.CookieSecure = false
	admin, err := auth.New(context.Background(), db, cfg.Auth, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	managed, err := servers.New(context.Background(), db, cipher, cfg)
	if err != nil {
		t.Fatal(err)
	}
	remote := testutil.StartSSH(t)
	gate := make(chan struct{})
	remote.BlockHandshake(gate)
	defer close(gate)
	token, login, _, err := admin.Login(context.Background(), "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	port := remote.Port()
	view, err := managed.Create(context.Background(), login, servers.Input{Name: "shutdown-test", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "ssh-secret"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.PublicOrigin = "http://" + listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	management := &httpapi.Management{Auth: admin, Servers: managed, Config: cfg, Logger: logger}
	go func() {
		done <- serve(ctx, cfg, db, fstest.MapFS{"index.html": {Data: []byte("Web SSH")}}, listener, logger, management)
	}()
	request, err := http.NewRequest("POST", cfg.Server.PublicOrigin+"/api/servers/"+view.ID+"/test", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", cfg.Server.PublicOrigin)
	request.AddCookie(&http.Cookie{Name: "web_ssh_auth", Value: token})
	requestDone := make(chan struct{})
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	go func() {
		defer close(requestDone)
		response, err := client.Do(request)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	select {
	case <-remote.Accepted:
	case <-time.After(time.Second):
		t.Fatal("未进入 SSH 握手")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("退出没有回收 SSH Handler")
	}
	<-requestDone
	if err := db.Ping(); err == nil {
		t.Fatal("SQLite 未在 Handler 退出后关闭")
	}
}
