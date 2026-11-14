package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/sshclient"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func setup(t *testing.T) (*Service, *auth.Session, *sql.DB) {
	t.Helper()
	db := testutil.DB(t)
	cipher, err := secret.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), db, cipher, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	return s, &auth.Session{ID: strings.Repeat("1", 32), Expires: time.Now().Add(time.Hour)}, db
}
func input(port int) Input {
	return Input{Name: "demo", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "ssh-secret", GroupName: "生产"}
}
func create(t *testing.T, s *Service, login *auth.Session, in Input) View {
	t.Helper()
	v, err := s.Create(context.Background(), login, in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func probeFor(t *testing.T, s *Service, login *auth.Session, id string) *Confirmation {
	t.Helper()
	_, p, err := s.Test(context.Background(), login, id, time.Second)
	if err != nil || p == nil {
		t.Fatal("预期探测", p, err)
	}
	return p
}
func confirm(t *testing.T, s *Service, login *auth.Session, id string, p *Confirmation) {
	t.Helper()
	if _, err := s.Confirm(context.Background(), login, id, p.ProbeID); err != nil {
		t.Fatal(err)
	}
}
func TestCRUDValidationAndPassword(t *testing.T) {
	s, login, db := setup(t)
	ctx := context.Background()
	in := input(22)
	in.Name = "  demo_%  "
	in.Host = "LOCALHOST"
	v := create(t, s, login, in)
	if v.Name != "demo_%" || v.Host != "localhost" {
		t.Fatal(v)
	}
	st, _ := s.state(v.ID)
	old := bytes.Clone(st.record.PasswordEncrypted)
	if bytes.Contains(old, []byte(in.Password)) {
		t.Fatal("明文落盘")
	}
	in.Password = ""
	in.Name = "renamed"
	if _, err := s.Update(ctx, login, v.ID, in); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old, st.record.PasswordEncrypted) {
		t.Fatal("留空覆盖密文")
	}
	plain, err := s.cipher.Decrypt(st.record.PasswordEncrypted)
	if err != nil || string(plain) != "ssh-secret" {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "%", nil)
	if err != nil || len(list) != 0 {
		t.Fatal("搜索通配符被解释")
	}
	group := "生产"
	list, err = s.List(ctx, "renamed", &group)
	if err != nil || len(list) != 1 {
		t.Fatal(err, list)
	}
	data, _ := json.Marshal(v)
	for _, sensitive := range []string{"password", "public_key", "config_version", "ssh-secret"} {
		if bytes.Contains(data, []byte(sensitive)) {
			t.Fatal("DTO 泄露", sensitive)
		}
	}
	badInputs := []Input{input(0), input(65536), input(22), input(22), input(22), input(22), input(22)}
	badInputs[2].Host = "ssh://example.com"
	badInputs[3].Host = "host:22"
	badInputs[4].Username = "x\x00"
	badInputs[5].Name = strings.Repeat("中", 43)
	badInputs[6].Password = ""
	for i, bad := range badInputs {
		if _, err := s.Create(ctx, login, bad); err != fault.Error("INVALID_REQUEST") {
			t.Fatalf("非法参数 %d: %v", i, err)
		}
	}
	s.SetActivityCheck(func(string) bool { return true })
	if err := s.Delete(ctx, login, v.ID); err != fault.Error("SERVER_HAS_ACTIVE_SESSIONS") {
		t.Fatal(err)
	}
	s.SetActivityCheck(func(string) bool { return false })
	if _, err := db.Exec(`INSERT INTO sessions(id,server_id,name,host,username,port,status,created_at) VALUES('old',?,'old','host','user',22,'CONNECTED',1)`, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, login, v.ID); err != nil {
		t.Fatal("旧数据库状态阻止删除", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatal(err, count)
	}
	if err := s.Delete(ctx, login, v.ID); err != fault.Error("SERVER_NOT_FOUND") {
		t.Fatal(err)
	}
}
func TestHostKeyTrustAndAuthentication(t *testing.T) {
	s, login, _ := setup(t)
	remote := testutil.StartSSH(t)
	v := create(t, s, login, input(remote.Port()))
	ctx := context.Background()
	p := probeFor(t, s, login, v.ID)
	if p.Reason != "first_trust" || p.PreviousFingerprint != nil || remote.AuthCalls.Load() != 0 {
		t.Fatal("信任前发生认证")
	}
	view, err := s.Get(ctx, v.ID)
	if err != nil || view.HostKey != nil {
		t.Fatal("未确认就保存信任")
	}
	confirm(t, s, login, v.ID, p)
	if _, err := s.Confirm(ctx, login, v.ID, p.ProbeID); err != fault.Error("INVALID_PROBE") {
		t.Fatal("重放", err)
	}
	result, p, err := s.Test(ctx, login, v.ID, time.Second)
	if err != nil || p != nil || !result.Authenticated {
		t.Fatal(result, p, err)
	}
	if remote.AuthCalls.Load() != 1 || remote.Channels.Load() != 0 {
		t.Fatal("认证或 Channel 数量错误")
	}
	remote.Rotate(t)
	p = probeFor(t, s, login, v.ID)
	if p.Reason != "key_changed" || p.PreviousFingerprint == nil || remote.AuthCalls.Load() != 1 {
		t.Fatal("变化密钥未拦截")
	}
	confirm(t, s, login, v.ID, p)
	remote.Rotate(t)
	p = probeFor(t, s, login, v.ID)
	confirm(t, s, login, v.ID, p)
	in := input(remote.Port())
	in.Password = "wrong"
	if _, err := s.Update(ctx, login, v.ID, in); err != nil {
		t.Fatal(err)
	}
	_, p, err = s.Test(ctx, login, v.ID, time.Second)
	if p != nil || err != fault.Error("SSH_AUTH_FAILED") {
		t.Fatal(err, p)
	}
	remote.WaitIdle(t)
}
func TestProbeEditingAndRestart(t *testing.T) {
	for _, field := range []string{"name", "username", "password", "host", "port"} {
		t.Run(field, func(t *testing.T) {
			s, login, db := setup(t)
			remote := testutil.StartSSH(t)
			in := input(remote.Port())
			v := create(t, s, login, in)
			first := probeFor(t, s, login, v.ID)
			confirm(t, s, login, v.ID, first)
			remote.Rotate(t)
			p := probeFor(t, s, login, v.ID)
			st, _ := s.state(v.ID)
			version := st.version
			switch field {
			case "name":
				in.Name = "new"
			case "username":
				in.Username = "other"
			case "password":
				in.Password = "other-secret"
			case "host":
				in.Host = "localhost"
			case "port":
				port := remote.Port() + 1
				if port > 65535 {
					port = 22
				}
				in.Port = &port
			}
			if _, err := s.Update(context.Background(), login, v.ID, in); err != nil {
				t.Fatal(err)
			}
			_, err := s.Confirm(context.Background(), login, v.ID, p.ProbeID)
			if field == "name" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err != fault.Error("INVALID_PROBE") || st.version <= version {
					t.Fatal(err, st.version)
				}
			}
			if (field == "host" || field == "port") && len(st.record.PublicKey) != 0 {
				t.Fatal("旧目标信任未清除")
			}
			if (field == "username" || field == "password") && len(st.record.PublicKey) == 0 {
				t.Fatal("用户名/密码不应清信任")
			}
			restarted, err := New(context.Background(), db, s.cipher, config.Defaults())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.Confirm(context.Background(), login, v.ID, p.ProbeID); err != fault.Error("INVALID_PROBE") {
				t.Fatal("重启凭证仍然有效", err)
			}
			wrong, _ := secret.New(bytes.Repeat([]byte{2}, 32))
			if _, err := New(context.Background(), db, wrong, config.Defaults()); err == nil {
				t.Fatal("错误主密钥允许启动")
			}
		})
	}
}
func TestConcurrentConfirmationAndOwnership(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(map[bool]string{true: "same", false: "different"}[same], func(t *testing.T) {
			s, login, _ := setup(t)
			remote := testutil.StartSSH(t)
			v := create(t, s, login, input(remote.Port()))
			p := probeFor(t, s, login, v.ID)
			q := p
			if !same {
				remote.Rotate(t)
				q = probeFor(t, s, login, v.ID)
			}
			other := &auth.Session{ID: strings.Repeat("2", 32), Expires: login.Expires}
			if _, err := s.Confirm(context.Background(), other, v.ID, p.ProbeID); err != fault.Error("INVALID_PROBE") {
				t.Fatal("异登录确认", err)
			}
			v2 := create(t, s, login, input(remote.Port()))
			if _, err := s.Confirm(context.Background(), login, v2.ID, p.ProbeID); err != fault.Error("INVALID_PROBE") {
				t.Fatal("异服务器确认", err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, token := range []string{p.ProbeID, q.ProbeID} {
				go func() { <-start; _, err := s.Confirm(context.Background(), login, v.ID, token); results <- err }()
			}
			close(start)
			successes := 0
			for i := 0; i < 2; i++ {
				err := <-results
				if err == nil {
					successes++
				} else if err != fault.Error("INVALID_PROBE") {
					t.Fatal(err)
				}
			}
			if successes != 1 {
				t.Fatal(successes)
			}
		})
	}
}
func TestProbeCapacityExpiryAndRevocation(t *testing.T) {
	s, login, _ := setup(t)
	s.probeLimit = 2
	remote := testutil.StartSSH(t)
	v := create(t, s, login, input(remote.Port()))
	var now atomic.Int64
	now.Store(time.Now().UnixMilli())
	s.now = func() time.Time { return time.UnixMilli(now.Load()) }
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, p, err := s.Test(context.Background(), login, v.ID, time.Second)
			if p != nil {
				accepted.Add(1)
			} else if err != fault.Error("HOST_KEY_PROBE_LIMIT") {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 2 || len(s.probes) != 2 {
		t.Fatal("容量超限")
	}
	now.Add(int64((301 * time.Second) / time.Millisecond))
	s.Cleanup()
	if len(s.probes) != 0 {
		t.Fatal("过期未清理")
	}
	p := probeFor(t, s, login, v.ID)
	now.Add(int64((301 * time.Second) / time.Millisecond))
	if _, err := s.Confirm(context.Background(), login, v.ID, p.ProbeID); err != fault.Error("INVALID_PROBE") {
		t.Fatal(err)
	}
	p = probeFor(t, s, login, v.ID)
	s.Revoke(login.ID)
	if _, err := s.Confirm(context.Background(), login, v.ID, p.ProbeID); err != fault.Error("INVALID_PROBE") {
		t.Fatal(err)
	}
}
func TestProbeTransactionFailures(t *testing.T) {
	for _, mode := range []string{"rollback", "committed-uncertain", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			s, login, db := setup(t)
			remote := testutil.StartSSH(t)
			v := create(t, s, login, input(remote.Port()))
			p := probeFor(t, s, login, v.ID)
			original := s.transact
			s.transact = func(ctx context.Context, fn func(*sql.Tx) error) database.Outcome {
				if mode == "rollback" {
					return database.RolledBack
				}
				if mode == "committed-uncertain" {
					if original(ctx, fn) != database.Committed {
						t.Fatal("注入前提交失败")
					}
				}
				if mode == "unreadable" {
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return database.Uncertain
			}
			_, err := s.Confirm(context.Background(), login, v.ID, p.ProbeID)
			st, _ := s.state(v.ID)
			if mode == "rollback" {
				if err != fault.Error("STORAGE_ERROR") || len(st.record.PublicKey) != 0 {
					t.Fatal(err)
				}
				s.transact = original
				confirm(t, s, login, v.ID, p)
			} else {
				if err != fault.Error("SERVER_UNAVAILABLE") || len(s.probes) != 0 {
					t.Fatal(err)
				}
				if mode == "committed-uncertain" && (st.unavailable || len(st.record.PublicKey) == 0) {
					t.Fatal("没有重读已提交信任")
				}
				if mode == "unreadable" {
					if !st.unavailable {
						t.Fatal("没有冻结")
					}
					if _, err := s.Update(context.Background(), login, v.ID, input(22)); err != fault.Error("SERVER_UNAVAILABLE") {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
func TestOccupiedProbeSurvivesCleanup(t *testing.T) {
	s, login, _ := setup(t)
	remote := testutil.StartSSH(t)
	v := create(t, s, login, input(remote.Port()))
	p := probeFor(t, s, login, v.ID)
	var now atomic.Int64
	now.Store(time.Now().UnixMilli())
	s.now = func() time.Time { return time.UnixMilli(now.Load()) }
	entered, proceed := make(chan struct{}), make(chan struct{})
	original := s.transact
	s.transact = func(ctx context.Context, fn func(*sql.Tx) error) database.Outcome {
		close(entered)
		<-proceed
		return original(ctx, fn)
	}
	result := make(chan error, 1)
	go func() { _, err := s.Confirm(context.Background(), login, v.ID, p.ProbeID); result <- err }()
	<-entered
	now.Add(int64((301 * time.Second) / time.Millisecond))
	s.Cleanup()
	s.probesMu.Lock()
	size := len(s.probes)
	s.probesMu.Unlock()
	close(proceed)
	if err := <-result; err != nil || size != 1 {
		t.Fatal("清理消费了在途凭证", err, size)
	}
}
func TestInFlightEditDeleteAndRecreate(t *testing.T) {
	for _, action := range []string{"edit", "delete", "recreate"} {
		t.Run(action, func(t *testing.T) {
			s, login, _ := setup(t)
			remote := testutil.StartSSH(t)
			gate := make(chan struct{})
			remote.BlockHandshake(gate)
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(gate) }) })
			in := input(remote.Port())
			v := create(t, s, login, in)
			results := make(chan error, 1)
			go func() {
				_, p, err := s.Test(context.Background(), login, v.ID, 2*time.Second)
				if p != nil {
					err = fault.Error("INTERNAL_ERROR")
				}
				results <- err
			}()
			<-remote.Accepted
			if action == "edit" {
				in.Username = "changed"
				if _, err := s.Update(context.Background(), login, v.ID, in); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.Delete(context.Background(), login, v.ID); err != nil {
					t.Fatal(err)
				}
				if action == "recreate" {
					other := create(t, s, login, in)
					if other.ID == v.ID {
						t.Fatal("ID 复用")
					}
				}
			}
			once.Do(func() { close(gate) })
			err := <-results
			if action == "edit" && err != fault.Error("SERVER_CONFIG_CHANGED") {
				t.Fatal(err)
			}
			if action != "edit" && err != fault.Error("SERVER_UNAVAILABLE") {
				t.Fatal(err)
			}
			if len(s.probes) != 0 || remote.AuthCalls.Load() != 0 {
				t.Fatal("旧目标凭证或认证泄漏")
			}
		})
	}
}
func TestOldSnapshotCannotPublishAfterTrustCommit(t *testing.T) {
	s, login, _ := setup(t)
	remote := testutil.StartSSH(t)
	v := create(t, s, login, input(remote.Port()))
	st, _ := s.state(v.ID)
	snapshot := st.record
	version := st.version
	p := probeFor(t, s, login, v.ID)
	confirm(t, s, login, v.ID, p)
	key := remote.PublicKey()
	_, _, err := s.publish(context.Background(), login, st, snapshot, version, sshclient.Observed{PublicKey: key.Marshal(), Algorithm: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)})
	if err != fault.Error("SERVER_CONFIG_CHANGED") {
		t.Fatal(err)
	}
}
func TestEditAndDeleteUncertainCommit(t *testing.T) {
	for _, operation := range []string{"edit", "delete"} {
		t.Run(operation, func(t *testing.T) {
			s, login, _ := setup(t)
			v := create(t, s, login, input(22))
			original := s.transact
			s.transact = func(ctx context.Context, fn func(*sql.Tx) error) database.Outcome {
				if original(ctx, fn) != database.Committed {
					t.Fatal("提交失败")
				}
				return database.Uncertain
			}
			if operation == "edit" {
				in := input(22)
				in.Host = "localhost"
				if _, err := s.Update(context.Background(), login, v.ID, in); err != fault.Error("SERVER_UNAVAILABLE") {
					t.Fatal(err)
				}
				view, err := s.Get(context.Background(), v.ID)
				if err != nil || view.Host != "localhost" {
					t.Fatal(err, view)
				}
			} else {
				if err := s.Delete(context.Background(), login, v.ID); err != fault.Error("SERVER_UNAVAILABLE") {
					t.Fatal(err)
				}
				if _, err := s.Get(context.Background(), v.ID); err != fault.Error("SERVER_NOT_FOUND") {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestExpiredLoginCannotPublishAndTargetRevertNeedsTrust(t *testing.T) {
	s, login, _ := setup(t)
	remote := testutil.StartSSH(t)
	in := input(remote.Port())
	v := create(t, s, login, in)
	st, _ := s.state(v.ID)
	snapshot, version := st.record, st.version
	key := remote.PublicKey()
	expired := &auth.Session{ID: login.ID, Expires: time.Now().Add(-time.Second)}
	_, _, err := s.publish(context.Background(), expired, st, snapshot, version, sshclient.Observed{PublicKey: key.Marshal(), Algorithm: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)})
	if err != fault.Error("AUTH_REQUIRED") {
		t.Fatal(err)
	}
	p := probeFor(t, s, login, v.ID)
	confirm(t, s, login, v.ID, p)
	in.Host = "localhost"
	if _, err := s.Update(context.Background(), login, v.ID, in); err != nil {
		t.Fatal(err)
	}
	in.Host = "127.0.0.1"
	if _, err := s.Update(context.Background(), login, v.ID, in); err != nil {
		t.Fatal(err)
	}
	p = probeFor(t, s, login, v.ID)
	if p.Reason != "first_trust" || p.PreviousFingerprint != nil {
		t.Fatal("改回目标复用了旧信任")
	}
}
func TestRealDatabaseEditRollbackAndCorruptCiphertext(t *testing.T) {
	s, login, db := setup(t)
	in := input(22)
	v := create(t, s, login, in)
	if _, err := db.Exec(`CREATE TRIGGER fail_edit BEFORE UPDATE ON servers BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	in.Host = "localhost"
	if _, err := s.Update(context.Background(), login, v.ID, in); err != fault.Error("STORAGE_ERROR") {
		t.Fatal(err)
	}
	view, err := s.Get(context.Background(), v.ID)
	if err != nil || view.Host != "127.0.0.1" {
		t.Fatal(err, view)
	}
	if _, err := db.Exec("DROP TRIGGER fail_edit"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE servers SET password_encrypted=? WHERE id=?", []byte("corrupt"), v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), db, s.cipher, config.Defaults()); err == nil {
		t.Fatal("损坏密文允许启动")
	}
}

func TestUncertainCreationReloadsActualExistence(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rolled-back", true: "committed"}[committed], func(t *testing.T) {
			s, login, _ := setup(t)
			original := s.transact
			s.transact = func(ctx context.Context, fn func(*sql.Tx) error) database.Outcome {
				if committed && original(ctx, fn) != database.Committed {
					t.Fatal("提交失败")
				}
				return database.Uncertain
			}
			if _, err := s.Create(context.Background(), login, input(22)); err != fault.Error("SERVER_UNAVAILABLE") {
				t.Fatal(err)
			}
			list, err := s.List(context.Background(), "", nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if committed {
				want = 1
			}
			if len(list) != want || len(s.states) != want {
				t.Fatal("不确定创建留下虚构资料", len(list), len(s.states))
			}
		})
	}
}
