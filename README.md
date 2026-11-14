# Web SSH

当前交付范围为 Stage 2：在已有工程、配置、SQLite 和 HTTP 基础上，实现单管理员登录、服务器管理、AES-256-GCM 密码保存、Host Key 人工信任与临时 SSH 认证测试。尚未实现 PTY、Shell、WebSocket、Session Manager 或终端界面。

设计依据见 `doc/` 下四份设计文档，验证结果见 [Stage 1 实施报告](doc/stage-1-implementation-report.md)与 [Stage 2 实施报告](doc/stage-2-implementation-report.md)。

## 工具与构建

使用 Go 1.26.5、Node 24（第二阶段验证 v24.21.0），前端依赖按锁文件安装。WebSocket 依赖按设计固定为 v1.8.15，本阶段尚未引用其业务 API；不要通过依赖整理擅自删除或升级该锁定项。

```bash
cd frontend
npm ci
npm test
npm run build
cd ../backend
go vet ./...
go test ./...
go build -trimpath -o bin/web-ssh ./cmd/server
```

前端构建输出到 `backend/web/dist`，随后由 `go:embed` 编入程序。必须先构建前端，不能使用开发服务器或占位资源代替生产构建；该目录和二进制按现有 `.gitignore` 忽略。运行时不需要 Node 或项目源文件。

涉及并发的验证还应在具有 C 编译器的环境执行：

```bash
cd backend
CGO_ENABLED=1 go test -race ./...
```

SQLite 使用纯 Go 驱动，正常构建不依赖 CGO；race 检测器自身需要 CGO/C 编译器。

## 配置和本地运行

复制 `config.example.yaml` 到本地 `config.yaml`。示例与嵌入默认配置一致，修改后重启生效；未知项、重复项、类型错误、非法范围及关联约束均拒绝启动。配置文件必须存在，文件内部缺项才使用默认值。

本机 HTTP 开发需显式修改：

```yaml
server:
  host: "127.0.0.1"
  port: 8080
  public_origin: "http://127.0.0.1:8080"
auth:
  cookie_secure: false
database:
  path: "./data/web-ssh.db"
```

保留其他默认项即可。数据库相对路径以进程当前目录为准，父目录不存在时以 0700 创建；目录必须位于可写的本地持久化文件系统。SQLite 忙等待受其有符号 32 位毫秒范围限制，超过范围的配置直接报错，不能截断。

密钥以 `APP_ENCRYPTION_KEY`（标准 Base64 编码的 32 字节）或 `APP_ENCRYPTION_KEY_FILE` 二选一注入。文件内容可带结尾换行；变量内容须为规范 Base64。不要把密钥写进 YAML、镜像或 Git。

```bash
# 从项目根目录运行；生成一次后应独立安全保存，不要每次启动替换。
export APP_ENCRYPTION_KEY="$(openssl rand -base64 32)"
# 仅首次初始化，交互读取，避免把管理员密码写进命令历史。
read -rsp "管理员初始密码: " APP_ADMIN_PASSWORD
export APP_ADMIN_PASSWORD
./backend/bin/web-ssh -config ./config.yaml
```

首次启动且管理员表为空时，`APP_ADMIN_PASSWORD` 必须为 8～1024 UTF-8 字节，按 Argon2id PHC 保存；之后不会用该变量覆盖已有哈希，可以移除变量。启动会验证所有已保存的 SSH 密码密文；密钥缺失、格式错误、错误密钥或密文损坏会阻止开放服务，不能重新生成密钥掩盖问题。

浏览器打开配置的 `public_origin` 登录。创建服务器须提供 SSH 密码；编辑时留空或省略密码保留原密文。测试连接只测试已经保存的配置，首次或 Host Key 变化时必须人工核对目标、算法及 SHA256 指纹，确认后会重新连接验证。取消确认不改变信任。服务器 Host/Port 变更清空原信任，用户名/密码变更使旧探测凭证失效。

健康检查：

```bash
curl http://127.0.0.1:8080/api/health
# {"code":0,"message":"success","data":{"status":"ready"}}
```

只有配置、密钥、数据库、迁移、管理员、现有密文及信任记录验证和历史元数据清理成功后才开放监听。未就绪/退出状态使用 HTTP 503、`SERVER_UNAVAILABLE` 和 `status: not_ready`。未知 API 使用既有 `INVALID_REQUEST` 错误，不回退成 HTML；未定义静态路径返回 404。

前端开发可在 `frontend/` 执行 `npm run dev`，Vite 的 `/api` 代理指向 `127.0.0.1:8080`。进行同源认证开发时，应显式将 `server.public_origin` 改为实际 Vite 来源（通常为 `http://127.0.0.1:5173`），仍保持环回监听和 `cookie_secure: false`。所有写接口（包括登录、登出）均要求精确匹配的 Origin；手工调用写 API 时也必须提供该请求头。

## SQLite 行为及备份

- 固定最大打开/空闲连接各 1。`database/sql/driver.Connector` 包装器在每次物理连接创建或重开时设置并回读 `foreign_keys=ON`、配置的 `busy_timeout`、`journal_mode=WAL`。
- 只创建设计规定的 `servers`、`sessions`、`admin_credentials`、`web_auth_sessions`、`schema_migrations`，以及规定的索引/约束；没有 ORM。
- SQL 嵌入二进制，按数字版本排序，在事务内执行 SQL 并登记 UTC Unix 毫秒时间。重复启动不重复应用，迁移失败回滚并阻止启动，程序不认识的迁移版本也拒绝启动，不删除或重建旧库。
- 启动将旧的非 CLOSED 元数据更新为 CLOSED/process_restarted，不恢复 SSH 或凭证。
- DB、WAL、SHM 共用持久化目录。运行中不能只复制 `.db` 或删除旁路文件。
- 已在 modernc.org/sqlite v1.62.0 上实际验证 `VACUUM INTO ?` 得到包含已提交数据的一致性备份。该驱动也提供 `NewBackup`，但本次没有验证该接口，不宣称它已验收；本阶段不新增备份 API 或后台任务。
- 停机备份应先停止访问并关闭进程，再复制整个静止数据目录（保留仍存在的 WAL）；数据库与密钥分别保存。

## Ubuntu 服务器上的 Docker 待验证操作

本地不安装 Docker，不运行以下命令。本阶段没有执行镜像构建或 Compose 测试，以下操作留给用户安排的 Ubuntu 部署环境。

```bash
docker build -t web-ssh:stage2 .
# 准备 config.yaml、.env、data/ 和已有的 global_net 网络后：
docker compose up -d --build
```

Dockerfile 使用 Node 构建前端、Go 嵌入并以 `CGO_ENABLED=0` 编译，最终镜像只包含运行程序。程序以 UID/GID `10001:10001` 运行，宿主机绑定的 `data/` 必须允许该用户写入，`config.yaml` 必须允许该用户读取。

Compose 将配置只读挂载，并持久化整个 `/data`。镜像不包含密钥；`.env` 不会由 Go 自动加载，Compose 用它进行环境变量替换，原生进程需显式导出环境变量。

`stop_grace_period: 30s` 必须严格大于 `server.shutdown_timeout`（默认 25 秒）。应用无法从 YAML 得知 Docker 的实际停止宽限期，部署时须同时核对两者。容器使用 exec 入口向 Go 主进程直接传递 SIGTERM。

正式环境的 `public_origin` 应为实际 HTTPS 来源，`cookie_secure` 保持 true。NPM 配置、实际 Docker/NPM 部署及远程验收仍由 ops 负责；本阶段已验证本地认证和测试 SSH Server，Docker/NPM 与真实 Ubuntu SSH 仍须单独验收，不能由单元测试替代。

## 退出及后续接入点

`cmd/server/run.go` 先停止接入，取消请求与清理任务的上下文，主动关闭被取消的临时 SSH 底层连接，在统一退出期限内等待 HTTP Handler 和后台清理结束，最后关闭 SQLite。HTTP 强制关闭不能代替 Handler 等待；如果不可取消的底层任务超出总期限，会记录未完成事项并返回错误，由进程退出回收，绝不提前关库让仍在运行的 Handler 访问关闭的数据库。

服务器的每条状态使用独立锁；编辑、删除、信任确认和快照固定共享此锁。`SetActivityCheck` 是阶段 3 活动名额检查的接入点，当前默认无长期 Session，测试使用受控检查器。阶段 3 必须把预占和归还接入同一服务器锁；不能查询 SQLite 历史状态判断 Session 是否存活。
