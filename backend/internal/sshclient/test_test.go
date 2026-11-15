package sshclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// serveDiagnosticSSH 只在回环地址提供测试握手/认证，拒绝所有通道。
func serveDiagnosticSSH(t *testing.T, cfg *ssh.ServerConfig) (int, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		remote, channels, requests, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		defer remote.Close()
		requestsDone := make(chan struct{})
		go func() { ssh.DiscardRequests(requests); close(requestsDone) }()
		for channel := range channels {
			_ = channel.Reject(ssh.Prohibited, "no channels")
		}
		<-requestsDone
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return listener.Addr().(*net.TCPAddr).Port, done
}
func diagnosticCipher(t *testing.T, password string) (*secret.Cipher, []byte) {
	t.Helper()
	cipher, err := secret.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt([]byte(password))
	if err != nil {
		t.Fatal(err)
	}
	return cipher, encrypted
}

func TestDiagnosticAuthCallbackPreservesPasswordBehavior(t *testing.T) {
	for _, mode := range []string{"correct", "wrong", "password-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			type result struct {
				Code      error
				Calls     int32
				Passwords []string
			}
			var results []result
			for _, enabled := range []bool{false, true} {
				key := testutil.Key(t)
				var calls atomic.Int32
				var passwords []string
				cfg := &ssh.ServerConfig{}
				if mode == "password-unavailable" {
					cfg.PublicKeyCallback = func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, errors.New("denied") }
				} else {
					cfg.PasswordCallback = func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
						calls.Add(1)
						passwords = append(passwords, string(p))
						if string(p) != "expected" {
							return nil, errors.New("denied")
						}
						return nil, nil
					}
				}
				cfg.AddHostKey(key)
				port, done := serveDiagnosticSSH(t, cfg)
				password := "expected"
				if mode == "wrong" {
					password = "wrong"
				}
				cipher, encrypted := diagnosticCipher(t, password)
				ctx := context.Background()
				var d *Diagnostic
				if enabled {
					ctx, d = WithDiagnostic(ctx, "server")
				}
				observed, err := Test(ctx, Snapshot{Host: "127.0.0.1", Port: port, Username: "user", PasswordEncrypted: encrypted, PublicKey: key.PublicKey().Marshal()}, cipher, 3*time.Second)
				<-done
				if observed != nil {
					t.Fatal("可信密钥产生探测")
				}
				results = append(results, result{err, calls.Load(), passwords})
				if enabled {
					r := d.Snapshot()
					if r.AuthCallbackCount == 0 || r.HostKeyDecision != "match" {
						t.Fatal(r)
					}
					expectedPassword := mode != "password-unavailable"
					if r.PasswordCallbackEntered != expectedPassword || r.PasswordProvided != expectedPassword {
						t.Fatal(r)
					}
					if expectedPassword && calls.Load() != 1 {
						t.Fatal("密码认证次数变化", r)
					}
				}
			}
			if !reflect.DeepEqual(results[0], results[1]) {
				t.Fatalf("诊断改变认证行为: %#v / %#v", results[0], results[1])
			}
			want := error(nil)
			if mode == "wrong" {
				want = fault.Error("SSH_AUTH_FAILED")
			}
			if mode == "password-unavailable" {
				want = fault.Error("SSH_CONNECT_FAILED")
			}
			if results[0].Code != want {
				t.Fatalf("原有业务结果变化: %v", results)
			}
		})
	}
}

// 对完整 Auth 列表额外验证：观察回调不得改变方法顺序、重试或调用次数。
func TestDiagnosticAuthCallbackPreservesMethodOrder(t *testing.T) {
	var traces [][]string
	for _, enabled := range []bool{false, true} {
		hostKey, userKey := testutil.Key(t), testutil.Key(t)
		var trace []string
		cfg := &ssh.ServerConfig{
			PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, errors.New("denied") },
			PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
				if string(p) != "expected" {
					return nil, errors.New("denied")
				}
				return nil, nil
			},
		}
		cfg.AddHostKey(hostKey)
		port, done := serveDiagnosticSSH(t, cfg)
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		clientCfg := &ssh.ClientConfig{User: "user", HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()), Auth: []ssh.AuthMethod{
			ssh.PublicKeysCallback(func() ([]ssh.Signer, error) { trace = append(trace, "publickey"); return []ssh.Signer{userKey}, nil }),
			ssh.PasswordCallback(func() (string, error) { trace = append(trace, "password"); return "expected", nil }),
		}}
		_, d := WithDiagnostic(context.Background(), "server")
		if enabled {
			clientCfg.AuthCallback = d.observeAuth
		}
		client, _, _, err := ssh.NewClientConn(conn, address, clientCfg)
		if err != nil {
			t.Fatal(err)
		}
		_ = client.Close()
		_ = client.Wait()
		<-done
		traces = append(traces, trace)
	}
	if !reflect.DeepEqual(traces[0], []string{"publickey", "password"}) || !reflect.DeepEqual(traces[0], traces[1]) {
		t.Fatal(traces)
	}
}

type corruptSigner struct{ ssh.Signer }

func (s corruptSigner) Sign(random io.Reader, data []byte) (*ssh.Signature, error) {
	signature, err := s.Signer.Sign(random, data)
	if err == nil {
		signature.Blob = bytes.Clone(signature.Blob)
		signature.Blob[0] ^= 1
	}
	return signature, err
}

// 服务端仅公布不被客户端支持的名称，失败应发生在协商阶段。
type unsupportedKey struct{ ssh.PublicKey }

func (unsupportedKey) Type() string { return "private-token-UNTRUSTED\npassword=do-not-log" }

type unsupportedSigner struct{ ssh.Signer }

func (s unsupportedSigner) PublicKey() ssh.PublicKey { return unsupportedKey{s.Signer.PublicKey()} }

func TestDiagnosticHostKeySafety(t *testing.T) {
	for _, mode := range []string{"changed", "first-trust", "bad-signature", "no-common-host-key"} {
		t.Run(mode, func(t *testing.T) {
			key := testutil.Key(t)
			signer := ssh.Signer(key)
			if mode == "bad-signature" {
				signer = corruptSigner{key}
			}
			if mode == "no-common-host-key" {
				signer = unsupportedSigner{key}
			}
			var calls atomic.Int32
			cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { calls.Add(1); return nil, nil }}
			cfg.AddHostKey(signer)
			port, done := serveDiagnosticSSH(t, cfg)
			cipher, encrypted := diagnosticCipher(t, "PASSWORD-SECRET")
			old := testutil.Key(t).PublicKey().Marshal()
			if mode == "first-trust" {
				old = nil
			}
			ctx, d := WithDiagnostic(context.Background(), "server")
			observed, err := Test(ctx, Snapshot{Host: "127.0.0.1", Port: port, Username: "user", PasswordEncrypted: encrypted, PublicKey: old}, cipher, 3*time.Second)
			<-done
			r := d.Snapshot()
			if r.PasswordCallbackEntered || r.PasswordProvided || r.AuthCallbackCount != 0 || calls.Load() != 0 {
				t.Fatalf("未信任密钥进入认证: %+v", r)
			}
			if mode == "changed" || mode == "first-trust" {
				want := "changed"
				if mode == "first-trust" {
					want = "first_trust"
				}
				if err != nil || observed == nil || r.HostKeyDecision != want || !r.HostKeyRejectionPreserved || r.HostKeyCallbackCount != 1 {
					t.Fatal(observed, err, r)
				}
				if !bytes.Equal(observed.PublicKey, key.PublicKey().Marshal()) || r.ObservedFingerprint != ssh.FingerprintSHA256(key.PublicKey()) {
					t.Fatal("错误公钥进入确认结果")
				}
				if r.NegotiatedHostKeyAlgorithm != "" {
					t.Fatal("不匹配时伪造协商算法")
				}
			} else {
				if err != fault.Error("SSH_CONNECT_FAILED") || observed != nil || r.HostKeyCallbackCount != 0 {
					t.Fatal(observed, err, r)
				}
				if mode == "bad-signature" && r.FailureClass != "signature_check_failed" {
					t.Fatal(r)
				}
				if mode == "no-common-host-key" && (r.FailureClass != "algorithm_negotiation_failed" || r.NegotiationCategory != "host key" || r.PeerAlgorithmsOmitted == 0) {
					t.Fatal(r)
				}
			}
			encoded, _ := json.Marshal(r)
			for _, secret := range []string{"PASSWORD-SECRET", "private-token-UNTRUSTED", "do-not-log", string(encrypted), base64.StdEncoding.EncodeToString(encrypted), hex.EncodeToString(encrypted), base64.StdEncoding.EncodeToString(key.PublicKey().Marshal())} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("诊断泄露敏感值")
				}
			}
		})
	}
}

func TestDiagnosticRSAKeyTypeAndNegotiatedAlgorithm(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	port, done := serveDiagnosticSSH(t, cfg)
	cipher, encrypted := diagnosticCipher(t, "password")
	ctx, d := WithDiagnostic(context.Background(), "rsa-server")
	_, err = Test(ctx, Snapshot{Host: "127.0.0.1", Port: port, Username: "user", PasswordEncrypted: encrypted, PublicKey: signer.PublicKey().Marshal()}, cipher, 3*time.Second)
	<-done
	r := d.Snapshot()
	if err != nil || r.ObservedKeyType != "ssh-rsa" || (r.NegotiatedHostKeyAlgorithm != "rsa-sha2-256" && r.NegotiatedHostKeyAlgorithm != "rsa-sha2-512") {
		t.Fatal(err, r)
	}
}

func TestDiagnosticClassificationAndIsolation(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		want      string
		wantBasis string
	}{
		{"wrapped-timeout", fmt.Errorf("wrapped: %w", &net.DNSError{IsTimeout: true}), "timeout", "error_type"},
		{"wrapped-rejection", fmt.Errorf("wrapped: %w", errHostKeyConfirmation), "host_key_confirmation_required", "sentinel"},
		{"unknown", errors.New("password=SECRET cookie=COOKIE token=TOKEN"), "pre_host_key_callback_failure", "execution_checkpoint"},
		{"rsa", fmt.Errorf("wrapped: %w", rsa.ErrVerification), "signature_verification_failed", "sentinel"},
	}
	var wg sync.WaitGroup
	ids := make(chan string, len(cases))
	for _, item := range cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, d := WithDiagnostic(context.Background(), item.name)
			d.checkpoint("ssh_handshake_started")
			d.classify(item.err)
			r := d.Snapshot()
			ids <- r.AttemptID
			if r.ServerID != item.name || r.FailureClass != item.want || r.ClassificationBasis != item.wantBasis {
				t.Error(r)
			}
			encoded, _ := json.Marshal(r)
			for _, secret := range []string{"SECRET", "COOKIE", "TOKEN"} {
				if strings.Contains(string(encoded), secret) {
					t.Error("日志泄露")
				}
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatal("请求标识重复")
		}
		seen[id] = true
	}
	_, d := WithDiagnostic(context.Background(), "snapshot")
	d.observeAuth(&ssh.ClientAuthContext{AllowedMethods: []string{"password", "SECRET"}})
	r := d.Snapshot()
	r.AuthAllowedMethods[0] = "changed"
	if got := d.Snapshot().AuthAllowedMethods; !reflect.DeepEqual(got, []string{"password"}) {
		t.Fatal("快照不是独立副本", got)
	}
}

func TestDiagnosticMultipleHostKeysReportsSelectedKey(t *testing.T) {
	// 当前 v0.59.0 默认顺序中 ECDSA 优先于 Ed25519；只更换未被协商的密钥不应报变化。
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	cipher, encrypted := diagnosticCipher(t, "password")
	for i := 0; i < 2; i++ {
		cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
		cfg.AddHostKey(testutil.Key(t)) // 每次生成不同 Ed25519 密钥。
		cfg.AddHostKey(selected)
		port, done := serveDiagnosticSSH(t, cfg)
		ctx, d := WithDiagnostic(context.Background(), "multiple-keys")
		observed, err := Test(ctx, Snapshot{Host: "127.0.0.1", Port: port, Username: "user", PasswordEncrypted: encrypted, PublicKey: selected.PublicKey().Marshal()}, cipher, 3*time.Second)
		<-done
		r := d.Snapshot()
		if err != nil || observed != nil || r.HostKeyDecision != "match" || r.ObservedKeyType != "ecdsa-sha2-nistp256" || r.ObservedFingerprint != ssh.FingerprintSHA256(selected.PublicKey()) {
			t.Fatal(observed, err, r)
		}
	}
}

func TestDiagnosticTCPAndDecryptFailure(t *testing.T) {
	t.Run("tcp-dial-failure", func(t *testing.T) {
		ctx, d := WithDiagnostic(context.Background(), "tcp")
		cipher, encrypted := diagnosticCipher(t, "secret")
		// 非法端口在 Dial 阶段确定失败，不依赖外部网络服务。
		observed, err := Test(ctx, Snapshot{Host: "127.0.0.1", Port: 65536, Username: "user", PasswordEncrypted: encrypted}, cipher, time.Second)
		r := d.Snapshot()
		if observed != nil || err != fault.Error("SSH_CONNECT_FAILED") || r.TCPConnected || r.HostKeyCallbackCount != 0 || r.LastCheckpoint != "tcp_dial_started" {
			t.Fatal(observed, err, r)
		}
	})
	t.Run("decrypt", func(t *testing.T) {
		remote := testutil.StartSSH(t)
		ctx, d := WithDiagnostic(context.Background(), "decrypt")
		cipher, _ := diagnosticCipher(t, "secret")
		observed, err := Test(ctx, Snapshot{Host: "127.0.0.1", Port: remote.Port(), Username: "tester", PasswordEncrypted: []byte("CORRUPT-CIPHERTEXT"), PublicKey: remote.PublicKey().Marshal()}, cipher, time.Second)
		r := d.Snapshot()
		if observed != nil || err != fault.Error("SSH_CONNECT_FAILED") || !r.PasswordCallbackEntered || !r.PasswordDecryptFailed || r.PasswordProvided || r.FailureClass != "password_decrypt_failed" || remote.AuthCalls.Load() != 0 {
			t.Fatal(observed, err, r)
		}
	})
}
