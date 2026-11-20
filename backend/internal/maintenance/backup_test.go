package maintenance_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/maintenance"
	"github.com/stearsc/web-ssh/backend/internal/privatekey"
	"github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func TestStoppedBackupSelfContainedRestoreAndRealSSH(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys := filepath.Join(root, "production-keys")
	if err = os.Mkdir(keys, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(root, "production/web-ssh.db")
	db, err := database.Open(t.Context(), cfg.Database, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{9}, 32)
	cipher, _ := secret.New(key)
	store := privatekey.NewStore(db, keys, time.Hour)
	service, err := server.New(t.Context(), db, cipher, cfg, server.WithPrivateKeyStore(store))
	if err != nil {
		t.Fatal(err)
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "", []byte("backup-pass"))
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{AuthorizedKey: signer.PublicKey()})
	login := &auth.Session{ID: "login", Expires: time.Now().Add(time.Hour)}
	port := remote.Port()
	v, err := service.Create(t.Context(), login, server.Input{Name: "backup-key", Host: "127.0.0.1", Port: &port, Username: "tester", AuthType: "private_key", PrivateKey: raw, Passphrase: "backup-pass"})
	if err != nil {
		t.Fatal(err)
	}
	_, probe, err := service.Test(t.Context(), login, v.ID, 10*time.Second)
	if err != nil || probe == nil {
		t.Fatal(err)
	}
	if _, err = service.Confirm(t.Context(), login, v.ID, probe.ProbeID); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := maintenance.Acquire(cfg.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	set := filepath.Join(root, "recovery-set")
	manifest, err := maintenance.Backup(t.Context(), cfg.Database.Path, keys, set, key)
	lock.Close()
	if err != nil || len(manifest.Objects) != 1 || manifest.MasterKeyVerification != "历史密文解密验证成功" {
		t.Fatal(manifest, err)
	}
	saved, err := os.ReadFile(filepath.Join(set, "ssh-keys", manifest.Objects[0].ID+".key"))
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatal("备份转换了私钥原始字节", err)
	}
	// 历史恢复集必须与当前 production 私钥目录完全脱离。
	if err = os.Rename(keys, keys+".offline"); err != nil {
		t.Fatal(err)
	}
	if _, err = maintenance.Verify(t.Context(), set); err != nil {
		t.Fatal("校验依赖 production 目录", err)
	}
	restoredPath := filepath.Join(root, "restored/web-ssh.db")
	restoredKeys := filepath.Join(root, "restored-keys")
	lock, err = maintenance.Acquire(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	err = maintenance.Restore(t.Context(), set, restoredPath, restoredKeys)
	lock.Close()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database.Path = restoredPath
	restored, err := database.Open(t.Context(), cfg.Database, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredStore := privatekey.NewStore(restored, restoredKeys, time.Hour)
	restoredService, err := server.New(t.Context(), restored, cipher, cfg, server.WithPrivateKeyStore(restoredStore))
	if err != nil {
		t.Fatal(err)
	}
	if err = restoredService.ReconcilePrivateKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, probe, err := restoredService.Test(t.Context(), login, v.ID, 10*time.Second)
	if err != nil || probe != nil || !result.Authenticated {
		t.Fatal("恢复后不能使用原凭据认证", result, probe, err)
	}
	if err = maintenance.Restore(t.Context(), set, restoredPath, restoredKeys); err == nil {
		t.Fatal("覆盖了已有恢复数据")
	}
	if err = os.WriteFile(filepath.Join(set, "ssh-keys", manifest.Objects[0].ID+".key"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = maintenance.Verify(t.Context(), set); err == nil {
		t.Fatal("损坏恢复集通过校验")
	}
}

func TestMaintenanceLockAndNoHistoricalVerificationMaterial(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "source.db")
	first, err := maintenance.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := maintenance.Acquire(path); err == nil {
		second.Close()
		t.Fatal("服务和维护锁未互斥")
	}
	first.Close()
	lock, err := maintenance.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	cfg := config.Defaults()
	cfg.Database.Path = path
	db, err := database.Open(context.Background(), cfg.Database, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	db.Close()
	manifest, err := maintenance.Backup(t.Context(), path, filepath.Join(root, "missing-volume"), filepath.Join(root, "set"), bytes.Repeat([]byte{2}, 32))
	if err != nil || manifest.MasterKeyVerification != "无法验证历史匹配性" {
		t.Fatal(manifest, err)
	}
	if _, err = maintenance.Verify(t.Context(), filepath.Join(root, "set")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "set/database/web-ssh.db-wal"), []byte("unlisted WAL content"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = maintenance.Verify(t.Context(), filepath.Join(root, "set")); err == nil {
		t.Fatal("未登记的非空 WAL 被用于归档读取")
	}
}
