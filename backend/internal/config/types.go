package config

// 配置字段及数值单位与核心文档第 7 章保持一致，时间在使用边界转换。
type Config struct {
	Server    Server    `yaml:"server"`
	Auth      Auth      `yaml:"auth"`
	Session   Session   `yaml:"session"`
	Ssh       Ssh       `yaml:"ssh"`
	Websocket Websocket `yaml:"websocket"`
	Terminal  Terminal  `yaml:"terminal"`
	Database  Database  `yaml:"database"`
	Logging   Logging   `yaml:"logging"`
}

type Server struct {
	Host               string   `yaml:"host"`
	Port               int64    `yaml:"port"`
	PublicOrigin       string   `yaml:"public_origin"`
	TrustedProxies     []string `yaml:"trusted_proxies"`
	ReadHeaderTimeout  int64    `yaml:"read_header_timeout"`
	RequestTimeout     int64    `yaml:"request_timeout"`
	ShutdownTimeout    int64    `yaml:"shutdown_timeout"`
	MaxRequestBodySize int64    `yaml:"max_request_body_size"`
}

type Auth struct {
	SessionTtl      int64 `yaml:"session_ttl"`
	CookieSecure    bool  `yaml:"cookie_secure"`
	LoginRateWindow int64 `yaml:"login_rate_window"`
	LoginRateLimit  int64 `yaml:"login_rate_limit"`
	MaxSessions     int64 `yaml:"max_sessions"`
}

type Session struct {
	IdleTimeout            int64 `yaml:"idle_timeout"`
	MaxSessionsPerServer   int64 `yaml:"max_sessions_per_server"`
	OutputBufferSize       int64 `yaml:"output_buffer_size"`
	HistoryLimit           int64 `yaml:"history_limit"`
	BindingCredentialTtl   int64 `yaml:"binding_credential_ttl"`
	BindingCredentialLimit int64 `yaml:"binding_credential_limit"`
	OperationWaitTimeout   int64 `yaml:"operation_wait_timeout"`
}

type Ssh struct {
	ConnectTimeout       int64 `yaml:"connect_timeout"`
	OperationTimeout     int64 `yaml:"operation_timeout"`
	KeepaliveInterval    int64 `yaml:"keepalive_interval"`
	KeepaliveTimeout     int64 `yaml:"keepalive_timeout"`
	KeepaliveMaxFailures int64 `yaml:"keepalive_max_failures"`
	HostKeyProbeTtl      int64 `yaml:"host_key_probe_ttl"`
	HostKeyProbeLimit    int64 `yaml:"host_key_probe_limit"`
}

type Websocket struct {
	MaxConnections        int64 `yaml:"max_connections"`
	ControlQueueLimit     int64 `yaml:"control_queue_limit"`
	MaxControlMessageSize int64 `yaml:"max_control_message_size"`
	MaxInputMessageSize   int64 `yaml:"max_input_message_size"`
	OutputChunkSize       int64 `yaml:"output_chunk_size"`
	BindTimeout           int64 `yaml:"bind_timeout"`
	MessageReadTimeout    int64 `yaml:"message_read_timeout"`
	WriteTimeout          int64 `yaml:"write_timeout"`
	CloseTimeout          int64 `yaml:"close_timeout"`
	PingInterval          int64 `yaml:"ping_interval"`
	PongTimeout           int64 `yaml:"pong_timeout"`
}

type Terminal struct {
	MaxCols              int64 `yaml:"max_cols"`
	MaxRows              int64 `yaml:"max_rows"`
	WriteQueueBytes      int64 `yaml:"write_queue_bytes"`
	WriteDrainTimeout    int64 `yaml:"write_drain_timeout"`
	ReconnectMaxAttempts int64 `yaml:"reconnect_max_attempts"`
	ReconnectDelay       int64 `yaml:"reconnect_delay"`
	ReconnectStableTime  int64 `yaml:"reconnect_stable_time"`
}

type Database struct {
	Path        string `yaml:"path"`
	BusyTimeout int64  `yaml:"busy_timeout"`
}

type Logging struct {
	Level string `yaml:"level"`
}
