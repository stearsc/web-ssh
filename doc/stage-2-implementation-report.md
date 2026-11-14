# Stage 2 实施报告：登录与服务器管理

日期：2026-11-14。范围仅限第二阶段；未实现第三阶段终端链路，未提交或推送。

## 1. 实现内容

- 单管理员初始化、Argon2id 验证、独立 Cookie 登录、持久化撤销、过期和有界限流。
- AES-256-GCM 保存 SSH 密码；启动检查已有密文；编辑留空保留原密文。
- 服务器新增、详情、编辑、删除、名称字面搜索、分组过滤及服务器参数规范化。
- 首次 Host Key 探测与密钥变化提示、人工确认、一次性凭证、并发与事务失败处理。
- 临时 SSH 认证测试；认证前校验完整公钥；超时/取消后释放连接，不申请 Channel、PTY 或 Shell。
- 登录、服务器管理、首次/变化信任弹窗及主题/窄屏布局，按已确认 v2 原型实现本阶段界面。
- 修复应用退出顺序：拒绝新接入，取消业务，等待 Handler/清理任务，再关闭 SQLite。

## 2. 文件变更清单

| 类型 | 文件 | 职责 |
|---|---|---|
| 修改 | `README.md` | 第二阶段运行、初始化、Origin、信任和退出说明；Ubuntu 部署待验证操作。 |
| 修改 | `backend/cmd/server/run.go` | 初始化管理员和服务器状态；接入管理 API；停止接入、取消业务、等待任务后关库。 |
| 修改 | `backend/cmd/server/run_test.go` | 真实 HTTP、慢首部连接、阻塞 SSH 测试中的退出与关库顺序。 |
| 修改 | `backend/go.mod` | 固定 x/crypto v0.59.0；其所需 x/sys v0.50.0；保留现有 WebSocket 版本。 |
| 修改 | `backend/go.sum` | 新增依赖校验和。 |
| 新增 | `backend/internal/auth/auth.go` | 持久化登录、独立登录锁、全局窗口限流、容量、撤销/冻结、到期清理。 |
| 新增 | `backend/internal/auth/auth_test.go` | 初始化、PHC、重启、过期、并发容量、限流、注销事务故障。 |
| 新增 | `backend/internal/auth/password.go` | Argon2id PHC 生成、解析、资源边界及常量时间验证。 |
| 修改 | `backend/internal/config/secrets.go` | 更新已有密钥加载器的职责注释；读取契约保持不变。 |
| 新增 | `backend/internal/crypto/crypto.go` | AES-256-GCM 版本化密文、新 nonce、完整性校验。 |
| 新增 | `backend/internal/crypto/crypto_test.go` | 往返、nonce、错误密钥、密文/nonce/版本篡改和长度错误。 |
| 新增 | `backend/internal/database/transaction.go` | 区分提交成功、确定回滚及提交结果不确定。 |
| 新增 | `backend/internal/fault/fault.go` | 跨业务模块的公开错误码类型，避免向 HTTP 传递底层敏感错误。 |
| 新增 | `backend/internal/httpapi/management.go` | 本阶段十个管理接口、Cookie、Origin、请求/响应 DTO 和权限接入。 |
| 新增 | `backend/internal/httpapi/management_test.go` | HTTP 登录到删除的完整闭环、受控 SSH、敏感字段、Origin、严格请求校验。 |
| 修改 | `backend/internal/httpapi/request.go` | 在既有严格 JSON 校验前拒绝非法 UTF-8。 |
| 修改 | `backend/internal/httpapi/response.go` | 映射本阶段认证、服务器、Host Key、存储及 SSH 错误码。 |
| 新增 | `backend/internal/server/types.go` | 写入字段校验、Host 规范化和不含秘密的展示类型。 |
| 新增 | `backend/internal/server/store.go` | 服务器 SQL 读写、明确可空类型、资料与信任同事务保存。 |
| 新增 | `backend/internal/server/service.go` | CRUD、参数化字面搜索、每服务器锁、内存版本、不可用重载及删除检查接入点。 |
| 新增 | `backend/internal/server/probe.go` | 连接快照、探测凭证、过期/容量/归属、占用/消费、信任确认事务。 |
| 新增 | `backend/internal/server/server_test.go` | 真实测试 SSH 服务、确认竞争、过期/重放、编辑删除竞争、故障注入与密文兼容。 |
| 新增 | `backend/internal/sshclient/test.go` | 认证前完整 Host Key 比较；限时 TCP/SSH 认证；取消关闭；不申请 Channel、PTY 或 Shell。 |
| 新增 | `backend/internal/sshclient/test_test.go` | 握手超时、握手取消、认证取消。 |
| 新增 | `backend/internal/testutil/testutil.go` | 测试专用 SQLite 和可轮换密钥、控制阻塞、统计认证/连接的 SSH 服务。 |
| 修改 | `frontend/src/App.vue` | 由登录页和管理页分别承载布局。 |
| 修改 | `frontend/src/main.ts` | 在页面挂载前恢复主题偏好。 |
| 修改 | `frontend/src/router/index.ts` | 服务端登录检查、未登录跳转及失效处理。 |
| 新增 | `frontend/src/stores/auth.ts` | 内存登录状态、到期计时、登出失败重试状态；不持久化 Token 或密码。 |
| 新增 | `frontend/src/api/management.ts` | 同源管理请求、类型、结构化错误、探测响应校验。 |
| 修改 | `frontend/src/pages/LoginPage.vue` | 管理员密码登录、错误提示和未完成登出重试。 |
| 修改 | `frontend/src/pages/HomePage.vue` | 默认收起侧栏、分组/搜索/详情、表单入口、测试、删除、注销。 |
| 新增 | `frontend/src/components/layout/Modal.vue` | 原生 dialog 模态交互。 |
| 新增 | `frontend/src/components/layout/ThemeButton.vue` | 浅色/深色主题切换。 |
| 新增 | `frontend/src/components/server/ServerForm.vue` | 新增/编辑服务器，密码不回填，留空保留。 |
| 新增 | `frontend/src/components/server/HostKeyDialog.vue` | 首次/变化的目标、公钥算法及新旧指纹展示；仅提交 probe_id。 |
| 修改 | `frontend/src/styles/base.css` | v2 风格的配色、边框、布局、弹窗和窄屏抽屉。 |
| 新增 | `frontend/tests/management.test.mjs` | 请求凭据、退出失败、401、探测响应等前端测试。 |
| 修改 | `frontend/tsconfig.json` | 支持 Node 原生 TypeScript 测试使用显式 .ts 导入。 |
| 新增 | `doc/stage-2-implementation-report.md` | 本报告：实施、逐文件清单、API、验证证据与部署边界。 |

本次共 40 个新增或修改文件。

新增生产依赖只有既定技术栈所需 `golang.org/x/crypto v0.59.0`（SSH/Argon2id）；该版本要求 `golang.org/x/sys v0.50.0`，因此间接依赖从 v0.49.0 跟随调整。未升级其他现有依赖，也未删除已固定的 `coder/websocket v1.8.15`。前端运行依赖及其锁文件不变。临时格式化/浏览器工具位于 `/tmp/codex/`，不进入项目依赖。

## 3. 数据库变化

无新增迁移，无表或索引修改，继续使用 Stage 1 的 `001_initial.sql`。

- `admin_credentials`：唯一管理员 PHC 字符串及时间。
- `web_auth_sessions`：独立登录 ID、Token 哈希及绝对过期时间。
- `servers`：连接资料、版本化密码密文及当前目标的完整可信 Host Key。
- `sessions`：仍只作为历史元数据；启动清理仍生效。删除只依据活动检查器，不根据陈旧 status 判断存活，删除成功由外键级联清理历史行。
- `config_version`、probe 索引及凭证消费状态只在内存。进程重启丢失全部 probe，已确认信任继续从数据库读取。

## 4. 安全实现

### 管理员与 Cookie

首次初始化读取 `APP_ADMIN_PASSWORD`，8～1024 UTF-8 字节，不裁剪空白；存在管理员时不覆盖。Argon2id 使用 65536 KiB、迭代 3、并行度 1、16 字节随机盐、32 字节输出，验证读取 PHC 参数并限制损坏参数的资源开销。

Token 为 32 随机字节的无填充 Base64URL；仅 SHA-256 存 SQLite。Cookie 为 `web_ssh_auth`，HttpOnly、SameSite=Strict、Path=/、不设置 Domain，默认 Secure；有效期默认七天、绝对到期、不滑动续期。每次请求验证服务端登录对象及到期，重启从持久化记录恢复仍有效登录。

登录尝试共用一个固定时间窗口计数器，不按任意来源建立索引；昂贵哈希校验串行。登录容量的过期删除、检查和插入共用短事务。后台定期清理，验证不依赖清理及时性。退出确定回滚保留权限及 Cookie；不确定提交冻结本登录、撤销其 probe 并保留 Cookie 供重试；只有确认持久化撤销后清 Cookie。

### 密码与主密钥

继续使用已有 `APP_ENCRYPTION_KEY` / `APP_ENCRYPTION_KEY_FILE` 二选一机制，均为标准 Base64 的 32 字节独立密钥。应用不自动生成替代密钥。密文格式为版本字节 1 + 12 字节随机 nonce + GCM 密文及 16 字节标签。启动时验证所有已有密码密文，错误密钥或篡改停止启动。认证材料仅在短期建连任务持有；Go 运行时不保证所有字符串副本可立即擦除。

### Host Key 与探测凭证

使用 Host/Port 与完整 SSH 公钥字节校验，算法及指纹仅供展示。未信任/变化时 HostKeyCallback 拒绝并关闭连接，密码回调尚未执行；只有通过校验后才解密并发送用户认证材料。

probe 为 32 随机字节 Base64URL，索引只存哈希；绑定独立登录、服务器、目标、版本、原信任与观测公钥。默认 TTL 300 秒、上限 256；登录更早到期时同步缩短可用期限。签发时原子清理/容量检查/插入，访问时检查有效性，定期回收。取消不写信任。

锁顺序为服务器锁 → 登录读锁 → probe 索引锁；索引锁不跨数据库或网络。确认准入时标记占用，清理不能移除在途项；事务成功后消费、递增版本并使其他 probe 失效。确定回滚允许在原期限内重试；不确定提交使旧版本失效，标记不可用并重读，读取失败时后续操作只能有限重载或拒绝。Host/Port 改变清信任；用户名/实际密码改变保留信任但作废旧探测；纯展示资料不改变版本。

### HTTP 与退出

所有写接口（含登录/登出）精确匹配配置 Origin；代理头不能改变允许来源或 Cookie 策略。本阶段不依赖转发客户端 IP 作安全决策，也不采信任意转发 IP 写日志。除登录、专用幂等登出和健康检查外，本阶段管理接口均要求有效登录。请求严格拒绝未知/重复字段、错误类型、尾随 JSON、非法 UTF-8 和超限请求体。响应类型不包含密码、密文、完整公钥、内部版本或登录 ID。

临时 TCP 连接通过取消回调立即关闭，HTTP 请求总预算和 SSH 建连总预算共同约束。应用停止接入后取消任务并等待结束，不能因 HTTP Shutdown 超时返回就提前关闭 SQLite。不可取消任务若超过总退出期限，返回错误并由进程退出回收，不让活跃 Handler 访问已关闭数据库。

## 5. API 清单

响应统一为 `{code,message,data}`，成功 code=0；失败使用已定义字符串错误码。创建 HTTP 201，其他成功 HTTP 200；时间为 UTC RFC3339 固定毫秒。所有响应 no-store。

| 方法与路径 | 请求 | 成功 data | 权限 |
|---|---|---|---|
| POST `/api/auth/login` | 必填 `password`，最多 1024 字节 | `expires_at`，通过 Set-Cookie 交付 Token | Origin |
| POST `/api/auth/logout` | `{}` 或空请求体 | `{}`，成功才清 Cookie | Origin；允许缺失/过期/冻结 Cookie |
| GET `/api/auth/me` | 无 | `expires_at`、`client_settings` | 登录 |
| GET `/api/servers` | 可选 `search`、`group_name`，各最多 128 字节 | `items: ServerView[]`，名称/ID 升序 | 登录 |
| POST `/api/servers` | ServerInput；Port 可省略，密码必填 | ServerView | 登录 + Origin |
| GET `/api/servers/{id}` | 无 | ServerView | 登录 |
| PUT `/api/servers/{id}` | ServerInput；Port 必填；密码省略/空值保留 | ServerView | 登录 + Origin |
| DELETE `/api/servers/{id}` | `{}` 或空请求体 | `{}` | 登录 + Origin；活动检查必须通过 |
| POST `/api/servers/{id}/test` | `{}` 或空请求体，只测试已保存配置 | `host,port,username,authenticated:true` | 登录 + Origin |
| POST `/api/servers/{id}/host-key/confirm` | 仅 `probe_id` | HostKeyView | 登录 + Origin + 凭证归属/期限/版本检查 |

- ServerInput：`name,host,port,username,password,group_name,remark`。name 去空白后 1～128 字节；Host 是规范化 IP/ASCII DNS、最多 253 字节；Port 1～65535，创建省略为 22；username 1～128 字节且无 NUL；非空 password 最多 4096 字节；group_name 去空白后最多 128 字节；remark 最多 1024 字节。PUT 省略 group_name/remark 清为空。
- ServerView：`id,name,host,port,username,group_name,remark,host_key,created_at,updated_at`。
- HostKeyView：`host,port,algorithm,fingerprint`；尚未信任时 ServerView.host_key=null。
- 需确认时返回 HTTP 409 / `HOST_KEY_CONFIRMATION_REQUIRED`，data 包含 `probe_id,expires_at,host,port,algorithm,fingerprint,previous_fingerprint,reason`；reason 为 `first_trust` 或 `key_changed`。
- AuthView.client_settings：`max_control_message_size,max_input_message_size,output_chunk_size,bind_timeout,operation_wait_timeout,write_timeout,max_cols,max_rows,write_queue_bytes,write_drain_timeout,reconnect_max_attempts,reconnect_delay,reconnect_stable_time`，均取当前配置，不实现对应后续终端功能。
- 既有 GET `/api/health` 保留。未新增 WebSocket、Session 或独立探测路由。

## 6. 测试结果

环境：WSL Ubuntu，Go 1.26.5 linux/amd64，Node v24.21.0，npm 11.19.0。所有开发、构建及测试命令在 WSL 执行。

| 实际命令 | 结果 |
|---|---|
| `go test -count=1 ./...` | 通过；最终 JSON 记录见下一行 |
| `TMPDIR=/tmp/codex go test -count=1 -json ./...` | 36 个顶层测试，含子测试共 123 项 pass，0 fail |
| `go test -race ./...` | 通过；最终变更后的完整记录见下一行 |
| `TMPDIR=/tmp/codex go test -race -count=1 -json ./...` | 同样 36 个顶层测试、123 项 pass，0 fail，无数据竞争报告 |
| `go vet ./...` | 通过 |
| `go mod verify` | `all modules verified` |
| `go build ./...` | 通过 |
| `go build -o /tmp/codex/web-ssh-stage2/server ./cmd/server` | 本地嵌入前端的应用构建通过 |
| `npm test` | 3 项通过，0 失败 |
| `npm run build` | 包含 `npm run typecheck`；Vue TypeScript 与 Vite 生产构建通过 |
| `git diff --check` | 通过 |

普通测试和 Race 原始 JSON 位于 `/tmp/codex/web-ssh-stage2/go-test.json`、`go-race.json`。测试使用独立 SQLite 数据库及 Go 实现的环回 SSH Server，可控制密钥轮换、错误密码、握手/认证阻塞和连接释放。覆盖重复/竞争确认、过期/归属/容量、编辑/删除/重建竞争、回滚与不确定提交、错误主密钥、密文篡改、注销/重启及优雅退出。

事务故障中的提交不确定路径使用可控事务结果注入；确定回滚另用实际 SQLite 触发器验证。它们不是磁盘损坏或真实部署验收。活动删除检查使用受控替身，未声称已验证未来 Session Manager 的真实名额竞争。

浏览器实际验收：Playwright 1.65.1 + Chromium 159.0.8154.6，访问本机真实 Go 应用及独立测试 SSH 服务，**13 组交互通过，页面运行时错误 0**。

覆盖：错误密码拒绝；正确登录与默认收起侧栏；新增与详情；首次信任展示及拒绝确认；确认后重新测试成功；实际密钥轮换、新旧指纹、取消与重新确认；编辑不回填密码、留空保留密码与信任；名称搜索及分组筛选；刷新恢复登录与主题；390px 窄屏抽屉及无横向溢出；删除取消及确认；Cookie 丢失后跳转登录；登出与登录页跳转。

命令：`FONTCONFIG_FILE=/tmp/codex/web-ssh-stage2/fonts.conf LD_LIBRARY_PATH=/tmp/codex/web-ssh-stage2/browser-libs/usr/lib/x86_64-linux-gnu TMPDIR=/tmp/codex node /tmp/codex/web-ssh-stage2/browser-check.mjs`。

浏览器报告为 `/tmp/codex/web-ssh-stage2/browser-results.json`，脚本为同目录 `browser-check.mjs`；已查看 `desktop.png`、`mobile.png` 和 `changed-host-key.png` 截图。1280×900 桌面与 390×844 窄屏通过，不能替代真实手机/软键盘验收。浏览器第一次尝试被应用内工具启动失败阻塞；随后在 WSL 的 `/tmp/codex` 下载校验浏览器、解包缺少的三项运行库并引用现有 Windows 中文字体，未安装系统包。首次浏览器测试因测试脚本没有等待异步选择完成而失败，补充等待后完整重跑通过。

首轮测试发现并修复：退出期限与已完成等待的竞态判断、Vue 模板闭合错误。上述最终结果均来自修复后的实际运行。

## 7. Docker 待验证事项

本机按要求不安装 Docker；本阶段未安装 Docker Desktop、Engine 或 Compose，也未执行镜像构建或 Compose 启动测试。

**待 Ubuntu 服务器部署验证**：

1. 在用户安排的服务器准备 `config.yaml`、独立持久密钥、首次初始化密码及可写 data 目录；正式 Origin 使用真实 HTTPS 来源，Secure 保持开启。
2. 按 README 构建 `web-ssh:stage2`，启动 Compose，核对配置/数据挂载、10001 用户权限及停止宽限期。
3. 通过 NPM 的 HTTPS 入口验证登录、Cookie、Origin、CRUD，确认代理首部不能绕过来源规则。
4. 对授权的真实 Ubuntu SSH 目标执行首次探测、拒绝/确认、测试认证、受控密钥轮换和目标编辑后重新确认。
5. 重启容器确认服务器资料/可信公钥持久化，旧 probe 无效，已成功退出的 Cookie 仍无效；在正在测试 SSH 时发送 SIGTERM，检查应用在宽限期内退出。
6. 分别验证数据库一致性备份及主密钥备份/恢复。未安排真实环境前，上述项目均不标记通过。

## 8. 已知问题与限制

- 没有未解决的本阶段自动化测试失败；真实 Ubuntu SSH、Docker/NPM、真实手机和软键盘操作仍未验收。
- 未创建 Session Manager；活动删除检查仅交付接口及替身验证，阶段 3 必须把名额预占/归还接入相同服务器同步边界。
- SSH 测试只认证，不创建 Channel/PTY/Shell，不占正式 Session 名额。
- 主密钥不支持轮换 API；管理员不支持密码管理界面。这些遵循当前设计。
- SQLite 驱动不可确定的提交错误按保守路径处理，失败响应后应重新查询或重新测试，不能无条件重复创建或确认。
- Go 的字符串/运行时副本不能保证立即擦除；实现限制认证材料的持有期并禁止进入响应、日志或长期连接对象。

## 9. Git 状态

- 当前分支：`codex/第二阶段-登录与服务器管理`。
- 创建基线：`1e7544f`，包含 Stage 1 `e346044`；当前 HEAD 未新增提交。
- 远程默认分支通过 `git ls-remote --symref origin HEAD` 确认为 `main`。检查时 main/master 均为 `bde41e1`，均不包含 Stage 1；包含 Stage 1 的远程分支是 `origin/codex/第一阶段-项目基础设施`。
- 已先汇报指南的一般 `origin/master` 基线规则与实际情况的差异，按本次授权和连续阶段保留前序成果要求，从 Stage 1 分支成果创建新的中文阶段分支。
- 开始时工作区干净，没有覆盖用户已有修改。
- 本阶段修改全部留在工作区，有未暂存修改及新增未跟踪文件；没有暂存变更。
- 执行过只读 Git 检查、`git fetch origin` 和功能分支创建；未执行 commit、push、merge、rebase、reset 或 clean。

## 10. 下一阶段建议

先审查第二阶段安全边界及本报告，安排真实 Ubuntu 部署验证。第三阶段可复用认证身份、可信连接快照、Host Key 流程及每服务器同步边界，新增 Session Manager 时必须落实活动名额预占/归还，并遵守最终 WebSocket 凭证协议和统一退出预算。

本次到此停止，不提前实现 PTY、Shell、WebSocket 终端、输出缓存或跨设备接管。
