# 6-K 私钥部署与受控停机备份恢复

适用于 schema 1+2 的 6-K 程序。首版只有离线命令，无在线备份 API、保留份数/天数默认策略或主密钥轮换工具。备份和恢复必须由运维安排维护窗口；会终止现有 SSH Session，重启不会恢复连接。

## 存储与权限

- SQLite 在配置 `database.path`，整个父目录包含 DB/WAL/SHM 一同持久化。
- 私钥固定在 `/var/lib/web-ssh/ssh-keys`；目录 0700、普通文件 0600，属主须为实际运行用户；Docker 使用 UID/GID `10001:10001`。每层目录均拒绝符号链接，macOS 开发也须使用真实目录。
- 文件为上传原始字节；无口令文件是敏感明文私钥。不会返回浏览器或通过静态资源提供下载；私钥原始字节不使用 APP 主密钥加密。
- Dockerfile 创建并赋权镜像内目录；Compose 默认独立命名卷 `ssh-keys`。首次空卷由 Docker 复制镜像目录属性，已有卷仍须人工核对。改用 bind mount 时先在宿主创建目录、设置 10001 属主和 0700；挂载会遮盖镜像权限，不能依赖镜像 chown 修复已有宿主目录。不要切 root 或 chmod 777。
- 父目录、ACL、宿主账号、其他容器和备份账号也须限制访问。当前环境未执行 Docker 构建、重建或宿主 ACL 验收。

原生进程由部署管理员预建该目录并赋给进程用户；程序不递归 chown。目录缺失、不可用或权限不正确时，密码认证仍可用，私钥新增/替换和 GC 暂停。单文件缺失/损坏只影响关联凭据，管理资料和重新上传仍可用。

唯一新增配置：

```yaml
ssh:
  # 孤儿对象候选登记后的宽限期，单位秒，正整数。
  private_key_gc_grace_period: 86400
```

周期固定 10 分钟，每轮最多检查 100、尝试删除 20、1 秒软预算；不会强制中断已开始的文件操作。按候选登记时刻计时，重启不重置。未知文件和没有创建归属证据的残留保留；不提供自动“清空目录”功能。

## 备份

1. 停止入口流量，禁止自动重启/滚动更新，停止进程，等待完整退出和 DB 关闭。Docker 使用 `docker compose stop web-ssh` 并确认容器已停止。不要删除命名卷或 SQLite WAL。
2. 使用同一版本二进制、相同 DB/私钥挂载和 UID 运行离线 backup；源数据库应为静止状态。匹配主密钥通过原有 `APP_ENCRYPTION_KEY` 或 `APP_ENCRYPTION_KEY_FILE` 二选一注入，不能放命令行参数。
3. 目标目录必须不存在，父目录受限且空间充足。示例：

```bash
umask 077
./backend/bin/web-ssh -operation backup -config ./config.yaml -recovery-set /secure-backups/web-ssh-20261120
./backend/bin/web-ssh -operation verify -recovery-set /secure-backups/web-ssh-20261120
```

Docker 可用停止服务后的一次性同镜像容器，挂相同数据库/私钥卷、只读配置、主密钥来源和受限备份父目录，执行相同参数。备份目标也必须允许 UID10001 写入。具体宿主路径由运维指定，本文没有执行远程命令。

服务、backup、restore 使用 `<database.path>.maintenance.lock` 的排他 flock，覆盖开库、复制和关库全过程。不要删除锁文件或让另一进程用不同路径绕过锁；锁不能阻止旧版本程序和外部 SQLite 工具，停机与禁自动重启仍是操作前提。源 DB 路径不得以别名/符号链接拼成第二把锁。

成功恢复集包含：

```text
manifest.json                  # 格式1、程序构建标识、schema [1,2]、对象引用、大小及 SHA256
database/web-ssh.db             # 静止 DB
database/web-ssh.db-wal         # 如源仍存在，原样复制；不能漏掉
ssh-keys/<object-id>.key        # 该 DB 引用的全部私钥副本
secrets/app-master-key          # 匹配的 Base64 APP 主密钥，0600
secrets/custody.txt             # 独立保管说明
```

目录 0700、文件 0600。校验包括 SQLite integrity_check/foreign_key_check、结构版本、全部文件大小/摘要、DB 与对象清单一致性、历史密码/Passphrase 解密及私钥解析。没有历史数据库密文时 manifest 明确“无法验证历史匹配性”，原始私钥不充当主密钥验证材料。

备份集自身含主密钥与原始私钥，必须作为敏感恢复材料限制存储和传输；另将主密钥保存在独立受限保管处。SHA256 用于检测意外损坏，不能证明清单和内容未被共同恶意改写。备份保留数量、天数和介质加密由运维制定。

任一引用文件缺失/损坏、主密钥不能解密、权限/空间错误会拒绝完成；失败可能留下没有有效 manifest 的受限目标，不能将它标记为可恢复备份，也不能用当前生产文件补齐历史归档。

## 校验与恢复

`verify` 只使用恢复集自身的 DB、主密钥和私钥副本，拒绝未登记的非空 WAL，不读取配置或 production 私钥目录。校验会由 SQLite 在恢复集内建立必要的临时 SHM；恢复集必须位于受限本地可读写工作副本。不要直接在只读介质上声称已完成校验。

1. 停止入口和服务进程，确认 DB 关闭并禁自动重启。
2. 保留当前生产数据，准备全新的 DB 目标和空的 0700 私钥目录。restore 拒绝覆盖已有 DB/WAL/SHM 或非空私钥目录。
3. 恢复配置中的 `database.path` 指向新 DB 目标，私钥目标始终为固定专用路径；在隔离容器/宿主准备该空卷。使用匹配版本执行：

```bash
./backend/bin/web-ssh -operation verify -recovery-set /secure-backups/web-ssh-20261120
./backend/bin/web-ssh -operation restore -config ./restore-config.yaml -recovery-set /secure-backups/web-ssh-20261120
```

4. restore 先完整验证恢复集，再复制 DB/WAL 与引用私钥，重新登记目标文件的 device/inode；凭据密文和私钥原始字节保持不变；再对恢复目标执行数据库完整性、实际文件摘要及秘密校验，同步目标目录后才报告成功。失败目标不得启动，须保留诊断并另选全新目标重试。
5. 将该恢复集的 `secrets/app-master-key` 通过原有环境变量或受限密钥文件注入，然后启动服务。restore 不擅自覆盖外部主密钥配置，也不生成新密钥。不要同时设置两个主密钥来源。
6. 在恢复目标核对管理列表、Host Key、两种认证的测试与真实终端；确认后再开放流量。重启会把旧 Session 元数据关闭为 process_restarted。

历史恢复不能借用“当前 production 私钥目录”。本轮隔离测试已将源目录移走后完成 verify、restore 和原凭据真实 SSH 认证；真实 Docker、远程生产恢复仍待运维验收。
