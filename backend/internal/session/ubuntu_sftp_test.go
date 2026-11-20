package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

// 仅在显式配置专用验收目录及测试账号时运行；不读取工作区部署配置或探测其他主机。
// 测试会写入并清理随机前缀文件，不自动创建目录，不输出密码。
func TestUbuntuSFTPAcceptance(t *testing.T) {
	host, user, password, directory := os.Getenv("WEB_SSH_UBUNTU_HOST"), os.Getenv("WEB_SSH_UBUNTU_USER"), os.Getenv("WEB_SSH_UBUNTU_PASSWORD"), os.Getenv("WEB_SSH_UBUNTU_DIRECTORY")
	if host == "" || user == "" || password == "" || directory == "" {
		t.Skip("未配置真实 Ubuntu SSH/SFTP 验收环境：需 WEB_SSH_UBUNTU_HOST/USER/PASSWORD/DIRECTORY")
	}
	if !path.IsAbs(directory) || directory == "/" {
		t.Fatal("需指定已存在的专用验收目录")
	}
	port := 22
	if raw := os.Getenv("WEB_SSH_UBUNTU_PORT"); raw != "" {
		var err error
		port, err = strconv.Atoi(raw)
		if err != nil {
			t.Fatal("端口无效")
		}
	}
	db := testutil.DB(t)
	cipher, _ := secret.New(bytes.Repeat([]byte{3}, 32))
	cfg := config.Defaults()
	servers, err := server.New(t.Context(), db, cipher, cfg)
	if err != nil {
		t.Fatal(err)
	}
	login := &auth.Session{ID: strings.Repeat("a", 32), Expires: time.Now().Add(time.Hour)}
	target, err := servers.Create(t.Context(), login, server.Input{Name: "Ubuntu SFTP 验收", Host: host, Port: &port, Username: user, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	_, probe, err := servers.Test(t.Context(), login, target.ID, time.Duration(cfg.Ssh.ConnectTimeout)*time.Second)
	if err != nil || probe == nil {
		t.Fatal("Ubuntu SSH 探测失败", err)
	}
	// 仅用于明确指定的验收主机；生产信任流程仍由用户确认。
	if _, err = servers.Confirm(t.Context(), login, target.ID, probe.ProbeID); err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), db, servers, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { m.Stop(); <-m.Done() })
	s := createSession(t, m, login, target.ID)
	random := make([]byte, 12)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	prefix := "web-ssh-acceptance-" + hex.EncodeToString(random)
	names := []string{prefix + "-one", prefix + "-two", prefix + "-large"}
	if err = s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
		file, e := c.Open("/etc/os-release")
		if e != nil {
			return e
		}
		data, e := io.ReadAll(io.LimitReader(file, 16384))
		_ = file.Close()
		if e != nil {
			return e
		}
		if !strings.Contains("\n"+string(data), "\nID=ubuntu\n") && !strings.Contains("\n"+string(data), "\nID=\"ubuntu\"\n") {
			t.Fatal("目标未确认是 Ubuntu")
		}
		if version, ok := c.HasExtension("posix-rename@openssh.com"); !ok || version != "1" {
			t.Fatal("Ubuntu 目标缺少原子替换扩展")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.withFileClient(context.Background(), func(_ context.Context, c *sftp.Client, _ func()) error {
			for _, name := range names {
				if e := c.Remove(path.Join(directory, name)); e != nil && !os.IsNotExist(e) {
					return e
				}
			}
			return nil
		}); err != nil {
			t.Error("验收文件清理失败", err)
		}
	})
	expected := map[string][]byte{names[0]: []byte("ordinary\n多行上传\n"), names[1]: []byte("second file"), names[2]: bytes.Repeat([]byte("0123456789abcdef"), 1<<20)}
	for _, name := range names {
		if _, err = s.UploadFile(t.Context(), directory, name, false, FileSource{Reader: bytes.NewReader(expected[name])}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.UploadFile(t.Context(), directory, names[0], false, source("must-skip")); err != fault.Error("SFTP_FILE_EXISTS") {
		t.Fatal(err)
	}
	expected[names[0]] = []byte("atomic replacement")
	if _, err = s.UploadFile(t.Context(), directory, names[0], true, FileSource{Reader: bytes.NewReader(expected[names[0]])}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CheckFile(t.Context(), path.Join(directory, prefix+"-missing"), "x"); err != fault.Error("SFTP_DIRECTORY_UNAVAILABLE") {
		t.Fatal(err)
	}
	if err = s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
		for _, name := range names {
			file, e := c.Open(path.Join(directory, name))
			if e != nil {
				return e
			}
			hash := sha256.New()
			_, e = io.Copy(hash, file)
			_ = file.Close()
			if e != nil {
				return e
			}
			want := sha256.Sum256(expected[name])
			if !bytes.Equal(hash.Sum(nil), want[:]) {
				t.Fatal("Ubuntu 文件哈希不一致", name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Log("Ubuntu 实际扩展、顺序多文件、跳过、原子覆盖、16 MiB 文件及 SHA-256 内容验证完成；权限/磁盘满/断链仍需专用环境场景")
}
