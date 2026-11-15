# Stage 3 实施报告：真实终端链路、Binding 与 WebSocket 资源管理

提交说明：本报告下述分支、未暂存及未提交记录为实现完成时的验收快照。此后用户明确授权将第三阶段成果提交到 `codex/第三阶段`；该分支从第二阶段 HEAD `d2fe9ce6540deea964ce5559c066a915a88c7f35` 创建，包含本阶段全部代码、测试和报告。第二阶段分支保持原提交，本次不推送远程。

日期：2026-11-15。实现基线为 `codex/第二阶段-登录与服务器管理`，HEAD `d2fe9ce6540deea964ce5559c066a915a88c7f35`，开始时工作区干净。本次未切换分支、未暂存、未 commit/push/merge/rebase。

## 1. 阶段边界与交付结论

按用户批准的修订计划实施第三阶段。仅支持 `POST /api/sessions/{id}/takeover` 的 `mode=attach`，不支持 `mode=takeover`、在线 Binding 主动替换、主动接管凭证或接管 UI。预期代次不符返回 `BIND_CONFLICT`；代次匹配但存在有效 Binding 返回 `BIND_BUSY`。原任务中的“Binding 原子替换”要求已由用户明确撤回，本报告记录的是原子 attach 提交与旧权限撤销。

已实现真实 SSH Session、PTY/Shell、Session Manager、双上限缓存、一次性 attach 凭证、WebSocket Supervisor、独立心跳期限、底层强制关闭、两类名额及前端 xterm 最小闭环。手动恢复复用既定协议。没有活动 Session 列表 API/UI、自动恢复协调器、自动重试预算或第五阶段业务路径。前端保留既有协议控制类型的校验能力，不代表启用主动 takeover。

未修改冻结的设计、架构、WebSocket 协议和阶段文档，也未改动之前的实施报告。实现中发现的是可按现有文档修复的实现/测试问题，未发现需要改写冻结设计的实质冲突。

## 2. 文档与模块对应

| 冻结依据 | 本次实现 |
|---|---|
| 阶段文档 §5.2；设计 Session API；架构所有权/生命周期 | `server/session.go`、`session/session.go`、`sshclient/terminal.go`、`httpapi/sessions.go` |
| 阶段 §5.3；架构绑定、操作顺序 | `session/binding.go`、`websocket/service.go`、`websocket/connection.go` |
| 架构 §7.5；阶段 §5.5.1 | `websocket/connection.go` 的 Writer、消息计时器、Supervisor、raw Conn 强关与收尾；真实 TCP 专项测试 |
| 阶段 §5.4；架构输出缓存；协议发送顺序与客户端 tracker | `session/buffer.go`、`terminal/output.ts`、`terminal/client.ts`、`TerminalPane.vue` |
| 阶段 §5.5；服务器信任/删除保护、应用退出 | Server/Session/HTTP/WS 真实业务测试，`cmd/server/run.go` 的应用关闭顺序 |

文档路径：`doc/web-ssh-design-development-plan.md`、`doc/web-ssh-architecture-implementation-plan.md`、`doc/web-ssh-websocket-protocol.md`、`doc/web-ssh-implementation-stages.md`。

## 3. 修改与新增文件

下表路径相对于项目根目录；完整 Git 清单见末节。

| 类型 | 文件 | 内容 |
|---|---|---|
| 修改 | `backend/cmd/server/run.go` | 初始化 Session/WS 服务、登录撤销联动、ConnContext、应用绝对退出预算和关库顺序 |
| 修改 | `backend/internal/httpapi/management.go` | 服务装配、Session 路由和 WS Origin 精确校验 |
| 修改 | `backend/internal/httpapi/management_test.go` | Management 测试构造改为具名字段 |
| 修改 | `backend/internal/httpapi/response.go` | Session/Binding/容量/SSH 错误 HTTP 映射与提示 |
| 新增 | `backend/internal/httpapi/sessions.go` | 创建、详情、关闭、attach 签发和 WS 接入；必填代次校验 |
| 新增 | `backend/internal/httpapi/sessions_test.go` | 真实 HTTP/WS/SSH、Origin、权限及 Logout/提交竞争 |
| 修改 | `backend/internal/server/service.go` | 每服务器占用计数与删除同步边界、连接预算 |
| 新增 | `backend/internal/server/session.go` | 固定快照、Lease、短期 Creation、信任变化后的释放与探测 |
| 新增 | `backend/internal/server/session_test.go` | 名额并发、删除、初始失败及实际 SSH 公钥轮换 |
| 新增 | `backend/internal/session/session.go` | Manager、生命周期、SSH 健康检查、离线扫描、元数据写入与幂等回收 |
| 新增 | `backend/internal/session/binding.go` | 凭证、原子 attach、Detach、登录撤销及操作准入 |
| 新增 | `backend/internal/session/buffer.go` | 原始字节环、LF 行限制、稳定快照、绝对偏移和缺口 |
| 新增 | `backend/internal/session/session_test.go` | 实际 Session/SSH、凭证、隔离、关闭、数据库失败和并发测试 |
| 修改 | `backend/internal/sshclient/test.go` | 共用认证拨号、释放认证闭包、保留第二阶段诊断、包装超时错误识别 |
| 新增 | `backend/internal/sshclient/host.go` | 仅含公钥/诊断的独立 Host Key 校验器，支持后续重协商校验 |
| 新增 | `backend/internal/sshclient/terminal.go` | TCP/认证/Channel/PTY/Shell 总建连预算、输入/resize/keepalive、底层关闭 |
| 新增 | `backend/internal/sshclient/terminal_test.go` | 真实 SSH 通道与失败/取消回收 |
| 新增 | `backend/internal/testutil/terminal.go` | 环回真实 SSH 测试对端、阻塞点、回显 Shell、可轮换公钥 |
| 新增 | `backend/internal/websocket/service.go` | Upgrade 前预占、注册全部连接、停止接入、登录撤销与等待回收 |
| 新增 | `backend/internal/websocket/connection.go` | Reader/Writer/执行器/监督器、Ping、有限队列、一次回放和统一关闭 |
| 新增 | `backend/internal/websocket/protocol.go` | 严格消息解码、具名输出 DTO、关闭码 |
| 新增 | `backend/internal/websocket/connection_test.go` | 固定库上的真实 TCP/Upgrade/WS/SSH 专项验收 |
| 修改 | `frontend/package.json`、`frontend/package-lock.json` | 锁定 xterm 6.0.0 与 addon-fit 0.11.0 |
| 修改 | `frontend/src/components/server/HostKeyDialog.vue` | 区分信任后测试/连接动作 |
| 修改 | `frontend/src/pages/HomePage.vue` | 创建终端、多标签和保留实例 |
| 修改 | `frontend/src/styles/base.css` | 终端区域、工具栏及窄屏布局 |
| 修改 | `frontend/vite.config.ts` | 开发 WS 代理 |
| 新增 | `frontend/src/api/sessions.ts` | Session DTO 与 API |
| 新增 | `frontend/src/components/terminal/TerminalPane.vue` | xterm、输入、尺寸、手动恢复、查询状态、关闭确认及清理 |
| 新增 | `frontend/src/terminal/client.ts` | 手动 attach、输入分块与背压、resize 合并、发送任务回收 |
| 新增 | `frontend/src/terminal/output.ts` | 回调偏移、单次在途 write、有界队列、排空与重建 |
| 新增 | `frontend/tests/terminal.test.mjs` | tracker 和手动客户端测试 |
| 新增 | `doc/stage-3-implementation-report.md` | 本报告 |

共修改 12 个已跟踪文件、新增 22 个文件，合计 34 个文件。没有新增数据库迁移；复用已有 sessions 元数据表。`backend/go.mod`、`backend/go.sum` 不变，`coder/websocket` 仍为 v1.8.15。临时浏览器/格式化工具不进入项目依赖。

## 4. Session 所有权、快照与名额

Session context 来自应用，不来自创建 HTTP 请求或 WS。创建请求返回丢失/浏览器断开不会销毁 SSH；未绑定的成功创建从 DISCONNECTED 开始离线计时，最终能由离线扫描回收。

服务器快照与名额预占在服务器状态锁内，与编辑和删除使用相同同步边界。快照固定目标、认证材料和可信公钥；网络操作在锁外执行。初始元数据 INSERT 失败不发布 Session。创建任务在发布前计入工作组，防止关闭遗漏尚未登记的资源；应用停止后不得再发布。

每服务器名额覆盖创建中、在线、离线及资源回收中的 Session。正常关闭在 SSH 资源、工作任务和最终元数据处理完成后释放，Lease 通过 once 防重复释放。未信任目标只做探测，不占正式 Session 名额。预占后实际握手发现公钥变化，先关闭连接并释放名额，再按旧版本发布 probe；并发编辑造成版本变化时拒绝过时结果，不擅自更新信任。

长期 Session 只保存非敏感目标字段和已建立 SSH 资源。短期 Creation 在 Open 返回时清掉记录。HostKeyCallback 不捕获密码密文/主密钥；清空 SSH 配置共享 Auth 切片，释放库配置副本可能保留的密码回调。Go 运行时不保证所有临时字符串副本即时物理擦除。

## 5. Binding 提交、撤销与输入竞争

凭证为 32 随机字节的 Base64URL，内存只按 SHA-256 索引保存；检查登录归属、Session、有效期、容量和预期代次。Stage 3 只生成 attach 凭证。HTTP `mode` 和 `expected_generation` 均必填，缺省/null 代次拒绝，不能默认为零。

attach 先取得 Session 操作门，再取得登录读锁、Session 状态锁及缓存锁，生成稳定快照。提交前重新检查凭证是否仍存在/未过期、登录期限、Session 状态、预期代次及 context。唯一提交点消费凭证、使该 Session 的其他凭证失效、递增 generation、安装 Binding 并改为 CONNECTED。快照或校验失败不消费仍有效的凭证，也不延长离线期限。不存在替换在线 Binding 的分支。

登录撤销与提交通过登录对象同步：撤销先冻结权限，随后清凭证、Detach 对应 Binding，并终止所属登录的全部 WS（含未绑定/升级中）。通知失败不能恢复权限。Detach 立即标记 Binding 无效并取消其 context，不等待网络或在途 SSH 操作。

输入/resize 在执行器捕获所属 Binding；取得操作门后再校验当前身份、generation、登录及 Session 状态。旧排队操作拒绝；已经准入的调用可以完成，后续 attach 必须等待同一操作门。延迟 Detach 按 Binding 指针身份判断，不能撤销新 Binding。SSH 在途调用在 Session 工作组内，操作超时通过统一 Session 关闭解除底层阻塞。

## 6. WebSocket Supervisor、Slot 与关闭顺序

Upgrade 前在服务锁内预占全局 WS 名额并登记连接；应用停止接入使用同一锁，因此已预占但尚未完成 Upgrade 的对象也会被关闭。注册期间持登录读锁，网络 Upgrade 不持此锁。Upgrade 失败归还名额；正常连接直到 Reader、Writer、执行器、计时任务和监督器全部退出后才归还，关闭中的连接仍占名额。

每连接一个 Reader、一个应用 Writer、一个有限操作执行器。普通控制通知走有界队列，队列满触发统一终止；输出直接按缓存游标读取，无无界输出队列。独立终止槽不受控制队列容量阻挡。

关闭流程：

1. 首次终止同步 Detach，确定 `min(终止时刻 + close_timeout, 应用退出期限)`，取消已登记的 Ping context，发布终止信号并唤醒 Writer。
2. Writer 不再准入新消息；已准入的一次写受 write_timeout 和总关闭期限约束，预算内至多尝试一次最终通知及 Close。
3. 独立监督器不获取 Writer 调度锁、不等待 Reader/Close；到期直接关闭 ConnContext 保存的底层 net.Conn。不能用并发 CloseNow 代替该强关。
4. Reader、Writer（含 Ping/Close）、输入执行器和计时任务退出，连接收尾者再等待监督器退出，清快照引用、注销连接并归还名额；收尾者不在被等待组内。

Session 关闭先变 CLOSING、失效凭证与 Binding、取消任务；独立回收者关闭原始 SSH socket，等待创建/输入/读取/keepalive/传输收尾，再提交 CLOSED 元数据、释放缓存和名额、移除活动索引。运行时元数据写失败只记录，不恢复权限、不逆转内存状态。元数据有独立取消上下文和截止定时器，应用退出可收紧既有关闭预算。

应用退出先停止接入并取消业务，在共同绝对期限内关闭 Session/WS、等待 HTTP Handler 与资源任务，再关闭 SQLite。若期限内仍有任务，不提前关库冒险，而是返回退出错误供进程结束处理。

## 7. 心跳、消息限制与输出偏移

严格遵循架构 §7.5：Writer 独立维护 nextPing，空闲时按到期时刻唤醒；持续输出时每个完整消息边界优先检查到期 Ping。业务流量不会重置到期时间。唯一应用 Writer 调用 Conn.Ping，pong_timeout 同时覆盖写 Ping 和等 Pong，最多一个在途 Ping。终止入口同步取消 Ping context，原始 socket 强关只承担解除 Read/Write 阻塞；没有第二个 Ping Writer。

绑定总超时从 Upgrade 成功开始；收到首条完整合法 bind 后停止接收等待，提交的操作门等待另受 operation_wait_timeout 约束。消息体计时在 Reader 返回读取器后启动，跨片不重置；不会给正常空闲 Reader 加消息期限。不完整帧头通过绑定总超时或 Ping/Pong/Close 链路最终回收。固定库读取上限与按消息类型的 limit+1 流式读取同时生效，关闭压缩。

OutputBuffer 只存原始字节，不解码 UTF-8/ANSI。字节容量和 LF/尾行数量双限；绝对偏移保持单调，在 JS 安全整数上界前触发 counter_exhausted。Shell 启动前安装 stdout/stderr，因此早期 banner/prompt 进入缓存。

提交时固定一份 [start,end) 回放；Writer 顺序为 bind_success、必要时一次 history_truncated、回放、实时输出。实时游标落后缓存起点返回 OUTPUT_GAP 并关闭 WS，SSH 继续读取，不追加第二份快照。内存由每 Session 缓存、每连接至多一份快照/一个输出块及有限队列构成，叠加两类名额与凭证容量约束。

前端从认证 client_settings 读取限制；每个 xterm 实例只有一个在途 write。接收游标用于标记块范围，processedOffset 只在 xterm 回调后推进。断线先冻结接收、按原因丢弃或保留待写队列并有期限排空；排空超时重建实例、重置偏移。旧 epoch 回调不能推进新实例。截断提示位于终端字节流外，按服务端基线重建。

## 8. 前端最小闭环

服务器“连接终端”在必要时先人工确认 Host Key，创建 Session 后申请 attach。xterm 提供原始二进制输出、中文/ANSI、输入与粘贴、尺寸同步；输入按配置分块、bufferedAmount 有限等待，发送失败明确提示不会自动补发。尺寸更新合并并受有限等待约束。

多标签通过保留组件实例切换；关闭标签清理本地 WS、事件监听、Observer、发送与写入排空任务，不调用 Session DELETE。关闭远端 SSH 有独立确认。已知 Session 可查询状态和手动恢复；不会因冲突自动刷新 generation/重试，也不增加自动重连协调。

## 9. 测试矩阵与证据性质

| 组 | 用例/实际行为 | 证据性质 |
|---|---|---|
| 服务器预占 | 32 路 Reserve 竞争、幂等 Lease、删除拒绝、初始存储失败、未信任不预占 | 实际服务器服务/SQLite；Reserve 回调为受控初始化任务，另有真实 Manager 创建测试 |
| Host Key | 预占后真实 SSH 对端轮换公钥；释放名额后发布 probe；并发编辑拒绝旧版本；不自动改信任 | 真实环回 TCP/SSH 握手 + 实际服务 |
| SSH 建连 | TCP/认证/PTY/Shell 失败；取消/预算；banner/输入/resize/keepalive | 实际 SSH 客户端及真实协议对端；默认 Shell 为回显测试程序 |
| Session 与快照 | 12 路真实创建仅两个成功；创建时编辑保留完整快照、删除受保护；离线、手动/自然退出、keepalive/操作超时 | 实际 Manager/SSH/SQLite |
| attach 凭证 | 单赢家、无效 offset 不消费、到期/清理提交复查、登录/目标隔离、容量、重复使用、冲突、拒绝 takeover | 实际凭证和 Binding 代码；到期/清理/计数上界用受控时序/状态注入 |
| 权限竞态 | 真正 HTTP Logout 与 WS bind 五轮竞争；旧排队输入拒绝、准入输入完成、旧 Detach 不影响新代次 | 实际登录/数据库/HTTP/WS/SSH；操作准入用测试同步 hook 固定交错 |
| 元数据失败 | SQLite INSERT/UPDATE 触发器中止；不得发布/恢复权限/泄漏名额 | 实际 SQLite 失败；不是磁盘故障模拟 |
| 输出 | 原始字节、UTF-8 切片、LF/CR/尾行、双限、稳定快照、计数耗尽 | 实际 Buffer 单元测试，counter 上界为直接状态注入 |
| 半帧头 | 首字节、16/64 位扩展长度、掩码不完整；非法拼接 Pong；确认服务端读到注入字节 | 真实 TCP/HTTP 1.1 Upgrade、固定 coder/websocket、真实 Binding/SSH；无 mock Reader |
| 正常心跳 | 无输入跨多个消息期限及至少六轮 Ping/Pong、连续输出 Ping 不饥饿、正常拆包和重复恢复 | 真实网络；逐轮输入标记避免旧回放误判 |
| Close/Slot | Close 阻塞底层强关、在途 Ping 显式取消、上限 1 关闭中拒绝、收尾后可重新升级 | 真实网络及实际 Slot/监督器，断言工作组与监督器完成 |
| 接入/退出 | Upgrade 失败、门控真实 Hijack 与 Stop 竞争、应用期限收紧、未绑定/已绑定登录到期 | 实际服务与真实 HTTP/WS；门控 Hijack 仅控制交错 |
| 限制/隔离 | 实际 TCP 堵写、控制队列满、输出缺口只关闭 WS；消息非法/超限、不完整消息体、64 个合法粘贴块有序 | 真实网络/Session；堵写要求 Write 实际失败，不只断言客户端断开 |
| 前端 tracker | 回调前不推进、唯一在途、有界队列、跨快照边界拒绝、排空超时、旧回调、幂等销毁 | 实际 tracker + 可控 TerminalPort 替身，用于控制回调时间，不冒充浏览器 |
| 前端客户端 | attach-only、分块发送、断开不自动发请求、不刷新代次 | 实际 TerminalClient + fetch/WebSocket 替身；真实 UI 另行验收 |
| 浏览器/系统 PTY | 生产前端/应用、真实 Chromium、SSH TCP、Linux /dev/ptmx、/bin/bash、交互程序、resize、断线恢复、多标签、SIGTERM | 独立环境集成，详见下一节 |

测试源中的非生产同步 hook 默认为 nil，仅用于可重复地定位快照与操作准入交错；真实网络专项不以 hook/mock 代替底层 Reader、Writer 或 SSH 连接。

## 10. 最终自动化验证

环境：WSL Ubuntu、Go 1.26.5 linux/amd64、Node v24.21.0、npm 11.19.0。所有开发/构建/测试在 WSL 完成。

| 命令 | 最终结果 |
|---|---|
| `cd backend && go test -count=1 ./...` | 通过，22.08 秒 |
| `cd backend && go test -race -count=1 ./...` | **通过，23.99 秒，无数据竞争报告** |
| `cd backend && go vet ./...` | 通过 |
| `cd backend && go build ./...` | 通过 |
| `cd backend && go mod verify` | 通过，all modules verified |
| `cd frontend && npm test` | 9 项通过、0 失败，包含既有 3 项与本阶段新增 6 项 |
| `cd frontend && npm run build` | vue-tsc 类型检查及 Vite 生产构建通过 |
| `git diff --check` | 通过；Git 的 LF→CRLF 配置提示不是检查错误 |
| `go build -o /tmp/codex/web-ssh-stage3/server ./cmd/server` | 前端最终构建后再次构建嵌入应用，通过 |

原始日志与命令耗时：`/tmp/codex/web-ssh-stage3/final-validation.json`、`go-test-final.log`、`go-race-final.log`、`go-vet-final.log`、`go-build-final.log`、`go-mod-verify-final.log`、`frontend-test-final.log`、`frontend-build-final.log`、`git-diff-check-final.log`。临时目录可能被系统清理，本报告保留结论和方法。

定向回归还包括输出缺口/连续粘贴/正常拆包 5 轮 Race，以及握手取消/超时/资源用例 10 轮 Race，均通过。

## 11. 浏览器与真实 Linux PTY 集成

最终构建验收 **10 项通过，页面异常 0，应用 SIGTERM 退出码 0**。结果文件 `/tmp/codex/web-ssh-stage3/browser-results.json`；本轮目录 `/tmp/codex/web-ssh-stage3/browser-XVjXgg`，其中 `terminal.png`、`truncated.png`、`mobile.png` 已逐张查看。

1. UI 创建、Host Key 确认、PTY/Shell 与首次回放。
2. 中文、ANSI、真实 PTY 尺寸同步。
3. 交互式 top 与退出后输入。
4. 断线保留 SSH、离线输出、按回调偏移手动 attach。
5. 双上限缓存、唯一截断前缀、终端实例重建。
6. 多标签保留实例及真实 SSH 输入输出隔离。
7. 关闭标签仅解除 Binding，其他 Session 保持。
8. 独立确认关闭 SSH 与详情不存在。
9. 390px 窄屏基本布局（非真实手机验收）。
10. 真实应用 SIGTERM 退出，保留会话完成回收。

最终临时配置的缓存容量为 65536 字节，离线生成 120000 个 X 字节及结束标记，确认恢复时仅一个 history_truncated 且只有一个 xterm 实例。尺寸验收比较 resize 消息的 rows/cols 与真实 PTY 的 stty size 输出。输入动作留出 250ms 渲染/标签布局时间，未验证回放尚未处理完时允许输入（协议要求此时禁止输入）。

测试入口为 `/tmp/codex/web-ssh-stage3/browser-check.mjs`，临时 SSH 对端源为 `ssh-main.go`，使用真实 `/dev/ptmx` 分配、PTY ioctl 和 `/bin/bash`；不将仓库默认回显 Shell 称为 OS 终端。

浏览器为临时 Playwright 启动的 Chromium 159.0.8154.6。所需动态库仅下载并解包到临时目录，未安装系统软件；使用隔离 SQLite、随机本地端口与专用测试凭据。浏览器工具不进入项目依赖。

验收运行方式：先 `npm run build`，然后构建上述临时 server，再运行：

```bash
FONTCONFIG_FILE=/tmp/codex/web-ssh-stage3/fonts.conf \
LD_LIBRARY_PATH=/tmp/codex/web-ssh-stage3/browser-libs/usr/lib/x86_64-linux-gnu \
node /tmp/codex/web-ssh-stage3/browser-check.mjs
```

## 12. 过程中发现并修复的问题

- 扩展 Management 字段后既有位置字面量编译失败，改为具名字段，未删除旧测试。
- 半帧头最初只在 ConnContext 包装观察对象，未观察实际被 Accept 读取的连接；改为 Listener 包装实际接受的 TCP 连接，保留服务端读取证据。
- 慢写测试最初假定 SLOW_CLIENT 总能成为第一个终止原因；固定库超时关闭 socket 后，Reader 的 NETWORK_ERROR 可能先到。改为必须观测实际 Write 失败及完整回收，允许两个合法首因，不削弱 SSH 保留断言。
- 第一次全套普通测试的 output-gap 测试误与 Writer 竞争合并通知，造成未检查已经更新的缓存；改为有期限观察缓存状态，仍通过真实 SSH 生产超出缓存的输出并断言 OUTPUT_GAP。失败日志保留为 `go-test-first-full-failure.log`。
- 新增 Host Key 测试最初仅改 Creation 中的旧公钥，触发正确的 SERVER_CONFIG_CHANGED；改为实际轮换 SSH 对端后续握手公钥，保持业务快照不变。该定向失败未另存文件，工具输出保留，后续定向及全套已通过。
- 一轮完整 Race 在握手超时断言失败（非 data race）：SSH 包装了底层超时，原直接 net.Error 类型断言遗漏。改为 errors.As 识别包装错误，并通过 10 轮定向及最终完整 Race。失败日志 `go-race-timeout-failure.log` 保留。
- API 复核发现缺省 expected_generation 会解为零，改为指针必填校验并覆盖缺省/null；会话错误提示按相应语义拆分。
- 代码审查修正完整 bind 接收与提交等待的计时分界、独立监督截止值、应用收紧已有关闭期限与元数据定时器收尾、返回 View 快照时机、认证回调引用释放和前端发送任务等待。
- 浏览器环境最初缺少动态库；临时脚本还出现过 JS 转义、换行断言及未等待交互程序退出提示符的问题，修正脚本/环境后重跑。未以删除交互验证或 mock 浏览器替代。

- 最终浏览器第一轮使用极小 4KiB 缓存，top 整屏输出合法触发 OUTPUT_GAP；提高临时验收容量到 64KiB，并同时提高离线输出到 120000 字节继续验证截断。另一轮在大段回放尚未提交完时立即输入，按前端输入门控没有转发；脚本加入有限渲染等待后完整通过。失败分别保存为 `browser-output-gap-failure.json`、`browser-replay-input-failure.json`，未修改产品缓存/协议来使测试通过。

## 13. 未覆盖环境与限制

- 已完成 WSL Ubuntu 环回 TCP/SSH 和真实 Linux PTY/Bash/Chromium 集成；尚未在用户目标生产 SSH 机器及不同发行版 OpenSSH 上验收，也未做跨网络长时间运行/高负载/内存基线测量。
- 未执行 Docker 安装、镜像构建或 Compose 启动；未验证生产 Ubuntu 部署、Nginx Proxy Manager 的 HTTPS/WSS 反代和代理空闲期限。后续应在已有部署环境执行构建/启动、Origin/Cookie、Upgrade、空闲心跳、断线恢复和 SIGTERM 检查。本次没有为此修改部署配置。
- 窄屏检查为 Chromium 390px 视口，不等同真实手机、IME、系统剪贴板或所有浏览器验收。普通输入/中文输出/粘贴逻辑已测，系统剪贴板交互仍需人工环境验证。
- 没有执行 SSH 长连接主动 rekey 专项网络测试；保留独立 Host Key 回调及同步，认证引用释放通过代码检查与现有测试验证。
- 没有磁盘损坏/掉电恢复验收；SQLite 触发器只验证业务失败处理。运行时历史元数据失败会记录日志，活动内存状态和权限不回滚。
- Stage 3 的恢复为用户对已知 Session 手动触发。页面刷新后的活动列表发现、自动恢复协调、主动 takeover 保留后续阶段，不能用本报告的手动恢复验收替代。

## 14. Git 最终状态

基线与当前 HEAD 相同；全部变更未暂存。冻结文档及 Go 依赖锁文件无 diff。以下 Git 输出在报告完成后采集；`git diff --stat` 仅包含已跟踪文件，新增文件需结合 `git status --short` 与本报告文件表查看。

`git diff --stat`：

```text
 backend/cmd/server/run.go                        | 33 ++++++++-
 backend/internal/httpapi/management.go           | 18 +++--
 backend/internal/httpapi/management_test.go      |  5 +-
 backend/internal/httpapi/response.go             | 18 ++++-
 backend/internal/server/service.go               | 31 ++++----
 backend/internal/sshclient/test.go               | 93 +++++++++++++-----------
 frontend/package-lock.json                       | 17 +++++
 frontend/package.json                            |  4 +-
 frontend/src/components/server/HostKeyDialog.vue | 17 ++++-
 frontend/src/pages/HomePage.vue                  | 80 ++++++++++++++++++--
 frontend/src/styles/base.css                     | 57 +++++++++++++++
 frontend/vite.config.ts                          |  8 +-
 12 files changed, 301 insertions(+), 80 deletions(-)
```

`git status --short`：

```text
 M backend/cmd/server/run.go
 M backend/internal/httpapi/management.go
 M backend/internal/httpapi/management_test.go
 M backend/internal/httpapi/response.go
 M backend/internal/server/service.go
 M backend/internal/sshclient/test.go
 M frontend/package-lock.json
 M frontend/package.json
 M frontend/src/components/server/HostKeyDialog.vue
 M frontend/src/pages/HomePage.vue
 M frontend/src/styles/base.css
 M frontend/vite.config.ts
?? backend/internal/httpapi/sessions.go
?? backend/internal/httpapi/sessions_test.go
?? backend/internal/server/session.go
?? backend/internal/server/session_test.go
?? backend/internal/session/
?? backend/internal/sshclient/host.go
?? backend/internal/sshclient/terminal.go
?? backend/internal/sshclient/terminal_test.go
?? backend/internal/testutil/terminal.go
?? backend/internal/websocket/
?? doc/stage-3-implementation-report.md
?? frontend/src/api/sessions.ts
?? frontend/src/components/terminal/
?? frontend/src/terminal/
?? frontend/tests/terminal.test.mjs
```

`git ls-files --others --exclude-standard` 统计为 22 个新增文件（含本报告）；这些文件不计入上面的 301 行新增/80 行删除。最终仅新增本实施报告，没有修改其他 doc 文件。未进入第四阶段，等待人工审查。
