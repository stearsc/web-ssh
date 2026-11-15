package testutil

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TerminalSSH 是真实 TCP/SSH 测试对端，默认 Shell 仅回显字节，不冒充系统 PTY。
type TerminalSSH struct {
	Listener net.Listener
	Key      ssh.Signer
	Shells   atomic.Int64
	Options  TerminalOptions
	Ready    chan ssh.Channel
	Requests chan string
	stop     chan struct{}
	mu       sync.Mutex
	conns    map[net.Conn]bool
	wg       sync.WaitGroup
}
type TerminalOptions struct {
	RejectPTY, RejectShell bool
	BlockRequest           string
	Gate                   <-chan struct{}
	Banner                 []byte
	Shell                  func(ssh.Channel)
	KeepaliveGate          <-chan struct{}
}

func StartTerminalSSH(t *testing.T, options TerminalOptions) *TerminalSSH {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &TerminalSSH{Listener: l, Key: Key(t), Options: options, Ready: make(chan ssh.Channel, 32), Requests: make(chan string, 128), stop: make(chan struct{}), conns: make(map[net.Conn]bool)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			s.mu.Lock()
			s.conns[c] = true
			s.mu.Unlock()
			s.wg.Add(1)
			go s.serve(c)
		}
	}()
	t.Cleanup(func() {
		close(s.stop)
		l.Close()
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}
func (s *TerminalSSH) Port() int { return s.Listener.Addr().(*net.TCPAddr).Port }

// RotateHostKey changes the key presented by subsequent real SSH handshakes.
func (s *TerminalSSH) RotateHostKey(key ssh.Signer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Key = key
}
func (s *TerminalSSH) serve(c net.Conn) {
	defer s.wg.Done()
	defer c.Close()
	defer func() { s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
	cfg := &ssh.ServerConfig{PasswordCallback: func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if m.User() != "tester" || string(p) != "ssh-secret" {
			return nil, errors.New("denied")
		}
		return nil, nil
	}}
	s.mu.Lock()
	cfg.AddHostKey(s.Key)
	s.mu.Unlock()
	remote, channels, requests, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer remote.Close()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for r := range requests {
			if s.Options.KeepaliveGate != nil {
				select {
				case <-s.Options.KeepaliveGate:
				case <-s.stop:
					return
				}
			}
			r.Reply(false, nil)
		}
	}()
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			incoming.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, reqs, err := incoming.Accept()
		if err != nil {
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer ch.Close()
			var shell sync.WaitGroup
			defer shell.Wait()
			for r := range reqs {
				select {
				case s.Requests <- r.Type:
				default:
				}
				if r.Type == s.Options.BlockRequest {
					select {
					case <-s.Options.Gate:
					case <-s.stop:
						return
					}
				}
				switch r.Type {
				case "pty-req":
					r.Reply(!s.Options.RejectPTY, nil)
				case "window-change":
					if r.WantReply {
						r.Reply(true, nil)
					}
				case "shell":
					if s.Options.RejectShell {
						r.Reply(false, nil)
						continue
					}
					s.Shells.Add(1)
					r.Reply(true, nil)
					shell.Add(1)
					go func() {
						defer shell.Done()
						if len(s.Options.Banner) > 0 {
							ch.Write(s.Options.Banner)
						}
						select {
						case s.Ready <- ch:
						case <-s.stop:
							return
						}
						if s.Options.Shell != nil {
							s.Options.Shell(ch)
						} else {
							io.Copy(ch, ch)
						}
					}()
				default:
					r.Reply(false, nil)
				}
			}
		}()
	}
	workers.Wait()
}
