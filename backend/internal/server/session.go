package server

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/privatekey"
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
	mu         sync.Mutex
	started    bool
	canceled   bool
	stopCancel func() bool
	fileLease  *privatekey.Lease
	service    *Service
	state      *State
	record     Record
	version    uint64
	lease      *Lease
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
	var fileLease *privatekey.Lease
	if st.record.AuthType == "private_key" {
		fileLease, err = s.keys.Acquire(ctx, st.record.PrivateKeyObjectID)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	st.occupied++
	r := st.record
	target := Target{r.ID, r.Name, r.Host, r.Username, r.Port}
	if err := initialize(target); err != nil {
		fileLease.Release()
		st.occupied--
		return nil, nil, nil, err
	}
	lease := &Lease{state: st}
	creation := &Creation{service: s, state: st, record: r, version: st.version, lease: lease, fileLease: fileLease}
	creation.stopCancel = context.AfterFunc(ctx, creation.Cancel)
	return creation, lease, nil, nil
}
func (c *Creation) Open(ctx context.Context, login *auth.Session, timeout time.Duration, cols, rows int, output io.Writer) (*sshclient.Terminal, *Confirmation, error) {
	c.mu.Lock()
	if c.started || c.canceled {
		c.mu.Unlock()
		return nil, nil, fault.Error("SERVER_SHUTTING_DOWN")
	}
	c.started = true
	r := c.record
	c.record = Record{}
	c.mu.Unlock()
	c.stopCancel()
	defer c.fileLease.Release()
	term, observed, err := sshclient.Open(ctx, c.service.authSnapshot(r), c.service.cipher, timeout, cols, rows, output)
	if observed != nil {
		// 握手拒绝后底层资源已退出，先归还名额再按旧版本发布探测结果。
		c.lease.Release()
		_, confirmation, err := c.service.publish(ctx, login, c.state, r, c.version, *observed)
		return nil, confirmation, err
	}
	return term, nil, err
}

// Cancel 只释放尚未开始的短文件任务；已运行 Open 必须等实际读取/解析完成再释放。
func (c *Creation) Cancel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.canceled {
		return
	}
	c.canceled = true
	c.record = Record{}
	c.fileLease.Release()
}
