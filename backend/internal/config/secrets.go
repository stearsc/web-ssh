package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// 这里只校验启动密钥来源和格式；AES-GCM 与管理员初始化属于阶段 2。
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
