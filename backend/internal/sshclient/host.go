package sshclient

import (
	"bytes"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// SSH 重协商仍会调用 HostKeyCallback。该对象只保存公钥与诊断，不能捕获认证快照或主密钥。
type hostVerifier struct {
	mu         sync.Mutex
	key        []byte
	trusted    bool
	observed   *Observed
	diagnostic *Diagnostic
}

func (v *hostVerifier) check(_ string, _ net.Addr, key ssh.PublicKey) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.diagnostic.update(func(r *DiagnosticRecord) {
		r.HostKeyCallbackCount++
		r.LastCheckpoint = "host_key_callback"
		r.ObservedKeyType = safeAlgorithm(key.Type())
		r.ObservedFingerprint = ssh.FingerprintSHA256(key)
	})
	if len(v.key) > 0 && bytes.Equal(v.key, key.Marshal()) {
		v.trusted = true
		v.diagnostic.update(func(r *DiagnosticRecord) { r.HostKeyDecision = "match" })
		return nil
	}
	v.trusted = false
	v.observed = &Observed{append([]byte(nil), key.Marshal()...), key.Type(), ssh.FingerprintSHA256(key)}
	v.diagnostic.update(func(r *DiagnosticRecord) {
		r.HostKeyDecision = "first_trust"
		if len(v.key) > 0 {
			r.HostKeyDecision = "changed"
		}
	})
	return errHostKeyConfirmation
}
func (v *hostVerifier) isTrusted() bool        { v.mu.Lock(); defer v.mu.Unlock(); return v.trusted }
func (v *hostVerifier) observation() *Observed { v.mu.Lock(); defer v.mu.Unlock(); return v.observed }
