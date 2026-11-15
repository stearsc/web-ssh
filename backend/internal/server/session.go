package server

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/sshclient"
)

// Target 是长期 Session 唯一保存的连接资料。
type Target struct {
	ID, Name, Host, Username string
	Port                     int
}
type Lease struct {
	state *State
	once  sync.Once
}

func (l *Lease) Release() {
	l.once.Do(func() { l.state.mu.Lock(); l.state.occupied--; l.state.mu.Unlock() })
}

// Creation 只在创建任务中持有，不能存入长期 Session。
type Creation struct {
	service *Service
	state   *State
	record  Record
	version uint64
	lease   *Lease
}

// Reserve 与编辑/删除共用服务器锁。initialize 仅允许短元数据写入和内存发布，不做网络操作。
func (s *Service) Reserve(ctx context.Context, login *auth.Session, id string, limit int64, initialize func(Target) error) (*Creation, *Lease, *Confirmation, error) {
	st, err := s.state(id)
	if err != nil {
		return nil, nil, nil, err
	}
	st.mu.Lock()
	if err = s.reload(ctx, st); err != nil {
		st.mu.Unlock()
		return nil, nil, nil, err
	}
	release, err := login.Acquire(s.now())
	if err != nil {
		st.mu.Unlock()
		return nil, nil, nil, err
	}
	if len(st.record.PublicKey) == 0 {
		release()
		st.mu.Unlock()
		_, confirmation, err := s.Test(ctx, login, id, s.connectTimeout)
		if err == nil && confirmation == nil {
			err = fault.Error("SERVER_CONFIG_CHANGED")
		}
		return nil, nil, confirmation, err
	}
	defer st.mu.Unlock()
	defer release()
	if st.occupied >= limit {
		return nil, nil, nil, fault.Error("SESSION_LIMIT")
	}
	st.occupied++
	r := st.record
	target := Target{r.ID, r.Name, r.Host, r.Username, r.Port}
	if err := initialize(target); err != nil {
		st.occupied--
		return nil, nil, nil, err
	}
	lease := &Lease{state: st}
	return &Creation{s, st, r, st.version, lease}, lease, nil, nil
}
func (c *Creation) Open(ctx context.Context, login *auth.Session, timeout time.Duration, cols, rows int, output io.Writer) (*sshclient.Terminal, *Confirmation, error) {
	r := c.record
	defer func() { c.record = Record{} }()
	term, observed, err := sshclient.Open(ctx, sshclient.Snapshot{Host: r.Host, Port: r.Port, Username: r.Username, PasswordEncrypted: r.PasswordEncrypted, PublicKey: r.PublicKey}, c.service.cipher, timeout, cols, rows, output)
	if observed != nil {
		// 握手拒绝后底层资源已退出，先归还名额再按旧版本发布探测结果。
		c.lease.Release()
		_, confirmation, err := c.service.publish(ctx, login, c.state, r, c.version, *observed)
		return nil, confirmation, err
	}
	return term, nil, err
}
