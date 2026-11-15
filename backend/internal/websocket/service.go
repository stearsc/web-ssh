package websocket

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	ws "github.com/coder/websocket"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/session"
)

type connKey struct{}

func ConnContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

type Service struct {
	mu       sync.Mutex
	conns    map[*Connection]bool
	stopping bool
	cfg      config.Config
	log      *slog.Logger
	changed  chan struct{}
}

func New(cfg config.Config, logger *slog.Logger) *Service {
	return &Service{conns: make(map[*Connection]bool), cfg: cfg, log: logger, changed: make(chan struct{}, 1)}
}
func (s *Service) Count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.conns) }

// Upgrade 预占与停止接入共用锁；应用 Shutdown 必须包含尚在升级的对象。
func (s *Service) Upgrade(w http.ResponseWriter, r *http.Request, login *auth.Session, target *session.Session) error {
	raw, ok := r.Context().Value(connKey{}).(net.Conn)
	if !ok {
		return fault.Error("INTERNAL_ERROR")
	}
	release, e := login.Acquire(time.Now())
	if e != nil {
		return e
	}
	defer func() { release() }()
	ctx, cancel := context.WithCancel(context.Background())
	readCtx, readCancel := context.WithCancel(context.Background())
	c := &Connection{service: s, raw: raw, login: login, target: target, ctx: ctx, cancel: cancel, readCtx: readCtx, readCancel: readCancel, wake: make(chan struct{}, 1), terminated: make(chan struct{}), workersDone: make(chan struct{}), supervisorDone: make(chan struct{}), done: make(chan struct{}), operations: make(chan operation, 1), controls: make(chan statusMessage, int(s.cfg.Websocket.ControlQueueLimit)), bindReceived: make(chan struct{}), deadlineChanged: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		cancel()
		readCancel()
		return fault.Error("SERVER_SHUTTING_DOWN")
	}
	if int64(len(s.conns)) >= s.cfg.Websocket.MaxConnections {
		s.mu.Unlock()
		cancel()
		readCancel()
		return fault.Error("CONNECTION_LIMIT_REACHED")
	}
	s.conns[c] = true
	s.mu.Unlock()
	release()
	release = func() {}
	conn, err := ws.Accept(w, r, &ws.AcceptOptions{InsecureSkipVerify: true, CompressionMode: ws.CompressionDisabled}) // Origin 由 HTTP 接入层按配置精确校验。
	if err != nil {
		c.cancel()
		c.readCancel()
		s.remove(c)
		close(c.done)
		return nil
	} // Accept 已写 HTTP 失败响应。
	c.mu.Lock()
	c.conn = conn
	c.accepted = true
	c.upgraded = time.Now()
	stopping := c.reason != ""
	c.mu.Unlock()
	conn.SetReadLimit(max(s.cfg.Websocket.MaxControlMessageSize, s.cfg.Websocket.MaxInputMessageSize))
	if err := raw.SetDeadline(time.Time{}); err != nil {
		c.Terminate("NETWORK_ERROR")
	}
	if stopping {
		raw.Close()
	}
	go c.run()
	return nil
}
func (s *Service) remove(c *Connection) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}
func (s *Service) Stop(deadline time.Time) {
	s.mu.Lock()
	s.stopping = true
	all := make([]*Connection, 0, len(s.conns))
	for c := range s.conns {
		all = append(all, c)
	}
	s.mu.Unlock()
	for _, c := range all {
		c.terminate("SERVER_SHUTTING_DOWN", deadline)
	}
}
func (s *Service) Wait(ctx context.Context) error {
	for {
		if s.Count() == 0 {
			return nil
		}
		select {
		case <-s.changed:
		case <-ctx.Done():
			s.mu.Lock()
			for c := range s.conns {
				c.raw.Close()
			}
			s.mu.Unlock()
			return ctx.Err()
		}
	}
}
func (s *Service) Revoke(id string) {
	s.mu.Lock()
	all := make([]*Connection, 0, len(s.conns))
	for c := range s.conns {
		if c.login.ID == id {
			all = append(all, c)
		}
	}
	s.mu.Unlock()
	for _, c := range all {
		c.Terminate("AUTH_REQUIRED")
	}
}

var errStopped = errors.New("connection stopped")
