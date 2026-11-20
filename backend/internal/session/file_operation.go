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
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	progress     chan struct{}
	cleanup      chan struct{}
	cleanupOnce  sync.Once
	mu           sync.Mutex
	channel      ssh.Channel
	abnormal     bool
	committed    bool
	lastProgress time.Time
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
	op.mu.Lock()
	op.lastProgress = time.Now()
	op.mu.Unlock()
	select {
	case op.progress <- struct{}{}:
	default:
	}
}

// confirmCommit 只在原子提交明确返回成功后调用；清理失败不能撤销这个事实。
func (op *fileOperation) confirmCommit() {
	op.mu.Lock()
	op.committed = true
	op.mu.Unlock()
	op.cleanupOnce.Do(func() { close(op.cleanup) })
}

func (op *fileOperation) hasCommitted() bool {
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.committed
}

// fileOperationTimer 允许测试精确投递到期事件，不依赖休眠或 select 的随机选择。
type fileOperationTimer interface {
	Reset(time.Duration) bool
	Stop() bool
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
	timer := time.NewTimer(time.Duration(s.manager.cfg.Ssh.ConnectTimeout) * time.Second)
	defer timer.Stop()
	return s.waitFileOperation(op, result, workerDone, opened, timer, timer.C)
}

// waitFileOperation 分别裁决文件结果和资源回收；收到结果后仍须收齐所有工作协程。
func (s *Session) waitFileOperation(op *fileOperation, result <-chan error, workerDone <-chan struct{}, opened <-chan struct{}, timer fileOperationTimer, expired <-chan time.Time) error {
	cleanupBudget := time.Duration(s.manager.cfg.Ssh.OperationTimeout) * time.Second
	idleBudget := time.Duration(s.manager.cfg.Server.RequestTimeout) * time.Second
	var err error
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
				case <-expired:
					force()
					<-closeDone
				}
			}
			if op.hasCommitted() {
				return nil
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
				op.progressed()
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
		case <-expired:
			if op.hasCommitted() && (phase == "opening" || phase == "transfer") {
				phase = "cleanup"
				timer.Reset(cleanupBudget)
				continue
			}
			if phase == "transfer" {
				// 即使 progress 和 timer 同时就绪，也按最后实际进展计算空闲时间。
				op.mu.Lock()
				remaining := idleBudget - time.Since(op.lastProgress)
				op.mu.Unlock()
				if remaining > 0 {
					timer.Reset(remaining)
					continue
				}
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
			if op.hasCommitted() {
				return nil
			}
			return fault.Error("SFTP_CANCEL_FAILED")
		}
	}
}
