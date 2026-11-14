-- 服务器配置；密码只存版本化密文，信任绑定当前 Host/Port。
CREATE TABLE servers (
 id TEXT NOT NULL PRIMARY KEY, -- 32 位小写十六进制随机 ID
 name TEXT NOT NULL, -- 展示名称，不设唯一约束
 host TEXT NOT NULL, -- 规范化 Host
 port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
 username TEXT NOT NULL,
 password_encrypted BLOB NOT NULL, -- 版本字节 + nonce + AES-GCM 密文及标签
 group_name TEXT NOT NULL DEFAULT '', -- 空字符串表示未分组
 remark TEXT NOT NULL DEFAULT '',
 trusted_host TEXT, -- 五个信任字段同时为空或同时非空
 trusted_port INTEGER,
 host_key BLOB, -- 完整 SSH 公钥 wire 字节
 host_key_algorithm TEXT,
 host_key_fingerprint TEXT, -- OpenSSH SHA256 展示指纹
 created_at INTEGER NOT NULL, -- UTC Unix 毫秒
 updated_at INTEGER NOT NULL, -- UTC Unix 毫秒
 CHECK (
  (trusted_host IS NULL AND trusted_port IS NULL AND host_key IS NULL
   AND host_key_algorithm IS NULL AND host_key_fingerprint IS NULL)
  OR
  (trusted_host IS NOT NULL AND trusted_port IS NOT NULL AND host_key IS NOT NULL
   AND host_key_algorithm IS NOT NULL AND host_key_fingerprint IS NOT NULL
   AND trusted_host = host AND trusted_port = port AND trusted_port BETWEEN 1 AND 65535)
 )
);
CREATE INDEX idx_servers_group_name_name ON servers(group_name, name);

-- 仅保存会话元数据；运行事实在内存，不能据此恢复 SSH。
CREATE TABLE sessions (
 id TEXT NOT NULL PRIMARY KEY,
 server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 name TEXT NOT NULL, -- 以下目标信息固定于创建时，不跟随服务器编辑
 host TEXT NOT NULL,
 username TEXT NOT NULL,
 port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
 status TEXT NOT NULL CHECK (status IN ('CONNECTING','CONNECTED','DISCONNECTED','CLOSING','CLOSED')),
 created_at INTEGER NOT NULL, -- UTC Unix 毫秒
 disconnected_at INTEGER, -- 以下可空时间均为 UTC Unix 毫秒
 last_attached_at INTEGER,
 closed_at INTEGER,
 close_reason TEXT CHECK (close_reason IN ('user_closed','idle_timeout','ssh_exit','ssh_error',
  'keepalive_timeout','server_shutdown','create_failed','process_restarted','counter_exhausted'))
);
CREATE INDEX idx_sessions_server_id_status ON sessions(server_id, status);

-- 单管理员；阶段 2 负责初始化及 Argon2id 验证。
CREATE TABLE admin_credentials (
 id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
 password_hash TEXT NOT NULL, -- Argon2id PHC 字符串
 created_at INTEGER NOT NULL, -- UTC Unix 毫秒
 updated_at INTEGER NOT NULL -- UTC Unix 毫秒
);

-- 每次登录独立记录；Cookie 原值永不进入数据库。
CREATE TABLE web_auth_sessions (
 id TEXT NOT NULL PRIMARY KEY,
 token_hash BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
 created_at INTEGER NOT NULL, -- UTC Unix 毫秒
 expires_at INTEGER NOT NULL CHECK (expires_at > created_at) -- UTC Unix 毫秒
);
CREATE INDEX idx_web_auth_sessions_expires_at ON web_auth_sessions(expires_at);
