package crypto

import (
	"bytes"
	"testing"
)

func TestPassphrasePurposeServerVersionAndNonce(t *testing.T) {
	cipher, _ := New(bytes.Repeat([]byte{3}, 32))
	first, err := cipher.EncryptPassphrase("server-a", []byte("口令 secret"))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := cipher.EncryptPassphrase("server-a", []byte("口令 secret"))
	if bytes.Equal(first, second) || first[0] != 2 || bytes.Contains(first, []byte("secret")) {
		t.Fatal("口令密文未隔离或 nonce 重复")
	}
	plain, err := cipher.DecryptPassphrase("server-a", first)
	if err != nil || string(plain) != "口令 secret" {
		t.Fatal(err)
	}
	if _, err = cipher.DecryptPassphrase("server-b", first); err == nil {
		t.Fatal("跨服务器口令被接受")
	}
	password, _ := cipher.Encrypt([]byte("secret"))
	for _, data := range [][]byte{password, {}, first[:20], append(bytes.Clone(first[:len(first)-1]), first[len(first)-1]^1)} {
		if _, err = cipher.DecryptPassphrase("server-a", data); err == nil {
			t.Fatal("错误密文被接受")
		}
	}
	if _, err = cipher.Decrypt(first); err == nil {
		t.Fatal("v2 被密码 v1 接受")
	}
	empty, err := cipher.EncryptPassphrase("server-a", nil)
	if err != nil || empty != nil {
		t.Fatal("空口令必须为无值")
	}
	wrong, _ := New(bytes.Repeat([]byte{4}, 32))
	if _, err = wrong.DecryptPassphrase("server-a", first); err == nil {
		t.Fatal("错误主密钥被接受")
	}
}
