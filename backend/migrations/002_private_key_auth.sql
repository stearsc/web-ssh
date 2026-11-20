-- 私钥对象账本先于文件创建持久化；未知文件不能仅凭名称被回收。
CREATE TABLE private_key_objects (
 id TEXT NOT NULL PRIMARY KEY CHECK (length(id)=64 AND id NOT GLOB '*[^0-9a-f]*'),
 sha256 BLOB NOT NULL CHECK (length(sha256)=32), -- 原始字节完整性摘要
 byte_size INTEGER NOT NULL CHECK (byte_size BETWEEN 1 AND 65536), -- 原始文件字节数
 created_at INTEGER NOT NULL, -- UTC Unix 毫秒
 file_device INTEGER, -- 创建后登记的设备号；无归属证据时保留文件
 file_inode INTEGER, -- 创建后登记的 inode；与设备号联合防止误删替换文件
 state TEXT NOT NULL CHECK (state IN ('promoting','ready','delete_claimed','deleted')), -- 提升中/可用/删除声明/墓碑
 candidate_at INTEGER, -- 最后引用消失后的候选登记时间，UTC Unix 毫秒；重启不重置
 delete_attempts INTEGER NOT NULL DEFAULT 0 CHECK (delete_attempts>=0), -- 已执行删除尝试次数
 last_delete_attempt_at INTEGER, -- UTC Unix 毫秒
 last_delete_error TEXT -- 固定错误类别，禁止保存底层错误或秘密路径
);
CREATE INDEX idx_private_key_gc ON private_key_objects(state,candidate_at,last_delete_attempt_at);

-- 同一迁移事务完整保留父子数据，先移除子表，避免 DROP 父表触发级联丢失。
CREATE TABLE servers_6k_copy AS SELECT * FROM servers;
CREATE TABLE sessions_6k_copy AS SELECT * FROM sessions;
DROP TABLE sessions;
DROP TABLE servers;
CREATE TABLE servers (
 id TEXT NOT NULL PRIMARY KEY, -- 32 位小写十六进制随机 ID
 name TEXT NOT NULL, -- 展示名称，不设唯一约束
 host TEXT NOT NULL, -- 规范化 Host
 port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
 username TEXT NOT NULL,
 auth_type TEXT NOT NULL CHECK (auth_type IN ('password','private_key')), -- 唯一认证方式
 password_encrypted BLOB, -- password 模式的原有 v1 密文
 private_key_object_id TEXT REFERENCES private_key_objects(id), -- 应用私钥对象，禁止客户端指定
 passphrase_encrypted BLOB, -- private_key 的 v2 AES-GCM 密文；无口令为 NULL
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
  (auth_type='password' AND password_encrypted IS NOT NULL AND length(password_encrypted)>0
   AND private_key_object_id IS NULL AND passphrase_encrypted IS NULL)
  OR (auth_type='private_key' AND password_encrypted IS NULL AND private_key_object_id IS NOT NULL
   AND (passphrase_encrypted IS NULL OR length(passphrase_encrypted)>0))
 ),
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


-- 旧密码保持原字节，旧 Host Key、时间和 ID 全部保留。
INSERT INTO servers (id,name,host,port,username,auth_type,password_encrypted,group_name,remark,
 trusted_host,trusted_port,host_key,host_key_algorithm,host_key_fingerprint,created_at,updated_at)
SELECT id,name,host,port,username,'password',password_encrypted,group_name,remark,
 trusted_host,trusted_port,host_key,host_key_algorithm,host_key_fingerprint,created_at,updated_at
FROM servers_6k_copy;
INSERT INTO sessions SELECT * FROM sessions_6k_copy;
DROP TABLE sessions_6k_copy;
DROP TABLE servers_6k_copy;
