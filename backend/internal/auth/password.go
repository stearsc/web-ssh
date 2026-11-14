package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

func HashPassword(password string) (string, error) {
	if len(password) < 8 || len(password) > 1024 {
		return "", errors.New("APP_ADMIN_PASSWORD 必须为 8～1024 UTF-8 字节")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("生成密码盐失败")
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 65536, 1, 32)
	defer clear(hash)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

// 限制存储参数的资源范围，损坏的 PHC 不能触发无界分配。
func parseHash(phc string) (uint32, uint32, uint8, []byte, []byte, error) {
	bad := errors.New("管理员密码哈希格式无效")
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return 0, 0, 0, nil, nil, bad
	}
	var memory, iterations uint32
	var parallel uint8
	n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallel)
	if err != nil || n != 3 || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, iterations, parallel) || memory < 8*uint32(parallel) || memory > 262144 || iterations < 1 || iterations > 10 || parallel < 1 || parallel > 16 {
		return 0, 0, 0, nil, nil, bad
	}
	salt, e1 := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	hash, e2 := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(hash) != 32 {
		return 0, 0, 0, nil, nil, bad
	}
	return memory, iterations, parallel, salt, hash, nil
}
func VerifyPassword(phc, password string) bool {
	if len(password) > 1024 {
		return false
	}
	m, t, p, salt, want, err := parseHash(phc)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	defer clear(got)
	return subtle.ConstantTimeCompare(got, want) == 1
}
