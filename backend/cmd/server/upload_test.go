package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pkg/sftp"
	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/httpapi"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/session"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
	browser "github.com/stearsc/web-ssh/backend/internal/websocket"
	"golang.org/x/crypto/ssh"
)

func TestServeUploadOutlivesRequestBudgetWithProgress(t *testing.T) {
	db := testutil.DB(t)
	cfg := config.Defaults()
	cfg.Server.RequestTimeout = 1
	cfg.Ssh.ConnectTimeout = 1
	cfg.Server.ShutdownTimeout = 3
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.PublicOrigin = "http://" + listener.Addr().String()
	admin, err := auth.New(t.Context(), db, cfg.Auth, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := secret.New(bytes.Repeat([]byte{9}, 32))
	managed, err := servers.New(t.Context(), db, cipher, cfg)
	if err != nil {
		t.Fatal(err)
	}
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{FileChannel: func(incoming ssh.NewChannel, _ <-chan struct{}) {
		ch, requests, e := incoming.Accept()
		if e != nil {
			return
		}
		defer ch.Close()
		r := <-requests
		if r == nil {
			return
		}
		_ = r.Reply(true, nil)
		srv, e := sftp.NewServer(ch)
		if e != nil {
			t.Error(e)
			return
		}
		_ = srv.Serve()
		_ = srv.Close()
	}})
	token, login, _, err := admin.Login(t.Context(), "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	port := remote.Port()
	target, err := managed.Create(t.Context(), login, servers.Input{Name: "upload", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "ssh-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, probe, err := managed.Test(t.Context(), login, target.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = managed.Confirm(t.Context(), login, target.ID, probe.ProbeID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := session.New(ctx, db, managed, cfg, logger)
	sockets := browser.New(cfg, logger)
	view, _, err := sessions.Create(login, target.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- serve(ctx, cfg, db, nil, fstest.MapFS{"index.html": {Data: []byte("Web SSH")}}, listener, logger, &httpapi.Management{Auth: admin, Servers: managed, Sessions: sessions, Websocket: sockets, Config: cfg, Logger: logger})
	}()
	defer func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(4 * time.Second):
			t.Error("服务未回收上传资源")
		}
	}()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pipe, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	producer := make(chan error, 1)
	go func() {
		if err := form.WriteField("directory", directory); err != nil {
			producer <- err
			return
		}
		part, err := form.CreateFormFile("file", "slow")
		if err != nil {
			producer <- err
			return
		}
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for range 8 {
			if _, err = part.Write([]byte("progress")); err != nil {
				producer <- err
				return
			}
			<-ticker.C
		}
		err = form.Close()
		_ = writer.Close()
		producer <- err
	}()
	request, err := http.NewRequest("POST", cfg.Server.PublicOrigin+"/api/sessions/"+view.ID+"/files", pipe)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", cfg.Server.PublicOrigin)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: "web_ssh_auth", Value: token})
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		_ = writer.CloseWithError(err)
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatal(response.StatusCode, string(data), err)
	}
	if err = <-producer; err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(directory, "slow"))
	if err != nil || string(actual) != "progressprogressprogressprogressprogressprogressprogressprogress" {
		t.Fatal(string(actual), err)
	}
	// 相邻 check 路由保持固定期限例外之外的分类，运行入口也只对精确上传路由跳过 WithTimeout。
	if httpapi.IsFileUploadRequest(httptest.NewRequest("POST", "/api/sessions/"+view.ID+"/files/check", nil)) {
		t.Fatal("check 被错误豁免")
	}
}
