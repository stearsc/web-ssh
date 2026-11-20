// Package privatekey 管理应用私钥原始文件及短期解析资源，不转换磁盘私钥格式。
package privatekey

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"unicode/utf8"

	"github.com/stearsc/web-ssh/backend/internal/fault"
	"golang.org/x/crypto/ssh"
)

const MaxFileSize = 65536
const MaxPassphraseSize = 4096
const Directory = "/var/lib/web-ssh/ssh-keys"

// 所有上传/Test/Creation 共用进程级名额；同步解析真正返回前绝不归还。
var parseSlot = make(chan struct{}, 1)

func withParseSlot(ctx context.Context, compute func() (ssh.Signer, error)) (ssh.Signer, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	select {
	case parseSlot <- struct{}{}:
	default:
		return nil, fault.Error("PRIVATE_KEY_PARSE_BUSY")
	}
	defer func() { <-parseSlot }()
	signer, err := compute()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return signer, err
}

// Parse 先低成本预检再构造 Signer；对外不暴露密码错误、私钥内容或底层解析详情。
func Parse(ctx context.Context, raw, passphrase []byte) (ssh.Signer, error) {
	if len(raw) == 0 || len(raw) > MaxFileSize || len(passphrase) > MaxPassphraseSize || !utf8.Valid(passphrase) {
		return nil, fault.Error("PRIVATE_KEY_INVALID")
	}
	return withParseSlot(ctx, func() (ssh.Signer, error) {
		signer, err := parse(raw, passphrase)
		if err != nil {
			return nil, fault.Error("PRIVATE_KEY_INVALID")
		}
		return signer, nil
	})
}

func parse(raw, passphrase []byte) (ssh.Signer, error) {
	invalid := errors.New("私钥结构无效")
	trimmed := bytes.TrimSpace(raw)
	if !bytes.HasPrefix(trimmed, []byte("-----BEGIN ")) {
		return nil, invalid
	}
	block, rest := pem.Decode(trimmed)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, invalid
	}
	var key any
	var err error
	if block.Type == "OPENSSH PRIVATE KEY" {
		// 固定库在 OpenSSH RSA Validate/Precompute 前限制模数与因子；KDF 上限保持 2048。
		if len(passphrase) == 0 {
			key, err = ssh.ParseRawPrivateKey(raw)
		} else {
			key, err = ssh.ParseRawPrivateKeyWithPassphrase(raw, passphrase)
		}
	} else {
		der := block.Bytes
		if x509.IsEncryptedPEMBlock(block) {
			if len(passphrase) == 0 {
				return nil, invalid
			}
			der, err = x509.DecryptPEMBlock(block, passphrase)
			if err != nil {
				return nil, invalid
			}
			defer clear(der)
		} else if len(passphrase) != 0 || len(block.Headers) != 0 {
			return nil, invalid
		}
		switch block.Type {
		case "RSA PRIVATE KEY":
			if err := precheckRSA(der); err != nil {
				return nil, err
			}
			key, err = x509.ParsePKCS1PrivateKey(der)
		case "PRIVATE KEY":
			if err := precheckPKCS8(der); err != nil {
				return nil, err
			}
			key, err = x509.ParsePKCS8PrivateKey(der)
		case "EC PRIVATE KEY":
			key, err = x509.ParseECPrivateKey(der)
		default:
			return nil, invalid
		}
	}
	if err != nil {
		return nil, err
	}
	if r, ok := key.(*rsa.PrivateKey); ok && r.N.BitLen() > 16384 {
		return nil, invalid
	}
	return ssh.NewSignerFromKey(key)
}

// 只遍历 DER TLV 和整数长度，不执行 RSA 校验、乘除或 CRT 预计算。
func precheckRSA(der []byte) error {
	invalid := errors.New("RSA 私钥结构或大小无效")
	var sequence asn1.RawValue
	rest, err := asn1.Unmarshal(der, &sequence)
	if err != nil || len(rest) != 0 || sequence.Tag != asn1.TagSequence || !sequence.IsCompound {
		return invalid
	}
	data := sequence.Bytes
	for i := 0; i < 9; i++ {
		var integer asn1.RawValue
		data, err = asn1.Unmarshal(data, &integer)
		if err != nil || integer.Tag != asn1.TagInteger || len(integer.Bytes) == 0 || integer.Bytes[0]&0x80 != 0 {
			return invalid
		}
		value := integer.Bytes
		if value[0] == 0 {
			value = value[1:]
		}
		maxBits := 16384
		if i == 0 || i == 2 {
			maxBits = 24
		}
		if i == 4 || i == 5 {
			maxBits = 8192
		}
		if len(value) > (maxBits+7)/8 {
			return invalid
		}
		// big.Int 仅对有界整数求位数，不进入昂贵验证。
		if new(big.Int).SetBytes(value).BitLen() > maxBits {
			return invalid
		}
	}
	// PKCS#1 的多素数扩展也会进入 x509 的校验，不能遗漏额外因子的预算。
	if len(data) > 0 {
		var others asn1.RawValue
		data, err = asn1.Unmarshal(data, &others)
		if err != nil || len(data) != 0 || others.Tag != asn1.TagSequence || !others.IsCompound {
			return invalid
		}
		for len(others.Bytes) > 0 {
			var prime asn1.RawValue
			others.Bytes, err = asn1.Unmarshal(others.Bytes, &prime)
			if err != nil || prime.Tag != asn1.TagSequence || !prime.IsCompound {
				return invalid
			}
			for i := 0; i < 3; i++ {
				var value asn1.RawValue
				prime.Bytes, err = asn1.Unmarshal(prime.Bytes, &value)
				maxBits := 16384
				if i == 0 {
					maxBits = 8192
				}
				if err != nil || value.Tag != asn1.TagInteger || len(value.Bytes) == 0 || value.Bytes[0]&0x80 != 0 || len(value.Bytes) > maxBits/8+1 || new(big.Int).SetBytes(value.Bytes).BitLen() > maxBits {
					return invalid
				}
			}
			if len(prime.Bytes) != 0 {
				return invalid
			}
		}
	}
	return nil
}

func precheckPKCS8(der []byte) error {
	var container struct {
		Version    int
		Algorithm  pkix.AlgorithmIdentifier
		PrivateKey []byte
	}
	rest, err := asn1.Unmarshal(der, &container)
	if err != nil || len(rest) != 0 {
		return errors.New("PKCS#8 结构无效")
	}
	if container.Algorithm.Algorithm.Equal(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}) {
		return precheckRSA(container.PrivateKey)
	}
	return nil
}
