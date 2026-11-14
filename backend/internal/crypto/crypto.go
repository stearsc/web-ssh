package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

type Cipher struct{ aead cipher.AEAD }

func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("加密主密钥必须为 32 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead}, nil
}
func (c *Cipher) Encrypt(plain []byte) ([]byte, error) {
	data := make([]byte, 13)
	data[0] = 1
	if _, err := rand.Read(data[1:]); err != nil {
		return nil, errors.New("生成加密随机数失败")
	}
	return c.aead.Seal(data, data[1:13], plain, nil), nil
}
func (c *Cipher) Decrypt(data []byte) ([]byte, error) {
	if len(data) < 29 || data[0] != 1 {
		return nil, errors.New("SSH 密码密文格式错误")
	}
	plain, err := c.aead.Open(nil, data[1:13], data[13:], nil)
	if err != nil {
		return nil, errors.New("SSH 密码解密失败，请检查主密钥及数据完整性")
	}
	return plain, nil
}
