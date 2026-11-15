package sshclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// hostKeyRejection 是不可变的包内哨兵，不是公开业务错误码。
type hostKeyRejection string

func (e hostKeyRejection) Error() string { return string(e) }

const errHostKeyConfirmation hostKeyRejection = "需要人工确认 Host Key"

type diagnosticKey struct{}

// Diagnostic 每个 HTTP 测试请求独立创建；不存储配置、密码、原始错误或探测令牌。
type Diagnostic struct {
	mu      sync.Mutex
	started time.Time
	record  DiagnosticRecord
}

type DiagnosticRecord struct {
	AttemptID                  string   `json:"attempt_id"`
	ServerID                   string   `json:"server_id"`
	ElapsedMS                  int64    `json:"elapsed_ms"`
	LastCheckpoint             string   `json:"last_checkpoint"`
	FailureClass               string   `json:"failure_class"`
	ClassificationBasis        string   `json:"classification_basis"`
	TCPConnected               bool     `json:"tcp_connected"`
	HostKeyCallbackCount       int      `json:"host_key_callback_count"`
	HostKeyDecision            string   `json:"host_key_decision"`
	ObservedKeyType            string   `json:"observed_key_type"`
	ObservedFingerprint        string   `json:"observed_fingerprint"`
	TrustedFingerprint         string   `json:"trusted_fingerprint"`
	HostKeyRejectionPreserved  bool     `json:"host_key_rejection_preserved"`
	AuthCallbackCount          int      `json:"auth_callback_count"`
	NegotiatedHostKeyAlgorithm string   `json:"negotiated_host_key_algorithm"`
	NegotiatedKEX              string   `json:"negotiated_kex"`
	PasswordCallbackEntered    bool     `json:"password_callback_entered"`
	PasswordDecryptFailed      bool     `json:"password_decrypt_failed"`
	PasswordProvided           bool     `json:"password_provided"`
	AuthAllowedMethods         []string `json:"auth_allowed_methods"`
	AuthTriedMethods           []string `json:"auth_tried_methods"`
	NegotiationCategory        string   `json:"negotiation_category"`
	LocalAlgorithms            []string `json:"local_algorithms"`
	PeerAlgorithms             []string `json:"peer_algorithms"`
	LocalAlgorithmsOmitted     int      `json:"local_algorithms_omitted"`
	PeerAlgorithmsOmitted      int      `json:"peer_algorithms_omitted"`
	ContextResult              string   `json:"context_result"`
	ProbePublishResult         string   `json:"probe_publish_result"`
	BusinessCode               string   `json:"business_code"`
}

func WithDiagnostic(ctx context.Context, serverID string) (context.Context, *Diagnostic) {
	d := &Diagnostic{started: time.Now(), record: DiagnosticRecord{
		AttemptID: rand.Text(), ServerID: serverID, LastCheckpoint: "request_started",
		FailureClass: "none", ClassificationBasis: "none", HostKeyDecision: "not_observed",
		ContextResult: "not_observed", ProbePublishResult: "not_attempted",
	}}
	return context.WithValue(ctx, diagnosticKey{}, d), d
}
func diagnostic(ctx context.Context) *Diagnostic {
	d, _ := ctx.Value(diagnosticKey{}).(*Diagnostic)
	return d
}
func (d *Diagnostic) update(fn func(*DiagnosticRecord)) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(&d.record)
}

// Snapshot 返回独立副本，日志序列化不接触在途回调状态。
func (d *Diagnostic) Snapshot() DiagnosticRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.record
	r.ElapsedMS = time.Since(d.started).Milliseconds()
	r.AuthAllowedMethods = slices.Clone(r.AuthAllowedMethods)
	r.AuthTriedMethods = slices.Clone(r.AuthTriedMethods)
	r.LocalAlgorithms = slices.Clone(r.LocalAlgorithms)
	r.PeerAlgorithms = slices.Clone(r.PeerAlgorithms)
	return r
}
func (d *Diagnostic) Complete(code string) {
	d.update(func(r *DiagnosticRecord) { r.BusinessCode = code })
}

// RecordProbePublication 的参数仅由 Service 的固定执行分支提供。
func RecordProbePublication(ctx context.Context, published bool) {
	diagnostic(ctx).update(func(r *DiagnosticRecord) {
		r.ProbePublishResult = "failed"
		if published {
			r.ProbePublishResult = "published"
		}
	})
}
func (d *Diagnostic) checkpoint(value string) {
	d.update(func(r *DiagnosticRecord) { r.LastCheckpoint = value })
}
func (d *Diagnostic) contextResult(ctx context.Context) {
	d.update(func(r *DiagnosticRecord) {
		switch ctx.Err() {
		case nil:
			r.ContextResult = "none"
		case context.Canceled:
			r.ContextResult = "canceled"
		case context.DeadlineExceeded:
			r.ContextResult = "deadline_exceeded"
		default:
			r.ContextResult = "unknown"
		}
	})
}

// 远端可影响的名称仅保留依赖提供的已知算法；绝不记录任意名称或错误文本。
func safeAlgorithms(values []string) []string {
	supported, insecure := ssh.SupportedAlgorithms(), ssh.InsecureAlgorithms()
	known := slices.Concat(supported.KeyExchanges, supported.HostKeys, supported.Ciphers, supported.MACs,
		insecure.KeyExchanges, insecure.HostKeys, insecure.Ciphers, insecure.MACs)
	out := make([]string, 0)
	for _, value := range values {
		if len(out) == 64 {
			break
		}
		if len(value) <= 128 && slices.Contains(known, value) && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}
func safeAlgorithm(value string) string {
	values := safeAlgorithms([]string{value})
	if len(values) == 1 {
		return values[0]
	}
	return "unknown"
}
func safeMethods(values []string) []string {
	out := make([]string, 0)
	for _, value := range values {
		if slices.Contains([]string{"none", "password", "publickey", "keyboard-interactive", "gssapi-with-mic"}, value) && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}
func (d *Diagnostic) observeAuth(c *ssh.ClientAuthContext) (ssh.AuthMethod, error) {
	d.update(func(r *DiagnosticRecord) {
		r.AuthCallbackCount++
		r.LastCheckpoint = "auth_callback"
		r.NegotiatedHostKeyAlgorithm = safeAlgorithm(c.Algorithms.HostKey)
		r.NegotiatedKEX = safeAlgorithm(c.Algorithms.KeyExchange)
		r.AuthAllowedMethods = safeMethods(c.AllowedMethods)
		r.AuthTriedMethods = safeMethods(c.TriedMethods)
	})
	// v0.59.0 此返回值继续使用 Auth 的原有顺序，不提供替代认证方法。
	return nil, nil
}

// classify 只生成日志分类，不改变业务错误映射。无法定位时保留 unknown。
func (d *Diagnostic) classify(err error) {
	if d == nil || err == nil {
		return
	}
	d.update(func(r *DiagnosticRecord) {
		r.FailureClass, r.ClassificationBasis = "unknown", "unclassified"
		if r.HostKeyCallbackCount == 0 && r.LastCheckpoint == "ssh_handshake_started" {
			r.FailureClass = "pre_host_key_callback_failure"
		}
		if r.PasswordDecryptFailed {
			r.FailureClass, r.ClassificationBasis = "password_decrypt_failed", "execution_point"
			return
		}
		if errors.Is(err, errHostKeyConfirmation) {
			r.HostKeyRejectionPreserved = true
			r.FailureClass, r.ClassificationBasis = "host_key_confirmation_required", "sentinel"
			return
		}
		var negotiation *ssh.AlgorithmNegotiationError
		if errors.As(err, &negotiation) {
			r.FailureClass, r.ClassificationBasis = "algorithm_negotiation_failed", "error_type"
			// What 是远端协商类别的库内标签，只允许固定值。
			switch negotiation.What {
			case "key exchange", "host key", "client to server cipher", "server to client cipher", "client to server MAC", "server to client MAC", "client to server compression", "server to client compression":
				r.NegotiationCategory = negotiation.What
			default:
				r.NegotiationCategory = "unknown"
			}
			r.LocalAlgorithms = safeAlgorithms(negotiation.SupportedAlgorithms)
			r.PeerAlgorithms = safeAlgorithms(negotiation.RequestedAlgorithms)
			r.LocalAlgorithmsOmitted = len(negotiation.SupportedAlgorithms) - len(r.LocalAlgorithms)
			r.PeerAlgorithmsOmitted = len(negotiation.RequestedAlgorithms) - len(r.PeerAlgorithms)
			return
		}
		var network net.Error
		if errors.As(err, &network) && network.Timeout() {
			r.FailureClass, r.ClassificationBasis = "timeout", "error_type"
			return
		}
		if errors.Is(err, rsa.ErrVerification) && r.HostKeyCallbackCount == 0 {
			r.FailureClass, r.ClassificationBasis = "signature_verification_failed", "sentinel"
			return
		}
		leaf := err
		for errors.Unwrap(leaf) != nil {
			leaf = errors.Unwrap(leaf)
		}
		// 仅匹配当前依赖产生的固定错误，且不接受远端 disconnect 的文本字段。
		if r.HostKeyCallbackCount == 0 && (leaf.Error() == "ssh: signature did not verify" || leaf.Error() == "ssh: signature parse error") {
			r.FailureClass, r.ClassificationBasis = "signature_check_failed", "v0.59.0_exact_message"
			return
		}
		if r.AuthCallbackCount > 0 && strings.HasPrefix(leaf.Error(), "ssh: unable to authenticate, attempted methods [") && strings.HasSuffix(leaf.Error(), ", no supported methods remain") {
			r.FailureClass, r.ClassificationBasis = "authentication_exhausted", "v0.59.0_message_pattern"
			return
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			r.FailureClass, r.ClassificationBasis = "transport_eof", "sentinel"
		}
	})
}
