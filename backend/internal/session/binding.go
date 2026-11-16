package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/server"
)

type credential struct {
	loginID, sessionID string
	mode               string
	expected           uint64
	expires            time.Time
}
type CredentialView struct {
	Credential string `json:"credential"`
	ExpiresAt  string `json:"expires_at"`
}
type Binding struct {
	Generation uint64
	Replay     Replay
	Cols, Rows int
	session    *Session
	login      *auth.Session
	active     bool // 只在所属 Session 状态锁内访问。
	superseded bool // 同样由 Session.mu 保护；只表示被替换，不是 Connection 终止原因。
	ctx        context.Context
	cancel     context.CancelFunc
	end        func(string)
}

// CommitResult 将成功提交后的旧对象收尾责任交给调用方。必须在所有成功分支
// 调用 FinishDisplaced，且调用时不得持有 Session、操作门或任何 Connection 锁。
// 仅按指针传递；同一结果的重复收尾不会重复执行回调。
type CommitResult struct {
	NewBinding       *Binding
	DisplacedBinding *Binding
	finish           sync.Once
}

func (r *CommitResult) FinishDisplaced() {
	r.finish.Do(func() {
		if r.DisplacedBinding != nil {
			r.DisplacedBinding.end("taken_over")
		}
	})
}

func (b *Binding) Done() <-chan struct{} { return b.ctx.Done() }
func (b *Binding) Session() *Session     { return b.session }
func (b *Binding) Expires() time.Time    { return b.login.Expires }
func (m *Manager) Prepare(login *auth.Session, id, mode string, expected uint64) (CredentialView, error) {
	if (mode != "attach" && mode != "takeover") || expected > MaxCounter {
		return CredentialView{}, fault.Error("INVALID_REQUEST")
	}
	s, err := m.Find(id)
	if err != nil {
		return CredentialView{}, err
	}
	release, err := login.Acquire(m.now())
	if err != nil {
		return CredentialView{}, err
	}
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.canBind(mode, expected); err != nil {
		return CredentialView{}, err
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return CredentialView{}, fault.Error("INTERNAL_ERROR")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := m.now().Add(time.Duration(m.cfg.Session.BindingCredentialTtl) * time.Second)
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	for h, c := range m.credentials {
		if !m.now().Before(c.expires) {
			delete(m.credentials, h)
		}
	}
	if int64(len(m.credentials)) >= m.cfg.Session.BindingCredentialLimit {
		return CredentialView{}, fault.Error("CREDENTIAL_LIMIT")
	}
	m.credentials[sha256.Sum256([]byte(token))] = credential{loginID: login.ID, sessionID: id, mode: mode, expected: expected, expires: expires}
	return CredentialView{token, server.Timestamp(expires)}, nil
}
func (s *Session) canBind(mode string, expected uint64) error {
	if (mode != "attach" && mode != "takeover") || expected > MaxCounter {
		return fault.Error("INVALID_REQUEST")
	}
	if s.manager.ctx.Err() != nil {
		return fault.Error("SERVER_SHUTTING_DOWN")
	}
	if s.status != "CONNECTED" && s.status != "DISCONNECTED" {
		return fault.Error("SESSION_NOT_READY")
	}
	if s.status == "DISCONNECTED" && !s.manager.now().Before(s.expires) {
		return fault.Error("SESSION_NOT_READY")
	}
	if expected != s.generation {
		return fault.Error("BIND_CONFLICT")
	}
	if mode == "attach" && (s.binding != nil || s.status == "CONNECTED") {
		return fault.Error("BIND_BUSY")
	}
	return nil
}
func (s *Session) Attach(ctx context.Context, login *auth.Session, token string, offset uint64, end func(string)) (*CommitResult, error) {
	if offset > MaxCounter {
		return nil, fault.Error("INVALID_OFFSET")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return nil, fault.Error("INVALID_CREDENTIAL")
	}
	// 耗尽路径在全部事务锁和操作门释放后发起幂等关闭。
	exhausted := false
	defer func() {
		if exhausted {
			s.Close("counter_exhausted")
		}
	}()
	wait, cancel := context.WithTimeout(ctx, time.Duration(s.manager.cfg.Session.OperationWaitTimeout)*time.Second)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
	case <-wait.Done():
		return nil, fault.Error("BIND_BUSY")
	}
	defer func() { <-s.gate }()
	release, err := login.Acquire(s.manager.now())
	if err != nil {
		return nil, err
	}
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.manager
	hash := sha256.Sum256([]byte(token))
	m.credentialsMu.Lock()
	c, ok := m.credentials[hash]
	m.credentialsMu.Unlock()
	if !ok || !m.now().Before(c.expires) {
		return nil, fault.Error("INVALID_CREDENTIAL")
	}
	if c.loginID != login.ID || c.sessionID != s.id {
		return nil, fault.Error("PERMISSION_DENIED")
	}
	if err = s.canBind(c.mode, c.expected); err != nil {
		return nil, err
	}
	if s.generation == MaxCounter {
		exhausted = true
		return nil, fault.Error("SESSION_NOT_READY")
	}
	s.buffer.mu.Lock()
	defer s.buffer.mu.Unlock()
	// 快照仅为本次调用的局部副本；失败返回不发布或保留它，由 GC 回收。
	replay, err := s.buffer.snapshot(offset)
	if err != nil {
		return nil, fault.Error("INVALID_OFFSET")
	}
	if m.beforeCredentialCommit != nil {
		m.beforeCredentialCommit()
	}
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	current, exists := m.credentials[hash]
	if !exists || current != c || !m.now().Before(c.expires) {
		return nil, fault.Error("INVALID_CREDENTIAL")
	}
	if !m.now().Before(login.Expires) {
		return nil, fault.Error("AUTH_REQUIRED")
	}
	if err = s.canBind(c.mode, c.expected); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, fault.Error("BIND_BUSY")
	}
	// 唯一提交点：持有门 → 登录读锁 → Session → Buffer → 凭证锁。
	// attach 不替换在线 Binding；takeover 仅逻辑撤权，不能提前取消旧 context，
	// 否则 Reader 的 NETWORK_ERROR 可能抢先于锁外 end 确定终止原因。
	for h, item := range m.credentials {
		if item.sessionID == s.id {
			delete(m.credentials, h)
		}
	}
	old := s.binding
	if old != nil {
		old.active = false
		old.superseded = true
	}
	bctx, bcancel := context.WithCancel(s.ctx)
	s.generation++
	b := &Binding{Generation: s.generation, Replay: replay, Cols: s.cols, Rows: s.rows, session: s, login: login, active: true, ctx: bctx, cancel: bcancel, end: end}
	s.binding = b
	s.status = "CONNECTED"
	s.attached = m.now()
	s.expires = time.Time{}
	s.markDirty()
	return &CommitResult{NewBinding: b, DisplacedBinding: old}, nil
}
func (m *Manager) invalidate(id string) {
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	for h, c := range m.credentials {
		if c.sessionID == id {
			delete(m.credentials, h)
		}
	}
}
func (m *Manager) Revoke(login string) {
	m.credentialsMu.Lock()
	for h, c := range m.credentials {
		if c.loginID == login {
			delete(m.credentials, h)
		}
	}
	m.credentialsMu.Unlock()
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.mu.Lock()
		b := s.binding
		if b != nil && b.login.ID != login {
			b = nil
		}
		s.mu.Unlock()
		if b != nil {
			s.Detach(b)
			b.end("AUTH_REQUIRED")
		}
	}
}
func (s *Session) Detach(b *Binding) {
	if b == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b.active = false
	b.cancel()
	if s.binding == b && s.status == "CONNECTED" {
		s.binding = nil
		s.status = "DISCONNECTED"
		s.disconnected = s.manager.now()
		s.expires = s.disconnected.Add(time.Duration(s.manager.cfg.Session.IdleTimeout) * time.Minute)
		s.markDirty()
	}
}

// Execute 的准入与实际调用之间没有第二个队列；WS 执行器负责保持消息顺序。
func (s *Session) Execute(b *Binding, data []byte, cols, rows int) error {
	wait, cancel := context.WithTimeout(b.ctx, time.Duration(s.manager.cfg.Session.OperationWaitTimeout)*time.Second)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
	case <-wait.Done():
		return fault.Error("INPUT_BACKPRESSURE")
	}
	defer func() { <-s.gate }()
	release, err := b.login.Acquire(s.manager.now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.status != "CONNECTED" || s.binding != b || !b.active || s.generation != b.Generation {
		s.mu.Unlock()
		release()
		return fault.Error("BIND_CONFLICT")
	}
	if data == nil && !s.manager.validSize(cols, rows) {
		s.mu.Unlock()
		release()
		return fault.Error("INVALID_SIZE")
	}
	term := s.terminal
	s.wg.Add(1) // 准入与关闭互斥；Session 回收必须等待已准入的远端调用。
	s.mu.Unlock()
	release()
	defer s.wg.Done()
	if s.operationAdmitted != nil {
		s.operationAdmitted()
	}
	result := make(chan error, 1)
	go func() {
		var e error
		if data != nil {
			_, e = term.Write(data)
		} else {
			e = term.Resize(cols, rows)
		}
		result <- e
	}()
	timer := time.NewTimer(time.Duration(s.manager.cfg.Ssh.OperationTimeout) * time.Second)
	defer timer.Stop()
	select {
	case err = <-result:
	case <-s.ctx.Done():
		term.Close()
		err = <-result
	case <-timer.C:
		s.Close("ssh_error")
		term.Close()
		<-result
		return fault.Error("INTERNAL_ERROR")
	}
	if err != nil {
		s.Close("ssh_error")
		return fault.Error("INTERNAL_ERROR")
	}
	if data == nil {
		s.mu.Lock()
		if s.status != "CLOSING" && s.status != "CLOSED" {
			s.cols = cols
			s.rows = rows
		}
		s.mu.Unlock()
	}
	return nil
}
