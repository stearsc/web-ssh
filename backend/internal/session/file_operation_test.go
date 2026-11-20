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

// 手动计时器只在测试明确投递事件时到期；Reset 回执用于确定监控已进入目标阶段。
type controlledFileTimer struct {
	expired chan time.Time
	resets  chan time.Duration
}

func newControlledFileTimer() *controlledFileTimer {
	return &controlledFileTimer{expired: make(chan time.Time, 4), resets: make(chan time.Duration, 32)}
}
func (t *controlledFileTimer) Reset(d time.Duration) bool { t.resets <- d; return true }
func (t *controlledFileTimer) Stop() bool                 { return true }
func (timer *controlledFileTimer) awaitReset(t *testing.T, expected time.Duration) {
	t.Helper()
	for {
		select {
		case d := <-timer.resets:
			if d == expected {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("监控未进入指定计时阶段", expected)
		}
	}
}

func finishControlledOperation(s *Session, op *fileOperation) {
	op.cancel()
	s.mu.Lock()
	s.fileOperation = nil
	close(op.done)
	s.mu.Unlock()
}

func TestFileOperationConfirmedCommitResult(t *testing.T) {
	for _, event := range []string{"timer", "context", "result-and-timer", "cleanup-error", "unconfirmed-error", "uncommitted-cancel"} {
		t.Run(event, func(t *testing.T) {
			m, login, id, _ := setup(t)
			s := createSession(t, m, login, id)
			op, err := s.beginFileOperation(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer finishControlledOperation(s, op)
			releaseClose := make(chan struct{})
			close(releaseClose)
			op.channel = &blockedFileClose{entered: make(chan struct{}), release: releaseClose}
			timer := newControlledFileTimer()
			result, workerDone, opened := make(chan error, 1), make(chan struct{}), make(chan struct{})
			if event != "unconfirmed-error" && event != "uncommitted-cancel" {
				op.confirmCommit()
			}
			if event == "context" || event == "uncommitted-cancel" {
				op.cancel()
			}
			if event == "result-and-timer" {
				// 调用 select 前两个通道已经同时就绪；任意分支顺序都必须得到成功。
				timer.expired <- time.Now()
				result <- nil
				close(workerDone)
			}
			finished := make(chan error, 1)
			go func() { finished <- s.waitFileOperation(op, result, workerDone, opened, timer, timer.expired) }()
			if event == "timer" {
				timer.awaitReset(t, time.Duration(m.cfg.Ssh.OperationTimeout)*time.Second)
				timer.expired <- time.Now()
				<-op.ctx.Done()
			}
			if event != "result-and-timer" {
				if event == "cleanup-error" || event == "unconfirmed-error" {
					result <- fault.Error("SFTP_UNAVAILABLE")
				} else {
					result <- nil
				}
				// result 并不等于 workerDone；监控必须继续等待工作协程的完成屏障。
				select {
				case e := <-finished:
					t.Fatal("工作协程结束前提前返回", e)
				default:
				}
				close(workerDone)
			}
			err = awaitFileResult(t, finished)
			want := error(nil)
			if event == "unconfirmed-error" {
				want = fault.Error("SFTP_UNAVAILABLE")
			}
			if event == "uncommitted-cancel" {
				want = fault.Error("SFTP_CANCELED")
			}
			if err != want {
				t.Fatal(err, want)
			}
			if s.View().Status != "DISCONNECTED" {
				t.Fatal("正常回收关闭了共享 Shell", s.View())
			}
		})
	}
}

type blockedFileClose struct {
	ssh.Channel
	entered chan struct{}
	release chan struct{}
}

func (ch *blockedFileClose) Close() error { close(ch.entered); <-ch.release; return nil }

func TestFileOperationConfirmedCommitForcedCleanup(t *testing.T) {
	m, login, id, _ := setup(t)
	s := createSession(t, m, login, id)
	other := createSession(t, m, login, id)
	op, err := s.beginFileOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	timer := newControlledFileTimer()
	ch := &blockedFileClose{entered: make(chan struct{}), release: make(chan struct{})}
	op.channel = ch
	op.confirmCommit()
	result, workerDone, opened := make(chan error, 1), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- s.waitFileOperation(op, result, workerDone, opened, timer, timer.expired)
	}()
	budget := time.Duration(m.cfg.Ssh.OperationTimeout) * time.Second
	timer.awaitReset(t, budget)
	timer.expired <- time.Now()
	<-ch.entered
	// 首次到期只关闭独立 channel；该 channel 确认阻塞后再触发既有 Session 兜底。
	timer.awaitReset(t, budget)
	timer.expired <- time.Now()
	_ = s.terminal.WaitTransport()
	select {
	case <-s.Done():
		t.Fatal("operation 未回收便结束 Session")
	default:
	}
	close(ch.release)
	result <- fault.Error("SFTP_UNAVAILABLE")
	close(workerDone)
	if err = awaitFileResult(t, finished); err != nil {
		t.Fatal("已确认提交被清理失败覆盖", err)
	}
	finishControlledOperation(s, op)
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Session 未完成回收")
	}
	if _, err = other.terminal.Write([]byte("other-session-after-cleanup-fallback")); err != nil {
		t.Fatal(err)
	}
	waitBytes(t, other, []byte("other-session-after-cleanup-fallback"))
	if !op.abnormal {
		t.Fatal("没有记录异常回收")
	}
	if err = s.WaitFileOperation(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestFileOperationProgressWinsExpiredIdleEvent(t *testing.T) {
	m, login, id, _ := setup(t)
	s := createSession(t, m, login, id)
	op, err := s.beginFileOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finishControlledOperation(s, op)
	timer := newControlledFileTimer()
	result, workerDone, opened := make(chan error, 1), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() { finished <- s.waitFileOperation(op, result, workerDone, opened, timer, timer.expired) }()
	close(opened)
	timer.awaitReset(t, time.Duration(m.cfg.Server.RequestTimeout)*time.Second)
	op.progressed()
	timer.expired <- time.Now()
	// 屏障：只有 timer 分支计算的剩余预算严格小于完整预算，证明它实际处理了到期事件。
	for {
		select {
		case d := <-timer.resets:
			if d > 0 && d < time.Duration(m.cfg.Server.RequestTimeout)*time.Second {
				if op.ctx.Err() != nil {
					t.Fatal("有实际进展仍被空闲 timer 取消")
				}
				result <- nil
				close(workerDone)
				if err = awaitFileResult(t, finished); err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("未处理受控 timer")
		}
	}
}
