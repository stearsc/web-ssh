package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

func setup(t *testing.T) (*Manager, *auth.Session, string, *testutil.TerminalSSH) {
	return setupWith(t, testutil.TerminalOptions{Banner: []byte("banner\r\n")}, nil)
}
func setupWith(t *testing.T, options testutil.TerminalOptions, configure func(*config.Config)) (*Manager, *auth.Session, string, *testutil.TerminalSSH) {
	t.Helper()
	remote := testutil.StartTerminalSSH(t, options)
	db := testutil.DB(t)
	cipher, _ := secret.New(bytes.Repeat([]byte{1}, 32))
	cfg := config.Defaults()
	cfg.Session.MaxSessionsPerServer = 2
	if configure != nil {
		configure(&cfg)
	}
	servers, err := server.New(t.Context(), db, cipher, cfg)
	if err != nil {
		t.Fatal(err)
	}
	login := &auth.Session{ID: strings.Repeat("a", 32), Expires: time.Now().Add(time.Hour)}
	port := remote.Port()
	v, err := servers.Create(t.Context(), login, server.Input{Name: "test", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "ssh-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, p, err := servers.Test(t.Context(), login, v.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = servers.Confirm(t.Context(), login, v.ID, p.ProbeID); err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), db, servers, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		m.Stop()
		select {
		case <-m.Done():
		case <-time.After(3 * time.Second):
			t.Error("Manager 未退出")
		}
	})
	return m, login, v.ID, remote
}
func createSession(t *testing.T, m *Manager, login *auth.Session, id string) *Session {
	t.Helper()
	v, p, e := m.Create(login, id, 80, 24)
	if e != nil || p != nil {
		t.Fatal(p, e)
	}
	s, e := m.Find(v.ID)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

// 既有 attach 测试复用同一提交入口，并履行成功结果的锁外收尾责任。
func attachBinding(s *Session, ctx context.Context, login *auth.Session, token string, offset uint64, end func(string)) (*Binding, error) {
	result, err := s.Attach(ctx, login, token, offset, end)
	if err != nil {
		return nil, err
	}
	defer result.FinishDisplaced()
	return result.NewBinding, nil
}

func bind(t *testing.T, m *Manager, s *Session, login *auth.Session) *Binding {
	t.Helper()
	c, e := m.Prepare(login, s.id, "attach", s.View().Generation)
	if e != nil {
		t.Fatal(e)
	}
	b, e := attachBinding(s, t.Context(), login, c.Credential, 0, func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func waitBytes(t *testing.T, s *Session, want []byte) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		r, e := s.buffer.Snapshot(0)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(r.Data, want) {
			return
		}
		select {
		case <-s.buffer.Changed():
		case <-deadline.C:
			t.Fatalf("未收到 %q", want)
		}
	}
}
func TestRealSessionsIsolationAndAttachLifecycle(t *testing.T) {
	m, login, id, remote := setup(t)
	a := createSession(t, m, login, id)
	b := createSession(t, m, login, id)
	ab := bind(t, m, a, login)
	bb := bind(t, m, b, login)
	if _, e := m.Prepare(login, a.id, "attach", ab.Generation); e != fault.Error("BIND_BUSY") {
		t.Fatal(e)
	}
	if _, e := m.Prepare(login, a.id, "takeover", ab.Generation); e != nil {
		t.Fatal(e)
	}
	if a.View().Generation != ab.Generation || a.View().Status != "CONNECTED" {
		t.Fatal("Prepare 改变已有 Binding")
	}
	if e := a.Execute(ab, []byte("A-only"), 0, 0); e != nil {
		t.Fatal(e)
	}
	if e := b.Execute(bb, []byte("B-only"), 0, 0); e != nil {
		t.Fatal(e)
	}
	waitBytes(t, a, []byte("A-only"))
	waitBytes(t, b, []byte("B-only"))
	ar, _ := a.buffer.Snapshot(0)
	br, _ := b.buffer.Snapshot(0)
	if bytes.Contains(ar.Data, []byte("B-only")) || bytes.Contains(br.Data, []byte("A-only")) {
		t.Fatal("输出串流")
	}
	a.Detach(ab)
	if e := a.Execute(ab, []byte("stale"), 0, 0); e == nil {
		t.Fatal("旧输入通过")
	}
	next := bind(t, m, a, login)
	a.Detach(ab)
	if a.View().Status != "CONNECTED" || a.View().Generation != next.Generation {
		t.Fatal("旧 Detach 影响新 Binding")
	}
	if remote.Shells.Load() != 2 {
		t.Fatal("重新绑定创建 SSH", remote.Shells.Load())
	}
	a.Close("user_closed")
	<-a.Done()
	if e := b.Execute(bb, []byte("still-live"), 0, 0); e != nil {
		t.Fatal(e)
	}
	waitBytes(t, b, []byte("still-live"))
}
func TestConcurrentAttachAndCredentialFailure(t *testing.T) {
	m, login, id, _ := setup(t)
	s := createSession(t, m, login, id)
	first, e := m.Prepare(login, s.id, "attach", 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = attachBinding(s, t.Context(), login, first.Credential, 100000, func(string) {}); e != fault.Error("INVALID_OFFSET") {
		t.Fatal(e)
	}
	second, e := m.Prepare(login, s.id, "attach", 0)
	if e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	results := make(chan *Binding, 2)
	var wg sync.WaitGroup
	for _, token := range []string{first.Credential, second.Credential} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			b, e := attachBinding(s, t.Context(), login, token, 0, func(string) {})
			if e != nil && e != fault.Error("INVALID_CREDENTIAL") && e != fault.Error("BIND_CONFLICT") {
				t.Error(e)
			}
			results <- b
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for b := range results {
		if b != nil {
			wins++
		}
	}
	if wins != 1 || s.View().Generation != 1 {
		t.Fatal(wins, s.View())
	}
	m.Revoke(login.ID)
	if s.View().Status != "DISCONNECTED" {
		t.Fatal(s.View())
	}
}
func TestRealSessionSlotsAndConcurrentClose(t *testing.T) {
	m, login, id, _ := setup(t)
	start := make(chan struct{})
	results := make(chan View, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			v, _, e := m.Create(login, id, 80, 24)
			if e == nil {
				results <- v
			} else if e != fault.Error("SESSION_LIMIT") {
				t.Error(e)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var all []*Session
	for v := range results {
		s, _ := m.Find(v.ID)
		all = append(all, s)
	}
	if len(all) != 2 {
		t.Fatal(len(all))
	}
	if e := m.servers.Delete(t.Context(), login, id); e != fault.Error("SERVER_HAS_ACTIVE_SESSIONS") {
		t.Fatal(e)
	}
	for _, s := range all {
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); s.Close("user_closed") }()
		}
	}
	wg.Wait()
	for _, s := range all {
		<-s.Done()
	}
	if e := m.servers.Delete(t.Context(), login, id); e != nil {
		t.Fatal("名额未归还", e)
	}
}
func TestOfflineExpiryAndMetadata(t *testing.T) {
	m, login, id, _ := setup(t)
	s := createSession(t, m, login, id)
	b := bind(t, m, s, login)
	s.Detach(b)
	s.mu.Lock()
	s.expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if _, e := m.Prepare(login, s.id, "attach", b.Generation); e != fault.Error("SESSION_NOT_READY") {
		t.Fatal(e)
	}
	m.Sweep()
	<-s.Done()
	var status, reason string
	if e := m.db.QueryRow("SELECT status,close_reason FROM sessions WHERE id=?", s.id).Scan(&status, &reason); e != nil || status != "CLOSED" || reason != "idle_timeout" {
		t.Fatal(status, reason, e)
	}
	s.Detach(b)
	if s.View().Status != "CLOSED" {
		t.Fatal(s.View())
	}
}
func TestBufferByteAndLineLimits(t *testing.T) {
	for _, tc := range []struct {
		capacity, lines int
		input, want     string
	}{{8, 2, "a\r\nb\nc", "b\nc"}, {4, 20, "abcdef", "cdef"}, {32, 2, "a\nb\n", "a\nb\n"}, {4, 2, "中文", "\xad文"}} {
		b := NewBuffer(tc.capacity, tc.lines)
		for _, v := range []byte(tc.input) {
			if _, e := b.Write([]byte{v}); e != nil {
				t.Fatal(e)
			}
		}
		r, e := b.Snapshot(0)
		if e != nil || string(r.Data) != tc.want || r.End != uint64(len(tc.input)) {
			t.Fatalf("%q %+v %v", tc.want, r, e)
		}
		stable := bytes.Clone(r.Data)
		b.Write([]byte("zzzzzz"))
		if !bytes.Equal(stable, r.Data) {
			t.Fatal("快照被更改")
		}
	}
}

func TestSnapshotCommitRechecksCredentialAndFailureKeepsOffline(t *testing.T) {
	for _, mode := range []string{"expire", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			m, login, id, _ := setup(t)
			s := createSession(t, m, login, id)
			token, e := m.Prepare(login, s.id, "attach", 0)
			if e != nil {
				t.Fatal(e)
			}
			m.beforeCredentialCommit = func() {
				m.credentialsMu.Lock()
				defer m.credentialsMu.Unlock()
				for h, c := range m.credentials {
					if mode == "expire" {
						c.expires = time.Now().Add(-time.Second)
						m.credentials[h] = c
					} else {
						delete(m.credentials, h)
					}
				}
			}
			if _, e = attachBinding(s, t.Context(), login, token.Credential, 0, func(string) {}); e != fault.Error("INVALID_CREDENTIAL") {
				t.Fatal(e)
			}
			if v := s.View(); v.Status != "DISCONNECTED" || v.Generation != 0 {
				t.Fatal(v)
			}
		})
	}
}
func TestDetachInFlightInputAndReattachGate(t *testing.T) {
	m, login, id, _ := setup(t)
	s := createSession(t, m, login, id)
	b := bind(t, m, s, login)
	admitted, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.operationAdmitted = func() { once.Do(func() { close(admitted); <-proceed }) }
	result := make(chan error, 1)
	go func() { result <- s.Execute(b, []byte("admitted-before-detach"), 0, 0) }()
	<-admitted
	s.Detach(b)
	if s.View().Status != "DISCONNECTED" {
		t.Fatal("Detach 等待了在途操作")
	}
	queued := make(chan error, 1)
	go func() { queued <- s.Execute(b, []byte("must-not-run"), 0, 0) }()
	credential, e := m.Prepare(login, s.id, "attach", b.Generation)
	if e != nil {
		t.Fatal(e)
	}
	attached := make(chan *Binding, 1)
	go func() {
		next, e := attachBinding(s, t.Context(), login, credential.Credential, 0, func(string) {})
		if e != nil {
			t.Error(e)
		}
		attached <- next
	}()
	close(proceed)
	if e := <-result; e != nil {
		t.Fatal(e)
	}
	if e := <-queued; e == nil {
		t.Fatal("失效队列输入被执行")
	}
	next := <-attached
	if next == nil {
		t.Fatal("重新 attach 失败")
	}
	waitBytes(t, s, []byte("admitted-before-detach"))
	data, _ := s.buffer.Snapshot(0)
	if bytes.Contains(data.Data, []byte("must-not-run")) {
		t.Fatal("失效输入进入 SSH")
	}
	s.Detach(b)
	if s.View().Status != "CONNECTED" {
		t.Fatal("旧回调影响新 Binding")
	}
}
func TestInitialMetadataFailureNoSessionOrSlot(t *testing.T) {
	m, login, id, remote := setup(t)
	if _, e := m.db.Exec(`CREATE TRIGGER reject_session BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT,'fail'); END`); e != nil {
		t.Fatal(e)
	}
	if _, _, e := m.Create(login, id, 80, 24); e != fault.Error("STORAGE_ERROR") {
		t.Fatal(e)
	}
	if remote.Shells.Load() != 0 {
		t.Fatal("存储失败仍创建 Shell")
	}
	m.mu.Lock()
	n := len(m.sessions)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("发布失败 Session")
	}
	if e := m.servers.Delete(t.Context(), login, id); e != nil {
		t.Fatal("失败泄漏名额", e)
	}
}

func TestSSHOperationTimeoutAndKeepaliveReclaim(t *testing.T) {
	t.Run("input-timeout", func(t *testing.T) {
		gate := make(chan struct{})
		defer close(gate)
		m, login, id, _ := setupWith(t, testutil.TerminalOptions{Shell: func(ch ssh.Channel) { <-gate }}, func(c *config.Config) { c.Ssh.OperationTimeout = 1 })
		s := createSession(t, m, login, id)
		b := bind(t, m, s, login)
		result := make(chan error, 1)
		go func() { result <- s.Execute(b, bytes.Repeat([]byte("x"), 4<<20), 0, 0) }()
		select {
		case e := <-result:
			if e == nil {
				t.Fatal("阻塞输入没有失败")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("SSH 输入超时未解除")
		}
		<-s.Done()
		if s.reason != "ssh_error" {
			t.Fatal(s.reason)
		}
	})
	t.Run("keepalive-timeout", func(t *testing.T) {
		gate := make(chan struct{})
		defer close(gate)
		m, login, id, _ := setupWith(t, testutil.TerminalOptions{KeepaliveGate: gate}, func(c *config.Config) {
			c.Ssh.KeepaliveInterval = 1
			c.Ssh.KeepaliveTimeout = 1
			c.Ssh.KeepaliveMaxFailures = 2
		})
		s := createSession(t, m, login, id)
		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("Keepalive 未回收")
		}
		if s.reason != "keepalive_timeout" {
			t.Fatal(s.reason)
		}
	})
}
func TestCreateFailuresReleaseRealSessionSlots(t *testing.T) {
	for _, stage := range []string{"pty", "shell", "auth", "tcp"} {
		t.Run(stage, func(t *testing.T) {
			m, login, id, remote := setupWith(t, testutil.TerminalOptions{RejectPTY: stage == "pty", RejectShell: stage == "shell"}, nil)
			if stage == "auth" {
				port := remote.Port()
				if _, e := m.servers.Update(t.Context(), login, id, server.Input{Name: "wrong", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "wrong"}); e != nil {
					t.Fatal(e)
				}
			}
			if stage == "tcp" {
				remote.Listener.Close()
			}
			if _, _, e := m.Create(login, id, 80, 24); e == nil {
				t.Fatal("创建意外成功")
			}
			m.mu.Lock()
			n := len(m.sessions)
			m.mu.Unlock()
			if n != 0 {
				t.Fatal("创建失败仍有索引")
			}
			if e := m.servers.Delete(t.Context(), login, id); e != nil {
				t.Fatal("创建失败泄漏名额", e)
			}
		})
	}
}
func TestRuntimeMetadataFailureDoesNotRestoreBinding(t *testing.T) {
	m, login, id, _ := setup(t)
	s := createSession(t, m, login, id)
	if _, e := m.db.Exec(`CREATE TRIGGER reject_update BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'fail'); END`); e != nil {
		t.Fatal(e)
	}
	b := bind(t, m, s, login)
	s.Detach(b)
	s.Close("user_closed")
	<-s.Done()
	if s.View().Status != "CLOSED" {
		t.Fatal(s.View())
	}
	if e := m.servers.Delete(t.Context(), login, id); e != nil {
		t.Fatal(e)
	}
}

func TestCredentialOwnershipCapacityAndCounters(t *testing.T) {
	m, login, id, _ := setupWith(t, testutil.TerminalOptions{}, func(c *config.Config) { c.Session.BindingCredentialLimit = 1 })
	a := createSession(t, m, login, id)
	b := createSession(t, m, login, id)
	token, e := m.Prepare(login, a.id, "attach", 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Prepare(login, b.id, "attach", 0); e != fault.Error("CREDENTIAL_LIMIT") {
		t.Fatal(e)
	}
	other := &auth.Session{ID: strings.Repeat("b", 32), Expires: time.Now().Add(time.Hour)}
	if _, e = attachBinding(a, t.Context(), other, token.Credential, 0, func(string) {}); e != fault.Error("PERMISSION_DENIED") {
		t.Fatal(e)
	}
	if _, e = attachBinding(b, t.Context(), login, token.Credential, 0, func(string) {}); e != fault.Error("PERMISSION_DENIED") {
		t.Fatal(e)
	}
	binding, e := attachBinding(a, t.Context(), login, token.Credential, 0, func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	a.Detach(binding)
	if _, e = attachBinding(a, t.Context(), login, token.Credential, 0, func(string) {}); e != fault.Error("INVALID_CREDENTIAL") {
		t.Fatal(e)
	}
	if _, e = m.Prepare(login, a.id, "attach", 0); e != fault.Error("BIND_CONFLICT") {
		t.Fatal(e)
	}
	a.mu.Lock()
	a.generation = MaxCounter
	a.mu.Unlock()
	token, e = m.Prepare(login, a.id, "attach", MaxCounter)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = attachBinding(a, t.Context(), login, token.Credential, 0, func(string) {}); e != fault.Error("SESSION_NOT_READY") {
		t.Fatal(e)
	}
	<-a.Done()
	if a.reason != "counter_exhausted" {
		t.Fatal(a.reason)
	}
	b.buffer.mu.Lock()
	b.buffer.start = MaxCounter
	b.buffer.end = MaxCounter
	b.buffer.mu.Unlock()
	if _, e = b.Write([]byte("x")); e != ErrCounter {
		t.Fatal(e)
	}
	<-b.Done()
	if b.reason != "counter_exhausted" {
		t.Fatal(b.reason)
	}
}

func TestCreateSnapshotSurvivesEditAndProtectsDelete(t *testing.T) {
	gate := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	m, login, id, remote := setupWith(t, testutil.TerminalOptions{BlockRequest: "pty-req", Gate: gate}, nil)
	result := make(chan View, 1)
	go func() {
		v, _, e := m.Create(login, id, 80, 24)
		if e != nil {
			t.Error(e)
		}
		result <- v
	}()
	select {
	case request := <-remote.Requests:
		if request != "pty-req" {
			t.Fatal(request)
		}
	case <-time.After(time.Second):
		t.Fatal("未进入 PTY 创建")
	}
	if e := m.servers.Delete(t.Context(), login, id); e != fault.Error("SERVER_HAS_ACTIVE_SESSIONS") {
		t.Fatal(e)
	}
	port := remote.Port()
	if _, e := m.servers.Update(t.Context(), login, id, server.Input{Name: "new-name", Host: "127.0.0.1", Port: &port, Username: "different-user", Password: "different-password"}); e != nil {
		t.Fatal(e)
	}
	release.Do(func() { close(gate) })
	v := <-result
	if v.Name != "test" || v.Username != "tester" || v.Status != "DISCONNECTED" {
		t.Fatal(v)
	}
	s, e := m.Find(v.ID)
	if e != nil {
		t.Fatal(e)
	}
	binding := bind(t, m, s, login)
	if e := s.Execute(binding, []byte("snapshot-kept"), 0, 0); e != nil {
		t.Fatal(e)
	}
	waitBytes(t, s, []byte("snapshot-kept"))
}

func TestBusyCredentialNotConsumedAndGenerationPreserved(t *testing.T) {
	m, login, id, _ := setup(t)
	s := createSession(t, m, login, id)
	token, err := m.Prepare(login, s.id, "attach", 0)
	if err != nil {
		t.Fatal(err)
	}
	// 占住真实操作门，固定取消发生在取得许可之前。
	s.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = attachBinding(s, ctx, login, token.Credential, 0, func(string) {}); err != fault.Error("BIND_BUSY") {
		t.Fatal(err)
	}
	<-s.gate
	hash := sha256.Sum256([]byte(token.Credential))
	m.credentialsMu.Lock()
	_, exists := m.credentials[hash]
	m.credentialsMu.Unlock()
	if !exists || s.View().Generation != 0 {
		t.Fatal("失败已消费或增代")
	}
	b, err := attachBinding(s, t.Context(), login, token.Credential, 0, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Prepare(login, s.id, "attach", 1); err != fault.Error("BIND_BUSY") {
		t.Fatal(err)
	}
	m.credentialsMu.Lock()
	count := len(m.credentials)
	m.credentialsMu.Unlock()
	if count != 0 {
		t.Fatal("Prepare 忙仍签发凭证")
	}
	s.Detach(b)
	if s.View().Generation != 1 {
		t.Fatal("Detach 改代次")
	}
	next := bind(t, m, s, login)
	s.Detach(b)
	if s.View().Generation != 2 || s.View().Status != "CONNECTED" {
		t.Fatal("旧 Detach 影响新绑定")
	}
	if err = s.Execute(b, []byte("old-input"), 0, 0); err == nil {
		t.Fatal("旧输入获准")
	}
	if err = s.Execute(b, nil, 90, 25); err == nil {
		t.Fatal("旧 resize 获准")
	}
	s.Detach(next)
}

func TestAttachSnapshotCancellationAndOfflineScanOrdering(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "rebind"} {
		t.Run(mode, func(t *testing.T) {
			m, login, id, _ := setup(t)
			s := createSession(t, m, login, id)
			b := bind(t, m, s, login)
			s.Detach(b)
			token, err := m.Prepare(login, s.id, "attach", b.Generation)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m.beforeCredentialCommit = func() {
				if mode == "cancel" {
					cancel()
				}
				if mode == "deadline" {
					s.expires = time.Now().Add(-time.Second)
				}
			}
			next, err := attachBinding(s, ctx, login, token.Credential, 0, func(string) {})
			if mode == "rebind" {
				if err != nil {
					t.Fatal(err)
				}
				s.Detach(b)
				m.Sweep()
				if s.View().Status != "CONNECTED" || s.View().Generation != next.Generation {
					t.Fatal("扫描或旧回调关闭新绑定")
				}
			} else {
				want := fault.Error("BIND_BUSY")
				if mode == "deadline" {
					want = fault.Error("SESSION_NOT_READY")
				}
				if err != want || s.View().Generation != b.Generation {
					t.Fatal(err, s.View())
				}
				m.credentialsMu.Lock()
				_, exists := m.credentials[sha256.Sum256([]byte(token.Credential))]
				m.credentialsMu.Unlock()
				if !exists {
					t.Fatal("提交前失败消费凭证")
				}
			}
		})
	}
}

func TestBufferRawBoundariesAndStableCopies(t *testing.T) {
	for _, tc := range []struct {
		input, want     string
		capacity, lines int
	}{
		{"", "", 8, 2}, {"x", "x", 8, 2}, {"\n\n\n", "\n\n", 8, 2},
		{"a\r\nb\r", "a\r\nb\r", 16, 2}, {"a\nb\nc", "b\nc", 4, 2},
		{"abcdefghi", "fghi", 4, 8}, {"\x1b[31m中文\x1b[0m", "\x1b[31m中文\x1b[0m", 32, 2},
	} {
		for split := 0; split <= len(tc.input); split++ {
			b := NewBuffer(tc.capacity, tc.lines)
			b.Write([]byte(tc.input[:split]))
			b.Write([]byte(tc.input[split:]))
			replay, err := b.Snapshot(0)
			if err != nil || string(replay.Data) != tc.want || replay.Start != uint64(len(tc.input)-len(tc.want)) || replay.End != uint64(len(tc.input)) {
				t.Fatal(tc, split, replay, err)
			}
			copyOf := bytes.Clone(replay.Data)
			b.Write(bytes.Repeat([]byte{'Z'}, 2*tc.capacity))
			if !bytes.Equal(copyOf, replay.Data) {
				t.Fatal("快照改变")
			}
			if _, err := b.Read(replay.End, 1); err != ErrGap {
				t.Fatal("未报告实时游标缺口", err)
			}
		}
	}
}

func TestOneHundredSessionReclaims(t *testing.T) {
	m, login, id, remote := setupWith(t, testutil.TerminalOptions{}, func(c *config.Config) { c.Session.MaxSessionsPerServer = 1 })
	baseline := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		s := createSession(t, m, login, id)
		<-remote.Ready
		b := bind(t, m, s, login)
		s.Detach(b)
		var wg sync.WaitGroup
		for _, reason := range []string{"user_closed", "ssh_exit", "idle_timeout"} {
			wg.Add(1)
			go func() { defer wg.Done(); s.Close(reason) }()
		}
		wg.Wait()
		select {
		case <-s.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("回收超时", i)
		}
		s.Detach(b)
		if _, err := m.Get(s.id); err != fault.Error("SESSION_NOT_FOUND") {
			t.Fatal(err)
		}
		s.buffer.mu.Lock()
		released := s.buffer.data == nil
		s.buffer.mu.Unlock()
		if !released {
			t.Fatal("缓存未释放")
		}
		m.mu.Lock()
		count := len(m.sessions)
		m.mu.Unlock()
		if count != 0 {
			t.Fatal("索引未释放")
		}
	}
	if remote.Shells.Load() != 100 {
		t.Fatal(remote.Shells.Load())
	}
	if err := m.servers.Delete(t.Context(), login, id); err != nil {
		t.Fatal("名额未归还", err)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline+4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runtime.NumGoroutine() > baseline+4 {
		t.Fatalf("协程基线 %d，结束 %d", baseline, runtime.NumGoroutine())
	}
	t.Logf("100次真实SSH创建/关闭，goroutine baseline=%d final=%d", baseline, runtime.NumGoroutine())
}
