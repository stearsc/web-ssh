package server

import (
	"bytes"
	"context"
	"log/slog"

	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/privatekey"
	"github.com/stearsc/web-ssh/backend/internal/sshclient"
	"golang.org/x/crypto/ssh"
)

type Option func(*Service)

// WithPrivateKeyStore 供应用组装及隔离集成测试注入同一个私钥存储实例。
func WithPrivateKeyStore(store *privatekey.Store) Option { return func(s *Service) { s.keys = store } }

func (s *Service) credentials(ctx context.Context, in Input, r *Record, create bool) (*privatekey.Lease, error) {
	mode := r.AuthType
	if in.AuthType != "" {
		mode = in.AuthType
	}
	if mode == "" {
		mode = "password"
	}
	hasFile := len(in.PrivateKey) > 0
	hasPassphrase := in.PassphrasePresent || in.Passphrase != ""
	if mode == "password" {
		if hasFile || hasPassphrase || (create || r.AuthType == "private_key") && in.Password == "" {
			return nil, fault.Error("INVALID_REQUEST")
		}
		if in.Password != "" {
			hasChanged := true
			if r.AuthType == "password" && len(r.PasswordEncrypted) > 0 {
				plain, err := s.cipher.Decrypt(r.PasswordEncrypted)
				if err != nil {
					return nil, fault.Error("SERVER_UNAVAILABLE")
				}
				hasChanged = !bytes.Equal(plain, []byte(in.Password))
				clear(plain)
			}
			if hasChanged {
				encrypted, err := s.cipher.Encrypt([]byte(in.Password))
				if err != nil {
					return nil, fault.Error("INTERNAL_ERROR")
				}
				r.PasswordEncrypted = encrypted
			}
		}
		r.AuthType = "password"
		r.PrivateKeyObjectID = ""
		r.PassphraseEncrypted = nil
		return nil, nil
	}
	if mode != "private_key" || in.PasswordPresent || in.Password != "" {
		return nil, fault.Error("INVALID_REQUEST")
	}
	if !hasFile {
		if create || r.AuthType != "private_key" || hasPassphrase {
			return nil, fault.Error("INVALID_REQUEST")
		}
		return nil, nil
	}
	if in.AuthType != "private_key" {
		return nil, fault.Error("INVALID_REQUEST")
	}
	_, err := privatekey.Parse(ctx, in.PrivateKey, []byte(in.Passphrase))
	if err != nil {
		return nil, err
	}
	upload, err := s.keys.Save(ctx, in.PrivateKey)
	if err != nil {
		return nil, err
	}
	encrypted, err := s.cipher.EncryptPassphrase(r.ID, []byte(in.Passphrase))
	if err != nil {
		upload.Release()
		return nil, fault.Error("INTERNAL_ERROR")
	}
	r.AuthType = "private_key"
	r.PasswordEncrypted = nil
	r.PrivateKeyObjectID = upload.ID
	r.PassphraseEncrypted = encrypted
	return upload, nil
}

// authSnapshot 的文件 lease 由短期 Test/Creation 持有，只有验证 Host Key 后才读取/解析。
func (s *Service) authSnapshot(r Record) sshclient.Snapshot {
	snapshot := sshclient.Snapshot{Host: r.Host, Port: r.Port, Username: r.Username, AuthType: r.AuthType, PasswordEncrypted: r.PasswordEncrypted, PublicKey: r.PublicKey}
	if r.AuthType == "private_key" {
		snapshot.PrivateKeySigner = func(ctx context.Context) (ssh.Signer, error) {
			raw, err := s.keys.Read(ctx, r.PrivateKeyObjectID)
			if err != nil {
				return nil, err
			}
			defer clear(raw)
			var passphrase []byte
			if len(r.PassphraseEncrypted) > 0 {
				passphrase, err = s.cipher.DecryptPassphrase(r.ID, r.PassphraseEncrypted)
				if err != nil {
					return nil, fault.Error("SERVER_UNAVAILABLE")
				}
				defer clear(passphrase)
			}
			signer, err := privatekey.Parse(ctx, raw, passphrase)
			if err == fault.Error("PRIVATE_KEY_INVALID") {
				return nil, fault.Error("PRIVATE_KEY_UNAVAILABLE")
			}
			return signer, err
		}
	}
	return snapshot
}

func (s *Service) KeyVerificationAvailable() bool                 { return s.keyVerified }
func (s *Service) ReconcilePrivateKeys(ctx context.Context) error { return s.keys.Reconcile(ctx) }
func (s *Service) SweepPrivateKeys(ctx context.Context, log *slog.Logger) error {
	_, err := s.keys.Sweep(ctx, log)
	return err
}
