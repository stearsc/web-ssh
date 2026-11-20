package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/privatekey"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func uploadedKey(t *testing.T, passphrase string) ([]byte, ssh.PublicKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block), signer.PublicKey()
}

func configureStore(t *testing.T, s *Service) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s.keys = privatekey.NewStore(s.db, dir, time.Nanosecond)
	return dir
}

func TestPrivateKeyCRUDTrustRealAuthenticationAndTerminal(t *testing.T) {
	for _, passphrase := range []string{"", "new secret 口令"} {
		name := "unencrypted"
		if passphrase != "" {
			name = "encrypted"
		}
		t.Run(name, func(t *testing.T) {
			s, login, db := setup(t)
			dir := configureStore(t, s)
			raw, public := uploadedKey(t, passphrase)
			remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{AuthorizedKey: public})
			in := input(remote.Port())
			in.Password = ""
			in.AuthType = "private_key"
			in.PrivateKey = raw
			in.Passphrase = passphrase
			v, err := s.Create(t.Context(), login, in)
			if err != nil {
				t.Fatal(err)
			}
			if v.AuthType != "private_key" || !v.CredentialConfigured {
				t.Fatal(v)
			}
			st, _ := s.state(v.ID)
			old := st.record
			if len(old.PasswordEncrypted) != 0 || (passphrase != "" && (len(old.PassphraseEncrypted) == 0 || old.PassphraseEncrypted[0] != 2)) {
				t.Fatal("凭据没有互斥")
			}
			saved, err := os.ReadFile(filepath.Join(dir, old.PrivateKeyObjectID+".key"))
			if err != nil || !bytes.Equal(raw, saved) {
				t.Fatal("文件字节变化", err)
			}
			probe := probeFor(t, s, login, v.ID)
			if remote.PublicAuthCalls.Load() != 0 {
				t.Fatal("信任前发送了公钥认证")
			}
			confirm(t, s, login, v.ID, probe)
			result, probe, err := s.Test(t.Context(), login, v.ID, 10*time.Second)
			if err != nil || probe != nil || !result.Authenticated || remote.Shells.Load() != 0 {
				t.Fatal(result, probe, err)
			}
			edit := input(remote.Port())
			edit.Password = ""
			edit.Name = "renamed"
			if _, err = s.Update(t.Context(), login, v.ID, edit); err != nil {
				t.Fatal(err)
			}
			if st.record.PrivateKeyObjectID != old.PrivateKeyObjectID || !bytes.Equal(st.record.PassphraseEncrypted, old.PassphraseEncrypted) {
				t.Fatal("普通编辑丢失凭据")
			}
			creation, slot, confirmation, err := s.Reserve(t.Context(), login, v.ID, 2, func(Target) error { return nil })
			if err != nil || confirmation != nil {
				t.Fatal(err)
			}
			terminal, confirmation, err := creation.Open(t.Context(), login, 10*time.Second, 80, 24, io.Discard)
			if err != nil || confirmation != nil {
				t.Fatal(err)
			}
			if remote.Shells.Load() != 1 {
				t.Fatal("没有建立真实 Shell")
			}
			// 已建立连接不保留文件 lease，替换认证也不重连/关闭此 Shell。
			password := edit
			password.AuthType = "password"
			password.Password = "ssh-secret"
			if _, err = s.Update(t.Context(), login, v.ID, password); err != nil {
				t.Fatal(err)
			}
			if _, err = terminal.Write([]byte("still alive\n")); err != nil {
				t.Fatal("切换认证关闭了现有连接", err)
			}
			terminal.Close()
			terminal.WaitTransport()
			slot.Release()
			result, _, err = s.Test(t.Context(), login, v.ID, 10*time.Second)
			if err != nil || !result.Authenticated {
				t.Fatal("密码回归", err)
			}
			var objects int
			if err = db.QueryRow(`SELECT count(*) FROM private_key_objects`).Scan(&objects); err != nil || objects != 1 {
				t.Fatal(objects, err)
			}
		})
	}
}

func TestPrivateKeyReplacementRollbackKeepsOldCredentialAndCollectsNewObject(t *testing.T) {
	s, login, _ := setup(t)
	dir := configureStore(t, s)
	in := input(22)
	in.AuthType, in.Password = "private_key", ""
	in.PrivateKey, _ = uploadedKey(t, "")
	v, err := s.Create(t.Context(), login, in)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.state(v.ID)
	oldID := st.record.PrivateKeyObjectID
	s.transact = func(ctx context.Context, operation func(*sql.Tx) error) database.Outcome {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = operation(tx); err != nil {
			t.Fatal(err)
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		return database.RolledBack
	}
	in.PrivateKey, _ = uploadedKey(t, "")
	if _, err = s.Update(t.Context(), login, v.ID, in); err != fault.Error("STORAGE_ERROR") {
		t.Fatal(err)
	}
	if st.record.PrivateKeyObjectID != oldID {
		t.Fatal("回滚破坏旧配置")
	}
	if err = s.SweepPrivateKeys(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != oldID+".key" {
		t.Fatal("回滚新对象未回收或旧对象被删", entries, err)
	}
}

func TestSavedTestLeaseProtectsOldFileDuringReplacementAndGC(t *testing.T) {
	s, login, _ := setup(t)
	dir := configureStore(t, s)
	raw, public := uploadedKey(t, "")
	gate := make(chan struct{})
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{AuthorizedKey: public, AuthGate: gate})
	in := input(remote.Port())
	in.AuthType, in.Password, in.PrivateKey = "private_key", "", raw
	v, err := s.Create(t.Context(), login, in)
	if err != nil {
		t.Fatal(err)
	}
	probe := probeFor(t, s, login, v.ID)
	confirm(t, s, login, v.ID, probe)
	st, _ := s.state(v.ID)
	oldID := st.record.PrivateKeyObjectID
	done := make(chan error, 1)
	go func() { _, _, err := s.Test(t.Context(), login, v.ID, 5*time.Second); done <- err }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for remote.PublicAuthCalls.Load() == 0 {
		select {
		case <-ticker.C:
		case <-deadline.C:
			close(gate)
			t.Fatal("真实 SSH 未进入认证")
		}
	}
	in.PrivateKey, _ = uploadedKey(t, "")
	if _, err = s.Update(t.Context(), login, v.ID, in); err != nil {
		close(gate)
		t.Fatal(err)
	}
	if err = s.SweepPrivateKeys(t.Context(), nil); err != nil {
		close(gate)
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, oldID+".key")); err != nil {
		close(gate)
		t.Fatal("Test lease 被 GC 穿过", err)
	}
	close(gate)
	if err = <-done; err != nil {
		t.Fatal("固定旧快照混入新凭据", err)
	}
	if err = s.SweepPrivateKeys(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, oldID+".key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Test 完成未释放租约", err)
	}
}

func TestPrivateKeyUncertainReplacementProtectsBothObjectsUntilReload(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "not_committed"
		if committed {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			s, login, _ := setup(t)
			dir := configureStore(t, s)
			in := input(22)
			in.AuthType, in.Password = "private_key", ""
			in.PrivateKey, _ = uploadedKey(t, "")
			v, err := s.Create(t.Context(), login, in)
			if err != nil {
				t.Fatal(err)
			}
			st, _ := s.state(v.ID)
			oldID := st.record.PrivateKeyObjectID
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s.transact = func(ctx context.Context, operation func(*sql.Tx) error) database.Outcome {
				if committed && database.TransactionFor(s.db)(ctx, operation) != database.Committed {
					t.Fatal("故障注入前提交失败")
				}
				// 模拟提交结果丢失且本次请求结束，不能依赖本次 reload 确认事实。
				cancel()
				return database.Uncertain
			}
			in.PrivateKey, _ = uploadedKey(t, "")
			if _, err = s.Update(ctx, login, v.ID, in); err != fault.Error("SERVER_UNAVAILABLE") {
				t.Fatal(err)
			}
			if err = s.SweepPrivateKeys(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 {
				t.Fatal("事实确认前回收了新旧对象", entries, err)
			}
			if _, err = s.Get(t.Context(), v.ID); err != nil {
				t.Fatal("不能根据 DB 事实恢复", err)
			}
			if (st.record.PrivateKeyObjectID != oldID) != committed {
				t.Fatal("未采用实际 DB 提交事实")
			}
			if err = s.SweepPrivateKeys(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			entries, err = os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != st.record.PrivateKeyObjectID+".key" {
				t.Fatal("reload 后仍误判对象引用", entries, err)
			}
		})
	}
}

func TestPrivateKeyReplacementFailureMissingFilesAndStartupIsolation(t *testing.T) {
	s, login, db := setup(t)
	dir := configureStore(t, s)
	raw, public := uploadedKey(t, "old-pass")
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{AuthorizedKey: public})
	in := input(remote.Port())
	in.AuthType = "private_key"
	in.Password = ""
	in.PrivateKey = raw
	in.Passphrase = "old-pass"
	v, err := s.Create(t.Context(), login, in)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.state(v.ID)
	old := st.record
	newRaw, _ := uploadedKey(t, "new-pass")
	in.PrivateKey = newRaw
	in.Passphrase = ""
	if _, err = s.Update(t.Context(), login, v.ID, in); err != fault.Error("PRIVATE_KEY_INVALID") {
		t.Fatal("替换复用了旧口令", err)
	}
	in.Passphrase = "wrong"
	if _, err = s.Update(t.Context(), login, v.ID, in); err != fault.Error("PRIVATE_KEY_INVALID") {
		t.Fatal(err)
	}
	if st.record.PrivateKeyObjectID != old.PrivateKeyObjectID {
		t.Fatal("验证失败破坏旧引用")
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM private_key_objects`).Scan(&count)
	if count != 1 {
		t.Fatal("失败校验落盘", count)
	}
	in.PrivateKey = nil
	in.Passphrase = "new-pass"
	if _, err = s.Update(t.Context(), login, v.ID, in); err != fault.Error("INVALID_REQUEST") {
		t.Fatal("接受了单独口令更新")
	}
	in.Passphrase = ""
	in.PasswordPresent = true
	if _, err = s.Update(t.Context(), login, v.ID, in); err != fault.Error("INVALID_REQUEST") {
		t.Fatal("私钥模式接受空 password 字段")
	}
	if err = os.Remove(filepath.Join(dir, old.PrivateKeyObjectID+".key")); err != nil {
		t.Fatal(err)
	}
	if _, err = New(t.Context(), db, s.cipher, config.Defaults(), WithPrivateKeyStore(s.keys)); err != nil {
		t.Fatal("单文件缺失阻止启动", err)
	}
	if _, err = s.Get(t.Context(), v.ID); err != nil {
		t.Fatal("单文件故障阻止查询", err)
	}
	probe := probeFor(t, s, login, v.ID)
	confirm(t, s, login, v.ID, probe)
	if _, _, err = s.Test(t.Context(), login, v.ID, 10*time.Second); err != fault.Error("PRIVATE_KEY_UNAVAILABLE") {
		t.Fatal("本地文件被误报远端失败", err)
	}
	in.PasswordPresent = false
	in.PrivateKey = newRaw
	in.Passphrase = "new-pass"
	if _, err = s.Update(t.Context(), login, v.ID, in); err != nil {
		t.Fatal("缺失文件不能重新上传修复", err)
	}
	if _, _, err = s.Test(t.Context(), login, v.ID, 10*time.Second); err != fault.Error("SSH_AUTH_FAILED") {
		t.Fatal("远端拒绝被误报本地错误", err)
	}
	if err = os.WriteFile(filepath.Join(dir, st.record.PrivateKeyObjectID+".key"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = New(t.Context(), db, s.cipher, config.Defaults(), WithPrivateKeyStore(s.keys)); err != nil {
		t.Fatal("单文件损坏阻止启动", err)
	}
	if _, err = db.Exec(`UPDATE servers SET passphrase_encrypted=x'020102' WHERE id=?`, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = New(t.Context(), db, s.cipher, config.Defaults(), WithPrivateKeyStore(s.keys)); err == nil || err.Error() != "保存口令无法解密，可能是主密钥或数据问题" {
		t.Fatal("错误密文未阻止启动或臆断原因", err)
	}
}

func TestCreationSnapshotFileLeaseSurvivesReplacementAndReleasesOnCancel(t *testing.T) {
	s, login, _ := setup(t)
	dir := configureStore(t, s)
	raw, public := uploadedKey(t, "")
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{AuthorizedKey: public})
	in := input(remote.Port())
	in.Password = ""
	in.AuthType = "private_key"
	in.PrivateKey = raw
	v, err := s.Create(t.Context(), login, in)
	if err != nil {
		t.Fatal(err)
	}
	probe := probeFor(t, s, login, v.ID)
	confirm(t, s, login, v.ID, probe)
	st, _ := s.state(v.ID)
	oldID := st.record.PrivateKeyObjectID
	creation, slot, _, err := s.Reserve(t.Context(), login, v.ID, 2, func(Target) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	in.PrivateKey, _ = uploadedKey(t, "")
	if _, err = s.Update(t.Context(), login, v.ID, in); err != nil {
		t.Fatal(err)
	}
	if err = s.SweepPrivateKeys(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, oldID+".key")); err != nil {
		t.Fatal("Creation 使用前被回收", err)
	}
	term, _, err := creation.Open(t.Context(), login, 10*time.Second, 80, 24, io.Discard)
	if err != nil {
		t.Fatal("旧快照混入新认证", err)
	}
	term.Close()
	term.WaitTransport()
	slot.Release()
	if err = s.SweepPrivateKeys(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, oldID+".key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Creation 完成未释放文件保护", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	pending, pendingSlot, _, err := s.Reserve(ctx, login, v.ID, 2, func(Target) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	pending.Cancel()
	pendingSlot.Release()
	if _, _, err = pending.Open(t.Context(), login, 10*time.Second, 80, 24, io.Discard); err != fault.Error("SERVER_SHUTTING_DOWN") {
		t.Fatal("取消后仍创建连接", err)
	}
}
