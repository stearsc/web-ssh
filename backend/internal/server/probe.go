package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/sshclient"
)

type probe struct {
	loginID, serverID string
	version           uint64
	snapshot          Record
	observed          sshclient.Observed
	expires           time.Time
	occupied          bool
}
type Confirmation struct {
	ProbeID             string  `json:"probe_id"`
	ExpiresAt           string  `json:"expires_at"`
	Host                string  `json:"host"`
	Port                int     `json:"port"`
	Algorithm           string  `json:"algorithm"`
	Fingerprint         string  `json:"fingerprint"`
	PreviousFingerprint *string `json:"previous_fingerprint"`
	Reason              string  `json:"reason"`
}
type TestResult struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Authenticated bool   `json:"authenticated"`
}

func (s *Service) invalidate(id string) {
	s.probesMu.Lock()
	defer s.probesMu.Unlock()
	for hash, p := range s.probes {
		if p.serverID == id {
			delete(s.probes, hash)
		}
	}
}
func (s *Service) Revoke(login string) {
	s.probesMu.Lock()
	defer s.probesMu.Unlock()
	for hash, p := range s.probes {
		if p.loginID == login {
			delete(s.probes, hash)
		}
	}
}
func (s *Service) cleanProbes() {
	now := s.now()
	for hash, p := range s.probes {
		if !p.occupied && !now.Before(p.expires) {
			delete(s.probes, hash)
		}
	}
}
func (s *Service) Cleanup() { s.probesMu.Lock(); defer s.probesMu.Unlock(); s.cleanProbes() }
func (s *Service) Test(ctx context.Context, login *auth.Session, id string, timeout time.Duration) (TestResult, *Confirmation, error) {
	st, err := s.state(id)
	if err != nil {
		return TestResult{}, nil, err
	}
	st.mu.Lock()
	if err := s.reload(ctx, st); err != nil {
		st.mu.Unlock()
		return TestResult{}, nil, err
	}
	release, err := login.Acquire(s.now())
	if err != nil {
		st.mu.Unlock()
		return TestResult{}, nil, err
	}
	snapshot := st.record
	version := st.version
	release()
	st.mu.Unlock()
	observed, err := sshclient.Test(ctx, sshclient.Snapshot{Host: snapshot.Host, Port: snapshot.Port, Username: snapshot.Username, PasswordEncrypted: snapshot.PasswordEncrypted, PublicKey: snapshot.PublicKey}, s.cipher, timeout)
	if err != nil {
		return TestResult{}, nil, err
	}
	if observed == nil {
		return TestResult{snapshot.Host, snapshot.Port, snapshot.Username, true}, nil, nil
	}
	return s.publish(ctx, login, st, snapshot, version, *observed)
}
func (s *Service) publish(ctx context.Context, login *auth.Session, st *State, snapshot Record, version uint64, observed sshclient.Observed) (TestResult, *Confirmation, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if ctx.Err() != nil {
		return TestResult{}, nil, fault.Error("SERVER_UNAVAILABLE")
	}
	if st.deleted || st.unavailable {
		return TestResult{}, nil, fault.Error("SERVER_UNAVAILABLE")
	}
	if st.version != version || st.record.Host != snapshot.Host || st.record.Port != snapshot.Port || !bytes.Equal(st.record.PublicKey, snapshot.PublicKey) {
		return TestResult{}, nil, fault.Error("SERVER_CONFIG_CHANGED")
	}
	release, err := login.Acquire(s.now())
	if err != nil {
		return TestResult{}, nil, err
	}
	defer release()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return TestResult{}, nil, fault.Error("INTERNAL_ERROR")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	expires := s.now().Add(s.probeTTL).Truncate(time.Millisecond)
	if login.Expires.Before(expires) {
		expires = login.Expires
	}
	// 探测凭证不保留认证密文，仅记录目标与原信任。
	snapshot.PasswordEncrypted = nil
	p := &probe{loginID: login.ID, serverID: snapshot.ID, version: version, snapshot: snapshot, observed: observed, expires: expires}
	s.probesMu.Lock()
	s.cleanProbes()
	if int64(len(s.probes)) >= s.probeLimit {
		s.probesMu.Unlock()
		return TestResult{}, nil, fault.Error("HOST_KEY_PROBE_LIMIT")
	}
	s.probes[hash] = p
	s.probesMu.Unlock()
	view := &Confirmation{ProbeID: token, ExpiresAt: Timestamp(expires), Host: snapshot.Host, Port: snapshot.Port, Algorithm: observed.Algorithm, Fingerprint: observed.Fingerprint, Reason: "first_trust"}
	if len(snapshot.PublicKey) > 0 {
		view.Reason = "key_changed"
		view.PreviousFingerprint = &snapshot.Fingerprint
	}
	return TestResult{}, view, nil
}
func (s *Service) Confirm(ctx context.Context, login *auth.Session, id, token string) (*HostKeyView, error) {
	hash := sha256.Sum256([]byte(token))
	st, err := s.state(id)
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := s.reload(ctx, st); err != nil {
		return nil, err
	}
	release, err := login.Acquire(s.now())
	if err != nil {
		return nil, err
	}
	defer release()
	s.probesMu.Lock()
	p := s.probes[hash]
	if p == nil || p.occupied || !s.now().Before(p.expires) || p.loginID != login.ID || p.serverID != id || p.version != st.version || p.snapshot.Host != st.record.Host || p.snapshot.Port != st.record.Port || !bytes.Equal(p.snapshot.PublicKey, st.record.PublicKey) {
		s.probesMu.Unlock()
		return nil, fault.Error("INVALID_PROBE")
	}
	p.occupied = true
	s.probesMu.Unlock()
	r := st.record
	r.PublicKey = p.observed.PublicKey
	r.Algorithm = p.observed.Algorithm
	r.Fingerprint = p.observed.Fingerprint
	r.UpdatedAt = s.now().UnixMilli()
	// 事务期间仅持服务器/登录锁；索引清理不能移除正在提交的凭证。
	result := s.transact(ctx, func(tx *sql.Tx) error { return save(ctx, tx, r, false) })
	if err := s.outcome(ctx, st, result); err != nil {
		s.probesMu.Lock()
		p.occupied = false
		s.cleanProbes()
		s.probesMu.Unlock()
		return nil, err
	}
	st.record = r
	s.changed(st)
	return r.view().HostKey, nil
}
