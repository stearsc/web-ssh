package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultsAndExample(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, Defaults()) {
		t.Fatal("缺项未使用默认值")
	}
	example, err := os.ReadFile("../../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(example) != string(defaultYAML) {
		t.Fatal("示例与内置默认值不一致")
	}
	if c.Server.Port != 8080 || c.Session.IdleTimeout != 120 || c.Database.BusyTimeout != 5000 {
		t.Fatal(c)
	}
	c, err = Parse([]byte("server:\n  port: 9000\n"))
	if err != nil || c.Server.Port != 9000 || c.Server.RequestTimeout != 30 {
		t.Fatal(c, err)
	}
}

func TestStrictConfig(t *testing.T) {
	cases := []struct{ name, body, field string }{
		{"未知键", "server:\n  typo: 1", "server.typo"},
		{"重复键", "server:\n  port: 1\n  port: 2", "server.port"},
		{"字符串数字", "server:\n  port: '8080'", "server.port"},
		{"浮点数", "server:\n  port: 8080.0", "server.port"},
		{"null", "server:\n  port: null", "server.port"},
		{"null对象", "server: null", "server"},
		{"布尔类型", "auth:\n  cookie_secure: 'false'", "auth.cookie_secure"},
		{"CIDR类型", "server:\n  trusted_proxies: [1]", "server.trusted_proxies"},
		{"零", "session:\n  history_limit: 0", "session.history_limit"},
		{"负数", "ssh:\n  connect_timeout: -1", "ssh.connect_timeout"},
		{"int溢出", "session:\n  history_limit: 9223372036854775808", "session.history_limit"},
		{"duration溢出", "session:\n  idle_timeout: 9223372036854775807", "session.idle_timeout"},
		{"端口", "server:\n  port: 65536", "server.port"},
		{"监听IP", "server:\n  host: localhost", "server.host"},
		{"非法CIDR", "server:\n  trusted_proxies: [localhost]", "server.trusted_proxies"},
		{"路径", "server:\n  public_origin: https://example.com/path", "server.public_origin"},
		{"密码来源", "server:\n  public_origin: https://user:secret@example.com", "server.public_origin"},
		{"HTTP安全Cookie", "server:\n  public_origin: http://localhost:8080", "server.public_origin"},
		{"非环回开发", "auth:\n  cookie_secure: false", "auth.cookie_secure"},
		{"请求预算", "server:\n  request_timeout: 10", "server.request_timeout"},
		{"请求大小", "server:\n  max_request_body_size: 1000", "server.max_request_body_size"},
		{"登录天数", "auth:\n  session_ttl: 366", "auth.session_ttl"},
		{"凭证预算", "session:\n  binding_credential_ttl: 1", "session.binding_credential_ttl"},
		{"控制大小", "websocket:\n  max_control_message_size: 10", "websocket.max_control_message_size"},
		{"输出块", "websocket:\n  output_chunk_size: 1048577", "websocket.output_chunk_size"},
		{"队列", "terminal:\n  write_queue_bytes: 1", "websocket.output_chunk_size"},
		{"列数", "terminal:\n  max_cols: 65536", "terminal.max_cols"},
		{"次数", "terminal:\n  reconnect_max_attempts: 11", "terminal.reconnect_max_attempts"},
		{"稳定期", "terminal:\n  reconnect_stable_time: 1", "terminal.reconnect_stable_time"},
		{"空路径", "database:\n  path: ''", "database.path"},
		{"内存路径", "database:\n  path: ':memory:'", "database.path"},
		{"日志", "logging:\n  level: trace", "logging.level"},
		{"客户端整数", "terminal:\n  write_queue_bytes: 9007199254740992", "terminal.write_queue_bytes"},
		{"客户端时间", "terminal:\n  write_drain_timeout: 2147484", "terminal.write_drain_timeout"},
		{"绑定合计", "websocket:\n  bind_timeout: 1000000\n  write_timeout: 1000000\nsession:\n  binding_credential_ttl: 1000000\n  operation_wait_timeout: 1000000", "合计"},
		{"多文档", "{}\n---\n{}", "一个"},
		{"根类型", "[]", "配置"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("期望 %s 错误，实际 %v", tc.field, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("错误泄露值")
			}
		})
	}
}

func TestLocalDevelopment(t *testing.T) {
	for _, origin := range []string{"http://localhost:5173", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		_, err := Parse([]byte("server:\n  host: '127.0.0.1'\n  public_origin: '" + origin + "'\nauth:\n  cookie_secure: false\n"))
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestLoadAndKey(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("读取失败未报告")
	}
	oldDirect, hasDirect := os.LookupEnv("APP_ENCRYPTION_KEY")
	oldFile, hasFile := os.LookupEnv("APP_ENCRYPTION_KEY_FILE")
	t.Cleanup(func() {
		if hasDirect {
			os.Setenv("APP_ENCRYPTION_KEY", oldDirect)
		} else {
			os.Unsetenv("APP_ENCRYPTION_KEY")
		}
		if hasFile {
			os.Setenv("APP_ENCRYPTION_KEY_FILE", oldFile)
		} else {
			os.Unsetenv("APP_ENCRYPTION_KEY_FILE")
		}
	})
	os.Unsetenv("APP_ENCRYPTION_KEY")
	os.Unsetenv("APP_ENCRYPTION_KEY_FILE")
	if _, err := LoadEncryptionKey(); err == nil {
		t.Fatal("允许缺失密钥")
	}
	os.Setenv("APP_ENCRYPTION_KEY", "bad-secret")
	if _, err := LoadEncryptionKey(); err == nil || strings.Contains(err.Error(), "bad-secret") {
		t.Fatal(err)
	}
	value := base64.StdEncoding.EncodeToString(make([]byte, 32))
	os.Setenv("APP_ENCRYPTION_KEY", value)
	if key, err := LoadEncryptionKey(); err != nil || len(key) != 32 {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(filename, []byte(value+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Setenv("APP_ENCRYPTION_KEY_FILE", filename)
	if _, err := LoadEncryptionKey(); err == nil {
		t.Fatal("允许两个来源")
	}
	os.Unsetenv("APP_ENCRYPTION_KEY")
	if _, err := LoadEncryptionKey(); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentCredentialTimeout(t *testing.T) {
	for _, body := range []string{"", "http: {}"} {
		c, err := Parse([]byte(body))
		if err != nil || c.HTTP.CredentialTimeout != 5 || c.Websocket.BindTimeout != 5 {
			t.Fatal(c, err)
		}
	}
	c, err := Parse([]byte("http:\n  credential_timeout: 2\nwebsocket:\n  bind_timeout: 7"))
	if err != nil || c.HTTP.CredentialTimeout != 2 || c.Websocket.BindTimeout != 7 {
		t.Fatal(c, err)
	}
	for _, value := range []string{"0", "-1", "null", "'5'", "1.5", "9223372036854775808", "9223372036854775807", "9007199254740992", "2147484"} {
		for _, key := range []string{"http:\n  credential_timeout: ", "websocket:\n  bind_timeout: "} {
			if _, err := Parse([]byte(key + value)); err == nil {
				t.Fatalf("accepted %s%s", key, value)
			}
		}
	}
}
