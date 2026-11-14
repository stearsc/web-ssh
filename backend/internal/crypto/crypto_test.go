package crypto

import (
	"bytes"
	"testing"
)

func TestRoundTripAndTamper(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	c, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.Encrypt([]byte("密碼 ssh-secret"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Encrypt([]byte("密碼 ssh-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a[1:13], b[1:13]) {
		t.Fatal("nonce 复用")
	}
	plain, err := c.Decrypt(a)
	if err != nil || string(plain) != "密碼 ssh-secret" {
		t.Fatal(err)
	}
	for _, index := range []int{0, 1, 12, 13, len(a) - 1} {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			bad := bytes.Clone(a)
			bad[index] ^= 1
			if _, err := c.Decrypt(bad); err == nil {
				t.Fatal("篡改未拒绝")
			}
		})
	}
	other, _ := New(bytes.Repeat([]byte{8}, 32))
	if _, err := other.Decrypt(a); err == nil {
		t.Fatal("错误密钥未拒绝")
	}
	for _, size := range []int{0, 1, 12, 28} {
		if _, err := c.Decrypt(a[:size]); err == nil {
			t.Fatal("错误长度未拒绝")
		}
	}
	if _, err := New(nil); err == nil {
		t.Fatal("缺失密钥未拒绝")
	}
}
