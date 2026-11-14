# Stage 1 实施报告

日期：2026-11-14（Asia/Shanghai）。范围：仅项目基础设施。初次交付时实现代码未提交、未推送；用户随后授权提交并推送当前功能分支，未授权合并 main，未进入 Stage 2。

## 1. 完成情况

| Stage 1 任务 | 实现及验证状态 |
|---|---|
| 1. Go/Vue/TypeScript/Vite 工程及依赖锁定 | 已完成；Go 1.26.5、Vue 3；WebSocket 固定 v1.8.15，尚未实现 WS 业务 |
| 2. 完整配置、默认值、严格解析及启动校验 | 已完成；字段级类型/重复/未知/范围/溢出/联动测试通过；示例与内置默认配置逐字一致 |
| 3. 纯 Go SQLite 驱动、版本记录及完整表结构 | 已完成；modernc.org/sqlite v1.62.0，规定的五张表及索引，无额外业务表 |
| 4. 逐物理连接初始化、短事务和关闭 | 已完成；PRAGMA 回读、外键拒绝、真实锁等待、重开、失败回滚和关闭测试通过 |
| 5. JSON 请求、响应、错误转换及健康检查 | 已完成；严格解析、大小限制、脱敏、ready/not_ready 与路由测试通过 |
| 6. 基础前端、错误展示、路由及生产嵌入 | 已完成；安装/类型检查/测试/生产构建通过，真实 HTTP 验证资源与路由刷新；未做浏览器视觉验收 |
| 7. Docker 多阶段构建骨架及示例 | 文件已完成；当前环境无 Docker，镜像实际构建/启动未验证 |
| 8. 启动、退出及 HTTP/SQLite 回收 | 已完成；真实进程启动、两轮重启、SIGTERM、慢首部连接强制关闭和关库已验证 |

范围边界：仅做密钥来源与格式校验。管理员初始化、Argon2id、AES-GCM、已有密码密文解密验证、认证与 Origin 业务均属于 Stage 2，没有提前实现。历史 Session 仅执行文档要求的启动元数据清理，没有创建运行时 Session、绑定凭证、输出缓存或 SSH 资源。

## 2. 文件清单

全部为新增文件；没有修改或删除既有用户文件。构建产物和依赖目录按原有 `.gitignore` 忽略。

| 文件 | 用途 |
|---|---|
| `.dockerignore` | 排除数据、秘密、依赖、旧构建产物及无关目录 |
| `.env.example` | 密钥注入和后续管理员初始化变量说明，无默认秘密 |
| `Dockerfile` | Node → Go → 单运行容器三阶段构建，非 root 运行 |
| `docker-compose.yml` | 数据目录持久化、只读配置、环境注入、30 秒停止宽限期 |
| `config.example.yaml` | 核心文档全部默认配置 |
| `README.md` | 构建、运行、开发、SQLite 备份、退出和容器说明 |
| `doc/stage-1-implementation-report.md` | 本报告 |
| `backend/go.mod` | 固定 Go 版本、直接及间接依赖 |
| `backend/go.sum` | Go 依赖内容校验和 |
| `backend/cmd/server/main.go` | 配置参数、日志、SIGINT/SIGTERM 和进程退出码 |
| `backend/cmd/server/run.go` | 密钥格式校验、数据库初始化/迁移/清理、HTTP 启动和退出顺序 |
| `backend/cmd/server/run_test.go` | 真实 HTTP、正常退出和半首部连接强制关闭测试 |
| `backend/internal/config/types.go` | 全部配置项的具名类型 |
| `backend/internal/config/defaults.yaml` | 嵌入的唯一运行默认值来源 |
| `backend/internal/config/config.go` | 严格 YAML 节点校验、默认值合并及关联校验 |
| `backend/internal/config/secrets.go` | 加密密钥二选一来源及规范 Base64/32 字节校验 |
| `backend/internal/config/config_test.go` | 默认值一致性、非法配置、开发来源与密钥测试 |
| `backend/internal/database/database.go` | 每物理连接设置/回读 PRAGMA、固定单连接池、短事务及启动清理 |
| `backend/internal/database/migrate.go` | 数字版本排序、迁移记录、事务执行及未知版本拒绝 |
| `backend/internal/database/database_test.go` | 真实驱动迁移、约束、重开、锁等待、备份及失败测试 |
| `backend/migrations/001_initial.sql` | 规定的四张业务/安全数据表、索引及约束；版本表由迁移入口创建 |
| `backend/migrations/embed.go` | SQL 迁移嵌入 |
| `backend/internal/httpapi/request.go` | 有界 JSON 读取及字段、类型、缺项、重复、尾随内容校验 |
| `backend/internal/httpapi/response.go` | 统一响应结构及本阶段错误转换，不泄露内部错误 |
| `backend/internal/httpapi/router.go` | 健康检查、静态资源和 SPA 路由边界，no-store |
| `backend/internal/httpapi/httpapi_test.go` | 请求/响应、路由、健康状态及错误脱敏测试 |
| `backend/web/embed.go` | 嵌入生产前端资源，不提供构建占位页 |
| `frontend/package.json` | 固定直接依赖与实际可用的 dev/typecheck/test/build 脚本 |
| `frontend/package-lock.json` | 前端依赖树锁文件 |
| `frontend/tsconfig.json` | 严格 TypeScript 配置 |
| `frontend/vite.config.ts` | Vue 构建、输出到后端嵌入目录、开发 API 代理 |
| `frontend/index.html` | 中文页面及前端加载入口 |
| `frontend/src/main.ts` | Vue 挂载、路由与样式接入 |
| `frontend/src/App.vue` | 紧凑产品栏和基础路由容器 |
| `frontend/src/api/client.ts` | 健康 API、响应类型检查和具名 API 错误 |
| `frontend/src/router/index.ts` | `/` 和 `/login` 基础路由 |
| `frontend/src/pages/HomePage.vue` | 健康状态、按需错误提示、重试和卸载时取消请求 |
| `frontend/src/pages/LoginPage.vue` | 路由占位，不实现登录业务 |
| `frontend/src/styles/base.css` | 基础白色页面、边框、状态栏和错误样式 |
| `frontend/tests/client.test.mjs` | Node 内置测试，验证 API 成功、错误、格式和取消信号传递 |

## 3. 核心实现与约束

### 配置

配置包含文档全部八组参数。先检查 YAML 节点实际类型，再合并默认值并校验；不接受字符串数字、浮点、null、重复键、未知键及多 YAML 文档。正整数在容量/duration 转换前检查溢出，客户端整数和毫秒计时器有额外安全范围检查。HTTP 环回开发、请求/建连预算、凭证/绑定预算、输出块/缓存/队列、恢复稳定期等按核心表关联验证。

`database.busy_timeout` 还检查 SQLite API 的有符号 32 位毫秒表示范围，防止传入驱动时截断。容器停止宽限期属于外部部署状态，应用不伪称能从 YAML 自动读取 Docker 的实际设置；示例 30 秒大于默认退出预算 25 秒，部署说明要求同步核对。

### 数据库

选用纯 Go `modernc.org/sqlite v1.62.0`。使用该版本的 `NewConnector`，只包装物理连接创建，保留原始连接的 context/事务等可选接口。每次 Connect 设置 busy_timeout、foreign_keys，执行 journal_mode=WAL 并核验三个实际返回值；失败关闭新连接并拒绝继续。最大打开/空闲连接均为 1。

SQL 迁移和版本记录在同一事务中执行，失败不保留部分新结构、不删除旧数据。所有表、字段和业务索引来自核心文档；所有持久化时间为 UTC Unix 毫秒。启动只清理旧的非 CLOSED 元数据，失败不开放 HTTP。

已在实际驱动上验证 `VACUUM INTO ?` 包含 WAL 中已提交的数据。源码另有 `NewBackup`，本次仅检查其存在，没有测试该接口。备份没有新增 HTTP API、任务或业务模块。

### HTTP、静态资源及前端

HTTP 使用 net/http，提供配置化首部/请求预算。请求解析在读取前设置 MaxBytesReader；具名 DTO 字段严格匹配，omitempty 表示允许缺项；业务范围留给各业务 DTO，不在基础解析器中猜测。成功 code 为数字 0，错误 code 为文档字符串，本阶段不新增服务器错误码。

`GET /api/health` 就绪返回 200/ready；未就绪返回 503/SERVER_UNAVAILABLE/not_ready。生产页面由 go:embed 提供，`/login` 刷新回退首页入口，未知 API/WS 不回退成 HTML。API 使用 no-store，静态错误路径也补齐 no-store；相关封装仅用于静态资源，不遮蔽未来 API 接入的可选 HTTP 接口。

前端只实现基础页面和错误展示。没有登录表单、服务器管理页、终端或后续业务。请求在页面卸载和重试替换时取消。

### 启动、退出及 Docker

启动顺序：配置校验 → 密钥来源/格式校验 → 打开并核验数据库 → 迁移 → 历史元数据清理 → 嵌入资源 → HTTP 监听。阶段 2 的管理员/服务器初始化及密码密文验证尚未加入，不能把本阶段健康检查当作后续业务已就绪。

退出先取消就绪状态，HTTP Shutdown 使用独立清理上下文和统一预算，超时 Close 解除慢连接，随后同步关闭本阶段已无操作的数据库池。本阶段所有 DB 操作在开放监听前完成；后续业务写入必须先在同一总期限内完成/取消，SSH/WS 资源按既定架构接入，不改变其所有权或锁顺序。本阶段未创建未来管理器占位接口。

Dockerfile 先构建前端、复制 dist 到 Go 构建阶段、以 CGO_ENABLED=0 编译，最后由非 root 单进程运行。配置和密钥不进入镜像，Compose 持久化整个 /data，入口直接接收 SIGTERM。

## 4. 依赖变化

| 直接依赖 | 固定版本 | 原因 |
|---|---|---|
| Go | 1.26.5 | 核心文档指定 |
| github.com/coder/websocket | v1.8.15 | Stage 1 必须锁定；尚未实现 WS 业务 |
| go.yaml.in/yaml/v3 | v3.0.6 | YAML 节点级严格解析 |
| modernc.org/sqlite | v1.62.0 | 纯 Go SQLite、逐连接 Connector 和事务 |
| Vue | 3.5.45 | 既定前端框架 |
| Vue Router | 4.6.3 | Vue 3 基础路由，避免引入无实际需要的状态库 |
| TypeScript | 5.9.3 | 与当前 Vue 类型检查工具组合验证的稳定版本 |
| Vite | 8.3.8 | 前端开发及生产构建 |
| @vitejs/plugin-vue | 6.0.11 | Vue 单文件组件构建 |
| vue-tsc | 3.3.14 | Vue/TypeScript 类型检查 |

Go 间接依赖来自 SQLite：go-humanize v1.1.0、uuid v1.6.0、bigfft v0.0.0-20230129092748-24d4a6f8daec、x/sys v0.49.0、modernc libc v1.80.1、mathutil v1.7.1、memory v1.12.1，准确版本以 go.mod 为准。前端间接依赖完整锁定于 package-lock.json。没有 ORM、HTTP 框架、Redis、消息队列、Pinia、xterm 或 SSH 业务依赖；没有发现本阶段不必要的直接依赖。

## 5. 实际命令和测试结果

环境：Windows 调用 WSL2 Ubuntu/Linux amd64。Go 来自 `/tmp/codex/ws-header-audit-20261114/go/bin/go`，Node 24.22.3 下载到 `/tmp/codex/stage1/`，下载文件 SHA256 核验通过。没有改动全局工具配置。

实际通过临时包装器 `python3 /tmp/codex/stage1/run.py <命令>` 执行下表 Go/npm 命令。包装器仅设置临时 PATH、模块/构建/npm 缓存及模块镜像。原始 sum.golang.org 直连超时，官方代理校验路径返回 404；改用 goproxy.cn 转送同一 sum.golang.org 签名校验后成功，未关闭校验。

| 工作目录与实际命令 | 结果 |
|---|---|
| frontend：`npm install --no-audit --no-fund` | 初始安装成功，52 个包，生成锁文件 |
| frontend：`npm ci --no-audit --no-fund` | 锁文件干净安装成功，52 个包，964ms |
| frontend：`npm test` | 1 项测试通过，0 失败；覆盖成功、错误、响应类型和 AbortSignal 传递 |
| frontend：`npm run build` | 内含 `vue-tsc --noEmit`，类型检查成功；Vite 33 模块构建，127ms |
| frontend：`npm ls --depth=0` | 六个直接依赖版本与清单一致 |
| backend：`go mod download all` | 成功，补齐模块内容与校验和 |
| backend：`go build -mod=mod ./...`、`go build ./...` | 成功 |
| backend：`go test -mod=mod -v ./internal/config` | 默认/示例、严格配置、开发来源、加载和密钥测试全部通过 |
| backend：`go test -v -count=1 -timeout=30s ./internal/database` | 所有真实 SQLite 场景通过 |
| backend：`go test -v ./internal/httpapi` | 初次发现静态 404 缓存头问题，修复后 `go test ./internal/httpapi` 通过 |
| backend：`go test -v ./cmd/server` | 初次发现异步关库返回过早，修复后正常及慢连接退出测试通过 |
| backend：`go vet ./...` | 成功，无诊断 |
| backend：`go test -count=1 ./...` | cmd/server 1.012s；config 0.015s；database 0.198s；httpapi 0.005s，全部通过 |
| backend：`go mod verify` | `all modules verified` |
| backend：`go test -race ./...` | 未能执行：race 要求 CGO |
| backend：`env CGO_ENABLED=1 go test -race ./...` | 未能执行：`C compiler "gcc" not found`；不记通过 |
| backend：`env CGO_ENABLED=0 go build -trimpath -o /tmp/codex/stage1/web-ssh ./cmd/server` | 修改后的最终生产二进制构建成功 |
| backend：收尾 `go test ./internal/httpapi ./cmd/server` | 0.005s / 1.012s，通过 |
| backend：加强断言后 `go test -v ./internal/database ./cmd/server` | 0.196s / 1.012s，通过 |
| backend：收尾 `go vet ./cmd/server ./internal/httpapi` | 成功，无诊断 |
| 项目根：`python3 /tmp/codex/stage1/smoke.py` | 修改后的最终二进制两轮真实进程测试通过，详见下方 |
| 项目根：`docker version`、`docker compose version` | `docker: command not found`；镜像构建/启动未执行 |

SQLite 实测关键结果：逐连接回读 `journal_mode=wal foreign_keys=1 busy_timeout=120`；实际锁测试配置 150ms，最终一次实测约 152.36ms 后拒绝写入；调用 context 提前截止生效。真实物理连接驱逐后再次回读并用不同主键执行非法外键写入，避免被主键冲突掩盖；WAL/SHM 与 DB 同目录，失败迁移保留已有数据并回滚新结构。

真实生产进程测试输出：

```text
round=1: health/static/assets/route/WAL/migration OK; SIGTERM exit=0 in 0.001s
round=2: health/static/assets/route/WAL/migration OK; SIGTERM exit=0 in 0.001s
invalid-config: rejected before serving, exit=1
missing-key: rejected before serving, exit=1
unwritable-path: rejected before serving, exit=1
Stage 1 process smoke PASS; all test processes stopped
```

二进制从临时工作目录运行，没有 Vite 服务或运行时源文件依赖。测试读取真实 HTML 引用的 JS/CSS、刷新 `/login`、检查未知 API 不返回页面，第二轮验证历史 CONNECTED 行变为 CLOSED/process_restarted。临时数据库/密钥只用于测试；测试进程全部停止。

## 6. Git 状态及补丁

当前分支：`codex/第一阶段-项目基础设施`，基于用户最新授权的 `origin/main`，起始提交 `bde41e1bde6875fc4a6960745f7c4d022781c519`。本地 main 跟踪 origin/main。此前按单独授权创建的本地/远程 master 保留，未使用它实施，未继续删除 main。

没有修改既有已跟踪文件、没有暂存修改。所有实现文件目前为未跟踪新增文件，因此普通 `git diff --stat` 为空；不能把空输出误当作没有实现改动。

```text
git status --short
?? .dockerignore
?? .env.example
?? Dockerfile
?? README.md
?? backend/
?? config.example.yaml
?? docker-compose.yml
?? doc/stage-1-implementation-report.md
?? frontend/

git diff --stat
（无输出，新增文件尚未暂存）

git diff --cached --stat
（无输出）

git diff --check
（无输出）
```

另对每个新增文件执行 `git diff --no-index --check /dev/null <文件>`，并将实际新增补丁保存到 `/tmp/codex/stage1/stage1.patch`，通过 `git apply --stat` 查看；该命令只统计，没有应用补丁，也没有改动索引。最终统计见交付回复。

## 7. 未完成验证、限制和后续风险

1. 当前 WSL shell 未找到 Docker 命令，未进一步确认服务状态；Docker 镜像构建、容器启动、挂载权限、停止信号在容器中的实际行为仍待 ops 验证。不能宣称 Docker 部署通过。
2. 实施时的执行环境未找到 C 编译器，race 检测当时未成功运行；用户随后在 WSL Ubuntu 中执行 `CGO_ENABLED=1 go test -race ./...`，确认所有 Go 测试包均通过，未检测到数据竞争。该验证缺口已由用户补充结果补齐，详见第 10 节。
3. 未执行真实浏览器视觉和交互验收；本阶段前端有类型/构建/API 单元测试及真实 HTTP 资源验证，没有把它们描述为浏览器验收。
4. 本阶段没有认证、密码加解密和完整终端能力；阶段 2/3 必须按原文档接入，不能把基础路由、格式校验或历史元数据清理当作业务已完成。
5. HTTP 超时当前只服务普通 HTTP。阶段 3 接入 Upgrade 时必须按架构登记底层连接、处理升级后期限及独立资源回收，不得直接复用 HTTP 请求生命周期。
6. 单连接逐连接初始化、迁移和已有文件保护均已验证；未发现需要改变表结构、配置默认值、协议、锁顺序或配置版本规则的设计问题。

## 8. 验收结论

Stage 1 的八项实现任务已完成，当前可执行的本地构建、单元/驱动/真实进程验收已通过；不存在已知的核心契约实现偏差。用户已补充 race 验证通过的结果；Docker 验证仍存在明确环境缺口，不能给出“完整验收全部通过”的结论。

建议先审查本地代码、补丁和本报告，补齐 Docker 验证后再决定是否授权 Stage 2。当前停止于 Stage 1；仅按用户后续明确授权提交并推送当前功能分支，不合并 main，不继续后续阶段。

## 9. 用户确认后的执行环境更正与提交授权

用户已明确说明代码和开发命令全部在 WSL Ubuntu 中执行，Windows ChatGPT 应用只作为沟通载体。已将此规则写入 Windows 应用使用的全局 `/mnt/c/Users/x/.codex/AGENTS.md`，该全局文件不属于本项目提交内容。

通过 `wsl.exe -d Ubuntu --cd /home/x/workspaces/web-ssh -- bash -ic ...` 实际复核：

```text
command -v go node npm
/home/x/app/go/bin/go
/home/x/.nvm/versions/node/v24.21.0/bin/node
/home/x/.nvm/versions/node/v24.21.0/bin/npm

go version
go version go1.26.5 linux/amd64
node -v
v24.21.0
npm -v
11.19.0
```

先前非交互式执行环境没有发现 Node/npm，不能据此认为 WSL 未安装。Node/npm 由 nvm 路径提供，加载交互式 Bash 环境后可正常找到。第 5 节记录的构建结果确实使用了临时工具，是历史实际验证证据，未改写为使用用户当前工具执行的结果。本次仅更正文档并核对补丁，不重复未发生代码变化的完整构建。

用户现已授权 `commit` 和 `push`。提交范围为此前 Stage 1 文件及本次报告更正，推送 `codex/第一阶段-项目基础设施`，不合并 main、不进入 Stage 2。第 6 节的未跟踪状态是初次交付时的快照，提交后状态以本次操作结果和 Git 为准。


## 10. 用户补充的 race 验证结果

用户补充确认：在 WSL Ubuntu 环境执行以下命令，所有 Go 测试包均通过，没有检测到数据竞争。

```bash
CGO_ENABLED=1 go test -race ./...
```

结果来源：用户提供的实际执行结果，本次未由 Agent 重新执行。第 5 节中 race 未能执行的记录保留为实施时的历史记录，当前 race 验证状态以本节补充结果为准。本次仅更新实施报告，不修改业务代码、不扩大测试范围。
