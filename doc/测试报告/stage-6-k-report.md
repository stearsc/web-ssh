# 第六阶段 6-K 实现与验证报告

日期：2026-11-20（Asia/Shanghai）。开发基线 `S6@56a346bf9a832c9e71c2ca6ff439f5b57979f9d7`。用户明确不创建功能分支，批准采用编码前报告中的契约建议，完成 6-K.0 后继续完整编码。当前仍在 S6，未 commit/push/merge/rebase，未部署或修改生产数据。

## 1. 实际实现

已实现密码与私钥互斥的完整认证路径：认证表单、JSON/原生 FormData、受限 multipart、验证后原样持久化、Passphrase v2 密文、真实 SSH Test/Session/WS、Host Key 门禁、短文件 lease、持久化候选/claim/重试、启动对账、单文件故障隔离、受控停机备份/校验/恢复命令。必要表单内测试已接入；其他 6-3 UI 工作未扩展。

密码新建默认、编辑省略/空密码保留、已有 v1 密文、Host Key 确认和第五阶段业务语义保持。新文件验证失败不改变旧配置；替换不使用旧 Passphrase；普通私钥资料编辑完全省略秘密字段；两模式切换在同一服务器事务中清除另一种凭据。

## 2. 修改文件与目的

以下路径相对仓库根目录；同一行的各文件用途相同或直接配套。未修改 001、第五阶段冻结文档、WebSocket 协议及历史报告。

| 文件 | 目的 |
|---|---|
| `backend/migrations/002_private_key_auth.sql` | 对象账本、认证互斥列/CHECK、完整重建父子表 |
| `backend/internal/database/migrate.go` | 迁移事务内 foreign_key_check |
| `backend/internal/database/private_key_migration_test.go` | 旧库升级保留字节/信任/Session、互斥与 CASCADE |
| `backend/internal/database/database_test.go` | 版本2/对象表预期、迁移失败回滚夹具 |
| `backend/internal/crypto/passphrase.go` | v2 AES-GCM、用途/服务器 AAD、禁止 fallback、NULL 空值 |
| `backend/internal/crypto/passphrase_test.go` | 上下文/版本/主密钥/损坏/无值测试 |
| `backend/internal/config/types.go` | 唯一 GC 宽限字段 |
| `backend/internal/config/config.go` | 新字段 duration 溢出校验，沿用正整数校验 |
| `backend/internal/config/defaults.yaml`、`config.example.yaml` | 默认 86400 秒与中文说明 |
| `backend/internal/privatekey/parse.go` | 全局解析名额、格式解析、PEM RSA 低成本预检/统一上限 |
| `backend/internal/privatekey/parse_test.go` | 支持格式/口令、超大 RSA、16384 bit 结构边界、多素数、bcrypt 上限与真实取消生命周期 |
| `backend/internal/privatekey/files.go` | 逐层 NOFOLLOW、对象权限/身份/摘要、安全相对删除 |
| `backend/internal/privatekey/store.go` | 账本、原始文件提升、独立 lease、三态事务保护、对账、原子 claim 与有界 GC |
| `backend/internal/privatekey/store_test.go` | 引用/lease/claim、未知文件、宽限、卷/DB 错误、删除重试、共享末引用、崩溃遗留状态 |
| `backend/internal/server/types.go` | 输入存在性、安全 View、认证 Record 与校验 |
| `backend/internal/server/store.go` | 新凭据字段查询与原子存取 |
| `backend/internal/server/credentials.go` | 两模式变更规则、密文保持、Host Key 后的按需 Signer、私钥存储组装 |
| `backend/internal/server/service.go` | 启动解密、Create/Update/Delete 引用事务及 uncertain/reload |
| `backend/internal/server/probe.go` | Test 独立 lease，probe 清除新增秘密 |
| `backend/internal/server/session.go` | Creation 独立文件 lease，initialize/取消/Open 全出口释放 |
| `backend/internal/server/private_key_test.go` | CRUD/切换/真实认证终端、故障隔离、回滚、不确定提交及 Test/Creation 对 GC 保护 |
| `backend/internal/server/server_test.go` | 安全 DTO 断言兼容 auth_type 元数据，不放宽秘密断言 |
| `backend/internal/sshclient/test.go` | PublicKeysCallback 共用既有 dial/Host Key，区分本地错误、释放认证闭包 |
| `backend/internal/testutil/terminal.go` | 真实 SSH 对端公钥认证计数及阻塞门，保留密码对端 |
| `backend/internal/httpapi/private_key_request.go` | 独立上传准入、有界原始 header 检查、NextRawPart、精确两 part |
| `backend/internal/httpapi/management.go` | 原 POST/PUT 按 Content-Type 分流并保证退出清理 |
| `backend/internal/httpapi/response.go` | 五个私钥 HTTP 错误及两模式适用的 SSH_AUTH_FAILED 提示 |
| `backend/internal/httpapi/private_key_request_test.go` | part/header/预算/忙/端点保存/普通编辑互斥测试 |
| `backend/internal/httpapi/management_test.go` | 隔离测试目录及带 auth_type 的数据库故障夹具 |
| `backend/internal/httpapi/browser_fixture_test.go` | 显式环境开关的真实 SQLite/SSH/HTTP/Session/WS 浏览器夹具，默认跳过 |
| `backend/internal/maintenance/lock.go` | 服务/离线命令排他 flock |
| `backend/internal/maintenance/backup.go` | 停机恢复集、manifest/完整性/秘密校验、全新目标恢复、inode 重登记及目标复核/目录同步 |
| `backend/internal/maintenance/backup_test.go` | 源目录移走后的历史恢复与真实 SSH、锁互斥、无历史材料、篡改及未登记 WAL 拒绝 |
| `backend/cmd/server/main.go` | serve/backup/verify/restore CLI，秘密不放命令行/普通日志 |
| `backend/cmd/server/run.go` | 开库前维护锁、历史材料提示、对账及10分钟 GC/统一退出 |
| `backend/go.mod` | 现有 x/sys 提为直接依赖；补齐锁定版本的 Darwin SQLite 间接依赖，无版本升级；go.sum 未变 |
| `frontend/src/api/management.ts` | 认证类型与 FormData 分支，保留 Cookie/no-store/signal/响应/失效处理 |
| `frontend/src/components/server/ServerForm.vue` | 认证切换、文件/口令、预算/清理、dirty 门禁、已保存测试及独立主机确认 |
| `frontend/src/pages/HomePage.vue` | 表单 key 和退出中禁用接线 |
| `frontend/src/components/layout/Modal.vue` | useId 防止嵌套主机确认与表单共用 aria-labelledby |
| `frontend/tests/private-key-form.test.mjs` | 5 项真实 SFC/请求组合测试 |
| `frontend/tests/browser/stage-6-k.mjs` | Chrome + 真实后端端到端测试及截图，不加入产品依赖 |
| `Dockerfile` | 非 root 镜像内创建并赋权专用0700目录 |
| `docker-compose.yml` | 与 DB 分离的命名私钥卷 |
| `README.md` | 当前能力、唯一配置、卷与离线命令入口 |
| `doc/stage-6-k-preflight-report.md` | 编码前调查记录，标明用户已解除分支/契约阻塞 |
| `doc/stage-6-k-design-contract.md` | 6-K.0 定稿、浏览器 filename 空值、身份字段与实际同步协议 |
| `doc/stage-6-k-operations.md` | UID/权限/卷、停机顺序、敏感恢复集与恢复步骤 |
| `doc/stage-6-implementation-plan.md` | 登记本轮授权和实施状态，保留原设计修订历史 |
| `doc/web-ssh-design-development-plan.md` | 实际错误码、对象表、multipart 有界读取契约 |
| `doc/web-ssh-architecture-implementation-plan.md`、`doc/web-ssh-implementation-stages.md` | 连接6-K定稿、实施证据与运维说明 |
| 本报告与 `doc/测试报告/stage-6-k-logs/` | 新增验证证据；不覆盖历史证据目录 |

## 3. Migration

新增002，001未变。对象表使用64位小写十六进制 ID、SHA256、原始大小、创建身份 device/inode、promoting/ready/delete_claimed/deleted、候选时刻和删除尝试记录。

同一 SQLite 迁移事务复制 servers/sessions，先移除子表再重建父表及子表，回填全部原字段、索引和 `sessions.server_id ON DELETE CASCADE`，foreign_key_check 后才记录版本。旧记录变为 password，v1 密文字节不重新加密。新认证 CHECK 禁止混合凭据。

Passphrase 为 `2 || nonce(12) || GCM ciphertext/tag`；AAD 为版本字节、固定用途长度/字节及 serverID 长度/字节，长度采用 uint32 大端。无口令 SQL NULL；旧密码仍 v1。新旧密文均通过实际解密验证主密钥；没有历史材料时明确无法验证历史匹配性。

## 4. 目录、配置及生命周期

固定 `/var/lib/web-ssh/ssh-keys`，最终目录0700、文件0600、实际进程属主；逐层拒绝符号链接。Docker UID/GID10001，独立命名卷，bind mount 需宿主预建赋权。文件名不采纳用户输入，不进入静态资源或日志。

唯一新增 `ssh.private_key_gc_grace_period` 默认86400秒。固定128KiB请求、48KiBmetadata、4096字节口令、8KiB原始headers、每part最多8项、64KiB文件、RSA16384bit。上传2与解析1为全局独立名额，满额立即拒绝。取消/截止只丢弃结果，不提前释放正在计算的槽位；bcrypt上限仍为固定库2048。

验证后先登记账本、排他创建暂存文件、登记inode、原始写入/fsync、linkat不覆盖提升、删除暂存名/fsync，再登记ready。上传lease覆盖提升和引用提交。服务器锁→登录读锁→存储锁→短DB事务；GC不反向取业务锁。引用变更和候选更新同事务，Uncertain保留新旧对象直到reload确认事实。

Test与Creation各持独立短文件lease，Session名额不能替代文件保护；已有终端不长期持文件或Signer。GC先在统一同步边界核对引用/lease/uncertain/宽限并持久化claim，才解锁做安全文件操作；claim拒绝新引用和lease。每轮最多100检查/20尝试/1秒软预算，固定10分钟；失败持久化次数/类别/时刻并重试。单实例串行sweep避免重复执行。

启动对账在迁移、历史秘密验证和Session元数据恢复后进行；候选时刻不重置。未知文件不扫描删除；归属证据缺失的创建残留宁可保留。卷不可用暂停GC/写入，DB错误禁止GC。文件缺失/损坏保留引用和管理修复能力；任意Passphrase解密失败阻止启动，不臆断主密钥或数据具体根因。

## 5. 备份与恢复

见 [操作说明](../stage-6-k-operations.md)。先停入口、禁自动重启、停进程并确认DB关闭，然后通过同程序的 `-operation backup -config FILE -recovery-set NEW_DIR` 创建静止恢复集；`verify` 仅用恢复集自身；`restore` 拒绝已有DB/非空私钥目标。

恢复集含DB/仍存在WAL、全部引用私钥原始副本、manifest大小/SHA256/程序与结构版本、匹配主密钥及独立保管说明。核对DB integrity/FK、引用、摘要、全部历史秘密和私钥解析。恢复后重登记物理inode，再对目标副本验证摘要/秘密并同步目录，拒绝未登记的非空WAL；主密钥由运维按归档来源注入，不覆盖外部secret配置。没有备份保留默认策略，只有离线保护，不支持在线备份。

## 6. 实际验证

环境：macOS、Go1.26.5、Node24.16.0、Chrome157.0.8103.79；本机生成的隔离SSH对端只提供真实协议认证和字节回显，不冒充Ubuntu系统PTY。

| 命令/场景 | 真实结果 |
|---|---|
| `cd frontend && npm test` | 314通过，0失败 |
| `npm run build` | TypeScript和生产构建通过；单chunk504.67kB提示，未为此扩大范围重构 |
| `cd backend && go test ./...` | 失败：仅既有WebSocket慢写writer子测试；其余包通过 |
| 临时原始HEAD副本 `go test -mod=mod ./internal/websocket -run '^TestBlockedRealTCPWriterAndBoundFailureRelease/writer$' -count=1` | 同样失败：`connection_test.go:517 未验证真实写超时 NETWORK_ERROR`；临时副本因Darwin需补齐既有版本依赖，产品代码未改测试 |
| `go test ./... -skip 'TestBlockedRealTCPWriterAndBoundFailureRelease/writer'` | 所有其余场景通过，明确只排除此子测试；bound子测试仍执行 |
| `CGO_ENABLED=1 go test -race ./...` 首轮 | 无race报告；失败为既有writer及新增加密认证测试1秒期限在race下不足 |
| 仅将新增测试握手期限改为10秒后，`CGO_ENABLED=1 go test -race ./internal/server ./internal/maintenance` | 通过；产品timeout/KDF设置未改 |
| `CGO_ENABLED=1 go test -race ./... -skip 'TestBlockedRealTCPWriterAndBoundFailureRelease/writer'` | 所有其余场景通过，无race报告 |
| 后补 `go test -race ./internal/server -run '^TestPrivateKeyUncertainReplacementProtectsBothObjectsUntilReload$' -count=1` | 已提交/未提交两分支均通过 |
| 最后恢复补强后 `CGO_ENABLED=1 go test -race ./internal/maintenance` | 通过，含目标复核及未登记WAL拒绝 |
| `go vet ./...`；最后补强后 `go vet ./internal/maintenance ./cmd/server` | 通过 |
| `go build -trimpath -o bin/web-ssh ./cmd/server` | 通过，已嵌入本轮前端生产资源 |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/codex/web-ssh-6k/web-ssh-linux-amd64 ./cmd/server` | 通过；不代表容器运行验收 |
| `PLAYWRIGHT_MODULE=<执行环境playwright/index.mjs> node tests/browser/stage-6-k.mjs` | 5个真实端到端流程通过；真实Go/SQLite/SSH/Session/WS，无响应夹具替代API |
| `git diff --check` | 通过；新增文本也另行检查尾随空白 |

浏览器覆盖：登录→原生FormData加密私钥创建→编辑内测试→人工主机确认→真实公钥认证→普通编辑保留→错误替换保留旧配置→真实终端回显→390px窄屏→password/key双向切换→再测试。无未捕获页面异常。三张截图已查看，窄屏长表单可滚动，保存按钮实际点击成功。证据见 [results](stage-6-k-logs/results.json)及[最终脚本复验](stage-6-k-logs/results-final.json)、[桌面测试](stage-6-k-logs/desktop-key-test.png)、[真实终端](stage-6-k-logs/desktop-terminal.png)、[窄屏表单](stage-6-k-logs/narrow-key-form.png)。

后端测试覆盖：无/正确/错误口令，OpenSSH RSA/EC/Ed25519、PKCS1、PKCS8、SEC1、原生旧PEM加密；超大RSA三种容器、16384bit低成本结构边界和多素数；固定库2049轮拒绝；真实64轮bcrypt取消和deadline保持槽位。multipart预算/异常headers/缺失重复part/CTE/截断/忙；CRUD与两种认证真实连接；短lease/claim/卷DB故障/未知文件/重启宽限/重试/共享引用；真实Test/Creation与替换GC并发；回滚与提交结果不确定两种DB事实；缺失损坏文件启动隔离及损坏口令阻止启动；源目录移走后的自包含恢复和原凭据真实认证。

崩溃测试通过重建提升前、link后、提升后遗留文件/账本状态来验证reconciliation，未对真实主机进行断电或SIGKILL注入。16384bit边界是结构预检证据，未生成并认证完整16384bit RSA；2048轮未做性能基准，未降低上限。取消与超时使用真实KDF和context截止验证共享解析层；未额外执行真实HTTP网络超时中止KDF的端到端专项。

## 7. 未验事项、已知风险与设计差异

1. Android Chrome/iOS Safari真机未执行，缺少本轮设备会话和输入法/系统信息；390px桌面Chrome不替代它们。真实Docker镜像构建/Compose/UID挂载/重建、Ubuntu SSH及远程生产备份恢复未执行，本轮未安装Docker或部署。
2. 全量Go测试和全量race不能标记全绿：慢写writer子测试在原始HEAD和本轮均失败。其要求writeFailures>0，本机连接先因NETWORK_ERROR终止，当前没有足够证据把根因定为具体内核机制。未改冻结测试、生产WS实现或协议。完整其余回归/race已过。
3. 不可取消的同步KDF/文件IO可超过HTTP或1秒软预算；槽位/lease等待真实结束。Go内存中的字符串/库副本不承诺完整擦除。最高允许KDF/RSA尚无本机最坏耗时基准。
4. 同UID恶意进程和拥有主密钥/归档写权限的主体属于部署信任边界；恢复集校验和不能抵抗内容和manifest一起篡改。维护锁不约束旧程序或绕过约定的外部DB工具，必须真正停机。
5. 缺少inode归属证据的极早创建残留和未知文件保留，可能占磁盘；本轮没有新增自动删除未知文件或离线清理命令。删除墓碑也保留。
6. 对批准报告的实现细化：metadata接受缺省filename或原生FormData的空filename，非空拒绝；硬限额原始body先有界内存读取，再预检headers/NextRawPart；对象增加device/inode以证明归属，restore重登记目标身份；维护保护由服务/离线命令全程排他flock实现。上述细节均已同步6-K.0和核心文档，没有改变产品规则。
7. 原第六阶段计划保留旧“文档轮未授权”历史正文，顶部已明确本轮授权和实现状态，不能将原AK未来清单当通过记录。没有未实现的本轮核心认证路径；上述环境验证和性能/故障专项仍未完成，不宣称全部产品验收结束。

最终验证未执行任何commit/push或生产数据写操作。生成资源/二进制仍在项目原有忽略输出目录；代理主动临时文件均在 `/tmp/codex/`。
