# Web SSH 第四阶段实施报告

- 日期：2026-11-15。
- 阶段：Session 生命周期与输出缓存。
- 实施基线：`codex/第三阶段`，HEAD `34d7e04424a60c2bdcbf71006993aaa6889e60e0`。
- 结论：第四阶段代码实现及本地可执行验收完成，待人工代码审查；生产/代理/真机验收仍待执行，不宣称全部环境通过。
- 未提交、未推送、未切换分支；未进入第五阶段；五份实施依据未修改。

## 1. 实施前事实与文件保护

本轮开始时工作区及暂存区为空。此前三份设计修订已进入当前 HEAD，并非仍为未提交差异；核对内容后保留当前版本。最近提交为 `34d7e04`、`f7fd93a`、`65abdf2`（问题修复）及 `5635982`（第三阶段实现）。

`luan-qi-ba-zao.txt` 在实施前后 SHA-256 均为 `6dd12e2c0ad00639dcb36ee42c1ec0570a41f17a4082933ac0ae8a92370ff2c0`。`web-ssh-stage04.zip` 在本轮开始和结束时均不在仓库根目录，本轮没有删除、移动或覆盖该文件。没有清理用户工作区。

## 2. 实际修改文件

| 文件 | 原因与实际变化 |
|---|---|
| `backend/internal/config/types.go` | 新增独立 HTTP 配置组及 CredentialTimeout |
| `backend/internal/config/defaults.yaml` | 新字段默认5秒，其他默认值不变 |
| `backend/internal/config/config.go` | 严格解析兼容新组；正整数、duration、浏览器计时器范围校验 |
| `config.example.yaml` | 与内置默认配置逐字一致 |
| `backend/internal/httpapi/management.go` | client_settings 独立下发 credential_timeout |
| `backend/internal/config/config_test.go` | 缺省、独立值、类型、零/负数和溢出拒绝 |
| `backend/internal/httpapi/management_test.go` | 实际认证 /me 返回两项不同配置值 |
| `backend/internal/session/session_test.go` | 凭证消费、代次、取消提交、离线扫描、原始字节缓存和100次回收 |
| `backend/internal/websocket/connection_test.go` | HTTP/首bind期限独立；100轮真实半帧头阻塞/关闭及应用停止 |
| `frontend/src/terminal/client.ts` | 单任务恢复、两种Promise、同步stop、任务身份、有限预算、结构化通知和尺寸发送交错修正 |
| `frontend/src/terminal/output.ts` | callback后建立稳定资格、一次消费、重建清资格、就绪通知 |
| `frontend/src/terminal/recovery.ts`（新增） | 每个Pane私有的详情请求、取消、候选及人工恢复所有者，不另设自动恢复循环 |
| `frontend/src/api/sessions.ts` | SessionView运行时字段与目标ID校验 |
| `frontend/src/api/management.ts` | 复用认证过期通知入口供WS明确AUTH_REQUIRED调用 |
| `frontend/src/components/terminal/TerminalPane.vue` | 两步人工恢复、查询取消、独立忙状态、停止入口、尺寸/焦点和关闭语义 |
| `frontend/src/pages/HomePage.vue` | 持有Pane停止入口；退出前同步封闭准入；logout与排空并行 |
| `frontend/src/stores/auth.ts` | 单在途logout、保留失败意图、身份epoch隔离迟到/me |
| `frontend/src/router/index.ts` | 退出未完成时AUTH_REQUIRED/路由检查不清退出意图 |
| `frontend/src/pages/LoginPage.vue` | 退出未完成时禁止新登录覆盖Cookie，保留退出重试 |
| `frontend/tests/terminal.test.mjs` | 真实client/tracker/recovery类配合可控时钟、fetch/WS/TerminalPort替身验证固定交错 |
| `frontend/tests/auth.test.mjs`（新增） | 实际auth store的退出失败、重复请求、AUTH_REQUIRED及迟到/me竞争 |
| `doc/stage-4-implementation-report.md`（新增） | 本报告 |

合计19个已跟踪文件修改、3个新增文件。后端 Session/Binding/Buffer/SSH/WS 生命周期业务实现复用第三阶段代码，本轮新增针对性验证，没有为阶段名称重写已满足契约的机制。无数据库迁移、新接口、主动takeover或会话列表；Go/npm依赖清单及锁文件均未改动。

## 3. 配置拆分

| 配置 | 默认值/单位 | 下发字段 | 实际用途 |
|---|---|---|---|
| http.credential_timeout | 正整数5秒 | client_settings.credential_timeout | 浏览器凭证POST定时Abort |
| websocket.bind_timeout | 正整数5秒 | client_settings.bind_timeout | 服务端升级后首条完整bind等待 |

两字段不共用变量、不互相回退。前端缺字段或越界拒绝初始化。HTTP timer在响应处理finally清除；Abort后迟到凭证还须通过signal及任务身份检查，不能继续创建WS。Abort不证明服务端未签发凭证。

详情GET没有本地自动计时器，由用户、任务切换和生命周期取消。server.request_timeout、操作门等待、WS写入/Ping/关闭和客户端完整前缀算式均保持原定义。

## 4. 自动恢复与Promise

每标签一个TerminalClient；任务身份在第一处异步执行前发布。`connect()`返回首次尝试的ConnectionResult Promise，完整绑定前缀即可成功结束，不等待历史全部处理或在线周期。`run()`/`completion`表示长期任务，覆盖连接、在线、断开、排空、等待与最终清理，最后返回带taskId/code的RecoveryState。

首个用户尝试不计自动次数；HTTP/WS/本地失败共用预算；每次自动尝试重新Prepare，使用attach及最后成功获知的代次。等待间隔在settle和发送收尾完成后起算。只有通过校验的bind_success更新自动代次。

tracker在历史write回调完成后建立readySince，断线结束入口立即消费稳定资格，避免排空时间补足稳定期。一次稳定可重置一次；新连接需重新满足稳定期。连续HTTP失败或立即OUTPUT_GAP不能无限重置。预算耗尽显示原因并停止联网。

## 5. stop/dispose与竞争保护

stop先同步标记任务stopped，然后取消凭证、重试、当前attempt；即使controller/socket/finish尚不存在也不能继续。检查任务身份及外部登录准入贯穿settle、HTTP完成、WS构造、onopen/onmessage和发送路径。

新任务拒绝越过旧任务或停止收尾；旧handler移除，捕获的迟到回调仍检查任务/socket身份。stop保留终端并有限排空；dispose同步不可逆、与tracker销毁并行，重复调用复用Promise，销毁期间不重建实例。发送任务不跨连接重放输入。

## 6. 人工恢复和查询

旧任务结束后，用户先点击“刷新 Session 状态”。GET只有展示快照和候选功能，不修改客户端代次、不发凭证、不建WS。再次点击“手动恢复”在首次await前消费候选和锁定动作，等待旧停止收尾后以候选代次启动独立预算的attach任务。

查询序号、AbortController、任务ID和准入检查隔离乱序/迟到响应；新查询立即清旧候选，失败不回退props。CONNECTING/CLOSING不可恢复；CONNECTED只允许普通attach有限等待，不替换有效Binding。SESSION_NOT_FOUND标记不可恢复。

SESSION_NOT_FOUND/SESSION_NOT_READY终止后由Pane发起一次诊断GET；诊断不创建候选、不递归查询。显式刷新和诊断使用同一个查询所有者。

## 7. 输出、终端和输入

沿用receiveCursor与processedOffset，只有write callback推进处理偏移。普通故障丢未提交队列、等待唯一在途write；排空超时重建到0，旧terminalEpoch回调失效。截断仅在合法history_truncated后重建并设声明基线，提示明确后缀不能恢复完整历史画面。

factory每次安装并清理onData/onBinary/onResize；保存最近有效尺寸。完整前缀后可发送resize，历史回放处理完成前输入及自动应答均被门控。活动可见标签才fit和显式聚焦，后台自动恢复不抢焦点。修正空发送完成与resize到达交错时最新尺寸未再次发送的问题。

## 8. 登出、关闭和生命周期

HomePage首次await前设置logoutPending，取消页面操作并调用所有Pane.stop；logout HTTP与终端排空并行。失败保留身份和退出意图，显示重试；AUTH_REQUIRED不能将未完成退出改成成功或普通到期。logout串行，迟到/me不能恢复身份，退出未完成不允许重新登录覆盖Cookie。

关闭标签只dispose本地；关闭SSH有独立确认并调用既有DELETE。DELETE成功显示“已请求关闭、正在回收”，不伪报CLOSED。当前后端Detach不增代，BIND_BUSY不消费凭证、不替换Binding，Close/Sweep/旧回调竞争沿用既有锁和幂等回收。

## 9. 错误码与关闭码

明确业务结果优先于后续Close。BIND_CONFLICT、AUTH_REQUIRED、INPUT_BACKPRESSURE及协议错误停止；INVALID_CREDENTIAL/BIND_BUSY/OUTPUT_GAP等使用原有限集合；4001和1000停止，4002、1006、1011、1012、1013有限恢复。裸1008不猜BIND_BUSY，裸4003不自动纳入可重试集合；本地队列过载主动close(4003)后按本地原因恢复。

Upgrade失败由浏览器通用onerror按NETWORK_ERROR处理，不假定能读HTTP JSON业务码。未引入invalid_bind或retryable字段。没有输入自动重发。

## 10. 自动化测试及真实网络证据

前端28项全部通过，无失败/跳过/取消。覆盖独立HTTP期限、空controller停止、迟到凭证及handler、双Promise、回调输入门禁、resize、HTTP/WS共享预算、稳定一次消费、settle后间隔、全部停止/重试业务分类、裸关闭码、连续缺口、两步人工恢复、详情乱序与取消、本地过载/重建、退出意图及认证竞争。

Go新增测试验证：

- 实际/me独立值2秒与7秒；真实WS的HTTP期限9秒、首bind期限1秒互不替代。
- 操作门忙及提交前取消未消费凭证、未增代；Prepare忙未签发；Detach不增代；旧输入/resize/Detach无效。
- 快照提交前离线到期拒绝，成功重绑后旧Detach及Sweep不关闭新Binding。
- 原始UTF-8/ANSI、CR/LF、空/单字节、双上限、所有分割位置、稳定快照和实时缺口。
- 100次真实环回SSH创建/绑定/并发关闭，活动索引/缓存/服务器名额释放；定向运行goroutine基线9、结束7。
- 100轮真实TCP/WS绑定、四类半帧头阻塞和硬关闭，末轮应用Stop；每轮worker/Supervisor完成、WS名额归零，SSH Shell始终只有1个；定向运行goroutine基线26、结束26。

原有SSH操作超时、Keepalive、元数据故障、名额、帧头/Pong、慢写、输入队列、应用退出等测试也在完整Go及Race运行中通过。协程计数为有界重复场景观察，不声称证明任意部署下永无泄漏。

## 11. 实际命令及结果

环境：WSL Ubuntu，Go 1.26.5 linux/amd64、Node v24.21.0、npm 11.19.0。命令通过用户规定的WSL bash -ic执行。未安装Docker或升级依赖。

| 工作目录 | 命令 | 结果 |
|---|---|---|
| backend | go test ./internal/config ./internal/httpapi | 通过 |
| backend | go test ./internal/websocket -run TestBindTimeoutAndConcurrentTermination -count=1 | 通过，约2秒 |
| backend | go test ./internal/session ./internal/websocket -run 'TestBusyCredential\|TestAttachSnapshot\|TestBufferRaw\|TestOneHundred' -count=1 -v | 全部通过 |
| backend | go vet ./... | 退出0，0.375秒 |
| backend | go test ./... | 退出0，23.740秒 |
| backend | go test -race ./... | 退出0，27.219秒，无race报告 |
| backend | go build ./... | 退出0，0.667秒 |
| frontend | npm test | 最终28/28通过，退出0 |
| frontend | npm run build | 类型检查及Vite生产构建通过，退出0 |
| backend | go build -o /tmp/codex/web-ssh-stage4/server ./cmd/server | 最终前端嵌入构建通过 |
| 仓库根 | git diff --check | 退出0；仅既有LF/CRLF提示 |
| 仓库根 | git status --short / git diff --stat / git diff --cached --name-only | 完成范围复核，暂存区为空 |

对本轮Go文件执行gofmt；前端使用此前已缓存的Prettier，不下载、不写依赖。初期新增resize测试先失败：发现空发送交错，并将测试发送容量设为可容纳resize控制消息的128字节；修正实现后通过。另一次新增模拟时钟测试在drain计时器注册前推进时钟导致等待，停止该测试进程后补足固定异步等待并重跑；未降低产品断言或删除原测试。

完整原始日志位于 `/tmp/codex/web-ssh-stage4/`：`go-vet.log`、`go-test.log`、`go-race.log`、`go-build.log`、`frontend-test.log`、`frontend-build.log`、`embedded-build.log`；命令/退出码/耗时见 `validation.json`、`frontend-validation.json`。临时目录可能被系统清理，下节及本报告保留测试方法和结论。

## 12. 浏览器、SSH和系统PTY实测

复用已有临时Chromium工具及动态库，使用随机环回端口、隔离SQLite、测试凭据和真实Linux `/dev/ptmx`、`/bin/bash`，未访问生产服务。SSH对端是本地测试程序承载真实PTY/Bash，不冒充生产OpenSSH。

最终入口 `/tmp/codex/web-ssh-stage4/browser-check.mjs`；构建使用本轮最终代码。运行命令：

```bash
FONTCONFIG_FILE=/tmp/codex/web-ssh-stage3/fonts.conf LD_LIBRARY_PATH=/tmp/codex/web-ssh-stage3/browser-libs/usr/lib/x86_64-linux-gnu node /tmp/codex/web-ssh-stage4/browser-check.mjs
```

最终13项通过，页面异常0，应用SIGTERM退出码0。结果：`/tmp/codex/web-ssh-stage4/browser-results.json`；现场目录：`/tmp/codex/web-ssh-stage4/browser-fvt3FT`。

1. UI 创建、Host Key 确认、PTY/Shell 与首次回放。
2. 中文、ANSI、真实 PTY 尺寸同步。
3. 交互式 top 与退出后输入。
4. 断线保留 SSH、离线输出、按回调偏移手动 attach。
5. 双上限缓存、唯一截断前缀、终端实例重建。
6. 隐藏标签按原代次自动 attach，仍为原 Session且不抢焦点。
7. 多标签保留实例及真实 SSH 输入输出隔离。
8. 关闭标签仅解除 Binding，其他 Session 保持。
9. 独立确认关闭 SSH 与详情不存在。
10. bind_success丢失后旧代次冲突停止，查询不联网，再点击按实际代次恢复。
11. 登出失败保留意图并停止所有标签，重试成功后清理页面，SSH未被DELETE。
12. 390px 窄屏基本布局（非真实手机验收）。
13. 真实应用 SIGTERM 退出，保留会话完成回收。

浏览器故障注入使用真实WebSocket客户端close(4002/4003)、拦截一次bind_success投递以及模拟logout STORAGE_ERROR响应；不将这些故障注入称为真实网络切换或真实DB故障。后端DB故障另由Go测试验证。截断输入为120000字节、服务端缓存64KiB，验证唯一history_truncated及单xterm实例。已查看terminal.png与truncated.png；窄屏为390px浏览器视口。

## 13. 未验证环境及服务器验证步骤

仍待验：Docker/Compose启动与停止、Nginx Proxy Manager HTTPS/WSS、用户目标Ubuntu/OpenSSH、跨网络长时间运行、真实手机/软键盘/IME/系统剪贴板、实际Wi-Fi/移动网络切换和后台挂起；本轮交互程序实测top，未重新执行vim。没有安装Docker，没有生产操作。这些不记为通过。

在用户授权的隔离服务器环境执行：

1. 使用现有Dockerfile/Compose构建，配置持久化目录、有效秘密与public_origin；保留两个独立超时键。先确认启动、健康检查、/me字段及严格配置错误行为。
2. 在NPM启用WebSocket代理与HTTPS，核对同源Origin和Secure Cookie。用浏览器确认wss成功，代理拒绝时不会依赖onerror解析HTTP业务码。
3. 连接目标OpenSSH，执行中文/ANSI、stty size、vim/top、大段粘贴；确认回放期间输入自动应答被禁用，resize早于回放完成仍可发送。
4. 断网/恢复并保持Shell任务运行，检查原Session ID、代次、processedOffset及有限预算；制造高输出缺口，预算耗尽后必须先刷新再点击恢复。
5. 双登录只验证普通attach忙/冲突、不主动接管；丢失bind_success后确认不会自动采用查询代次。
6. 标签关闭、明确SSH关闭、登出成功/失败、后台标签恢复分别核查；确认无误删SSH、无抢焦点、无未完成退出误报成功。
7. SIGTERM/Compose停止期间观察创建中、在线、离线、关闭中资源和日志；总预算不逐步重置、退出后端口和名额释放。随后重复运行及观察内存/协程基线，记录实际代理和OpenSSH版本。

## 14. 最终Git状态

分支及HEAD保持本报告开头值，暂存区为空。以下为交付状态（包含本报告）：

```text
 M backend/internal/config/config.go
 M backend/internal/config/config_test.go
 M backend/internal/config/defaults.yaml
 M backend/internal/config/types.go
 M backend/internal/httpapi/management.go
 M backend/internal/httpapi/management_test.go
 M backend/internal/session/session_test.go
 M backend/internal/websocket/connection_test.go
 M config.example.yaml
 M frontend/src/api/management.ts
 M frontend/src/api/sessions.ts
 M frontend/src/components/terminal/TerminalPane.vue
 M frontend/src/pages/HomePage.vue
 M frontend/src/pages/LoginPage.vue
 M frontend/src/router/index.ts
 M frontend/src/stores/auth.ts
 M frontend/src/terminal/client.ts
 M frontend/src/terminal/output.ts
 M frontend/tests/terminal.test.mjs
?? frontend/src/terminal/recovery.ts
?? frontend/tests/auth.test.mjs
?? doc/stage-4-implementation-report.md
```

五份实施依据与HEAD内容一致；未修改锁文件、数据库结构或第五阶段接口。构建产物位于既有忽略目录backend/web/dist，不暂存。未执行commit/push/merge/rebase/reset/clean/checkout/restore。

## 15. 交付结论

第四阶段代码实现及本地Go/Race/前端/真实环回浏览器PTY验证已完成，可以提交人工代码审查。未发现需修改冻结设计的新增阻塞。部署与真机验证仍按第13节待办，不将本地通过扩大为全部环境验收通过。交付后停止，等待人工审查；不提交、不推送、不进入第五阶段。
