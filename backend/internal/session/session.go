package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/sshclient"
)

type View struct {
	ID               string  `json:"id"`
	ServerID         string  `json:"server_id"`
	Name             string  `json:"name"`
	Host             string  `json:"host"`
	Username         string  `json:"username"`
	Port             int     `json:"port"`
	Status           string  `json:"status"`
	CreatedAt        string  `json:"created_at"`
	DisconnectedAt   *string `json:"disconnected_at"`
	LastAttachedAt   *string `json:"last_attached_at"`
	OfflineExpiresAt *string `json:"offline_expires_at"`
	RemainingSeconds *int64  `json:"remaining_seconds"`
	Generation       uint64  `json:"binding_generation"`
	Cols             int     `json:"current_cols"`
	Rows             int     `json:"current_rows"`
}
type Manager struct {
	shutdownDeadline atomic.Int64
	fileFailures     map[string]time.Time
	// 测试同步点只用于确定快照完成后的凭证失效时序，生产为 nil。
	beforeCredentialCommit func()
	mu                     sync.Mutex
	sessions               map[string]*Session
	stopping               bool
	pending                sync.WaitGroup
	cfg                    config.Config
	servers                *server.Service
	db                     *sql.DB
	log                    *slog.Logger
	ctx                    context.Context
	cancel                 context.CancelFunc
	done                   chan struct{}
	scanDone               chan struct{}
	credentialsMu          sync.Mutex
	credentials            map[[32]byte]credential
	now                    func() time.Time
}
type Session struct {
	fileOperation *fileOperation
	// 测试同步点位于准入之后、远端调用之前，生产不安装。
	operationAdmitted                                func()
	metadataCtx                                      context.Context
	metadataCancel                                   context.CancelFunc
	metadataTimer                                    *time.Timer
	metadataTimerDone                                chan struct{}
	metadataStopped                                  bool
	manager                                          *Manager
	id                                               string
	target                                           server.Target
	mu                                               sync.Mutex
	status, reason                                   string
	created, disconnected, attached, expires, closed time.Time
	cols, rows                                       int
	generation                                       uint64
	binding                                          *Binding
	gate                                             chan struct{}
	ctx                                              context.Context
	cancel                                           context.CancelFunc
	terminal                                         *sshclient.Terminal
	lease                                            *server.Lease
	buffer                                           *Buffer
	wg                                               sync.WaitGroup
	done                                             chan struct{}
	dirty                                            chan struct{}
	final                                            chan struct{}
	metadataDone                                     chan struct{}
	closeDeadline                                    time.Time
}

func New(parent context.Context, db *sql.DB, servers *server.Service, cfg config.Config, logger *slog.Logger) *Manager {
	ctx, cancel := context.WithCancel(parent)
	m := &Manager{sessions: make(map[string]*Session), cfg: cfg, servers: servers, db: db, log: logger, ctx: ctx, cancel: cancel, done: make(chan struct{}), scanDone: make(chan struct{}), credentials: make(map[[32]byte]credential), now: time.Now}
	go m.scan()
	go func() { <-ctx.Done(); m.Stop() }()
	return m
}
func (m *Manager) Create(login *auth.Session, serverID string, cols, rows int) (View, *server.Confirmation, error) {
	if !m.validSize(cols, rows) {
		return View{}, nil, fault.Error("INVALID_SIZE")
	}
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return View{}, nil, fault.Error("SERVER_SHUTTING_DOWN")
	}
	m.pending.Add(1)
	m.mu.Unlock()
	defer m.pending.Done()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return View{}, nil, fault.Error("INTERNAL_ERROR")
	}
	ctx, cancel := context.WithCancel(m.ctx)
	s := &Session{manager: m, id: hex.EncodeToString(raw), status: "CONNECTING", created: m.now(), cols: cols, rows: rows, gate: make(chan struct{}, 1), ctx: ctx, cancel: cancel, buffer: NewBuffer(int(m.cfg.Session.OutputBufferSize), int(m.cfg.Session.HistoryLimit)), done: make(chan struct{}), dirty: make(chan struct{}, 1), final: make(chan struct{}), metadataDone: make(chan struct{})}
	s.metadataCtx, s.metadataCancel = context.WithCancel(context.Background())
	s.metadataTimerDone = make(chan struct{})
	s.wg.Add(1) // 在发布前登记创建任务，Close 可以等待尚未登记完的资源。
	creation, lease, confirmation, err := m.servers.Reserve(ctx, login, serverID, m.cfg.Session.MaxSessionsPerServer, func(target server.Target) error {
		s.target = target
		storage, stop := context.WithTimeout(ctx, time.Duration(m.cfg.Server.RequestTimeout)*time.Second)
		defer stop()
		_, e := m.db.ExecContext(storage, `INSERT INTO sessions(id,server_id,name,host,username,port,status,created_at) VALUES(?,?,?,?,?,?,?,?)`, s.id, target.ID, target.Name, target.Host, target.Username, target.Port, s.status, s.created.UnixMilli())
		if e != nil {
			return fault.Error("STORAGE_ERROR")
		}
		m.mu.Lock()
		if m.stopping || ctx.Err() != nil {
			m.mu.Unlock()
			cleanup, finish := context.WithTimeout(context.Background(), time.Duration(m.cfg.Server.ShutdownTimeout)*time.Second)
			_, updateErr := m.db.ExecContext(cleanup, `UPDATE sessions SET status='CLOSED',closed_at=?,close_reason='create_failed' WHERE id=?`, m.now().UnixMilli(), s.id)
			finish()
			if updateErr != nil {
				m.log.Error("未发布 Session 元数据清理失败", "session_id", s.id)
			}
			return fault.Error("SERVER_SHUTTING_DOWN")
		}
		m.sessions[s.id] = s
		m.mu.Unlock()
		return nil
	})
	if creation == nil {
		s.metadataCancel()
		cancel()
		s.wg.Done()
		return View{}, confirmation, err
	}
	s.mu.Lock()
	s.lease = lease
	s.mu.Unlock()
	go s.metadata()
	term, confirmation, err := creation.Open(ctx, login, time.Duration(m.cfg.Ssh.ConnectTimeout)*time.Second, cols, rows, s)
	s.mu.Lock()
	if term != nil {
		s.terminal = term
	}
	var readyView View
	ready := err == nil && confirmation == nil && s.status == "CONNECTING" && ctx.Err() == nil
	if ready {
		s.status = "DISCONNECTED"
		s.disconnected = m.now()
		s.expires = s.disconnected.Add(time.Duration(m.cfg.Session.IdleTimeout) * time.Minute)
		s.markDirty()
		s.wg.Add(2)
		readyView = s.viewLocked()
	}
	s.mu.Unlock()
	if ready {
		go s.monitor()
		go s.keepalive()
	}
	s.wg.Done()
	if !ready {
		s.Close("create_failed")
		<-s.done
		if err == nil && confirmation == nil {
			err = fault.Error("SERVER_SHUTTING_DOWN")
		}
		return View{}, confirmation, err
	}
	m.log.Info("SSH Session 已创建", "session_id", s.id, "server_id", s.target.ID)
	return readyView, nil, nil
}
func (m *Manager) validSize(c, r int) bool {
	return c > 0 && r > 0 && int64(c) <= m.cfg.Terminal.MaxCols && int64(r) <= m.cfg.Terminal.MaxRows
}
func (m *Manager) Find(id string) (*Session, error) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return nil, fault.Error("SESSION_NOT_FOUND")
	}
	s.mu.Lock()
	closed := s.status == "CLOSED"
	s.mu.Unlock()
	if closed {
		return nil, fault.Error("SESSION_NOT_FOUND")
	}
	return s, nil
}
func (m *Manager) Get(id string) (View, error) {
	s, e := m.Find(id)
	if e != nil {
		return View{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == "CLOSED" {
		return View{}, fault.Error("SESSION_NOT_FOUND")
	}
	return s.viewLocked(), nil
}

// List returns per-Session snapshots, not a globally atomic snapshot of the list.
func (m *Manager) List(serverID string) []View {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	// Never hold the index lock while acquiring a Session lock. References remain
	// valid after removal; CLOSED is checked when each row is copied.
	type row struct {
		view          View
		createdMillis int64
	}
	rows := make([]row, 0, len(sessions))
	for _, s := range sessions {
		s.mu.Lock()
		if s.status != "CLOSED" && (serverID == "" || s.target.ID == serverID) {
			rows = append(rows, row{view: s.viewLocked(), createdMillis: s.created.UnixMilli()})
		}
		s.mu.Unlock()
	}
	// Match the API's millisecond precision before breaking ties by ID.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].createdMillis != rows[j].createdMillis {
			return rows[i].createdMillis < rows[j].createdMillis
		}
		return rows[i].view.ID < rows[j].view.ID
	})
	items := make([]View, 0, len(rows))
	for _, r := range rows {
		items = append(items, r.view)
	}
	return items
}

func (m *Manager) Close(id, reason string) {
	s, e := m.Find(id)
	if e == nil {
		s.Close(reason)
	}
}
func (m *Manager) Done() <-chan struct{} { return m.done }
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return
	}
	m.stopping = true
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	m.cancel()
	for _, s := range all {
		s.Close("server_shutdown")
	}
	go func() {
		m.pending.Wait()
		for _, s := range all {
			<-s.done
		}
		<-m.scanDone
		m.credentialsMu.Lock()
		clear(m.credentials)
		m.credentialsMu.Unlock()
		close(m.done)
	}()
}
func (m *Manager) scan() {
	defer close(m.scanDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.Sweep()
		}
	}
}
func (m *Manager) Sweep() {
	now := m.now()
	m.mu.Lock()
	for id, until := range m.fileFailures {
		if !now.Before(until) {
			delete(m.fileFailures, id)
		}
	}
	m.mu.Unlock()
	m.credentialsMu.Lock()
	for hash, c := range m.credentials {
		if !now.Before(c.expires) {
			delete(m.credentials, hash)
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
		if s.status == "DISCONNECTED" && s.binding == nil && !now.Before(s.expires) {
			s.closeLocked("idle_timeout")
		}
		b := s.binding
		s.mu.Unlock()
		if b != nil && !now.Before(b.login.Expires) {
			s.Detach(b)
			b.end("AUTH_REQUIRED")
		}
	}
}
func (s *Session) View() View { s.mu.Lock(); defer s.mu.Unlock(); return s.viewLocked() }
func (s *Session) viewLocked() View {
	v := View{ID: s.id, ServerID: s.target.ID, Name: s.target.Name, Host: s.target.Host, Port: s.target.Port, Username: s.target.Username, Status: s.status, CreatedAt: server.Timestamp(s.created), Generation: s.generation, Cols: s.cols, Rows: s.rows}
	v.DisconnectedAt = timestamp(s.disconnected)
	v.LastAttachedAt = timestamp(s.attached)
	if s.status == "DISCONNECTED" {
		v.OfflineExpiresAt = timestamp(s.expires)
		n := max(int64(0), int64((s.expires.Sub(s.manager.now())+time.Second-1)/time.Second))
		v.RemainingSeconds = &n
	}
	return v
}
func timestamp(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := server.Timestamp(t)
	return &v
}
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Buffer() *Buffer       { return s.buffer }
func (s *Session) Write(p []byte) (int, error) {
	n, e := s.buffer.Write(p)
	if errors.Is(e, ErrCounter) {
		s.Close("counter_exhausted")
	}
	return n, e
}
func (s *Session) markDirty() {
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}
func (s *Session) Close(reason string) { s.mu.Lock(); s.closeLocked(reason); s.mu.Unlock() }
func (s *Session) closeLocked(reason string) {
	if s.status == "CLOSING" || s.status == "CLOSED" {
		return
	}
	s.status = "CLOSING"
	s.reason = reason
	s.expires = time.Time{}
	s.closeDeadline = s.manager.now().Add(time.Duration(s.manager.cfg.Server.ShutdownTimeout) * time.Second)
	if stamp := s.manager.shutdownDeadline.Load(); stamp != 0 && time.Unix(0, stamp).Before(s.closeDeadline) {
		s.closeDeadline = time.Unix(0, stamp)
	}
	s.metadataTimer = time.AfterFunc(time.Until(s.closeDeadline), func() { s.metadataCancel(); close(s.metadataTimerDone) })
	old := s.binding
	if old != nil {
		old.active = false
		old.cancel()
		s.binding = nil
	}
	s.manager.invalidate(s.id)
	s.markDirty()
	s.cancel()
	if s.fileOperation != nil {
		s.fileOperation.cancel()
	}
	// 回收任务不在工作组内，触发 Close 的读取/监测协程不会等待自身。
	go s.reap(old)
}
func (s *Session) reap(old *Binding) {
	s.manager.log.Info("SSH Session 开始关闭", "session_id", s.id, "reason", s.reason)
	// 创建任务的取消回调先解除网络阻塞；已发布的终端可立即关闭。
	s.mu.Lock()
	term := s.terminal
	op := s.fileOperation
	s.mu.Unlock()
	if term != nil {
		term.Close()
	}
	s.wg.Wait()
	if op != nil {
		<-op.done
	}
	s.mu.Lock()
	term = s.terminal
	s.mu.Unlock()
	if term != nil {
		term.Close()
		_ = term.WaitTransport()
	}
	if old != nil {
		old.end("closed:" + s.reason)
	}
	s.mu.Lock()
	s.status = "CLOSED"
	s.closed = s.manager.now()
	s.mu.Unlock()
	close(s.final)
	<-s.metadataDone
	s.metadataCancel()
	s.mu.Lock()
	s.metadataStopped = true
	timerStopped := s.metadataTimer.Stop()
	s.mu.Unlock()
	if !timerStopped {
		<-s.metadataTimerDone
	}
	s.buffer.clear()
	s.lease.Release()
	s.manager.mu.Lock()
	delete(s.manager.sessions, s.id)
	s.manager.mu.Unlock()
	close(s.done)
}
func (s *Session) monitor() {
	defer s.wg.Done()
	err := s.terminal.Wait()
	reason := "ssh_exit"
	if err != nil {
		reason = "ssh_error"
	}
	s.Close(reason)
}
func (s *Session) keepalive() {
	defer s.wg.Done()
	cfg := s.manager.cfg.Ssh
	timer := time.NewTimer(time.Duration(cfg.KeepaliveInterval) * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
		result := make(chan error, 1)
		go func() { result <- s.terminal.Keepalive() }()
		failures := int64(0)
		timeout := time.NewTimer(time.Duration(cfg.KeepaliveTimeout) * time.Second)
		waiting := true
		for waiting {
			select {
			case <-s.ctx.Done():
				s.terminal.Close()
				<-result
				timeout.Stop()
				return
			case e := <-result:
				timeout.Stop()
				if e != nil {
					s.Close("ssh_error")
					return
				}
				waiting = false
			case <-timeout.C:
				failures++
				if failures >= cfg.KeepaliveMaxFailures {
					s.Close("keepalive_timeout")
					s.terminal.Close()
					<-result
					return
				}
				timeout.Reset(time.Duration(cfg.KeepaliveTimeout) * time.Second)
			}
		}
		timer.Reset(time.Duration(cfg.KeepaliveInterval) * time.Second)
	}
}
func (s *Session) metadata() {
	defer close(s.metadataDone)
	for {
		final := false
		select {
		case <-s.dirty:
		case <-s.final:
			final = true
		}
		s.mu.Lock()
		status, reason := s.status, s.reason
		disconnected, attached, closed, deadline := s.disconnected, s.attached, s.closed, s.closeDeadline
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(s.metadataCtx, time.Duration(s.manager.cfg.Server.RequestTimeout)*time.Second)
		if !deadline.IsZero() {
			cancel()
			ctx, cancel = context.WithDeadline(s.metadataCtx, deadline)
		}
		_, err := s.manager.db.ExecContext(ctx, `UPDATE sessions SET status=?,disconnected_at=?,last_attached_at=?,closed_at=?,close_reason=? WHERE id=?`, status, millis(disconnected), millis(attached), millis(closed), nullable(reason), s.id)
		cancel()
		if err != nil {
			s.manager.log.Error("Session 元数据更新失败", "session_id", s.id)
		}
		if final {
			return
		}
	}
}
func millis(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// StopAt 将已有 Session 收尾预算收紧到应用的同一绝对期限。
func (m *Manager) StopAt(deadline time.Time) {
	stamp := deadline.UnixNano()
	for {
		old := m.shutdownDeadline.Load()
		if old != 0 && old <= stamp {
			break
		}
		if m.shutdownDeadline.CompareAndSwap(old, stamp) {
			break
		}
	}
	m.Stop()
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.mu.Lock()
		if !s.closeDeadline.IsZero() && deadline.Before(s.closeDeadline) {
			s.closeDeadline = deadline
			if !s.metadataStopped && s.metadataTimer != nil && s.metadataTimer.Stop() {
				s.metadataTimer.Reset(time.Until(deadline))
			}
		}
		s.mu.Unlock()
	}
}
