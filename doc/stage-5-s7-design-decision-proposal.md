# Web SSH 第五阶段 S7 设计裁决准备与任务计划草案（第二轮定点修订）

> 状态：提案稿。本文中的推荐方案、状态机、任务拆分和验收标准均 **待用户裁决，不得视为已冻结设计**。
>
> 本轮是第二轮定点修订：只解决“Pane 销毁后关闭状态丢失”和“Pane/无 Pane 两套关闭状态机”两个问题，并同步修订相关章节。没有修改任何生产代码、测试、配置或既有冻结文档，没有开始 S7 编码。

## 0. 本轮范围与修订摘要

### 0.1 上一轮提案的两个遗留问题

1. **BLOCKER：Pane 销毁后，DELETE 的 unknown 关闭状态可能丢失。** 上一轮把关闭状态放在 `TerminalPane` 内；Pane 关闭标签时 `close()` 会 abort DELETE 并销毁组件，状态随组件消失。用户再次从 SessionManager 打开同一 Session 时，新 Pane 可能依据尚未更新的列表状态重新进入 takeover 或手动恢复。
2. **MAJOR：S7-1 的 Pane 内状态机与 S7-2 的 HomePage 无 Pane 路径可能形成两套实现。** 上一轮把 `frontend/src/session/close.ts` 写成 S7-2 的可选文件，S7-1 并不依赖它；这会导致 pending、unknown、abort、重试和迟到响应在两个地方各写一遍。

### 0.2 本轮推荐方向（待用户裁决）

- 由 **HomePage 持有页面级 `CloseCoordinator`**，按 Session ID 管理关闭状态、AbortController、DELETE、重试、epoch 和订阅。
- **TerminalPane 只负责关闭 UI**：确认、取消等待、重试、刷新状态和展示；通过 HomePage 注入的 `CloseHandle` 调用协调器；Pane 销毁只退订和释放终端，不 abort、不清除关闭任务。
- **SessionManager 只 emit 关闭意图**；HomePage 路由到同一个协调器；SessionManager 不直接 DELETE，不持有关闭状态。
- **Pane 路径与无 Pane 路径共用同一个状态机**；`frontend/src/session/close.ts` 作为 S7-1 的正式文件，而不是 S7-2 的可选项。
- **页面卸载/浏览器刷新后本地状态不可恢复**；新页面不把“本地无记录”当作可安全接管的依据，必须以服务端详情查询为权威，并保留“本地 unknown 无法跨页面”的明确设计限制。
- 不新增 localStorage、IndexedDB、全局 Store、后端接口或数据库字段；如确需这些能力，只作为备选方案列出，并要求单独授权。

### 0.3 本轮允许与禁止

- 允许：读取当前提案、相关冻结文档和代码；分析替代方案；只修改 `doc/stage-5-s7-design-decision-proposal.md`；执行文档 diff 和一致性检查。
- 禁止且未执行：修改生产代码、测试、其他文档、S5/S6/第四阶段冻结契约、API、配置、依赖或后端；commit、push、merge、rebase、reset、clean；切换分支；部署；连接真实 SSH 或生产环境；开始 S7-0 之后的编码；进入 S8。

## 1. 审查依据与当前基线

### 1.1 Git 基线（本轮开始时实测）

| 项目 | 实际值 |
|---|---|
| 分支 | `S6` |
| HEAD | `4ebe66820ca894b2527742be20237c72ca8e92c4` |
| HEAD 提交说明 | `4ebe668 S6-C2` |
| 暂存区 | `A  doc/stage-5-s7-design-decision-proposal.md`（上一轮提案已被用户暂存） |
| 工作区 | ` M luan-qi-ba-zao.txt` |
| 未暂存 | `luan-qi-ba-zao.txt` 的打包命令修改；本轮修改提案后，提案会显示为“已暂存 + 工作区已修改” |
| 未跟踪文件 | 无 |
| `git diff --check` | 退出码 0，无空白错误 |

最近提交：

```text
4ebe668 S6-C2
9ba0ba5 S6-C1
fe6fb9c S6-C1
```

保护说明：

- `luan-qi-ba-zao.txt` 是用户所有，本轮未修改、未格式化、未还原。
- 提案文档已被用户暂存；本轮只在工作区修改它，不执行 `git add`，因此暂存区仍保留上一轮版本，工作区是本轮修订版。最终报告会同时列出两者。
- 不执行任何会覆盖暂存区或用户修改的 Git 操作。

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
| `doc/web-ssh-prototype-v2.html` | 会话表格行级“打开/关闭”操作原型（第 606 行附近） |

### 1.3 实际检查的代码与测试

生产代码：

| 文件 | 重点 |
|---|---|
| `frontend/src/pages/HomePage.vue` | `openSession`、`registerPane`、`paneClosed`、`paneChanged`、`signOut`、视图切换、SessionManager 挂载参数 |
| `frontend/src/components/session/SessionManager.vue` | 列表控制器、`canRefresh`/`authToken` 监听、visibilitychange、`open` 事件、操作列、`defineExpose` |
| `frontend/src/components/terminal/TerminalPane.vue` | `life`、`stop()`、`close()`、`closeRemote()`、初始化、watcher、工具栏、关闭 SSH 确认框、接管确认框 |
| `frontend/src/session/list.ts` | `SessionListController` 的 sequence、Abort、`isAllowed`、`currentIdentity`、dispose |
| `frontend/src/terminal/takeover.ts` | `SessionTakeover` 阶段、三个检查点、`invalidate()` 调用约束 |
| `frontend/src/terminal/recovery.ts` | `refresh`、`recover`、`stop`、`allowed` |
| `frontend/src/terminal/client.ts` | `running`、`taskId`、`connectTakeover`、`stop`、`dispose`（只读核对 S5 契约） |
| `frontend/src/api/sessions.ts` | `closeSession`、`listSessions`、`sessionView`、`sessionList` |
| `frontend/src/api/management.ts` | `request`、`AUTH_REQUIRED` 回调、`message` |
| `frontend/src/stores/auth.ts` | `identity`、`logoutPending`、`logoutTask`、`checkIdentity`、`logout`、`expireIdentity` |
| `frontend/src/router/index.ts` | `beforeEach`、`expired`、`router.replace` |
| `frontend/src/components/layout/Modal.vue` | `<dialog showModal>` 行为 |
| `backend/internal/httpapi/sessions.go` | `DELETE /api/sessions/{id}` 的幂等返回 |
| `backend/internal/session/session.go` | `Manager.Close`、`Session.Close`、`closeLocked` 的幂等性 |

测试：

- `frontend/tests/auth.test.mjs`
- `frontend/tests/session-list.test.mjs`
- `frontend/tests/session-takeover.test.mjs`
- `frontend/tests/terminal-pane.test.mjs`
- `frontend/tests/stage-5-c2.test.mjs`
- `frontend/tests/client.test.mjs`（只读核对既有 S5 契约）

### 1.4 与首轮 S7 复审报告的关系与修正

仓库中不存在 `doc/测试报告/stage-5-s7-design-audit-report.md`，也没有同名或近似的 S7 审查文件；首轮报告只输出在终端。本文没有把首轮结论当作事实，而是重新从冻结文档和当前代码推导。首轮提案相对复审的修正仍然有效：

1. **DELETE 失败/响应丢失并非完全未定义。** 第四阶段 §17 与 F10 已规定：失败或响应丢失时保留停止状态和明确结果，允许查询实际状态或重试幂等关闭，不能自动恢复未知结果；显式查询证实仍可绑定后，用户可重新开始人工动作。真正未定义的是取消等待的语义、是否设置本地超时、结果未知期间是否禁止接管以及如何退出未知状态。
2. **行级关闭不是可选项。** 核心设计 §9 明确 CONNECTING“允许取消/关闭”、CLOSING“重复关闭幂等”，原型也提供行级“关闭”按钮；当前 SessionManager 只有“打开”，CONNECTING/CLOSING 无法创建 Pane，因此没有任何关闭入口。
3. **`logoutPending` 在 HTTP 200 时复位与冻结契约并不冲突。** 第四阶段 §13.2 规定 logout 200 后“清 Cookie/身份”，`auth.ts` 与之一致；`logoutPending` 不能兼作本地 close 的 busy 标志。真正的缺口是 `signOut` 没有同步让列表控制器失效。
4. `paneChanged` 缺少登出守卫、SessionManager 的 `canRefresh` watcher 非同步、同一同步批次多个 changed 未合并，均经代码核对属实。

### 1.5 本轮定点修订记录（2026-11-19）

| 问题 | 原提案状态 | 本轮修订 |
|---|---|---|
| Pane 销毁后 unknown 丢失 | 关闭状态在 Pane 内；`close()` abort DELETE 并销毁组件 | 关闭任务上移到 HomePage 的 `CloseCoordinator`；Pane `close()` 只退订，不 abort、不清状态 |
| Pane/无 Pane 两套状态机 | S7-1 在 Pane 内实现；S7-2 才可选新增 `session/close.ts` | `session/close.ts` 成为 S7-1 正式文件；Pane 与无 Pane 路径共用同一协调器 |
| 页面刷新后的本地状态 | 原提案只说明“迟到回调隔离”，未说明状态丢失 | 新增“页面卸载/刷新后本地 unknown 不可恢复”的限制与服务端权威确认规则 |
| 登出与关闭任务 | 原提案未说明协调器在登出时的行为 | 明确 `signOut` 同步 dispose 协调器，禁止新 GET/DELETE/WS |
| 测试矩阵 | 无 Pane 销毁、重开、双所有者、页面重进用例 | 新增 15 个生命周期/所有权测试场景 |

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
| 本地关闭标签 | `TerminalPane.vue:237-239`、`201-227`：只 `close()`，不 DELETE | 保留“不 DELETE”；但 `close()` 不再 abort 协调器任务，只退订 |
| 行级关闭 | `SessionManager.vue:281-288` 只有“打开” | S7-2 增加“关闭”，只 emit 意图 |
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
  │     │     ├── controller: AbortController | null
  │     │     ├── epoch: number
  │     │     ├── promise: Promise<CloseSnapshot> | null
  │     │     └── subscribers: Set<listener>
  │     ├── requestClose(sessionId)
  │     ├── retryClose(sessionId)
  │     ├── cancelWaiting(sessionId)
  │     ├── refreshClose(sessionId)
  │     ├── getCloseState(sessionId)
  │     ├── subscribeClose(sessionId, listener)
  │     └── dispose()
  │
  ├── TerminalPane（只读快照 + UI）
  │     ├── 确认关闭 → closeHandle.requestClose()
  │     ├── 取消等待 → closeHandle.cancelWaiting()
  │     ├── 重试 → closeHandle.retryClose()
  │     ├── 刷新状态 → closeHandle.refreshClose()
  │     └── 展示 state/message
  │
  └── SessionManager（只 emit）
        └── emit('close', item) → HomePage → coordinator.requestClose(item.id)
```

所有权不变量：

- 一个 Session ID 在页面生命周期内至多一个 `CloseTask`；同一时刻至多一个有效 DELETE 请求。
- 只有协调器持有 AbortController 和 DELETE Promise；Pane 和 SessionManager 都不持有。
- Pane 销毁只退订，不 abort、不清除任务；协调器继续持有状态。
- 只有 HomePage 卸载/登出调用 `dispose()` 才 abort 全部本地等待；abort 不等于服务端撤销。
- 关闭状态与 takeover/recovery 状态分离：协调器只提供“是否允许接管”的只读判断，不复制 takeover 状态机。
## 3. 问题清单与严重程度（本轮修订）

| 编号 | 级别 | 问题 | 文档 | 代码/现状 | 是否需用户裁决 |
|---|---|---|---|---|---|
| S7-B1 | BLOCKER | Pane 销毁后，DELETE 的 unknown/closeRequested 随组件消失；重新打开同一 Session 可能绕过关闭意图 | 实施计划 §11.3、§12.2；第四阶段 §17；F10 | `TerminalPane.vue:366-390`、`201-227` | 是 |
| S7-B2 | BLOCKER | Pane 内状态机与无 Pane 路径缺少统一所有权；`session/close.ts` 被列为 S7-2 可选，可能形成两套实现 | 实施计划 §15 S7 行；核心设计 §9 | `SessionManager.vue:281-288`；`HomePage.vue:78-94` | 是 |
| S7-M1 | MAJOR | `logoutPending` 在 200 时复位，是否需要保持到本地 close 完成存在语义歧义 | 第四阶段 §13.2；S6 §6.5 | `auth.ts:39-51`；`HomePage.vue:353-389` | 是（确认一次） |
| S7-M2 | MAJOR | `signOut` 未同步 `invalidate` 列表；`paneChanged` 无登出守卫；`isAllowed` 在同步边界内读取旧 prop | 实施计划 §6.1、§12.2；D1=A | `HomePage.vue:109-112`、`353-389`；`SessionManager.vue:41-45`、`108-113` | 否（实施待办） |
| S7-M3 | MAJOR | 同一同步批次多个 `changed` 未合并为一次 GET | 实施计划 §6.1 D1=A | `HomePage.vue:109-112` | 否（实施待办） |
| S7-M4 | MAJOR | `handle.stop()`/`handle.close()` 同步抛错会逃出 `signOut` 的 `try` | S6 §6.5；R32/R33 | `HomePage.vue:361-363`、`375-381` | 否（健壮性修复） |
| S7-M5 | MAJOR | 关闭协调器与登出的任务失效策略未定义时，可能在 logout 期间继续发起 GET/DELETE | 第四阶段 §13.1；实施计划 §12.2 | 本轮新增设计 | 是（D3 确认） |
| S7-N1 | MINOR | DELETE 失败只写通用 `message(error)`，且错误显示在 `dialog` 之外 | F10；第四阶段 §17 | `TerminalPane.vue:379-380`、`269-284` | 否 |
| S7-N2 | MINOR | `closeRemote` 成功但 `life !== 'ready'` 时静默返回；迟到响应没有 epoch 隔离 | S6 §12.2；D1=A | `TerminalPane.vue:373-375` | 否（S7-1 解决） |
| S7-N3 | MINOR | 缺少“关闭标签不发送 DELETE”的显式断言 | 核心设计 §10.4；第四阶段 §17 | `terminal-pane.test.mjs` | 否 |
| S7-N4 | MINOR | 缺少 init_failed/dispose_failed 参与 signOut、recovery `retry_wait` 与关闭组合的测试 | S6 §12.4、R32/R33 | `session-list.test.mjs`、`terminal-pane.test.mjs` | 否 |
| S7-N5 | MINOR | 协调器任务在页面生命周期内的内存回收规则未明确 | 本轮新增设计 | `session/close.ts`（待新增） | 否（S7-1 明确） |
| S7-O1 | OBSERVATION | 路由守卫把网络错误当作身份失效（U6） | S6 §19.2 | `router/index.ts:29-38` | 否（独立风险） |
| S7-O2 | OBSERVATION | logout 成功后 `router.replace` 触发 `/me`，`authNotice` 可能先被改写 | S6 §6.5、R33 | `HomePage.vue:382-385`；`router/index.ts:23-39` | 否 |
| S7-O3 | OBSERVATION | DELETE 与 logout 都没有本地超时；请求悬挂时 UI 只能等待或取消 | D1=A；第四阶段 §13 | `api/sessions.ts`、`auth.ts` | 是（与 D1 一并裁决） |
| S7-O4 | OBSERVATION | 后端 `DELETE` 对不存在或已关闭的 Session 也返回 200；前端不能靠状态码区分 | 核心设计 §11.4；架构 §4.3 | `backend/internal/httpapi/sessions.go:64-70`；`session.go:251-256` | 否 |
| S7-O5 | OBSERVATION | `closeRequested` 在 DELETE 成功后保持为真，这是“关闭意图不撤销”的现有实现 | 实施计划 §11.3 | `TerminalPane.vue:370-388` | 否（迁入协调器） |
| S7-O6 | OBSERVATION | 页面刷新后本地 unknown 不可恢复；只能依赖服务端详情查询 | 本轮新增设计限制 | `session/close.ts`（待新增） | 是（D1 确认） |

## 4. D1：关闭任务所有权、状态与生命周期

### 4.1 必须区分的概念

| 概念 | 是否可被浏览器改变 | 说明 |
|---|---|---|
| 用户取消本地等待 | 是 | 只停止本地 Promise 的等待，不改变服务端 |
| `AbortController` 终止本地 HTTP | 是 | fetch 以 `AbortError` 结束；请求可能已到达服务端 |
| 服务端取消关闭操作 | 否 | 现有 API 没有“撤销关闭”；DELETE 一旦被服务端接受即进入幂等关闭 |
| 服务端已执行关闭但响应丢失 | 否 | 前端只能标记为 unknown |
| 服务端尚未执行关闭、请求失败 | 部分可推断 | 明确业务错误响应可视为未接受；网络错误不能推断 |
| 请求结果无法确认 | — | unknown，必须保留关闭意图 |
| Pane 销毁 | 否 | 只释放本地 UI/终端，不等于关闭服务端，也不等于放弃关闭意图 |

结论：**本地 abort 只意味着前端停止等待，不意味着服务端撤销关闭；Pane 销毁也不能成为自动放弃关闭意图的途径。**

### 4.2 推荐所有权模型：HomePage 的 CloseCoordinator

推荐在 `frontend/src/session/close.ts` 实现 `CloseCoordinator`，由 HomePage 在页面生命周期内持有。理由：

1. **关闭任务的生命周期长于 Pane。** Pane 关闭标签后，DELETE 可能仍在途或结果未知；状态必须由页面级所有者保留。
2. **单一 DELETE 所有者。** 协调器按 Session ID 去重，Pane 和 SessionManager 都只能调用它，避免双 DELETE。
3. **统一状态机。** Pane 路径与无 Pane 路径共用 pending/requested/unknown/failed 转换、abort、epoch、重试和刷新。
4. **页面卸载有明确边界。** HomePage 卸载时统一 `dispose()`，取消本地等待并隔离迟到响应；不会出现 Pane 和 HomePage 各自 abort 同一请求。
5. **不引入全局 Store。** 协调器是 HomePage 私有的普通类实例，不是全局单例，不使用事件总线或定时轮询。

不推荐把关闭任务继续放在 Pane 内，也不推荐“Pane 关闭时把任务移交给 HomePage”的转移模型：转移需要额外的所有权交接、并发窗口和迟到回调隔离，复杂度高于直接由 HomePage 持有。

### 4.3 每个 Session 的关闭状态机（待用户裁决）

状态由协调器按 Session ID 维护；`closeRequested` 不再由 Pane 独立维护，而是 `state !== 'idle'` 的派生值。

| 状态 | 含义 | 进入条件 | 允许操作 | 退出 |
|---|---|---|---|---|
| `idle` | 无关闭意图 | 初始；从未确认关闭 | 接管、手动恢复、刷新、关闭 SSH | `requestClose` → `pending` |
| `pending` | 已确认，DELETE 在途；本地 `stop()` 已由 Pane 发起 | `requestClose` 成功占用 | 取消等待（本地 abort）、关闭标签 | 200 → `requested`；abort/网络错误 → `unknown`；明确业务错误 → `failed` |
| `requested` | 服务端已接受幂等关闭（200），物理回收未完成 | `pending` 收到 200 | 刷新状态、幂等重试、关闭标签 | 详情 CLOSING → 保持；SESSION_NOT_FOUND → `ended` |
| `unknown` | 请求已发出但结果无法确认 | `pending` 被 abort、网络错误或响应丢失 | 刷新状态、幂等重试、关闭标签 | 重试 → `pending`/`requested`；查询确认结束 → `ended` |
| `failed` | 服务端明确拒绝，未接受关闭 | `pending` 收到明确业务错误 | 刷新状态、幂等重试、关闭标签 | 重试 → `pending`；查询确认结束 → `ended` |
| `ended` | 已确认 Session 结束 | 详情 SESSION_NOT_FOUND，或连接任务以 `closed` 停止 | 关闭本地标签 | 页面卸载时随协调器释放 |

状态转换要点：

1. `pending` 的“取消等待”只调用协调器的 `AbortController.abort()`，随后进入 `unknown`；提示必须是“已停止等待，关闭结果未知”，不能是“已取消关闭”。
2. `unknown` 不得自动回到 `idle`；`getCloseState` 保持非 idle，接管和手动恢复保持禁用。
3. `requested`、`unknown`、`failed` 都允许用户显式重试 DELETE；后端 `Manager.Close` 对 CLOSING/CLOSED/不存在均为幂等成功（`session.go:251-256`、`closeLocked` 早返回），因此重试安全。
4. 只有详情查询返回 `SESSION_NOT_FOUND`，或连接任务以 `closed` 停止，才进入 `ended`；DELETE 200 不等于 CLOSED（第四阶段 §17）。
5. `ended` 作为页面内 tombstone 保留到协调器 dispose，防止列表中尚未更新的旧行再次打开同一 Session；Session ID 不会复用（核心设计 §11.4），因此 tombstone 不会误伤新 Session。
6. 关闭任务与 takeover/recovery 状态分离：协调器只提供 `state !== 'idle'` 的只读准入，不管理 takeover 阶段。

### 4.4 生命周期边界（必须明确）

| 场景 | 必须行为 |
|---|---|
| 关闭 Pane 标签 | Pane `close()` 只退订协调器、释放 xterm/client；**不 abort** 协调器任务，不清除 pending/unknown；DELETE 继续由协调器等待 |
| Pane 被重新创建 | HomePage 创建 Pane 前读取 `coordinator.getCloseState(sessionId)`；非 idle 时新 Pane 不自动 `takeover.prepare()`，只展示关闭状态/重试；takeover 和手动恢复保持禁用 |
| SessionManager 卸载 | 只 dispose 列表控制器；协调器归 HomePage，不受影响；已发出的 DELETE 继续 |
| HomePage 页面卸载 | `coordinator.dispose()`：abort 所有本地等待、标记 disposed、清空订阅；迟到响应被 epoch/disposed 丢弃；不声称撤销服务端关闭 |
| 页面重新进入（刷新/新标签） | 本地关闭状态不存在；不得仅凭“本地无记录”认为可接管；必须由新 Pane 的详情 GET 或用户显式刷新确认服务端状态 |
| 用户登出 | `signOut` 同步设置 `logoutPending` 后立即 `coordinator.dispose()`；协调器不再发起新的 GET/DELETE/WS；随后按 S6 §6.5 stop/logout/close |
| DELETE 返回 200 | 只标记 `requested`；不 emit `closed`，不释放 Session ID 预占，不显示“已完全关闭” |
| DELETE 网络错误/abort | 进入 `unknown`；保留关闭意图；不自动回 idle |
| DELETE 明确业务拒绝 | 进入 `failed`；保留关闭意图；只允许重试/刷新 |
| 重试 DELETE | 协调器在同一 Session 上复用/替换唯一任务；同一时刻只有一个有效请求所有者 |
| 关闭状态变化 | 协调器通知当前仍订阅的 Pane 和 HomePage；已销毁 Pane 不会收到 UI 更新 |
| 状态释放 | `ended` 保留为 tombstone；页面 dispose 时统一释放；不在 Pane 销毁时释放 |

### 4.5 页面卸载与浏览器刷新：设计限制

这是本轮必须明确回答的部分。

**HomePage 卸载/浏览器刷新后，协调器和本地 unknown 状态都会消失。** 当前架构没有跨页面持久化；本轮不新增 localStorage、IndexedDB、全局 Store 或后端字段。因此：

1. 页面卸载后，服务端仍可能继续执行关闭；前端无法在下一个页面证明“关闭已完成”或“关闭未发生”。
2. 新页面不能仅凭“本地没有关闭记录”就认为 Session 可以安全接管。
3. 新页面必须通过服务端权威接口确认状态：
   - 列表只是快照，不能作为接管依据；
   - 候选 Pane 的详情 GET 是接管前的必要检查；
   - 详情返回 CLOSING → 禁止接管；
   - 详情返回 SESSION_NOT_FOUND → 标记结束；
   - 详情返回 CONNECTED/DISCONNECTED → 仍需用户显式确认，提交时服务端再次校验；
   - 详情 GET 失败/超时 → 不生成候选，不允许接管或手动恢复。
4. 如果详情 GET 在关闭提交之前被服务端处理，可能短暂返回 CONNECTED；此时用户确认 takeover 后，服务端提交会因 NOT_READY 或关闭先完成而拒绝。服务端是最终权威，前端不把列表/详情快照当作关闭已取消的证明。
5. 若用户要求“页面刷新后仍禁止接管，直到人工确认关闭结果”，则需要持久化关闭意图（localStorage/IndexedDB）或后端关闭意图记录。这两者都超出当前授权，列为备选方案，不默认实施。

**明确的设计限制**：本地 `unknown` 不能跨页面存活；跨页面只能依赖服务端详情/提交结果。不得在文档或 UI 中声称“刷新后仍保留 unknown”。

### 4.6 备选方案比较

| 方案 | 内容 | 优点 | 缺点 | 是否推荐 |
|---|---|---|---|---|
| A（推荐） | HomePage 持有 `CloseCoordinator`，按 Session ID 管理 | 单一所有者；Pane 销毁不丢状态；Pane/无 Pane 共用；页面卸载边界清晰 | 新增一个前端模块；HomePage 需要注入句柄 | 是 |
| B | Pane 持有任务，关闭时把任务移交给 HomePage | 不新增页面级所有者 | 交接窗口复杂；迟到回调/epoch 需要跨组件；容易出现双所有者 | 否 |
| C | 模块级单例 Map（全局关闭任务） | 跨 Pane 简单 | 等价于全局 Store；跨 HomePage/登录周期泄漏；测试隔离差 | 否 |
| D | localStorage/IndexedDB 持久化 unknown | 刷新后仍能阻止接管 | 新增持久化、清理、跨标签同步和隐私边界；当前授权不允许 | 否（备选） |
| E | 后端新增“关闭意图”记录/接口 | 跨设备/刷新权威 | 修改 API/数据库/后端；超出 S7 授权 | 否（备选） |

### 4.7 对现有模块的影响

| 模块 | 影响 |
|---|---|
| `frontend/src/session/close.ts` | 新增 `CloseCoordinator`、`CloseTask`、`CloseSnapshot`、`CloseHandle` |
| `HomePage.vue` | 创建/持有/dispose 协调器；按 Session ID 路由；打开 Session 前检查状态；订阅状态变化刷新列表 |
| `TerminalPane.vue` | 移除 Pane 内 `closeRequested` 驱动的 DELETE 所有权；改为读取 `closeHandle`；`close()` 只退订；确认/取消/重试/刷新调用句柄 |
| `SessionManager.vue` | 只增加 `close` 事件；不持有关闭状态 |
| `api/sessions.ts` | `closeSession(id, signal?)`；仍不承担 UI/所有权 |
| `terminal/takeover.ts` | 原则上不修改；`allowed` 读取 `closeHandle.state === 'idle'` |
| `client.ts`/`output.ts`/`recovery.ts` | 不修改 |
| `auth.ts`/`router` | 默认不修改 |

### 4.8 需要用户确认的具体事项

1. 是否同意由 HomePage 持有按 Session ID 管理的 `CloseCoordinator`。
2. 是否同意 Pane 只负责关闭 UI，不拥有跨 Pane 生命周期的 DELETE 任务。
3. 是否同意 Pane 销毁不清除 pending/unknown 关闭状态。
4. 页面卸载后，是否接受依靠服务端权威查询重新确认状态，而不是本地持久化关闭意图。
5. 是否授权新增 `frontend/src/session/close.ts` 作为 S7-1 的正式文件。
6. 是否确认 Pane 与无 Pane 两种关闭路径共用同一套状态机。
7. 是否确认 unknown 状态下禁止 takeover 和手动恢复。
8. 是否确认 unknown 状态只允许查询、幂等重试和关闭本地标签，不允许自动放弃关闭意图。
9. 原有 D1 裁决是否保留：本地 abort 只停止等待、不新增本地超时、`failed` 保留关闭意图、DELETE 200 不等于 CLOSED。
## 5. D2：Pane 与无 Pane 的统一关闭状态机

### 5.1 原提案为什么会出现两套实现

首轮提案把 S7-1 定义为“Pane 内关闭状态机”，把 S7-2 定义为“SessionManager 行级关闭”，并把 `session/close.ts` 写成 S7-2 的可选文件。结果是：

- S7-1 在 `TerminalPane.vue` 内实现 pending/unknown/abort/重试；
- S7-2 在 HomePage 为无 Pane 会话再实现一次 DELETE；
- 两者对 epoch、迟到响应、abort、重试和错误分类的处理可能不一致；
- 同一个 Session 在 Pane 与列表两个入口同时操作时，可能出现两个 DELETE 所有者。

本轮修订把关闭任务上移为页面级唯一所有者，从根本上消除这两套实现。

### 5.2 推荐架构（待用户裁决）

```text
HomePage
  │
  ├── CloseCoordinator（每个 HomePage 一个）
  │     ├── 按 sessionId 管理 CloseTask
  │     ├── 持有 AbortController、epoch、Promise、订阅者
  │     └── 唯一执行 DELETE / 重试 / 刷新 / 取消等待
  │
  ├── TerminalPane（每个 Session 一个）
  │     ├── 通过 CloseHandle 读取状态、订阅变化
  │     ├── 确认关闭 → closeHandle.requestClose()
  │     ├── 取消等待 → closeHandle.cancelWaiting()
  │     ├── 重试关闭 → closeHandle.retryClose()
  │     ├── 刷新状态 → closeHandle.refreshClose()
  │     └── 展示关闭状态/错误
  │
  └── SessionManager
        └── emit('close', item)
              │
              └── HomePage.closeSession(item.id) → coordinator.requestClose(id)
```

职责关系：

| 角色 | 负责 | 不负责 |
|---|---|---|
| `CloseCoordinator` | 状态、AbortController、DELETE、epoch、重试、去重、订阅、dispose | 不渲染 UI；不管理 takeover/recovery；不持有 xterm/client |
| `TerminalPane` | 关闭确认框、取消等待/重试/刷新按钮、状态文案、错误展示 | 不直接 `closeSession`；不持有 DELETE Promise/controller；不跨 Pane 保留状态 |
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

export interface CloseSnapshot {
  sessionId: string
  state: CloseState
  message: string
  updatedAt: number
}

export interface CloseCoordinator {
  // 查询当前快照；未知 Session 返回 idle。
  getCloseState(sessionId: string): CloseSnapshot

  // 用户确认关闭：idle -> pending；非 idle 时返回当前快照，不重复发 DELETE。
  requestClose(sessionId: string): Promise<CloseSnapshot>

  // 在 requested/unknown/failed 下幂等重试；其他状态返回当前快照。
  retryClose(sessionId: string): Promise<CloseSnapshot>

  // 仅 pending 可用：abort 本地等待，状态 -> unknown；不发送新请求。
  cancelWaiting(sessionId: string): void

  // 查询服务端详情：CLOSING 保持；SESSION_NOT_FOUND -> ended；
  // 网络错误不改变关闭状态，只返回当前快照并附带错误消息。
  refreshClose(sessionId: string): Promise<CloseSnapshot>

  // 订阅状态变化；立即回调一次当前快照；返回退订函数。
  subscribeClose(
    sessionId: string,
    listener: (snapshot: CloseSnapshot) => void,
  ): () => void

  // 页面卸载/登出：abort 全部本地等待、标记 disposed、清空订阅；
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
}
```

接口所有权说明：

1. **谁调用**：Pane 通过 `CloseHandle` 调用；SessionManager 只 emit，HomePage 调用 `coordinator.requestClose(item.id)`；HomePage 自己订阅状态变化刷新列表。
2. **谁拥有状态**：`CloseCoordinator` 的 `Map<sessionId, CloseTask>`。
3. **谁拥有 AbortController**：`CloseCoordinator`；Pane 和 SessionManager 都不持有。
4. **谁负责 DELETE**：`CloseCoordinator` 的 `requestClose/retryClose`；其他模块不得调用 `closeSession`。
5. **谁负责通知 UI**：协调器通过 `subscribeClose` 通知；Pane 只更新仍然有效的订阅；HomePage 用同一通知做列表刷新合并。
6. **Pane 销毁后任务如何继续**：Pane 退订；协调器的 Promise/controller/state 不变；任务继续到 200/失败/unknown。
7. **HomePage 销毁后如何隔离**：`dispose()` abort 本地等待、标记 disposed、清空订阅；迟到响应检查 disposed/epoch 后丢弃；不发起新请求。
8. **同一 Session 重复请求如何去重**：`requestClose` 在 pending 时返回同一个 `promise`；在 requested/unknown/failed 时返回当前快照，只有 `retryClose` 才发新 DELETE；同一时刻至多一个 controller。
9. **迟到响应如何隔离**：每个 `CloseTask` 有单调 `epoch`；每次 request/retry 递增；响应返回后检查 `epoch`、`disposed`、`sessionId` 和当前任务身份；不匹配则丢弃。
10. **关闭状态何时释放**：`ended` 作为 tombstone 保留到 `dispose()`；`idle` 不保留任务对象；页面卸载统一释放。Pane 销毁不释放。

### 5.4 HomePage 的打开与路由规则

`openSession(item)` 在创建/定位 Pane 前检查协调器：

| 协调器状态 | 行为 |
|---|---|
| `idle` | 正常候选 Pane 流程（S6 方案 B） |
| `pending` | 已有 Pane → 定位；无 Pane → 创建 Pane 但**不自动 prepare**，只展示“正在请求关闭”；takeover/手动恢复禁用 |
| `requested` | 同上；展示“已请求关闭，正在回收”；允许刷新/重试 |
| `unknown` | 同上；展示“关闭结果未知”；允许刷新/重试；禁止 takeover/手动恢复 |
| `failed` | 同上；展示“关闭请求失败”；允许重试；禁止 takeover/手动恢复 |
| `ended` | 不创建 Pane；提示“Session 已结束”；列表中旧行不会再次打开 |

说明：

- “创建 Pane 但不自动 prepare”不是进入候选 takeover 流程；Pane 只是关闭状态的展示/操作载体，不申请凭证、不创建 WebSocket、不显示接管确认框。
- 如果不希望为关闭状态创建 Pane，备选是 HomePage 直接提示并提供页面级“重试关闭/刷新状态”；但这会少一个统一的关闭 UI，推荐仍创建只读 Pane。
- 已有 Pane 时只定位，不创建第二个 Pane；重复点击不会产生第二个 DELETE。

无 Pane 的行级关闭：

- SessionManager 只 emit `close`；
- HomePage 调用 `coordinator.requestClose(item.id)`；
- 二次确认由 HomePage 的页面级 Modal 负责（或复用 Pane 的确认文案）；
- 由于没有 Pane，不存在 takeover/recovery 互斥问题，但仍必须走同一协调器。

有 Pane 的行级关闭：

- HomePage 定位 Pane，并调用该 Pane 的 `requestClose()`（Pane 内部再调用 `CloseHandle.requestClose()`）；
- 如果 Pane 尚未 ready（`handle === null`），提示“终端正在初始化，请稍后重试”；
- HomePage 绝不在有 Pane 时自行 `closeSession`。

### 5.5 TerminalPane 的关闭 UI 规则

- 工具栏“关闭 SSH 会话”按钮：`life === 'ready' && closeState === 'idle' && !ended && !logoutPending` 时可打开确认框；`pending/requested/unknown/failed` 时按钮仍可见但改为“查看关闭状态/重试”或禁用，具体文案由 UI 设计确定。
- 确认框：确认后调用 `closeHandle.requestClose()`；错误显示在确认框内。
- pending 时显示“取消等待”（调用 `cancelWaiting()`）；requested/unknown/failed 时显示“重试关闭”（`retryClose()`）和“刷新状态”（`refreshClose()`）。
- 状态文案：`pending`“正在请求关闭”、`requested`“已请求关闭，正在回收”、`unknown`“关闭结果未知”、`failed`“关闭请求失败”、`ended`“会话已结束”。
- `close()`（关闭标签）只退订并释放 xterm/client；不调用 `cancelWaiting`，不 abort 协调器任务。
- Pane 的 takeover/recovery 准入读取 `closeHandle.snapshot().state === 'idle'`；不得复制一份本地 `closeRequested`。
- 已销毁 Pane 的迟到 `subscribe` 回调不更新 UI；订阅在 `onBeforeUnmount` 中解除。

### 5.6 备选方案

| 方案 | 内容 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| A（推荐） | HomePage 协调器 + Pane 只读句柄 | 单一所有权；Pane 销毁不丢状态；两路径共用 | 新增 `session/close.ts`；HomePage 注入句柄 | 推荐 |
| B | Pane 持有 + 关闭时移交 HomePage | 不改 HomePage 所有权 | 交接窗口复杂；双所有者风险 | 不推荐 |
| C | SessionManager 持有无 Pane 关闭任务 | 列表入口直接 | 与“SessionManager 只 emit”冲突；Pane 与列表两套 | 不推荐 |
| D | 模块级单例 | 跨 Pane 简单 | 全局状态、跨页面/登录泄漏、测试隔离差 | 不推荐 |
| E | localStorage/后端持久化 unknown | 刷新后仍阻止接管 | 新增存储/API/数据库；超出授权 | 备选，需单独授权 |

### 5.7 需要用户确认的具体事项

1. 是否采用方案 A（HomePage 协调器 + Pane 只读句柄）。
2. 是否授权 `frontend/src/session/close.ts` 作为 S7-1 正式文件。
3. 是否确认 Pane 与无 Pane 共用同一状态机。
4. 是否确认 Pane 销毁只退订、不 abort、不清状态。
5. 是否确认“非 idle 时创建 Pane 但不自动 prepare”的交互；或选择“不创建 Pane，HomePage 直接提示”。
6. 是否确认 unknown 期间只允许查询、幂等重试和关闭本地标签。
7. 是否确认 `ended` tombstone 保留到页面 dispose。
8. 是否接受页面刷新后不保留本地 unknown、依赖服务端详情确认。
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

1. `signOut()` 在同步设置 `logoutPending=true` 后，立即调用 `coordinator.dispose()`；协调器标记 disposed、abort 所有本地 DELETE 等待、清空订阅，之后拒绝任何新的 `requestClose/retryClose/refreshClose`。
2. `dispose()` 只停止本地等待，不发送新请求，不声称服务端撤销；已在服务端的 DELETE 仍可能继续执行。
3. 协调器 dispose 后，Pane 的关闭 UI 不再更新；Pane 随后由 `stop()`/`close()` 处理，属于登出流程，不是关闭任务继续。
4. 登出期间不得产生新的列表 GET、详情 GET、DELETE、WebSocket 或 takeover：`logoutPending` + `identity=null` + `manager.invalidate()` + `coordinator.dispose()` + Pane `live()` 共同保证。
5. logout 失败时页面停留，协调器已 dispose，关闭任务不能继续；这是“退出未完成”的明确代价。Pane 已 stop，用户可重试 logout；关闭任务不再自动恢复。
6. logout 成功时按 S6 §6.5 close Pane 并跳转；协调器随页面卸载释放。

### 6.4 备选方案 B：`logoutPending` 保持到 close 完成

- 需要修改 `auth.ts`，把“HTTP 登出完成”和“本地清理完成”分开。
- 风险：`acceptIdentity()` 在 `logoutPending` 为真时直接返回，登录页可能无法登录；路由守卫会持续把 `/login` 重定向；需要同时修改 auth/router。
- 结论：不推荐；如用户坚持，需要单独授权修改 `auth.ts`、`router/index.ts` 和 `auth.test.mjs`。

### 6.5 需要用户确认的具体事项

1. 是否确认推荐方案 A（`auth.ts` 不改，`logoutPending` 不覆盖本地 close）。
2. 是否确认 `signOut` 同步 `coordinator.dispose()`，登出期间不再有新的关闭/查询请求。
3. logout 成功后 close 失败是否仍然跳转（推荐：是，U9 已裁决）。
4. logout 失败时是否保持“页面停留、Pane 已停止、可重试”，并接受协调器已 dispose、关闭任务不继续。

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
5. `openSession(item)` 先检查 `coordinator.getCloseState(item.id)`：非 idle 时按 §5.4 处理，不自动 prepare；列表快照不能覆盖协调器状态。
6. HomePage 订阅协调器的状态变化；当状态变为 requested/unknown/failed/ended 时，按同一 microtask 合并刷新列表；不在 Pane 内直接刷新列表。
7. `controller.invalidate()` 与 `dispose()` 的现有实现保留；旧请求的 `finally` 继续由 sequence 保护。

### 7.4 备选方案 B：只依赖 `manager.invalidate()`，不改 `isAllowed`

- 优点：改动更小。
- 缺点：`invalidate()` 只取消当前请求并递增 sequence；如果 `paneChanged` 在 `invalidate()` 之后、props 更新之前调用 `refresh()`，`isAllowed()` 仍可能返回旧的真值并发起新 GET。因此不推荐单独使用。

### 7.5 需要用户确认的具体事项

1. 是否接受在 `SessionManager` 内直接读取 `logoutPending`（而不是新增 prop 或事件）。
2. 是否接受 `queueMicrotask` 合并（不新增定时器）。
3. 是否确认列表只作为快照，打开前必须检查协调器状态，接管前必须详情 GET。
4. 隐藏列表时是否只标记待刷新、下次进入再 GET（现有行为，推荐保持）。

## 8. D5：其他问题

### 8.1 stop 失败与 DELETE 失败分别反馈

- 现状：`closeRemote()` 的 catch 把 DELETE 失败写入 `notice`；`finally` 中 stop 失败也写入 `notice`，后者会覆盖前者。
- 推荐：DELETE 结果由协调器状态/消息展示在关闭确认框或关闭状态区；stop 失败使用独立的 `closeNotice`/`connectionNotice` 文案（例如“已请求关闭，但本地终端未确认停止”）；不要用一条消息覆盖另一条。

### 8.2 DELETE 成功但本地 stop 失败

- 推荐：协调器保持 `requested`，Pane 显示“已请求关闭 SSH 会话；本地终端未确认停止（原因）”；允许刷新状态/关闭标签；不得显示“已完全关闭”。

### 8.3 router guard 与主动 signOut 同时发生

- 现状：`expireIdentity()` 在 `logoutPending` 为真时返回 false；guard 在 `logoutPending` 为真时重定向 `/login`；`signOut` 的 `router.replace('/login')` 与 guard 一致。
- 推荐：保持现状；S7 只补 `router.replace` 的 rejection 处理和跳转后的反馈写入。

### 8.4 `router.replace()` reject

- 现状：`await router.replace('/login')` 若 reject，`finally` 会复位 `signingOut`，但清理反馈不会写入，且 rejection 可能成为未处理异常。
- 推荐：`try { await router.replace('/login') } catch (error) { console.error(...) }`，无论跳转是否成功都写入清理反馈；不改变登出成功语义。

### 8.5 stop/close 同步抛错

- 现状：`entries.map((entry) => entry.handle.stop())` 在 `try` 之外求值；同步抛错会跳过 `logout()`，`signingOut` 永久为真。
- 推荐：把每个调用包装为 `Promise.resolve().then(() => entry.handle.stop())`，或用 `try/catch` 收集同步错误；`close()` 同理。保证 `allSettled` 始终拿到 Promise。

### 8.6 浏览器关闭

- 冻结：不依赖 `beforeunload` 发送 DELETE；服务端按断线/心跳/Supervisor 回收 Binding，SSH 离线保留。
- 推荐：保持；S7 不新增 `beforeunload`/`sendBeacon`。

### 8.7 页面卸载后的迟到回调与重复 abort

- 列表：已有 sequence/dispose/Abort；保持。
- 协调器：`dispose()` 是唯一 abort 所有任务的入口；Pane `close()` 只退订，不 abort。这样不会出现 Pane 与 HomePage 同时 abort 同一 controller。
- Pane：`life`、`taskId`、订阅 token 和 `live()` 隔离迟到回调；已销毁 Pane 不更新 UI。
- `dispose()` 幂等；重复调用不重复 abort/释放。

### 8.8 U6 认证网络错误

- 现状：`router/index.ts:29-38` 在 `/me` 网络错误时也 `clearIdentity()`，并显示“无法验证登录状态”。
- 结论：S6 已裁决不修改；S7 不扩大范围。作为独立风险保留。
## 9. 14 项竞争场景逐项分析（按统一协调器修订）

以下每项均按“冻结预期 / 当前实现 / 修订后目标行为 / 测试 / 结论”核对。结论中的“通过”仅指当前代码与冻结设计一致；“需裁决”表示设计或所有权未明确；“阻塞”表示不解决不能安全开始 S7。

### 场景 1：点击关闭 SSH 后立即再次点击接管

- 冻结预期：实施计划 §11.3（S6 修订）规定关闭意图优先；关闭按钮不因 takeover 进行中而禁用；确认关闭后 `stop()` 使 takeover 失效。S6 §10.1 规定关闭意图行拒绝 takeover。
- 当前实现：`closeRemote()` 在任何 await 之前设置 Pane 内 `closeRequested=true` 并调用 `stop()`；`takeover.invalidate()` 递增 sequence，`allowed()` 立即为假。
- 修订后目标：`requestClose()` 同步在协调器把状态置为 `pending`；`Pane` 读取 `closeState !== 'idle'`，takeover/recovery 准入为假；`stop()` 仍由 Pane 发起。
- 测试：S6-T24 覆盖确认后零 takeover POST；新增“协调器 pending 时 takeover 拒绝”用例。
- 结论：正常路径通过；实现时需把 `closeRequested` 迁入协调器。

### 场景 2：用户正在接管时发起关闭 SSH Session

- 冻结预期：S6 §10.1、R14：关闭意图优先；关闭确认框打开期间 `canBegin()` 为假，prepare 拒绝；确认关闭后 takeover 失效。
- 当前实现：`closing` 是独立状态；`canBegin()` 读取 `!closing`；确认关闭调用 `closeRemote()` → `stop()`。
- 修订后目标：确认关闭调用 `closeHandle.requestClose()`，协调器置 `pending`；Pane 同时 `stop()` 使 takeover 失效；`closing` 仍只控制确认框。
- 测试：S6-T24、S6-T38 的 `closeRemote × stopping/loading/connecting` 组合。
- 结论：通过。

### 场景 3：DELETE 请求已发出，用户取消等待

- 冻结预期：实施计划 §15 S7 行要求“DELETE 可取消等待”，但没有定义取消的含义、状态和提示。
- 当前实现：没有取消按钮；`closeSession` 不接受 `AbortSignal`；请求只能等待网络结束。
- 修订后目标：`cancelWaiting(sessionId)` 由协调器 abort 本地等待，状态 `pending → unknown`；不发送新请求；Pane 只显示“已停止等待，关闭结果未知”。
- 测试：新增“pending 取消等待”用例；断言 DELETE 次数仍为 1，状态 unknown，接管禁用。
- 结论：需用户确认取消语义；确认后可由 S7-1 实现，不再是设计阻塞。

### 场景 4：DELETE 已在服务端成功，但浏览器未收到响应

- 冻结预期：第四阶段 §17、F10：失败/响应丢失保留停止状态和明确结果，允许查询或幂等重试，不能自动恢复未知结果；显式查询证实仍可绑定后用户可重新开始人工动作。
- 当前实现：网络错误进入 catch，Pane 内 `closeRequested=false`，显示通用错误；接管准入重新开放。
- 修订后目标：协调器进入 `unknown`，保持非 idle；Pane 销毁不丢状态；新 Pane 打开时读取 unknown，不自动 prepare；只能刷新/重试/关闭标签。
- 测试：新增“响应丢失 + Pane 销毁 + 重开”用例；断言零 takeover POST/WS。
- 结论：需用户确认 unknown 的准入；确认后不阻塞。

### 场景 5：DELETE 失败或网络超时，服务端实际结果不确定

- 冻结预期：同场景 4；D1=A 规定 GET 只支持主动/生命周期取消，不引入新的本地定时期限。
- 当前实现：没有本地超时；网络错误与 HTTP 业务错误都走同一 catch；Pane 内 `closeRequested=false`。
- 修订后目标：协调器区分 `unknown`（abort/网络）与 `failed`（明确业务错误）；两者都保持非 idle，允许幂等重试/刷新。
- 测试：新增网络错误、业务错误、超时/abort 分类用例。
- 结论：需用户确认是否新增本地超时；不新增时按 unknown 处理。

### 场景 6：关闭 Session 时，原 Pane 正处于 recovery、retry_wait、takeover、confirming 或 connecting

- 冻结预期：S6 §10.1：关闭通过 `stop()` 使 takeover 失效；`stop()` 使 recovery 失效并停止 Client；`confirming` 时 Modal 独占界面。
- 当前实现：`stop()` 先 `takeover.invalidate()`，再 `recovery.stop()`（内部 `invalidate()` + `client.stop()`）；`closeRemote` 并行发 DELETE。
- 修订后目标：Pane `stop()` 不变；`closeHandle.requestClose()` 只负责协调器状态/DELETE；两者在同一同步边界发起，互不等待。
- 测试：S6-T38 覆盖 takeover 四阶段；新增 recovery `retry_wait` × 关闭用例。
- 结论：通过。

### 场景 7：用户退出 Session 管理视图，但仍希望保留本地 SSH Pane

- 冻结预期：实施计划 §11.1、§12.1：切换视图保留 Pane；SessionManager 卸载只取消列表 GET；不把关闭列表当关闭 SSH。
- 当前实现：`SessionManager` 用 `v-if` 卸载；`TerminalPane` 用 `v-show` 保持挂载；`records` 不因视图切换变化。
- 修订后目标：协调器归 HomePage，SessionManager 卸载不影响关闭任务；Pane 保留。
- 测试：S6-T33。
- 结论：通过。

### 场景 8：用户关闭本地 Pane，但不希望关闭远端 SSH Session

- 冻结预期：核心设计 §10.4、第四阶段 §17：关闭标签只 dispose 本地资源，不执行 DELETE。
- 当前实现：`closeLocal()` → `close()` → `takeover.invalidate()`、`recovery.invalidate()`、`client.dispose()`；没有 DELETE。
- 修订后目标：`close()` 只退订协调器并 dispose；**不 abort 协调器任务**；关闭标签不发送 DELETE。
- 测试：新增“关闭标签零 DELETE”断言；新增“关闭标签后协调器任务仍在”用例。
- 结论：通过（实现需调整 `close()` 不再 abort）。

### 场景 9：用户退出登录时，多个 Pane 分别处于正常连接、恢复中、接管中、初始化失败或清理失败

- 冻结预期：S6 §6.5：同步 `logoutPending` → 同步 stop 全部 → 立即 logout → 等 stop → 成功后 close 全部 → 跳转；stop/close 失败分别统计。U9：登出成功优先。
- 当前实现：`signOut()` 按此顺序；`registeredPanes()` 包含 ready/init_failed 的句柄；dispose_failed 的 `close()` 返回同一失败；`allSettled` 收集。
- 修订后目标：在 `logoutPending` 后同步 `coordinator.dispose()`；协调器不再发新请求；stop/close 顺序不变。
- 测试：S6-T35/T50/T39；新增 init_failed/dispose_failed 参与 signOut 的用例。
- 结论：通过（需补协调器 dispose 与测试）。

### 场景 10：logout 请求失败，但本地 Pane 已经 stop

- 冻结预期：第四阶段 §13.2：保持身份与退出意图，停留本页，显示“退出未完成，请重试”，不 close，不自动恢复。
- 当前实现：catch 中等待 stop 屏障，写 notice，返回；`logoutPending` 仍为真；Pane 已 stop。
- 修订后目标：协调器已 dispose；关闭任务不继续；页面停留，用户重试 logout。
- 测试：S6-T50；新增“logout 失败 + 协调器 pending”用例，断言零新请求。
- 结论：通过（需明确协调器 dispose 的代价）。

### 场景 11：logout 成功，但部分 Pane close 失败

- 冻结预期：U9、R33：仍跳转登录页；记录失败；提示“本地资源未释放”；不阻止退出。
- 当前实现：`Promise.allSettled` 收集 close；`router.replace('/login')`；跳转后写 `authNotice`。
- 修订后目标：不变；协调器随页面 dispose。
- 测试：S6-T50。
- 结论：通过。

### 场景 12：stop、logout、close 的完成顺序不同，或其中任一步骤抛错

- 冻结预期：logout 与 stop 并行；close 只在 logout 成功且 stop 屏障完成后；logout 失败不 close；任一步骤失败不阻止后续合法步骤（登出成功优先）。
- 当前实现：顺序一致；`stopTask` 在 logout 之前启动；`await stopTask` 在 logout 成功/失败分支中都会执行；close 在 stop 之后。
- 修订后目标：`coordinator.dispose()` 在 stop 之前同步执行；stop/close 的同步 throw 包装为 Promise；`router.replace` reject 被捕获。
- 测试：S6-T50；新增同步 throw、router reject、dispose 幂等用例。
- 结论：顺序通过；同步 throw 是 MAJOR 实施缺陷（S7-M4）。

### 场景 13：路由守卫、认证失效和主动 signOut 同时发生

- 冻结预期：第四阶段 §13.2：主动退出未完成时 AUTH_REQUIRED 不得覆盖“退出未完成”；`expireIdentity()` 在 `logoutPending` 时返回 false。
- 当前实现：`expireIdentity()` 检查 `logoutPending`；guard 在 `logoutPending` 时重定向 `/login`；`expired()` 在 `expireIdentity()` 返回 false 时不做跳转。
- 修订后目标：不变；协调器 disposed 后不再发新请求。
- 测试：`auth.test.mjs`；新增真实 router 顺序的浏览器用例（S7-6）。
- 结论：通过（真实 router 顺序为 OBSERVATION）。

### 场景 14：旧列表请求、旧 DELETE 响应或旧 Pane 回调晚于页面生命周期返回

- 冻结预期：D1=A：列表 sequence/Abort/disposed；旧 finally 不清新 loading；迟到响应不能发布。Pane 的旧回调按 `life`/taskId 隔离；页面卸载后不联网。
- 当前实现：列表满足；Pane 的 GET/takeover 有 sequence 和 `live()`；`closeRemote` 的 DELETE 没有 abort/epoch，`finally` 仍会写 `remoteBusy`、`revision` 并 emit。
- 修订后目标：DELETE 归协调器，每个任务有 epoch；旧响应只更新匹配的 task；Pane 订阅 token 过滤旧 Pane；页面 dispose 后 disposed 检查丢弃响应。
- 测试：新增“旧 Pane 销毁后响应迟到”“旧响应不覆盖新任务”“dispose 后响应丢弃”用例。
- 结论：需按 S7-1 实现 epoch/dispose；不再是设计阻塞。

## 10. DELETE、Pane 销毁、页面重进、signOut 的时序分析

### 10.1 远端关闭（协调器版，推荐）

```text
用户点击“关闭 SSH 会话”
  -> Pane 打开确认框（不停止任何任务）
用户点击“确认关闭”
  -> Pane：同步 stop()（takeover.invalidate + recovery.invalidate + client.stop）
  -> Pane：closeHandle.requestClose()
  -> 协调器：state = pending；创建 epoch；创建 AbortController；发送 DELETE
  -> 协调器：200 -> requested；abort/网络错误 -> unknown；明确业务错误 -> failed
  -> Pane：订阅状态，显示“正在请求/已请求/未知/失败”
  -> 用户可 cancelWaiting / retryClose / refreshClose
  -> refreshClose：GET 详情；CLOSING 保持；SESSION_NOT_FOUND -> ended
  -> 关闭标签：Pane 退订 + dispose；协调器任务继续
  -> HomePage dispose/登出：abort 本地等待；不声称撤销服务端关闭
```

关键不变量：

- DELETE 200 不等于 CLOSED，也不释放 Session ID 预占；预占只在 Pane `closed` 后释放。
- 关闭标签只释放本地 Pane，不 abort 协调器任务，不发送 DELETE。
- 协调器是唯一 DELETE 所有者；Pane/SessionManager 不直接调用 `closeSession`。
- 关闭意图与 takeover/recovery 状态分离；协调器只提供 `state === 'idle'` 的准入。

### 10.2 Pane 销毁与重新创建

```text
Pane 发起 requestClose（pending/unknown）
  -> 用户关闭 Pane 标签
  -> Pane.close()：解除订阅；dispose client/xterm；不 abort 协调器
  -> 协调器继续等待 DELETE / 保持 unknown
  -> 用户从 SessionManager 再次打开同一 Session
  -> HomePage 检查 coordinator.getCloseState(id)
  -> 非 idle：创建/定位 Pane，但不自动 prepare；takeover/手动恢复禁用
  -> Pane 显示关闭状态 + 重试/刷新
  -> 旧 DELETE 响应到达：协调器按 epoch 更新状态；旧 Pane 无订阅，不更新 UI
```

### 10.3 页面卸载与重新进入

```text
HomePage 卸载/刷新
  -> coordinator.dispose()：abort 所有本地等待；disposed=true；清空订阅
  -> 迟到响应被丢弃；不发送新请求
  -> 新页面：新协调器为空
  -> 列表可能仍是旧快照；不能据此接管
  -> 用户打开 Session：创建候选 Pane
  -> Pane 详情 GET（S6 流程）：
       CLOSING -> 禁止接管
       SESSION_NOT_FOUND -> ended
       CONNECTED/DISCONNECTED -> 仍需用户确认；提交时服务端复查
       网络错误 -> 不生成候选
  -> 不自动 attach/takeover；不凭本地无记录恢复
```

设计限制：本地 unknown 不跨页面；刷新后只能依赖服务端详情/提交结果。若需跨页面阻止接管，必须新增持久化或后端关闭意图记录（未授权）。

### 10.4 登出

```text
用户点击“退出登录”
  -> 同步：signingOut=true；logoutPending=true
  -> 同步：operations.abort()；query.abort()；clearTimeout(debounce)；probe=null
  -> 同步：manager.invalidate()；coordinator.dispose()
  -> 同步：对已登记 Pane 发起 stop()（takeover.invalidate + recovery/client stop）
  -> 立即：logout()（auth.ts 设置 logoutPending=true，identityEpoch++，单请求）
  -> logout 失败：await stop；写“退出未完成，请重试” + stop 失败数；return
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

### 10.5 失败组合

| 组合 | 预期行为 | 目标实现 |
|---|---|---|
| stop 失败 + logout 成功 | 跳转；提示“未确认停止”；记录日志 | 保留 + 同步 throw 防护 |
| stop 失败 + logout 失败 | 停留；提示“退出未完成 + 未确认停止”；可重试 | 保留 + 协调器 dispose |
| close 失败 + logout 成功 | 跳转；提示“本地资源未释放”；记录日志 | 保留 |
| close 失败 + logout 失败 | 不 close、不跳转 | 保留 |
| DELETE 失败 + 关闭标签 | 协调器保持 failed/unknown；Pane 销毁不丢状态 | S7-1 |
| DELETE 成功 + stop 失败 | 协调器 requested；提示本地未停止 | 分离提示 |
| Pane 销毁 + 协调器 pending | 协调器继续；新 Pane 识别非 idle | S7-1 |
| 页面刷新 + 旧 DELETE 迟到 | 旧页面 disposed 丢弃；新页面靠详情确认 | S7-1/S7-5 |
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

### 11.3 新增生命周期与所有权测试场景（本轮补充）

层级说明：U=协调器单元；P=Pane 组件；H=HomePage/SessionManager；B=浏览器/真实组件。网络请求数量指该场景中新增的 fetch/WS 次数，不含场景开始前已完成的列表查询。

| 编号 | 层级 | 模拟条件 | 预期状态 | 预期网络请求 | 所有权边界 |
|---|---|---|---|---|---|
| S7-C01 | U/P | Pane 发起 DELETE 后处于 pending，关闭 Pane | 协调器仍 pending；Pane 已退订；无 closed/ended | DELETE 1；关闭后新增 0 | 协调器持有任务；Pane 只退订 |
| S7-C02 | U/P | pending 时 abort，再关闭 Pane | 协调器 unknown；Pane 已销毁；状态保留 | DELETE 1；abort 0 新请求 | 只有协调器 abort |
| S7-C03 | H/P | 同一 Session 新建 Pane，协调器 unknown | 新 Pane 显示 unknown；不进入候选确认 | 新增 POST 0；WS 0；详情 GET 0（不自动 prepare） | HomePage 路由；Pane 只读 |
| S7-C04 | P | 新 Pane 在 unknown 下挂载 | 不自动调用 `recover()`/`prepare()`；手动恢复禁用 | 0 | Pane 读取协调器准入 |
| S7-C05 | U | unknown 下 retryClose | 状态 pending → requested/unknown/failed；只一个有效请求 | DELETE 1 | 协调器去重 |
| S7-C06 | U/P | 旧 Pane 销毁后 DELETE 响应迟到 | 协调器更新；旧 Pane 无 UI 更新 | 0 新请求 | epoch + 订阅解除 |
| S7-C07 | U | 旧响应迟到，同 Session 已有新任务 | 旧响应被 epoch 丢弃；新任务状态不变 | 0 新请求 | 每任务 epoch |
| S7-C08 | H | SessionManager 行级关闭与 Pane 关闭同时触发 | 协调器只创建一个任务；一个确认/一个 DELETE | DELETE 1 | HomePage 路由 + 协调器去重 |
| S7-C09 | H | 无 Pane 行级关闭与新 Pane 创建竞争 | 协调器先占用；新 Pane 识别非 idle，不 prepare | DELETE 1；POST 0；WS 0 | 单一协调器 |
| S7-C10 | U | 两个不同 Session 并行关闭 | 两个独立 CloseTask；状态互不影响 | DELETE 2（各 1） | 按 Session ID 隔离 |
| S7-C11 | H | HomePage 卸载时仍有在途 DELETE | dispose abort 本地等待；disposed 后无新请求；迟到响应丢弃 | DELETE 1；卸载后新增 0 | HomePage 唯一 dispose |
| S7-C12 | B/H | 页面重新进入，服务端详情返回 CLOSING | 候选 Pane 不进入确认；禁止 takeover | 详情 GET 1；POST 0；WS 0 | 服务端权威 |
| S7-C13 | B/H | 页面重新进入，详情 GET 网络错误 | 无候选；不自动恢复；提示错误 | 详情 GET 1；POST 0；WS 0 | 不凭本地无记录恢复 |
| S7-C14 | H | 登出与 DELETE pending 并发 | 协调器 dispose；无新列表 GET/详情 GET/DELETE/WS；stop/logout/close 顺序正确 | 新增 0（除 logout 本身） | logoutPending + dispose |
| S7-C15 | H/P | dispose、signOut、Pane close 交错 | 只 abort 一次；只 dispose 一次；无迟到 UI；无重复释放 | 0 新请求 | 幂等 dispose/退订 |

补充断言：

- S7-C01/C02/C11 必须检查 `closed` 事件次数为 0（Pane 关闭标签不是 Session closed）。
- S7-C03/C04/C12/C13 必须检查零 `POST /takeover`、零 WebSocket。
- S7-C05/C08/C09/C10 必须检查 DELETE 请求体/路径按 Session ID 隔离，且没有第二个 controller。
- S7-C14 必须检查 `manager.refresh()` 次数为 0、`closeSession` 次数不增加、WebSocket 构造次数为 0。
- S7-C15 必须检查协调器 `dispose()` 幂等、Pane `close()` 不 abort。

## 12. S7 任务计划草案（按统一协调器修订）

编号仅用于本提案，不代表已冻结的实施顺序；任务依赖以本节表格为准。

### S7-0：设计裁决与计划冻结

| 项目 | 内容 |
|---|---|
| 任务目标 | 把 D1–D5 的用户裁决写入计划，消除“待裁决”项；不写代码 |
| 前置依赖 | 用户回答第 18 节问题 |
| 修改文件 | 仅文档（修改 `doc/stage-5-implementation-plan.md` 或新增 S7 子计划，需单独授权） |
| 不修改范围 | 所有生产代码、测试、配置、依赖、既有冻结文档（除非用户明确授权修订） |
| 核心实现 | 记录协调器所有权、状态机、生命周期、D2 行级关闭、D3 登出、D4 列表、D5 反馈 |
| 测试要求 | 无 |
| 验收标准 | 裁决条目逐项有明确结论；任务边界、文件范围、验收条件可执行 |
| 回滚边界 | 只撤销本次文档增量；不影响代码 |

### S7-1：统一关闭协调器与 DELETE 状态机（完整单元）

| 项目 | 内容 |
|---|---|
| 任务目标 | 实现 HomePage 持有的 `CloseCoordinator`：按 Session ID 管理 pending/requested/unknown/failed/ended、AbortController、DELETE、epoch、重试、去重、订阅、dispose；Pane 只保留关闭 UI；Pane 销毁不丢状态 |
| 前置依赖 | S7-0 的 D1/D2 裁决 |
| 修改文件 | 新增 `frontend/src/session/close.ts`；修改 `frontend/src/api/sessions.ts`、`frontend/src/components/terminal/TerminalPane.vue`、`frontend/src/pages/HomePage.vue`、`frontend/tests/terminal-pane.test.mjs`、`frontend/tests/session-list.test.mjs`（HomePage 部分）；必要时新增 `frontend/tests/session-close.test.mjs` |
| 不修改范围 | `client.ts`、`output.ts`、`recovery.ts`、`terminal/takeover.ts`（除非仅做准入读取适配）、`auth.ts`、`router`、后端、`Modal.vue` |
| 核心实现 | `CloseCoordinator`/`CloseTask`/`CloseSnapshot`/`CloseHandle`；`closeSession(id, signal?)`；Pane 通过句柄请求/重试/取消/刷新；HomePage 创建/dispose/路由；非 idle 时禁止 takeover/手动恢复；Pane `close()` 只退订；新 Pane 识别遗留状态；单 Session 单 DELETE；epoch/dispose 迟到隔离；`ended` tombstone |
| 测试要求 | S7-C01–C11、S7-C15；DELETE pending/200/失败/网络错误/abort；unknown 禁止 takeover；重试幂等；旧 Pane/旧响应；关闭标签零 DELETE；关闭成功不 emit closed/不释放预占；关闭确认框错误可见 |
| 验收标准 | 协调器是唯一 DELETE 所有者；Pane 销毁后状态仍在；新 Pane 非 idle 时零 takeover POST/WS；同一 Session 至多一个有效 DELETE；dispose 幂等；`npm test` 相关文件通过 |
| 回滚边界 | 撤销 `session/close.ts`、`closeSession` 可选参数、Pane/HomePage 接线增量；恢复 S6 Pane 内 `closeRemote`；不影响候选/takeover |

### S7-2：SessionManager 行级关闭入口（只加意图）

| 项目 | 内容 |
|---|---|
| 任务目标 | 为列表每行提供“关闭”入口，满足核心设计 §9 的 CONNECTING/CLOSING 操作；只 emit 意图，复用 S7-1 协调器 |
| 前置依赖 | S7-0 的 D2 裁决；S7-1 完成 |
| 修改文件 | `frontend/src/components/session/SessionManager.vue`、`frontend/src/pages/HomePage.vue`、`frontend/tests/session-list.test.mjs` |
| 不修改范围 | `TerminalPane.vue` 的候选/takeover 逻辑、`session/close.ts` 的状态机、`client.ts`、`recovery.ts`、auth/router、后端 |
| 核心实现 | SessionManager 只 `emit('close', item)`；HomePage 有 handle → 定位 Pane + `requestClose()`；无记录 → 页面级确认 + `coordinator.requestClose(id)`；有记录无 handle → 提示稍后；CONNECTING/CLOSING 允许；结果刷新列表 |
| 测试要求 | 行按钮 emit 当前 item；有 Pane 时零 HomePage DELETE；无 Pane 时恰好一次 DELETE；确认取消零 DELETE；重复点击幂等；CONNECTING/CLOSING；多 Session 不串扰；S7-C08/C09 |
| 验收标准 | SessionManager 不直接 DELETE；同一 Session 任意时刻只有一个 DELETE 所有者；关闭列表不误删 SSH；关闭 Pane 不发 DELETE |
| 回滚边界 | 移除操作列“关闭”按钮与 HomePage 路由增量；保留 S7-1 协调器；不改变“打开”行为 |

### S7-3：列表同步失效与 changed 刷新合并

| 项目 | 内容 |
|---|---|
| 任务目标 | 登出/身份失效/卸载时同步撤销列表发布身份；同一同步批次多个 changed 合并为一次 GET；打开前检查协调器状态 |
| 前置依赖 | S7-0 的 D4 裁决；S7-1（读取协调器状态） |
| 修改文件 | `frontend/src/pages/HomePage.vue`、`frontend/src/components/session/SessionManager.vue`、`frontend/tests/session-list.test.mjs`；`session/list.ts` 仅在发现真实缺陷时修改 |
| 不修改范围 | `SessionListController` 的既有 sequence/Abort 语义、auth/router、Pane 生命周期 |
| 核心实现 | `signOut()` 同步 `manager.invalidate()`；`paneChanged()` 守卫 + `queueMicrotask` 合并；`isAllowed` 增加 `!logoutPending` 同步检查；`openSession` 检查 `getCloseState`；HomePage 订阅协调器状态变化并合并刷新 |
| 测试要求 | logoutPending 设置后同步零新 GET；paneChanged 在登出中零 GET；同批多 changed 一次 GET；旧 finally 不清新 loading；隐藏列表只标记待刷新；非 idle 打开不 prepare |
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
| 任务目标 | 页面卸载、关闭标签、登出、迟到 GET/DELETE/WS 回调不修改已失效 Pane，不启动新任务；不重复 abort/释放 |
| 前置依赖 | S7-1、S7-4 |
| 修改文件 | `frontend/src/components/terminal/TerminalPane.vue`、`frontend/src/pages/HomePage.vue`、对应测试 |
| 不修改范围 | `client.ts`/`output.ts`/`recovery.ts` 的 S5 语义；auth/router；`session/close.ts` 的状态机 |
| 核心实现 | Pane 订阅解除；协调器 dispose/epoch；HomePage `pageDisposed` 守卫；列表 dispose 保持；页面重进后的详情确认（不自动恢复） |
| 测试要求 | DELETE 在途时关闭标签/卸载；迟到 200/错误；takeover loading 时卸载；recovery retry_wait 时卸载；S7-C11/C12/C13/C15；零新 fetch/WS |
| 验收标准 | 卸载后零新请求；不因迟到回调 emit closed/changed；Session ID 预占只在 closed 后释放；刷新后不凭本地无记录恢复 |
| 回滚边界 | 撤销订阅/epoch/卸载守卫；保留 S7-1 协调器 |

### S7-6：自动化测试、浏览器验收与报告

| 项目 | 内容 |
|---|---|
| 任务目标 | 补齐 F10–F13、B01–B05 的 S7 子集；执行全量验证；输出报告与截图 |
| 前置依赖 | S7-1 至 S7-5 完成 |
| 修改文件 | 测试文件、`doc/测试报告/stage-5-s7-*-report.md`、日志/截图目录（需授权新增） |
| 不修改范围 | 生产代码（除非发现并报告缺陷后获准定点修复）；冻结文档 |
| 核心实现 | 单元/组件/浏览器分层；Mock 与真实环境分开记录；覆盖 Pane 销毁、重开、页面刷新、登出并发 |
| 测试要求 | `npm test`、`npm run typecheck`、`npm run build`、`git diff --check`；浏览器至少覆盖行级关闭、DELETE 失败/取消、登出失败/成功、列表退出、Pane 销毁后重开 |
| 验收标准 | 测试数量与结果可复现；截图对应场景；未验证项明确；无伪造真实环境结论 |
| 回滚边界 | 仅撤销测试/报告增量；不影响生产代码 |
## 13. 文件修改范围汇总

| 文件 | S7 允许用途 | 明确不修改 |
|---|---|---|
| `frontend/src/session/close.ts` | **S7-1 新增**：`CloseCoordinator`/`CloseTask`/`CloseSnapshot`/`CloseHandle`；按 Session ID 管理状态、controller、DELETE、epoch、订阅、dispose | 不建全局 Store/事件总线/定时轮询；不管理 takeover/recovery；不渲染 UI |
| `frontend/src/pages/HomePage.vue` | 创建/持有/dispose 协调器；按 Session ID 路由；打开前检查状态；订阅状态变化刷新列表；同步 invalidate；signOut 异常处理 | 不在已有 Pane 时另行 DELETE；不复制状态机；不参与 takeover 流程 |
| `frontend/src/components/session/SessionManager.vue` | 操作列“关闭”、`close` 事件、`isAllowed` 同步检查 | 不直接 DELETE；不持有关闭状态；不按状态隐藏“打开” |
| `frontend/src/components/terminal/TerminalPane.vue` | 关闭确认框、取消等待/重试/刷新按钮、状态文案、错误展示；通过 `CloseHandle` 调用协调器；`close()` 只退订 | 不直接 `closeSession`；不持有 DELETE Promise/controller；不跨 Pane 保留状态；不改候选/takeover 三检查点；不改 `connectTakeover` 调用 |
| `frontend/src/api/sessions.ts` | `closeSession(id, signal?)` | 不改 List/详情 DTO 校验；不改错误码 |
| `frontend/src/session/list.ts` | 仅在发现真实缺陷时最小修改 | 不重写 sequence/Abort；不引入定时器 |
| `frontend/src/terminal/takeover.ts` | 原则上不修改；仅做 `allowed` 读取关闭状态的适配 | 不改阶段、检查点、`invalidate()` 调用约束 |
| `frontend/tests/terminal-pane.test.mjs` | Pane 关闭 UI、退订、迟到响应、关闭标签零 DELETE | 不删除/弱化 S6 用例 |
| `frontend/tests/session-list.test.mjs` | HomePage 协调器接线、行级关闭、signOut、列表失效、changed 合并 | 不删除/弱化 S6 用例 |
| `frontend/tests/session-close.test.mjs` | 新增协调器单元测试（若采用独立文件） | 不测试真实网络；用受控 fetch/Abort |
| `frontend/tests/auth.test.mjs` | 仅在 D3 需要时扩展 logout 生命周期用例 | 不改认证协议 |
| `frontend/src/styles/base.css` | 仅当行级关闭按钮需要最小样式时 | 不做无关视觉重构 |
| `frontend/src/stores/auth.ts`、`router/index.ts` | 仅 D3 方案 B 且单独授权时 | 默认不改 |
| 后端、`client.ts`、`output.ts`、`recovery.ts`、`Modal.vue`、冻结文档 | 不允许修改 | — |

## 14. 任务依赖关系

```text
S7-0（裁决）
  ├─> S7-1（统一协调器 + DELETE 状态机 + Pane/HomePage 接线）
  │     ├─> S7-2（行级关闭入口）
  │     ├─> S7-3（列表失效/合并/打开前检查）
  │     ├─> S7-4（完整登出）
  │     └─> S7-5（页面退出/迟到回调/刷新后确认）
  └─> S7-2/S7-3/S7-4/S7-5 ──> S7-6（测试/浏览器/报告）
```

强耦合说明：

- S7-1 必须把“协调器 + DELETE 状态 + Pane 销毁保留 + 重开识别 + 单 Session 去重 + Pane/HomePage 共用状态机”作为一个完整单元；不能拆成“先做 Pane、后补协调器”的中间态。
- S7-2 只加入口，不得再新增第二套状态机；它依赖 S7-1 的 `requestClose`。
- S7-3 依赖 S7-1 的 `getCloseState`；否则打开前无法阻止非 idle 接管。
- S7-4 依赖 S7-1 的 `dispose()` 与 S7-3 的同步失效；否则登出期间仍可能有新请求。
- S7-5 依赖 S7-1 的 epoch/dispose；不再重复实现 abort/epoch。

## 15. 验收标准汇总

### 15.1 功能验收

- [ ] 关闭 SSH 的确认框、pending、requested、unknown、failed、重试、取消等待、刷新状态均有明确 UI 和状态。
- [ ] DELETE 200 不显示“已关闭”，不 emit `closed`，不释放 Session ID 预占。
- [ ] Pane 销毁后，协调器仍保留 pending/unknown；重新打开同一 Session 不进入 takeover/手动恢复。
- [ ] 新 Pane 在非 idle 状态下不自动 prepare、不显示接管确认框、零 Prepare/WS。
- [ ] Pane 路径与无 Pane 路径共用同一协调器；同一 Session 至多一个有效 DELETE。
- [ ] 关闭标签不发送 DELETE、不 abort 协调器任务；只退订并 dispose 本地终端。
- [ ] 页面卸载后零新请求；迟到响应不更新已失效 Pane；刷新后不凭本地无记录自动恢复。
- [ ] 登出期间零列表 GET、详情 GET、DELETE、WebSocket、takeover；协调器 dispose 幂等。
- [ ] S6 候选 Pane、takeover、recovery、两步人工 attach 行为不变。
- [ ] `ended` tombstone 防止旧列表行再次打开已结束 Session。

### 15.2 测试与构建验收

- [ ] S7-C01–C15 全部通过，且没有删除或弱化既有断言。
- [ ] `npm test` 全量通过。
- [ ] `npm run typecheck` 通过。
- [ ] `npm run build` 通过。
- [ ] `git diff --check` 退出码 0。
- [ ] 浏览器验收覆盖：行级关闭、DELETE pending/失败/取消、Pane 销毁后重开、页面刷新后 CLOSING、登出成功/失败、控制台无 Vue/资源/运行时错误。
- [ ] 报告区分 Mock、浏览器和真实后端/SSH 环境；未验证项明确列出。

## 16. 回滚方案

| 任务 | 回滚方式 |
|---|---|
| S7-0 | 撤销文档增量；不改代码 |
| S7-1 | 撤销 `session/close.ts`、`closeSession` 可选参数、Pane/HomePage 接线增量；恢复 S6 Pane 内 `closeRemote`；保留候选/takeover |
| S7-2 | 移除行级“关闭”按钮与 HomePage 路由；保留 S7-1；恢复 S6 列表 |
| S7-3 | 撤销 `invalidate`/守卫/合并/打开前检查；恢复 `paneChanged` 直接 refresh |
| S7-4 | 撤销 signOut 增量；恢复 S6 正常/失败路径；不回退第四阶段语义 |
| S7-5 | 撤销订阅/epoch/卸载守卫；保留 S7-1 |
| S7-6 | 仅撤销测试/报告/日志增量 |

回滚约束：

- 只逐文件/逐 hunk 撤销本任务未提交增量；保护用户同文件修改和 S6 成果。
- 禁止 `reset --hard`、`clean`、整文件覆盖用户内容。
- 若无法安全分离，保留并报告，不强行回滚。
- 提案文档已被用户暂存；回滚本文档时只撤销工作区增量，不修改暂存区。

## 17. 尚未解决的风险与非目标

### 17.1 风险

| 风险 | 说明 | 处理建议 |
|---|---|---|
| 页面刷新后本地 unknown 丢失 | 协调器随 HomePage 销毁；新页面无法证明关闭结果 | 依赖服务端详情/提交；不凭本地无记录恢复；如需更强保证需持久化/后端记录（未授权） |
| DELETE 结果未知 | 前端无法证明服务端是否已关闭 | unknown 禁止 takeover；只允许刷新/重试/关闭标签 |
| 请求悬挂 | DELETE/logout 无本地超时 | 方案 A 由用户取消；或 D1 方案 B 增加 UI 期限 |
| 协调器内存 | 每个 Session 一个任务；ended tombstone 保留到页面 dispose | 数量受页面内 Session 数限制；dispose 统一释放；不引入全局 Map |
| 非 idle 创建 Pane | Pane 只读展示关闭状态，仍需确保不自动 prepare | Pane 挂载时读取 `CloseHandle`；HomePage 不调用 prepare；测试覆盖零 POST/WS |
| 行级关闭 UI | 无 Pane 时的确认框由 HomePage 持有 | 与 Pane 确认框文案一致；同一 Session 不会同时出现两个确认框 |
| 路由守卫网络错误（U6） | 网络抖动可能被当作身份失效 | 保持独立问题，不在 S7 修改 |
| `authNotice` 覆盖 | logout 后 `/me` 可能先写“登录已过期” | 跳转后写清理反馈；真实 router 需浏览器验证 |
| 同步抛错 | 自定义 Pane 句柄可能同步 throw | 包装为 Promise；测试覆盖 |
| 真实环境差异 | Mock 不能证明真实网络/SSH 行为 | S7-6 明确标记未验证项，不伪造结论 |

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

按重要程度排序：

1. **D1-1（BLOCKER）**：是否同意由 HomePage 持有按 Session ID 管理的 `CloseCoordinator`。
2. **D1-2（BLOCKER）**：是否同意 Pane 只负责关闭 UI，不拥有跨 Pane 生命周期的 DELETE 任务。
3. **D1-3（BLOCKER）**：是否同意 Pane 销毁不清除 pending/unknown 关闭状态。
4. **D1-4（BLOCKER）**：页面卸载后，是否接受依靠服务端权威查询重新确认状态，而不是本地持久化关闭意图。
5. **D1-5（BLOCKER）**：是否授权新增 `frontend/src/session/close.ts` 作为 S7-1 的正式文件。
6. **D1-6（BLOCKER）**：是否确认 Pane 与无 Pane 两种关闭路径共用同一套状态机。
7. **D1-7（BLOCKER）**：是否确认 unknown 状态下禁止 takeover 和手动恢复。
8. **D1-8（BLOCKER）**：是否确认 unknown 状态只允许查询、幂等重试和关闭本地标签，不允许自动放弃关闭意图。
9. **D1-9（MAJOR）**：是否保留原有裁决：本地 abort 只停止等待、不新增本地超时、`failed` 保留关闭意图、DELETE 200 不等于 CLOSED。
10. **D2-1（BLOCKER）**：是否在 SessionManager 增加行级“关闭”；CONNECTING 是否允许关闭、CLOSING 是否允许重复关闭（推荐均为是）。
11. **D2-2（MAJOR）**：非 idle 时打开 Session，是创建“只读关闭状态 Pane”还是由 HomePage 直接提示（推荐前者）。
12. **D3-1（MAJOR）**：是否确认 `logoutPending` 不必保持到本地 close 完成，`auth.ts` 不改；`signOut` 同步 `coordinator.dispose()`。
13. **D4-1（MAJOR）**：是否接受 `SessionManager` 直接读取 `logoutPending`、使用 `queueMicrotask` 合并。
14. **D5-1（MINOR）**：关闭确认框内显示 DELETE 错误、stop 失败独立提示。
15. **D5-2（MINOR）**：`router.replace` reject 时记录日志并仍写清理反馈。
16. **S7-0**：是否授权在裁决后修订实施计划或新增 S7 子计划文档（本轮未修改任何冻结文档）。

## 19. 结论

1. **本轮修订解决了两个遗留问题：**
   - 关闭任务从 Pane 上移到 HomePage 的 `CloseCoordinator`，Pane 销毁不再丢失 pending/unknown；
   - Pane 与无 Pane 路径共用同一个状态机，`session/close.ts` 成为 S7-1 的正式文件，S7-2 只加行级入口。
2. **页面刷新后的限制已明确：** 本地 unknown 不能跨页面；新页面必须依赖服务端详情/提交结果，不凭本地无记录自动恢复；如需持久化，必须单独授权 localStorage/IndexedDB 或后端字段。
3. **当前仍不具备直接开始 S7 编码的条件。** 第 18 节的 D1-1 至 D1-8、D2-1 等 BLOCKER 需要用户裁决；裁决后按 S7-0 → S7-1 → S7-2 → S7-3 → S7-4 → S7-5 → S7-6 推进。
4. **S6 基线可靠。** 候选 Pane、takeover、recovery、stop/close 和 signOut 最小适配不变；S7 只在其上增加关闭协调器、行级入口、列表失效和登出异常处理。
5. **本轮没有修改任何生产代码、测试、配置或既有冻结文档**；只修改本提案文档。Git 工作区应只有用户原有的 `luan-qi-ba-zao.txt` 修改和本提案的工作区修订；提案暂存区仍是上一轮版本（本轮未执行 `git add`）。
6. 本文所有推荐方案均为“待用户裁决，不得视为已冻结设计”。完成裁决前，不开始 S7 编码，不进入 S8。