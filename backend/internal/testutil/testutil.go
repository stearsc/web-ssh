// Package testutil 提供测试专用 SQLite 与可控 SSH 服务，不用于生产启动。
package testutil

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"golang.org/x/crypto/ssh"
)

func DB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := config.Defaults().Database
	cfg.Path = filepath.Join(t.TempDir(), "test.db")
	db, err := database.Open(context.Background(), cfg, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}
func Key(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type SSH struct {
	Listener                    net.Listener
	AuthCalls, Channels, Active atomic.Int64
	mu                          sync.Mutex
	signer                      ssh.Signer
	conns                       map[net.Conn]bool
	wg                          sync.WaitGroup
	// 设置后，认证回调等待此通道，测试可确定地制造认证阻塞。
	AuthGate      <-chan struct{}
	HandshakeGate <-chan struct{}
	Accepted      chan struct{}
}

func StartSSH(t *testing.T) *SSH {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &SSH{Listener: listener, signer: Key(t), conns: make(map[net.Conn]bool), Accepted: make(chan struct{}, 32)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[conn] = true
			key := s.signer
			s.mu.Unlock()
			s.Active.Add(1)
			s.wg.Add(1)
			go s.handle(conn, key)
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		s.mu.Lock()
		for conn := range s.conns {
			conn.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}
func (s *SSH) handle(conn net.Conn, key ssh.Signer) {
	defer s.wg.Done()
	defer s.Active.Add(-1)
	defer conn.Close()
	defer func() { s.mu.Lock(); delete(s.conns, conn); s.mu.Unlock() }()
	select {
	case s.Accepted <- struct{}{}:
	default:
	}
	s.mu.Lock()
	handshakeGate, authGate := s.HandshakeGate, s.AuthGate
	s.mu.Unlock()
	if handshakeGate != nil {
		<-handshakeGate
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		s.AuthCalls.Add(1)
		if authGate != nil {
			<-authGate
		}
		if meta.User() != "tester" || string(password) != "ssh-secret" {
			return nil, errors.New("认证失败")
		}
		return nil, nil
	}}
	cfg.AddHostKey(key)
	remote, channels, requests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{})
	go func() { ssh.DiscardRequests(requests); close(done) }()
	for channel := range channels {
		s.Channels.Add(1)
		channel.Reject(ssh.Prohibited, "测试不允许 Channel")
	}
	<-done
}
func (s *SSH) Port() int           { return s.Listener.Addr().(*net.TCPAddr).Port }
func (s *SSH) Rotate(t *testing.T) { s.mu.Lock(); defer s.mu.Unlock(); s.signer = Key(t) }
func (s *SSH) PublicKey() ssh.PublicKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signer.PublicKey()
}
func (s *SSH) WaitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for s.Active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.Active.Load() != 0 {
		t.Fatal("临时 SSH 连接没有回收")
	}
}

func (s *SSH) BlockHandshake(gate <-chan struct{}) {
	s.mu.Lock()
	s.HandshakeGate = gate
	s.mu.Unlock()
}
func (s *SSH) BlockAuth(gate <-chan struct{}) { s.mu.Lock(); s.AuthGate = gate; s.mu.Unlock() }
