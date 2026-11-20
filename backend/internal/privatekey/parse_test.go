package privatekey

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/fault"
	"golang.org/x/crypto/ssh"
)

func TestSupportedPrivateKeyFormatsAndPassphrase(t *testing.T) {
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	type sample struct {
		name string
		raw  []byte
		pass string
	}
	cases := []sample{}
	for name, key := range map[string]any{"ed25519": ed, "ecdsa": ec, "rsa": rsaKey} {
		plain, err := ssh.MarshalPrivateKey(key, "")
		if err != nil {
			t.Fatal(err)
		}
		protected, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("新口令"))
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, sample{"openssh-" + name, pem.EncodeToMemory(plain), ""}, sample{"encrypted-openssh-" + name, pem.EncodeToMemory(protected), "新口令"})
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, sample{"pkcs8-" + name, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), ""})
	}
	ecDER, _ := x509.MarshalECPrivateKey(ec)
	for name, block := range map[string]*pem.Block{"pkcs1": {Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}, "sec1": {Type: "EC PRIVATE KEY", Bytes: ecDER}} {
		cases = append(cases, sample{name, pem.EncodeToMemory(block), ""})
		protected, err := x509.EncryptPEMBlock(rand.Reader, block.Type, block.Bytes, []byte("新口令"), x509.PEMCipherAES256)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, sample{"encrypted-" + name, pem.EncodeToMemory(protected), "新口令"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(t.Context(), c.raw, []byte(c.pass)); err != nil {
				t.Fatal(err)
			}
			if c.pass != "" {
				for _, bad := range []string{"", "wrong"} {
					if _, err := Parse(t.Context(), c.raw, []byte(bad)); err != fault.Error("PRIVATE_KEY_INVALID") {
						t.Fatal("错误口令被接受", err)
					}
				}
			}
		})
	}
	raw := cases[0].raw
	padded := append(append([]byte(nil), raw...), []byte(strings.Repeat("\n", MaxFileSize-len(raw)))...)
	if _, err := Parse(t.Context(), padded, nil); err != nil {
		t.Fatal("64 KiB 边界被拒绝", err)
	}
	for _, bad := range [][]byte{nil, []byte("not a private key"), append(padded, '\n'), pem.EncodeToMemory(&pem.Block{Type: "DSA PRIVATE KEY", Bytes: []byte{1}}), pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{1}})} {
		if _, err := Parse(t.Context(), bad, nil); err != fault.Error("PRIVATE_KEY_INVALID") {
			t.Fatal("非法格式被接受", err)
		}
	}
	if _, err := Parse(t.Context(), raw, []byte(strings.Repeat("中", 1366))); err != fault.Error("PRIVATE_KEY_INVALID") {
		t.Fatal("UTF-8 口令预算未检查")
	}
}

func TestRSAOversizePrecheckInPKCS1PKCS8AndOpenSSH(t *testing.T) {
	n := new(big.Int).Lsh(big.NewInt(1), 16384)
	der, err := asn1.Marshal(struct {
		Version                     int
		N, E, D, P, Q, DP, DQ, Qinv *big.Int
	}{0, n, big.NewInt(65537), big.NewInt(3), big.NewInt(3), big.NewInt(3), big.NewInt(1), big.NewInt(1), big.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, _ := asn1.Marshal(struct {
		Version    int
		Algorithm  pkix.AlgorithmIdentifier
		PrivateKey []byte
	}{0, pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}}, der})
	private := ssh.Marshal(struct {
		Check1, Check2      uint32
		Type                string
		N, E, D, Iqmp, P, Q *big.Int
		Comment             string
		Pad                 []byte `ssh:"rest"`
	}{7, 7, "ssh-rsa", n, big.NewInt(65537), big.NewInt(3), big.NewInt(1), big.NewInt(3), big.NewInt(3), "", nil})
	for len(private)%8 != 0 {
		private = append(private, byte(len(private)%8+1))
	}
	public := ssh.Marshal(struct {
		Type string
		E, N *big.Int
	}{"ssh-rsa", big.NewInt(65537), n})
	openSSH := append([]byte("openssh-key-v1\x00"), ssh.Marshal(struct {
		Cipher, KDF, Options string
		Count                uint32
		Public, Private      string
	}{"none", "none", "", 1, string(public), string(private)})...)
	for name, block := range map[string]*pem.Block{"pkcs1": {Type: "RSA PRIVATE KEY", Bytes: der}, "pkcs8": {Type: "PRIVATE KEY", Bytes: wrapped}, "openssh": {Type: "OPENSSH PRIVATE KEY", Bytes: openSSH}} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(t.Context(), pem.EncodeToMemory(block), nil); err != fault.Error("PRIVATE_KEY_INVALID") {
				t.Fatal("超大 RSA 被接受", err)
			}
		})
	}
}

func TestParsingCancellationHoldsSlotUntilActualComputationFinishes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	entered, finish, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := withParseSlot(ctx, func() (ssh.Signer, error) { close(entered); <-finish; return nil, nil })
		done <- err
	}()
	<-entered
	cancel()
	for range 5 {
		if _, err := withParseSlot(context.Background(), func() (ssh.Signer, error) { t.Error("忙时执行了计算"); return nil, nil }); err != fault.Error("PRIVATE_KEY_PARSE_BUSY") {
			t.Fatal(err)
		}
	}
	close(finish)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := withParseSlot(t.Context(), func() (ssh.Signer, error) { return nil, nil }); err != nil {
		t.Fatal("真实完成后未释放", err)
	}
}

func TestOpenSSHBcryptRoundsLimitAndRSAPrecheckBoundary(t *testing.T) {
	// rounds 超限在 KDF 前拒绝，不必执行 2048 轮昂贵计算来证明固定库的上限。
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("pass"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Cipher, KDF, Options string
		Count                uint32
		Public, Private      string
	}
	if err = ssh.Unmarshal(block.Bytes[len("openssh-key-v1\x00"):], &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Options = string(ssh.Marshal(struct {
		Salt   string
		Rounds uint32
	}{"salt", 2049}))
	raw := append([]byte("openssh-key-v1\x00"), ssh.Marshal(envelope)...)
	if _, err := parse(pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: raw}), []byte("pass")); err == nil || !strings.Contains(err.Error(), "rounds") {
		t.Fatal("超限 KDF 未拒绝", err)
	}
	boundary := new(big.Int).Lsh(big.NewInt(1), 16383)
	der, err := asn1.Marshal(struct {
		Version                     int
		N, E, D, P, Q, DP, DQ, Qinv *big.Int
	}{0, boundary, big.NewInt(65537), big.NewInt(3), big.NewInt(3), big.NewInt(3), big.NewInt(1), big.NewInt(1), big.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	if err = precheckRSA(der); err != nil {
		t.Fatal("16384 bit 结构预检边界拒绝", err)
	}
	// 边界仅验证低成本预检；不把无效数学参数误称为有效 16384 bit 私钥。
	multiPrime, err := rsa.GenerateMultiPrimeKey(rand.Reader, 3, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(t.Context(), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(multiPrime)}), nil); err != nil {
		t.Fatal("合法多素数私钥被拒绝", err)
	}
}

func TestRealBcryptCancellationAndDeadlineDoNotReleaseRunningSlot(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("pass"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Cipher, KDF, Options string
		Count                uint32
		Public, Private      string
	}
	if err = ssh.Unmarshal(block.Bytes[len("openssh-key-v1\x00"):], &envelope); err != nil {
		t.Fatal(err)
	}
	var options struct {
		Salt   string
		Rounds uint32
	}
	if err = ssh.Unmarshal([]byte(envelope.Options), &options); err != nil {
		t.Fatal(err)
	}
	// 保持真实密文结构，只提高到仍在固定库许可范围内的轮数，确实执行同步 bcrypt。
	options.Rounds = 64
	envelope.Options = string(ssh.Marshal(options))
	raw := pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: append([]byte("openssh-key-v1\x00"), ssh.Marshal(envelope)...)})
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(t.Context(), 30*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(t.Context())
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := Parse(ctx, raw, []byte("pass")); done <- err }()
			deadline := time.Now().Add(3 * time.Second)
			for len(parseSlot) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if len(parseSlot) == 0 {
				t.Fatal("未进入实际解析")
			}
			if mode == "cancel" {
				cancel()
			}
			<-ctx.Done()
			if _, err := Parse(context.Background(), raw, []byte("pass")); err != fault.Error("PRIVATE_KEY_PARSE_BUSY") {
				t.Fatal("真实 KDF 完成前归还槽位", err)
			}
			if err := <-done; !errors.Is(err, ctx.Err()) {
				t.Fatal(err)
			}
		})
	}
}
