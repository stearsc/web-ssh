package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed defaults.yaml
var defaultYAML []byte

func Defaults() Config {
	var c Config
	if err := yaml.Unmarshal(defaultYAML, &c); err != nil {
		panic("内置默认配置损坏")
	}
	return c
}

func Load(filename string) (Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置文件失败: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) (Config, error) {
	c := Defaults()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil && err != io.EOF {
		return c, fmt.Errorf("配置 YAML 语法错误")
	}
	if len(doc.Content) > 0 {
		if err := checkNode(doc.Content[0], reflect.TypeFor[Config](), ""); err != nil {
			return c, err
		}
		if err := doc.Decode(&c); err != nil {
			return c, fmt.Errorf("配置整数超出可表示范围")
		}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("配置只允许一个 YAML 文档")
	}
	return c, c.Validate()
}

// 先按节点类型校验，避免 YAML 将字符串数字、null 或浮点数隐式转换。
func checkNode(n *yaml.Node, t reflect.Type, prefix string) error {
	fail := func(reason string) error { return fmt.Errorf("配置 %s: %s", strings.TrimSuffix(prefix, "."), reason) }
	if t.Kind() == reflect.Struct {
		if n.Kind != yaml.MappingNode || n.Tag != "!!map" {
			return fail("必须是对象")
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fields[f.Tag.Get("yaml")] = f.Type
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			name := key.Value
			ft, ok := fields[name]
			if key.Tag != "!!str" || !ok {
				return fmt.Errorf("配置 %s%s: 未知配置项", prefix, name)
			}
			if seen[name] {
				return fmt.Errorf("配置 %s%s: 重复配置项", prefix, name)
			}
			seen[name] = true
			if err := checkNode(n.Content[i+1], ft, prefix+name+"."); err != nil {
				return err
			}
		}
		return nil
	}
	switch t.Kind() {
	case reflect.String:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
			return fail("必须是字符串")
		}
	case reflect.Bool:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
			return fail("必须是布尔值")
		}
	case reflect.Int64:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
			return fail("必须是整数")
		}
		var value int64
		if err := n.Decode(&value); err != nil {
			return fail("整数溢出")
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return fail("必须是字符串数组")
		}
		for _, item := range n.Content {
			if err := checkNode(item, t.Elem(), prefix); err != nil {
				return err
			}
		}
	default:
		return fail("不支持的配置类型")
	}
	return nil
}

func (c Config) Validate() error {
	// 数值先检查容量和 duration 溢出，再在使用时转换，避免先乘法再检查。
	maxInt := int64(^uint(0) >> 1)
	seconds := map[string]bool{}
	for _, key := range strings.Fields(`http.credential_timeout server.read_header_timeout server.request_timeout server.shutdown_timeout auth.login_rate_window session.binding_credential_ttl session.operation_wait_timeout ssh.private_key_gc_grace_period ssh.connect_timeout ssh.operation_timeout ssh.keepalive_interval ssh.keepalive_timeout ssh.host_key_probe_ttl websocket.bind_timeout websocket.message_read_timeout websocket.write_timeout websocket.close_timeout websocket.ping_interval websocket.pong_timeout terminal.write_drain_timeout terminal.reconnect_delay terminal.reconnect_stable_time`) {
		seconds[key] = true
	}
	v := reflect.ValueOf(c)
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		group := v.Field(i)
		gt := group.Type()
		for j := 0; j < group.NumField(); j++ {
			f := group.Field(j)
			if f.Kind() != reflect.Int64 {
				continue
			}
			key := t.Field(i).Tag.Get("yaml") + "." + gt.Field(j).Tag.Get("yaml")
			n := f.Int()
			if n <= 0 || n > maxInt {
				return fmt.Errorf("配置 %s: 必须是可表示的正整数", key)
			}
			unit := int64(0)
			if seconds[key] {
				unit = int64(time.Second)
			}
			switch key {
			case "auth.session_ttl":
				unit = int64(24 * time.Hour)
			case "session.idle_timeout":
				unit = int64(time.Minute)
			case "database.busy_timeout":
				unit = int64(time.Millisecond)
			}
			if unit > 0 && n > math.MaxInt64/unit {
				return fmt.Errorf("配置 %s: 时间溢出", key)
			}
		}
	}
	bad := func(field, reason string) error { return fmt.Errorf("配置 %s: %s", field, reason) }
	addr, err := netip.ParseAddr(c.Server.Host)
	if err != nil || addr.Zone() != "" {
		return bad("server.host", "必须是无端口的监听 IP")
	}
	if c.Server.Port > 65535 {
		return bad("server.port", "必须为 1～65535")
	}
	u, err := url.Parse(c.Server.PublicOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(c.Server.PublicOrigin, "?#") {
		return bad("server.public_origin", "必须为规范化的 scheme://host[:port]")
	}
	host := u.Hostname()
	ip, ipErr := netip.ParseAddr(host)
	if ipErr != nil {
		if len(host) > 253 || host != strings.ToLower(host) {
			return bad("server.public_origin", "主机名必须规范化")
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return bad("server.public_origin", "主机名非法")
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
					return bad("server.public_origin", "主机名非法")
				}
			}
		}
	} else if ip.Zone() != "" || ip.String() != host {
		return bad("server.public_origin", "IP 必须规范化")
	}
	expectedHost := host
	if ipErr == nil && ip.Is6() {
		expectedHost = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		p, e := strconv.Atoi(port)
		if e != nil || p < 1 || p > 65535 || strconv.Itoa(p) != port || (u.Scheme == "https" && p == 443) || (u.Scheme == "http" && p == 80) {
			return bad("server.public_origin", "端口非法或未规范化")
		}
		expectedHost = net.JoinHostPort(host, port)
	}
	if u.Host != expectedHost || c.Server.PublicOrigin != u.Scheme+"://"+expectedHost {
		return bad("server.public_origin", "来源未规范化")
	}
	if c.Auth.CookieSecure {
		if u.Scheme != "https" {
			return bad("server.public_origin", "Secure Cookie 要求 HTTPS")
		}
	} else if !addr.IsLoopback() || u.Scheme != "http" || !(host == "localhost" || ipErr == nil && ip.IsLoopback()) {
		return bad("auth.cookie_secure", "关闭 Secure 仅允许 HTTP 环回开发环境")
	}
	for _, cidr := range c.Server.TrustedProxies {
		if _, e := netip.ParsePrefix(cidr); e != nil {
			return bad("server.trusted_proxies", "CIDR 非法")
		}
	}
	if c.Server.RequestTimeout < c.Ssh.ConnectTimeout {
		return bad("server.request_timeout", "不得小于 ssh.connect_timeout")
	}
	if c.Server.MaxRequestBodySize < 1024 {
		return bad("server.max_request_body_size", "至少 1024 字节")
	}
	if c.Auth.SessionTtl > 365 {
		return bad("auth.session_ttl", "不得超过 365 天")
	}
	if c.Session.BindingCredentialTtl < c.Websocket.BindTimeout {
		return bad("session.binding_credential_ttl", "不得小于 websocket.bind_timeout")
	}
	if c.Websocket.MaxControlMessageSize < 1024 {
		return bad("websocket.max_control_message_size", "至少 1024 字节")
	}
	if c.Websocket.OutputChunkSize > c.Session.OutputBufferSize || c.Websocket.OutputChunkSize > c.Terminal.WriteQueueBytes {
		return bad("websocket.output_chunk_size", "不得超过输出缓存或终端队列")
	}
	if c.Terminal.MaxCols > 65535 {
		return bad("terminal.max_cols", "不得超过 65535")
	}
	if c.Terminal.MaxRows > 65535 {
		return bad("terminal.max_rows", "不得超过 65535")
	}
	if c.Terminal.ReconnectMaxAttempts > 10 {
		return bad("terminal.reconnect_max_attempts", "不得超过 10")
	}
	if c.Terminal.ReconnectStableTime < c.Terminal.ReconnectDelay {
		return bad("terminal.reconnect_stable_time", "不得小于 reconnect_delay")
	}
	if strings.TrimSpace(c.Database.Path) == "" || strings.ContainsRune(c.Database.Path, 0) || c.Database.Path == ":memory:" {
		return bad("database.path", "必须为非空持久化文件路径")
	}
	// SQLite C API 使用有符号 32 位毫秒值；禁止驱动截断忙等待时间。
	if c.Database.BusyTimeout > math.MaxInt32 {
		return bad("database.busy_timeout", "超过 SQLite 毫秒上限")
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return bad("logging.level", "必须为 debug/info/warn/error")
	}
	client := map[string]int64{"websocket.max_control_message_size": c.Websocket.MaxControlMessageSize, "websocket.max_input_message_size": c.Websocket.MaxInputMessageSize, "websocket.output_chunk_size": c.Websocket.OutputChunkSize, "terminal.write_queue_bytes": c.Terminal.WriteQueueBytes}
	for key, n := range client {
		if n > 9007199254740991 {
			return bad(key, "超过 JavaScript 安全整数范围")
		}
	}
	timers := map[string]int64{"http.credential_timeout": c.HTTP.CredentialTimeout, "websocket.bind_timeout": c.Websocket.BindTimeout, "session.operation_wait_timeout": c.Session.OperationWaitTimeout, "websocket.write_timeout": c.Websocket.WriteTimeout, "terminal.write_drain_timeout": c.Terminal.WriteDrainTimeout, "terminal.reconnect_delay": c.Terminal.ReconnectDelay, "terminal.reconnect_stable_time": c.Terminal.ReconnectStableTime}
	for key, n := range timers {
		if n > math.MaxInt32/1000 {
			return bad(key, "超过浏览器计时器范围")
		}
	}
	if c.Websocket.BindTimeout+c.Session.OperationWaitTimeout+c.Websocket.WriteTimeout > math.MaxInt32/1000 {
		return bad("websocket.bind_timeout + session.operation_wait_timeout + websocket.write_timeout", "合计超过浏览器计时器范围")
	}
	return nil
}
