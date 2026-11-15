package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/fault"
)

type State struct {
	mu                   sync.Mutex
	record               Record
	version              uint64
	occupied             int64
	deleted, unavailable bool
}
type Service struct {
	mu             sync.Mutex
	states         map[string]*State
	db             *sql.DB
	cipher         *secret.Cipher
	transact       database.Transaction
	now            func() time.Time
	timeout        time.Duration
	connectTimeout time.Duration
	probesMu       sync.Mutex
	probes         map[[32]byte]*probe
	probeTTL       time.Duration
	probeLimit     int64
	// 此检查在服务器锁内调用。阶段 3 的名额预占必须接入同一个同步边界。
	active func(string) bool
}

func New(ctx context.Context, db *sql.DB, cipher *secret.Cipher, cfg config.Config) (*Service, error) {
	s := &Service{states: make(map[string]*State), db: db, cipher: cipher, transact: database.TransactionFor(db), now: time.Now, timeout: time.Duration(cfg.Server.RequestTimeout) * time.Second, probes: make(map[[32]byte]*probe), connectTimeout: time.Duration(cfg.Ssh.ConnectTimeout) * time.Second, probeTTL: time.Duration(cfg.Ssh.HostKeyProbeTtl) * time.Second, probeLimit: cfg.Ssh.HostKeyProbeLimit, active: func(string) bool { return false }}
	rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM servers")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if err := s.check(r); err != nil {
			return nil, err
		}
		s.states[r.ID] = &State{record: r, version: 1}
	}
	return s, rows.Err()
}
func (s *Service) check(r Record) error {
	plain, err := s.cipher.Decrypt(r.PasswordEncrypted)
	if err != nil {
		return err
	}
	clear(plain)
	if len(r.PublicKey) > 0 {
		key, err := ssh.ParsePublicKey(r.PublicKey)
		if err != nil || key.Type() != r.Algorithm || ssh.FingerprintSHA256(key) != r.Fingerprint {
			return errors.New("已保存 Host Key 信息无效")
		}
	}
	return nil
}
func (s *Service) state(id string) (*State, error) {
	s.mu.Lock()
	st := s.states[id]
	s.mu.Unlock()
	if st == nil {
		return nil, fault.Error("SERVER_NOT_FOUND")
	}
	return st, nil
}
func (s *Service) reload(ctx context.Context, st *State) error {
	if st.deleted {
		return fault.Error("SERVER_NOT_FOUND")
	}
	if !st.unavailable {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	r, err := scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM servers WHERE id=?", st.record.ID))
	if errors.Is(err, sql.ErrNoRows) {
		st.deleted = true
		st.unavailable = false
		s.mu.Lock()
		delete(s.states, st.record.ID)
		s.mu.Unlock()
		return fault.Error("SERVER_NOT_FOUND")
	}
	if err != nil || s.check(r) != nil {
		return fault.Error("SERVER_UNAVAILABLE")
	}
	st.record = r
	st.unavailable = false
	return nil
}
func (s *Service) changed(st *State) { st.version++; s.invalidate(st.record.ID) }
func (s *Service) outcome(ctx context.Context, st *State, result database.Outcome) error {
	if result == database.Committed {
		return nil
	}
	if result == database.RolledBack {
		return fault.Error("STORAGE_ERROR")
	}
	s.changed(st)
	st.unavailable = true
	// 重载只确定以后请求的状态，本次仍明确报告不确定提交。
	_ = s.reload(ctx, st)
	return fault.Error("SERVER_UNAVAILABLE")
}
func (s *Service) Create(ctx context.Context, login *auth.Session, input Input) (View, error) {
	in, err := validate(input, true)
	if err != nil {
		return View{}, err
	}
	// 先锁定新对象，再取得登录读锁，与编辑/删除保持相同锁顺序。
	st := &State{version: 1}
	st.mu.Lock()
	defer st.mu.Unlock()
	release, err := login.Acquire(s.now())
	if err != nil {
		return View{}, err
	}
	defer release()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return View{}, fault.Error("INTERNAL_ERROR")
	}
	encrypted, err := s.cipher.Encrypt([]byte(in.Password))
	if err != nil {
		return View{}, fault.Error("INTERNAL_ERROR")
	}
	now := s.now().UnixMilli()
	r := Record{ID: hex.EncodeToString(id), Name: in.Name, Host: in.Host, Port: *in.Port, Username: in.Username, PasswordEncrypted: encrypted, GroupName: in.GroupName, Remark: in.Remark, CreatedAt: now, UpdatedAt: now}
	st.record = r
	// 提交前登记 State，提交结果确定前一直持有服务器锁。索引锁不跨数据库操作。
	// 列表即使读到了已提交的 ID，也一定能找到 State，并等待创建完成。
	s.mu.Lock()
	s.states[r.ID] = st
	s.mu.Unlock()
	result := s.transact(ctx, func(tx *sql.Tx) error { return save(ctx, tx, r, true) })
	if result == database.RolledBack {
		// 已取得指针的并发请求也必须看到删除标记，不能读取回滚的对象。
		st.deleted = true
		s.mu.Lock()
		delete(s.states, r.ID)
		s.mu.Unlock()
		return View{}, fault.Error("STORAGE_ERROR")
	}
	if result == database.Uncertain {
		st.unavailable = true
		// 仍持有服务器锁，先确定不确定创建是否真正存在，避免留下虚构服务器。
		_ = s.reload(ctx, st)
		if st.deleted {
			return View{}, fault.Error("SERVER_UNAVAILABLE")
		}
	}
	if result == database.Uncertain {
		return View{}, fault.Error("SERVER_UNAVAILABLE")
	}
	return r.view(), nil
}
func (s *Service) Get(ctx context.Context, id string) (View, error) {
	st, err := s.state(id)
	if err != nil {
		return View{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := s.reload(ctx, st); err != nil {
		return View{}, err
	}
	return st.record.view(), nil
}
func (s *Service) List(ctx context.Context, search string, group *string) ([]View, error) {
	// 对上次重读失败的对象在本次查询中做一次有限重载，不创建后台重试队列。
	s.mu.Lock()
	known := make([]*State, 0, len(s.states))
	for _, st := range s.states {
		known = append(known, st)
	}
	s.mu.Unlock()
	for _, st := range known {
		st.mu.Lock()
		err := s.reload(ctx, st)
		st.mu.Unlock()
		if err != nil && err != fault.Error("SERVER_NOT_FOUND") {
			return nil, err
		}
	}
	// 先完成参数化字面搜索并关闭游标，再取服务器锁，避免单连接池与服务器锁相互等待。
	query := "SELECT id FROM servers WHERE instr(name,?)>0"
	args := []any{search}
	if group != nil {
		query += " AND group_name=?"
		args = append(args, *group)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fault.Error("SERVER_UNAVAILABLE")
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fault.Error("SERVER_UNAVAILABLE")
		}
		ids = append(ids, id)
	}
	readErr, closeErr := rows.Err(), rows.Close()
	if readErr != nil || closeErr != nil {
		return nil, fault.Error("SERVER_UNAVAILABLE")
	}
	states := make([]*State, 0, len(ids))
	s.mu.Lock()
	for _, id := range ids {
		if st := s.states[id]; st != nil {
			states = append(states, st)
		}
	}
	s.mu.Unlock()
	result := make([]View, 0)
	for _, st := range states {
		st.mu.Lock()
		err := s.reload(ctx, st)
		r := st.record
		st.mu.Unlock()
		if err == fault.Error("SERVER_NOT_FOUND") {
			continue
		}
		if err != nil {
			return nil, err
		}
		// 查询后资料可能已变更，因此对固定的当前记录重新检查筛选项。
		if strings.Contains(r.Name, search) && (group == nil || r.GroupName == *group) {
			result = append(result, r.view())
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
func (s *Service) Update(ctx context.Context, login *auth.Session, id string, input Input) (View, error) {
	in, err := validate(input, false)
	if err != nil {
		return View{}, err
	}
	st, err := s.state(id)
	if err != nil {
		return View{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := s.reload(ctx, st); err != nil {
		return View{}, err
	}
	release, err := login.Acquire(s.now())
	if err != nil {
		return View{}, err
	}
	defer release()
	old := st.record
	r := old
	r.Name = in.Name
	r.Host = in.Host
	r.Port = *in.Port
	r.Username = in.Username
	r.GroupName = in.GroupName
	r.Remark = in.Remark
	r.UpdatedAt = s.now().UnixMilli()
	passwordChanged := false
	if in.Password != "" {
		plain, err := s.cipher.Decrypt(old.PasswordEncrypted)
		if err != nil {
			return View{}, fault.Error("SERVER_UNAVAILABLE")
		}
		passwordChanged = !bytes.Equal(plain, []byte(in.Password))
		clear(plain)
		if passwordChanged {
			r.PasswordEncrypted, err = s.cipher.Encrypt([]byte(in.Password))
			if err != nil {
				return View{}, fault.Error("INTERNAL_ERROR")
			}
		}
	}
	targetChanged := r.Host != old.Host || r.Port != old.Port
	if targetChanged {
		r.PublicKey = nil
		r.Algorithm = ""
		r.Fingerprint = ""
	}
	result := s.transact(ctx, func(tx *sql.Tx) error { return save(ctx, tx, r, false) })
	if err := s.outcome(ctx, st, result); err != nil {
		return View{}, err
	}
	st.record = r
	if targetChanged || passwordChanged || r.Username != old.Username {
		s.changed(st)
	}
	return r.view(), nil
}
func (s *Service) Delete(ctx context.Context, login *auth.Session, id string) error {
	st, err := s.state(id)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := s.reload(ctx, st); err != nil {
		return err
	}
	release, err := login.Acquire(s.now())
	if err != nil {
		return err
	}
	defer release()
	if st.occupied > 0 || s.active(id) {
		return fault.Error("SERVER_HAS_ACTIVE_SESSIONS")
	}
	result := s.transact(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM servers WHERE id=?", id)
		return err
	})
	if err := s.outcome(ctx, st, result); err != nil {
		return err
	}
	st.deleted = true
	s.changed(st)
	s.mu.Lock()
	delete(s.states, id)
	s.mu.Unlock()
	return nil
}

// SetActivityCheck 仅在开放 HTTP 前接入活动索引；回调不得取反序锁或访问 SQLite。
// 后续阶段的预占/归还需与 State.mu 共用边界，不能只在此回调外查询后再创建。
func (s *Service) SetActivityCheck(check func(string) bool) {
	if check != nil {
		s.active = check
	}
}
