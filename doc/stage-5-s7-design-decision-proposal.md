# Web SSH 第五阶段 S7 设计裁决准备与任务计划草案（第三轮定点修订）

> 状态：提案稿。本文中的推荐方案、状态机、任务拆分和验收标准均 **待用户裁决，不得视为已冻结设计**。
>
> 本轮是第三轮定点修订：在保留第二轮「HomePage 持有 CloseCoordinator、Pane 只负责关闭 UI」整体架构的前提下，修复关闭状态查询、Session 结束判定、页面刷新后的候选 Pane 规则、关闭与 takeover/recovery 的同步互斥、DELETE 错误分类，并整理裁决问题。没有修改任何生产代码、测试、配置或既有冻结文档，没有开始 S7 编码。

## 0. 本轮范围与修订摘要

### 0.1 第二轮遗留的问题

1. **BLOCKER：`refreshClose()` 可能错误解除 unknown/requested/failed。** 第二轮只规定了 CLOSING 保持、SESSION_NOT_FOUND 转 ended、网络错误保持，没有规定详情返回 CONNECTED/DISCONNECTED 时的行为。若据此回到 idle，就会重新放开 takeover/recovery，破坏「关闭意图优先」。
2. **BLOCKER：`ended` 判定过宽。** 第二轮把「连接任务以 `closed` 停止」与「服务端 SESSION_NOT_FOUND」并列为 ended 依据，容易把本地 Pane 关闭、WebSocket 断开、本地 stop 误当成远端 Session 结束，从而提前释放 Session ID 预占。
3. **MAJOR：页面刷新后的候选 Pane 规则与 S6 方案 B 不一致。** 第二轮写了「详情 GET 失败/超时就不生成候选」，与 S6「HomePage 创建候选 Pane、Pane 负责详情查询」冲突。
4. **MAJOR：关闭与 takeover/recovery 的同步互斥只写了 `closeState === 'idle'`。** 没有定义同步屏障的建立顺序，也没有覆盖 takeover preparing/confirming/connecting、recovery retry_wait/attach、行级关闭与新 Pane 竞争等时序。
5. **MAJOR：DELETE 错误分类缺少真实协议依据。** 「明确业务拒绝转 failed」没有对应到后端执行顺序，容易把 4xx/5xx 直接当成「服务端未执行」。
6. **MAJOR：S7-1/S7-2 边界需要再确认。** 总体拆分合理，但必须写明 S7-1 是完整强耦合单元，S7-2 只加入口。
7. **MINOR：裁决问题过于零散。** 第二轮的 D1-1 至 D1-8 本质是一组所有权决策，应合并为一个整体决策。

### 0.2 本轮修订结论（待用户裁决）

- 关闭状态拆成两个维度：**本地关闭意图**（协调器状态）与**服务端详情状态**（GET 结果）。详情 GET 永远不能把非 idle 的关闭意图改回 idle。
- `ended` 只接受两类服务端权威证据：详情 `SESSION_NOT_FOUND`，或服务端 WebSocket `closed` 控制消息。本地关闭标签、本地 client 关闭、WebSocket 断开、连接任务停止、DELETE 200 都不算。
- 页面刷新后按 S6 方案 B 创建候选 Pane；详情查询失败时保留候选 Pane 并显示错误，不显示接管确认、不申请凭证、不创建 WebSocket。
- 关闭确认采用「同步准入 → 同步本地 stop → 异步 DELETE」的顺序；协调器只提供关闭准入，不复制 takeover/recovery 状态机。
- DELETE 结果按后端真实执行顺序分类：只有能证明「请求在 `Sessions.Close` 之前被拒绝」的错误才进入 `failed`，其余一律 `unknown`。
- 新增 `queryEpoch` 隔离详情 GET 与 DELETE/重试的并发；不使用 `updatedAt` 做请求身份判断。
- 第 18 节的 D1 合并为一个整体决策，D2 行级关闭入口保持独立。

### 0.3 本轮允许与禁止

- 允许：读取当前提案、冻结文档和代码；分析替代方案；只修改 `doc/stage-5-s7-design-decision-proposal.md`；执行文档 diff 与一致性检查。
- 禁止且未执行：修改生产代码、测试、其他文档、S5/S6/第四阶段冻结契约、API、配置、依赖或后端；`git add`、commit、push、merge、rebase、reset、clean；切换分支；部署；连接真实 SSH 或生产环境；开始 S7-0 之后的编码；进入 S8。

### 0.4 第四轮定点修订（本轮）

本轮只处理五类问题，不改变第二轮/第三轮的所有权架构，也不整体重写文档：

| 问题 | 本轮结论 |
|---|---|
| `closed` 能否作为 `ended` 证据 | 可以保留，但依据必须写成「Session 已进入不可逆 CLOSING」，不是「Binding 结束」；补充完整证据链、限制与反例 |
| localStop 同步保证 | 明确同步失效必须发生在返回 Promise 之前；核实当前 `Pane.stop()` 已满足；补充 throw/reject、注册 token 与注销边界 |
| 刷新后旧 DELETE 与新 takeover 竞态 | 补充完整时序，并把「接受该竞态」写入 D1 的接受条件，不作为普通备注 |
| §18 裁决层次 | 拆为 D1（整体架构）与 D1-A～D1-E（独立技术规则），消除「接受 D1 即接受全部细节」的歧义 |
| 接口注释与术语 | 补充 `getCloseState` 四态、注册/注销身份隔离、状态发布规则与术语统一 |

## 1. 审查依据与当前基线

### 1.1 Git 基线（本轮开始时实测）

| 项目 | 实际值 |
|---|---|
| 分支 | `S6` |
| HEAD | `eb9407d2802532f543a18627f060f7ff53d12d36` |
| HEAD 提交说明 | `eb9407d S7`（用户提交，含第二轮提案与本轮开始前的 `luan-qi-ba-zao.txt`） |
| 暂存区 | 空 |
| 工作区 | 干净 |
| 未跟踪文件 | 无 |
| `git diff --check` | 退出码 0 |

最近提交：

```text
eb9407d S7
4ebe668 S6-C2
9ba0ba5 S6-C1
```

保护说明：

- 第二轮结束时提案处于 `AM`（暂存首轮、工作区第二轮）；用户已自行提交为 `eb9407d`。本轮从干净工作区开始，不假设旧状态仍然有效。
- `luan-qi-ba-zao.txt` 已随该提交入库；本轮不修改、不格式化、不还原。
- 本轮只在工作区修改提案文件，不执行 `git add`，不触碰暂存区。

### 1.2 实际阅读的文档

| 文档 | 阅读范围 |
|---|---|
| `doc/stage-5-implementation-plan.md` | §6.1–§6.3 列表刷新与请求所有权；§10 并发场景表；§11.1–§11.3 所有权、五类场景、同目标关闭与新动作；§12.1–§12.5 关闭、登出、页面退出与迟到响应；§13 第四阶段兼容性；§15 S7 行；§16 文件职责；§17.4 F10–F13、B01–B05；§20 验收门槛；§21.2、§21.5 已确认裁决 |
| `doc/stage-5-s6-design-plan.md` | §4 职责与资源所有权；§5 Pane 状态机；§6.5 signOut 最小适配；§10.1–§10.3 互斥与 takeover 状态机；§12 close/dispose；§13 R32、R33 等竞态；§14 UI；§15 文件范围；§18 验收；§19 U1–U9 与残留风险 |
| `doc/stage-5-s6-design-review-report.md` | §2 当前代码真实状态；§3 旧文档差异；§4–§6 采用设计、替换项与反向一致性；§7 用户裁决；MAJOR-3 signOut 失败语义 |
| `doc/web-ssh-design-development-plan.md` | §6.1–§6.5 Session 产品行为；§9 会话管理界面状态表；§10.4 终端工作区；§11.4 Session API 与 DELETE；§11.7 关闭原因 |
| `doc/web-ssh-architecture-implementation-plan.md` | §3.1 锁规则；§4.2 普通断开与离线计时；§4.3 幂等关闭与查询；§5.2 唯一提交点 |
| `doc/stage-4-implementation-plan.md` | §13.1 主动登出顺序；§13.2 失败、过期与重试；§17 前端 UI 与独立操作；§18.4 登出和远端关闭测试计划 |
| `doc/stage-4-implementation-report.md` | §8 登出、关闭和生命周期实际交付说明 |
| `doc/web-ssh-implementation-stages.md` | §7 第五阶段范围、验证与完成条件 |
| `doc/测试报告/stage-5-s5-design-review.md` | §14 中 S5 明确不做的 S7 项 |
| `doc/测试报告/stage-5-s6-c1-implementation-report.md` | C1 实现范围、验证结果、未执行项 |
| `doc/测试报告/stage-5-s6-c2-test-report.md` | C2 T38–T45 结果、真实浏览器证据、回归数据、Git 状态 |
| `doc/web-ssh-prototype-v2.html` | 会话表格行级「打开/关闭」操作原型（第 606 行附近） |

### 1.3 实际检查的代码与测试

生产代码：

| 文件 | 重点 |
|---|---|
| `frontend/src/pages/HomePage.vue` | `openSession`、`registerPane`、`paneClosed`、`paneChanged`、`signOut`、视图切换、SessionManager 挂载参数 |
| `frontend/src/components/session/SessionManager.vue` | 列表控制器、`canRefresh`/`authToken` 监听、visibilitychange、`open` 事件、操作列、`defineExpose` |
| `frontend/src/components/terminal/TerminalPane.vue` | `life`、`stop()`、`close()`、`closeRemote()`、`ended`、初始化、watcher、工具栏、关闭 SSH 确认框、接管确认框 |
| `frontend/src/session/list.ts` | `SessionListController` 的 sequence、Abort、`isAllowed`、`currentIdentity`、dispose |
| `frontend/src/terminal/takeover.ts` | `SessionTakeover` 阶段、三个检查点、`invalidate()` 调用约束、`SESSION_NOT_FOUND` 处理 |
| `frontend/src/terminal/recovery.ts` | `refresh`、`recover`、`stop`、`allowed`、`ended` |
| `frontend/src/terminal/client.ts` | `running`、`taskId`、`connectTakeover`、`stop`、`dispose`、控制消息到任务结果的映射（只读核对 S5 契约） |
| `frontend/src/terminal/output.ts` | `closed` 控制消息解析与 `closeReasons` |
| `frontend/src/api/sessions.ts` | `closeSession`、`listSessions`、`sessionView`、`sessionList` |
| `frontend/src/api/management.ts` | `request`、`AUTH_REQUIRED` 回调、JSON 解析与错误包装、`message` |
| `frontend/src/stores/auth.ts` | `identity`、`logoutPending`、`logoutTask`、`checkIdentity`、`logout`、`expireIdentity` |
| `frontend/src/router/index.ts` | `beforeEach`、`expired`、`router.replace` |
| `frontend/src/components/layout/Modal.vue` | `<dialog showModal>` 行为 |
| `backend/internal/httpapi/sessions.go` | DELETE / 详情 / takeover 路由与返回 |
| `backend/internal/httpapi/management.go` | Origin、readiness、认证、id 格式检查顺序 |
| `backend/internal/httpapi/request.go` | `DecodeJSON` 的错误码 |
| `backend/internal/httpapi/response.go` | 错误码到 HTTP 状态与消息的映射 |
| `backend/internal/session/session.go` | `Find`、`Get`、`Close`、`closeLocked`、`reap` |
| `backend/internal/websocket/connection.go` | `closed` 控制消息的发出条件 |
| `backend/internal/websocket/protocol.go` | `closeCode` 映射 |

测试：

- `frontend/tests/auth.test.mjs`
- `frontend/tests/session-list.test.mjs`
- `frontend/tests/session-takeover.test.mjs`
- `frontend/tests/terminal-pane.test.mjs`
- `frontend/tests/stage-5-c2.test.mjs`
- `frontend/tests/client.test.mjs`（只读核对既有 S5 契约）

### 1.4 后端 DELETE、详情与 closed 协议事实（本轮新增）

| 事实 | 代码位置 | 结论 |
|---|---|---|
| DELETE 处理器只做 decode、`Sessions.Close`、返回 200 | `backend/internal/httpapi/sessions.go:64-71` | 处理器内没有业务失败分支；`Close` 没有返回值可被检查 |
| `Manager.Close` 对不存在或已关闭的 Session 静默返回 | `backend/internal/session/session.go:251-256` | DELETE 200 只代表「请求已被处理」，不代表关闭完成，也不代表该 ID 曾经存在 |
| `Manager.Get` 在 Session 不存在或状态 `CLOSED` 时返回 `SESSION_NOT_FOUND` | `session.go:186-212` | 详情 404 是「该 ID 当前没有可用 Session」的权威结果 |
| `reap` 先关闭终端、等待任务，再置 `CLOSED`，最后从 Map 删除 | `session.go:386-425` | `SESSION_NOT_FOUND` 出现在回收完成或从未存在之后 |
| `closeLocked` 幂等：CLOSING/CLOSED 直接返回 | `session.go:361-365` | 重试 DELETE 幂等，不会产生第二次关闭 |
| 服务端 `closed` 控制消息只在 Binding 以 `closed:` 结束时发出 | `backend/internal/websocket/connection.go:583-600` | 它是服务端主动发出的事件，与本地 socket 关闭不同 |
| 前端把该消息映射为任务结果 `closed` | `frontend/src/terminal/client.ts:540-556` | S5 已区分来源；S7 必须保留，不得用本地 stop 合成 `closed` |
| `request()` 对非 JSON 响应直接抛错 | `frontend/src/api/management.ts:60-88` | 网关 HTML、代理错误无法被识别为后端业务错误，只能归 unknown |
| DELETE 前置拒绝点：Origin、readiness、认证、id 格式、decode | `management.go:250-266`、`management.go:267-282`、`request.go:17-56` | 这些错误发生在 `Sessions.Close` 之前，可证明「本次请求未被接受」 |

### 1.5 三轮修订记录

| 轮次 | 解决的问题 |
|---|---|
| 第一轮 | 关闭、登出、列表失效的总体方案 |
| 第二轮 | 关闭任务从 Pane 上移到 HomePage 的 `CloseCoordinator`；Pane 与无 Pane 共用状态机；`session/close.ts` 成为 S7-1 正式文件 |
| 第三轮（本轮） | 详情 GET 不解除关闭意图；`ended` 只认服务端权威证据；刷新后按 S6 方案 B 保留候选 Pane；同步互斥顺序；DELETE 真实分类；S7-1/S7-2 边界；裁决合并 |

### 1.6 `closed` 控制消息证据核查（第四轮）

核查范围：`Connection.finish` 的全部调用点、`closed` 消息的全部构造点、`Session.reap` 调用链、Session 状态赋值与绑定准入。

| 核查项 | 结论 | 证据 |
|---|---|---|
| `Connection.finish` 调用点 | 仅 1 处：`writer()` 在 `c.reason != ""` 时调用 | `backend/internal/websocket/connection.go:449-453` |
| `closed` 消息构造点 | 仅 1 处：`finish()` 中 `reason` 以 `closed:` 开头 | `connection.go:583-600` |
| `closed:` 原因的唯一来源 | `Session.reap` 中的 `old.end("closed:" + s.reason)` | `backend/internal/session/session.go:403-405` |
| 其他 `Binding.end` 原因 | `taken_over`、`AUTH_REQUIRED`，分别产生 `taken_over`/`error` 消息 | `session/binding.go:51`、`binding.go:245`、`session.go:323` |
| 发出 `closed` 时的 Session 状态 | `CLOSING`：`closeLocked` 已置，`CLOSED` 尚未写入 | `session.go:361-384`、`session.go:403-409` |
| CLOSING 是否可逆 | 否：`canBind` 只接受 CONNECTED/DISCONNECTED；`closeLocked` 对 CLOSING/CLOSED 直接返回；唯一后继是 CLOSED | `binding.go:96-116`、`session.go:361-365`、`session.go:407` |
| 状态写入与准入是否同一临界区 | 是：`Attach` 持 `s.mu` 从 `canBind` 一直到 `status = "CONNECTED"` | `binding.go:141-208` |
| 后端测试 | `close` 场景断言 `closed` + 1000 + 状态 CLOSED；`revoke` 场景断言 `error` + 1008 + DISCONNECTED | `websocket/takeover_test.go:552-566`、`:744` |

结论：`closed` 不是「当前 Binding 结束」的同义词。它只在 Session 生命周期所有者执行 `Close → closeLocked → reap` 后发出；此时 SSH 终端已关闭、任务已排空、状态为不可逆的 CLOSING。它可以保留为 `ended` 证据，但依据必须写成「CLOSING 不可逆」，而不是「消息本身证明 CLOSED」。

## 2. S7 的冻结范围与 S6 基线

### 2.1 冻结任务来源

实施计划 §15 的 S7 行是唯一权威任务定义：

| 项目 | 冻结内容 |
|---|---|
| 依赖 | S6 |
| 修改文件 | `frontend/src/pages/HomePage.vue`、`components/session/SessionManager.vue`、`components/terminal/TerminalPane.vue`、`session/list.ts`、`terminal/takeover.ts`、`api/sessions.ts` 及对应前端测试；必要时扩展 `frontend/tests/auth.test.mjs`；无必需新增生产文件 |
| 实现职责 | 同目标关闭与接管互斥、DELETE 可取消等待、隐藏列表/关闭 Pane 区分（方案 B：隐藏或卸载列表不取消已归 Pane 的候选动作）、`logoutPending` 同步封闭新任务、列表失效刷新；复用 auth/router，不改认证协议 |
| 测试 | F10–F13、B01–B05；DELETE/登出成功与失败/不确定、任务转移、浏览器关闭与迟到响应；只关闭列表不误删 SSH，退出后不联网 |
| 回滚 | 撤销本步退出/关闭/反馈增量；在功能恢复完整前保持新增入口禁用，不回退第四阶段既有 `logoutPending`/stop 语义；前序受依赖部分不单独发布 |

F10–F13 是测试场景，不是独立实现任务；本文第 12 节把它们映射到具体任务和验收条件。

### 2.2 S7 与 S6-C1/C2 的边界

S6 已冻结且 S7 不得改变：

- 候选 Pane 所有权方案 B：SessionManager 只负责列表与 `open` 事件；HomePage 同步预占并创建唯一候选 Pane；Pane `ready` 后详情/确认/取消归 Pane；SessionManager 卸载不取消已移交动作。
- 用户确认前不申请凭证、不创建 WebSocket；`connectTakeover(generation)` 只由 `SessionTakeover.confirm()` 在检查点 3 之后调用，且中间无 await。
- `takeover.invalidate()` 只允许在 `Pane.stop()` 和 `Pane.close()` 内调用；关闭 SSH 通过 `stop()` 使 takeover 失效。
- `client.dispose()` 失败被缓存；清理失败不 emit `closed`，HomePage 保留记录与 Session ID 预占（U1）。
- 登出成功优先，stop/close 失败分别统计、记录和反馈（U9、R32、R33）。
- S5 的 `client.ts`、`output.ts`、`recovery.ts` 不修改；S6 的 `terminal/takeover.ts` 不重写。

S7 只允许在这些边界之上增加：

- 由 HomePage 持有的关闭任务协调器，以及 DELETE 的 pending/requested/unknown/failed 状态。
- 列表行级关闭入口与关闭意图路由。
- 列表同步失效、changed 合并和页面退出后的迟到回调隔离。
- 完整登出流程中 stop/logout/close/跳转的最终语义与反馈。

S7 不得：

- 把列表视图关闭等同于 SSH Session 关闭。
- 把本地 Pane 关闭等同于远端 Session 关闭。
- 让 SessionManager 直接 DELETE，或让 HomePage 与 Pane 同时管理同一 Session 的 DELETE。
- 在登出过程中启动新的列表 GET、详情 GET、DELETE、WebSocket 或 takeover。
- 修改 S5 连接/恢复/绑定契约，或提前实现 S8。

### 2.3 当前实现情况（S6 基线，供修订对照）

| 能力 | 当前实现 | 与本轮目标设计的差异 |
|---|---|---|
| 候选 Pane、确认前零连接 | `TerminalPane.vue` 的 `startMode='candidate'`、`takeover.prepare()` 只 stop + GET | 保留；不改变候选/takeover 流程 |
| 关闭 SSH 确认框 | `TerminalPane.vue:269-284`，独立 `closing` 状态 | 保留 UI，确认后改为调用 HomePage 注入的关闭句柄 |
| DELETE 调用 | `api/sessions.ts:40-42`，`closeSession(id)` 无 `AbortSignal` | S7-1 增加可选 signal，并由协调器持有 controller |
| `closeRemote` | `TerminalPane.vue:366-390`：Pane 内 `closeRequested`、`stop()`、`await closeSession()` | S7-1 把状态/DELETE/abort/重试上移到协调器；Pane 只保留 UI |
| 本地关闭标签 | `TerminalPane.vue:237-239`、`201-227`：只 `close()`，不 DELETE | 保留「不 DELETE」；但 `close()` 不再 abort 协调器任务，只退订 |
| `ended` 判定 | `TerminalPane.vue:174-180` 在连接任务 `stopped` 且 `code === 'closed'` 时置 `ended` | 保留「服务端 closed 消息」这条证据；明确排除本地关闭、WS 断开、连接任务普通停止 |
| 详情查询 | `takeover.prepare()` 的 GET；`SESSION_NOT_FOUND` 时 `markEnded()` | 保留；作为 ended 的权威证据之一，并补充来源标记要求 |
| 行级关闭 | `SessionManager.vue:281-288` 只有「打开」 | S7-2 增加「关闭」，只 emit 意图 |
| 列表失效 | `SessionListController.invalidate/dispose` 已有；`signOut` 未调用 `manager.invalidate()` | S7-3 同步失效 |
| changed 刷新 | `HomePage.vue:109-112` 每次 changed 直接 `manager.refresh()` | S7-3 增加守卫与同批合并 |
| 登出顺序 | `HomePage.vue:353-389` 与 S6 §6.5 一致 | S7-4 增加协调器 dispose 与异常防护 |
| 登出时的 stop/close 失败 | `rejectedSessions` 分别统计并写日志，跳转后写 `authNotice` | 保留；补同步 throw 防护 |
| 同步抛错防护 | `entries.map((entry) => entry.handle.stop())` 在 `try` 之前求值 | S7-4 包装为 Promise |
| 页面卸载后的迟到回调 | 列表有 sequence/dispose；Pane 有 `life`/taskId 守卫；DELETE 没有 abort/epoch | S7-1 由协调器 epoch 隔离；S7-5 补卸载测试 |

### 2.4 本轮目标所有权模型（待用户裁决）

```text
HomePage（页面生命周期）
  │
  ├── CloseCoordinator（每个 HomePage 一个，按 Session ID 管理）
  │     ├── Map<sessionId, CloseTask>
  │     │     ├── state: idle | pending | requested | unknown | failed | ended
  │     │     ├── controller: AbortController | null      // DELETE
  │     │     ├── queryController: AbortController | null // 详情 GET
  │     │     ├── epoch: number                           // DELETE/重试身份
  │     │     ├── queryEpoch: number                      // 详情查询身份
  │     │     ├── promise: Promise<CloseSnapshot> | null
  │     │     ├── localStop: (() => Promise<void>) | null
  │     │     └── subscribers: Set<listener>
  │     ├── requestClose(sessionId)
  │     ├── retryClose(sessionId)
  │     ├── cancelWaiting(sessionId)
  │     ├── refreshClose(sessionId)
  │     ├── getCloseState(sessionId)
  │     ├── subscribeClose(sessionId, listener)
  │     ├── registerLocalStop(sessionId, stop)
  │     └── dispose()
  │
  ├── TerminalPane（只读快照 + UI）
  │     ├── 注册本地 stop（takeover.invalidate + recovery.stop）
  │     ├── 确认关闭 → closeHandle.requestClose()
  │     ├── 取消等待 → closeHandle.cancelWaiting()
  │     ├── 重试 → closeHandle.retryClose()
  │     ├── 刷新状态 → closeHandle.refreshClose()
  │     └── 展示 state/message/serverStatus
  │
  └── SessionManager（只 emit）
        └── emit('close', item) → HomePage → coordinator.requestClose(item.id)
```

所有权不变量：

- 一个 Session ID 在页面生命周期内至多一个 `CloseTask`；同一时刻至多一个有效 DELETE 请求。
- 只有协调器持有 DELETE/详情查询的 AbortController 与 Promise；Pane 和 SessionManager 都不持有。
- Pane 销毁只退订并注销本地 stop，不 abort、不清除任务；协调器继续持有状态。
- 只有 HomePage 卸载/登出调用 `dispose()` 才 abort 全部本地等待；abort 不等于服务端撤销。
- 关闭状态与 takeover/recovery 状态分离：协调器只提供「是否允许接管」的只读判断，不复制 takeover 状态机。

## 3. 问题清单与严重程度（第三轮修订）

| 编号 | 级别 | 问题 | 文档 | 代码/现状 | 是否需用户裁决 |
|---|---|---|---|---|---|
| S7-B1 | BLOCKER | Pane 销毁后，DELETE 的 unknown/closeRequested 随组件消失；重新打开同一 Session 可能绕过关闭意图 | 实施计划 §11.3、§12.2；第四阶段 §17；F10 | `TerminalPane.vue:366-390`、`201-227` | 是（并入 D1） |
| S7-B2 | BLOCKER | Pane 内状态机与无 Pane 路径缺少统一所有权；`session/close.ts` 被列为 S7-2 可选，可能形成两套实现 | 实施计划 §15 S7 行；核心设计 §9 | `SessionManager.vue:281-288`；`HomePage.vue:78-94` | 是（并入 D1） |
| S7-B3 | BLOCKER | `refreshClose()` 未定义详情返回 CONNECTED/DISCONNECTED 的处理，可能把 requested/unknown/failed 解除为 idle | 核心设计 §11.4；第四阶段 §17 | `session/close.ts`（待新增） | 是（D1-A） |
| S7-B4 | BLOCKER | `ended` 判定过宽，可能把本地关闭/WS 断开/连接任务停止当成远端 Session 结束，提前释放预占 | 第四阶段 §17；S6 §12.2 | `TerminalPane.vue:174-180`；`takeover.ts:128-130` | 是（D1-B） |
| S7-M1 | MAJOR | `logoutPending` 在 200 时复位，是否需要保持到本地 close 完成存在语义歧义 | 第四阶段 §13.2；S6 §6.5 | `auth.ts:39-51`；`HomePage.vue:353-389` | 是（D3） |
| S7-M2 | MAJOR | `signOut` 未同步 `invalidate` 列表；`paneChanged` 无登出守卫；`isAllowed` 在同步边界内读取旧 prop | 实施计划 §6.1、§12.2；D1=A | `HomePage.vue:109-112`、`353-389`；`SessionManager.vue:41-45`、`108-113` | 否（实施待办） |
| S7-M3 | MAJOR | 同一同步批次多个 `changed` 未合并为一次 GET | 实施计划 §6.1 D1=A | `HomePage.vue:109-112` | 否（实施待办） |
| S7-M4 | MAJOR | `handle.stop()`/`handle.close()` 同步抛错会逃出 `signOut` 的 `try` | S6 §6.5；R32/R33 | `HomePage.vue:361-363`、`375-381` | 否（健壮性修复） |
| S7-M5 | MAJOR | 关闭协调器与登出的任务失效策略未定义时，可能在 logout 期间继续发起 GET/DELETE | 第四阶段 §13.1；实施计划 §12.2 | 本轮新增设计 | 是（D3 确认） |
| S7-M6 | MAJOR | 刷新后「详情 GET 失败就不生成候选」与 S6 方案 B 冲突 | S6 §10.1、§10.2 | `HomePage.vue:78-94`；`TerminalPane.vue:490-500` | 是（D1） |
| S7-M7 | MAJOR | 关闭与 takeover/recovery 的同步互斥顺序未定义，仅写 `closeState === 'idle'` | S6 §10.1；实施计划 §11.3 | `TerminalPane.vue:196-199`、`366-390`；`takeover.ts:164-186` | 是（D1-C） |
| S7-M8 | MAJOR | DELETE 错误分类缺少后端执行顺序依据，4xx/5xx 不能直接推断服务端未执行 | 核心设计 §11.4；架构 §4.3 | `sessions.go:64-71`；`session.go:251-256`；`management.ts:60-88` | 是（D1-D） |
| S7-B5 | BLOCKER | `closed` 控制消息作为 ended 证据未经核实；可能把 Binding 结束误当 Session 结束 | 第四阶段 §17；S6 §12.2 | `connection.go:583-600`；`session.go:403-405` | 是（D1-B） |
| S7-M9 | MAJOR | localStop 只定义了 Promise 回调，未定义同步失效保证、异常处理与注册注销边界 | S6 §10.1；实施计划 §11.3 | `TerminalPane.vue:196-199`；`client.ts:750-769` | 是（D1-C） |
| S7-N1 | MINOR | DELETE 失败只写通用 `message(error)`，且错误显示在 `dialog` 之外 | F10；第四阶段 §17 | `TerminalPane.vue:379-380`、`269-284` | 否 |
| S7-N2 | MINOR | `closeRemote` 成功但 `life !== 'ready'` 时静默返回；迟到响应没有 epoch 隔离 | S6 §12.2；D1=A | `TerminalPane.vue:373-375` | 否（S7-1 解决） |
| S7-N3 | MINOR | 缺少「关闭标签不发送 DELETE」的显式断言 | 核心设计 §10.4；第四阶段 §17 | `terminal-pane.test.mjs` | 否 |
| S7-N4 | MINOR | 缺少 init_failed/dispose_failed 参与 signOut、recovery `retry_wait` 与关闭组合的测试 | S6 §12.4、R32/R33 | `session-list.test.mjs`、`terminal-pane.test.mjs` | 否 |
| S7-N5 | MINOR | 协调器任务在页面生命周期内的内存回收规则未明确 | 本轮新增设计 | `session/close.ts`（待新增） | 否（S7-1 明确） |
| S7-N6 | MINOR | 缺少「详情查询与重试并发」的显式断言 | 本轮新增设计 | `session/close.ts`（待新增） | 否（S7-1 解决） |
| S7-O1 | OBSERVATION | 路由守卫把网络错误当作身份失效（U6） | S6 §19.2 | `router/index.ts:29-38` | 否（独立风险） |
| S7-O2 | OBSERVATION | logout 成功后 `router.replace` 触发 `/me`，`authNotice` 可能先被改写 | S6 §6.5、R33 | `HomePage.vue:382-385`；`router/index.ts:23-39` | 否 |
| S7-O3 | OBSERVATION | DELETE 与 logout 都没有本地超时；请求悬挂时 UI 只能等待或取消 | D1=A；第四阶段 §13 | `api/sessions.ts`、`auth.ts` | 是（与 D1 一并裁决） |
| S7-O4 | OBSERVATION | 后端 DELETE 对不存在或已关闭的 Session 也返回 200；前端不能靠状态码区分 | 核心设计 §11.4；架构 §4.3 | `sessions.go:64-71`；`session.go:251-256` | 否 |
| S7-O5 | OBSERVATION | `closeRequested` 在 DELETE 成功后保持为真，这是「关闭意图不撤销」的现有实现 | 实施计划 §11.3 | `TerminalPane.vue:370-388` | 否（迁入协调器） |
| S7-O6 | OBSERVATION | 页面刷新后本地 unknown 不可恢复；只能依赖服务端详情查询 | 本轮新增设计限制 | `session/close.ts`（待新增） | 是（D1） |
| S7-O7 | OBSERVATION | 详情 GET 无法证明「先前未完成的 DELETE 不再生效」；刷新后存在残余风险 | 本轮新增设计限制 | `sessions.go:56-63`；`session.go:186-212` | 是（D1） |
| S7-O8 | OBSERVATION | 刷新后旧 DELETE 可能在新 takeover 之后到达；现有接口无法消除 | 本轮新增设计限制 | `sessions.go:64-71`；`session.go:251-256` | 是（D1 接受条件） |

## 4. D1：关闭任务所有权、状态与生命周期

### 4.1 必须区分的概念

| 概念 | 是否可被浏览器改变 | 说明 |
|---|---|---|
| 用户取消本地等待 | 是 | 只停止本地 Promise 的等待，不改变服务端 |
| `AbortController` 终止本地 HTTP | 是 | fetch 以 `AbortError` 结束；请求可能已到达服务端 |
| 服务端取消关闭操作 | 否 | 现有 API 没有「撤销关闭」；DELETE 一旦被服务端接受即进入幂等关闭 |
| 服务端已执行关闭但响应丢失 | 否 | 前端只能标记为 unknown |
| 服务端尚未执行关闭、请求失败 | 部分可推断 | 只有在 `Sessions.Close` 之前发生的明确拒绝可证明；网络错误不能推断 |
| 请求结果无法确认 | — | unknown，必须保留关闭意图 |
| Pane 销毁 | 否 | 只释放本地 UI/终端，不等于关闭服务端，也不等于放弃关闭意图 |
| 详情 GET 返回 CONNECTED/DISCONNECTED | 否 | 只描述查询瞬间的服务端状态，不撤销已经建立的关闭意图 |
| DELETE 返回 200 | 否 | 只代表请求被处理；`Manager.Close` 对不存在/已关闭也静默返回 200 |
| 本地 WebSocket 关闭 / 连接任务停止 | 否 | 不等于远端 Session 已结束 |

结论：**本地 abort 只意味着前端停止等待，不意味着服务端撤销关闭；Pane 销毁也不能成为自动放弃关闭意图的途径；详情 GET 与 DELETE 200 都不能解除关闭意图。**

### 4.2 推荐所有权模型：HomePage 的 CloseCoordinator

推荐在 `frontend/src/session/close.ts` 实现 `CloseCoordinator`，由 HomePage 在页面生命周期内持有。理由：

1. **关闭任务的生命周期长于 Pane。** Pane 关闭标签后，DELETE 可能仍在途或结果未知；状态必须由页面级所有者保留。
2. **单一 DELETE 所有者。** 协调器按 Session ID 去重，Pane 和 SessionManager 都只能调用它，避免双 DELETE。
3. **统一状态机。** Pane 路径与无 Pane 路径共用 pending/requested/unknown/failed 转换、abort、epoch、重试和刷新。
4. **页面卸载有明确边界。** HomePage 卸载时统一 `dispose()`，取消本地等待并隔离迟到响应；不会出现 Pane 和 HomePage 各自 abort 同一请求。
5. **不引入全局 Store。** 协调器是 HomePage 私有的普通类实例，不是全局单例，不使用事件总线或定时轮询。

不推荐把关闭任务继续放在 Pane 内，也不推荐「Pane 关闭时把任务移交给 HomePage」的转移模型：转移需要额外的所有权交接、并发窗口和迟到回调隔离，复杂度高于直接由 HomePage 持有。

### 4.3 每个 Session 的关闭状态机（本地意图，待用户裁决）

状态由协调器按 Session ID 维护；`closeRequested` 不再由 Pane 独立维护，而是 `state !== 'idle'` 的派生值。

| 状态 | 含义 | 进入条件 | 允许操作 | 退出 |
|---|---|---|---|---|
| `idle` | 无关闭意图 | 初始；从未确认关闭 | 接管、手动恢复、刷新详情、关闭 SSH | `requestClose` → `pending` |
| `pending` | 已确认，DELETE 在途；本地 stop 已由协调器触发 | `requestClose` 同步占用 | 取消等待（本地 abort）、关闭标签 | 200 → `requested`；abort/网络/未知错误 → `unknown`；可证明的前置拒绝 → `failed` |
| `requested` | 服务端已接受幂等关闭（200），物理回收未完成 | `pending` 收到 200；或详情 CLOSING 由 unknown/failed 升格 | 刷新状态、幂等重试、关闭标签 | 详情 CLOSING → 保持；SESSION_NOT_FOUND → `ended` |
| `unknown` | 请求已发出但结果无法确认 | `pending` 被 abort、网络错误、响应丢失、无法解析、不可证明的错误 | 刷新状态、幂等重试、关闭标签 | 重试 → `pending`；详情 CLOSING → `requested`；SESSION_NOT_FOUND → `ended` |
| `failed` | 明确证明本次请求未被服务端接受 | `pending` 收到前置拒绝（见 §8.2） | 刷新状态、幂等重试、关闭标签 | 重试 → `pending`；详情 CLOSING → `requested`；SESSION_NOT_FOUND → `ended` |
| `ended` | 服务端权威确认 Session 不存在 | 详情 SESSION_NOT_FOUND，或服务端 `closed` 控制消息 | 关闭本地标签 | 页面卸载时随协调器释放 |

状态转换要点：

1. **只允许向更强的关闭意图转换，不允许回退。** 除 `ended` 外，任何状态都不会回到 `idle`；`requested/unknown/failed` 之间只允许由「详情 CLOSING」升格为 `requested`。
2. `pending` 的「取消等待」只调用协调器的 DELETE `AbortController.abort()`，随后进入 `unknown`；提示必须是「已停止等待，关闭结果未知」，不能是「已取消关闭」。
3. `requested`、`unknown`、`failed` 都允许用户显式重试 DELETE；`Manager.Close` 幂等，重试安全。
4. `ended` 只能由 §4.5 的两类服务端证据产生；DELETE 200、本地 close、WS 断开都不能产生 `ended`。
5. `ended` 作为页面内 tombstone 保留到协调器 dispose，防止列表中尚未更新的旧行再次打开同一 Session；Session ID 不会复用（核心设计 §11.4），tombstone 不会误伤新 Session。
6. 关闭任务与 takeover/recovery 状态分离：协调器只提供 `state === 'idle'` 的只读准入，不管理 takeover 阶段。

### 4.4 服务端详情与本地关闭意图的二维判定（refreshClose 规则）

**本地关闭意图与服务端详情是两个不同维度。** 详情 GET 只更新 `serverStatus` 展示字段，并按下表决定是否改变本地关闭状态；它不能凭 CONNECTED/DISCONNECTED 解除已经建立的关闭意图。

| 当前关闭状态 | 详情结果 | 处理 | 依据 |
|---|---|---|---|
| `idle` | CONNECTED/DISCONNECTED/CLOSING | 保持 `idle`；按 S6 正常候选流程处理（CLOSING 不允许接管） | 协调器没有关闭意图 |
| `pending` | 任意 | 保持 `pending`，不发送详情 GET；DELETE 结果是唯一权威 | 避免 GET 与在途 DELETE 竞争 |
| `requested` | CLOSING | 保持 `requested` | 与服务端关闭中一致 |
| `requested` | CONNECTED/DISCONNECTED | **保持 `requested`**，显示「服务端尚未显示关闭中」 | 200 已表示请求被处理；快照可能早于 closeLocked |
| `requested` | SESSION_NOT_FOUND | → `ended` | §4.5 证据 A |
| `unknown` | CLOSING | → `requested` | CLOSING 只可能来自服务端 `closeLocked`，证明关闭已被服务端接受 |
| `unknown` | CONNECTED/DISCONNECTED | **保持 `unknown`** | 无法证明先前的 DELETE 未被接受 |
| `unknown` | SESSION_NOT_FOUND | → `ended` | §4.5 证据 A |
| `failed` | CLOSING | → `requested` | 服务端已进入关闭，重试已无必要 |
| `failed` | CONNECTED/DISCONNECTED | **保持 `failed`** | 只有用户显式重试才重新发起 |
| `failed` | SESSION_NOT_FOUND | → `ended` | §4.5 证据 A |
| 任意非 idle | 网络错误/超时/无法解析 | 保持原状态，只记录错误消息 | 查询失败不提供任何服务端事实 |
| `ended` | 任意 | 保持 `ended`，不再查询 | tombstone |

补充规则：

1. **不得把 `requested`、`unknown`、`failed` 自动重置成 `idle`。** 如果未来要支持解除，必须先有服务端权威证据，并单独裁决；本轮不提供。
2. 详情 GET 不能证明「先前的 DELETE 没有被服务端接受」：`GET` 与 `DELETE` 由不同请求处理，GET 只反映查询瞬间的状态。
3. 「刷新服务端详情」与「重试 DELETE」是两个操作：前者只读，后者会再次提交关闭请求；UI 必须分开。
4. `unknown → requested` 的依据是服务端 `CLOSING` 状态本身；即使 CLOSING 由离线过期或服务端关闭触发，也意味着该 Session 正在关闭，关闭意图已达成，禁止接管仍然正确。
5. `requested + CONNECTED/DISCONNECTED` 不是矛盾：DELETE 200 与后续 GET 之间没有顺序保证，GET 可能读到 closeLocked 之前的快照。

### 4.5 `ended` 的权威判定

`ended` 表示「服务端确认该 Session 已不存在或已不可恢复」，会移除页面内的本地记录并阻止再次打开。只接受以下两类证据，且两者的依据强度不同。

**证据 A：详情 GET 返回 `SESSION_NOT_FOUND`（强证据）。**

- 后端 `Manager.Get` 先 `Find`：ID 不在 Map，或状态为 `CLOSED`，都返回 `SESSION_NOT_FOUND`（`session.go:186-212`）。
- `reap` 在关闭终端、等待任务、置 `CLOSED` 之后，才从 Map 删除（`session.go:386-425`）。
- Session ID 为随机 32 位十六进制且不复用（核心设计 §11.4），因此该结果表示「该 ID 当前没有可用 Session」。

**证据 B：服务端 `closed` 控制消息（充分，但依据必须限定）。**

不能仅凭「收到 closed 消息」推断，必须按以下链路证明：

1. `closed` 只在 `Connection.finish` 中构造，条件是 `reason` 以 `closed:` 开头（`connection.go:583-600`）。
2. `finish` 只有一个调用点：`writer()` 观察到 `c.reason != ""`（`connection.go:449-453`）。
3. `closed:` 原因只有一个来源：`Session.reap` 的 `old.end("closed:" + s.reason)`（`session.go:403-405`）。
4. `reap` 只由 `closeLocked` 启动（`session.go:384`）；`closeLocked` 先把状态置为 `CLOSING`（`session.go:366`）。
5. 发出 `closed` 时状态仍是 `CLOSING`，但 CLOSING 不可逆：
   - `canBind` 只接受 CONNECTED/DISCONNECTED，CLOSING/CLOSED 返回 `SESSION_NOT_READY`（`binding.go:96-116`）；
   - `Attach` 持 `s.mu` 从 `canBind` 一直到 `status = "CONNECTED"`，与 `closeLocked` 互斥（`binding.go:141-208`）；
   - `closeLocked` 对 CLOSING/CLOSED 直接返回（`session.go:361-365`）；
   - 唯一后继是 `reap` 写入 `CLOSED`（`session.go:407`）。
6. 发出 `closed` 前，SSH 终端已 `Close()`、`s.wg.Wait()` 已完成（`session.go:386-405`），不存在「Binding 结束但 Session 仍可接管」的分支。
7. 后端测试验证两类原因不会混淆：`close` 场景为 `closed` + 1000 + 状态 CLOSED；`revoke` 场景为 `error` + 1008 + 状态 DISCONNECTED（`takeover_test.go:552-566`）。

因此，`closed` 可以作为 `ended` 证据，但表述必须是「服务端已进入不可逆 CLOSING，该 Session 不可能再被接管」，而不是「Binding 结束」或「消息本身证明 CLOSED」。

**明确不能单独作为 `ended` 依据：**

- 本地 Pane `close()` / `dispose()`；
- 本地 `client.stop()` / `client.dispose()`；
- WebSocket close 事件或 close code（1000 也可能是本地正常关闭；1006/1011/1012 不是结束证明）；
- 网络断开、`offline` 事件；
- Binding 本地停止；
- `taken_over`、`AUTH_REQUIRED`、`BIND_CONFLICT` 等终止原因；
- 连接任务的普通 `stopped` 状态；
- DELETE 返回 HTTP 200；
- 列表刷新中该行消失（列表是快照，且可能被旧响应覆盖）。

**实现约束：**

- 前端必须保留「该结果来自服务端 `closed` 控制消息」的来源标记；不得用本地 `USER_STOPPED` 或 socket close code 合成。
- 若未来 S5 的映射发生变化（例如把 `closed` 与本地停止合并），该证据必须重新评估，不得默认继续有效。
- 若未来出现「Binding 结束但仍可 attach」的服务端路径，证据 B 立即失效；当前代码不存在该路径。

**与 Session ID 预占的关系：**

- 只有 `ended` 才允许 HomePage 移除记录并释放前端预占；后端租约由服务端 `reap` 释放，前端不能代替。
- 本地 Pane 关闭只移除本地记录（用户主动关标签），不发送 `closed`、不释放远端 Session。
- 清理失败（`dispose_failed`）不 emit `closed`、不释放预占（U1 保持不变）。

**列表与 tombstone：**

- `ended` 后协调器保留 tombstone 到页面 dispose；列表刷新返回旧行时，UI 按 tombstone 过滤或禁用，不能重新打开。
- tombstone 只影响当前页面生命周期；页面刷新后不存在，新页面依赖服务端详情。

### 4.6 生命周期边界（必须明确）

| 场景 | 必须行为 |
|---|---|
| 关闭 Pane 标签 | Pane `close()` 只退订协调器、注销本地 stop、释放 xterm/client；**不 abort** 协调器任务，不清除 pending/unknown；DELETE 继续由协调器等待 |
| Pane 被重新创建 | HomePage 创建 Pane 前读取 `coordinator.getCloseState(sessionId)`；非 idle 时新 Pane 不自动 `takeover.prepare()`，只展示关闭状态/重试；takeover 和手动恢复保持禁用 |
| SessionManager 卸载 | 只 dispose 列表控制器；协调器归 HomePage，不受影响；已发出的 DELETE 继续 |
| HomePage 页面卸载 | `coordinator.dispose()`：abort 所有 DELETE 与详情查询、标记 disposed、清空订阅；迟到响应被 epoch/disposed 丢弃；不声称撤销服务端关闭 |
| 页面重新进入（刷新/新标签） | 本地关闭状态不存在；按 S6 方案 B 创建候选 Pane，由 Pane 详情 GET 确认服务端状态（见 §5.5）；不得凭「本地无记录」自动接管 |
| 用户登出 | `signOut` 同步设置 `logoutPending` 后立即 `coordinator.dispose()`；协调器不再发起新的 GET/DELETE/WS；随后按 S6 §6.5 stop/logout/close |
| DELETE 返回 200 | 只标记 `requested`；不 emit `closed`，不释放 Session ID 预占，不显示「已完全关闭」 |
| DELETE 网络错误/abort | 进入 `unknown`；保留关闭意图；不自动回 idle |
| DELETE 可证明的前置拒绝 | 进入 `failed`；保留关闭意图；只允许重试/刷新 |
| 重试 DELETE | 协调器在同一 Session 上复用/替换唯一任务；同一时刻只有一个有效请求所有者 |
| 关闭状态变化 | 协调器通知当前仍订阅的 Pane 和 HomePage；已销毁 Pane 不会收到 UI 更新 |
| 状态释放 | `ended` 保留为 tombstone；页面 dispose 时统一释放；不在 Pane 销毁时释放 |

### 4.7 页面卸载与浏览器刷新：设计限制与竞态

**HomePage 卸载/浏览器刷新后，协调器和本地 unknown 状态都会消失。** 当前架构没有跨页面持久化；本轮不新增 localStorage、IndexedDB、全局 Store 或后端字段。

#### 4.7.1 基本限制

1. 页面卸载后，服务端仍可能继续执行关闭；前端无法在下一个页面证明「关闭已完成」或「关闭未发生」。
2. 新页面不能仅凭「本地没有关闭记录」就认为 Session 可以安全接管。
3. 新页面按 S6 方案 B 创建候选 Pane，由 Pane 查询服务端详情：
   - 列表只是快照，不能作为接管依据；
   - 详情返回 CLOSING → 禁止接管，提示正在关闭；
   - 详情返回 SESSION_NOT_FOUND → 按 §4.5 证据 A 进入 `ended`；
   - 详情返回 CONNECTED/DISCONNECTED → 仍需用户显式确认，提交时服务端再次校验；
   - 详情 GET 失败/超时 → 保留候选 Pane，显示查询错误，不显示接管确认，允许重新查询。
4. 若详情 GET 在 closeLocked 之前被处理，可能短暂返回 CONNECTED；用户确认 takeover 后，服务端提交会因状态变化或关闭先完成而拒绝。服务端是最终权威。

#### 4.7.2 旧 DELETE 与新 takeover 的竞态（必须显式接受）

```text
t0  旧页面：用户确认关闭，协调器 pending，DELETE 已发出
t1  DELETE 因网络延迟尚未到达服务端
t2  旧页面卸载：coordinator.dispose() 只 abort 本地等待；请求可能仍在网络中
t3  新页面加载：新协调器为空，没有任何旧 unknown
t4  新页面：用户打开 Session，HomePage 创建候选 Pane
t5  候选 Pane 详情 GET 返回 CONNECTED/DISCONNECTED
t6  用户确认 takeover；服务端复查通过并提交新 Binding
t7  旧 DELETE 到达服务端；Manager.Close 幂等执行，Session 进入 CLOSING
t8  新 Binding 被关闭（服务端以 closed:user_closed 结束），新页面收到 closed
```

必须明确：

- 详情 GET 无法证明旧 DELETE 已失效；它只反映查询瞬间的状态。
- 新页面内存中没有旧 `unknown`，不代表远端没有未完成的关闭请求。
- 提交时服务端复查可以保护「查询 → 提交」之间的普通状态变化，但**不能证明旧 DELETE 不会在提交之后到达**。
- 因此当前设计**不是**跨页面关闭操作的强一致性保证，而是当前协议能力下的最小化风险控制：关闭意图优先于同页面的接管，但无法跨页面排序。

#### 4.7.3 用户必须接受的风险（D1 接受条件）

若用户接受「不引入持久化 / 后端关闭意图记录」，必须同时接受：

1. 刷新后无法恢复旧页面的 unknown；
2. 旧 DELETE 可能在新页面 takeover 成功之后才到达服务端，并导致新连接被关闭；
3. 现有接口无法完全消除该竞态；
4. 该方案提供的是最小化风险控制，不是跨页面强一致性保证。

该风险不是普通备注，必须作为 D1 整体架构决策的接受条件之一（见 §18）。如不接受，只能另行授权持久化或后端关闭意图方案；本轮不得自行实现。

### 4.8 关闭与 takeover/recovery 的同步互斥边界

`closeState === 'idle'` 只是准入条件，必须配合同步建立顺序才能形成屏障。本节定义关闭确认后的严格顺序。

#### 4.8.1 同步建立顺序

在用户确认关闭的同一个同步执行块内，按以下顺序完成：

1. **同步建立关闭意图。** `coordinator.requestClose(id)` 在任何 await 之前把状态置为 `pending`、递增 epoch 并同步通知订阅者。此步之后，该 Session 的关闭准入立即生效。
2. **同步使当前 Pane 的 takeover/recovery 失效。** 协调器在 `pending` 发布后、发送 DELETE 之前，同步调用 Pane 注册的本地 stop；该 stop 执行 `takeover.invalidate()` 与 `recovery.stop()`（内部 `invalidate()` + `client.stop()`）。
3. **同步启动本地 stop。** `client.stop()` 的同步部分立即把连接任务标记为停止中，阻止新资源建立；其 Promise 由协调器单独等待，不阻塞 DELETE。
4. **随后发送 DELETE。** DELETE 在同步块结束后立即发出，与本地 stop 并行。
5. **之后所有 takeover/recovery 新任务都必须检查协调器准入。** 非 idle 一律拒绝；拒绝发生在各自模块的 admission 检查点，不依赖异步 UI 状态。

顺序约束：

- 第 1 步必须先于第 2 步：关闭意图先建立，避免 stop 过程中的回调重新放开接管。
- 第 2 步必须先于第 4 步：先让本地任务失效，再向服务端提交关闭。
- 第 1～3 步全部同步完成，中间没有 await；因此其他点击处理函数不可能插入。
- 本地 stop 同步抛错时，记录 `localStopFailed` 并继续发送 DELETE；不得因本地异常跳过关闭意图。
- 协调器不复制 takeover/recovery 状态机；它只调用 Pane 注册的 stop，并读取自身准入。

#### 4.8.2 调用时序

```text
用户点击「确认关闭」
  └─ coordinator.requestClose(id)            [同步]
       ├─ state = pending; epoch++; publish  [同步]
       ├─ localStop()                        [同步：takeover.invalidate + recovery.stop]
       │    └─ client.stop() 同步标记停止中
       └─ void runDelete(epoch, controller)  [异步：DELETE 在同步块结束后发出]
```

行级关闭：

```text
用户点击列表「关闭」
  └─ HomePage.handleClose(item)              [同步]
       ├─ 已有 ready Pane → coordinator.requestClose(id) 会调用该 Pane 注册的 localStop
       ├─ 已有记录但 handle 为空 → coordinator.requestClose(id)，无本地 stop
       └─ 无记录 → coordinator.requestClose(id)，无本地 stop
```

无 Pane 时不存在 takeover/recovery 互斥问题；协调器同步置 `pending` 后，`openSession` 读取 `getCloseState` 必然看到非 idle，不会创建会自动 prepare 的候选 Pane。

#### 4.8.3 逐场景

| 场景 | 同步顺序 | 结果 | 覆盖 |
|---|---|---|---|
| Pane 内确认关闭 | pending → localStop → DELETE | takeover/recovery 立即失效；DELETE 一次 | S7-C08、C16 |
| SessionManager 行级关闭（有 Pane） | pending → Pane.localStop → DELETE | 与 Pane 内确认等价，只发一次 DELETE | S7-C08 |
| SessionManager 行级关闭（无 Pane） | pending → DELETE | 新 Pane 打开时看到非 idle，不 prepare | S7-C09 |
| takeover preparing | pending → `takeover.invalidate()`（prepare 的 sequence 失效）→ DELETE | prepare 的 GET 结果被丢弃；不进入 confirming | S6-T38、S7-C16 |
| takeover confirming | pending → invalidate → DELETE | 确认框关闭；`confirm()` 因 phase 不是 confirming 直接返回；零 `connectTakeover` | S6-T24、S7-C16 |
| takeover connecting | pending → invalidate → DELETE | `connectTakeover` 已发出时无法撤回；结果到达后因 sequence 失效被丢弃；不产生第二次 takeover | S6-T38 |
| recovery retry_wait | pending → `recovery.stop()` → DELETE | 取消等待与重试；不再自动 attach | S7-C19 |
| recovery attach | pending → `recovery.stop()` → DELETE | 停止在途 attach；结果被 sequence 丢弃 | S6-T38 |
| 同 Session 同时关闭/接管/恢复 | 关闭意图先建立（同步），其他两个模块的 admission 立即为假 | 至多一个 DELETE、至多一个 takeover 尝试 | S7-C08、C09 |
| stop 同步抛错 | pending → 捕获异常并记 `localStopFailed` → DELETE | 关闭意图仍成立；提示「本地终端未确认停止」 | S7-C20 |
| HomePage 正在 signOut | `logoutPending=true` → `coordinator.dispose()` → 各 Pane stop | 协调器拒绝新请求；不再有 GET/DELETE/WS | S7-C14、C21 |

#### 4.8.4 准入判定的单一来源

- Pane 的 takeover/recovery 准入读取 `closeHandle.snapshot().state === 'idle'`（或等价的注入函数），不得复制一份本地 `closeRequested`。
- `takeover.allowed()`、`recovery.allowed()` 仍由 Pane 注入；S7 只把「关闭状态」加入该注入函数，不改变 takeover/recovery 自身的阶段判断。
- 关闭状态变化必须同步反映到准入；不得依赖 `watch`、`nextTick` 或组件重新渲染。

#### 4.8.5 本地 stop 的同步契约

`requestClose` 的同步顺序依赖 localStop 在返回 Promise 之前完成关键失效动作。契约如下：

1. **同步部分必须完成：**
   - `takeover.invalidate()`：递增 sequence、abort 在途详情 GET、清空候选、phase 回 `idle`；
   - `recovery.invalidate()` / `recovery.stop()` 的同步部分：递增 sequence、abort、清空候选；
   - `client.stop()` 的同步部分：`task.stopped = true`、abort controller、取消 delay、`finish()`、发布 `stopping`。
2. **异步部分可以延后：** 等待 `task.done`、`sendTask`、`output.settle` 的排空 Promise 可以在 localStop 返回的 Promise 中继续。
3. **DELETE 只能在 localStop 返回之后启动**；不得先发 DELETE 再停止本地连接。
4. **localStop 同步抛错：** 协调器捕获，记录 `localStopFailed=true`，继续发送 DELETE；不得恢复关闭准入，不得回退 `pending`。
5. **localStop Promise reject：** 记录失败原因并展示「本地终端未确认停止」；关闭状态仍由 DELETE 结果决定；不得把 reject 解释为远端已关闭。
6. **无论 localStop 成功或失败，都不得把它等同于远端关闭成功。**

已核实的 S6 行为（`TerminalPane.vue:196-199`、`client.ts:750-769`、`recovery.ts:120-123`、`takeover.ts:59-66`）：

- `Pane.stop()` 在返回 Promise 前依次完成 `busy=false`、`takeover.invalidate()`、`recovery.stop()` 的同步部分；
- `client.stop()` 的 `task.stopped = true`、abort、`finish()` 均在任何 await 之前；
- 因此当前 `Pane.stop()` 满足上述契约，S7 不需要修改 S5/S6 语义。

无 Pane 注册时（行级关闭、候选 Pane 尚未挂载）：协调器只建立 `pending`，没有本地 stop 可调用；这不会削弱关闭准入，因为 `pending` 已经阻止新 Pane 自动 prepare。

### 4.9 备选方案比较

| 方案 | 内容 | 优点 | 缺点 | 是否推荐 |
|---|---|---|---|---|
| A（推荐） | HomePage 持有 `CloseCoordinator`，按 Session ID 管理 | 单一所有者；Pane 销毁不丢状态；Pane/无 Pane 共用；页面卸载边界清晰 | 新增一个前端模块；HomePage 需要注入句柄 | 是 |
| B | Pane 持有任务，关闭时把任务移交给 HomePage | 不新增页面级所有者 | 交接窗口复杂；迟到回调/epoch 需要跨组件；容易出现双所有者 | 否 |
| C | 模块级单例 Map（全局关闭任务） | 跨 Pane 简单 | 等价于全局 Store；跨 HomePage/登录周期泄漏；测试隔离差 | 否 |
| D | localStorage/IndexedDB 持久化 unknown | 刷新后仍能阻止接管 | 新增持久化、清理、跨标签同步和隐私边界；当前授权不允许 | 否（备选） |
| E | 后端新增「关闭意图」记录/接口 | 跨设备/刷新权威 | 修改 API/数据库/后端；超出 S7 授权 | 否（备选） |

### 4.10 对现有模块的影响

| 模块 | 影响 |
|---|---|
| `frontend/src/session/close.ts` | 新增 `CloseCoordinator`、`CloseTask`、`CloseSnapshot`、`CloseHandle` |
| `HomePage.vue` | 创建/持有/dispose 协调器；按 Session ID 路由；打开 Session 前检查状态；订阅状态变化刷新列表 |
| `TerminalPane.vue` | 移除 Pane 内 `closeRequested` 驱动的 DELETE 所有权；改为读取 `closeHandle`；注册本地 stop；`close()` 只退订；确认/取消/重试/刷新调用句柄 |
| `SessionManager.vue` | 只增加 `close` 事件；不持有关闭状态 |
| `api/sessions.ts` | `closeSession(id, signal?)`；仍不承担 UI/所有权 |
| `terminal/takeover.ts` | 原则上不修改；`allowed` 读取 `closeHandle.state === 'idle'`；保留 `SESSION_NOT_FOUND → markEnded` |
| `client.ts`/`output.ts`/`recovery.ts` | 不修改 |
| `auth.ts`/`router` | 默认不修改 |

### 4.11 需要用户确认的具体事项

1. 是否同意由 HomePage 持有按 Session ID 管理的 `CloseCoordinator`。
2. 是否同意 Pane 只负责关闭 UI，不拥有跨 Pane 生命周期的 DELETE 任务。
3. 是否同意详情 GET 永远不把 `requested/unknown/failed` 解除为 `idle`（§4.4 表）。
4. 是否同意 `ended` 只接受详情 `SESSION_NOT_FOUND` 与服务端 `closed` 控制消息（§4.5）。
5. 是否同意页面刷新后按 S6 方案 B 创建候选 Pane，并接受「详情查询无法证明旧 DELETE 已失效」的能力边界。
6. 是否同意关闭确认的同步顺序（§4.8.1）。
7. 是否同意 DELETE 按 §8.2 的真实后端依据分类。
8. 原有裁决是否保留：本地 abort 只停止等待、不新增本地超时、`failed` 保留关闭意图、DELETE 200 不等于 CLOSED。

## 5. D2：Pane 与无 Pane 的统一关闭状态机

### 5.1 原提案为什么会出现两套实现

首轮提案把 S7-1 定义为「Pane 内关闭状态机」，把 S7-2 定义为「SessionManager 行级关闭」，并把 `session/close.ts` 写成 S7-2 的可选文件。结果是：

- S7-1 在 `TerminalPane.vue` 内实现 pending/unknown/abort/重试；
- S7-2 在 HomePage 为无 Pane 会话再实现一次 DELETE；
- 两者对 epoch、迟到响应、abort、重试和错误分类的处理可能不一致；
- 同一个 Session 在 Pane 与列表两个入口同时操作时，可能出现两个 DELETE 所有者。

第二轮把关闭任务上移为页面级唯一所有者；本轮进一步把「详情查询」纳入同一协调器，并定义查询与重试的并发隔离。

### 5.2 推荐架构（待用户裁决）

```text
HomePage
  │
  ├── CloseCoordinator（每个 HomePage 一个）
  │     ├── 按 sessionId 管理 CloseTask
  │     ├── 持有 DELETE controller / query controller / epoch / queryEpoch
  │     └── 唯一执行 DELETE / 重试 / 详情查询 / 取消等待
  │
  ├── TerminalPane（每个 Session 一个）
  │     ├── 通过 CloseHandle 读取状态、订阅变化
  │     ├── 注册本地 stop（takeover.invalidate + recovery.stop）
  │     ├── 确认关闭 → closeHandle.requestClose()
  │     ├── 取消等待 → closeHandle.cancelWaiting()
  │     ├── 重试关闭 → closeHandle.retryClose()
  │     ├── 刷新状态 → closeHandle.refreshClose()
  │     └── 展示关闭状态/错误/服务端状态
  │
  └── SessionManager
        └── emit('close', item)
              │
              └── HomePage.closeSession(item.id) → coordinator.requestClose(id)
```

职责关系：

| 角色 | 负责 | 不负责 |
|---|---|---|
| `CloseCoordinator` | 状态、DELETE/查询 controller、epoch/queryEpoch、重试、去重、订阅、dispose | 不渲染 UI；不管理 takeover/recovery；不持有 xterm/client |
| `TerminalPane` | 关闭确认框、取消等待/重试/刷新按钮、状态文案、错误展示、本地 stop 注册 | 不直接 `closeSession`；不持有 DELETE Promise/controller；不跨 Pane 保留状态 |
| `HomePage` | 创建/dispose 协调器；按 Session ID 路由；打开 Session 前检查状态；订阅状态变化刷新列表 | 不在已有 Pane 时另行 DELETE；不复制状态机 |
| `SessionManager` | 展示列表；emit `open`/`close` 意图 | 不直接 DELETE；不持有关闭状态；不管理任务生命周期 |
| `api/sessions.ts` | `closeSession(id, signal?)` HTTP 包装 | 不做 UI/所有权/状态机 |

### 5.3 接口草案（不写代码，只定契约）

```ts
export type CloseState =
  | 'idle'
  | 'pending'
  | 'requested'
  | 'unknown'
  | 'failed'
  | 'ended'

export type CloseServerStatus =
  | 'CONNECTING'
  | 'CONNECTED'
  | 'DISCONNECTED'
  | 'CLOSING'
  | 'NOT_FOUND'
  | null

export interface CloseSnapshot {
  sessionId: string
  state: CloseState
  message: string
  serverStatus: CloseServerStatus
  localStopFailed: boolean
  // 协调器是否已 dispose；为 true 时快照只用于展示，不得作为新操作准入。
  disposed: boolean
  updatedAt: number
}

export interface CloseCoordinator {
  // 查询当前快照。见下方 getCloseState 四态说明；disposed 后返回 disposed=true 的最后快照。
  getCloseState(sessionId: string): CloseSnapshot

  // 用户确认关闭：idle -> pending；非 idle 时返回当前快照，不重复发 DELETE。
  // 同步建立 pending、同步调用已注册的本地 stop，然后才发送 DELETE。
  // 同步阶段不等待 localStop 的 Promise。
  requestClose(sessionId: string): Promise<CloseSnapshot>

  // 在 requested/unknown/failed 下幂等重试；pending 返回同一 Promise；
  // idle/ended 返回当前快照，不发请求。
  retryClose(sessionId: string): Promise<CloseSnapshot>

  // 仅 pending 可用：abort DELETE 本地等待，状态 -> unknown；不发送新请求。
  cancelWaiting(sessionId: string): void

  // 查询服务端详情；按 §4.4 表更新状态。pending 下不发送 GET。
  // 返回值中的 serverStatus 只反映本次查询；查询失败时保持原状态。
  refreshClose(sessionId: string): Promise<CloseSnapshot>

  // 订阅状态变化；立即回调一次当前快照；返回退订函数。
  // 退订后不再收到任何通知；dispose 后不再发布。
  subscribeClose(
    sessionId: string,
    listener: (snapshot: CloseSnapshot) => void,
  ): () => void

  // 注册当前 Pane 的本地 stop；返回带身份校验的注销函数。
  // 同一 Session 只保留最后一次有效注册；旧注销函数不会移除新注册。
  // disposed 后注册返回空操作注销函数，且不保存回调。
  registerLocalStop(
    sessionId: string,
    stop: () => Promise<void>,
  ): () => void

  // 页面卸载/登出：abort 全部 DELETE 与详情查询、标记 disposed、清空订阅与注册；
  // 不发送任何新请求，不声称撤销服务端关闭。幂等。
  dispose(): void
}
```

Pane 侧的窄接口（由 HomePage 注入，绑定到具体 Session）：

```ts
export interface CloseHandle {
  readonly sessionId: string
  snapshot(): CloseSnapshot
  requestClose(): Promise<CloseSnapshot>
  retryClose(): Promise<CloseSnapshot>
  cancelWaiting(): void
  refreshClose(): Promise<CloseSnapshot>
  subscribe(listener: (snapshot: CloseSnapshot) => void): () => void
  registerLocalStop(stop: () => Promise<void>): () => void
}
```

#### 5.3.1 `getCloseState` 的四种情况

| 情况 | 返回 | 说明 |
|---|---|---|
| 从未创建关闭任务 | `state='idle'`、`disposed=false` | 正常 S6 流程；不保留任务对象 |
| 已创建但当前 `idle` | `state='idle'`、`disposed=false` | 当前实现中 `requestClose` 直接从 idle 进入 pending，不存在「创建后回到 idle」；保留该语义以备将来 |
| 已 `ended`、tombstone 存在 | `state='ended'` | 不得按「未知任务」释放；tombstone 保留到 dispose |
| 协调器已 dispose | 最后一次快照、`disposed=true` | 只用于展示；不得据此准入新操作 |

`getCloseState` 不能把 ended tombstone 当作普通未知 Session 返回 idle；否则会重新允许打开已结束的 Session。

#### 5.3.2 `registerLocalStop` 的身份与生命周期

- **身份：** 每次注册生成一个内部 token；注销函数只在 token 仍匹配当前注册时生效。
- **唯一性：** S6 的 HomePage 每个 Session 至多一个 Pane（`findRecord` 保证），因此同一 Session 只保留最后一次有效注册；新注册显式替换旧注册，旧注销函数变成空操作。
- **重复注册：** 新 token 覆盖旧 token；不产生第二个有效回调，也不重复触发状态变化。
- **重复注销：** 幂等；token 不匹配时直接返回，不删除当前注册。
- **旧注销迟到：** Pane A 注销时 Pane B 已注册 → token 不匹配 → 不移除 B 的注册。
- **注册与 requestClose 并发：** 二者都在同一个同步执行块中完成；先注册则本次 requestClose 会调用该 stop，先 requestClose 则本次没有 stop 可调用，但 `pending` 已建立，新 Pane 挂载时读取非 idle，不会 prepare。
- **dispose 后注册：** 返回空操作注销函数，不保存回调，不发布状态。
- **dispose 后注销：** 空操作，不发布状态。
- **不引入全局注册中心：** 注册表就是 `CloseTask` 内的一个字段。

#### 5.3.3 状态发布规则

- 已注销的 listener 不再收到通知。
- 旧 Pane 的订阅 token 失效后，不收到新 Pane 的关闭状态回调。
- `dispose()` 后不发布任何状态，也不因迟到响应触发通知。
- 重复注销、重复退订不产生额外的状态变化通知。
- 一次状态变化只通知一次；同一操作链只发布最终状态。

#### 5.3.4 接口所有权说明

1. **谁调用**：Pane 通过 `CloseHandle` 调用；SessionManager 只 emit，HomePage 调用 `coordinator.requestClose(item.id)`；HomePage 自己订阅状态变化刷新列表。
2. **谁拥有状态**：`CloseCoordinator` 的 `Map<sessionId, CloseTask>`。
3. **谁拥有 AbortController**：`CloseCoordinator` 的 DELETE controller 与 query controller；Pane 和 SessionManager 都不持有。
4. **谁负责 DELETE**：`CloseCoordinator` 的 `requestClose/retryClose`；其他模块不得调用 `closeSession`。
5. **谁负责通知 UI**：协调器通过 `subscribeClose` 通知；Pane 只更新仍然有效的订阅；HomePage 用同一通知做列表刷新合并。
6. **Pane 销毁后任务如何继续**：Pane 退订并注销本地 stop；协调器的 Promise/controller/state 不变；任务继续到 200/失败/unknown。
7. **HomePage 销毁后如何隔离**：`dispose()` abort DELETE 与查询、标记 disposed、清空订阅与注册；迟到响应检查 disposed/epoch 后丢弃；不发起新请求。
8. **同一 Session 重复请求如何去重**：`requestClose` 在 pending 时返回同一个 `promise`；在 requested/unknown/failed 时返回当前快照，只有 `retryClose` 才发新 DELETE；同一时刻至多一个 DELETE controller。
9. **迟到响应如何隔离**：DELETE 用 `epoch`，详情 GET 用 `queryEpoch`；响应返回后检查对应 epoch、`disposed`、`sessionId` 和当前任务身份；不匹配则丢弃。
10. **关闭状态何时释放**：`ended` 作为 tombstone 保留到 `dispose()`；`idle` 不保留任务对象；页面卸载统一释放。Pane 销毁不释放。

#### 5.3.5 术语约定

| 术语 | 含义 | 不等于 |
|---|---|---|
| 本地 stop | 停止当前 Pane 的本地连接与任务（takeover/recovery 失效、client 停止） | 关闭远端 Session |
| DELETE | 请求服务端关闭 Session 的 HTTP 操作 | 关闭本地终端 |
| `requested` | 服务端已接受关闭请求（HTTP 200） | `CLOSED`、Session 已回收 |
| `unknown` | 无法确认 DELETE 是否被服务端接受 | 关闭已撤销、可以重新接管 |
| `ended` | 存在权威证据证明远端 Session 已不存在或不可恢复 | 本地标签已关闭、WebSocket 已断开 |
| 服务端 `CLOSED` | `Session.status` 的终态，出现在 `reap` 末尾 | HTTP 200 |
| `SESSION_NOT_FOUND` | 详情接口表示该 ID 当前没有可用 Session | 权限错误、网络错误 |

### 5.4 `refreshClose` 与 `retryClose` 的并发规则

同一个 Session 的刷新与重试可能并发，必须用请求身份而不是时间戳判断。

#### 5.4.1 身份与去重

- 每个 `CloseTask` 维护两个单调计数器：`epoch`（DELETE/重试）与 `queryEpoch`（详情查询）。
- 每次发起 DELETE 或重试：`epoch++`。
- 每次发起详情查询：`queryEpoch++`。
- **任何会改变权威结论的操作都必须使另一类在途请求失效**：重试会 `queryEpoch++` 并 abort 在途 GET；`requestClose` 同样使在途 GET 失效。
- 不使用 `updatedAt` 做「谁更新」的判断；`updatedAt` 只用于展示。
- 状态发布前必须同时满足：`!disposed`、任务对象仍是 Map 中该 Session 的当前任务、`epoch`/`queryEpoch` 与发起时一致。

#### 5.4.2 逐场景

| 场景 | 处理 | 结果 |
|---|---|---|
| `refreshClose` 在 `pending` | 直接返回当前快照，不发送 GET | DELETE 在途时详情查询没有权威意义 |
| `refreshClose` 在 `idle` | 返回 `idle` 快照，不发送 GET | 没有关闭意图可刷新；S6 的详情查询由 Pane 自己负责 |
| `refreshClose` 在 `ended` | 返回 `ended` 快照，不发送 GET | tombstone 是终态 |
| `refreshClose` 在 `requested/unknown/failed` | `queryEpoch++`，abort 上一个 query controller，发送 GET | 按 §4.4 表更新 |
| 多个 `refreshClose` 同时触发 | 后发者 `queryEpoch++` 并 abort 前一个 | 只有最后一次可以发布；更早的响应即使到达也被丢弃 |
| GET 在途时触发 `retryClose` | `queryEpoch++` 并 abort GET；`epoch++`；状态 → `pending`；发送 DELETE | GET 结果被丢弃；DELETE 是新的权威 |
| GET 在途时触发 `requestClose` | 只可能发生在 `idle`；`queryEpoch++` 使在途 GET 失效 | 不出现「GET 结果覆盖 pending」 |
| DELETE 在途（`pending`）时触发 `refreshClose` | 不发送 GET，返回当前快照 | 避免 GET/DELETE 交错 |
| DELETE 在途（`pending`）时触发 `retryClose` | 返回同一个 `promise`，不发送第二个 DELETE | 单 Session 单 DELETE |
| 重试刚完成、`refreshClose` 立即触发 | 重试已 `epoch++`；刷新读取新状态并 `queryEpoch++` | 按新状态应用 §4.4 |
| `cancelWaiting` 与 `refreshClose` 同时 | `cancelWaiting` 先 abort DELETE → `unknown`；刷新随后按 unknown 查询 | 状态以 abort 后为准 |
| `dispose` 与任一操作同时 | `disposed=true`；全部 abort；后续调用直接返回当前快照 | 不再发布、不再发请求 |

#### 5.4.3 发布规则

- 状态变化与 `subscribeClose` 通知在同一个同步块内完成，一次变化只通知一次。
- 同一操作链内（例如「GET 返回 CLOSING → requested」）只发布最终状态，不发布中间态。
- 迟到的成功/失败响应必须静默丢弃，不写 `message`、不改 `updatedAt`、不通知订阅者。
- 重试失败进入 `unknown` 或 `failed` 时，必须保留原 `serverStatus` 直到下一次查询更新。

### 5.5 HomePage 的打开与路由规则

`openSession(item)` 在创建/定位 Pane 前检查协调器：

| 协调器状态 | 行为 |
|---|---|
| `idle` | 正常候选 Pane 流程（S6 方案 B） |
| `pending` | 已有 Pane → 定位；无 Pane → 创建 Pane 但**不自动 prepare**，只展示「正在请求关闭」；takeover/手动恢复禁用 |
| `requested` | 同上；展示「已请求关闭，正在回收」；允许刷新/重试 |
| `unknown` | 同上；展示「关闭结果未知」；允许刷新/重试；禁止 takeover/手动恢复 |
| `failed` | 同上；展示「关闭请求失败」；允许重试；禁止 takeover/手动恢复 |
| `ended` | 不创建 Pane；提示「Session 已结束」；列表中旧行不会再次打开 |

列表状态与协调器状态的关系：

- 列表 `CONNECTING`：保持现有「正在建立，暂不能打开」提示；不创建 Pane。
- 列表 `CLOSING`：保持现有「正在关闭，不能打开」提示；不创建 Pane，不允许 takeover。
- 列表 `CONNECTED/DISCONNECTED` 且协调器 `idle`：按 S6 创建候选 Pane。
- 列表快照永远不能覆盖协调器的非 idle 状态。

说明：

- 「创建 Pane 但不自动 prepare」不是进入候选 takeover 流程；Pane 只是关闭状态的展示/操作载体，不申请凭证、不创建 WebSocket、不显示接管确认框。
- 如果不希望为关闭状态创建 Pane，备选是 HomePage 直接提示并提供页面级「重试关闭/刷新状态」；这会少一个统一的关闭 UI，推荐仍创建只读 Pane。
- 已有 Pane 时只定位，不创建第二个 Pane；重复点击不会产生第二个 DELETE。

无 Pane 的行级关闭：

- SessionManager 只 emit `close`；
- HomePage 调用 `coordinator.requestClose(item.id)`；
- 二次确认由 HomePage 的页面级 Modal 负责（或复用 Pane 的确认文案）；
- 由于没有 Pane，不存在 takeover/recovery 互斥问题，但仍必须走同一协调器。

有 Pane 的行级关闭：

- HomePage 定位 Pane，并调用 `coordinator.requestClose(item.id)`；
- 协调器同步调用该 Pane 注册的本地 stop；
- 如果 Pane 存在但 handle 尚未注册（初始化中），协调器仍建立 `pending`，新 Pane 挂载时读取非 idle，不会 prepare；
- HomePage 绝不在有 Pane 时自行 `closeSession`。

### 5.6 TerminalPane 的关闭 UI 规则

- 工具栏「关闭 SSH 会话」按钮：`life === 'ready' && closeState === 'idle' && !ended && !logoutPending` 时可打开确认框；`pending/requested/unknown/failed` 时按钮仍可见但改为「查看关闭状态/重试」或禁用，具体文案由 UI 设计确定。
- 确认框：确认后调用 `closeHandle.requestClose()`；错误显示在确认框内。
- pending 时显示「取消等待」（调用 `cancelWaiting()`）；requested/unknown/failed 时显示「重试关闭」（`retryClose()`）和「刷新状态」（`refreshClose()`）。
- 状态文案：`pending`「正在请求关闭」、`requested`「已请求关闭，正在回收」、`unknown`「关闭结果未知」、`failed`「关闭请求失败」、`ended`「会话已结束」。
- 服务端状态与本地状态分开展示：例如「关闭结果未知 · 服务端显示已连接」；不得用服务端 CONNECTED 覆盖本地 unknown。
- `close()`（关闭标签）只退订、注销本地 stop 并释放 xterm/client；不调用 `cancelWaiting`，不 abort 协调器任务。
- Pane 的 takeover/recovery 准入读取 `closeHandle.snapshot().state === 'idle'`；不得复制一份本地 `closeRequested`。
- 详情查询失败时，候选 Pane 保留并显示错误；不得因为查询失败而销毁 Pane 或自动进入其他流程。
- 已销毁 Pane 的迟到 `subscribe` 回调不更新 UI；订阅在 `onBeforeUnmount` 中解除。
- **localStop 失败必须独立提示：** 同步 throw 或 Promise reject 时显示「已请求关闭，但本地终端未确认停止」；不得显示「已完全关闭」，也不得因此恢复接管准入。
- **服务端 `closed` 消息只影响关闭状态与 ended 判定，不直接关闭本地终端：** 本地终端仍由 Pane 自己的生命周期处理。

### 5.7 备选方案

| 方案 | 内容 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| A（推荐） | HomePage 协调器 + Pane 只读句柄 | 单一所有权；Pane 销毁不丢状态；两路径共用 | 新增 `session/close.ts`；HomePage 注入句柄 | 推荐 |
| B | Pane 持有 + 关闭时移交 HomePage | 不改 HomePage 所有权 | 交接窗口复杂；双所有者风险 | 不推荐 |
| C | SessionManager 持有无 Pane 关闭任务 | 列表入口直接 | 与「SessionManager 只 emit」冲突；Pane 与列表两套 | 不推荐 |
| D | 模块级单例 | 跨 Pane 简单 | 全局状态、跨页面/登录泄漏、测试隔离差 | 不推荐 |
| E | localStorage/后端持久化 unknown | 刷新后仍阻止接管 | 新增存储/API/数据库；超出授权 | 备选，需单独授权 |

### 5.8 需要用户确认的具体事项

1. 是否采用方案 A（HomePage 协调器 + Pane 只读句柄 + 本地 stop 注册）。
2. 是否授权 `frontend/src/session/close.ts` 作为 S7-1 正式文件。
3. 是否确认 Pane 与无 Pane 共用同一状态机。
4. 是否确认 Pane 销毁只退订、不 abort、不清状态。
5. 是否确认「非 idle 时创建 Pane 但不自动 prepare」的交互；或选择「不创建 Pane，HomePage 直接提示」。
6. 是否确认 unknown 期间只允许查询、幂等重试和关闭本地标签。
7. 是否确认 `ended` tombstone 保留到页面 dispose。
8. 是否接受页面刷新后不保留本地 unknown、依赖服务端详情确认。
9. 是否确认 §5.4 的查询/重试并发规则与 `queryEpoch` 方案。

## 6. D3：logoutPending 与关闭协调器的生命周期

### 6.1 冻结契约

S6 §6.5 冻结顺序：

1. 同步设置 `logoutPending=true`。
2. 同步让所有 Pane 执行 `stop()`，使新任务准入关闭。
3. 立即调用 `logout()`，与本地 stop drain 并行。
4. 等待 stop 屏障。
5. logout 成功后调用 `close()`，收集结果；close 失败也必须跳转登录页。
6. logout 失败时不 close、不跳转，停留本页，保留重试能力。

第四阶段 §13.2：

- logout 200：按既有契约清 Cookie/身份，确认退出完成。
- `STORAGE_ERROR` 确定回滚：保留 Cookie/身份和退出意图，允许重试。
- `SERVER_UNAVAILABLE` / 网络/响应丢失：结果未知，保持退出意图，允许幂等重试。

### 6.2 当前实现

`auth.ts:39-51`：HTTP 200 时立即 `clearIdentity()` 并 `logoutPending=false`；HomePage 随后才 close Pane 并跳转。

### 6.3 推荐方案 A（待用户裁决）

保持 `auth.ts` 不变，明确三个闸门：

- `logoutPending`：退出意图/HTTP 登出闸门；从点击到 logout 成功或失败期间为真。
- `signingOut`：HomePage 本地清理闸门；从点击到 close/跳转完成期间为真。
- `identity === null`：logout 成功后的硬闸门；`Pane.live()` 和列表 `canRefresh` 都依赖它。

协调器的登出规则：

1. `signOut()` 在同步设置 `logoutPending=true` 后，立即调用 `coordinator.dispose()`；协调器标记 disposed、abort 所有 DELETE 与详情查询、清空订阅，之后拒绝任何新的 `requestClose/retryClose/refreshClose`。
2. `dispose()` 只停止本地等待，不发送新请求，不声称服务端撤销；已在服务端的 DELETE 仍可能继续执行。
3. 协调器 dispose 后，Pane 的关闭 UI 不再更新；Pane 随后由 `stop()`/`close()` 处理，属于登出流程，不是关闭任务继续。
4. 登出期间不得产生新的列表 GET、详情 GET、DELETE、WebSocket 或 takeover：`logoutPending` + `identity=null` + `manager.invalidate()` + `coordinator.dispose()` + Pane `live()` 共同保证。
5. logout 失败时页面停留，协调器已 dispose，关闭任务不能继续；这是「退出未完成」的明确代价。Pane 已 stop，用户可重试 logout；关闭任务不再自动恢复。
6. logout 成功时按 S6 §6.5 close Pane 并跳转；协调器随页面卸载释放。
7. `ended` tombstone 在 dispose 时一并释放；登出后不再需要阻止旧行打开。

### 6.4 备选方案 B：`logoutPending` 保持到 close 完成

- 需要修改 `auth.ts`，把「HTTP 登出完成」和「本地清理完成」分开。
- 风险：`acceptIdentity()` 在 `logoutPending` 为真时直接返回，登录页可能无法登录；路由守卫会持续把 `/login` 重定向；需要同时修改 auth/router。
- 结论：不推荐；如用户坚持，需要单独授权修改 `auth.ts`、`router/index.ts` 和 `auth.test.mjs`。

### 6.5 需要用户确认的具体事项

1. 是否确认推荐方案 A（`auth.ts` 不改，`logoutPending` 不覆盖本地 close）。
2. 是否确认 `signOut` 同步 `coordinator.dispose()`，登出期间不再有新的关闭/查询请求。
3. logout 成功后 close 失败是否仍然跳转（推荐：是，U9 已裁决）。
4. logout 失败时是否保持「页面停留、Pane 已停止、可重试」，并接受协调器已 dispose、关闭任务不继续。

## 7. D4：列表同步失效、打开前检查与 changed 刷新合并

### 7.1 冻结要求

- 实施计划 §6.1：离开列表视图/卸载/登出/认证失效时，**同步撤销列表发布身份并 Abort**；不把取消当网络错误。
- 实施计划 §6.1 D1=A：同一同步事件批次可用一个 microtask 合并；不追加请求队列/周期轮询。
- 实施计划 §6.2：每次刷新先 ++sequence、abort 旧请求；fetch/JSON 完成后检查 sequence、signal、页面身份、当前登录及 `logoutPending`；旧 finally 不清新 loading。
- `SessionListController` 已实现 sequence、Abort、disposed、identity 检查。

### 7.2 当前缺口

1. `signOut()` 没有调用 `manager.invalidate()`；只依赖 `canRefresh` prop 变化触发的 watcher（默认 `flush: 'pre'`，不是同步）。
2. `SessionManager` 的 `isAllowed: () => props.canRefresh` 读取父组件传入的 prop；`logoutPending` 改变后，props 要等父组件重新渲染才更新。同步边界内 `controller.refresh()` 仍可能通过准入并发起 GET。
3. `paneChanged()` 没有 `logoutPending`/`identity`/`pageDisposed` 守卫；Pane 在 stop 过程中 emit `changed` 会触发列表刷新。
4. `paneChanged()` 每次直接 `manager.refresh()`，同一同步批次多个 changed 会产生多次 GET。
5. 打开 Session 时没有检查协调器的非 idle 关闭状态；仅依赖列表快照可能让新 Pane 进入 takeover。
6. `ended` 后列表可能仍返回旧行；没有 tombstone 过滤规则。

### 7.3 推荐方案 A（待用户裁决）

1. `signOut()` 在设置 `logoutPending=true` 后、第一个 await 前，同步调用 `manager.value?.invalidate()` 和 `coordinator.dispose()`；`manager` 的类型扩展为 `{ refresh(): void; invalidate(): void }`。
2. `paneChanged()` 增加守卫：

   ```ts
   if (pageDisposed || logoutPending.value || !identity.value) return
   if (view.value !== 'sessions') return
   scheduleRefresh()
   ```

3. 同批合并：用 `let changedQueued = false` + `queueMicrotask()`；微任务执行时再次检查守卫，只调用一次 `manager.refresh()`。不引入定时器、轮询或事件总线。
4. `SessionManager` 的 `isAllowed` 增加同步检查 `!logoutPending.value`（从 auth store 导入 ref）；`currentIdentity` 仍使用 `props.authToken` 以保持测试可注入。
5. `openSession(item)` 先检查 `coordinator.getCloseState(item.id)`：非 idle 时按 §5.5 处理，不自动 prepare；列表快照不能覆盖协调器状态。
6. HomePage 订阅协调器的状态变化；当状态变为 requested/unknown/failed/ended 时，按同一 microtask 合并刷新列表；不在 Pane 内直接刷新列表。
7. `ended` tombstone 参与列表渲染：同一 Session ID 的行禁用「打开」并提示已结束；列表刷新返回旧行不能覆盖 tombstone。
8. `controller.invalidate()` 与 `dispose()` 的现有实现保留；旧请求的 `finally` 继续由 sequence 保护。

### 7.4 备选方案 B：只依赖 `manager.invalidate()`，不改 `isAllowed`

- 优点：改动更小。
- 缺点：`invalidate()` 只取消当前请求并递增 sequence；如果 `paneChanged` 在 `invalidate()` 之后、props 更新之前调用 `refresh()`，`isAllowed()` 仍可能返回旧的真值并发起新 GET。因此不推荐单独使用。

### 7.5 需要用户确认的具体事项

1. 是否接受在 `SessionManager` 内直接读取 `logoutPending`（而不是新增 prop 或事件）。
2. 是否接受 `queueMicrotask` 合并（不新增定时器）。
3. 是否确认列表只作为快照，打开前必须检查协调器状态，接管前必须详情 GET。
4. 隐藏列表时是否只标记待刷新、下次进入再 GET（现有行为，推荐保持）。
5. 是否确认 `ended` tombstone 在列表中的过滤/禁用规则。

## 8. D5：DELETE 错误分类、ended 判定与反馈

### 8.1 真实后端执行顺序（分类依据）

DELETE `/api/sessions/{id}` 的处理链：

```text
1. Origin 检查（非 GET）              → PERMISSION_DENIED
2. readiness 检查（非 GET）           → SERVER_SHUTTING_DOWN
3. 路由到 protected
4. 认证 Lookup                        → AUTH_REQUIRED
5. id 格式检查                        → INVALID_REQUEST
6. handler: decode 空 body            → INVALID_REQUEST / MESSAGE_TOO_LARGE
7. m.Sessions.Close(id, "user_closed")  ← 从这里开始服务端可能已接受关闭
8. m.success(200)
```

关键事实：

- `Manager.Close` 不返回错误；ID 不存在或已 CLOSED 时静默返回（`session.go:251-256`）。
- 因此 **HTTP 200 只表示「请求已到达并处理」**，不表示 Session 曾存在，更不表示回收完成。
- 第 1～6 步的错误发生在 `Sessions.Close` 之前，可以证明「本次请求未被接受」。
- 第 7 步之后没有业务错误分支；任何 5xx、网关错误、JSON 解析失败都不能证明服务端未执行。
- `request()` 对非 JSON 响应直接抛错（`management.ts:60-88`），无法区分代理 502 与后端错误。

### 8.2 DELETE 结果分类表

| DELETE 结果 | 目标状态 | 判定依据 | 说明 |
|---|---|---|---|
| HTTP 200，响应 `{code:0}` 且结构合法 | `requested` | `sessions.go:64-71` | 服务端接受请求；不代表已关闭 |
| 本地 `AbortController.abort()` | `unknown` | 客户端主动停止等待 | 服务端可能仍在处理 |
| 网络错误/连接重置/响应丢失 | `unknown` | fetch 抛错，未收到响应 | 无法证明服务端未接受 |
| 网关超时/代理错误/非 JSON 响应 | `unknown` | `management.ts:60-88` 解析失败 | 不能仅凭 HTTP 状态判断 |
| `AUTH_REQUIRED` | `failed` | `protected` 在 handler 之前拒绝（`management.go:267-282`） | 请求未进入 `Sessions.Close` |
| `INVALID_REQUEST` | `failed` | id 格式或 decode 在 handler 之前/之内、`Close` 之前拒绝 | 请求未进入 `Sessions.Close` |
| `MESSAGE_TOO_LARGE` | `failed` | `DecodeJSON` 在 `Close` 之前拒绝（`request.go:17-26`） | 请求未进入 `Sessions.Close` |
| `PERMISSION_DENIED` | `failed` | Origin 检查在路由之前（`management.go:250-256`） | 请求未进入 `Sessions.Close` |
| `SERVER_SHUTTING_DOWN` | `failed` | readiness 检查在路由之前（`management.go:256-260`） | 请求未进入 `Sessions.Close` |
| `INTERNAL_ERROR` / 其他 5xx / 未知业务码 | `unknown` | 无法证明执行位置 | 默认不做乐观推断 |
| `AUTH_FAILED` 等其他认证错误 | `unknown` | 不由 DELETE 路径产生 | 视为未知 |

补充说明：

1. **`failed` 的语义严格限定为「明确证明本次关闭请求未被服务端接受」**，不是「客户端收到错误」。
2. 反向代理可能伪造 4xx/5xx，但无法伪造后端 JSON 错误信封中的业务码；若响应结构不合法，一律 `unknown`。
3. 即使收到 `failed`，关闭意图仍保留：用户可显式重试或刷新；不得回到 `idle`。
4. `retryClose()` 的幂等性由 `Manager.Close`/`closeLocked` 保证（CLOSING/CLOSED 直接返回），重试不会产生第二次关闭。
5. 不新增后端错误码来「方便分类」；如未来需要更精确的分类，必须单独授权修改后端。

### 8.3 `failed` 的展示与重试

- 展示：「关闭请求未被服务端接受（原因）；可重试或刷新状态」。
- 允许：`retryClose`、`refreshClose`、关闭本地标签。
- 禁止：takeover、手动恢复、自动回到 idle。
- 重试后进入 `pending`；若详情返回 CLOSING，则升格为 `requested`。

### 8.4 stop 失败与 DELETE 失败分别反馈

- DELETE 结果由协调器状态/消息展示在关闭确认框或关闭状态区；stop 失败使用独立的 `closeNotice`/`connectionNotice` 文案（例如「已请求关闭，但本地终端未确认停止」）；不要用一条消息覆盖另一条。
- DELETE 成功但本地 stop 失败：协调器保持 `requested`，Pane 显示「已请求关闭 SSH 会话；本地终端未确认停止（原因）」；允许刷新状态/关闭标签；不得显示「已完全关闭」。
- stop 同步抛错：`requestClose` 捕获并记 `localStopFailed`，仍发送 DELETE；不得因本地异常跳过关闭意图。

### 8.5 router guard 与主动 signOut 同时发生

- 现状：`expireIdentity()` 在 `logoutPending` 为真时返回 false；guard 在 `logoutPending` 为真时重定向 `/login`；`signOut` 的 `router.replace('/login')` 与 guard 一致。
- 推荐：保持现状；S7 只补 `router.replace` 的 rejection 处理和跳转后的反馈写入。

### 8.6 `router.replace()` reject

- 现状：`await router.replace('/login')` 若 reject，`finally` 会复位 `signingOut`，但清理反馈不会写入，且 rejection 可能成为未处理异常。
- 推荐：`try { await router.replace('/login') } catch (error) { console.error(...) }`，无论跳转是否成功都写入清理反馈；不改变登出成功语义。

### 8.7 stop/close 同步抛错

- 现状：`entries.map((entry) => entry.handle.stop())` 在 `try` 之外求值；同步抛错会跳过 `logout()`，`signingOut` 永久为真。
- 推荐：把每个调用包装为 `Promise.resolve().then(() => entry.handle.stop())`，或用 `try/catch` 收集同步错误；`close()` 同理。保证 `allSettled` 始终拿到 Promise。

### 8.8 浏览器关闭

- 冻结：不依赖 `beforeunload` 发送 DELETE；服务端按断线/心跳/Supervisor 回收 Binding，SSH 离线保留。
- 推荐：保持；S7 不新增 `beforeunload`/`sendBeacon`。

### 8.9 页面卸载后的迟到回调与重复 abort

- 列表：已有 sequence/dispose/Abort；保持。
- 协调器：`dispose()` 是唯一 abort 所有任务的入口；Pane `close()` 只退订，不 abort。这样不会出现 Pane 与 HomePage 同时 abort 同一 controller。
- Pane：`life`、`taskId`、订阅 token 和 `live()` 隔离迟到回调；已销毁 Pane 不更新 UI。
- `dispose()` 幂等；重复调用不重复 abort/释放。

### 8.10 U6 认证网络错误

- 现状：`router/index.ts:29-38` 在 `/me` 网络错误时也 `clearIdentity()`，并显示「无法验证登录状态」。
- 结论：S6 已裁决不修改；S7 不扩大范围。作为独立风险保留。

## 9. 14 项竞争场景逐项分析（第三轮修订）

以下每项均按「冻结预期 / 当前实现 / 修订后目标行为 / 测试 / 结论」核对。结论中的「通过」仅指当前代码与冻结设计一致；「需裁决」表示设计或所有权未明确；「阻塞」表示不解决不能安全开始 S7。

### 场景 1：点击关闭 SSH 后立即再次点击接管

- 冻结预期：实施计划 §11.3（S6 修订）规定关闭意图优先；关闭按钮不因 takeover 进行中而禁用；确认关闭后 `stop()` 使 takeover 失效。S6 §10.1 规定关闭意图行拒绝 takeover。
- 当前实现：`closeRemote()` 在任何 await 之前设置 Pane 内 `closeRequested=true` 并调用 `stop()`；`takeover.invalidate()` 递增 sequence，`allowed()` 立即为假。
- 修订后目标：`requestClose()` 同步把协调器状态置为 `pending`；随后同步调用本地 stop；Pane 的 takeover/recovery 准入读取 `closeState !== 'idle'`，立即为假；DELETE 在同步块之后发出。
- 测试：S6-T24；新增「协调器 pending 时 takeover 拒绝」用例（S7-C16）。
- 结论：正常路径通过；实现时需把 `closeRequested` 迁入协调器，并保证准入读取同步状态。

### 场景 2：用户正在接管时发起关闭 SSH Session

- 冻结预期：S6 §10.1、R14：关闭意图优先；关闭确认框打开期间 `canBegin()` 为假，prepare 拒绝；确认关闭后 takeover 失效。
- 当前实现：`closing` 是独立状态；`canBegin()` 读取 `!closing`；确认关闭调用 `closeRemote()` → `stop()`。
- 修订后目标：确认关闭先同步置 `pending`，再调用 `stop()`；`closing` 仍只控制确认框；takeover 的三个检查点都读取协调器准入。
- 测试：S6-T24、S6-T38；新增 S7-C16。
- 结论：通过。

### 场景 3：DELETE 请求已发出，用户取消等待

- 冻结预期：实施计划 §15 S7 行要求「DELETE 可取消等待」，但没有定义取消的含义、状态和提示。
- 当前实现：没有取消按钮；`closeSession` 不接受 `AbortSignal`；请求只能等待网络结束。
- 修订后目标：`cancelWaiting(sessionId)` 由协调器 abort DELETE 本地等待，状态 `pending → unknown`；不发送新请求；Pane 显示「已停止等待，关闭结果未知」；takeover/recovery 保持禁用。
- 测试：新增「pending 取消等待」用例（S7-C02）；断言 DELETE 次数仍为 1、状态 unknown、接管禁用。
- 结论：需用户确认取消语义；确认后由 S7-1 实现，不再是设计阻塞。

### 场景 4：DELETE 已在服务端成功，但浏览器未收到响应

- 冻结预期：第四阶段 §17、F10：失败/响应丢失保留停止状态和明确结果，允许查询或幂等重试，不能自动恢复未知结果；显式查询证实仍可绑定后用户可重新开始人工动作。
- 当前实现：网络错误进入 catch，Pane 内 `closeRequested=false`，显示通用错误；接管准入重新开放。
- 修订后目标：协调器进入 `unknown`，保持非 idle；Pane 销毁不丢状态；新 Pane 打开时读取 unknown，不自动 prepare；详情返回 CONNECTED/DISCONNECTED 也保持 unknown；只能刷新/重试/关闭标签。
- 测试：新增「响应丢失 + Pane 销毁 + 重开」用例（S7-C02、C03、C04）；断言零 takeover POST/WS。
- 结论：需用户确认 unknown 的准入；确认后不阻塞。

### 场景 5：DELETE 失败或网络超时，服务端实际结果不确定

- 冻结预期：同场景 4；D1=A 规定 GET 只支持主动/生命周期取消，不引入新的本地定时期限。
- 当前实现：没有本地超时；网络错误与 HTTP 业务错误都走同一 catch；Pane 内 `closeRequested=false`。
- 修订后目标：协调器按 §8.2 分类：可证明的前置拒绝 → `failed`，其余 → `unknown`；两者都保持非 idle，允许幂等重试/刷新；不把 4xx/5xx 直接当 failed。
- 测试：新增网络错误、`AUTH_REQUIRED`、`INVALID_REQUEST`、5xx、非 JSON 响应分类用例（S7-C05、C18）。
- 结论：需用户确认分类表；不新增本地超时。

### 场景 6：关闭 Session 时，原 Pane 正处于 recovery、retry_wait、takeover、confirming 或 connecting

- 冻结预期：S6 §10.1：关闭通过 `stop()` 使 takeover 失效；`stop()` 使 recovery 失效并停止 Client；`confirming` 时 Modal 独占界面。
- 当前实现：`stop()` 先 `takeover.invalidate()`，再 `recovery.stop()`（内部 `invalidate()` + `client.stop()`）；`closeRemote` 并行发 DELETE。
- 修订后目标：协调器先同步置 `pending`，再调用 Pane 注册的 `stop()`，最后发 DELETE；各阶段结果按 sequence 丢弃；不产生第二次 takeover。
- 测试：S6-T38 覆盖 takeover 四阶段；新增 recovery `retry_wait` × 关闭用例（S7-C19）。
- 结论：通过（实现需把顺序固定为 pending → stop → DELETE）。

### 场景 7：用户退出 Session 管理视图，但仍希望保留本地 SSH Pane

- 冻结预期：实施计划 §11.1、§12.1：切换视图保留 Pane；SessionManager 卸载只取消列表 GET；不把关闭列表当关闭 SSH。
- 当前实现：`SessionManager` 用 `v-if` 卸载；`TerminalPane` 用 `v-show` 保持挂载；`records` 不因视图切换变化。
- 修订后目标：协调器归 HomePage，SessionManager 卸载不影响关闭任务；Pane 保留。
- 测试：S6-T33。
- 结论：通过。

### 场景 8：用户关闭本地 Pane，但不希望关闭远端 SSH Session

- 冻结预期：核心设计 §10.4、第四阶段 §17：关闭标签只 dispose 本地资源，不执行 DELETE。
- 当前实现：`closeLocal()` → `close()` → `takeover.invalidate()`、`recovery.invalidate()`、`client.dispose()`；没有 DELETE。
- 修订后目标：`close()` 只退订协调器、注销本地 stop 并 dispose；**不 abort 协调器任务**；关闭标签不发送 DELETE；不进入 `ended`。
- 测试：新增「关闭标签零 DELETE」「关闭标签后协调器任务仍在」「关闭标签不产生 ended」（S7-C01、C21）。
- 结论：通过（实现需调整 `close()` 不再 abort）。

### 场景 9：用户退出登录时，多个 Pane 分别处于正常连接、恢复中、接管中、初始化失败或清理失败

- 冻结预期：S6 §6.5：同步 `logoutPending` → 同步 stop 全部 → 立即 logout → 等 stop → 成功后 close 全部 → 跳转；stop/close 失败分别统计。U9：登出成功优先。
- 当前实现：`signOut()` 按此顺序；`registeredPanes()` 包含 ready/init_failed 的句柄；dispose_failed 的 `close()` 返回同一失败；`allSettled` 收集。
- 修订后目标：在 `logoutPending` 后同步 `coordinator.dispose()`；协调器不再发新请求；stop/close 顺序不变。
- 测试：S6-T35/T50/T39；新增 init_failed/dispose_failed 参与 signOut 的用例（S7-C21）。
- 结论：通过（需补协调器 dispose 与测试）。

### 场景 10：logout 请求失败，但本地 Pane 已经 stop

- 冻结预期：第四阶段 §13.2：保持身份与退出意图，停留本页，显示「退出未完成，请重试」，不 close，不自动恢复。
- 当前实现：catch 中等待 stop 屏障，写 notice，返回；`logoutPending` 仍为真；Pane 已 stop。
- 修订后目标：协调器已 dispose；关闭任务不继续；页面停留，用户重试 logout。
- 测试：S6-T50；新增「logout 失败 + 协调器 pending」用例（S7-C21），断言零新请求。
- 结论：通过（需明确协调器 dispose 的代价）。

### 场景 11：logout 成功，但部分 Pane close 失败

- 冻结预期：U9、R33：仍跳转登录页；记录失败；提示「本地资源未释放」；不阻止退出。
- 当前实现：`Promise.allSettled` 收集 close；`router.replace('/login')`；跳转后写 `authNotice`。
- 修订后目标：不变；协调器随页面 dispose。
- 测试：S6-T50。
- 结论：通过。

### 场景 12：stop、logout、close 的完成顺序不同，或其中任一步骤抛错

- 冻结预期：logout 与 stop 并行；close 只在 logout 成功且 stop 屏障完成后；logout 失败不 close；任一步骤失败不阻止后续合法步骤（登出成功优先）。
- 当前实现：顺序一致；`stopTask` 在 logout 之前启动；`await stopTask` 在 logout 成功/失败分支中都会执行；close 在 stop 之后。
- 修订后目标：`coordinator.dispose()` 在 stop 之前同步执行；stop/close 的同步 throw 包装为 Promise；`router.replace` reject 被捕获。
- 测试：S6-T50；新增同步 throw、router reject、dispose 幂等用例（S7-C20、C22）。
- 结论：顺序通过；同步 throw 是 MAJOR 实施缺陷（S7-M4）。

### 场景 13：路由守卫、认证失效和主动 signOut 同时发生

- 冻结预期：第四阶段 §13.2：主动退出未完成时 AUTH_REQUIRED 不得覆盖「退出未完成」；`expireIdentity()` 在 `logoutPending` 时返回 false。
- 当前实现：`expireIdentity()` 检查 `logoutPending`；guard 在 `logoutPending` 时重定向 `/login`；`expired()` 在 `expireIdentity()` 返回 false 时不做跳转。
- 修订后目标：不变；协调器 disposed 后不再发新请求。
- 测试：`auth.test.mjs`；新增真实 router 顺序的浏览器用例（S7-6）。
- 结论：通过（真实 router 顺序为 OBSERVATION）。

### 场景 14：旧列表请求、旧 DELETE 响应或旧 Pane 回调晚于页面生命周期返回

- 冻结预期：D1=A：列表 sequence/Abort/disposed；旧 finally 不清新 loading；迟到响应不能发布。Pane 的旧回调按 `life`/taskId 隔离；页面卸载后不联网。
- 当前实现：列表满足；Pane 的 GET/takeover 有 sequence 和 `live()`；`closeRemote` 的 DELETE 没有 abort/epoch，`finally` 仍会写 `remoteBusy`、`revision` 并 emit。
- 修订后目标：DELETE 归协调器，每个任务有 `epoch`；详情查询有 `queryEpoch`；旧响应只更新匹配的任务；Pane 订阅 token 过滤旧 Pane；页面 dispose 后 disposed 检查丢弃响应。
- 测试：新增「旧 Pane 销毁后响应迟到」「旧响应不覆盖新任务」「dispose 后响应丢弃」「旧 GET 不覆盖新 DELETE」（S7-C06、C07、C18、C22）。
- 结论：需按 S7-1 实现 epoch/queryEpoch/dispose；不再是设计阻塞。

## 10. 时序分析

### 10.1 远端关闭（协调器版，推荐）

```text
用户点击「关闭 SSH 会话」
  -> Pane 打开确认框（不停止任何任务）
用户点击「确认关闭」
  -> coordinator.requestClose(id)                 [同步]
       -> state = pending; epoch++; publish       [同步]
       -> Pane 注册的 localStop()                 [同步]
            -> takeover.invalidate()
            -> recovery.stop() -> client.stop() 同步部分
       -> fetch DELETE                            [异步]
  -> 200 -> requested；abort/网络/不可证明错误 -> unknown；可证明前置拒绝 -> failed
  -> Pane 订阅状态，显示「正在请求/已请求/未知/失败」
  -> 用户可 cancelWaiting / retryClose / refreshClose
  -> refreshClose：GET 详情；按 §4.4 表处理；SESSION_NOT_FOUND -> ended
  -> 关闭标签：Pane 退订 + 注销本地 stop + dispose；协调器任务继续
  -> HomePage dispose/登出：abort DELETE 与查询；不声称撤销服务端关闭
```

关键不变量：

- DELETE 200 不等于 CLOSED，也不释放远端 Session；本地记录的移除只发生在服务端权威 `ended` 或用户主动关闭本地标签时，后者不释放远端 Session。
- 关闭标签只释放本地 Pane，不 abort 协调器任务，不发送 DELETE，不产生 `ended`。
- 协调器是唯一 DELETE 所有者；Pane/SessionManager 不直接调用 `closeSession`。
- 关闭意图与 takeover/recovery 状态分离；协调器只提供 `state === 'idle'` 的准入。

### 10.2 Pane 销毁与重新创建

```text
Pane 发起 requestClose（pending/unknown）
  -> 用户关闭 Pane 标签
  -> Pane.close()：解除订阅；注销本地 stop；dispose client/xterm；不 abort 协调器
  -> 协调器继续等待 DELETE / 保持 unknown
  -> 用户从 SessionManager 再次打开同一 Session
  -> HomePage 检查 coordinator.getCloseState(id)
  -> 非 idle：创建/定位 Pane，但不自动 prepare；takeover/手动恢复禁用
  -> Pane 显示关闭状态 + 重试/刷新
  -> 旧 DELETE 响应到达：协调器按 epoch 更新状态；旧 Pane 无订阅，不更新 UI
```

### 10.3 页面卸载与重新进入（S6 候选 Pane 完整时序）

```text
HomePage 卸载/刷新
  -> coordinator.dispose()：abort DELETE 与查询；disposed=true；清空订阅
  -> 迟到响应被丢弃；不发送新请求
  -> 新页面：新协调器为空
  -> 列表可能仍是旧快照；不能据此接管
  -> 用户打开 Session
  -> HomePage 同步预占并创建唯一候选 Pane（S6 方案 B）
  -> 候选 Pane 挂载：不自动连接
  -> Pane 详情 GET（takeover.prepare）：
       CLOSING -> 不显示接管确认；提示正在关闭；禁止 takeover
       SESSION_NOT_FOUND -> 按 §4.5 证据 A 进入 ended
       CONNECTED/DISCONNECTED -> 显示接管确认；用户确认后服务端复查
       网络错误/超时/响应非法 -> 保留候选 Pane；显示查询错误；不显示确认；可重新查询
  -> 不自动 attach/takeover；不凭本地无记录恢复
```

设计限制：本地 unknown 不跨页面；刷新后只能依赖服务端详情/提交结果。详情接口无法证明旧 DELETE 未生效，这是明确的能力边界，不是实现遗漏。

### 10.4 详情查询与重试并发

```text
refreshClose A: q=++queryEpoch; GET A 在途
  -> 用户点击重试
       -> queryEpoch++（A 失效）；abort A
       -> epoch++; state=pending; publish
       -> DELETE 在途
  -> GET A 返回：queryEpoch 不匹配 -> 丢弃
  -> DELETE 返回：按 §8.2 分类 -> requested/unknown/failed

refreshClose B: q=++queryEpoch; GET B 在途
  -> 用户再次 refresh
       -> queryEpoch++; abort B
       -> GET C 在途
  -> B 返回：丢弃
  -> C 返回：按 §4.4 表发布

pending 时 refresh
  -> 不发 GET；返回当前快照
```

### 10.5 登出

```text
用户点击「退出登录」
  -> 同步：signingOut=true；logoutPending=true
  -> 同步：operations.abort()；query.abort()；clearTimeout(debounce)；probe=null
  -> 同步：manager.invalidate()；coordinator.dispose()
  -> 同步：对已登记 Pane 发起 stop()（takeover.invalidate + recovery/client stop）
  -> 立即：logout()（auth.ts 设置 logoutPending=true，identityEpoch++，单请求）
  -> logout 失败：await stop；写「退出未完成，请重试」 + stop 失败数；return
  -> logout 成功：await stop；对全部句柄 close()；收集 stop/close 失败
  -> router.replace('/login')；跳转后写 authNotice 清理反馈
  -> finally：signingOut=false
```

顺序约束：

- `coordinator.dispose()` 必须在任何新的关闭/查询请求之前；它不等待服务端。
- stop 在 logout 之前发起，但不必在 logout 之前完成。
- close 必须在 logout 成功且 stop 屏障完成后开始。
- logout 失败不得 close、不得跳转；协调器已 dispose，关闭任务不继续。
- 登出期间不得发送 Session DELETE；已在途的 DELETE 只被本地 abort 等待，服务端可能继续。

### 10.6 失败组合

| 组合 | 预期行为 | 目标实现 |
|---|---|---|
| stop 失败 + logout 成功 | 跳转；提示「未确认停止」；记录日志 | 保留 + 同步 throw 防护 |
| stop 失败 + logout 失败 | 停留；提示「退出未完成 + 未确认停止」；可重试 | 保留 + 协调器 dispose |
| close 失败 + logout 成功 | 跳转；提示「本地资源未释放」；记录日志 | 保留 |
| close 失败 + logout 失败 | 不 close、不跳转 | 保留 |
| DELETE 失败 + 关闭标签 | 协调器保持 failed/unknown；Pane 销毁不丢状态 | S7-1 |
| DELETE 成功 + stop 失败 | 协调器 requested；提示本地未停止 | 分离提示 |
| Pane 销毁 + 协调器 pending | 协调器继续；新 Pane 识别非 idle | S7-1 |
| 页面刷新 + 旧 DELETE 迟到 | 旧页面 disposed 丢弃；新页面靠详情确认 | S7-1/S7-5 |
| 详情 GET 失败 + 关闭意图 | 保留原关闭状态；只显示查询错误 | S7-1 |
| 本地 Pane 关闭 + 服务端仍在 | 不产生 ended；远端 Session 保留 | S7-1/S7-5 |

### 10.7 旧 DELETE 与新页面 takeover 的竞态

```text
旧页面：DELETE 发出（未到达）
  -> 页面卸载：dispose() 只停止本地等待，请求仍在网络中
  -> 新页面：新协调器为空；候选 Pane 详情 GET 返回 CONNECTED
  -> 用户确认 takeover；服务端复查通过并绑定
  -> 旧 DELETE 到达；Manager.Close 幂等执行
  -> Session 进入 CLOSING，新 Binding 以 closed:user_closed 结束
  -> 新页面收到 closed，进入 ended
```

该时序说明：详情 GET 与提交时复查都无法证明旧 DELETE 不存在。当前设计不提供跨页面强一致性；该风险作为 D1 接受条件（§4.7.3、§18）。

## 11. 测试覆盖矩阵及缺失项

### 11.1 现有覆盖（S6 基线）

| 场景 | 现有测试 | 结论 |
|---|---|---|
| 候选 Pane 零 Prepare/零 WS | S6-T13、S6-T43（浏览器） | 通过 |
| 接管与关闭确认互斥 | S6-T24 | 通过 |
| takeover 四阶段 × stop/close/identity/closeRemote | S6-T38（40 例） | 通过 |
| HomePage 各阶段卸载 | S6-T39（5 例） | 通过 |
| 多 Pane 并发接管不同 Session | S6-T40 | 通过 |
| signOut 正常路径 | S6-T35 | 通过 |
| signOut stop/close 失败 | S6-T50 | 通过 |
| logout 串行、失败保留意图、晚到 me | `auth.test.mjs` | 通过 |
| 列表乱序、旧 finally、身份变化 | `session-list.test.mjs` 控制器用例 | 通过 |
| 列表卸载/登出/Abort | S6-T39、控制器用例 | 通过 |
| 真实浏览器接管/取消/视觉 | S6-T41–T44 | 通过 |

### 11.2 缺失覆盖（S7 必须补）

| 场景 | 缺失 | 归属任务 |
|---|---|---|
| Pane 销毁后协调器保留 pending/unknown | 无协调器；无测试 | S7-1 |
| 新 Pane 识别遗留关闭状态 | 无入口 | S7-1 |
| 非 idle 时零 takeover POST/WS | 无用例 | S7-1 |
| 取消等待/abort → unknown | 无 AbortSignal/状态 | S7-1 |
| DELETE 200 后不伪报 CLOSED | 无断言 | S7-1 |
| DELETE 失败/网络错误/响应丢失 | 无用例 | S7-1 |
| 幂等重试、单 Session 单 DELETE | 无用例 | S7-1 |
| 迟到响应 epoch 隔离 | 无用例 | S7-1 |
| 关闭标签不 abort 协调器 | 无用例 | S7-1 |
| 关闭标签零 DELETE | 无显式断言 | S7-1 |
| 关闭确认框内显示错误 | 无用例 | S7-1 |
| 详情 GET 不解除 requested/unknown/failed | 无协调器；无测试 | S7-1 |
| 详情 GET 与重试并发 | 无 queryEpoch | S7-1 |
| ended 只认服务端证据 | 无来源断言 | S7-1 |
| 行级关闭入口与意图路由 | 无入口 | S7-2 |
| CONNECTING/CLOSING 行级关闭 | 无入口 | S7-2 |
| signOut 同步 invalidate + dispose | 无 fetch 计数断言 | S7-3/S7-4 |
| 登出期间 paneChanged 零 GET | 无用例 | S7-3 |
| 同批 changed 只发一次 GET | 无用例 | S7-3 |
| stop/close 同步 throw | 无用例 | S7-4 |
| router.replace reject | 无用例 | S7-4 |
| init_failed/dispose_failed 参与 signOut | 无用例 | S7-4 |
| recovery retry_wait × 关闭 | 无用例 | S7-5 |
| 页面刷新后详情确认 | 无用例 | S7-5/S7-6 |
| 真实浏览器行级关闭/失败/登出 | 无 S7 脚本 | S7-6 |

### 11.3 新增生命周期与所有权测试场景（第三轮扩充，第四轮增补）

层级说明：U=协调器单元；P=Pane 组件；H=HomePage/SessionManager；B=浏览器/真实组件。网络请求数量指该场景中新增的 fetch/WS 次数，不含场景开始前已完成的列表查询。

| 编号 | 层级 | 模拟条件 | 预期状态 | 预期网络请求 | 所有权边界 |
|---|---|---|---|---|---|
| S7-C01 | U/P | Pane 发起 DELETE 后处于 pending，关闭 Pane | 协调器仍 pending；Pane 已退订；无 closed/ended | DELETE 1；关闭后新增 0 | 协调器持有任务；Pane 只退订 |
| S7-C02 | U/P | pending 时 abort，再关闭 Pane | 协调器 unknown；Pane 已销毁；状态保留 | DELETE 1；abort 0 新请求 | 只有协调器 abort |
| S7-C03 | H/P | 同一 Session 新建 Pane，协调器 unknown | 新 Pane 显示 unknown；不进入候选确认 | POST 0；WS 0；详情 GET 0（不自动 prepare） | HomePage 路由；Pane 只读 |
| S7-C04 | P | 新 Pane 在 unknown 下挂载 | 不自动调用 `recover()`/`prepare()`；手动恢复禁用 | 0 | Pane 读取协调器准入 |
| S7-C05 | U | unknown 下 retryClose | 状态 pending → requested/unknown/failed；只一个有效请求 | DELETE 1 | 协调器去重 |
| S7-C06 | U/P | 旧 Pane 销毁后 DELETE 响应迟到 | 协调器更新；旧 Pane 无 UI 更新 | 0 新请求 | epoch + 订阅解除 |
| S7-C07 | U | 旧响应迟到，同 Session 已有新任务 | 旧响应被 epoch 丢弃；新任务状态不变 | 0 新请求 | 每任务 epoch |
| S7-C08 | H | SessionManager 行级关闭与 Pane 关闭同时触发 | 协调器只创建一个任务；一个确认/一个 DELETE | DELETE 1 | HomePage 路由 + 协调器去重 |
| S7-C09 | H | 无 Pane 行级关闭与新 Pane 创建竞争 | 协调器先占用；新 Pane 识别非 idle，不 prepare | DELETE 1；POST 0；WS 0 | 单一协调器 |
| S7-C10 | U | 两个不同 Session 并行关闭 | 两个独立 CloseTask；状态互不影响 | DELETE 2（各 1） | 按 Session ID 隔离 |
| S7-C11 | H | HomePage 卸载时仍有在途 DELETE | dispose abort 本地等待；disposed 后无新请求；迟到响应丢弃 | DELETE 1；卸载后新增 0 | HomePage 唯一 dispose |
| S7-C12 | B/H | 页面重新进入，服务端详情返回 CLOSING | 候选 Pane 不进入确认；禁止 takeover | 详情 GET 1；POST 0；WS 0 | 服务端权威 |
| S7-C13 | B/H | 页面重新进入，详情 GET 网络错误 | 候选 Pane 保留；显示错误；不自动恢复；不显示确认 | 详情 GET 1；POST 0；WS 0 | 不凭本地无记录恢复 |
| S7-C14 | H | 登出与 DELETE pending 并发 | 协调器 dispose；无新列表 GET/详情 GET/DELETE/WS；stop/logout/close 顺序正确 | 新增 0（除 logout 本身） | logoutPending + dispose |
| S7-C15 | H/P | dispose、signOut、Pane close 交错 | 只 abort 一次；只 dispose 一次；无迟到 UI；无重复释放 | 0 新请求 | 幂等 dispose/退订 |
| S7-C16 | U/P | requested 下详情返回 CONNECTED/DISCONNECTED | 保持 requested；不回 idle；takeover 继续禁用 | 详情 GET 1；POST 0；WS 0 | 本地意图优先 |
| S7-C17 | U | unknown 下详情返回 CLOSING | 升格为 requested；允许重试；仍禁用 takeover | 详情 GET 1；DELETE 0 | 服务端 CLOSING 证据 |
| S7-C18 | U | 详情 GET 在途时 retryClose | GET 被 abort/丢弃；只发一次 DELETE；状态由 DELETE 决定 | 详情 GET 1；DELETE 1 | queryEpoch + epoch |
| S7-C19 | P | recovery retry_wait 时确认关闭 | 停止重试；不自动 attach；takeover/recovery 禁用 | WS 0（关闭后）；DELETE 1 | 协调器准入 + Pane.stop |
| S7-C20 | U/H | Pane 本地 stop 同步抛错 | 仍发送 DELETE；`localStopFailed=true`；提示本地未停止 | DELETE 1 | 协调器捕获本地异常 |
| S7-C21 | U/P | 本地 close / WS 1006 / client.dispose 后，再收到服务端 closed 或详情 NOT_FOUND | 前三种不产生 ended；后两种产生 ended | 按场景 0 或 1 次 GET | ended 只认服务端证据 |
| S7-C22 | U/H | 多个 refresh 并发；ended 后列表返回旧行 | 只有最后一次 refresh 可发布；旧行被 tombstone 过滤，不能重开 | 详情 GET ≥1；POST 0；WS 0 | queryEpoch + tombstone |
| S7-C23 | P | Pane 确认关闭，检查 localStop 同步部分 | takeover/recovery 已失效；DELETE 在同步部分完成后发出 | DELETE 1 | localStop 同步契约 |
| S7-C24 | U | localStop 同步 throw | 仍发 DELETE；`localStopFailed=true`；状态非 idle；不恢复准入 | DELETE 1 | 协调器捕获本地异常 |
| S7-C25 | U | localStop Promise reject | 同 C24；提示本地未停止 | DELETE 1 | 异步失败不改关闭状态 |
| S7-C26 | U | Pane A 注销时 Pane B 已注册 | B 的注册保留；A 的注销为空操作 | 0 | 注册 token 隔离 |
| S7-C27 | U | 重复注销、dispose 后注册 | 幂等；不保存回调；不发布状态 | 0 | dispose 边界 |
| S7-C28 | U/B | `taken_over` / `AUTH_REQUIRED` / WS 1006 / 本地 stop | 均不产生 ended；仅服务端 `closed` 或 `NOT_FOUND` 产生 ended | 按场景 | ended 证据 |

补充断言：

- S7-C01/C02/C11 必须检查 `closed` 事件次数为 0（Pane 关闭标签不是 Session closed）。
- S7-C03/C04/C12/C13/C16 必须检查零 `POST /takeover`、零 WebSocket。
- S7-C05/C08/C09/C10/C18 必须检查 DELETE 请求体/路径按 Session ID 隔离，且没有第二个 controller。
- S7-C14 必须检查 `manager.refresh()` 次数为 0、`closeSession` 次数不增加、WebSocket 构造次数为 0。
- S7-C15 必须检查协调器 `dispose()` 幂等、Pane `close()` 不 abort。
- S7-C16/C17 必须检查状态不会回到 `idle`。
- S7-C21 必须分别断言「本地关闭/WS 断开不置 ended」与「服务端 closed 消息/详情 NOT_FOUND 置 ended」。
- S7-C22 必须断言旧 refresh 的响应不写 `message`、不更新 `updatedAt`、不通知订阅者。
- S7-C23 必须用调用顺序断言 `takeover.invalidate()`、`recovery.stop()` 同步部分、`client.stop()` 同步部分都先于 DELETE。
- S7-C24/C25 必须断言 `state !== 'idle'`、`localStopFailed === true`、takeover/recovery 准入仍为假。
- S7-C26/C27 必须断言旧注销不移除新注册、dispose 后注册不触发回调、不发布状态。
- S7-C28 必须断言 `taken_over`、`AUTH_REQUIRED`、WS 1006、本地 stop 都不产生 ended tombstone。

## 12. S7 任务计划草案（第三轮修订）

编号仅用于本提案，不代表已冻结的实施顺序；任务依赖以本节表格为准。

### S7-0：设计裁决与计划冻结

| 项目 | 内容 |
|---|---|
| 任务目标 | 把 D1–D5 的用户裁决写入计划，消除「待裁决」项；不写代码 |
| 前置依赖 | 用户回答第 18 节问题 |
| 修改文件 | 仅文档（修改 `doc/stage-5-implementation-plan.md` 或新增 S7 子计划，需单独授权） |
| 不修改范围 | 所有生产代码、测试、配置、依赖、既有冻结文档（除非用户明确授权修订） |
| 核心实现 | 记录协调器所有权、状态机、生命周期、D2 行级关闭、D3 登出、D4 列表、D5 反馈与 DELETE 分类 |
| 测试要求 | 无 |
| 验收标准 | 裁决条目逐项有明确结论；任务边界、文件范围、验收条件可执行 |
| 回滚边界 | 只撤销本次文档增量；不影响代码 |

### S7-1：统一关闭协调器、DELETE 状态机与详情查询（完整强耦合单元）

| 项目 | 内容 |
|---|---|
| 任务目标 | 实现 HomePage 持有的 `CloseCoordinator`：按 Session ID 管理 pending/requested/unknown/failed/ended、DELETE controller、query controller、epoch/queryEpoch、重试、去重、订阅、dispose、本地 stop 注册；Pane 只保留关闭 UI；Pane 销毁不丢状态；详情查询不解除关闭意图；ended 只认服务端证据 |
| 前置依赖 | S7-0 的 D1 裁决 |
| 修改文件 | 新增 `frontend/src/session/close.ts`；修改 `frontend/src/api/sessions.ts`、`frontend/src/components/terminal/TerminalPane.vue`、`frontend/src/pages/HomePage.vue`、`frontend/tests/terminal-pane.test.mjs`、`frontend/tests/session-list.test.mjs`（HomePage 部分）；必要时新增 `frontend/tests/session-close.test.mjs` |
| 不修改范围 | `client.ts`、`output.ts`、`recovery.ts`、`terminal/takeover.ts`（除非仅做准入读取适配）、`auth.ts`、`router`、后端、`Modal.vue` |
| 核心实现 | `CloseCoordinator`/`CloseTask`/`CloseSnapshot`/`CloseHandle`；`closeSession(id, signal?)`；§4.3 状态机；§4.4 详情判定；§4.5 ended 权威；§5.4 并发规则；Pane 通过句柄请求/重试/取消/刷新；HomePage 创建/dispose/路由；非 idle 时禁止 takeover/手动恢复；Pane `close()` 只退订；新 Pane 识别遗留状态；单 Session 单 DELETE；epoch/queryEpoch/dispose 迟到隔离；`ended` tombstone |
| 测试要求 | S7-C01–C07、C10、C15–C28；DELETE pending/200/可证明拒绝/网络错误/abort；unknown 禁止 takeover；重试幂等；旧 Pane/旧响应；关闭标签零 DELETE；关闭成功不 emit closed/不释放预占；关闭确认框错误可见；详情 GET 不解除关闭意图；ended 只认服务端证据 |
| 验收标准 | 协调器是唯一 DELETE 所有者；Pane 销毁后状态仍在；新 Pane 非 idle 时零 takeover POST/WS；同一 Session 至多一个有效 DELETE；详情查询不把非 idle 改回 idle；ended 只由服务端证据产生；dispose 幂等；`npm test` 相关文件通过 |
| 回滚边界 | 撤销 `session/close.ts`、`closeSession` 可选参数、Pane/HomePage 接线增量；恢复 S6 Pane 内 `closeRemote`；不影响候选/takeover |

说明：S7-1 通过协调器单元测试即可验证无 Pane 路径（直接调用 `requestClose/refreshClose`），不要求提前完成 SessionManager 行级 UI。S7-1 不得留下「Pane 仍拥有 DELETE、S7-2 再迁移」的中间架构。

### S7-2：SessionManager 行级关闭入口（只加意图）

| 项目 | 内容 |
|---|---|
| 任务目标 | 为列表每行提供「关闭」入口，满足核心设计 §9 的 CONNECTING/CLOSING 操作；只 emit 意图，复用 S7-1 协调器 |
| 前置依赖 | S7-0 的 D2 裁决；S7-1 完成 |
| 修改文件 | `frontend/src/components/session/SessionManager.vue`、`frontend/src/pages/HomePage.vue`、`frontend/tests/session-list.test.mjs` |
| 不修改范围 | `TerminalPane.vue` 的候选/takeover 逻辑、`session/close.ts` 的状态机、`client.ts`、`recovery.ts`、auth/router、后端 |
| 核心实现 | SessionManager 只 `emit('close', item)`；HomePage 有记录 → `coordinator.requestClose(id)`（有 ready Pane 时协调器调用其本地 stop）；无记录 → 页面级确认 + `coordinator.requestClose(id)`；CONNECTING/CLOSING 允许；结果刷新列表 |
| 测试要求 | 行按钮 emit 当前 item；有 Pane 时零 HomePage DELETE；无 Pane 时恰好一次 DELETE；确认取消零 DELETE；重复点击幂等；CONNECTING/CLOSING；多 Session 不串扰；S7-C08/C09 |
| 验收标准 | SessionManager 不直接 DELETE；同一 Session 任意时刻只有一个 DELETE 所有者；关闭列表不误删 SSH；关闭 Pane 不发 DELETE；S7-2 不新增第二套状态机 |
| 回滚边界 | 移除操作列「关闭」按钮与 HomePage 路由增量；保留 S7-1 协调器；不改变「打开」行为 |

### S7-3：列表同步失效与 changed 刷新合并

| 项目 | 内容 |
|---|---|
| 任务目标 | 登出/身份失效/卸载时同步撤销列表发布身份；同一同步批次多个 changed 合并为一次 GET；打开前检查协调器状态；ended tombstone 过滤 |
| 前置依赖 | S7-0 的 D4 裁决；S7-1（读取协调器状态） |
| 修改文件 | `frontend/src/pages/HomePage.vue`、`frontend/src/components/session/SessionManager.vue`、`frontend/tests/session-list.test.mjs`；`session/list.ts` 仅在发现真实缺陷时修改 |
| 不修改范围 | `SessionListController` 的既有 sequence/Abort 语义、auth/router、Pane 生命周期 |
| 核心实现 | `signOut()` 同步 `manager.invalidate()`；`paneChanged()` 守卫 + `queueMicrotask` 合并；`isAllowed` 增加 `!logoutPending` 同步检查；`openSession` 检查 `getCloseState`；HomePage 订阅协调器状态变化并合并刷新；ended tombstone 过滤 |
| 测试要求 | logoutPending 设置后同步零新 GET；paneChanged 在登出中零 GET；同批多 changed 一次 GET；旧 finally 不清新 loading；隐藏列表只标记待刷新；非 idle 打开不 prepare；ended 旧行不能重开 |
| 验收标准 | fetch 计数符合 D1=A；无定时器/轮询；无旧响应覆盖新快照；登出后零列表请求 |
| 回滚边界 | 撤销守卫与合并；恢复 `paneChanged` 直接 refresh；不影响列表组件本身 |

### S7-4：完整登出语义

| 项目 | 内容 |
|---|---|
| 任务目标 | 完成 D3/D5：stop/logout/close/跳转的失败与异常处理；同步抛错防护；`router.replace` reject 处理；协调器 dispose；反馈分离 |
| 前置依赖 | S7-1（协调器）、S7-3（同步失效）、S7-0 的 D3 裁决 |
| 修改文件 | `frontend/src/pages/HomePage.vue`、`frontend/tests/session-list.test.mjs`、`frontend/tests/auth.test.mjs`（仅扩展测试）；`auth.ts`/`router` 仅在 D3 方案 B 且获授权时修改 |
| 不修改范围 | 认证协议、后端、S5 连接契约、Pane 候选/takeover 状态机、`session/close.ts` 的状态机 |
| 核心实现 | 安全包装 stop/close 调用；`try/catch` 处理 `router.replace`；stop/close 失败分别反馈；logout 失败保持重试；close 失败仍跳转；同步 `coordinator.dispose()`；不发送 Session DELETE |
| 测试要求 | logout 成功/失败/不确定；stop/close reject 与同步 throw；router.replace reject；迟到 AUTH_REQUIRED；init_failed/dispose_failed 参与；多 Pane 混合状态；S7-C14 |
| 验收标准 | 顺序符合 §6.5；logout 成功必跳转；logout 失败不 close 不跳转；`signingOut` 必定复位；登出期间零新 GET/DELETE/WS |
| 回滚边界 | 撤销 signOut 增量；恢复 S6 现有正常路径；不回退第四阶段 logoutPending/stop 语义 |

### S7-5：页面退出与迟到回调隔离

| 项目 | 内容 |
|---|---|
| 任务目标 | 页面卸载、关闭标签、登出、迟到 GET/DELETE/WS 回调不修改已失效 Pane，不启动新任务；不重复 abort/释放；刷新后按 S6 候选 Pane 流程确认 |
| 前置依赖 | S7-1、S7-4 |
| 修改文件 | `frontend/src/components/terminal/TerminalPane.vue`、`frontend/src/pages/HomePage.vue`、对应测试 |
| 不修改范围 | `client.ts`/`output.ts`/`recovery.ts` 的 S5 语义；auth/router；`session/close.ts` 的状态机 |
| 核心实现 | Pane 订阅解除与本地 stop 注销；协调器 dispose/epoch/queryEpoch；HomePage `pageDisposed` 守卫；列表 dispose 保持；页面重进后的详情确认（不自动恢复） |
| 测试要求 | DELETE 在途时关闭标签/卸载；迟到 200/错误；takeover loading 时卸载；recovery retry_wait 时卸载；S7-C11/C12/C13/C15/C21；零新 fetch/WS |
| 验收标准 | 卸载后零新请求；不因迟到回调 emit closed/changed；远端 Session 只由服务端回收；本地记录只在 Pane closed 或 ended 时按规则移除；刷新后不凭本地无记录恢复 |
| 回滚边界 | 撤销订阅/epoch/卸载守卫；保留 S7-1 协调器 |

### S7-6：自动化测试、浏览器验收与报告

| 项目 | 内容 |
|---|---|
| 任务目标 | 补齐 F10–F13、B01–B05 的 S7 子集；执行全量验证；输出报告与截图 |
| 前置依赖 | S7-1 至 S7-5 完成 |
| 修改文件 | 测试文件、`doc/测试报告/stage-5-s7-*-report.md`、日志/截图目录（需授权新增） |
| 不修改范围 | 生产代码（除非发现并报告缺陷后获准定点修复）；冻结文档 |
| 核心实现 | 单元/组件/浏览器分层；Mock 与真实环境分开记录；覆盖 Pane 销毁、重开、页面刷新、登出并发、详情查询与重试并发 |
| 测试要求 | `npm test`、`npm run typecheck`、`npm run build`、`git diff --check`；浏览器至少覆盖行级关闭、DELETE 失败/取消、详情刷新、登出失败/成功、列表退出、Pane 销毁后重开 |
| 验收标准 | 测试数量与结果可复现；截图对应场景；未验证项明确；无伪造真实环境结论 |
| 回滚边界 | 仅撤销测试/报告增量；不影响生产代码 |

## 13. 文件修改范围汇总

| 文件 | S7 允许用途 | 明确不修改 |
|---|---|---|
| `frontend/src/session/close.ts` | **S7-1 新增**：`CloseCoordinator`/`CloseTask`/`CloseSnapshot`/`CloseHandle`；按 Session ID 管理状态、DELETE/query controller、epoch/queryEpoch、订阅、dispose、本地 stop 注册 | 不建全局 Store/事件总线/定时轮询；不管理 takeover/recovery；不渲染 UI |
| `frontend/src/pages/HomePage.vue` | 创建/持有/dispose 协调器；按 Session ID 路由；打开前检查状态；订阅状态变化刷新列表；同步 invalidate；signOut 异常处理；ended tombstone 过滤 | 不在已有 Pane 时另行 DELETE；不复制状态机；不参与 takeover 流程 |
| `frontend/src/components/session/SessionManager.vue` | 操作列「关闭」、`close` 事件、`isAllowed` 同步检查、ended 行禁用 | 不直接 DELETE；不持有关闭状态；不按状态隐藏「打开」 |
| `frontend/src/components/terminal/TerminalPane.vue` | 关闭确认框、取消等待/重试/刷新按钮、状态文案、错误展示；通过 `CloseHandle` 调用协调器；注册/注销本地 stop；`close()` 只退订；详情查询失败保留候选 Pane | 不直接 `closeSession`；不持有 DELETE Promise/controller；不跨 Pane 保留状态；不改候选/takeover 三检查点；不改 `connectTakeover` 调用 |
| `frontend/src/api/sessions.ts` | `closeSession(id, signal?)` | 不改 List/详情 DTO 校验；不改错误码 |
| `frontend/src/session/list.ts` | 仅在发现真实缺陷时最小修改 | 不重写 sequence/Abort；不引入定时器 |
| `frontend/src/terminal/takeover.ts` | 原则上不修改；仅做 `allowed` 读取关闭状态的适配 | 不改阶段、检查点、`invalidate()` 调用约束；保留 `SESSION_NOT_FOUND → markEnded` |
| `frontend/tests/terminal-pane.test.mjs` | Pane 关闭 UI、退订、本地 stop 注册、迟到响应、关闭标签零 DELETE、详情失败保留候选 | 不删除/弱化 S6 用例 |
| `frontend/tests/session-list.test.mjs` | HomePage 协调器接线、行级关闭、signOut、列表失效、changed 合并、tombstone | 不删除/弱化 S6 用例 |
| `frontend/tests/session-close.test.mjs` | 新增协调器单元测试（若采用独立文件） | 不测试真实网络；用受控 fetch/Abort |
| `frontend/tests/auth.test.mjs` | 仅在 D3 需要时扩展 logout 生命周期用例 | 不改认证协议 |
| `frontend/src/styles/base.css` | 仅当行级关闭按钮或关闭状态区需要最小样式时 | 不做无关视觉重构 |
| `frontend/src/stores/auth.ts`、`router/index.ts` | 仅 D3 方案 B 且单独授权时 | 默认不改 |
| 后端、`client.ts`、`output.ts`、`recovery.ts`、`Modal.vue`、冻结文档 | 不允许修改 | — |

## 14. 任务依赖关系

```text
S7-0（裁决）
  ├─> S7-1（统一协调器 + DELETE/详情状态机 + Pane/HomePage 接线）
  │     ├─> S7-2（行级关闭入口）
  │     ├─> S7-3（列表失效/合并/打开前检查）
  │     ├─> S7-4（完整登出）
  │     └─> S7-5（页面退出/迟到回调/刷新后确认）
  └─> S7-2/S7-3/S7-4/S7-5 ──> S7-6（测试/浏览器/报告）
```

强耦合说明：

- S7-1 必须把「协调器 + DELETE 状态 + 详情判定 + Pane 销毁保留 + 重开识别 + 单 Session 去重 + Pane/HomePage 共用状态机」作为一个完整单元；不能拆成「先做 Pane、后补协调器」的中间态。
- S7-2 只加入口，不得再新增第二套状态机；它依赖 S7-1 的 `requestClose`。
- S7-3 依赖 S7-1 的 `getCloseState` 与 tombstone；否则打开前无法阻止非 idle 接管。
- S7-4 依赖 S7-1 的 `dispose()` 与 S7-3 的同步失效；否则登出期间仍可能有新请求。
- S7-5 依赖 S7-1 的 epoch/queryEpoch/dispose；不再重复实现 abort/epoch。

## 15. 验收标准汇总

### 15.1 功能验收

- [ ] 关闭 SSH 的确认框、pending、requested、unknown、failed、重试、取消等待、刷新状态均有明确 UI 和状态。
- [ ] DELETE 200 不显示「已关闭」，不 emit `closed`，不释放 Session ID 预占。
- [ ] 详情 GET 返回 CONNECTED/DISCONNECTED 时，requested/unknown/failed 均不回到 idle。
- [ ] 详情 GET 返回 CLOSING 时，unknown/failed 升格为 requested；不得恢复接管。
- [ ] `ended` 只由详情 SESSION_NOT_FOUND 或服务端 `closed` 控制消息产生；本地关闭/WS 断开/连接任务停止不产生 ended。
- [ ] Pane 销毁后，协调器仍保留 pending/unknown；重新打开同一 Session 不进入 takeover/手动恢复。
- [ ] 新 Pane 在非 idle 状态下不自动 prepare、不显示接管确认框、零 Prepare/WS。
- [ ] 页面刷新后按 S6 方案 B 创建候选 Pane；详情失败保留候选并显示错误，不显示确认。
- [ ] Pane 路径与无 Pane 路径共用同一协调器；同一 Session 至多一个有效 DELETE。
- [ ] 关闭标签不发送 DELETE、不 abort 协调器任务；只退订、注销本地 stop 并 dispose 本地终端。
- [ ] 页面卸载后零新请求；迟到响应不更新已失效 Pane；刷新后不凭本地无记录自动恢复。
- [ ] 登出期间零列表 GET、详情 GET、DELETE、WebSocket、takeover；协调器 dispose 幂等。
- [ ] DELETE 分类符合 §8.2；只有可证明的前置拒绝进入 failed。
- [ ] S6 候选 Pane、takeover、recovery、两步人工 attach 行为不变。
- [ ] `ended` tombstone 防止旧列表行再次打开已结束 Session。

- [ ] localStop 的同步部分（takeover/recovery 失效、client stop 启动）在 DELETE 之前完成；throw/reject 不恢复准入。
- [ ] 旧注销函数不影响新注册；dispose 后注册不执行、不发布状态。
- [ ] `closed` 证据符合 §4.5 链路；`taken_over`/`AUTH_REQUIRED`/WS 断开不产生 ended。
- [ ] 刷新后旧 DELETE 与新 takeover 的竞态已作为 D1 接受条件记录。

### 15.2 测试与构建验收

- [ ] S7-C01–C28 全部通过，且没有删除或弱化既有断言。
- [ ] `npm test` 全量通过。
- [ ] `npm run typecheck` 通过。
- [ ] `npm run build` 通过。
- [ ] `git diff --check` 退出码 0。
- [ ] 浏览器验收覆盖：行级关闭、DELETE pending/失败/取消、详情刷新、Pane 销毁后重开、页面刷新后 CLOSING、登出成功/失败、控制台无 Vue/资源/运行时错误。
- [ ] 报告区分 Mock、浏览器和真实后端/SSH 环境；未验证项明确列出。

## 16. 回滚方案

| 任务 | 回滚方式 |
|---|---|
| S7-0 | 撤销文档增量；不改代码 |
| S7-1 | 撤销 `session/close.ts`、`closeSession` 可选参数、Pane/HomePage 接线增量；恢复 S6 Pane 内 `closeRemote`；保留候选/takeover |
| S7-2 | 移除行级「关闭」按钮与 HomePage 路由；保留 S7-1；恢复 S6 列表 |
| S7-3 | 撤销 `invalidate`/守卫/合并/打开前检查/tombstone；恢复 `paneChanged` 直接 refresh |
| S7-4 | 撤销 signOut 增量；恢复 S6 正常/失败路径；不回退第四阶段语义 |
| S7-5 | 撤销订阅/epoch/卸载守卫；保留 S7-1 |
| S7-6 | 仅撤销测试/报告/日志增量 |

回滚约束：

- 只逐文件/逐 hunk 撤销本任务未提交增量；保护用户同文件修改和 S6 成果。
- 禁止 `reset --hard`、`clean`、整文件覆盖用户内容。
- 若无法安全分离，保留并报告，不强行回滚。
- 回滚本文档时只撤销工作区增量，不修改暂存区与已提交内容。

## 17. 尚未解决的风险与非目标

### 17.1 风险

| 风险 | 说明 | 处理建议 |
|---|---|---|
| 页面刷新后本地 unknown 丢失 | 协调器随 HomePage 销毁；新页面无法证明关闭结果 | 依赖服务端详情/提交；不凭本地无记录恢复；如需更强保证需持久化/后端记录（未授权） |
| 详情无法证明旧 DELETE 失效 | 详情只反映查询瞬间；旧页面 DELETE 可能尚未生效 | 明确列为能力边界；不声称刷新后绝对安全；必要时由用户裁决是否引入持久化 |
| DELETE 结果未知 | 前端无法证明服务端是否已关闭 | unknown 禁止 takeover；只允许刷新/重试/关闭标签 |
| ended 误判 | 把本地关闭/WS 断开当成远端结束会提前释放预占 | 只接受 §4.5 两类服务端证据；测试覆盖 C21 |
| 请求悬挂 | DELETE/logout 无本地超时 | 方案 A 由用户取消；或 D1 方案 B 增加 UI 期限 |
| 协调器内存 | 每个 Session 一个任务；ended tombstone 保留到页面 dispose | 数量受页面内 Session 数限制；dispose 统一释放；不引入全局 Map |
| 非 idle 创建 Pane | Pane 只读展示关闭状态，仍需确保不自动 prepare | Pane 挂载时读取 `CloseHandle`；HomePage 不调用 prepare；测试覆盖零 POST/WS |
| 行级关闭 UI | 无 Pane 时的确认框由 HomePage 持有 | 与 Pane 确认框文案一致；同一 Session 不会同时出现两个确认框 |
| 路由守卫网络错误（U6） | 网络抖动可能被当作身份失效 | 保持独立问题，不在 S7 修改 |
| `authNotice` 覆盖 | logout 后 `/me` 可能先写「登录已过期」 | 跳转后写清理反馈；真实 router 需浏览器验证 |
| 同步抛错 | 自定义 Pane 句柄可能同步 throw | 包装为 Promise；测试覆盖 |
| 真实环境差异 | Mock 不能证明真实网络/SSH 行为 | S7-6 明确标记未验证项，不伪造结论 |
| localStop 契约漂移 | 若未来 `Pane.stop()` 把失效动作移到 await 之后，同步屏障失效 | S7-1 增加顺序断言；变更 stop 需重新审查 |
| `closed` 证据依赖 CLOSING 不可逆 | 若服务端新增「Binding 结束但 Session 仍可 attach」的路径，证据 B 失效 | 记录为协议前提；变更需重新审查 |
| 旧 DELETE 竞态 | 刷新后旧 DELETE 可能在新 takeover 之后到达并关闭新连接 | 列为 D1 接受条件；如需消除需持久化或后端方案 |

### 17.2 非目标

- 不实现 S8 全集成、真实 Docker/NPM/SSH 验收。
- 不修改后端 DELETE/Close 语义。
- 不新增认证协议、Cookie、数据库、配置或依赖。
- 不引入全局 Store、事件总线、定时轮询或第二套 takeover 状态机。
- 不把列表关闭等同于 SSH 关闭，不把 Pane 关闭等同于远端关闭。
- 不修改 S5 `client.ts`、`output.ts`、`recovery.ts`。
- 不新增 localStorage/IndexedDB/后端关闭意图记录（仅作为备选列出）。
- 不提前实现 S8。

## 18. 需要用户明确裁决的问题

裁决分两层：**D1 只决定整体所有权与架构边界**；D1-A～D1-E 是独立技术规则，必须分别裁决。接受 D1 不等于自动接受 D1-A～D1-E。

### 18.1 第一层：D1 整体架构决策（BLOCKER）

接受 D1 表示同时接受以下全部内容：

1. HomePage 持有按 Session ID 管理的 `CloseCoordinator`；
2. Pane 只负责关闭 UI，不拥有跨 Pane 生命周期的关闭任务；
3. Pane 销毁只退订、不 abort、不清除协调器任务；
4. Pane 路径与无 Pane 路径共用同一状态机；
5. `unknown/requested/failed` 期间禁止 takeover 与手动恢复；
6. 页面刷新后不持久化 unknown，依赖服务端详情/提交结果；
7. **接受 §4.7.2 的旧 DELETE / 新 takeover 竞态**：刷新后旧 DELETE 可能在新页面 takeover 成功之后到达并关闭新连接，现有接口无法消除；
8. 授权新增 `frontend/src/session/close.ts` 作为 S7-1 正式文件。

D1 只裁决整体所有权与架构边界，不包含 D1-A～D1-E 的具体协议规则。

### 18.2 第二层：独立技术规则

| 编号 | 推荐方案 | 拒绝推荐方案的后果 | 影响 S7-1 | 依赖 D1 |
|---|---|---|---|---|
| D1-A | §4.4 详情判定表：详情 GET 永不把非 idle 解除为 idle；unknown/failed + CLOSING 升格为 requested | 关闭意图可能被一次普通查询解除，重新放开 takeover | 是：状态机核心 | 是 |
| D1-B | §4.5 `ended` 只接受详情 `SESSION_NOT_FOUND` 与服务端 `closed` 控制消息 | 误把本地关闭/WS 断开当成远端结束，提前释放预占 | 是：ended 判定 | 是 |
| D1-C | §4.8.1 / §4.8.5 同步顺序：建立 pending → 本地 stop 同步部分 → DELETE | 关闭与 takeover 之间出现竞态窗口 | 是：requestClose 实现 | 是 |
| D1-D | §8.2 DELETE 分类表：`failed` 仅限可证明的前置拒绝，其余 unknown | 误判「服务端未执行」，可能提前放开接管 | 是：错误分类 | 是 |
| D1-E | 本地 abort 只停止等待、不新增本地超时、`failed` 保留关闭意图、DELETE 200 不等于 CLOSED | 取消等待被误解为撤销关闭，或 200 被当成 CLOSED | 是：状态语义 | 部分（引用第三轮已列裁决） |

每项均可在 D1 被接受后单独调整；调整 D1-A～D1-E 不改变 D1 的所有权结论，但会改变 S7-1 的状态机细节。

### 18.3 D2–D5 与既有裁决

| 编号 | 级别 | 内容 | 依赖 / 引用 |
|---|---|---|---|
| D2-1 | BLOCKER | 是否增加 SessionManager 行级「关闭」；CONNECTING 允许关闭、CLOSING 允许幂等重复关闭（推荐均为是） | 依赖 D1、D1-C |
| D2-2 | MAJOR | 非 idle 时打开 Session：创建「只读关闭状态 Pane」还是 HomePage 直接提示（推荐前者） | 依赖 D1 |
| D3-1 | MAJOR | 确认 `logoutPending` 不覆盖本地 close、`auth.ts` 不改、`signOut` 同步 `dispose()` | 引用 S6 §6.5 已冻结顺序与第四阶段 §13.2；本项只确认实施选择，不重新裁决登出语义 |
| D4-1 | MAJOR | SessionManager 直接读取 `logoutPending`、`queueMicrotask` 合并、ended tombstone 过滤 | 引用实施计划 §6.1 D1=A；依赖 D1、D1-A |
| D5-1 | MINOR | 关闭确认框内显示 DELETE 错误、localStop 失败独立提示 | 引用第四阶段 §17 的「失败保留明确结果」；不新增裁决 |
| D5-2 | MINOR | `router.replace` reject 时记录日志并仍写清理反馈 | 实施细节；不改登出成功语义 |
| S7-0 | — | 是否授权在裁决后修订实施计划或新增 S7 子计划文档 | 本轮未修改任何冻结文档 |

不存在「接受 D1 即自动接受 D1-A～D1-E」的隐含关系；也不存在与已冻结规则重复的裁决项。

## 19. 结论

1. **`closed` 证据已核实。** 它只在 `Session.reap` 中通过 `old.end("closed:" + reason)` 产生，发出时 Session 处于不可逆的 `CLOSING`；`canBind` 与 `closeLocked` 保证不会回到 CONNECTED/DISCONNECTED。因此可以保留为 `ended` 证据，但依据必须写成「CLOSING 不可逆」，不是「Binding 结束」。
2. **localStop 同步契约已补齐。** 当前 `Pane.stop()` 在返回 Promise 前已完成 takeover/recovery 失效与 `client.stop()` 同步部分，满足「先 stop 后 DELETE」；同步 throw / Promise reject 均不恢复准入，DELETE 继续，失败独立反馈。
3. **刷新竞态已如实记录。** 旧 DELETE 可能在新页面 takeover 之后到达；详情 GET 与提交复查都无法消除。该风险已写入 §4.7.2，并作为 D1 的接受条件，不是普通备注。
4. **裁决已分层。** D1 只决定所有权与架构；D1-A～D1-E 是独立技术规则，分别说明推荐方案、拒绝后果、对 S7-1 的影响与对 D1 的依赖。
5. **接口与术语已统一。** `getCloseState` 区分「从未创建 / 已创建 idle / ended tombstone / disposed」；`registerLocalStop` 使用 token 身份隔离；状态发布规则覆盖注销、旧 Pane、dispose 与重复注销。
6. **当前仍不具备直接开始 S7 编码的条件。** 需要用户先对 §18 的 D1、D1-A～D1-E、D2-1 作出裁决；裁决后按 S7-0 → S7-1 → S7-2 → S7-3 → S7-4 → S7-5 → S7-6 推进。
7. **本轮没有修改任何生产代码、测试、配置或既有冻结文档**；只修改本提案文档。Git 工作区只有本提案的未暂存修改；暂存区为空。
