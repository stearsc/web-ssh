package sshclient

import (
	"context"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/fault"
)

// Terminal 只持有已建立的资源，不保存认证材料。Wait 由唯一退出监测器调用。
type Terminal struct {
	transport *transport
	shell     *ssh.Session
	input     io.WriteCloser
	once      sync.Once
}

// Open 的总期限覆盖 TCP、认证、Channel、PTY 和 Shell。输出在 Shell 前安装。
func Open(ctx context.Context, snapshot Snapshot, cipher *secret.Cipher, timeout time.Duration, cols, rows int, output io.Writer) (result *Terminal, observed *Observed, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t, observed, err := dial(ctx, snapshot, cipher, timeout)
	if err != nil || observed != nil {
		return nil, observed, err
	}
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { t.raw.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
		if ctx.Err() != nil && result != nil {
			result.Close()
			_ = result.Wait()
			_ = result.WaitTransport()
			result = nil
			err = fault.Error("SSH_CONNECT_TIMEOUT")
		}
	}()
	term := &Terminal{transport: t}
	failure := func(code string) error {
		if ctx.Err() != nil {
			return fault.Error("SSH_CONNECT_TIMEOUT")
		}
		return fault.Error(code)
	}
	success := false
	defer func() {
		if !success {
			term.Close()
			_ = t.client.Wait()
		}
	}()
	shell, err := t.client.NewSession()
	if err != nil {
		return nil, nil, failure("SSH_CONNECT_FAILED")
	}
	term.shell = shell
	shell.Stdout, shell.Stderr = output, output
	term.input, err = shell.StdinPipe()
	if err != nil {
		return nil, nil, failure("SSH_CONNECT_FAILED")
	}
	if err = shell.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{}); err != nil {
		return nil, nil, failure("SSH_PTY_FAILED")
	}
	if err = shell.Shell(); err != nil {
		return nil, nil, failure("SSH_SHELL_FAILED")
	}
	if ctx.Err() != nil {
		return nil, nil, fault.Error("SSH_CONNECT_TIMEOUT")
	}
	success = true
	return term, nil, nil
}
func (t *Terminal) Write(p []byte) (int, error) { return t.input.Write(p) }
func (t *Terminal) Resize(cols, rows int) error { return t.shell.WindowChange(rows, cols) }
func (t *Terminal) Keepalive() error {
	_, _, err := t.transport.client.SendRequest("keepalive@openssh.com", true, nil)
	return err
}
func (t *Terminal) Wait() error          { return t.shell.Wait() }
func (t *Terminal) WaitTransport() error { return t.transport.client.Wait() }

// OpenFileChannel 复用现有连接。调用者必须登记 operation，负责 channel 及请求流的完整回收。
// 底层 OpenChannel 无 context；无法独立取消时，由 Session 现有关闭流程解除阻塞。
func (t *Terminal) OpenFileChannel() (ssh.Channel, <-chan *ssh.Request, error) {
	return t.transport.client.OpenChannel("session", nil)
}

// 先关闭底层 socket，避免 SSH Channel 请求阻塞关闭；重复关闭只执行一次。
func (t *Terminal) Close() {
	t.once.Do(func() {
		t.transport.raw.Close()
		if t.shell != nil {
			t.shell.Close()
		}
		t.transport.client.Close()
	})
}
