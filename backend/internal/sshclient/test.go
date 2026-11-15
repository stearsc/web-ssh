package sshclient

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"golang.org/x/crypto/ssh"
)

// Snapshot 仅由短期任务持有，任务返回后释放，不进入长期 Session。
type Snapshot struct {
	Host                         string
	Port                         int
	Username                     string
	PasswordEncrypted, PublicKey []byte
}
type Observed struct {
	PublicKey              []byte
	Algorithm, Fingerprint string
}

func Test(ctx context.Context, snapshot Snapshot, cipher *secret.Cipher, timeout time.Duration) (*Observed, error) {
	d := diagnostic(ctx)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer d.contextResult(ctx)
	d.checkpoint("tcp_dial_started")
	address := net.JoinHostPort(snapshot.Host, strconv.Itoa(snapshot.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		d.classify(err)
		if ctx.Err() != nil {
			return nil, fault.Error("SSH_CONNECT_TIMEOUT")
		}
		return nil, fault.Error("SSH_CONNECT_FAILED")
	}
	defer conn.Close()
	d.update(func(r *DiagnosticRecord) { r.TCPConnected = true; r.LastCheckpoint = "tcp_connected" })
	// context 取消主动关闭底层连接，覆盖无法接收 context 的 SSH 握手/认证阻塞。
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); close(closed) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		d.checkpoint("set_deadline_failed")
		d.classify(err)
		return nil, fault.Error("SSH_CONNECT_FAILED")
	}
	if key, err := ssh.ParsePublicKey(snapshot.PublicKey); err == nil {
		d.update(func(r *DiagnosticRecord) { r.TrustedFingerprint = ssh.FingerprintSHA256(key) })
	}
	observed := (*Observed)(nil)
	trusted := false
	authStarted := false
	cfg := &ssh.ClientConfig{User: snapshot.Username, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		d.update(func(r *DiagnosticRecord) {
			r.HostKeyCallbackCount++
			r.LastCheckpoint = "host_key_callback"
			r.ObservedKeyType = safeAlgorithm(key.Type())
			r.ObservedFingerprint = ssh.FingerprintSHA256(key)
		})
		if len(snapshot.PublicKey) > 0 && bytes.Equal(snapshot.PublicKey, key.Marshal()) {
			d.update(func(r *DiagnosticRecord) { r.HostKeyDecision = "match" })
			trusted = true
			return nil
		}
		observed = &Observed{append([]byte(nil), key.Marshal()...), key.Type(), ssh.FingerprintSHA256(key)}
		d.update(func(r *DiagnosticRecord) {
			r.HostKeyDecision = "first_trust"
			if len(snapshot.PublicKey) > 0 {
				r.HostKeyDecision = "changed"
			}
		})
		return errHostKeyConfirmation
	}, Auth: []ssh.AuthMethod{ssh.PasswordCallback(func() (string, error) {
		d.update(func(r *DiagnosticRecord) { r.PasswordCallbackEntered = true; r.LastCheckpoint = "password_callback" })
		if !trusted {
			return "", errors.New("Host Key 尚未校验")
		}
		authStarted = true
		plain, err := cipher.Decrypt(snapshot.PasswordEncrypted)
		if err != nil {
			d.update(func(r *DiagnosticRecord) {
				r.PasswordDecryptFailed = true
				r.LastCheckpoint = "password_decrypt_failed"
			})
			return "", err
		}
		defer clear(plain)
		d.update(func(r *DiagnosticRecord) { r.PasswordProvided = true; r.LastCheckpoint = "password_provided" })
		return string(plain), nil
	})}}
	if d != nil {
		cfg.AuthCallback = d.observeAuth
	}
	d.checkpoint("ssh_handshake_started")
	client, _, _, err := ssh.NewClientConn(conn, address, cfg)
	cfg.Auth = nil
	d.classify(err)
	if observed != nil {
		return observed, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, fault.Error("SSH_CONNECT_TIMEOUT")
		}
		if n, ok := err.(net.Error); ok && n.Timeout() {
			return nil, fault.Error("SSH_CONNECT_TIMEOUT")
		}
		if authStarted && strings.Contains(err.Error(), "unable to authenticate") {
			return nil, fault.Error("SSH_AUTH_FAILED")
		}
		return nil, fault.Error("SSH_CONNECT_FAILED")
	}
	d.checkpoint("ssh_authenticated")
	if algorithms, ok := client.(ssh.AlgorithmsConnMetadata); ok {
		a := algorithms.Algorithms()
		d.update(func(r *DiagnosticRecord) {
			r.NegotiatedHostKeyAlgorithm = safeAlgorithm(a.HostKey)
			r.NegotiatedKEX = safeAlgorithm(a.KeyExchange)
		})
	}
	if err := client.Close(); err != nil {
		d.checkpoint("close_failed")
		d.classify(err)
		return nil, fault.Error("SSH_CONNECT_FAILED")
	}
	// 不创建 Client/Session/Channel，不申请 PTY 或 Shell；等待 SSH 传输退出。
	_ = client.Wait()
	d.checkpoint("connection_closed")
	return nil, nil
}
