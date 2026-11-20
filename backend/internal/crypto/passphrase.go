package crypto

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

const passphraseVersion byte = 2
const passphrasePurpose = "web-ssh/private-key-passphrase"

// 长度前缀防止用途与服务器 ID 拼接产生歧义；不绑定可变的展示资料。
func passphraseAAD(serverID string) []byte {
	aad := []byte{passphraseVersion}
	aad = binary.BigEndian.AppendUint32(aad, uint32(len(passphrasePurpose)))
	aad = append(aad, passphrasePurpose...)
	aad = binary.BigEndian.AppendUint32(aad, uint32(len(serverID)))
	return append(aad, serverID...)
}

// EncryptPassphrase 使用独立 v2 格式；无口令用 SQL NULL 表示，不加密空串。
func (c *Cipher) EncryptPassphrase(serverID string, plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, nil
	}
	if len(serverID) == 0 || len(serverID) > 128 || len(plain) > 4096 {
		return nil, errors.New("保存口令参数无效")
	}
	data := make([]byte, 13)
	data[0] = passphraseVersion
	if _, err := rand.Read(data[1:]); err != nil {
		return nil, errors.New("生成口令加密随机数失败")
	}
	return c.aead.Seal(data, data[1:13], plain, passphraseAAD(serverID)), nil
}

// DecryptPassphrase 不降级到密码 v1；认证失败不能区分主密钥、数据或上下文问题。
func (c *Cipher) DecryptPassphrase(serverID string, data []byte) ([]byte, error) {
	failure := errors.New("保存口令无法解密，可能是主密钥或数据问题")
	if len(data) < 30 || data[0] != passphraseVersion || len(serverID) == 0 || len(serverID) > 128 {
		return nil, failure
	}
	plain, err := c.aead.Open(nil, data[1:13], data[13:], passphraseAAD(serverID))
	if err != nil || len(plain) == 0 || len(plain) > 4096 {
		clear(plain)
		return nil, failure
	}
	return plain, nil
}
