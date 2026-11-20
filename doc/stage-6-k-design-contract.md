# 第六阶段 6-K.0 实现契约

- 定稿日期：2026-11-20（Asia/Shanghai）。
- 用户已批准采用编码前报告的实现契约，并明确直接在 S6 分支继续，不创建功能分支。
- 基线：S6@56a346b；实现范围为完整 6-K 和必要认证表单，保持第五阶段状态机及 WebSocket 协议。
- 本文登记已确认的实现方案，不代表测试或部署完成。

## 1. 请求与错误契约（已确认）

复用服务器 POST/PUT，无新增 endpoint、upload token 或客户端对象 ID。

| 请求 | 完整行为 |
|---|---|
| JSON POST，未传 auth_type | password；要求非空 password |
| JSON PUT，未传 auth_type | 保留当前模式；私钥模式连空字符串 password 字段也拒绝 |
| JSON PUT，private_key 且未换文件 | 保留对象引用及 Passphrase 密文；禁止 passphrase 字段 |
| JSON PUT，private_key→password | 明确 auth_type=password 且提供非空新 password；清除私钥引用/口令须同事务提交 |
| multipart POST/PUT | metadata 明确 auth_type=private_key，且仅有 metadata/private_key 两个 part；禁止 password 字段 |
| multipart 替换 | passphrase 缺失/空字符串均按无口令尝试；绝不读取旧口令替新文件解锁 |
| JSON passphrase 或 null | 拒绝；不提供单独改口令，null 不替代省略或空字符串 |

输入应区分字段缺失和空字符串；不能用现有 `Password string` 直接判断 private_key 请求是否含 password。JSON 严格校验继续拒绝重复键、未知键、非 UTF-8、null 和类型错误。

part header 白名单：

- 两种 part 均只允许单次 `Content-Disposition`，以及最多单次 `Content-Type`；拒绝其他 header、重复 header 和 Content-Transfer-Encoding。
- metadata：Content-Disposition 为 form-data，name=metadata，filename 缺省或为空字符串（原生 FormData 的 JSON Blob 使用 filename=""）；非空 filename 拒绝；Content-Type=application/json，可选 charset=utf-8。
- private_key：name=private_key，必须为文件 part；filename 只用于验证文件 part 形态，不存储、不用于路径、不写日志；不将文件 MIME 或扩展名作为私钥格式判据。
- metadata 和文件的 part 顺序不影响合法性；读取结束还须验证只有两项、结束边界完整及完整请求总预算。
- headers 预算按原始 header 行字节累计，包含行结束符；在标准库归一化前限制并识别重复项。必须验证 Go 1.26.5 NextRawPart 的读前缓冲行为，不能只在解析完成后检查 map 长度冒充 8 KiB 硬预算。
- 使用受限 Reader 与 NextRawPart，不调用 ParseMultipartForm/ReadForm；发现任一超限拒绝，不把截断结果继续交给解析器。

新增错误名称登记如下，均是 HTTP 业务错误，不新增 WS 消息或关闭码：

| 场景 | code / HTTP |
|---|---|
| multipart/JSON 结构、互斥字段非法 | 既有 INVALID_REQUEST / 400 |
| 任何字节/数量预算超限 | 既有 MESSAGE_TOO_LARGE / 413；格式非法仍 INVALID_REQUEST |
| 上传接收名额满 | PRIVATE_KEY_UPLOAD_BUSY / 429 |
| 解析/KDF 名额满 | PRIVATE_KEY_PARSE_BUSY / 429 |
| 上传私钥不支持、损坏、口令不匹配、RSA 超限 | PRIVATE_KEY_INVALID / 400；提示格式或口令无效，不输出底层解析详情 |
| 已存单文件缺失、摘要/格式不正确、不可安全读取 | PRIVATE_KEY_UNAVAILABLE / 503；保留管理查询和替换能力 |
| 私钥卷不可用或无法安全写入 | PRIVATE_KEY_STORAGE_UNAVAILABLE / 503 |
| DB 确定回滚/不确定提交 | 沿用 STORAGE_ERROR / 500、SERVER_UNAVAILABLE / 503 |
| 远端拒绝用户认证 | 沿用 SSH_AUTH_FAILED / 502；提示改为适用两种认证的文字 |

以上 HTTP 错误在本次实现中注册，不进入 WebSocket 协议。

## 2. 数据及密文契约（已确认）

新增 `002_private_key_auth.sql`，不修改 001。servers 采用核心 §12.2 的 auth_type、可空密码密文/对象引用/口令密文及互斥 CHECK。

私钥元数据表名 `private_key_objects`：

| 字段 | 类型/用途 |
|---|---|
| id | TEXT PRIMARY KEY；应用随机 32 字节编码的对象 ID，客户端不能指定 |
| sha256 | BLOB，32 字节；原始文件摘要 |
| byte_size | INTEGER，1～65536；原始字节数 |
| created_at | INTEGER；UTC Unix 毫秒 |
| file_device / file_inode | INTEGER 可空；创建后登记设备/inode，防止误删替换文件；缺少归属证据时保留 |
| state | TEXT；promoting/ready/delete_claimed/deleted；配 CHECK |
| candidate_at | INTEGER 可空；本轮无引用候选的登记时间，跨重启不重置 |
| delete_attempts | INTEGER，非负；删除失败累计次数 |
| last_delete_attempt_at | INTEGER 可空；UTC Unix 毫秒 |
| last_delete_error | TEXT 可空；固定安全错误类别，不存底层错误/路径 |

存储名由 ID 推导，包含最终对象和专用暂存对象的固定后缀，无用户命名。持久化记录须先于任何文件创建；无可确认记录的文件一律当未知文件保留，不能凭随机外观删除。需给 candidate_at/state 建有界扫描索引。

重建迁移在一个事务中复制 servers 和 sessions，先完整保留 sessions 到新表/受控中间表，再按外键依赖顺序移除旧表并重建名称/索引，最后回填并执行 foreign_key_check；不在已有事务里假装关闭 foreign_keys。对全部列逐项比较，特别是密码 BLOB 原字节、Host Key、时间、Session ID/status/reason，以及 ON DELETE CASCADE。失败全部回滚。

Passphrase 密文：版本字节 `2` + 12 字节随机 nonce + AES-256-GCM ciphertext/tag。AAD 为 version byte + uint32 大端长度/purpose UTF-8 字节 + uint32 大端长度/server ID UTF-8 字节；purpose 固定 `web-ssh/private-key-passphrase`，拒绝未知版本或错误上下文。旧密码仍只读写 v1；新解密函数不 fallback。

无口令为 SQL NULL，不加密空串。任何已存口令解密失败统一返回启动错误“保存口令无法解密，可能是主密钥或数据问题”；不判断具体根因。全部历史密文均成功解密才可报告有历史匹配证据；若根本无密文，明确记录“无法验证历史匹配性”。

## 3. 文件同步、事务与回收（已确认）

### 3.1 同步边界和锁序

保留 State.mu→登录读锁的现有顺序；新增私钥存储同步边界，顺序为：服务器锁→必要的登录读锁→私钥存储锁→短 DB 事务。probe 索引锁不跨 DB/文件操作；GC 只取私钥存储锁，不反向取得服务器锁或登录锁。

所有新引用/lease、上传 promotion、事务保护与 GC claim 都通过同一存储实例准入。DB 查询游标必须关闭后才能等待其他业务锁，避免最大一条 SQLite 连接导致死锁。

文件 IO 放在短事务外。为避免全局锁被慢文件系统长期占用，claim 在锁内持久化并阻止新准入，之后才解锁执行删除；新引用/lease 见 claim 必须失败，不能越过 claim 使用对象。删除后重新进入同一边界登记结果。不能只有引用查询而没有准入排斥。

### 3.2 上传及提交三态

1. 有界内存完成格式/口令/Signer 验证；获得全局解析 slot 的同步函数结束后才释放，取消只标记结果不用，不提前归还 slot。
2. 应用生成对象 ID，先在 DB 登记 promoting（同时登记候选时刻作为失败清理后备），持久化成功才创建专用暂存文件。登记提交不确定且无法确认时不写文件。
3. 持上传保护，排他创建 0600 文件、完整写入/fsync、不可覆盖地提升、fsync 目录；保存的是原始字节。安全目录打开/相对文件操作和普通文件校验需防路径穿越、符号链接和卷替换。
4. 原服务器锁内，通过存储准入给新旧对象建立事务保护，短事务更新服务器引用与新旧对象候选时间（ready 在上传提升后先行提交）；保证切换两种凭据同事务提交。
5. Committed：发布完整新记录并失效相应 probe；解除事务保护；旧对象只有在全库无引用后才登记候选。
6. RolledBack：原记录不动，新对象登记候选；清理按安全机制进行，不假定响应失败即可删除文件。
7. Uncertain：在数据库事实确认前保护新旧对象并沿用 SERVER_UNAVAILABLE/reload。确认失败保留保护；后续仅在受控重载/对账确认引用事实后解除。进程重启时所有未完成对象经 DB 恢复及 reconciliation 重新分类，不能先 GC 后查库。

单进程实际存在但尚未完成的保护可只在内存；候选、promotion、delete claim 和删除失败记录必须持久化。崩溃后内存任务已不存在，不恢复 lease，而是依据数据库事实消除遗留状态。数据库不可读/结构异常时暂停全部删除。

### 3.3 Test 与 Creation

- Test 在固定全部认证字段和版本、释放 State.mu 之前取得独立文件 lease；不计 Session occupied；完成/失败/取消后释放。
- Creation 在 Reserve 固定快照时取得独立文件 lease；initialize 失败、未进入 Open 即取消、Open 所有出口都要可幂等释放，不能只在 Open 的 defer 中处理。
- 文件 lease 和 Session 名额 lease 分别拥有，不能通过提前释放 Session 名额来释放文件。
- SSH 公钥认证材料只在已通过 Host Key 校验的认证回调中提供；本地错误保存为明确类型，不用握手错误字符串将其改报 SSH_AUTH_FAILED。
- 上传校验、Test、Creation 使用同一个解析/KDF 准入实例，不为每个 Service/请求创建独立 semaphore。长期 Session、Terminal、probe 不持私钥/口令/Signer/文件 lease。

### 3.4 Reconciliation 和周期 GC

- 启动：完成迁移、历史数据库秘密验证、引用事实恢复后对账。promoting/deletion 遗留状态以 DB 引用为准；有引用但缺失/损坏只隔离关联凭据，保留引用和管理能力。
- 未知文件只保留/安全报告，不凭 ID 外观登记为可删除对象。卷打开/读目录/身份校验失败暂停删除，不按空目录处理。
- 从持久化 candidate_at 计算宽限；重复启动、删除失败不重置；对象重新被引用后取消当前候选，下次最后引用消失重新登记候选。
- 每 10 分钟取有序、有界候选，最多检查 100、删除 20；错误尝试同样消耗本轮删除工作预算，失败对象放到后续轮次；避免反复命中同一失败对象饿死其他候选。
- 单轮 1 秒软预算，启动下一项前检查；不声称可强停同步文件系统操作。
- claim 前在统一边界内同时验证全库引用、Test/Creation leases、promotion、事务不确定及恢复/备份保护、宽限；claim 后删除前用安全目录 fd 验证归属及普通文件类型，任何失败都保留对象。摘要用于读取/备份完整性；已知未完成上传可按创建时登记的 inode 回收。持久化 claim 失败或不确定时不执行删除。
- claim 成功后新引用/lease 准入被拒绝；文件操作返回后记录成功/失败及 attempts、时间、类别。后续周期重试；不记录完整路径或秘密。
- 删除完成保留必要 tombstone，避免相同 ID 的未知文件被误认为旧应用文件。卷不可用期间不把缺失结果当成功删除。

## 4. 受控备份与恢复（已确认）

首版只支持进程完全停止后的外部离线操作；不新增在线 API，不用单独 VACUUM INTO 替代完整恢复集。增加随程序构建的离线命令，与服务启动共用进程互斥机制，保护整个复制/恢复时段；仅“操作前检查一次 PID”不能保证进程不会并发重启。

恢复集布局：`database/`（静止 DB 及仍存在的 WAL）、`ssh-keys/`（该 DB 引用的全部对象）、`manifest.json`、受限的 `secrets/`。主密钥仅从既有注入渠道读取，不放命令行参数；匹配密钥材料与独立保管说明属于恢复集的一部分，运维还须独立安全保管，不输出密钥摘要以外的秘密。

manifest 明确格式版本、程序版本/构建标识、schema_migrations 版本、DB/文件长度与 SHA-256、对象 ID/引用清单、主密钥材料的保管位置说明；文件相对路径必须固定且受白名单约束。校验和用于完整性检测，不宣称能够抵抗归档与 manifest 同时被恶意篡改。

备份操作：先停止外部访问→停止服务进程并确认数据库关闭→独占离线锁→复制到新建 0700 目标→验证复制 DB 与全部引用对象、秘密解密→生成清单并同步→才标记恢复集完整。任一引用文件缺失/损坏则不能宣称得到完整备份；源数据保留，不修补、不生成假文件。

恢复操作：使用该备份自身的文件与主密钥；先校验清单、版本、摘要、引用及秘密解密→恢复至全新受限目标→完整校验后才由运维切换。拒绝覆盖运行中目标；不借用 production 私钥目录拼配旧 DB。原样文件缺失/损坏情况下不能宣称完整恢复验收成功；正常服务启动仍按单凭据故障隔离处理。

备份/恢复复制时禁止服务启动和 GC，等价于全局离线保护；维护锁覆盖服务和离线操作全程，运行中的存储不另设重复维护状态。备份会终止全部现有 SSH Session；不新增保留份数、天数、在线备份或 master key 轮换策略。

## 5. 推荐编码顺序与文件边界

批准开发基线和实现契约后，按完整纵向能力交付，不提交半套认证：

1. `backend/migrations/002_private_key_auth.sql`、`internal/database` 及迁移测试：互斥结构、安全升级及外键保留。
2. `internal/crypto`、`internal/config`、默认及示例配置：独立 Passphrase 格式、启动分类及唯一 GC 配置。
3. 新 `internal/privatekey`：解析资源准入、原始文件、持久化对象状态、lease/claim/对账/有界 sweep；无新增依赖服务。
4. `internal/server/{types,store,service,probe,session}.go`：完整快照、引用三态、短租约和 probe 安全字段；不改 Session/Binding 状态机。
5. `internal/httpapi/{management,request,response}.go`、`internal/sshclient/{test,diagnostic}.go`：受限 multipart、Host Key 门禁下的私钥认证、明确错误；不改 WS 协议。
6. `cmd/server` 及必要离线运维入口：启动对账、周期任务、统一退出、受控备份恢复工具及集成测试。
7. `frontend/src/api/management.ts`、`components/server/ServerForm.vue` 及认证必要接线/测试：JSON/FormData 两路径、认证选择、换文件清口令、普通编辑保留、已保存配置测试；不借此实现其余 6-3 UI。
8. Dockerfile、Compose、README 和新增运维说明：独立 `/var/lib/web-ssh/ssh-keys` 卷、UID/GID 10001:10001、目录 0700/文件 0600；宿主预建并授权，不用 root/0777 绕过。

保持不变：001 migration、第五阶段冻结设计/历史报告、WebSocket 协议及 Session/Binding/接管/关闭/登出语义。


## 验证与交付

按任务原文认证、资源、生命周期、启动/备份、前端矩阵验证；真实设备和 Docker 未验项目分别记录，不用模拟结果替代。新证据写入第六阶段目录，不覆盖历史报告。

实际离线命令为 `-operation backup|verify|restore -recovery-set DIR`；backup/restore 另使用 `-config FILE`，verify 不读取当前配置。锁文件为数据库路径加 `.maintenance.lock`。Restore 复制后只重登记新物理文件的 device/inode，不改私钥字节和凭据。操作细节见 [部署与恢复](stage-6-k-operations.md)。

原始请求先用 MaxBytesReader 限制并累计读取至 128 KiB 内存，再在原始字节上检查 headers，最后交给 NextRawPart；因此标准 MIME 解析器开始前已完成硬预算检查，无默认临时文件。不能把内存持有结束当解析任务已取消；上传名额持有至 handler 结束。
