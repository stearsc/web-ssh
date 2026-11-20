package session

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/stearsc/web-ssh/backend/internal/fault"
)

// fileOperation 的登记、撤销与 Session 关闭共用 s.mu，不向正在 Wait 的工作组 Add。
// done 只在网络调用、channel 关闭、SFTP 接收协程和请求流全部结束后关闭。
type fileOperation struct {
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	progress    chan struct{}
	cleanup     chan struct{}
	cleanupOnce sync.Once
	mu          sync.Mutex
	channel     ssh.Channel
	abnormal    bool
}

func (s *Session) beginFileOperation(ctx context.Context) (*fileOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, fault.Error("SFTP_CANCELED")
	}
	if s.status != "CONNECTED" && s.status != "DISCONNECTED" {
		return nil, fault.Error("SFTP_SESSION_CLOSED")
	}
	if s.fileOperation != nil {
		return nil, fault.Error("SFTP_BUSY")
	}
	ctx, cancel := context.WithCancel(ctx)
	op := &fileOperation{ctx: ctx, cancel: cancel, done: make(chan struct{}), progress: make(chan struct{}, 1), cleanup: make(chan struct{})}
	s.fileOperation = op
	return op, nil
}

func (op *fileOperation) progressed() {
	select {
	case op.progress <- struct{}{}:
	default:
	}
}

// WaitFileOperation 是队列取消后的完成屏障，不查询上传进度，也不自动重试文件。
// 后续 check 等待此前请求真正回收，避免浏览器 abort 后立即撞上尚未结束的 operation。
func (s *Session) WaitFileOperation(ctx context.Context) error {
	s.mu.Lock()
	op := s.fileOperation
	s.mu.Unlock()
	if op != nil {
		select {
		case <-op.done:
			if op.abnormal {
				return fault.Error("SFTP_CANCEL_FAILED")
			}
		case <-ctx.Done():
			return fault.Error("SFTP_BUSY")
		}
	}
	return nil
}

// withFileClient 在返回之前收齐所有本次 operation 的协程。正常取消先给协作清理机会，
// 再关闭独立 channel；只有独立资源仍不能回收时才进入现有 Session.Close。
func (s *Session) withFileClient(ctx context.Context, work func(context.Context, *sftp.Client, func()) error) error {
	op, err := s.beginFileOperation(ctx)
	if err != nil {
		return err
	}
	defer func() {
		op.cancel()
		s.mu.Lock()
		s.fileOperation = nil
		close(op.done)
		s.mu.Unlock()
	}()
	openBudget := time.Duration(s.manager.cfg.Ssh.ConnectTimeout) * time.Second
	cleanupBudget := time.Duration(s.manager.cfg.Ssh.OperationTimeout) * time.Second
	idleBudget := time.Duration(s.manager.cfg.Server.RequestTimeout) * time.Second
	result := make(chan error, 1)
	opened := make(chan struct{})
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		result <- func() (err error) {
			ch, requests, err := s.terminal.OpenFileChannel()
			if err != nil {
				return fault.Error("SFTP_UNAVAILABLE")
			}
			op.mu.Lock()
			op.channel = ch
			op.mu.Unlock()
			requestsDone := make(chan struct{})
			go func() { ssh.DiscardRequests(requests); close(requestsDone) }()
			var client *sftp.Client
			defer func() {
				op.cleanupOnce.Do(func() { close(op.cleanup) })
				if client != nil {
					_ = client.Close()
				} else {
					_ = ch.Close()
				}
				<-requestsDone
			}()
			if op.ctx.Err() != nil {
				return op.ctx.Err()
			}
			accepted, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{"sftp"}))
			if err != nil || !accepted {
				return fault.Error("SFTP_UNAVAILABLE")
			}
			client, err = sftp.NewClientPipe(ch, ch)
			if err != nil {
				return fault.Error("SFTP_UNAVAILABLE")
			}
			close(opened)
			if op.ctx.Err() != nil {
				return op.ctx.Err()
			}
			return work(op.ctx, client, op.progressed)
		}()
	}()
	timer := time.NewTimer(openBudget)
	defer timer.Stop()
	ctxDone, cleanupStarted := op.ctx.Done(), op.cleanup
	phase := "opening"
	var closeDone chan struct{}
	abnormal := false
	force := func() {
		abnormal = true
		op.abnormal = true
		s.recordFileFailure()
		op.cancel()
		// closed.reason 的枚举已冻结，复用既有 ssh_error；细分错误仅通过上传 HTTP 返回。
		s.Close("ssh_error")
	}
	for {
		select {
		case err = <-result:
			<-workerDone
			if closeDone != nil {
				select {
				case <-closeDone:
				case <-timer.C:
					force()
					<-closeDone
				}
			}
			if abnormal {
				return fault.Error("SFTP_CANCEL_FAILED")
			}
			if op.ctx.Err() != nil {
				return fault.Error("SFTP_CANCELED")
			}
			return err
		case <-opened:
			opened = nil
			if phase == "opening" {
				phase = "transfer"
				timer.Reset(idleBudget)
			}
		case <-op.progress:
			if phase == "transfer" {
				timer.Reset(idleBudget)
			}
		case <-cleanupStarted:
			cleanupStarted = nil
			if phase != "closing" {
				phase = "cleanup"
				timer.Reset(cleanupBudget)
			}
		case <-ctxDone:
			ctxDone = nil
			if phase != "closing" {
				phase = "cleanup"
				timer.Reset(cleanupBudget)
			}
		case <-timer.C:
			if phase == "transfer" {
				op.cancel()
				phase = "cleanup"
				timer.Reset(cleanupBudget)
				continue
			}
			if phase != "closing" {
				op.cancel()
				op.mu.Lock()
				ch := op.channel
				op.mu.Unlock()
				if ch != nil {
					closeDone = make(chan struct{})
					go func() { _ = ch.Close(); close(closeDone) }()
					phase = "closing"
					timer.Reset(cleanupBudget)
					continue
				}
			}
			force()
			// transport 由唯一 reap 先关闭；不能提前返回或把阻塞调用留在后台。
			err = <-result
			<-workerDone
			if closeDone != nil {
				<-closeDone
			}
			return fault.Error("SFTP_CANCEL_FAILED")
		}
	}
}
