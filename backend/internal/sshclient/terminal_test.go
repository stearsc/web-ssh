package sshclient

import (
	"bytes"
	"context"
	"testing"
	"time"

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
