package sshclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

type outputChannel chan []byte

func (c outputChannel) Write(p []byte) (int, error) { c <- bytes.Clone(p); return len(p), nil }
func TestTerminalRealSSH(t *testing.T) {
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{Banner: []byte("提示符\r\n")})
	cipher, password := diagnosticCipher(t, "ssh-secret")
	output := make(outputChannel, 16)
	term, observed, err := Open(t.Context(), Snapshot{Host: "127.0.0.1", Port: remote.Port(), Username: "tester", PasswordEncrypted: password, PublicKey: remote.Key.PublicKey().Marshal()}, cipher, time.Second, 120, 32, output)
	if err != nil || observed != nil {
		t.Fatal(observed, err)
	}
	defer term.Close()
	select {
	case p := <-output:
		if string(p) != "提示符\r\n" {
			t.Fatal(string(p))
		}
	case <-time.After(time.Second):
		t.Fatal("启动输出丢失")
	}
	payload := []byte("中文\x1b[31m原始字节\x1b[0m")
	if _, err := term.Write(payload); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-output:
		if !bytes.Equal(p, payload) {
			t.Fatal(p)
		}
	case <-time.After(time.Second):
		t.Fatal("输入未回显")
	}
	if err := term.Resize(90, 24); err != nil {
		t.Fatal(err)
	}
	if err := term.Keepalive(); err != nil {
		t.Fatal("拒绝响应仍应算健康", err)
	}
	term.Close()
	_ = term.Wait()
	_ = term.WaitTransport()
}
func TestTerminalFailuresAndCancellation(t *testing.T) {
	for _, stage := range []string{"pty-req", "shell"} {
		for _, blocked := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: "-reject", true: "-cancel"}[blocked], func(t *testing.T) {
				options := testutil.TerminalOptions{RejectPTY: stage == "pty-req", RejectShell: stage == "shell"}
				gate := make(chan struct{})
				if blocked {
					options.BlockRequest = stage
					options.Gate = gate
				}
				defer close(gate)
				remote := testutil.StartTerminalSSH(t, options)
				cipher, password := diagnosticCipher(t, "ssh-secret")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, _, e := Open(ctx, Snapshot{Host: "127.0.0.1", Port: remote.Port(), Username: "tester", PasswordEncrypted: password, PublicKey: remote.Key.PublicKey().Marshal()}, cipher, time.Second, 80, 24, make(outputChannel, 8))
					done <- e
				}()
				if blocked {
					for r := range remote.Requests {
						if r == stage {
							cancel()
							break
						}
					}
				}
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("失败路径成功")
					}
					if !blocked {
						want := fault.Error("SSH_PTY_FAILED")
						if stage == "shell" {
							want = "SSH_SHELL_FAILED"
						}
						if err != want {
							t.Fatal(err)
						}
					}
				case <-time.After(2 * time.Second):
					t.Fatal("创建未退出")
				}
			})
		}
	}
}

func TestTerminalNewSessionTimeoutReclaimsConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	signer := testutil.Key(t)
	cfg := &ssh.ServerConfig{PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if metadata.User() != "tester" || string(password) != "ssh-secret" {
			return nil, errors.New("denied")
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	authenticated := make(chan struct{})
	requested := make(chan struct{})
	remoteDone := make(chan struct{})
	remoteError := make(chan error, 1)
	go func() {
		defer close(remoteDone)
		defer listener.Close()
		raw, e := listener.Accept()
		if e != nil {
			remoteError <- e
			return
		}
		defer raw.Close()
		// 仅用于失败测试的最终兜底；成功断言必须在该期限前观察到客户端主动关闭。
		if e = raw.SetDeadline(time.Now().Add(5 * time.Second)); e != nil {
			remoteError <- e
			return
		}
		remote, channels, requests, e := ssh.NewServerConn(raw, cfg)
		if e != nil {
			remoteError <- e
			return
		}
		defer remote.Close()
		close(authenticated)
		requestsDone := make(chan struct{})
		go func() { defer close(requestsDone); ssh.DiscardRequests(requests) }()
		seen := false
		for incoming := range channels {
			if incoming.ChannelType() != "session" || seen {
				remoteError <- errors.New("unexpected channel request")
				remote.Close()
				break
			}
			seen = true
			close(requested)
			// 故意不 Accept/Reject，NewSession 必须等待总建连期限关闭真实 TCP。
		}
		_ = remote.Wait()
		<-requestsDone
	}()
	t.Cleanup(func() {
		listener.Close()
		select {
		case <-remoteDone:
		case <-time.After(6 * time.Second):
			t.Error("SSH 测试对端任务未退出")
		}
	})
	cipher, password := diagnosticCipher(t, "ssh-secret")
	snap := Snapshot{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Username: "tester", PasswordEncrypted: password, PublicKey: signer.PublicKey().Marshal()}
	const budget = time.Second
	type outcome struct {
		terminal *Terminal
		observed *Observed
		err      error
	}
	result := make(chan outcome, 1)
	openDone := make(chan struct{})
	started := time.Now()
	go func() {
		defer close(openDone)
		terminal, observed, e := Open(t.Context(), snap, cipher, budget, 80, 24, io.Discard)
		result <- outcome{terminal, observed, e}
	}()
	for _, signal := range []struct {
		name string
		done <-chan struct{}
	}{
		{"SSH 握手及认证", authenticated}, {"Session Channel 请求", requested},
	} {
		select {
		case <-signal.done:
		case e := <-remoteError:
			t.Fatal(e)
		case <-time.After(2 * time.Second):
			t.Fatal("未完成", signal.name)
		}
	}
	select {
	case got := <-result:
		if got.terminal != nil {
			got.terminal.Close()
			t.Fatal("超时返回了终端资源")
		}
		if got.observed != nil || got.err != fault.Error("SSH_CONNECT_TIMEOUT") {
			t.Fatal(got.observed, got.err)
		}
		if time.Since(started) < budget {
			t.Fatal("未等待总建连期限")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NewSession 未被总建连期限解除")
	}
	// 不依赖 t.Cleanup 或测试对端的五秒兜底来证明回收。
	for _, signal := range []struct {
		name string
		done <-chan struct{}
	}{
		{"Open 调用任务", openDone}, {"SSH 传输、请求处理及底层连接", remoteDone},
	} {
		select {
		case <-signal.done:
		case <-time.After(time.Second):
			t.Fatal(signal.name, "未及时退出")
		}
	}
	select {
	case e := <-remoteError:
		t.Fatal(e)
	default:
	}
}
