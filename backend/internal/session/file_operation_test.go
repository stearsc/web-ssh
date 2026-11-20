package session

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

func fileServer(t *testing.T, root string) func(ssh.NewChannel, <-chan struct{}) {
	t.Helper()
	return func(incoming ssh.NewChannel, _ <-chan struct{}) {
		ch, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		for r := range requests {
			if r.Type != "subsystem" {
				_ = r.Reply(false, nil)
				continue
			}
			_ = r.Reply(true, nil)
			srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(root))
			if err != nil {
				t.Error(err)
				return
			}
			_ = srv.Serve()
			_ = srv.Close()
			return
		}
	}
}

func awaitFileResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("文件操作未完成回收")
		return nil
	}
}

func TestFileOperationNormalCancelKeepsTerminal(t *testing.T) {
	m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: fileServer(t, t.TempDir())}, nil)
	s := createSession(t, m, login, id)
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.withFileClient(ctx, func(ctx context.Context, c *sftp.Client, progress func()) error {
			if _, err := c.Getwd(); err != nil {
				return err
			}
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-entered
	if _, err := s.beginFileOperation(t.Context()); err != fault.Error("SFTP_BUSY") {
		t.Fatal(err)
	}
	cancel()
	if err := awaitFileResult(t, result); err != fault.Error("SFTP_CANCELED") {
		t.Fatal(err)
	}
	if s.View().Status != "DISCONNECTED" {
		t.Fatal(s.View())
	}
	if _, err := s.terminal.Write([]byte("shell-after-cancel")); err != nil {
		t.Fatal(err)
	}
	waitBytes(t, s, []byte("shell-after-cancel"))
	if err := s.WaitFileOperation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.withFileClient(t.Context(), func(context.Context, *sftp.Client, func()) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestFileOperationRealChannelOpen(t *testing.T) {
	for _, mode := range []string{"reject", "unanswered", "cancel-unanswered"} {
		t.Run(mode, func(t *testing.T) {
			observed, peerExited := make(chan struct{}), make(chan struct{})
			m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: func(ch ssh.NewChannel, done <-chan struct{}) {
				defer close(peerExited)
				close(observed)
				if mode == "reject" {
					_ = ch.Reject(ssh.Prohibited, "test")
					return
				}
				<-done
			}}, func(c *config.Config) { c.Ssh.ConnectTimeout = 1; c.Ssh.OperationTimeout = 1 })
			s := createSession(t, m, login, id)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- s.withFileClient(ctx, func(context.Context, *sftp.Client, func()) error {
					t.Error("未接受的 channel 进入工作阶段")
					return nil
				})
			}()
			<-observed
			if mode == "cancel-unanswered" {
				cancel()
				// 明确的既有 Shell 回显证明取消没有立即关闭共享连接。
				if _, err := s.terminal.Write([]byte("alive-before-fallback")); err != nil {
					t.Fatal(err)
				}
				waitBytes(t, s, []byte("alive-before-fallback"))
				select {
				case <-result:
					t.Fatal("阻塞调用尚未退出便返回")
				default:
				}
			}
			err := awaitFileResult(t, result)
			if mode == "reject" {
				if err != fault.Error("SFTP_UNAVAILABLE") || s.View().Status != "DISCONNECTED" {
					t.Fatal(err, s.View())
				}
			} else {
				if err != fault.Error("SFTP_CANCEL_FAILED") {
					t.Fatal(err)
				}
				select {
				case <-s.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("Session 未回收")
				}
			}
			<-peerExited
		})
	}
}

func TestFileOperationCloseAdmissionRace(t *testing.T) {
	m, login, id, _ := setup(t)
	for range 20 {
		s := createSession(t, m, login, id)
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		go func() { defer group.Done(); <-start; s.Close("test"); s.Close("test") }()
		go func() {
			defer group.Done()
			<-start
			op, err := s.beginFileOperation(t.Context())
			if err != nil {
				if err != fault.Error("SFTP_SESSION_CLOSED") {
					t.Error(err)
				}
				return
			}
			<-op.ctx.Done()
			s.mu.Lock()
			s.fileOperation = nil
			close(op.done)
			s.mu.Unlock()
		}()
		close(start)
		group.Wait()
		select {
		case <-s.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("关闭竞争未收齐 operation")
		}
		if _, err := s.beginFileOperation(t.Context()); err != fault.Error("SFTP_SESSION_CLOSED") {
			t.Fatal(err)
		}
	}
}

func TestFileOperationCancelBlockedInitializationKeepsTerminal(t *testing.T) {
	initialized, release := make(chan struct{}), make(chan struct{})
	m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: func(incoming ssh.NewChannel, done <-chan struct{}) {
		ch, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		r := <-requests
		if r == nil {
			return
		}
		_ = r.Reply(true, nil)
		// 完整接收真实 SFTP INIT，但故意不回复 VERSION。
		packet := make([]byte, 9)
		if _, err := io.ReadFull(ch, packet); err != nil {
			t.Error(err)
			return
		}
		close(initialized)
		select {
		case <-release:
		case <-done:
		}
	}}, func(c *config.Config) { c.Ssh.ConnectTimeout = 5; c.Ssh.OperationTimeout = 1 })
	s := createSession(t, m, login, id)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- s.withFileClient(ctx, func(context.Context, *sftp.Client, func()) error {
			t.Error("初始化未完成却进入工作阶段")
			return nil
		})
	}()
	<-initialized
	cancel()
	if err := awaitFileResult(t, result); err != fault.Error("SFTP_CANCELED") {
		t.Fatal(err)
	}
	close(release)
	if s.View().Status != "DISCONNECTED" {
		t.Fatal(s.View())
	}
	if _, err := s.terminal.Write([]byte("shell-after-init-cancel")); err != nil {
		t.Fatal(err)
	}
	waitBytes(t, s, []byte("shell-after-init-cancel"))
}

func TestFileOperationSessionCloseWaitsForWorker(t *testing.T) {
	m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: fileServer(t, t.TempDir())}, nil)
	s := createSession(t, m, login, id)
	entered, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.withFileClient(t.Context(), func(ctx context.Context, c *sftp.Client, progress func()) error {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
			return ctx.Err()
		})
	}()
	<-entered
	s.Close("user_closed")
	<-canceled
	select {
	case <-s.Done():
		t.Fatal("工作任务未退出就完成 Session 回收")
	default:
	}
	if _, err := s.beginFileOperation(t.Context()); err != fault.Error("SFTP_SESSION_CLOSED") {
		t.Fatal(err)
	}
	close(release)
	if err := awaitFileResult(t, result); err != fault.Error("SFTP_CANCELED") {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("工作任务退出后 Session 未回收")
	}
}
