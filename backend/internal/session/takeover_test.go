package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

// 纯内存夹具无扫描协程；可在快照 hook 中精确推进时钟，不以 sleep 模拟到期。
func takeoverMemory(t *testing.T, connected bool) (*Manager, *Session, *auth.Session, *atomic.Int64) {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	m := &Manager{ctx: ctx, cfg: config.Defaults(), sessions: make(map[string]*Session), credentials: make(map[[32]byte]credential), now: func() time.Time { return time.Unix(0, clock.Load()) }}
	login := &auth.Session{ID: strings.Repeat("a", 32), Expires: m.now().Add(time.Hour)}
	s := &Session{manager: m, id: strings.Repeat("b", 32), ctx: ctx, cancel: cancel, status: "DISCONNECTED", created: m.now(), expires: m.now().Add(time.Minute), cols: 80, rows: 24, buffer: NewBuffer(64, 20), gate: make(chan struct{}, 1), dirty: make(chan struct{}, 1)}
	m.sessions[s.id] = s
	s.buffer.Write([]byte("replay-data"))
	if connected {
		c := prepareTakeover(t, m, s, login, "attach")
		b, err := attachBinding(s, t.Context(), login, c.Credential, 0, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		b.end = func(string) { s.Detach(b) }
	}
	return m, s, login, clock
}
func prepareTakeover(t *testing.T, m *Manager, s *Session, login *auth.Session, mode string) CredentialView {
	t.Helper()
	c, err := m.Prepare(login, s.id, mode, s.View().Generation)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func commitTakeover(t *testing.T, s *Session, login *auth.Session, c CredentialView) *CommitResult {
	t.Helper()
	r, err := s.Attach(t.Context(), login, c.Credential, 0, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func credentialPresent(m *Manager, c CredentialView) bool {
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	_, ok := m.credentials[sha256.Sum256([]byte(c.Credential))]
	return ok
}
func receiveTakeover[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(4 * time.Second):
		t.Fatal("同步屏障超时")
	}
	var zero T
	return zero
}
func assertCurrent(t *testing.T, s *Session, b *Binding, generation uint64) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.binding != b || s.generation != generation || s.status != "CONNECTED" || !b.active || b.superseded || !s.expires.IsZero() {
		t.Fatalf("当前 Binding/状态错误: binding=%p want=%p generation=%d status=%s", s.binding, b, s.generation, s.status)
	}
}

func TestTakeoverU05PrepareMatrix(t *testing.T) {
	for _, mode := range []string{"attach", "takeover"} {
		for _, state := range []string{"CONNECTED", "DISCONNECTED", "CONNECTING", "CLOSING", "CLOSED", "expired"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				m, s, login, _ := takeoverMemory(t, state == "CONNECTED")
				s.mu.Lock()
				if state == "expired" {
					s.expires = m.now()
					s.status = "DISCONNECTED"
				} else {
					s.status = state
				}
				before := s.viewLocked()
				old := s.binding
				deadline := s.expires
				s.mu.Unlock()
				// Prepare 不需要操作门，即使已有 SSH 操作也能只读签发。
				s.gate <- struct{}{}
				_, err := m.Prepare(login, s.id, mode, before.Generation)
				<-s.gate
				var want error
				switch state {
				case "CONNECTED":
					if mode == "attach" {
						want = fault.Error("BIND_BUSY")
					}
				case "DISCONNECTED":
				case "CLOSED":
					want = fault.Error("SESSION_NOT_FOUND")
				default:
					want = fault.Error("SESSION_NOT_READY")
				}
				if err != want {
					t.Fatalf("got %v want %v", err, want)
				}
				s.mu.Lock()
				unchanged := reflect.DeepEqual(before, s.viewLocked()) && deadline == s.expires && old == s.binding
				if old != nil {
					unchanged = unchanged && old.active && !old.superseded && old.ctx.Err() == nil
				}
				s.mu.Unlock()
				if !unchanged {
					t.Fatal("Prepare 改变会话或旧 Binding")
				}
				if state == "CONNECTED" || state == "DISCONNECTED" {
					if _, err = m.Prepare(login, s.id, mode, before.Generation+1); err != fault.Error("BIND_CONFLICT") {
						t.Fatal("代次冲突未优先", err)
					}
				}
			})
		}
	}
	t.Run("invalid mode and generation", func(t *testing.T) {
		m, s, login, _ := takeoverMemory(t, false)
		for _, mode := range []string{"", "TAKEOVER", "unknown"} {
			if _, err := m.Prepare(login, s.id, mode, 0); err != fault.Error("INVALID_REQUEST") {
				t.Fatal(err)
			}
		}
		if _, err := m.Prepare(login, s.id, "takeover", MaxCounter+1); err != fault.Error("INVALID_REQUEST") {
			t.Fatal(err)
		}
	})
}

func TestTakeoverU06CredentialIdentityAndConsumption(t *testing.T) {
	m, s, login, _ := takeoverMemory(t, false)
	m.cfg.Session.BindingCredentialLimit = 2
	a := prepareTakeover(t, m, s, login, "attach")
	b := prepareTakeover(t, m, s, login, "takeover")
	for _, tc := range []struct {
		c    CredentialView
		mode string
	}{{a, "attach"}, {b, "takeover"}} {
		stored := m.credentials[sha256.Sum256([]byte(tc.c.Credential))]
		if stored.mode != tc.mode || stored.loginID != login.ID || stored.sessionID != s.id || stored.expected != 0 || stored.expires.Sub(m.now()) != time.Duration(m.cfg.Session.BindingCredentialTtl)*time.Second {
			t.Fatalf("凭证字段错误: %+v", stored)
		}
	}
	if _, err := m.Prepare(login, s.id, "takeover", 0); err != fault.Error("CREDENTIAL_LIMIT") {
		t.Fatal(err)
	}
	other := &auth.Session{ID: strings.Repeat("c", 32), Expires: login.Expires}
	if _, err := s.Attach(t.Context(), other, b.Credential, 0, func(string) {}); err != fault.Error("PERMISSION_DENIED") {
		t.Fatal(err)
	}
	_, target, _, _ := takeoverMemory(t, false)
	target.manager = m
	target.id = strings.Repeat("d", 32)
	if _, err := target.Attach(t.Context(), login, b.Credential, 0, func(string) {}); err != fault.Error("PERMISSION_DENIED") {
		t.Fatal(err)
	}
	if !credentialPresent(m, a) || !credentialPresent(m, b) {
		t.Fatal("归属失败消费凭证")
	}
	// 容量拒绝已验证；额外签发另一 Session 的凭证，接管不得跨 Session 清理。
	m.cfg.Session.BindingCredentialLimit = 3
	m.sessions[target.id] = target
	otherCredential := prepareTakeover(t, m, target, login, "takeover")
	r := commitTakeover(t, s, login, b)
	r.FinishDisplaced()
	if !credentialPresent(m, otherCredential) {
		t.Fatal("成功提交错误清理其他 Session 凭证")
	}
	if r.DisplacedBinding != nil || r.NewBinding.Generation != 1 {
		t.Fatal("离线 takeover 提交错误")
	}
	if credentialPresent(m, a) || credentialPresent(m, b) {
		t.Fatal("成功后未消费同 Session 凭证")
	}
	if _, err := s.Attach(t.Context(), login, b.Credential, 0, func(string) {}); err != fault.Error("INVALID_CREDENTIAL") {
		t.Fatal(err)
	}
	// 将先前的 attach 记录作为内部故障注入恢复到当前代次；提交只能服从
	// 服务端记录中的 attach 模式，调用者没有传入 mode 的途径。
	hash := sha256.Sum256([]byte(a.Credential))
	m.credentials[hash] = credential{loginID: login.ID, sessionID: s.id, mode: "attach", expected: 1, expires: m.now().Add(time.Minute)}
	if _, err := s.Attach(t.Context(), login, a.Credential, 0, func(string) {}); err != fault.Error("BIND_BUSY") {
		t.Fatal("attach 覆盖在线绑定", err)
	}
	assertCurrent(t, s, r.NewBinding, 1)
}

func TestTakeoverU06TTLAndCleanup(t *testing.T) {
	m, s, login, clock := takeoverMemory(t, false)
	m.cfg.Session.BindingCredentialLimit = 1
	c := prepareTakeover(t, m, s, login, "takeover")
	clock.Add(int64(time.Duration(m.cfg.Session.BindingCredentialTtl) * time.Second))
	if _, err := s.Attach(t.Context(), login, c.Credential, 0, func(string) {}); err != fault.Error("INVALID_CREDENTIAL") {
		t.Fatal(err)
	}
	fresh := prepareTakeover(t, m, s, login, "takeover")
	if credentialPresent(m, c) || !credentialPresent(m, fresh) {
		t.Fatal("签发未清理到期凭证")
	}
}

func TestTakeoverU07PrecommitFailures(t *testing.T) {
	for _, connected := range []bool{false, true} {
		for _, failure := range []string{"offset", "credential_expired", "credential_removed", "credential_changed", "login_expired", "offline_expired", "cancel", "shutdown"} {
			if connected && failure == "offline_expired" {
				continue
			}
			t.Run(fmt.Sprintf("connected=%t/%s", connected, failure), func(t *testing.T) {
				m, s, login, clock := takeoverMemory(t, connected)
				if failure == "login_expired" {
					login.Expires = m.now().Add(time.Second)
				}
				if failure == "offline_expired" {
					s.expires = m.now().Add(time.Second)
				}
				c := prepareTakeover(t, m, s, login, "takeover")
				hash := sha256.Sum256([]byte(c.Credential))
				if failure == "credential_expired" {
					stored := m.credentials[hash]
					stored.expires = m.now().Add(time.Second)
					m.credentials[hash] = stored
				}
				old := s.binding
				generation := s.generation
				state := s.status
				deadline := s.expires
				replay, _ := s.buffer.Snapshot(0)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				copied := false
				m.beforeCredentialCommit = func() {
					copied = true
					switch failure {
					case "credential_expired", "login_expired", "offline_expired":
						clock.Add(int64(time.Second))
					case "credential_removed":
						m.credentialsMu.Lock()
						delete(m.credentials, hash)
						m.credentialsMu.Unlock()
					case "credential_changed":
						m.credentialsMu.Lock()
						stored := m.credentials[hash]
						stored.mode = "attach"
						m.credentials[hash] = stored
						m.credentialsMu.Unlock()
					case "cancel":
						cancel()
					case "shutdown":
						s.cancel()
					}
				}
				offset := uint64(0)
				want := fault.Error("INVALID_CREDENTIAL")
				switch failure {
				case "offset":
					offset = replay.End + 1
					want = fault.Error("INVALID_OFFSET")
				case "login_expired":
					want = fault.Error("AUTH_REQUIRED")
				case "offline_expired":
					want = fault.Error("SESSION_NOT_READY")
				case "cancel":
					want = fault.Error("BIND_BUSY")
				case "shutdown":
					want = fault.Error("SERVER_SHUTTING_DOWN")
				}
				r, err := s.Attach(ctx, login, c.Credential, offset, func(string) { t.Error("失败候选收到结束回调") })
				if err != want || r != nil {
					t.Fatalf("result=%v error=%v want=%v", r, err, want)
				}
				if copied != (failure != "offset") {
					t.Fatal("失败未发生在预期快照边界")
				}
				if s.binding != old || s.generation != generation || s.status != state || s.expires != deadline {
					t.Fatal("提交前失败改变状态")
				}
				if old != nil && (!old.active || old.superseded || (failure != "shutdown" && old.ctx.Err() != nil)) {
					t.Fatal("提交前失败撤销旧权限")
				}
				after, _ := s.buffer.Snapshot(0)
				if !reflect.DeepEqual(after, replay) {
					t.Fatal("失败改变缓存；候选快照不应发布")
				}
				if failure != "credential_removed" && !credentialPresent(m, c) {
					t.Fatal("未成功却消费凭证")
				}
				if failure == "cancel" || failure == "offset" {
					m.beforeCredentialCommit = nil
					retry := commitTakeover(t, s, login, c)
					retry.FinishDisplaced()
					assertCurrent(t, s, retry.NewBinding, generation+1)
				}
			})
		}
	}
}

func TestTakeoverU08HandoffAndLateDetach(t *testing.T) {
	for _, failure := range []string{"candidate_cancelled", "registration_failed", "writer_unavailable", "replaced_again"} {
		t.Run(failure, func(t *testing.T) {
			m, s, login, _ := takeoverMemory(t, true)
			old := s.binding
			calls := 0
			old.end = func(reason string) {
				calls++
				if reason != "taken_over" {
					t.Fatal(reason)
				}
				if !s.mu.TryLock() {
					t.Fatal("回调仍持 Session 锁")
				}
				s.mu.Unlock()
				if !s.buffer.mu.TryLock() {
					t.Fatal("回调仍持 Buffer 锁")
				}
				s.buffer.mu.Unlock()
				if !m.credentialsMu.TryLock() {
					t.Fatal("回调仍持凭证锁")
				}
				m.credentialsMu.Unlock()
				select {
				case s.gate <- struct{}{}:
					<-s.gate
				default:
					t.Fatal("回调仍持操作门")
				}
				s.Detach(old)
			}
			c := prepareTakeover(t, m, s, login, "takeover")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r, err := s.Attach(ctx, login, c.Credential, 0, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			if r.DisplacedBinding != old || old.active || !old.superseded || old.ctx.Err() != nil || calls != 0 {
				t.Fatal("提交提前取消或丢失旧对象")
			}
			assertCurrent(t, s, r.NewBinding, 2)
			// 回调尚未执行、旧 Done 尚未取消时，身份检查已经拒绝旧 SSH 操作。
			if err := s.Execute(old, []byte("stale"), 0, 0); err != fault.Error("BIND_CONFLICT") {
				t.Fatal(err)
			}
			if err := s.Execute(old, nil, 100, 40); err != fault.Error("BIND_CONFLICT") {
				t.Fatal(err)
			}
			// 模拟调用方登记/启动失败，defer 必须在任何退出路径执行；不测 WS 网络行为。
			var latest *Binding
			func() {
				defer r.FinishDisplaced()
				cancel()
				if failure == "replaced_again" {
					next := prepareTakeover(t, m, s, login, "takeover")
					newer := commitTakeover(t, s, login, next)
					defer newer.FinishDisplaced()
					latest = newer.NewBinding
				}
				s.Detach(r.NewBinding)
			}()
			r.FinishDisplaced()
			if calls != 1 || old.ctx.Err() == nil || old.active {
				t.Fatal("收尾遗漏/重复或旧权限恢复")
			}
			if latest != nil {
				assertCurrent(t, s, latest, 3)
			} else if s.View().Status != "DISCONNECTED" || s.View().Generation != 2 {
				t.Fatal(s.View())
			}
			view := s.View()
			s.Detach(old)
			s.Detach(r.NewBinding)
			if !reflect.DeepEqual(view, s.View()) {
				t.Fatal("迟到 Detach 改变新对象或离线期限")
			}
		})
	}
}

// WithTimeout 在尝试操作门之前调用父 context.Deadline；在此汇合所有
// Attach 调用，避免仅凭 goroutine 已启动推断候选已进入真实提交路径。
type candidateEntryContext struct {
	context.Context
	entered chan<- struct{}
	proceed <-chan struct{}
	once    sync.Once
}

func (c *candidateEntryContext) Deadline() (time.Time, bool) {
	c.once.Do(func() { c.entered <- struct{}{}; <-c.proceed })
	return c.Context.Deadline()
}

func TestTakeoverR01R02CredentialCompetition(t *testing.T) {
	for _, kind := range []string{"distinct", "same_token", "attach_takeover"} {
		t.Run(kind, func(t *testing.T) {
			m, s, login, _ := takeoverMemory(t, kind != "attach_takeover")
			old := s.binding
			generation := s.generation
			tokens := make([]CredentialView, 8)
			for i := range tokens {
				mode := "takeover"
				if kind == "attach_takeover" && i%2 == 0 {
					mode = "attach"
				}
				if kind == "same_token" && i > 0 {
					tokens[i] = tokens[0]
				} else {
					tokens[i] = prepareTakeover(t, m, s, login, mode)
				}
			}
			m.credentialsMu.Lock()
			for _, c := range tokens {
				stored, exists := m.credentials[sha256.Sum256([]byte(c.Credential))]
				if !exists || stored.sessionID != s.id || stored.expected != generation || stored.loginID != login.ID || !m.now().Before(stored.expires) {
					m.credentialsMu.Unlock()
					t.Fatal("候选凭证不属于同一 Session/代次或已经无效")
				}
			}
			m.credentialsMu.Unlock()
			entered, enterProceed := make(chan struct{}, len(tokens)), make(chan struct{})
			var releaseEntry sync.Once
			unblockEntry := func() { releaseEntry.Do(func() { close(enterProceed) }) }
			defer unblockEntry()
			ready := make(chan struct{}, len(tokens))
			start := make(chan struct{})
			type attempt struct {
				result *CommitResult
				err    error
			}
			results := make(chan attempt, len(tokens))
			// 所有候选在门前汇合，首个快照暂停，其他候选仍不能越过门。
			snapshot, proceed := make(chan struct{}), make(chan struct{})
			var releaseSnapshot sync.Once
			unblockSnapshot := func() { releaseSnapshot.Do(func() { close(proceed) }) }
			defer unblockSnapshot()
			m.beforeCredentialCommit = func() { close(snapshot); <-proceed }
			for _, c := range tokens {
				go func() {
					ready <- struct{}{}
					<-start
					ctx := &candidateEntryContext{Context: t.Context(), entered: entered, proceed: enterProceed}
					r, err := s.Attach(ctx, login, c.Credential, 0, func(string) {})
					results <- attempt{r, err}
				}()
			}
			for range tokens {
				receiveTakeover(t, ready)
			}
			close(start)
			for range tokens {
				receiveTakeover(t, entered)
			}
			// 八次真实 Attach 均已通过 token 格式校验、进入操作门等待准备阶段。
			if s.View().Generation != generation {
				t.Fatal("入口屏障前已经提交")
			}
			unblockEntry()
			receiveTakeover(t, snapshot)
			unblockSnapshot()
			wins := 0
			var winner *Binding
			for range tokens {
				got := receiveTakeover(t, results)
				r, err := got.result, got.err
				if (r == nil) != (err != nil) {
					t.Fatal("提交结果与错误不一致")
				}
				if err != nil && err != fault.Error("INVALID_CREDENTIAL") && err != fault.Error("BIND_CONFLICT") {
					t.Fatal(err)
				}
				if r != nil {
					wins++
					winner = r.NewBinding
					r.FinishDisplaced()
				}
			}
			if wins != 1 {
				t.Fatal("赢家数量", wins)
			}
			assertCurrent(t, s, winner, generation+1)
			if old != nil && (old.active || !old.superseded) {
				t.Fatal("旧对象未撤权")
			}
			m.beforeCredentialCommit = nil
			if _, err := m.Prepare(login, s.id, "attach", generation+1); err != fault.Error("BIND_BUSY") {
				t.Fatal(err)
			}
			next := prepareTakeover(t, m, s, login, "takeover")
			r := commitTakeover(t, s, login, next)
			r.FinishDisplaced()
			assertCurrent(t, s, r.NewBinding, generation+2)
		})
	}
}

func TestTakeoverR03OperationGate(t *testing.T) {
	for _, operation := range []string{"write", "resize"} {
		for _, outcome := range []string{"commit", "timeout"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				m, login, id, remote := setupWith(t, testutil.TerminalOptions{}, func(c *config.Config) { c.Session.OperationWaitTimeout = 1 })
				s := createSession(t, m, login, id)
				receiveTakeover(t, remote.Ready)
				old := bind(t, m, s, login)
				admitted, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				var operations atomic.Int64
				s.operationAdmitted = func() {
					if operations.Add(1) == 1 {
						close(admitted)
						<-release
					}
				}
				old.end = func(string) { s.Detach(old) }
				data := []byte("before-takeover")
				cols, rows := 0, 0
				if operation == "resize" {
					data = nil
					cols = 91
					rows = 31
				}
				executed := make(chan error, 1)
				go func() { executed <- s.Execute(old, data, cols, rows) }()
				receiveTakeover(t, admitted)
				c := prepareTakeover(t, m, s, login, "takeover")
				type attempt struct {
					r   *CommitResult
					err error
				}
				result := make(chan attempt, 1)
				started := make(chan struct{})
				snapshot, finish := make(chan struct{}), make(chan struct{})
				if outcome == "commit" {
					m.beforeCredentialCommit = func() { close(snapshot); <-finish }
				}
				go func() {
					close(started)
					r, err := s.Attach(t.Context(), login, c.Credential, 0, func(string) {})
					result <- attempt{r, err}
				}()
				receiveTakeover(t, started)
				if outcome == "timeout" {
					got := receiveTakeover(t, result)
					if got.err != fault.Error("BIND_BUSY") || got.r != nil || !credentialPresent(m, c) {
						t.Fatal("操作门超时改变凭证", got.err)
					}
					assertCurrent(t, s, old, 1)
					if old.ctx.Err() != nil {
						t.Fatal("超时提前取消旧对象")
					}
					unblock()
					if err := receiveTakeover(t, executed); err != nil {
						t.Fatal(err)
					}
					r := commitTakeover(t, s, login, c)
					r.FinishDisplaced()
					assertCurrent(t, s, r.NewBinding, 2)
				} else {
					select {
					case got := <-result:
						t.Fatalf("在途 SSH 未结束却已提交: %+v", got)
					default:
					}
					unblock()
					receiveTakeover(t, snapshot)
					if err := receiveTakeover(t, executed); err != nil {
						t.Fatal(err)
					}
					// 候选已取得门并停在快照内，旧排队项在此期间发起，绝不能获得 SSH 准入。
					queued := make(chan error, 2)
					queuedStart := make(chan struct{}, 2)
					for _, resize := range []bool{false, true} {
						go func() {
							queuedStart <- struct{}{}
							if resize {
								queued <- s.Execute(old, nil, 99, 35)
							} else {
								queued <- s.Execute(old, []byte("forbidden-after-takeover"), 0, 0)
							}
						}()
					}
					receiveTakeover(t, queuedStart)
					receiveTakeover(t, queuedStart)
					close(finish)
					got := receiveTakeover(t, result)
					if got.err != nil {
						t.Fatal(got.err)
					}
					got.r.FinishDisplaced()
					for i := 0; i < 2; i++ {
						if err := receiveTakeover(t, queued); err == nil {
							t.Fatal("旧排队项获准")
						}
					}
					assertCurrent(t, s, got.r.NewBinding, 2)
				}
				if operations.Load() != 1 {
					t.Fatal("旧排队项产生额外远端操作", operations.Load())
				}
				if operation == "write" {
					waitBytes(t, s, []byte("before-takeover"))
				} else if s.View().Cols != 91 || s.View().Rows != 31 {
					t.Fatal("已准入 resize 未先完成", s.View())
				}
				replay, _ := s.buffer.Snapshot(0)
				if bytes.Contains(replay.Data, []byte("forbidden-after-takeover")) {
					t.Fatal("旧输入写入远端")
				}
				if remote.Shells.Load() != 1 {
					t.Fatal("接管重建 SSH")
				}
				s.Close("user_closed")
				receiveTakeover(t, s.Done())
				count := 0
				for {
					select {
					case request := <-remote.Requests:
						if request == "window-change" {
							count++
						}
					default:
						goto checked
					}
				}
			checked:
				expected := 0
				if operation == "resize" {
					expected = 1
				}
				if count != expected {
					t.Fatalf("实际 resize 次数 %d want %d", count, expected)
				}
			})
		}
	}
}

func TestTakeoverR04CloseSSHAndSweep(t *testing.T) {
	for _, cause := range []string{"close", "ssh_exit", "sweep"} {
		for _, first := range []string{"lifecycle", "takeover"} {
			t.Run(cause+"/"+first, func(t *testing.T) {
				m, login, id, remote := setupWith(t, testutil.TerminalOptions{}, nil)
				s := createSession(t, m, login, id)
				channel := receiveTakeover(t, remote.Ready)
				var old *Binding
				if cause != "sweep" {
					old = bind(t, m, s, login)
					old.end = func(string) { s.Detach(old) }
				}
				c := prepareTakeover(t, m, s, login, "takeover")
				lifecycle := func() {
					switch cause {
					case "close":
						s.Close("user_closed")
					case "ssh_exit":
						channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
						channel.Close()
					case "sweep":
						s.mu.Lock()
						s.expires = time.Now().Add(-time.Second)
						s.mu.Unlock()
						m.Sweep()
					}
				}
				if first == "lifecycle" {
					lifecycle()
					receiveTakeover(t, s.Done())
					generation := s.View().Generation
					r, err := s.Attach(t.Context(), login, c.Credential, 0, func(string) {})
					if r != nil || (err != fault.Error("INVALID_CREDENTIAL") && err != fault.Error("SESSION_NOT_READY")) {
						t.Fatal(r, err)
					}
					if s.View().Status != "CLOSED" || s.View().Generation != generation {
						t.Fatal("关闭状态恢复")
					}
					expected := map[string]string{"close": "user_closed", "ssh_exit": "ssh_exit", "sweep": "idle_timeout"}[cause]
					if s.reason != expected {
						t.Fatalf("首关闭原因 %s want %s", s.reason, expected)
					}
				} else {
					r := commitTakeover(t, s, login, c)
					r.FinishDisplaced()
					if cause == "sweep" {
						// 查询/扫描早已取得的引用也必须在 Session 锁下重看状态；接管后扫描
						// 不可把仍 CONNECTED 的对象当作之前的离线对象关闭。
						m.Sweep()
						assertCurrent(t, s, r.NewBinding, r.NewBinding.Generation)
						s.Detach(old)
						assertCurrent(t, s, r.NewBinding, r.NewBinding.Generation)
						s.Close("user_closed")
					} else {
						lifecycle()
					}
					receiveTakeover(t, s.Done())
					if s.View().Status != "CLOSED" || r.NewBinding.active || r.NewBinding.ctx.Err() == nil {
						t.Fatal("提交后关闭未撤权")
					}
					expected := "user_closed"
					if cause == "ssh_exit" {
						expected = "ssh_exit"
					}
					if s.reason != expected {
						t.Fatal("首因被改写", s.reason)
					}
					s.Detach(old)
					s.Detach(r.NewBinding)
					if s.View().Status != "CLOSED" || !s.expires.IsZero() {
						t.Fatal("迟到回调回退关闭")
					}
				}
				if remote.Shells.Load() != 1 || len(m.List("")) != 0 {
					t.Fatal("回收后残留 Session/额外 Shell")
				}
			})
		}
	}
}

func TestTakeoverR04OldSweepAfterCommit(t *testing.T) {
	m, s, login, clock := takeoverMemory(t, false)
	s.expires = m.now().Add(time.Second)
	c := prepareTakeover(t, m, s, login, "takeover")
	captured, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	m.now = func() time.Time {
		stamp := time.Unix(0, clock.Load())
		if first.CompareAndSwap(false, true) {
			close(captured)
			<-release
		}
		return stamp
	}
	scanned := make(chan struct{})
	go func() { m.Sweep(); close(scanned) }()
	receiveTakeover(t, captured)
	r := commitTakeover(t, s, login, c)
	r.FinishDisplaced()
	// 扫描在提交前已取得查询时刻，提交后才继续处理同一对象。
	clock.Add(int64(2 * time.Second))
	close(release)
	receiveTakeover(t, scanned)
	m.Sweep()
	assertCurrent(t, s, r.NewBinding, 1)
}

// 仅在测试中观测现有错误日志：第一次失败暂停 worker，后续失败只发通知。
// Handler 不持 Session/数据库锁；释放通道在所有退出路径都会关闭。
type metadataFailureObserver struct {
	slog.Handler
	failures chan string
	release  chan struct{}
	count    atomic.Int64
}

func (h *metadataFailureObserver) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "Session 元数据更新失败" {
		id := ""
		record.Attrs(func(a slog.Attr) bool {
			if a.Key == "session_id" {
				id = a.Value.String()
			}
			return true
		})
		if h.count.Add(1) == 1 {
			h.failures <- id
			<-h.release
		} else {
			select {
			case h.failures <- id:
			default:
			}
		}
	}
	return h.Handler.Handle(ctx, record)
}

func TestTakeoverU09CountersOffsetsAndMetadata(t *testing.T) {
	t.Run("offset boundaries", func(t *testing.T) {
		for _, offset := range []uint64{0, MaxCounter - 2, MaxCounter, MaxCounter + 1} {
			m, s, login, _ := takeoverMemory(t, true)
			s.buffer.mu.Lock()
			s.buffer.start = MaxCounter - 2
			s.buffer.end = MaxCounter
			s.buffer.size = 2
			s.buffer.head = 0
			s.buffer.data[0] = 'x'
			s.buffer.data[1] = 'y'
			s.buffer.mu.Unlock()
			c := prepareTakeover(t, m, s, login, "takeover")
			r, err := s.Attach(t.Context(), login, c.Credential, offset, func(string) {})
			if offset > MaxCounter {
				if err != fault.Error("INVALID_OFFSET") || r != nil || !credentialPresent(m, c) {
					t.Fatal(err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			r.FinishDisplaced()
			replay := r.NewBinding.Replay
			if replay.Requested != offset || replay.Start != max(offset, MaxCounter-2) || replay.End != MaxCounter || uint64(len(replay.Data)) != replay.End-replay.Start {
				t.Fatal(replay)
			}
		}
	})
	t.Run("generation exhausted", func(t *testing.T) {
		m, login, id, _ := setup(t)
		s := createSession(t, m, login, id)
		old := bind(t, m, s, login)
		s.mu.Lock()
		s.generation = MaxCounter
		s.mu.Unlock()
		c := prepareTakeover(t, m, s, login, "takeover")
		r, err := s.Attach(t.Context(), login, c.Credential, 0, func(string) {})
		if err != fault.Error("SESSION_NOT_READY") || r != nil {
			t.Fatal(err)
		}
		receiveTakeover(t, s.Done())
		if s.reason != "counter_exhausted" || s.generation != MaxCounter || old.active || old.superseded {
			t.Fatal("耗尽回绕或伪造接管")
		}
	})
	t.Run("metadata failure", func(t *testing.T) {
		m, login, id, _ := setup(t)
		observer := &metadataFailureObserver{
			Handler:  slog.NewTextHandler(io.Discard, nil),
			failures: make(chan string, 8), release: make(chan struct{}),
		}
		var release sync.Once
		unblock := func() { release.Do(func() { close(observer.release) }) }
		// defer 先于 setup 注册的 Cleanup 执行，失败分支也不会把 metadata worker 留住。
		defer unblock()
		// 此时尚无 Session/metadata worker；创建之前安装日志观测器。
		m.log = slog.New(observer)
		// 仅测试数据库故障注入；所有 sessions UPDATE 都由真实 metadata worker 执行。
		if _, err := m.db.Exec(`CREATE TRIGGER reject_takeover_update BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
			t.Fatal(err)
		}
		s := createSession(t, m, login, id)
		old := bind(t, m, s, login)
		old.end = func(string) { s.Detach(old) }
		if failedID := receiveTakeover(t, observer.failures); failedID != s.id {
			t.Fatal("基线失败不属于目标 Session", failedID)
		}
		// worker 已完成旧 UPDATE 并阻塞在首次错误日志。排空创建/首次 attach 的
		// 合并通知，使接下来唯一待处理的 dirty 明确来自 takeover 提交。
		for {
			select {
			case <-s.dirty:
			default:
				goto drained
			}
		}
	drained:
		c := prepareTakeover(t, m, s, login, "takeover")
		if len(s.dirty) != 0 {
			t.Fatal("Prepare 意外产生 metadata 通知")
		}
		r := commitTakeover(t, s, login, c)
		r.FinishDisplaced()
		if len(s.dirty) != 1 || observer.count.Load() != 1 {
			t.Fatal("takeover 未留下唯一 dirty，或 worker 提前越过屏障")
		}
		// 旧对象 Detach 不是当前对象，不产生 dirty。释放后 worker 只能消费
		// takeover 留下的通知，读取新 CONNECTED 状态并执行真实 UPDATE。
		unblock()
		if failedID := receiveTakeover(t, observer.failures); failedID != s.id || observer.count.Load() != 2 {
			t.Fatal("未观测到 takeover 对应 UPDATE 的失败", failedID, observer.count.Load())
		}
		if len(s.dirty) != 0 {
			t.Fatal("takeover 通知未被消费")
		}
		t.Log("已观测真实 metadata worker：旧写入失败暂停 → 清空旧通知 → takeover 唯一 dirty → 新 UPDATE 失败")
		assertCurrent(t, s, r.NewBinding, 2)
		items := m.List("")
		if len(items) != 1 || items[0].Status != "CONNECTED" || items[0].Generation != 2 {
			t.Fatal(items)
		}
		if old.active || !old.superseded {
			t.Fatal("元数据失败恢复旧权限")
		}
		s.Close("user_closed")
		receiveTakeover(t, s.Done())
		if len(m.List("")) != 0 {
			t.Fatal("元数据失败阻止回收")
		}
	})
}

func TestTakeoverR05RealLogoutOrdering(t *testing.T) {
	for _, first := range []string{"logout_before_prepare", "logout_before_commit", "commit_before_logout", "rollback"} {
		t.Run(first, func(t *testing.T) {
			m, creationLogin, id, remote := setup(t)
			s := createSession(t, m, creationLogin, id)
			admin, err := auth.New(t.Context(), m.db, m.cfg.Auth, "admin-secret")
			if err != nil {
				t.Fatal(err)
			}
			admin.OnRevoke(m.Revoke)
			token, login, _, err := admin.Login(t.Context(), "admin-secret")
			if err != nil {
				t.Fatal(err)
			}
			old := bind(t, m, s, login)
			old.end = func(string) { s.Detach(old) }
			c := prepareTakeover(t, m, s, login, "takeover")
			if first == "rollback" {
				// 登录删除确定回滚时，既有登录、凭证和 Binding 必须保持。
				if _, err = m.db.Exec(`CREATE TRIGGER reject_takeover_logout BEFORE DELETE ON web_auth_sessions BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
					t.Fatal(err)
				}
				if err = admin.Logout(t.Context(), token); err != fault.Error("STORAGE_ERROR") {
					t.Fatal(err)
				}
				assertCurrent(t, s, old, 1)
				r := commitTakeover(t, s, login, c)
				r.FinishDisplaced()
				assertCurrent(t, s, r.NewBinding, 2)
			} else if first == "commit_before_logout" {
				snapshot, release := make(chan struct{}), make(chan struct{})
				m.beforeCredentialCommit = func() { close(snapshot); <-release }
				committed := make(chan *CommitResult, 1)
				failures := make(chan error, 1)
				go func() {
					r, e := s.Attach(t.Context(), login, c.Credential, 0, func(string) {})
					committed <- r
					failures <- e
				}()
				receiveTakeover(t, snapshot)
				loggedOut := make(chan error, 1)
				started := make(chan struct{})
				go func() { close(started); loggedOut <- admin.Logout(t.Context(), token) }()
				receiveTakeover(t, started)
				close(release)
				r := receiveTakeover(t, committed)
				if err = receiveTakeover(t, failures); err != nil {
					t.Fatal(err)
				}
				r.FinishDisplaced()
				if err = receiveTakeover(t, loggedOut); err != nil {
					t.Fatal(err)
				}
				if r.NewBinding.active || r.NewBinding.ctx.Err() == nil || s.View().Generation != 2 || s.View().Status != "DISCONNECTED" {
					t.Fatal("登出扫描遗漏刚发布的 Binding")
				}
			} else {
				if err = admin.Logout(t.Context(), token); err != nil {
					t.Fatal(err)
				}
				if first == "logout_before_prepare" {
					if _, err = m.Prepare(login, s.id, "takeover", 1); err != fault.Error("AUTH_REQUIRED") {
						t.Fatal(err)
					}
				}
				r, e := s.Attach(t.Context(), login, c.Credential, 0, func(string) {})
				if r != nil || e != fault.Error("AUTH_REQUIRED") {
					t.Fatal(r, e)
				}
				if s.View().Generation != 1 || s.View().Status != "DISCONNECTED" || old.active {
					t.Fatal("撤销后又提交")
				}
			}
			if first != "rollback" && credentialPresent(m, c) {
				t.Fatal("撤销漏清凭证")
			}
			if remote.Shells.Load() != 1 || len(m.List("")) != 1 {
				t.Fatal("登出错误关闭 SSH")
			}
		})
	}
}

func TestTakeoverR05DifferentLogins(t *testing.T) {
	m, creationLogin, id, remote := setup(t)
	s := createSession(t, m, creationLogin, id)
	admin, err := auth.New(t.Context(), m.db, m.cfg.Auth, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	admin.OnRevoke(m.Revoke)
	tokenA, a, _, err := admin.Login(t.Context(), "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	tokenB, b, _, err := admin.Login(t.Context(), "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	old := bind(t, m, s, a)
	old.end = func(string) { s.Detach(old) }
	c := prepareTakeover(t, m, s, b, "takeover")
	r := commitTakeover(t, s, b, c)
	if err = admin.Logout(t.Context(), tokenA); err != nil {
		t.Fatal(err)
	}
	assertCurrent(t, s, r.NewBinding, 2)
	r.FinishDisplaced()
	assertCurrent(t, s, r.NewBinding, 2)
	if err = admin.Logout(t.Context(), tokenB); err != nil {
		t.Fatal(err)
	}
	if r.NewBinding.active || s.View().Status != "DISCONNECTED" || s.View().Generation != 2 {
		t.Fatal("新登录撤销遗漏")
	}
	if remote.Shells.Load() != 1 || len(m.List("")) != 1 {
		t.Fatal("登出关闭了 SSH")
	}
}
