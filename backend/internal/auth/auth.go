package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/fault"
)

type Session struct {
	mu      sync.RWMutex
	ID      string
	Expires time.Time
	invalid bool
}

// Acquire 为短业务事务提供登录准入；按服务器锁 → 登录读锁顺序调用。
func (s *Session) Acquire(now time.Time) (func(), error) {
	s.mu.RLock()
	if s.invalid || !now.Before(s.Expires) {
		s.mu.RUnlock()
		return nil, fault.Error("AUTH_REQUIRED")
	}
	return s.mu.RUnlock, nil
}

type Service struct {
	mu       sync.Mutex
	db       *sql.DB
	cfg      config.Auth
	hash     string
	sessions map[[32]byte]*Session
	window   time.Time
	attempts int64
	now      func() time.Time
	transact database.Transaction
	revoke   func(string)
}

func New(ctx context.Context, db *sql.DB, cfg config.Auth, initial string) (*Service, error) {
	a := &Service{db: db, cfg: cfg, sessions: make(map[[32]byte]*Session), now: time.Now, transact: database.TransactionFor(db), revoke: func(string) {}}
	err := db.QueryRowContext(ctx, "SELECT password_hash FROM admin_credentials WHERE id=1").Scan(&a.hash)
	if errors.Is(err, sql.ErrNoRows) {
		a.hash, err = HashPassword(initial)
		if err != nil {
			return nil, err
		}
		now := a.now().UnixMilli()
		if a.transact(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO admin_credentials(id,password_hash,created_at,updated_at) VALUES(1,?,?,?)", a.hash, now, now)
			return err
		}) != database.Committed {
			return nil, errors.New("初始化管理员失败")
		}
	} else if err != nil {
		return nil, errors.New("读取管理员失败")
	}
	if _, _, _, _, _, err := parseHash(a.hash); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT id,token_hash,expires_at FROM web_auth_sessions WHERE expires_at>?", a.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		s := &Session{}
		var raw []byte
		var expires int64
		if err := rows.Scan(&s.ID, &raw, &expires); err != nil {
			return nil, err
		}
		if len(raw) != 32 || int64(len(a.sessions)) >= cfg.MaxSessions {
			return nil, errors.New("登录记录数量或格式无效")
		}
		s.Expires = time.UnixMilli(expires)
		a.sessions[[32]byte(raw)] = s
	}
	return a, rows.Err()
}

// 回调在启动接入之前安装，调用时不持有登录锁或索引锁。
func (a *Service) OnRevoke(fn func(string)) { a.revoke = fn }
func tokenHash(token string) ([32]byte, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return sha256.Sum256([]byte(token)), err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == token
}
func (a *Service) Lookup(token string) (*Session, error) {
	hash, ok := tokenHash(token)
	if !ok {
		return nil, fault.Error("AUTH_REQUIRED")
	}
	a.mu.Lock()
	s := a.sessions[hash]
	a.mu.Unlock()
	if s == nil {
		return nil, fault.Error("AUTH_REQUIRED")
	}
	release, err := s.Acquire(a.now())
	if err != nil {
		return nil, err
	}
	release()
	return s, nil
}
func (a *Service) Login(ctx context.Context, password string) (string, *Session, int, error) {
	// 同一临界区串行校验密码及容量提交，限制昂贵哈希的同时在途数量。
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if a.window.IsZero() || !now.Before(a.window.Add(time.Duration(a.cfg.LoginRateWindow)*time.Second)) {
		a.window = now
		a.attempts = 0
	}
	if a.attempts >= a.cfg.LoginRateLimit {
		return "", nil, int(a.window.Add(time.Duration(a.cfg.LoginRateWindow)*time.Second).Sub(now).Seconds()) + 1, fault.Error("LOGIN_RATE_LIMIT")
	}
	a.attempts++
	if ctx.Err() != nil {
		return "", nil, 0, fault.Error("SERVER_UNAVAILABLE")
	}
	if !VerifyPassword(a.hash, password) {
		return "", nil, 0, fault.Error("AUTH_FAILED")
	}
	tokenBytes := make([]byte, 32)
	id := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", nil, 0, fault.Error("INTERNAL_ERROR")
	}
	if _, err := rand.Read(id); err != nil {
		return "", nil, 0, fault.Error("INTERNAL_ERROR")
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	now = a.now().Truncate(time.Millisecond)
	s := &Session{ID: hex.EncodeToString(id), Expires: now.Add(time.Duration(a.cfg.SessionTtl) * 24 * time.Hour)}
	full := false
	outcome := a.transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM web_auth_sessions WHERE expires_at<=?", now.UnixMilli()); err != nil {
			return err
		}
		var count int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM web_auth_sessions").Scan(&count); err != nil {
			return err
		}
		if count >= a.cfg.MaxSessions {
			full = true
			return errors.New("登录容量已满")
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO web_auth_sessions(id,token_hash,created_at,expires_at) VALUES(?,?,?,?)", s.ID, hash[:], now.UnixMilli(), s.Expires.UnixMilli())
		return err
	})
	if full {
		return "", nil, 0, fault.Error("LOGIN_SESSION_LIMIT")
	}
	if outcome != database.Committed {
		if outcome == database.Uncertain {
			return "", nil, 0, fault.Error("SERVER_UNAVAILABLE")
		}
		return "", nil, 0, fault.Error("STORAGE_ERROR")
	}
	for h, old := range a.sessions {
		if !now.Before(old.Expires) {
			delete(a.sessions, h)
		}
	}
	a.sessions[hash] = s
	return token, s, 0, nil
}
func (a *Service) Logout(ctx context.Context, token string) error {
	hash, ok := tokenHash(token)
	if !ok {
		return nil
	}
	a.mu.Lock()
	s := a.sessions[hash]
	a.mu.Unlock()
	if s != nil {
		s.mu.Lock()
	}
	outcome := a.transact(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM web_auth_sessions WHERE token_hash=?", hash[:])
		return err
	})
	if s != nil {
		if outcome != database.RolledBack {
			s.invalid = true
		}
		s.mu.Unlock()
		if outcome != database.RolledBack {
			a.revoke(s.ID)
		}
	}
	if outcome == database.RolledBack {
		return fault.Error("STORAGE_ERROR")
	}
	if outcome == database.Uncertain {
		return fault.Error("SERVER_UNAVAILABLE")
	}
	a.mu.Lock()
	delete(a.sessions, hash)
	a.mu.Unlock()
	return nil
}
func (a *Service) Cleanup(ctx context.Context) error {
	a.mu.Lock()
	expired := make([]*Session, 0)
	now := a.now()
	for h, s := range a.sessions {
		if !now.Before(s.Expires) {
			expired = append(expired, s)
			delete(a.sessions, h)
		}
	}
	a.mu.Unlock()
	for _, s := range expired {
		s.mu.Lock()
		s.invalid = true
		s.mu.Unlock()
		a.revoke(s.ID)
	}
	_, err := a.db.ExecContext(ctx, "DELETE FROM web_auth_sessions WHERE expires_at<=?", now.UnixMilli())
	return err
}
