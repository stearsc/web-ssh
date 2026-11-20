package sshclient

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/fault"
)

// Snapshot 仅由短期任务持有，任务返回后释放，不进入长期 Session。
type Snapshot struct {
	AuthType                     string
	PrivateKeySigner             func(context.Context) (ssh.Signer, error)
	Host                         string
	Port                         int
	Username                     string
	PasswordEncrypted, PublicKey []byte
}
type Observed struct {
	PublicKey              []byte
	Algorithm, Fingerprint string
}

// dial 的认证配置仅由本次调用持有；返回对象不保存密码或信任快照。
type transport struct {
	raw    net.Conn
	client *ssh.Client
}

func dial(ctx context.Context, snapshot Snapshot, cipher *secret.Cipher, timeout time.Duration) (*transport, *Observed, error) {
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
			return nil, nil, fault.Error("SSH_CONNECT_TIMEOUT")
		}
		return nil, nil, fault.Error("SSH_CONNECT_FAILED")
	}
	success := false
	defer func() {
		if !success {
			conn.Close()
		}
	}()
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
		return nil, nil, fault.Error("SSH_CONNECT_FAILED")
	}
	if key, err := ssh.ParsePublicKey(snapshot.PublicKey); err == nil {
		d.update(func(r *DiagnosticRecord) { r.TrustedFingerprint = ssh.FingerprintSHA256(key) })
	}
	verifier := &hostVerifier{key: append([]byte(nil), snapshot.PublicKey...), diagnostic: d}
	authStarted := false
	var localAuthError error
	cfg := &ssh.ClientConfig{User: snapshot.Username, HostKeyCallback: verifier.check, Auth: []ssh.AuthMethod{ssh.PasswordCallback(func() (string, error) {
		d.update(func(r *DiagnosticRecord) { r.PasswordCallbackEntered = true; r.LastCheckpoint = "password_callback" })
		if !verifier.isTrusted() {
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
	if snapshot.AuthType == "private_key" {
		clear(cfg.Auth)
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			if !verifier.isTrusted() {
				return nil, errors.New("Host Key 尚未校验")
			}
			if snapshot.PrivateKeySigner == nil {
				localAuthError = fault.Error("PRIVATE_KEY_UNAVAILABLE")
				return nil, localAuthError
			}
			signer, signErr := snapshot.PrivateKeySigner(ctx)
			if signErr != nil {
				localAuthError = signErr
				return nil, signErr
			}
			authStarted = true
			d.checkpoint("public_key_provided")
			return []ssh.Signer{signer}, nil
		})}
	} else if snapshot.AuthType != "" && snapshot.AuthType != "password" {
		return nil, nil, fault.Error("INVALID_REQUEST")
	}
	if d != nil {
		cfg.AuthCallback = d.observeAuth
	}
	d.checkpoint("ssh_handshake_started")
	client, channels, requests, err := ssh.NewClientConn(conn, address, cfg)
	// NewClientConn 复制 ClientConfig；清空共享 Auth 切片，释放其副本持有的认证闭包。
	clear(cfg.Auth)
	cfg.Auth = nil
	observed := verifier.observation()
	d.classify(err)
	if observed != nil {
		return nil, observed, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, fault.Error("SSH_CONNECT_TIMEOUT")
		}
		if localAuthError != nil {
			return nil, nil, localAuthError
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return nil, nil, fault.Error("SSH_CONNECT_TIMEOUT")
		}
		if authStarted && strings.Contains(err.Error(), "unable to authenticate") {
			return nil, nil, fault.Error("SSH_AUTH_FAILED")
		}
		return nil, nil, fault.Error("SSH_CONNECT_FAILED")
	}
	d.checkpoint("ssh_authenticated")
	if algorithms, ok := client.(ssh.AlgorithmsConnMetadata); ok {
		a := algorithms.Algorithms()
		d.update(func(r *DiagnosticRecord) {
			r.NegotiatedHostKeyAlgorithm = safeAlgorithm(a.HostKey)
			r.NegotiatedKEX = safeAlgorithm(a.KeyExchange)
		})
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		client.Close()
		return nil, nil, fault.Error("SSH_CONNECT_FAILED")
	}
	success = true
	return &transport{raw: conn, client: ssh.NewClient(client, channels, requests)}, nil, nil
}

func Test(ctx context.Context, snapshot Snapshot, cipher *secret.Cipher, timeout time.Duration) (*Observed, error) {
	t, observed, err := dial(ctx, snapshot, cipher, timeout)
	if err != nil || observed != nil {
		return observed, err
	}
	closeErr := t.client.Close()
	_ = t.client.Wait()
	if closeErr != nil {
		return nil, fault.Error("SSH_CONNECT_FAILED")
	}
	diagnostic(ctx).checkpoint("connection_closed")
	return nil, nil
}
