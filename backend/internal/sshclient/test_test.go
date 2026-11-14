package sshclient

import (
	"bytes"
	"context"
	"testing"
	"time"

	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

func TestCancellationTimeoutAndResources(t *testing.T) {
	for _, mode := range []string{"handshake-timeout", "handshake-cancel", "authentication-cancel"} {
		t.Run(mode, func(t *testing.T) {
			remote := testutil.StartSSH(t)
			gate := make(chan struct{})
			if mode == "authentication-cancel" {
				remote.BlockAuth(gate)
			} else {
				remote.BlockHandshake(gate)
			}
			defer close(gate)
			cipher, _ := secret.New(bytes.Repeat([]byte{1}, 32))
			password, _ := cipher.Encrypt([]byte("ssh-secret"))
			snap := Snapshot{Host: "127.0.0.1", Port: remote.Port(), Username: "tester", PasswordEncrypted: password, PublicKey: remote.PublicKey().Marshal()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := time.Second
			if mode == "handshake-timeout" {
				timeout = 40 * time.Millisecond
			}
			result := make(chan error, 1)
			go func() { _, err := Test(ctx, snap, cipher, timeout); result <- err }()
			<-remote.Accepted
			if mode == "authentication-cancel" {
				deadline := time.Now().Add(time.Second)
				for remote.AuthCalls.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if remote.AuthCalls.Load() == 0 {
					t.Fatal("没有进入认证")
				}
			}
			if mode != "handshake-timeout" {
				cancel()
			}
			select {
			case err := <-result:
				if err != fault.Error("SSH_CONNECT_TIMEOUT") {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("取消没有释放临时连接")
			}
		})
	}
}
