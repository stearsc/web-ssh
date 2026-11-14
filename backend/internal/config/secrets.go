package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// 校验唯一密钥来源和规范格式；应用将结果交给 crypto，随后清除临时字节。
func LoadEncryptionKey() ([]byte, error) {
	value, direct := os.LookupEnv("APP_ENCRYPTION_KEY")
	filename, file := os.LookupEnv("APP_ENCRYPTION_KEY_FILE")
	if direct == file {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY 与 APP_ENCRYPTION_KEY_FILE 必须且只能设置一个")
	}
	if file {
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("APP_ENCRYPTION_KEY_FILE: 无法读取密钥文件")
		}
		value = strings.TrimSpace(string(data))
	}
	key, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != value {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY: 必须为标准 Base64 编码的 32 字节密钥")
	}
	return key, nil
}
