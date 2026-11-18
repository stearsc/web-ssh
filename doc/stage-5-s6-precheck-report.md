# Web SSH 第六阶段（S6）编码前检查报告

- 编写日期：2026-11-18。
- 检查对象：第五阶段 S6“Pane 与同页标签协调”的编码前基线。
- 检查性质：只读分析；除新增本报告外，不修改生产代码、测试代码、既有设计文档、配置或样式。
- 当前分支：`codex/第五阶段`。
- 当前 HEAD：`feb0ac5`（`S5-C2`）。
- 工作区状态：检查开始时干净，暂存区为空；`git diff`、`git diff --cached` 均无内容。

## 0. 读取范围

本次检查实际读取了以下资料与源码，不依赖历史对话或 Agent 记忆：

- `doc/stage-5-implementation-plan.md`：§3、§6、§8、§11–§13、§15–§17、§21。
- `doc/测试报告/stage-5-s5-design-review.md`：§6、§14、§20–§24，重点 S6-PRE01–03。
- `doc/测试报告/stage-5-s5-c2-test-report.md`。
- `doc/web-ssh-implementation-stages.md` 的阶段边界。
- `frontend/src/terminal/client.ts`
- `frontend/src/terminal/recovery.ts`
- `frontend/src/terminal/output.ts`
- `frontend/src/components/terminal/TerminalPane.vue`
- `frontend/src/pages/HomePage.vue`
- `frontend/src/components/session/SessionManager.vue`
- `frontend/src/api/sessions.ts`
- `frontend/src/session/list.ts`
- `frontend/src/styles/base.css`
- `frontend/src/components/layout/Modal.vue`
- `frontend/src/components/server/HostKeyDialog.vue`
- `frontend/src/components/server/ServerForm.vue`
- `frontend/tests/terminal.test.mjs`
- `frontend/tests/terminal-pane.test.mjs`
- `frontend/tests/session-list.test.mjs`
- `frontend/tests/stage-5-c2.test.mjs`
- `backend/internal/httpapi/sessions.go`
- `backend/internal/session/session.go`

计划新增的 `frontend/src/session/takeover.ts` 和 `frontend/tests/session-takeover.test.mjs` 当前不存在；其余检查文件均存在。

---

# A. 实际代码基线

## A.1 TerminalClient 真实接口

`frontend/src/terminal/client.ts`：

| 方法/成员 | 实际签名或语义 | 位置 |
|---|---|---|
| `connect` | `connect(expectedGeneration = this.defaultExpectedGeneration()): Promise<ConnectionResult>`，固定 `attach` | `client.ts:230` |
| `run` | `run(expectedGeneration = ...): Promise<RecoveryState>`，固定 `attach` | `client.ts:235` |
| `connectTakeover` | `connectTakeover(expectedGeneration: number): Promise<ConnectionResult>`，显式 `takeover` | `client.ts:240` |
| `runTakeover` | `runTakeover(expectedGeneration: number): Promise<RecoveryState>`，显式 `takeover` | `client.ts:243` |
| `start` | `private start(expected: number, mode: ConnectionMode): RecoveryTask` | `client.ts:249` |
| `loop` | `private async loop(task: RecoveryTask): Promise<RecoveryState>` | `client.ts:327` |
| `nextAttemptGeneration` | `private nextAttemptGeneration(task): AttemptPlan` | `client.ts:399` |
| `prepareCredential` | `private async prepareCredential(task, attempt): Promise<PreparedCredential>` | `client.ts:414` |
| `attempt` | `private attempt(task, prepared, offset): Promise<AttemptResult>` | `client.ts:451` |
| `stop` | `stop(): Promise<void>` | `client.ts:750` |
| `dispose` | `dispose(): Promise<void>` | `client.ts:770` |
| `running` | `task !== null || stopping !== null` | `client.ts:202` |
| `taskId` | 当前任务序号 | `client.ts:205` |
| `currentGeneration` | 当前已确认代次 | `client.ts:208` |
| `canInput` | `admitted && socket OPEN && output.inputEnabled` | `client.ts:220` |
| `completion` | `this.task?.done ?? null` | `client.ts:246` |

### 首次 mode 消费与后续 attach

- `RecoveryTask` 保存不可变 `initialMode`、`initialExpectedGeneration`，以及 `initialModeConsumed`、`confirmedGeneration`：`client.ts:112-118`。
- `nextAttemptGeneration()` 在每轮循环的第一个 `await` 之前消费初始 mode：`client.ts:399-406`。
- 第一次 attempt 使用 `initialMode + initialExpectedGeneration`；之后固定：
  - `mode: 'attach'`
  - `expectedGeneration: confirmedGeneration ?? initialExpectedGeneration`
  - `client.ts:407-412`
- `initialModeConsumed` 与 `retries` 分离，稳定资格重置 `retries` 不会重新 takeover。
- `start()` 校验顺序：
  1. disposed/allowed；
  2. 安全非负整数 generation；
  3. 字面量 mode；
  4. `stopping` 或 `task.stopped` 拒绝；
  5. 活动任务同 mode/同 initial generation 才复用；
  6. 否则抛 `TerminalClientTaskConflictError`（`mode_mismatch`、`generation_mismatch`、`task_stopping`）。
  - `client.ts:249-264`

### 首次 Promise、长期 completion、stop 屏障

- `connection` 每个任务只创建一次，只由首次 attempt 的前缀结果 settle：`client.ts:265-268`。
- `task.done` 是长期 loop 的结果；`completion` 只暴露当前任务的 `done`：`client.ts:246-248`。
- 首次 attempt 前缀完成时 `task.connected({connected:true})`：`client.ts:568-571`。
- 首次 attempt 失败时 `task.connected({connected:false})`：`client.ts:490-497`。
- `stop()`：
  - 同步设置 `task.stopped`；
  - abort controller、取消 retry timer、调用 `finish`；
  - `await task.done`、`await sendTask`、`await output.settle(true)`；
  - `stopping` 非空期间 `running=true`，`start()` 拒绝新任务；
  - `client.ts:750-768`。
- 旧 loop 的 `finally` 只在 `this.task === task` 时清空任务引用：`client.ts:377-384`。

### 错误与关闭处理

- `retryCodes` 包含 `BIND_BUSY`、`INVALID_CREDENTIAL`、`OUTPUT_GAP`、`NETWORK_ERROR` 等：`client.ts:129-143`。
- `BIND_CONFLICT` 不在 `retryCodes` 中，直接停止，不自动查询新代次。
- `taken_over` 控制消息：`parseControl` 严格校验后，`finish(..., keepQueued=true)`，不重试：`client.ts:529-552`、`output.ts:399-405`。
- 裸 `4001`：`WS_4001`，`keepQueued=true`，提示“连接已因其他连接接管而关闭（4001）”，不重试：`client.ts:588-601`。
- `4002`：`keepQueued=false`、`retry=true`，有限恢复。
- `OUTPUT_GAP`：在 `retryCodes` 中，按原预算恢复，丢弃未提交队列。
- `1000`：`keepQueued=true`、不重试。
- `1006/1011/1012/1013`：可重试，丢弃未提交队列。
- `AUTH_REQUIRED`：不重试，Pane 触发登录失效处理。

### 身份隔离

- 旧 Socket：`valid()` 同时检查 `ended`、`admitted(task)`、`this.socket === socket`：`client.ts:470-473`。
- 旧 Prepare：`controller.signal.aborted` 或 `!admitted(task)` 后不创建 WS：`client.ts:429-433`。
- 旧 write callback：`TerminalOutputTracker` 用 `terminalEpoch` 和 `inFlight` 身份检查：`output.ts:259-285`。
- 旧任务状态发布：`publish()` 检查 `task.id === this.sequence`：`client.ts:300-320`。
- 旧 loop `finally`：任务对象身份检查。

## A.2 OutputTracker 实际接口

`frontend/src/terminal/output.ts`：

| 方法/成员 | 语义 | 位置 |
|---|---|---|
| `requestedStartOffset()` | 要求当前无在途/队列，返回 `processedOffset` | `output.ts:105` |
| `inputEnabled` | `phase === 'stream' && processedOffset >= replayEnd` | `output.ts:113` |
| `prefixComplete` | `phase === 'stream'` | `output.ts:116` |
| `begin(requested)` | 校验偏移，进入 binding | `output.ts:133` |
| `bind(message)` | 校验回放范围，设置 truncated/stream | `output.ts:143` |
| `truncated(message)` | 重建终端到 `replayStart` | `output.ts:159` |
| `binary(data)` | 严格校验块大小、偏移、队列上限 | `output.ts:174` |
| `freeze(keepQueued)` | 断线时冻结/丢弃队列 | `output.ts:200` |
| `settle(keepQueued)` | 有限排空；超时重建终端 | `output.ts:210` |
| `dispose()` | 排空后销毁 | `output.ts:238` |

S6 必须继续复用同一 tracker；不能在 Pane 内新建第二套输出恢复机制。

## A.3 HomePage 与 Pane 的实际所有权

### HomePage

`frontend/src/pages/HomePage.vue`：

- `tabs = ref<SessionView[]>([])`：当前页标签的唯一列表，保存 Session 快照：`HomePage.vue:19`。
- `panes = new Map<string, { stop(); dispose() }>()`：按 Session ID 保存 Pane 暴露句柄，但只保存 `stop/dispose`，没有状态查询：`HomePage.vue:22-25`。
- `setPane(id, pane)`：函数 ref，pane 为空时删除：`HomePage.vue:26-30`。
- `removeTab(id)`：过滤 tabs，必要时切换 activeTab：`HomePage.vue:33-36`。
- `connectServer()`：创建新 Session 后 `tabs.push(session)`，没有重复 ID 检查：`HomePage.vue:37-54`。
- 模板：
  - `<TerminalPane v-for="tab in tabs" :key="tab.id" v-show="view === 'servers' && activeTab === tab.id" ...>`：`HomePage.vue:382-390`
  - `<SessionManager v-if="view === 'sessions'" ...>`：`HomePage.vue:391-396`
- 视图切换：
  - `view === 'sessions'` 时 SessionManager 挂载，Pane 仍挂载但 `v-show` 隐藏。
  - 返回服务器时 SessionManager 卸载，Pane 不卸载。
- `signOut()`：
  - 同步 `logoutPending = true`；
  - abort `operations`/`query`；
  - 对所有 pane 调用 `stop()`；
  - 等 logout 和 stop 后，再 `dispose()` 所有 pane；
  - 清空 tabs，跳转登录页。
  - `HomePage.vue:240-264`
- `onBeforeUnmount()`：只 abort 查询和 `operations`，不显式停止 pane；Pane 自身的 `onBeforeUnmount` 会 `dispose()`。
- 当前没有“打开已有 Session”的入口，也没有 Session ID 预占机制。

### TerminalPane

`frontend/src/components/terminal/TerminalPane.vue`：

- props：`{ session: SessionView; active: boolean }`：`TerminalPane.vue:24`。
- emits：`closed`：`TerminalPane.vue:25`。
- 每个 Pane 私有：
  - `client: TerminalClient | null`
  - `terminal: Terminal | null`
  - `recovery: SessionRecovery | null`
  - `resize`、`theme` observer
- `onMounted()` 创建 `TerminalClient` 和 `SessionRecovery`，然后无条件 `void connect()`：`TerminalPane.vue:218-308`。
- `connect()` 默认 attach，成功前缀后 `layout(true)`：`TerminalPane.vue:156-168`。
- `recover()` 调用 `recovery.recover()`：`TerminalPane.vue:169-173`。
- `stop()` 返回 `recovery.stop() ?? client.stop()`：`TerminalPane.vue:125-128`。
- `dispose()` 设置 `disposed=true`，invalidate recovery，断开 observer，`client.dispose()`：`TerminalPane.vue:201-216`。
- `defineExpose({ stop, dispose })`：`TerminalPane.vue:129`。
- `watch([logoutPending, identity], ...)` 在退出/身份失效时同步 `stop()`：`TerminalPane.vue:321-329`。
- `onBeforeUnmount()` 调用 `dispose()`：`TerminalPane.vue:331-333`。

**关键事实：当前 Pane 没有“候选模式”，mount 必然自动 attach；也没有主动接管状态或详情查询入口。**

### SessionRecovery

`frontend/src/terminal/recovery.ts`：

- `snapshot`、`candidate`、`loading`、`acting`、`ended`、`error`：`recovery.ts:7-14`。
- `canRecover` 要求 `!client.running`，且 candidate 为 `CONNECTED/DISCONNECTED`：`recovery.ts:31-40`。
- `invalidate()`：sequence++、abort、清 candidate/loading/acting：`recovery.ts:42-49`。
- `refresh(manual = true, sourceTask = client.taskId)`：
  - 要求 allowed、非 acting、client 未 running；
  - 每次 refresh 先 invalidate；
  - GET 详情，校验 `sessionView`；
  - `manual && !ended` 时设置 candidate；
  - sequence、Abort、sourceTask、allowed 隔离迟到响应；
  - `recovery.ts:51-94`。
- `recover()`：
  - `canRecover` 后消费 candidate；
  - `await client.stop()`；
  - 检查 sequence/allowed/ended；
  - `client.connect(candidate.binding_generation)`，只发 attach；
  - `recovery.ts:96-119`。
- `stop()`：invalidate + `client.stop()`：`recovery.ts:120-123`。

**当前 Recovery 完全不识别 takeover；S6 必须通过 Pane 协调互斥，不能让 Recovery 的默认 recover 走到 takeover。**

### SessionManager 与列表控制器

`frontend/src/components/session/SessionManager.vue`：

- props：`servers: ServerView[]`、`canRefresh: boolean`、`authToken: unknown`：`SessionManager.vue:17-21`。
- `SessionListController` 负责 items/loading/error/lastLoadedAt/stale/sequence/Abort/dispose。
- 服务器筛选通过 `listSessions(serverID, signal)` 作为服务端 query；状态筛选只对当前 items 本地过滤：`SessionManager.vue:23-31`、`session/list.ts:1-100`。
- 刷新触发：
  - `onMounted` 首次 refresh：`SessionManager.vue:128-132`
  - 手动刷新按钮：`SessionManager.vue:85-87`
  - 服务器筛选变化：`SessionManager.vue:105`
  - 页面 hidden → visible：`SessionManager.vue:122-126`
  - 没有定时轮询。
- `onBeforeUnmount` 移除 visibility listener 并 `controller.dispose()`：`SessionManager.vue:134-137`。
- `defineExpose({ refresh, invalidate })`：`SessionManager.vue:139-142`。
- 当前表格只有展示列，没有“打开/接管”操作列：`SessionManager.vue:241-280`。

`frontend/src/session/list.ts`：

- `SessionListController.refresh()` 只调用 `load`，不创建 POST/WS：`list.ts:37-74`。
- `current()` 同时检查 disposed、sequence、Abort、allowed、identity：`list.ts:92-103`。
- 旧 finally 不会关闭新请求的 loading。

### API 与后端

`frontend/src/api/sessions.ts`：

- `sessionView(value, id)`：严格校验并只复制冻结字段：`sessions.ts:45-110`。
- `sessionList(value)`：校验 items 数组、ID 格式、重复 ID：`sessions.ts:112-133`。
- `listSessions(serverID, signal)`：`GET /api/sessions?server_id=...`：`sessions.ts:135-149`。
- `createSession`、`closeSession`：`sessions.ts:27-42`。
- **没有单独的 `getSession` 包装**；`SessionRecovery` 直接用 `request` + `sessionView` 查询详情。

后端 `backend/internal/httpapi/sessions.go`：

- `GET /api/sessions`：`sessions.go:27`
- `GET /api/sessions/{id}`：`sessions.go:56`
- `POST /api/sessions/{id}/takeover`：`sessions.go:72`
- `GET /ws/sessions/{id}`：`sessions.go:87`

`Manager.Get()` 对 CLOSED/不存在返回 `SESSION_NOT_FOUND`：`backend/internal/session/session.go:201-212`。

**结论：单 Session 详情 GET API 已存在，S6 不需要新增或修改后端接口。**

## A.4 实际所有权关系

```text
HomePage
├─ tabs: SessionView[]                 唯一标签列表（当前仅新创建 Session）
├─ panes: Map<SessionID, PaneHandle>   只含 stop/dispose
├─ activeTab / view
└─ TerminalPane × N
   ├─ TerminalClient × 1
   │  ├─ RecoveryTask × 0/1
   │  ├─ WebSocket × 0/1
   │  ├─ AbortController × 0/1
   │  └─ TerminalOutputTracker × 1
   └─ SessionRecovery × 1
      └─ 详情 GET / 人工 attach 候选
SessionManager
└─ SessionListController × 1
   └─ 列表 GET / 本地状态筛选
```

---

# B. 冻结设计与当前实现差异

## B.1 已具备能力

| 能力 | 现状 | S6 结论 |
|---|---|---|
| 显式 takeover 连接入口 | `connectTakeover` / `runTakeover` 已实现 | 直接复用 |
| 首次 mode 消费、后续固定 attach | `initialModeConsumed` + `nextAttemptGeneration` | 直接复用 |
| 首次 Promise / 长期 completion / stop 屏障 | 已实现且 S5-C2 已测 | 复用；S6 必须 `await stop()` |
| `BIND_CONFLICT` 停止、不自动换代 | 已实现 | 复用 |
| 裸 4001 / `taken_over` 停止且明确提示 | 已实现 | 复用 |
| 4002 / `OUTPUT_GAP` 有限恢复 | 已实现 | 复用 |
| 旧任务/Socket/Prepare/write callback 隔离 | 已实现 | 复用 |
| 两步人工 attach | `SessionRecovery.refresh/recover` 已实现 | 保持，不混入 takeover |
| 单 Session 详情 GET | 后端已实现 | 直接复用 |
| 列表请求身份/Abort/旧 finally 隔离 | `SessionListController` 已实现 | 直接复用 |
| 列表刷新 D1 触发 | 首次、手动、筛选、可见性 | 复用；操作完成依赖管理页重新挂载 |
| 现有 UI 设计令牌与组件 | `base.css`、`Modal.vue`、`.session-*` | 复用 |

## B.2 需要新增的能力

| 能力 | 当前缺口 | 最小新增 |
|---|---|---|
| 候选 Pane 不自动连接 | `onMounted` 无条件 `connect()` | Pane 增加显式启动模式；候选模式只创建 client，不 connect |
| 主动接管详情/确认/取消所有者 | 不存在 | 新增 `frontend/src/session/takeover.ts` |
| HomePage 同 Session 唯一登记 | 只有 tabs/panes 的隐式关系，无 pending | 增加同步预占（tabs + pending/closing） |
| SessionManager 行操作 | 没有“打开”按钮/emit | 增加操作列与 `open` 事件 |
| Pane 状态暴露 | 只 expose stop/dispose | 增加 `isOnline()` / `state()` / `prepareTakeover()` 等 |
| Pane 主动接管 UI | 不存在 | 候选摘要、确认弹窗、失败/取消提示 |
| 停止恢复并准备接管 | 只有“手动恢复” | 增加显式按钮，调用 `SessionTakeover.prepare()` |
| 接管后列表刷新 | 管理页当前会卸载 | 依赖重新挂载刷新；如保持挂载则需 ref.refresh() |
| Recovery 与 takeover 互斥 | Recovery 不识别 takeover | Pane 先 `recovery.invalidate()`；建议加 `blocked` 谓词 |

## B.3 需要适配的既有接口

| 接口/结构 | 适配点 | 说明 |
|---|---|---|
| `TerminalPane` props | 增加启动模式/候选标记 | 不能改变现有 `active`/`session` 语义 |
| `TerminalPane` expose | 增加状态查询和主动接管入口 | HomePage 需要判断在线/恢复中/已停止 |
| `HomePage.panes` 类型 | 从 `{stop,dispose}` 扩展为 `PaneHandle` | 仍只保存句柄，不保存 client |
| `SessionManager` emits | 增加 `open: [SessionView]` | 不改变列表刷新逻辑 |
| `SessionRecovery` | 可选增加 `blocked: () => boolean` | 防止主动动作期间人工候选/诊断查询并发 |
| `SessionView` 使用 | 候选 Pane 初始使用列表快照 | 确认必须使用详情快照 generation，不能使用列表推断 |

## B.4 潜在生命周期与竞态风险

1. **候选 Pane mount 自动 attach**  
   当前 `onMounted` 末尾无条件 `void connect()`：`TerminalPane.vue:308`。候选模式若不加显式门禁，会在用户确认前产生 POST/WS。

2. **重复点击/异步挂载窗口**  
   HomePage 当前没有已有 Session 的打开路径；若 S6 在 `await` 之后才 push tab，会出现两个 Pane。必须在任何 await 之前同步登记。

3. **在线/恢复中/已停止判定**  
   Pane 只暴露 stop/dispose；HomePage 无法判断“在线只定位”还是“恢复中准备接管”。需要 Pane 暴露阶段状态。

4. **stop 与详情 GET 的顺序**  
   若详情 GET 先于 `client.stop()` 完成，会违反“stop 未完成不得启动详情确认/新任务”。必须 `await client.stop()` 后再 GET。

5. **人工 attach 候选与 takeover 详情并发**  
   `SessionRecovery.refresh()` 在 client 未 running 时仍可执行；主动接管详情 GET 期间若用户点击“刷新 Session 状态”，会生成人工 candidate，形成两个候选。需要 Pane 门禁 + 可选 `blocked` 谓词。

6. **诊断刷新绕过 Pane 门禁**  
   `stoppedState()` 在 `state.diagnostic` 时直接调用 `recovery.refresh(false, taskId)`：`TerminalPane.vue:91-110`。主动接管期间旧任务停止可能触发该诊断 GET，需要加 takeover busy 判断。

7. **旧查询/旧 finally 覆盖新动作**  
   `SessionRecovery` 已有 sequence/Abort/sourceTask；新的 `SessionTakeover` 必须复制同等身份隔离，不能只用 `loading` 布尔值。

8. **关闭标签/重开窗口**  
   `removeTab()` 只过滤 tabs；旧 Pane 的卸载在下一 tick。若立即重开同一 Session，可能短暂存在两个 Pane。需要 closing 集合或等待卸载完成。

9. **登出/身份失效时主动动作未取消**  
   Pane 的 logout watch 只调用 `stop()`；如果 `stop()` 不 invalidate takeover controller，详情 GET 仍可能迟到并尝试启动任务。

10. **列表数据反向驱动终端**  
    不能在列表刷新时把新 `binding_generation` 写入运行中的 client；确认必须使用详情快照。当前 client 不会被 props 自动更新，但 S6 不应新增此类 watcher。

11. **关闭 SSH 与主动接管竞争**  
    `closeRemote()` 调用 `stop()` 后 DELETE；S6 至少要保证 `stop()` 同时 invalidate takeover。完整关闭互斥属于 S7，但 S6 不能留下“关闭后仍启动 takeover”的窗口。

12. **回放输入门禁**  
    接管成功前缀完成不等于输入可用；必须继续依赖 `output.inputEnabled`，不能在 `connected=true` 时提前发送输入。

## B.5 不需要修改的模块

| 模块 | 结论 |
|---|---|
| `frontend/src/terminal/client.ts` | S5 接口已足够；预计不改 |
| `frontend/src/terminal/output.ts` | 输出/偏移/排空算法无需改 |
| `frontend/src/api/sessions.ts` | 详情 GET 可复用 `request` + `sessionView`；预计不改 |
| 后端 `httpapi/sessions.go`、`session/*` | 列表/详情/takeover 路由已存在；不改 |
| auth/router | 沿用现有 `logoutPending`、`identity`；不改 |
| 数据库 migration/config/依赖/package lock | 不需要 |
| `frontend/src/styles/base.css` | 现有令牌/组件足够；预计不改全局样式 |

---

# C. S6 具体实施方案

## C.1 真实调用链

```text
SessionManager 行“打开”
  → emit('open', SessionView)
  → HomePage.openSession(item)
      ├─ 已有 tab？
      │   ├─ 是：view='servers'，activeTab=id
      │   │      → Pane 根据自身阶段显示：
      │   │         online/replaying：仅定位，不停止、不接管
      │   │         preparing/connecting/retry_wait/stopped：
      │   │           显示“停止恢复并准备接管”
      │   └─ 否：同步预占 + push 候选 tab + candidateIds.add(id)
      │         → view='servers'，activeTab=id
      ↓
TerminalPane（候选模式）
  → onMounted 创建唯一 client/tracker/recovery/takeover
  → 不调用 connect()
  → takeover.prepare()
      → client.stop() 完整屏障
      → GET /api/sessions/{id}（可取消）
      → sessionView 校验
      → 展示目标/状态/generation/离线期限/接管影响
      ↓
用户点击“确认主动接管”
  → takeover.confirm()
      → 再次检查 allowed/sequence/client 未 running
      → client.connectTakeover(snapshot.binding_generation)
      → 首次 Prepare：mode=takeover
      → WS bind
      → bind_success 且完整前缀
      → connection Promise connected=true
      → 同一 Pane layout(true)/resize/回放门禁
      ↓
后续掉线
  → client 内部同一任务固定 attach
```

## C.2 状态流转

### HomePage 层

```text
idle
  → reserved（同步登记 Session ID）
  → candidate-created（无 Pane 场景）
  → located（已有 Pane 场景）
  → released（关闭标签/取消/卸载）
```

### Pane/主动动作层

```text
candidate-idle
  → stopping-old       （存在旧任务时）
  → loading-detail     （GET 详情）
  → ready              （详情成功，等待用户确认）
  → starting           （已确认，首次 takeover 任务启动）
  → connected          （完整前缀成功）
  → failed             （详情/Prepare/首次绑定失败）
  → cancelled          （用户取消）
  → ended              （SESSION_NOT_FOUND 或 closed）
```

### Client 层（沿用 S5）

```text
idle
  → preparing（首次 takeover 或后续 attach）
  → connecting
  → replaying / online
  → retry_wait（后续固定 attach）
  → stopped / disposed
```

## C.3 每个模块的职责

| 模块 | S6 职责 | 明确不做 |
|---|---|---|
| `HomePage.vue` | Session ID 同步登记、候选 tab、已有 Pane 定位、view/activeTab、页面停止/销毁 | 不发详情 GET、不创建 Client、不管理恢复循环 |
| `SessionManager.vue` | 列表展示/筛选/刷新；行“打开”emit | 不查详情、不创建 Pane、不启动连接 |
| `TerminalPane.vue` | 唯一 client/tracker/recovery/takeover；候选启动模式；主动接管 UI；stop/dispose 协调 | 不建第二 Client、不写第二套恢复循环 |
| `session/takeover.ts`（新增） | 详情 GET、确认候选、取消/失效、调用 `client.stop()` 与 `client.connectTakeover()` | 不创建 socket、不实现重试循环、不改 output |
| `terminal/recovery.ts` | 保持两步人工 attach；可选增加 `blocked` 谓词 | 不把 takeover 混入默认 recover |
| `terminal/client.ts` | 提供显式 takeover、stop 屏障、身份隔离 | 不做 UI/详情/列表 |
| `session/list.ts` | 列表请求身份/Abort/快照 | 不驱动终端连接 |

## C.4 同一 Session 唯一 Pane 的实现方法

最小方案，不新增全局 registry：

```text
tabs: SessionView[]                  // 同步唯一标签列表
panes: Map<SessionID, PaneHandle>    // 挂载后的句柄
pending: Set<SessionID>              // push 之前的同步预占
closing: Set<SessionID>              // 关闭后等待卸载完成
candidateIds: Set<SessionID>         // 候选 Pane 标记
```

规则：

1. `openSession(item)` 首先检查 `pending.has(id)` 或 `closing.has(id)`，有则直接返回/定位。
2. 已有 `tabs` 项：只切换 `view/activeTab`，不 push、不创建 Pane。
3. 没有 tab：在**任何 await 之前**执行 `pending.add(id)`、`tabs.push(copy)`、`candidateIds.add(id)`、`activeTab=id`、`view='servers'`，然后 `pending.delete(id)`。
4. `setPane(id, handle)`：
   - `handle === null`：`panes.delete(id)`；
   - 否则写入；若已有不同 handle，说明唯一性被破坏，应拒绝覆盖并记录错误（测试断言）。
5. `removeTab(id)`：
   - 从 tabs/candidateIds/pending 移除；
   - 加入 `closing`，在 `nextTick` 或 `setPane(id, null)` 后移除；
   - 在 closing 期间禁止重开同一 ID。
6. `connectServer()` 对新创建 Session 也做一次 ID 重复检查，防止异常 DTO 导致重复标签。

这样即使 Pane 尚未 mount，`tabs` 已经同步登记；重复点击不会创建第二个 Pane。

## C.5 旧任务 stop 与新任务启动的严格先后

```text
1. takeover.acting = true            // 同步封闭重复动作
2. recovery.invalidate()             // 清除人工 attach 候选/旧详情
3. takeover.controller?.abort()      // 取消旧详情 GET
4. await client.stop()               // 等 task.done + sendTask + output.settle(true)
5. 检查 sequence/allowed/client.running
6. GET /api/sessions/{id}            // 只有 stop 完成后才查询详情
7. 用户确认
8. 再次检查 sequence/allowed/候选身份
9. client.connectTakeover(detail.binding_generation)
```

防线：

- `client.start()` 在 `stopping` 或 `task.stopped` 时抛 `task_stopping`：`client.ts:256-257`。
- `SessionTakeover` 不得在 stop Promise settle 前调用 `connectTakeover`。
- `Pane.stop()` 必须先 `takeover.invalidate()`，再 `recovery.stop()`/`client.stop()`。
- `dispose()`/logout/关闭标签同样先 invalidate takeover。

## C.6 详情候选与确认候选的身份隔离

建议 `SessionTakeover` 内部：

```text
sequence: number
controller: AbortController | null
snapshot: SessionView | null
loading: boolean
acting: boolean
error: string
phase: 'idle' | 'stopping' | 'loading' | 'ready' | 'starting' | 'failed' | 'cancelled' | 'ended'
```

- 每次 `prepare()`：
  - `sequence++`；
  - abort 旧 controller；
  - 清空 snapshot/error；
  - 记录本次 sequence；
  - stop → GET → 校验 → 发布 snapshot。
- 每次 `confirm()`：
  - 只使用当前 `snapshot`；
  - 在首次 await 前消费候选（置 `snapshot=null` 或 `consumed=true`）；
  - 记录 sequence；
  - await `connectTakeover`；
  - 返回后检查 sequence/allowed；旧结果不得写 `phase/error/snapshot`。
- `invalidate()`：
  - sequence++、abort、清 snapshot/loading/acting/error；
  - 用于取消、stop、dispose、logout、关闭标签、重开。
- Pane 的 `stoppedState` 继续用 `taskId` 隔离旧任务状态。
- Client 内部继续用 task/socket/sequence/epoch 隔离旧回调。

## C.7 失败、取消、迟到回调处理

| 事件 | 处理 |
|---|---|
| 详情 GET 失败 | 显示 `error`；不 POST、不建 WS；候选 Pane 保留，可重试 |
| 详情 `SESSION_NOT_FOUND` | 标记 `ended`；禁止确认；不自动删除 Pane |
| 详情 `CONNECTING/CLOSING` | 展示状态；确认按钮禁用；可刷新详情 |
| 用户取消 | `invalidate()`；清 snapshot；不 POST/WS；旧任务保持停止 |
| Prepare 失败（可重试） | `connection=false`；client 按方案 A 继续 attach；Pane 显示“接管未成功，自动恢复中”；不再 takeover |
| Prepare `BIND_CONFLICT` | client 停止；Pane 显示冲突；不自动查询新代次 |
| 首次前缀失败 | `connection=false`；不反馈接管成功；保留 Pane/原因 |
| 旧详情 GET 迟到 | sequence/Abort 丢弃 |
| 旧 Socket/finally 迟到 | client 身份检查丢弃；Pane taskId 检查 |
| logout/dispose | invalidate + stop；旧结果不能创建 WS/改状态 |
| 关闭标签 | dispose；取消详情/确认；不 DELETE Session |

---

# D. 样式复用检查

## D.1 现有可复用依据

### 设计令牌

`frontend/src/styles/base.css:2-40`：

- `--page`、`--panel`、`--hover`、`--text`、`--secondary`、`--line`
- `--strong`、`--on-strong`、`--red`、`--overlay`
- `[data-theme='dark']` 已有完整深色主题。

S6 不应新增颜色、字号、圆角、阴影体系。

### 按钮

- 基础按钮：`base.css:48-58`（`min-height: 35px; padding: 7px 12px; border-radius: 2px`）。
- 主按钮 `.primary`：`base.css:60-68`。
- 危险色 `.danger`：`base.css:110-112`。
- 小尺寸终端工具按钮：`base.css:458-463`（`.terminal-actions button`，`min-height: 29px; padding: 4px 7px; font-size: 12px`）。
- 图标按钮 `.icon-button`：`base.css:83-92`。

### 弹窗

- 组件：`frontend/src/components/layout/Modal.vue`。
- 样式：`base.css:297-330`（`dialog`、`::backdrop`、`.dialog-header`、`.dialog-body`、`.dialog-footer`）。
- 现有确认交互：
  - `TerminalPane.vue` 的“关闭 SSH 会话”确认：`TerminalPane.vue:409-441`。
  - `HomePage.vue` 的“删除服务器”确认：`HomePage.vue:455-480`。
  - `HostKeyDialog.vue`、`ServerForm.vue` 均复用 `Modal`。

### 提示、错误、空态、加载

- `.notice`：`base.css:289-296`。
- `.muted`：`base.css:106-109`。
- `.error`：`base.css:110-116`。
- `.session-query-state` / `.session-empty`：`base.css:540-545`。
- `.session-first-error` / `.session-refresh-error`：`base.css:546-565`。
- `TerminalPane` 的 `<p class="notice" role="status/alert">`：`TerminalPane.vue:352-404`。

### 状态标识

- `.session-status`：`base.css:611-624`。
- `status-connected`：绿色 `#377a4b`。
- `status-connecting` / `status-closing`：黄色 `#8a6a25`。
- `status-disconnected`：`var(--red)`。
- `.snapshot-badge` / `.stale`：`base.css:529-538`。

### 列表、表格、筛选器

- `.session-manager`：`base.css:474-480`。
- `.session-toolbar`：`base.css:499-527`。
- `.session-table-wrap` / `.session-table`：`base.css:566-609`。
- `.section-kicker`：`base.css:491-497`。
- `select`/`input` 基础样式：`base.css:38-46`、`70-75`。

### 详情、布局、标签

- `.details` / `dt` / `dd`：`base.css:274-288`。
- `.actions`：`base.css:165-170`。
- `.main`：`base.css:193-204`。
- `.server-detail`：`base.css:206-210`。
- `.terminal-tabs` / `.selected`：`base.css:418-435`。
- `.terminal-actions` / `.connection-state`：`base.css:442-470`。

## D.2 S6 新增 UI 的复用映射

| S6 UI | 复用依据 | 说明 |
|---|---|---|
| SessionManager 行“打开”按钮 | `.session-table` 单元格 + 基础 `button` + `.actions` | 增加一个操作列；表格已有横向滚动和 `white-space: nowrap`，不新增布局体系 |
| 候选 Pane 目标摘要 | `.details`（`dl/dt/dd`）+ `.mono` + `.muted` | 展示名称、服务器、状态、generation、离线期限 |
| 候选状态标识 | `.session-status` + `status-connected/connecting/closing/disconnected` | 与列表状态颜色一致 |
| 接管影响/失败原因 | `.notice` + `role="status"` / `role="alert"`；错误用 `.error` | 与 Pane 现有提示一致 |
| “停止恢复并准备接管” | `.terminal-actions button` | 与“手动恢复/刷新 Session 状态/停止恢复”同一尺寸 |
| 确认接管弹窗 | `Modal.vue` + `.dialog-body` + `.dialog-footer` + `button.primary`/`button.danger` | 与“关闭 SSH 会话”“删除服务器”完全同一模式 |
| 详情加载/空态/错误 | `.session-query-state`、`.session-empty`、`.session-first-error` 或 `.notice` | 不新造加载组件 |
| 候选标签 | 现有 `.terminal-tabs` / `button.selected` | 候选 Pane 使用同一标签栏，不新增特殊标签样式 |
| 成功/回放/停止状态 | `.connection-state` + 现有 `connectionLabel` | 不新增状态徽标体系 |

## D.3 需要新增的样式

**建议不修改全局 `base.css`。**

如果候选 Pane 需要一个带边框的摘要面板，只允许在 `TerminalPane.vue` 的 scoped CSS 中增加最小类，例如：

```css
.takeover-summary {
  padding: 18px 24px;
  border-bottom: 1px solid var(--line);
}
```

理由：

- `18px 24px` 与 `.dialog-body` 的内边距一致：`base.css:318-323`。
- `1px solid var(--line)` 与 `.terminal-actions`、`.session-toolbar` 的分隔线一致。
- 不新增颜色、字号、圆角、阴影、渐变、动画。

如果确认弹窗直接使用 `Modal.vue`，则**无需新增任何 CSS**。

---

# E. S6 测试计划

所有测试使用可控 `fetch`、Mock WebSocket、Deferred Promise、显式 stop/settle 屏障和 mock timer；不使用固定 `sleep`。

## E.1 `SessionTakeover` 单元/控制器测试

新增 `frontend/tests/session-takeover.test.mjs`，直接使用真实 `TerminalClient`、真实 `SessionRecovery`、Mock WebSocket 和可控 fetch。

| ID | 场景 | 异步屏障 | 预期断言 |
|---|---|---|---|
| T01 | 候选 Pane 首次准备 | 详情 GET 挂起 | 只有 GET；POST=0；WS=0；无 client task |
| T02 | 已有 attach 任务时准备接管 | `client.stop()` 用 Deferred 挂起 | stop 完成前无详情 GET；完成后才 GET；无 takeover POST |
| T03 | stop 的 output drain 未完成 | 旧 write callback 挂起 | `client.running=true`；详情 GET=0；新任务启动被 `task_stopping` 拒绝 |
| T04 | 详情 CONNECTING/CLOSING | GET resolve | `canConfirm=false`；确认按钮禁用；POST=0 |
| T05 | 详情 `SESSION_NOT_FOUND` | GET reject | 标记 ended；无 POST/WS；不可确认 |
| T06 | 用户取消确认 | 详情已 resolve | 清 snapshot；无 POST/WS；旧任务保持停止；不自动 attach |
| T07 | 确认成功 | POST/WS/bind_success 分段控制 | 首次 POST body 为 `{mode:'takeover', expected_generation:详情G}`；bind_success=G+1；connection 只在完整前缀后 true |
| T08 | 首次 takeover 失败但可重试 | 第一次 WS 关闭 1006 | connection=false；`client.running=true`；下一次 POST 为 `attach`；后续永不 takeover |
| T09 | `BIND_CONFLICT` | 首次 WS 返回 BIND_CONFLICT | 停止；无自动详情 GET；无第二次 takeover |
| T10 | 重复确认 | 两次点击同一确认 | 只有一个任务/一个 POST/一个 WS；第二次不创建新任务 |
| T11 | 旧详情 GET 迟到 | 两次 prepare 交错 | 旧响应被 sequence/Abort 丢弃；只发布新 snapshot |
| T12 | 旧 Socket/旧 finally 迟到 | stop 后启动 takeover，再触发旧 handler | 新 task/generation/socket/state/offset 不变 |
| T13 | 详情 GET 期间 stop/dispose | GET 挂起后 invalidate | GET signal aborted；无 POST/WS；无新任务 |
| T14 | 确认启动期间 stop | `connectTakeover` 已创建任务 | 任务被 stop；迟到 connection=false 不写新状态 |
| T15 | 同实例 offset 复用 | 旧任务已处理 8 字节后 stop | 新 takeover 的 bind `requested_start_offset=8` |
| T16 | 新 Pane 新实例 | 新 client/tracker | 首次 bind `requested_start_offset=0` |
| T17 | 两步人工 attach 回归 | `refresh()` → `recover()` | POST 仍为 `attach`；不出现 takeover；先 stop 后 connect |
| T18 | S5 方案 A / 4001 / 4002 / OUTPUT_GAP | 复用现有 client 测试场景 | 首次 takeover 只一次；4001/4002/OUTPUT_GAP 分类不变 |

## E.2 `TerminalPane` 集成测试

扩展 `frontend/tests/terminal-pane.test.mjs`：

| ID | 场景 | 断言 |
|---|---|---|
| P01 | 候选模式 mount | 无 POST/WS；只有详情 GET；显示目标/状态/代次 |
| P02 | 候选确认 | 点击确认后才 POST takeover；bind 前无 WS；成功后同一 Pane 聚焦 |
| P03 | 回放门禁 | bind_success 有 replay 范围时 `canInput=false`；回放 drain 后才允许输入 |
| P04 | 恢复中 Pane 准备接管 | 点击“停止恢复并准备接管”后 stop；stop 完成前无 GET；取消后无 POST；旧任务不复活 |
| P05 | 在线 Pane | 只定位；不调用 stop/prepare；不创建第二 Client |
| P06 | 详情失败/取消 | Pane 保留；错误可见；可重试；无 POST/WS |
| P07 | 关闭候选标签 | dispose 后旧 GET 迟到无效；无 POST/WS |
| P08 | logout/identity 变化 | 同步 stop；invalidate takeover；无新任务 |
| P09 | 旧事件隔离 | 旧 socket 的 close/message/error 不覆盖新任务状态 |
| P10 | 双击“准备接管” | 只有一次 stop/GET；不创建第二个详情候选 |

## E.3 `HomePage` 与 `SessionManager` 集成测试

扩展现有 `frontend/tests/session-list.test.mjs`（该文件已有 HomePage/SessionManager 的 SFC 测试夹具，复用比新建第二套夹具更小）：

| ID | 场景 | 断言 |
|---|---|---|
| H01 | 同一行连续点击两次 | 只挂载一个 Pane；只创建一个 tab；第二次只定位 |
| H02 | 异步挂载窗口 | `tabs` 已同步登记；`panes` 尚未 mount 时重复点击不创建第二个 |
| H03 | 同 Session 多次打开 | 同一 Pane 实例；activeTab 不变；无第二个 Client |
| H04 | 不同 Session 并行 | 两个独立 Pane/Client；互不影响 |
| H05 | 在线 Pane 从列表打开 | 只切 view/activeTab；不调用 prepare/stop；无 POST/WS |
| H06 | 关闭后立即重开 | closing 屏障阻止两个 Pane 同时存在 |
| H07 | 会话管理卸载/重进 | 列表重新挂载并刷新；Pane 不卸载 |
| H08 | 登出/页面卸载 | 所有 Pane stop/dispose；无迟到连接 |
| H09 | SessionManager 行按钮 | 只 emit `open`；不自行 fetch 详情/创建 Client |

## E.4 回归测试

必须重跑并通过：

- `frontend/tests/terminal.test.mjs`
  - 默认 attach；
  - 4001/4002/OUTPUT_GAP；
  - stop 屏障；
  - 人工恢复两步 attach；
  - tracker offset/重建。
- `frontend/tests/stage-5-c2.test.mjs`
  - M01–M08；
  - P01–P07；
  - S6-PRE01–03；
  - 方案 A、`BIND_CONFLICT`、`taken_over`。
- `frontend/tests/session-list.test.mjs`
  - 列表 DTO、筛选、刷新、可见性、旧响应隔离。
- `frontend/tests/terminal-pane.test.mjs`
  - 现有 UI 状态/历史诊断/网络展示回归。

## E.5 关键异步屏障

- stop：Deferred `client.stop()` / 手动 resolve 旧 write callback。
- 详情：Deferred fetch，手动 resolve/reject；检查 `signal.aborted`。
- Prepare：Deferred POST，手动返回 credential/错误。
- WS：手动 `socket.open()`、`socket.message(bind_success)`、`socket.onclose({code})`。
- 重试：`t.mock.timers`，显式 `tick`，不等待真实时间。
- 组件：`nextTick()` + 多轮 microtask `flush`，不固定 sleep。

---

# F. 修改文件与风险清单

## F.1 预计修改文件

| 文件 | 修改理由 | 具体职责 | 潜在风险 | 验证方法 |
|---|---|---|---|---|
| `frontend/src/components/terminal/TerminalPane.vue` | 当前 mount 自动 attach，无候选/接管 UI | 增加启动模式；创建 `SessionTakeover`；候选摘要/确认/取消；stop/dispose 先 invalidate；暴露状态/准备入口 | 候选模式误 attach；logout/dispose 漏取消详情；旧提示覆盖新状态 | P01–P10；terminal-pane 回归 |
| `frontend/src/pages/HomePage.vue` | 当前无已有 Session 打开/唯一登记 | `openSession`、同步预占、candidate/closing 集合、PaneHandle 类型、SessionManager 事件接线 | 重复 Pane；关闭后立即重开双 Pane；列表数据驱动连接 | H01–H09；HomePage SFC 测试 |
| `frontend/src/components/session/SessionManager.vue` | 当前无行操作 | 增加“打开”按钮/`open` emit；可选 busySessionId；保持列表刷新不变 | 列表刷新副作用；按钮重复触发 | H09；session-list 回归 |
| `frontend/src/session/takeover.ts`（新增） | 详情/确认/取消状态机需要独立所有者 | sequence/Abort；stop 屏障；详情校验；确认消费；调用 `connectTakeover` | 第二套连接所有者；旧响应覆盖；未等待 stop | T01–T18 |
| `frontend/src/terminal/recovery.ts` | 可选最小适配：主动动作期间阻止人工候选/诊断查询 | 增加可选 `blocked: () => boolean`，用于 `canRecover/refresh/recover` | 改变默认 attach 语义；影响两步恢复 | T17；terminal.test 人工恢复回归 |
| `frontend/tests/terminal-pane.test.mjs` | 覆盖候选/接管 UI 生命周期 | 候选零连接、stop 后接管、取消、旧事件 | 测试夹具需 stub 新 props/expose | P01–P10 |
| `frontend/tests/session-takeover.test.mjs`（新增） | 覆盖控制器状态机 | 详情/确认/失败/取消/迟到/offset | Mock 与真实 client 边界 | T01–T18 |
| `frontend/tests/session-list.test.mjs` | 现有 HomePage/SessionManager 夹具在此 | 增加 H01–H09 | 测试夹具需更新 stub props/emits | H01–H09 |

## F.2 预计不修改

| 文件/模块 | 原因 |
|---|---|
| `frontend/src/terminal/client.ts` | S5 已提供 `connectTakeover`、`stop`、`running`、`completion`、身份隔离；S6 不需要新接口 |
| `frontend/src/terminal/output.ts` | 复用现有 tracker/offset/drain/epoch |
| `frontend/src/api/sessions.ts` | 详情 GET 可用现有 `request` + `sessionView`；无需新 wrapper |
| `frontend/src/styles/base.css` | 现有按钮/弹窗/表格/状态/提示/令牌足够；最多在 Pane scoped CSS 增加摘要面板 |
| `frontend/src/stores/auth.ts`、`router/index.ts` | 沿用 `logoutPending`/`identity` |
| 后端 `httpapi/sessions.go`、`session/*` | 列表/详情/takeover 路由已存在 |
| 数据库 migration、config、go.mod、package lock、依赖 | 无新增需求 |

## F.3 验证命令与当前状态

获准编码后执行：

```bash
cd frontend
npm test
npm run typecheck
npm run build
```

如后端未改动，不需要重跑 Go 测试；S8 全量回归时再执行 `go test -race ./...`。

当前状态：

- `frontend/node_modules` 不存在。
- 本次未安装依赖，因此未执行 `npm test` / `typecheck` / `build`。
- 本次只读检查；除新增本报告外，未修改任何生产代码、测试、既有文档、配置或样式。

---

# G. 阻塞问题与需要用户裁决的事项

## G.1 必须裁决：D3 候选 Pane 与“离开管理页取消未确认动作”的冲突

冻结文档存在一处需要明确解释的边界：

- D3=A：候选 Pane 承载详情/确认，确认后任务归 Pane。
- `stage-5-implementation-plan.md` §12.1：确认前“离开管理页取消查询/确认并移除未启动候选”。

当前 HomePage 的结构是：

- `view === 'sessions'` 时只显示 `SessionManager`；
- `view === 'servers'` 时显示终端 Pane；
- 二者通过 `v-if`/`v-show` 切换。

因此，用户从 SessionManager 点击“打开”后，要看到候选 Pane 的确认界面，必然要离开管理视图。如果按字面执行“离开管理页即取消候选”，候选会在用户确认前被移除，D3=A 无法完成。

**建议裁决：**

- 点击行“打开”视为把待确认动作从管理视图移交给候选 Pane；
- 管理视图卸载只取消列表 GET，不取消已移交的候选；
- 只有以下情况取消未确认候选：
  - 用户点击“取消接管”；
  - 关闭候选标签；
  - 整个 HomePage 卸载/登出/身份失效；
  - 用户明确点击“返回服务器”放弃该候选（可选）。
- 候选 Pane 在确认前不发 POST、不建 WS。

如果用户希望“离开管理页”严格取消候选，则需要改变交互（例如确认弹窗放在管理页内），这会接近 D3=B，需重新裁决。

## G.2 建议的默认实现选择（不阻塞，若无异议按此执行）

1. **在线 Pane 判定**：`phase === 'online' || phase === 'replaying'` 都视为“已有正常连接 Pane”，只定位，不接管；`replaying` 只是输入门禁未开，不是未连接。
2. **CONNECTING/CLOSING 行**：列表仍允许点击“打开”，由详情快照决定是否可确认；列表状态不作为准入权威。
3. **确认 UI**：使用现有 `Modal.vue`，与“关闭 SSH 会话”“删除服务器”保持一致；候选 Pane 内用 `.details`/`.notice` 展示摘要。
4. **列表操作完成刷新**：当前 `SessionManager` 在管理视图卸载后重新挂载并 `onMounted` 刷新，已满足“再次进入管理页刷新”；若后续改为 `v-show` 保持挂载，再增加显式 `ref.refresh()`。
5. **Recovery 互斥**：优先在 Pane 内 `recovery.invalidate()` + 门禁；如增加 `blocked` 谓词，仅作为防御性最小改动，不改变默认 attach 路径。

---

# 结论

- 已完成 S6 编码前只读检查。
- 本报告是本次唯一新增文件；未修改生产代码、测试代码、设计文档、配置或样式。
- 未开始 S6-C1，未实现 S7/S8，未执行 commit/push/merge/rebase，未安装或升级依赖，未部署。
- 当前唯一需要用户裁决的是 G.1 的“候选 Pane 移交后是否仍受‘离开管理页取消’约束”。其余差异均有明确的最小实施路径，等待人工审查与编码授权后再进入 S6-C1。
