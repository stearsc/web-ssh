# Web SSH 第五阶段 S5 设计审查报告

## 1. 范围、基线与结论

- 审查分支：`codex/第五阶段`
- 首轮审查基线 HEAD：`b1cb6d4306f5a19639ccfe45ff3a087a0b9880ae`；最终补充审查 HEAD：`adb62c4d3fbcf245c27f85ce981cbf302228d1d8`
- 审查对象：第五阶段 S5，即 `TerminalClient` 的 attach/takeover mode、连接代次、终止通知和异步生命周期。
- 本次只读取、分析和新增本报告；未修改业务代码、API、WebSocket 协议、冻结设计或阶段计划，未开始 S5 编码，未提交、推送或部署。
- 开工时工作区已有用户修改 `M luan-qi-ba-zao.txt`；本报告未触碰该文件。

结论：S5 可以在当前后端 S2/S3 契约和前端第四阶段恢复机制上实现，不需要修改后端、冻结协议或 S4 已验收代码。当前主要实现缺口是：

1. `TerminalClient` 的 Prepare 请求固定发送 `mode: "attach"`，没有由调用方显式指定初始 mode 的连接入口。
2. 每个长期恢复任务没有独立的“首次尝试已消费”标记。增加 takeover 后，若用 `retries === 0` 判断 mode，稳定资格重置重试次数时可能再次发送 takeover。
3. Close `4001` 已经停止输入和自动恢复，并保留有限队列，但当前只显示通用“连接已结束（4001）”；S5 应明确提示其接管关闭语义，同时保持 `WS_4001` 本地原因，不伪造收到 `taken_over` 业务消息。
4. `taken_over`、4001、旧 socket、旧 Prepare、stop/dispose 隔离已有基础实现，S5 应保留这些机制并用 takeover 场景补齐确定性测试。

S5 只提供连接层能力。用户如何选择 Session、确认接管、定位/停止 Pane、建立待接管标签，属于 S6；因此 S5 完成后可独立验证 TerminalClient，但用户无法通过页面完成端到端主动接管，直到 S6 接入。

## 2. 阅读的文档清单

本次核对以下文档及其相关章节、交叉引用和 D1–D3 裁决落点：

| 文档 | 核对内容 |
|---|---|
| `doc/web-ssh-design-development-plan.md` | Session 生命周期、API、attach/takeover 语义、错误码、关闭原因、前端所有权边界。重点核对 §6、§11.4、§11.6、§11.7、§14。 |
| `doc/web-ssh-websocket-protocol.md` | Prepare 与 bind、mode 由凭证决定、bind_success、输出顺序、错误与 Close code、`taken_over`/`closed`、Reader/Writer、单任务恢复协调器、必须验证时序。重点核对 §2、§3、§4.4、§4.5、§5、§7、§8。 |
| `doc/web-ssh-architecture-implementation-plan.md` | 锁顺序、Connection/Binding 生命周期、凭证和唯一提交点、操作准入、输出与终止槽、物理回收。重点核对 §3.1、§4、§5、§6、§7。 |
| `doc/web-ssh-implementation-stages.md` | 第四阶段恢复边界和第五阶段 S5 范围。重点核对 §6、§7。 |
| `doc/stage-5-implementation-plan.md` | S5 前后依赖、前端完整接管流程、后端交接、并发矩阵、关闭/登出、S5 文件职责、U11/F04、D1–D3 裁决。重点核对 §3、§8、§9、§10、§11、§12、§13、§15、§16、§17、§21。 |
| `doc/stage-4-implementation-plan.md` | 第四阶段 TerminalClient 单任务恢复、Promise、稳定资格、输出 tracker、stop/dispose、Close 回退和人工恢复边界。 |
| `doc/stage-4-implementation-report.md` | 已落地并验收的 attach、重试预算、旧回调隔离、列表与 Pane 生命周期行为。 |

冻结要求与本报告新增建议已分开标注。D1=A、D2=A、D3=A 是已确认裁决，本报告没有重新裁决或改变它们。用户随后已正式裁决采用方案 A：首次 takeover 失败后，同一长期任务继续按原预算恢复，后续所有 Prepare 固定 attach；若尚无新 bind_success，则使用用户已确认的 initialExpectedGeneration，见 §26。

## 3. 阅读的源码与测试清单

### 3.1 前端

- `frontend/src/terminal/client.ts`
  - `RecoveryTask`、`start`、`loop`、`prepareCredential`、`attempt`、`stop`、`dispose`。
- `frontend/src/terminal/output.ts`
  - `TerminalOutputTracker`、控制消息解析、`taken_over`/`closed` DTO、输入启用、冻结和有限排空。
- `frontend/src/terminal/recovery.ts`
  - `SessionRecovery.refresh`、`recover`、人工 attach 候选和旧查询隔离。
- `frontend/src/components/terminal/TerminalPane.vue`
  - TerminalClient/SessionRecovery 创建、连接状态、输入、停止、销毁和迟到 UI 回调隔离。
- `frontend/src/pages/HomePage.vue`
  - Pane 集合、页面生命周期、登出停止/销毁、S4 会话管理视图。
- `frontend/src/api/sessions.ts`
  - SessionView、Session 详情、关闭 Session 和活动列表 API。
- `frontend/src/api/management.ts`
  - 同源 Cookie、`no-store`、AbortSignal、API 错误解析和 `AUTH_REQUIRED` 回调。
- `frontend/src/stores/auth.ts`
  - 登录身份、`logoutPending`、过期处理。
- `frontend/src/session/list.ts`
  - S4 列表请求序号、Abort、身份和卸载隔离。
- `frontend/src/components/session/SessionManager.vue`
  - S4 列表生命周期；确认其不拥有 TerminalClient。
- `frontend/tests/terminal.test.mjs`
  - TerminalOutputTracker、TerminalClient、SessionRecovery、恢复预算、4001/4002、迟到回调和 stop/dispose。
- `frontend/tests/client.test.mjs`
  - 基础 HTTP 请求封装回归。
- `frontend/tests/terminal-pane.test.mjs`
  - 实际 Pane 的旧 socket 隔离、4001、`taken_over`、恢复和网络状态。
- `frontend/tests/session-list.test.mjs`
  - S4 列表生命周期和 TerminalPane 保持挂载的回归边界。

### 3.2 后端

- `backend/internal/session/binding.go`
  - `Prepare`、`canBind`、`Attach`、唯一提交点、旧 Binding 逻辑撤权、`CommitResult`、`Detach`。
- `backend/internal/session/send_admission.go`
  - `BindingDisposition`、`WithSendAdmission`。
- `backend/internal/session/session.go`
  - Session 状态、generation、快照和生命周期辅助。
- `backend/internal/websocket/connection.go`
  - Reader、Writer、发送准入、提交登记、统一 `Terminate`、`taken_over`、Close 和物理回收。
- `backend/internal/websocket/protocol.go`
  - 客户端 bind/resize DTO、严格解码、Close code 映射和 `taken_over` DTO。
- `backend/internal/websocket/service.go`
  - 连接登记、槽位和统一终止调用。
- `backend/internal/httpapi/sessions.go`
  - Prepare 请求、Session 列表、详情、关闭和 WebSocket 路由。
- `backend/internal/session/takeover_test.go`
  - U05–U09、R01–R05、凭证和原子替换。
- `backend/internal/websocket/takeover_test.go`
  - S3 的 W/R/T 真实 WS、Writer 准入、终止首因和资源回收。
- `backend/internal/websocket/connection_test.go`
  - bind、Reader/Writer、Close 和并发终止回归。
- `backend/internal/httpapi/sessions_test.go`
  - Prepare 请求形状、HTTP 错误和 Session API。

## 4. 当前 TerminalClient 连接与恢复流程

### 4.1 对象与所有权

`TerminalPane.vue` 为每个 Pane 创建一个 `TerminalClient` 和一个 `TerminalOutputTracker`。HomePage 只保存 Pane 引用，S4 `SessionListController` 不创建或驱动 TerminalClient。TerminalClient 当前持有一个长期 `RecoveryTask`，其字段包括：

- 任务 ID 和 `stopped`；
- `retries`；
- 首次连接 Promise；
- 长期 completion Promise。

`connect(expected)` 和 `run(expected)` 都进入同一个 `start(expected)`；已有活动任务时返回同一任务，不允许同一 Client 同时启动第二条恢复链。

### 4.2 单次尝试

`loop(task)` 当前按以下顺序执行：

1. `output.settle(false)`，等待上一轮输出写回调。
2. 读取 `processedOffset` 作为 `requested_start_offset`。
3. 发布 `preparing`。
4. `prepareCredential(task)` 请求一次性凭证。
5. 建立 WebSocket，发送首条 `bind`。
6. 校验 `bind_success.binding_generation === expected + 1`。
7. 校验回放范围和输出顺序。
8. 前缀完成后将 `generation` 更新为服务端确认的新代次，并结束首次 connection Promise。
9. 前缀完成且历史处理完成后允许输入。

当前 `prepareCredential` 固定发送：

```json
{
  "mode": "attach",
  "expected_generation": 0
}
```

这里的 `0` 是示意值；实际发送 `this.generation`。源码位置为 `frontend/src/terminal/client.ts:340-369`。

### 4.3 重试

当前 `retryCodes` 允许有限重试的 HTTP/WS 业务错误，包括 `BIND_BUSY`、`INVALID_CREDENTIAL`、`BIND_TIMEOUT`、`OUTPUT_GAP`、`SLOW_CLIENT`、`SERVER_SHUTTING_DOWN`、`INTERNAL_ERROR`、本地过载/终端失败、网络错误和容量类错误。

可重试结果发生后：

- 先冻结/丢弃或保留输出队列；
- 等待唯一在途终端写回调；
- 从排空完成开始计算 `reconnect_delay`；
- 同一任务最多执行配置数量的自动重试；
- 每次重试重新申请凭证，不复用旧凭证；
- 只有已通过校验的 `bind_success` 才更新 generation。

稳定资格只重置一次重试计数。当前逻辑不能使用 `retries === 0` 判断是否首次尝试，因为稳定资格可以把 `retries` 重新置零。

### 4.4 终止、输入和迟到回调隔离

`attempt()` 为每次 socket 建立独立闭包，并以以下条件判断回调是否仍有效：

- 本 attempt 未结束；
- 任务仍是当前任务且未停止；
- 当前 Client 未被销毁且页面/登录仍允许；
- `this.socket === socket`。

`finish()` 是单次尝试幂等结束入口。它会：

- 标记 attempt 结束；
- 消费稳定资格；
- 清除 bind timer；
- 冻结输出；
- 移除所有 socket handler；
- 清除当前 socket 和 finish 引用；
- 立即 `changed(false)`；
- 结束首次 connection Promise（若尚未结束）；
- 关闭旧 socket。

旧 xterm 回调另有 `terminalEpoch` 隔离。旧任务完成时还会检查任务身份，不能覆盖新任务 UI 状态。

### 4.5 SessionRecovery

`SessionRecovery` 当前只执行人工 attach：

1. 用户主动查询 Session 详情；
2. 仅接受本次查询的新 SessionView 作为候选；
3. 候选只能消费一次；
4. 先 `client.stop()`，完成后以候选的 `binding_generation` 调用 `client.connect()`；
5. 不自行创建 WebSocket，不维护第二套重试循环，不根据 Session 状态选择 takeover。

这与第四阶段职责一致。S5 应保持 `SessionRecovery.recover()` 默认 attach，不把主动 takeover 塞进这个入口。

## 5. 后端 takeover 契约核对

### 5.1 HTTP Prepare

`backend/internal/httpapi/sessions.go:21-24,72-86` 和 `backend/internal/session/binding.go:59-95` 确认：

- 请求体为 `{mode, expected_generation}`，两项必填；
- `mode` 只允许 `attach` 和 `takeover`；
- `expected_generation` 必须为安全非负整数；
- Prepare 只签发凭证，不替换旧 Binding，不改变 Session 状态或离线期限；
- 凭证内部保存登录 ID、Session ID、mode、expected generation 和到期时间。

### 5.2 WebSocket bind

`backend/internal/websocket/protocol.go:12-16` 的 bind DTO 只有：

```json
{
  "type": "bind",
  "credential": "一次性凭证",
  "requested_start_offset": 0
}
```

bind 没有 mode 字段，且解码器拒绝未知字段。因此浏览器不能在 WebSocket 阶段覆盖凭证中的 mode。实际提交行为由 `Session.Attach()` 读取凭证中的 `credential.mode` 决定。

### 5.3 提交与 generation

`backend/internal/session/binding.go:117-211` 确认：

- attach 在当前已绑定/在线时返回 `BIND_BUSY`；
- takeover 可针对 CONNECTED 或 DISCONNECTED；
- 两种 mode 都要求 `expected_generation` 等于提交时的当前 generation；
- 唯一提交点持操作门、登录读锁、Session 锁、Buffer 锁和凭证锁；
- 提交成功时消费同一 Session 的全部待用凭证；
- 旧 Binding 在 Session 锁内只标记 `active=false/superseded=true`，不提前取消 context；
- 新 Binding 的 generation 固定为旧 generation + 1；
- 旧 Connection 的终止和 Detach 在事务锁释放后执行。

### 5.4 taken_over 与 Close 4001

`backend/internal/websocket/protocol.go:68-84,120-124` 和 `connection.go:79-157,440-604` 确认：

- `taken_over` DTO 精确为 `{"type":"taken_over","message":string}`；
- 正常接管最终发送一次 `taken_over`，随后 Close `4001`；
- `taken_over` 由唯一 Writer 的独立终止槽发送；
- 如果旧端独立真实故障先固定首因，则故障原因优先，通知可能丢失；
- 通知可能因断网丢失，不能把收到 4001 当作已经收到业务消息；
- 客户端不得根据 4001 自动查询新 generation 并抢回。

### 5.5 当前前后端契约结论

后端已经满足 S5 所需契约，S5 不需要新增 API、错误码、WS 字段或 Close code。前端需要落实的是：

- 显式选择首次 mode；
- 同一任务首次尝试后固定使用 attach；
- 保持 generation 只由 `bind_success` 自动更新；
- 保持 `taken_over`/4001 停止恢复；
- 补齐裸 4001 的明确提示语义和确定性测试。

## 6. S5-1 至 S5-7 逐项审查

### 6.1 S5-1：mode 的来源与传递

**冻结要求**

- 调用方显式指定 `attach` 或 `takeover`；
- 不能根据 Session 状态猜 mode；
- HTTP Prepare 携带 mode；
- 凭证保存 mode；
- bind 不携带 mode；
- 客户端不能覆盖服务端凭证中的 mode。

**当前实现**

- HTTP 与后端契约正确；
- TerminalClient 固定发送 attach；
- 没有显式 takeover 入口。

**S5 需要实现**

- 增加类型明确的初始 mode；
- 保持 `connect(expected)`/`run(expected)` 默认 attach；
- 另增明确的主动 takeover 连接入口，或增加受字面量类型限制的 mode 参数；
- 初始 mode 固定在 RecoveryTask；
- 只有第一次 Prepare 使用任务初始 mode，后续全部 attach；
- WebSocket bind 仍只发送 credential 和 offset。

**连接结果如何区分**

`ConnectionResult` 不需要新增服务端错误码或 wire 字段。调用方在启动任务时已经知道初始 mode：从默认 `connect/run` 启动就是 attach，从显式 takeover 入口启动就是 takeover。该任务返回的 `connected=true` 只表示本次首次绑定前缀已经完成，不另外证明某个新的后端结果类型。若 S6 需要展示或记录动作种类，应由发起 takeover 的 Pane/页面动作上下文保存 mode，不能从 Session 状态猜测，也不能为 UI 方便新增协议字段。

**Agent 建议，尚未批准为具体 API**

保留现有 attach 外观，增加类似 `connectTakeover(expectedGeneration)`/`runTakeover(expectedGeneration)` 的显式入口，内部仍调用唯一的 `start(expected, mode)`。具体命名不是冻结协议，可在 S5 编码前由实现审查确定。

### 6.2 S5-2：expected_generation

**当前实现**

- `start(expected)` 设置任务 generation；
- 每次 Prepare 发送当前 generation；
- 每次 WS attempt 捕获 expected，只有 `bind_success === expected + 1` 才接受并更新；
- `BIND_CONFLICT` 不在 retryCodes 中，会停止；
- SessionRecovery 只使用用户最新查询详情中的 generation；
- 列表数据和旧 props 不反向更新 TerminalClient。

**结论**

当前 generation 机制基本完整。S5 只改变 mode，不应改变 generation 来源、校验或重试策略。

S5 必须禁止：

- 自动 GET 详情并替换 generation；
- 自动把 SessionView 最新 generation 用于 takeover；
- 在 `bind_success` 丢失时猜测已提交并自增；
- 把 `BIND_CONFLICT` 当网络错误重试。

### 6.3 S5-3：attach 与 takeover 恢复策略

**Attach**

保持第四阶段行为：网络断开、OUTPUT_GAP 和其他冻结可重试错误继续在同一个有限任务中重新申请 attach，并只使用已成功获知的 generation。

**Takeover**

- 只能由调用方在用户授权后启动；
- 第一次 Prepare 使用 takeover；
- 该次 attempt 本身不能中途改成 attach；
- 同一任务的后续自动尝试必须是新的 attach 尝试；
- 不得再次自动发送 takeover；
- 不得在 bind_success 丢失后自动查询新代次；
- takeover 成功后才把 connection Promise 视为首次绑定前缀成功。

这里需要区分两句话：

- “takeover 失败不能把该次请求静默降级为 attach”；
- 第五阶段计划同时规定“同一任务的下一轮自动重试固定 attach”。

用户已正式确认方案 A，与 `doc/stage-5-implementation-plan.md` §8.1、§8.2 和 F04 一致：首次 takeover attempt 失败后，同一长期任务可按原预算继续，但后续独立 attempt 固定 attach。这里不是把失败的 takeover 请求改写成 attach，而是消费首次 mode 后进入新的恢复 attempt；不得再次自动 takeover，也不得自动刷新 generation。

### 6.4 S5-4：taken_over 与 Close 4001

**当前已实现**

- `parseControl()` 严格校验 `taken_over` DTO；
- 收到 `taken_over` 后 `keepQueued=true`，`retry=false`；
- 收到裸 4001 后 `keepQueued=true`，不进入 retryCodes；
- `finish()` 立即清除 socket、撤销输入和发布权限；
- 已接受队列在 `write_drain_timeout` 内有限排空；
- 后续旧 close/message 因 handler 已移除、attempt 已结束、socket 身份不匹配而被忽略；
- `taken_over` 与后续 close 竞争时，首个 `finish()` 固定结果。

**仍需 S5 补齐**

- 裸 4001 的 `RecoveryState.message` 目前是通用“连接已结束（4001）”，没有明确告诉用户“已被其他连接接管”；
- 应保留本地原因 `WS_4001`，只补充明确、稳定的用户提示；
- 不应把裸 4001 改成 `taken_over` 业务 code，也不能伪造服务端消息已到达。

### 6.5 S5-5：连接代次与异步回调隔离

当前隔离机制足以作为 S5 基线：

- 一个 TerminalClient 同时只有一个活动 RecoveryTask；
- 一个任务同时只有一个活动 attempt；
- 一个 attempt 同时只有一个 socket 和 finish；
- 所有异步回调检查 task/socket/sequence/allowed/disposed；
- 旧 Prepare 响应在 Abort 后不创建 WS；
- stop 同步标记任务 stopped；
- dispose 同步封闭后续 run；
- output tracker 用 terminalEpoch 隔离旧 write 回调。

S5 不得用新的全局任务管理器替换这些机制。新增 mode 只能成为任务局部不可变初始值和一个一次消费闩锁，不能成为第二套 socket/恢复状态。

### 6.6 S5-6：状态机和错误语义

当前状态集合为：

`idle → preparing → connecting → replaying/online → retry_wait → stopping → stopped/disposed`

S5 增加的是任务级“初始 mode”和“首次尝试是否已消费”，不需要增加服务端错误码或新的全局状态框架。

关键规则：

- 首次 Prepare 使用任务初始 mode；
- 首次尝试在 Prepare 前消费 mode 闩锁；
- 所有后续 Prepare 使用 attach；
- `bind_success` 才更新 generation；
- `taken_over` 和裸 4001 停止自动恢复；
- `OUTPUT_GAP`/4002 仍有限恢复；
- `BIND_CONFLICT` 停止；
- stop/dispose 覆盖迟到网络结果；
- 认证/权限错误按现有错误表显示，不改成 NETWORK_ERROR。

### 6.7 S5-7：TerminalClient 与 SessionRecovery 职责

建议并符合当前设计的所有权如下：

| 能力 | 所有者 |
|---|---|
| 初始 mode | 发起任务的调用方显式传入；TerminalClient 固定到 RecoveryTask。 |
| attach 自动恢复 | TerminalClient 的单一 loop。 |
| 每次凭证、socket、generation 和重试预算 | TerminalClient。 |
| 用户主动 takeover 的意图/确认 | S6 Pane/页面控制器；不属于 S5。 |
| 人工 attach 候选与详情查询 | SessionRecovery。 |
| `taken_over`/4001 连接层处理 | TerminalClient。 |
| 被接管后的 Pane 展示 | TerminalPane 读取 TerminalClient 的 RecoveryState；S6 不另建连接状态。 |
| 是否允许重新接管 | 只由用户新的主动操作和 S6 流程决定；任何自动恢复都不得 takeover。 |

SessionRecovery 不需要识别 `taken_over` 控制消息，也不需要保存“被接管”状态。它只需知道当前 TerminalClient 已不 running，并在用户再次明确查询/人工 attach 时按原有候选流程工作。S6 的主动接管必须调用 TerminalClient 的显式 takeover 入口，不能复用 SessionRecovery 的默认 recover。

## 7. 六个时序图

### 7.1 T1：正常 attach

```mermaid
sequenceDiagram
    participant P as Pane
    participant C as TerminalClient
    participant H as HTTP API
    participant W as WebSocket
    participant S as Session

    P->>C: connect(expected G)，默认 attach
    C->>C: 建立唯一 RecoveryTask，记录 initialMode=attach
    C->>H: POST /takeover {mode:attach, expected_generation:G}
    H-->>C: credential（服务端保存 mode=attach）
    C->>W: GET /ws/sessions/{id}
    C->>W: bind {credential, requested_start_offset}
    W->>S: Attach；按凭证 mode 校验并提交
    S-->>W: 新 Binding generation G+1
    W-->>C: bind_success(G+1, replay range)
    C->>C: 校验 G+1；更新 generation
    C-->>P: connection Promise connected=true（前缀完成）
    W-->>C: history/live output
    C->>C: tracker 写 xterm；输入在 replay 完成后启用
```

### 7.2 T2：attach 网络断开与恢复

```mermaid
sequenceDiagram
    participant C as TerminalClient
    participant O as OutputTracker
    participant H as HTTP API
    participant W1 as 旧 WebSocket
    participant W2 as 新 WebSocket

    W1--x C: onclose 1006 / NETWORK_ERROR
    C->>C: 结束当前 attempt，撤销输入
    C->>O: freeze(false)，保留唯一在途 write
    O-->>C: processedOffset
    C->>C: retry_wait；首次 mode 闩锁已经消费
    C->>H: 下一轮固定 POST {mode:attach, expected_generation:confirmedGeneration C}
    H-->>C: 新 credential
    C->>W2: 新 WS + bind
    W2-->>C: bind_success(C+1)
    C->>C: 只接受 C+1；更新 confirmedGeneration；不会改为 takeover
```

### 7.3 T3：显式 takeover 的连接层流程

```mermaid
sequenceDiagram
    participant Caller as S6 调用方（S5 不实现 UI）
    participant C as TerminalClient
    participant H as HTTP API
    participant W as WebSocket
    participant S as Session

    Caller->>C: 显式 start/connectTakeover(expected G)
    C->>C: 新 RecoveryTask；initialMode=takeover
    C->>C: 首次尝试消费 mode 闩锁
    C->>H: POST {mode:takeover, expected_generation:G}
    H-->>C: credential（mode=takeover）
    C->>W: bind（消息中无 mode）
    W->>S: 按凭证 mode 提交，替换旧 Binding
    S-->>W: 新 Binding G+1
    W-->>C: bind_success(G+1)
    C->>C: 校验成功；任务后续 Prepare 固定 attach
    C-->>Caller: connection Promise connected=true
```

### 7.4 T4：旧 TerminalClient 被 takeover

```mermaid
sequenceDiagram
    participant N as 新 TerminalClient
    participant S as Session
    participant O as 旧 Connection
    participant A as 旧 TerminalClient
    participant X as 旧 xterm/队列

    N->>S: takeover 提交，发布 generation G+1
    S->>O: 锁外请求 taken_over
    O-->>A: {"type":"taken_over","message":...}
    O-->>A: Close 4001
    A->>A: finish 幂等；retry=false
    A->>X: 立即停输入，keepQueued=true，有限排空
    Note over N,A: 新 Binding 已有效；旧 socket/task 回调不能修改新任务
```

### 7.5 T5：taken_over 通知丢失，只收到 4001

```mermaid
sequenceDiagram
    participant S as Session
    participant O as 旧 Connection
    participant A as 旧 TerminalClient
    participant U as Pane

    S->>O: 旧 Binding 已被替换
    O--xA: taken_over 因网络/关闭丢失
    O-->>A: Close 4001
    A->>A: 本地原因 WS_4001；retry=false
    A->>A: 撤销输入；keepQueued=true；有限排空
    A-->>U: 明确“已被其他连接接管”提示
    Note over A,U: 不自动 GET 新 generation，不自动 attach/takeover 抢回
```

### 7.6 T6：takeover 失败

下图按用户已确认的方案 A 统一：只有第一次 Prepare 使用 takeover；错误可恢复且预算未耗尽时，后续独立 attempt 固定 attach。

```mermaid
sequenceDiagram
    participant C as TerminalClient
    participant H as HTTP API
    participant W as WebSocket

    C->>H: 首次 POST {mode:takeover, expected_generation:G}
    alt Prepare 返回 BIND_CONFLICT / 不可重试错误
        H-->>C: 错误
        C-->>C: 停止任务；不查询新 G，不再次 takeover
    else Prepare 可重试失败或 WS bind 失败
        H-->>C: 可重试错误 / WS 无有效绑定
        C->>C: 结束首次 takeover attempt；首次 mode 已消费
        alt 已有 bind_success 确认的 generation C
            C->>H: 同一任务下一轮 POST {mode:attach, expected_generation:C}
        else 尚无新的 bind_success
            C->>H: 同一任务下一轮 POST {mode:attach, expected_generation:G}
            Note over C: G 是本次操作已确认的 initialExpectedGeneration
        end
        H-->>C: attach 结果；若 G 已过期则可能 BIND_CONFLICT 后停止
        Note over C: 后续只会 attach；不得再次 takeover，也不得推算 G+1
    end
```

## 8. 状态机与错误处理表

### 8.1 状态转换

| 当前状态 | 事件 | 下一状态 | 约束 |
|---|---|---|---|
| idle | 用户/调用方启动 | preparing | 显式 attach 或 takeover；同一 Client 不能启动第二任务。 |
| preparing | 凭证成功 | connecting | 迟到/取消响应不创建 WS。 |
| connecting | bind_success 前缀完成 | replaying/online | 只接受 expected+1；replay 未完成时禁用输入。 |
| replaying | tracker 完成回放 | online | 不提前启用输入。 |
| online | 可重试 HTTP/WS/网络错误 | retry_wait | 下一轮固定 attach。 |
| retry_wait | delay 结束 | preparing | 稳定资格可重置 retries，但不能重置 mode 闩锁。 |
| online/replaying | taken_over/4001 | 停止流程 | 不重试；立即禁输入；有限保留队列。 |
| online/replaying | OUTPUT_GAP/4002 | retry_wait | 丢未提交输出；有限 attach 恢复。 |
| preparing/connecting | BIND_CONFLICT | stopped | 不自动查询或更换 generation。 |
| preparing/connecting/online | AUTH_REQUIRED/PERMISSION_DENIED | stopped | 认证/权限语义，不伪装网络错误。 |
| 任意活动态 | stop | stopping | 同步封闭新请求和新 socket。 |
| 任意活动态 | dispose | stopping/disposed | 阻止所有迟到回调和后续 run。 |

### 8.2 错误处理

| 错误/关闭 | 当前动作 | S5 目标 | 说明 |
|---|---|---|---|
| `BIND_BUSY`、`INVALID_CREDENTIAL`、`BIND_TIMEOUT` | 有限重试 | 下一轮 attach | 不重用旧凭证，不自动换代。 |
| `OUTPUT_GAP` / 4002 | 丢未提交输出，有限重试 | 下一轮 attach | 不与 4001 混淆。 |
| `SLOW_CLIENT` / 1013 | 有限重试 | 下一轮 attach | 保持原队列/预算规则。 |
| `SERVER_SHUTTING_DOWN` / 1012 | 有限重试 | 下一轮 attach | 最终由用户重试。 |
| `INTERNAL_ERROR` / 1011 | 有限重试 | 下一轮 attach | 不重放旧凭证。 |
| `CLIENT_OVERLOAD`、`CLIENT_TERMINAL_ERROR`、`CLIENT_BIND_TIMEOUT`、`NETWORK_ERROR` | 有限重试 | 下一轮 attach | 保持 tracker 清理/重建规则。 |
| `CREDENTIAL_LIMIT`、`CONNECTION_LIMIT_REACHED` | 有限重试 | 下一轮 attach | 容量错误不改变 generation。 |
| `BIND_CONFLICT` | 停止 | 停止 | 不得自动 GET 新 generation。 |
| `AUTH_REQUIRED` | 停止并通知认证过期 | 停止 | 保持现有 auth 行为。 |
| `PERMISSION_DENIED` | 停止 | 停止 | 不得变成网络重试。 |
| `INPUT_BACKPRESSURE` | 停止 | 停止 | 明确输入可能未全部发送，不补发。 |
| `INVALID_MESSAGE`、`INVALID_OFFSET`、`INVALID_SIZE`、`ALREADY_BOUND`、`MESSAGE_TOO_LARGE` | 停止 | 停止 | 协议错误不重试。 |
| `SESSION_NOT_FOUND`、`SESSION_NOT_READY` | 停止并保留诊断 | 停止 | Pane 可查询一次状态；不自动 takeover。 |
| `taken_over` 业务消息 | 停止、保留有限队列 | 明确接管提示 | 不重试。 |
| Close 4001 | 停止、保留有限队列 | 明确接管关闭提示，保留 `WS_4001` 本地原因 | 不伪造业务消息。 |
| `closed` 业务消息 | 停止、保留有限队列 | 保持 | 表示 SSH Session 实际关闭。 |
| Close 1000 | 停止 | 保持 | 不能单凭 1000 推断 Session 状态。 |
| Close 1006/1011/1012/1013 | 有限重试 | 下一轮 attach | 不自动 takeover。 |

## 9. 设计风险审查

### 9.1 attach 恢复被 takeover mode 污染

- 当前代码是否存在：当前没有 takeover，因此无现有污染；增加 mode 后有实现风险。
- 相关位置：`TerminalClient.start/loop/prepareCredential`。
- 风险时序：一个 takeover 任务成功后断网，下一轮仍读取任务初始 takeover。
- 处理：初始 mode 和“首次尝试已消费”分开保存；后续固定 attach。
- 是否 S5 必修：是。

### 9.2 自动重复 takeover

- 当前代码是否存在：当前固定 attach，因此不会重复 takeover，但 takeover 能力缺失。
- 相关位置：`RecoveryTask`、`loop`、`prepareCredential`。
- 风险时序：首次 takeover 返回 INVALID_CREDENTIAL/BIND_TIMEOUT 或 bind_success 丢失，循环再次读取 takeover。
- 处理：首次 Prepare 前消费 mode 闩锁；后续只 attach。
- 是否 S5 必修：是。

### 9.3 Close 4001 被当普通网络故障

- 当前代码是否存在：基础分类已正确，4001 不进入自动恢复；提示语义不足。
- 相关位置：`client.ts:507-516`、`TerminalPane.vue:stoppedState`。
- 风险时序：通知丢失，只剩 4001，用户看到通用关闭码，难以判断已被接管。
- 处理：保留 `WS_4001`，将消息改为明确接管关闭原因；不重试。
- 是否 S5 必修：是，作为最小状态反馈修正。

### 9.4 taken_over 后仍允许输入

- 当前代码是否存在：否。
- 相关位置：`TerminalClient.finish`、`canInput`、`send`。
- 风险时序：`taken_over` 到达后旧队列尚在排空，用户继续输入。
- 处理：`finish` 先清 socket 和 `changed(false)`，再排空；测试立即断言 `canInput=false`、send 不产生网络写。
- 是否 S5 必修：保留并补 takeover 测试。

### 9.5 旧 WebSocket 回调污染新连接

- 当前代码是否存在：受 socket 身份、attempt ended、task identity 和 handler 清理保护，当前不存在已知缺陷。
- 相关位置：`TerminalClient.attempt/start/publish`。
- 风险时序：旧 close/error/message 在新 socket 建立后迟到。
- 处理：保持现有闭包和身份校验；S5 增加 takeover 成功后旧回调测试。
- 是否 S5 必修：保留机制，测试必须覆盖。

### 9.6 Prepare 迟到后创建过期 WebSocket

- 当前代码是否存在：已受 AbortSignal、controller identity、task admitted 检查保护。
- 相关位置：`prepareCredential`、`loop`、`attempt`。
- 风险时序：stop/dispose/旧任务取消后 fetch 才返回。
- 处理：保持现有检查；mode 只影响请求体，不改变该边界。
- 是否 S5 必修：回归测试。

### 9.7 generation 被列表或详情自动刷新

- 当前代码是否存在：否。列表控制器与 TerminalClient 独立；SessionRecovery 只在用户明确 recover 时传候选代次。
- 相关位置：`session/list.ts`、`SessionRecovery.recover`、`TerminalClient.attempt`。
- 风险时序：takeover 失败后为了“帮助”用户自动查询并换代。
- 处理：禁止。generation 自动更新只能来自已验证的 `bind_success`。
- 是否 S5 必修：作为不可越界约束。

### 9.8 takeover 失败后被自动恢复机制重复 takeover

- 当前代码是否存在：无 takeover，故尚未发生；S5 实现时是主要风险。
- 相关位置：`retry_wait`、`loop`、`prepareCredential`。
- 风险时序：稳定资格重置 `retries=0`，实现误把零值当首次尝试。
- 处理：独立 `initialModeConsumed` 闩锁，不能依赖 retry 数。
- 是否 S5 必修：是。

### 9.9 TerminalClient 与 SessionRecovery 双重恢复

- 当前代码是否存在：否。TerminalClient 拥有唯一循环；SessionRecovery 只在人工 attach 时启动一个新任务。
- 相关位置：`client.ts:270-339`、`recovery.ts:96-119`。
- 风险时序：S6 为了 takeover 另起一个 socket/重试循环。
- 处理：S5 只扩展 TerminalClient；S6 必须调用同一 Client，不能创建第二套连接所有者。
- 是否 S5 必修：定义边界并在 S5 API 中支持；完整 UI 留 S6。

### 9.10 S4 管理列表生命周期影响 TerminalClient

- 当前代码是否存在：否。
- 相关位置：`SessionListController`、`SessionManager.vue`、HomePage `v-show` Pane。
- 风险时序：列表刷新/卸载错误停止终端，或列表数据直接替换 Pane 对象。
- 处理：S5 不修改 S4 控制器；列表只能提供后续 S6 动作数据。
- 是否 S5 必修：只做回归，不修改。

### 9.11 replay、processedOffset、OUTPUT_GAP 和预算被破坏

- 当前代码是否存在：现有 tracker/恢复测试已覆盖主要行为，但尚无 takeover 场景。
- 相关位置：`TerminalOutputTracker`、`TerminalClient.loop`。
- 风险时序：新增 mode 时顺手重构恢复循环或输出状态。
- 处理：只增加任务 mode 字段和闩锁；不改 tracker、offset、稳定资格、重试预算和 4002 分类。
- 是否 S5 必修：通过回归测试证明未破坏。

### 9.12 stop/dispose 与连接建立竞争

- 当前代码是否存在：已有多项保护和测试。
- 相关位置：`stop`、`dispose`、`prepareCredential`、`attempt`、`finish`。
- 风险时序：takeover Prepare/WS 建立时用户停止、登出或销毁 Pane。
- 处理：保持同步停止标志、Abort、finish、output dispose 和 allowed/disposed 检查；补 takeover 专项测试。
- 是否 S5 必修：回归并扩展。

## 10. 编码拆分方案

S5 不应拆成多个会产生“可创建 takeover 凭证但客户端没有安全 mode 闩锁”之类的中间版本。建议采用一个完整实现单元加一个验证收口单元。

### 10.1 S5-C1：TerminalClient 完整连接层单元

必须同时完成：

- 显式初始 mode 类型和入口；
- 默认 attach 外观；
- RecoveryTask 初始 mode；
- 首次尝试已消费闩锁；
- Prepare body 的 mode；
- 后续自动 Prepare 固定 attach；
- generation 来源和 `bind_success` 校验不变；
- `taken_over`/4001 停止和有限排空不变；
- 裸 4001 明确提示；
- 任务/socket/Prepare 迟到回调隔离不变。

完成后的运行状态：

- 所有现有 attach 调用行为保持不变；
- 代码层可以显式发起一次 takeover；
- 首次 takeover 后不会自动重复 takeover；
- 没有 S6 UI 也能通过 TerminalClient 单测验证；
- 不修改 SessionRecovery 默认 attach。

建议改动集中在 `frontend/src/terminal/client.ts` 和 `frontend/tests/terminal.test.mjs`。若类型必须从 `client.ts` 导出，仍属于同一单元，不新增生产文件。

### 10.2 S5-C2：确定性与回归验证单元

只增加测试和验证，不扩展 S6/S7 行为：

- attach 全量回归；
- takeover 首轮/后续 attach；
- Prepare/WS 失败和 bind_success 丢失；
- `taken_over`、裸 4001、连续/重复通知；
- stop/dispose、旧 socket/Prepare 迟到；
- 稳定资格重置不重置 mode 闩锁；
- 实际 Pane 状态回归。

如果 C2 发现生产缺陷，应回到 C1 的最小实现修正并重跑全部测试，不能把修复分散到无关文件。

### 10.3 验收顺序

1. 基线：记录 Git 状态，确认只有用户已有的 `luan-qi-ba-zao.txt` 修改。
2. 完成 S5-C1 与核心测试。
3. 执行前端完整测试和类型检查/生产构建。
4. 完成 S5-C2 的竞争与终止测试。
5. 再次执行前端完整测试、生产构建和必要的后端回归。
6. 审查 diff 仅含 S5 允许文件。
7. 停止并提交审查报告；不自动进入 S6。

## 11. 测试矩阵

测试应继续使用可控 fetch、Mock WebSocket、可控 Promise、显式 resolve/reject 和 mock timers，不使用固定 sleep。

### 11.1 Attach 回归

| ID | 场景 | 关键断言 |
|---|---|---|
| A01 | 正常 attach | 首个 POST body 为 `{mode:"attach", expected_generation:G}`；WS bind 无 mode；只接受 G+1；connection Promise 在前缀完成时 true。 |
| A02 | attach 网络断开恢复 | 1006 后同一任务有限 retry_wait；下一次 POST 仍 attach；使用已确认 generation；不新增 Session/SSH。 |
| A03 | OUTPUT_GAP/4002 | 丢未提交队列，等待在途 write，下一轮 attach；预算耗尽后停止。 |
| A04 | attach 被他人 takeover 后冲突 | 原代次 attach 得到 BIND_CONFLICT；停止；不自动查询新 generation，不发起 takeover。 |
| A05 | 默认入口与 SessionRecovery | `connect()/run()` 和 `SessionRecovery.recover()` 仍只 attach。 |

### 11.2 Takeover

| ID | 场景 | 关键断言 |
|---|---|---|
| T01 | 正常 takeover | 首次且仅首次 POST mode=takeover；WS bind 不含 mode；接受 G+1；connection Promise true；后续 Prepare 固定 attach。 |
| T02 | takeover Prepare 不可重试失败 | BIND_CONFLICT 等停止；请求数不增加；无自动 GET generation。 |
| T03 | takeover Prepare 可重试失败 | 首次 takeover 结束；若已有 confirmedGeneration C，下一轮 POST 为 attach、expected_generation=C；否则使用 initialExpectedGeneration G；之后不再出现 takeover；G 已过期时 BIND_CONFLICT 停止。 |
| T04 | WS bind BIND_BUSY/BIND_TIMEOUT/INVALID_CREDENTIAL | 结束 takeover attempt；后续 attach 优先使用最新 confirmedGeneration C，尚无 C 时使用 initialExpectedGeneration G；不重用旧凭证；冲突时停止。 |
| T05 | takeover 提交结果不确定或 bind_success 丢失 | 不把 G+1 视为已确认；后续 attach 使用最新 confirmedGeneration，若尚无新确认值则使用 initialExpectedGeneration G；G 已过期时 BIND_CONFLICT 停止；不查询新代次。 |
| T06 | 稳定资格重置 | takeover 成功后断网、稳定资格被消费/重置 retries，后续仍 attach；mode 闩锁不受 retries 影响。 |
| T07 | takeover 成功后再次断网 | 同一个长期任务继续，但下一轮 attach；不得重新 takeover。 |

### 11.3 taken_over 与 Close 4001

| ID | 场景 | 关键断言 |
|---|---|---|
| D01 | 只收到 `taken_over` | 立即 canInput=false；后续 send 无网络写；retry=false；保留有限队列；提示服务端 message。 |
| D02 | 只收到 Close 4001 | 本地 code 为 `WS_4001`；提示明确接管关闭；retry=false；有限保留队列。 |
| D03 | 先 `taken_over` 再 4001 | 只完成一次；原因不被 4001 覆盖；不重试；不重复排空。 |
| D04 | 重复 `taken_over`/重复 close | handler 清空和 attempt 幂等使后到事件无效；请求/socket 数不增加。 |
| D05 | taken_over 到达时存在已接受队列 | keepQueued=true；在途 write 完成或 drain timeout 后结束；不联网抢回。 |
| D06 | 4001 通知丢失 | 仍停止自动恢复；不自动 GET generation；不自动 attach/takeover 抢回。 |
| D07 | taken_over 先于 bind_success | connection Promise false 且 code 为 taken_over；不重试；不伪造绑定成功。 |

### 11.4 异步竞争与资源隔离

| ID | 场景 | 关键断言 |
|---|---|---|
| C01 | Prepare 迟到 | stop/abort 后即使 fetch 返回，也不创建 WS；旧 controller 不清理新 controller。 |
| C02 | 多次 connect | 同一任务返回同一 Promise/完成态；只产生一个 POST 链和一个活动 socket。 |
| C03 | 旧 onopen/message/close 迟到 | 旧 attempt 和新任务均不被修改；旧 socket 不再发送 bind/input。 |
| C04 | 旧任务 completion 迟到 | sequence/task identity 阻止覆盖新任务状态。 |
| C05 | dispose 与连接建立竞争 | 不创建替代 socket/terminal；后续回调不更新 notice/state；重复 dispose 幂等。 |
| C06 | 旧 xterm write 回调迟到 | terminalEpoch 阻止推进新实例 offset。 |
| C07 | S4 列表刷新与 Pane | 列表请求不创建 TerminalClient、不停止 Pane、不修改 generation。 |

### 11.5 实际 Pane 集成

| ID | 场景 | 关键断言 |
|---|---|---|
| P01 | Pane 收到 taken_over | 输入禁用，状态提示明确，socket 数不因自动恢复增加。 |
| P02 | Pane 收到裸 4001 | 输入禁用，提示接管关闭语义，不自动重连。 |
| P03 | 旧 Pane socket 事件在新任务后迟到 | 当前 Pane 状态、notice、socket 和 generation 不变。 |
| P04 | stop/dispose 后请求返回 | 页面无新提示、无新连接、无终端重建。 |

## 12. 用户裁决与当前状态

D1=A、D2=A、D3=A 已确认。针对首次 takeover 失败后的恢复策略，用户已正式确认方案 A：

- 首次 takeover attempt 失败后，同一长期任务继续按原有有限恢复预算运行；后续 attach 优先使用最新 bind_success 确认值，尚无新确认值时使用 initialExpectedGeneration；
- 后续独立恢复 attempt 固定使用 attach；
- 不得再次自动 takeover，不得自动刷新或替换 generation；
- `BIND_CONFLICT`、taken_over、4001、预算耗尽等原停止条件不变；
- 首次 connection Promise 可以已经 resolve false，后续 attach 成功只通过 RecoveryState 表达。

该裁决与冻结实施计划一致，本轮不修改冻结文档。S5 设计层已无等待用户裁决的事项；实际 S5-C1/C2 编码仍须等待用户单独授权。

## 13. 明确的 S5 实施范围

S5 编码获准后只应实现：

1. TerminalClient 接收显式初始 attach/takeover mode。
2. 保持 `connect/run` 默认 attach。
3. 首次 Prepare 使用任务初始 mode，之后固定 attach。
4. 首次尝试闩锁独立于 retries/稳定资格。
5. expected_generation 来源、传递和 `bind_success` 校验不变。
6. `taken_over` 和 Close 4001 停止输入/自动恢复，有限排空。
7. 裸 4001 的明确用户提示。
8. 旧 socket、旧 Prepare、旧任务和 dispose 后迟到回调隔离。
9. 必要的 TerminalClient、SessionRecovery 回归和 Pane 状态测试。

S5 不需要：

- 修改后端；
- 修改 API、协议 DTO、错误码或 Close code；
- 修改 output tracker 算法；
- 修改 SessionRecovery 默认人工 attach；
- 修改 S4 列表控制器；
- 增加全局 store、事件总线、轮询或第二套恢复循环。

## 14. S6–S8 禁止越界范围

### S6

S5 不实现：

- Session 列表“主动接管”按钮；
- 接管确认弹窗；
- HomePage 的 Pane 预占/定位；
- 停止恢复中任务并等待收尾的 UI 协调；
- 创建待接管 Pane、确认后任务归属和跨标签协调；
- 接管完成后的列表刷新触发；
- 用户从 SessionManager 到 TerminalClient 的端到端接线。

S5 只提供连接层 API 和状态能力；S6 才能发起真实用户 takeover 操作。

### S7

S5 不修改：

- Session 关闭流程；
- 登出流程；
- `logoutPending`；
- HomePage 退出资源回收；
- 第四阶段 stop/dispose 生命周期；
- 跨页面异步任务转移。

### S8

S5 不进行：

- 第五阶段整体集成；
- 全链路真实部署验收；
- 冻结文档重写；
- 与 S5 无关的重构。

## 15. 预计修改文件清单与建议验收顺序

### 15.1 预计修改

| 文件 | 预期用途 | 是否确定 |
|---|---|---|
| `frontend/src/terminal/client.ts` | 显式初始 mode、首次尝试闩锁、后续 attach、4001 提示、保持现有生命周期。 | 必需 |
| `frontend/tests/terminal.test.mjs` | TerminalClient/tracker 的 S5 单元、失败和竞争测试。 | 必需 |
| `frontend/tests/terminal-pane.test.mjs` | 实际 Pane 的 taken_over/4001/迟到回调/状态测试。 | 很可能需要 |
| `doc/测试报告/stage-5-s5-test-report.md` | S5 编码后的实施/测试报告。 | 编码阶段新增 |

不预计修改：

- `frontend/src/terminal/output.ts`；
- `frontend/src/terminal/recovery.ts`；
- `frontend/src/components/terminal/TerminalPane.vue` 的业务结构；
- `frontend/src/pages/HomePage.vue`；
- `frontend/src/api/*`；
- `frontend/src/session/list.ts`；
- `frontend/src/components/session/SessionManager.vue`；
- `backend/**`；
- 冻结设计/协议/阶段计划。

如果实现时发现必须修改上述“不预计”文件，应先说明触发条件、最小变更和阶段边界，再取得相应授权。

### 15.2 建议验收顺序

1. 先锁定 attach 基线，证明默认入口和 SessionRecovery 不回归。
2. 完成显式 takeover 入口和首次 mode 闩锁。
3. 验证 takeover 成功、失败、bind_success 丢失和后续 attach。
4. 验证 taken_over/4001、有限排空、输入禁用和旧回调隔离。
5. 验证 stop/dispose、Prepare 迟到、多次 connect 和稳定资格重置。
6. 运行前端完整测试、类型检查和生产构建。
7. 运行必要的后端回归、Go Race、Vet 和 Build，确认 S5 没有意外后端影响。
8. 检查 diff 仅属于 S5；保存原始日志和测试报告。
9. 停止，等待人工审查；不得自动开始 S6。

## 16. 设计冲突与剩余空白

原冻结设计、协议和架构在首次 takeover 后续自动 attach 上保持一致。用户已确认方案 A，上一轮临时争议已经闭合：

- 后端 mode、凭证、WS bind 和原子替换契约一致；
- 前端 4001/4002 分类与冻结协议一致；
- TerminalClient 单任务恢复、SessionRecovery 人工 attach 和 S4 列表所有权可分离；
- 首次 takeover 失败后，同一任务后续只 attach；
- S5 依赖 S6 才能提供用户可操作的端到端接管流程，这是阶段边界而非设计冲突。

仍需在 S5 编码时闭合的实现空白：

1. 初始 mode API 尚未存在；
2. 首次 mode 闩锁尚未存在；
3. 活动任务缺少不可变 mode/初始 expected_generation 参数身份，当前会在校验新参数前复用任务；
4. stop 请求后拒绝新任务已有同步保护，但缺少跨 mode/generation 参数冲突的明确错误语义和测试；
5. 裸 4001 的用户提示尚未明确；
6. 现有测试没有 takeover 首轮/后续 attach、bind_success 丢失、稳定重置后的 mode、任务参数冲突和首次失败后恢复成功的确定性回归；
7. S6 尚未接入，因此不能宣称第五阶段主动接管功能已完整可用。

## 17. Git 与操作状态

审查开始时：

```text
 M luan-qi-ba-zao.txt
```

- 暂存区为空；
- 本报告之外未修改业务文件；
- 未执行 commit、push、merge、rebase、reset、clean 或部署；
- 未触碰用户已有的 `luan-qi-ba-zao.txt`。

本报告完成后应仅新增：

```text
doc/测试报告/stage-5-s5-design-review.md
```

本次审查不代表 S5 编码授权。S5 实施必须等待用户对报告和后续编码范围作出明确授权。

## 18. S5 补充审查范围与基线

本轮补充审查日期为 2026-11-18，分支为 `codex/第五阶段`，实际读取时 HEAD 为 `adb62c4d3fbcf245c27f85ce981cbf302228d1d8`。工作区保留用户已有修改 `M luan-qi-ba-zao.txt`，本轮未触碰该文件。

实际重新读取的对象包括：

- `doc/stage-5-implementation-plan.md`：§8.1、§8.2、§11、F04、F06、S5/S6 拆分和 D1–D3；
- `doc/web-ssh-design-development-plan.md`：Session 生命周期、自动恢复与主动接管边界；
- `doc/web-ssh-websocket-protocol.md`：Prepare mode、自动恢复、bind 成功丢失和接管失败矩阵；
- `doc/web-ssh-architecture-implementation-plan.md`：客户端自动恢复模式与后端唯一连接所有权；
- `doc/web-ssh-implementation-stages.md`：第五阶段 S5/S6 边界；
- `frontend/src/terminal/client.ts`：`RecoveryTask`、`connect`、`run`、`start`、`loop`、`prepareCredential`、`attempt`、`stop`、`dispose`、`running`、`completion`；
- `frontend/src/terminal/recovery.ts`：`SessionRecovery.canRecover`、`refresh`、`recover`、`stop`；
- `frontend/src/components/terminal/TerminalPane.vue`：首次连接结果与 `RecoveryState` 的使用方式；
- `frontend/tests/terminal.test.mjs`：stop、首次连接 Promise、重试预算、稳定资格和人工恢复测试。

结论标记在本轮统一使用以下四类：

- **冻结设计要求**：冻结文档或已确认 D1–D3 直接规定；
- **当前代码已实现行为**：当前源码可证实的行为；
- **当前代码缺失或不完整行为**：当前源码未实现或语义不足以支撑 S5；
- **Agent 建议的 S5 实施契约**：为落实冻结设计而建议的最小实施边界，尚待编码审查，不是协议变更。

本轮只修改本报告，没有修改业务代码、测试代码、API、协议或冻结设计，没有开始 S5-C1/C2 编码，没有执行 commit、push、merge、rebase、reset、clean 或部署。

## 19. 当前任务复用逻辑

**当前代码已实现行为**

- `RecoveryTask` 在 `frontend/src/terminal/client.ts:89-96` 只保存 `id`、`stopped`、`retries`、首次连接 `connection/connected` 和长期循环 `done`，没有 mode、初始 expected_generation 或首次 mode 消费状态。
- `connect(expectedGeneration)` 和 `run(expectedGeneration)` 分别返回 `start(expectedGeneration).connection` 与 `.done`，位置为 `frontend/src/terminal/client.ts:195-200`。
- `start(expected)` 位于 `frontend/src/terminal/client.ts:204-229`。当前判断顺序是：先检查 dispose/allowed，再检查 `stopping` 或当前任务 stopped，然后在 `if (this.task) return this.task` 处直接复用活动任务，最后才校验新传入的 `expected`。
- 因而当前行为确实存在“已有活动任务时直接返回旧任务”的逻辑。当前所有调用都只使用 attach，且 `SessionRecovery` 先 stop 再 connect，所以现状没有暴露 cross-mode 问题；引入显式 takeover 后，这一顺序会把不同 mode 或不同 generation 的新调用静默归并为旧任务。
- 当前每个 Client 最多只有一个 `this.task`，`attempt()` 也只有一个当前 `socket/finish`。这已经提供单长期任务基础，位置为 `frontend/src/terminal/client.ts:123-135,179-187,371-394`。
- `this.generation` 是可变连接状态：`start()` 初始设为 expected，`bind_success` 校验通过后更新为新 generation，位置为 `frontend/src/terminal/client.ts:224-225,460-464`。因此不能用 `currentGeneration` 代替任务创建时的 expected_generation 参数身份。
- `stop()` 同步设置 `task.stopped=true`，取消凭证请求、retry delay 和当前 finish，并把 `this.stopping` 保留到旧 `task.done`、待发送任务和输出排空完成，位置为 `frontend/src/terminal/client.ts:667-685`。
- loop 的 `finally` 只在 `this.task === task` 时清除引用，位置为 `frontend/src/terminal/client.ts:319-329`；这能防止旧任务身份清除新任务，但当前仍应通过“停止完成前禁止新任务”从结构上消除该窗口。

**当前代码缺失或不完整行为**

- 活动任务没有不可变的 mode 和初始 expected_generation 参数身份。
- 新参数校验发生在活动任务复用之后。
- 不同 mode/generation 的拒绝没有明确、可测试的本地错误语义。
- 现有 `generation` 可先由构造参数或 `start(expected)` 写入，再在 `bind_success` 后更新；当前没有独立标记区分“用户确认的 initialExpectedGeneration”和“bind_success 确认的 confirmedGeneration”。两者都可用于恢复，但来源和优先级不同，不能合并成一个不可区分的字段。
- 当前测试只验证单一 attach 任务复用和 stop 隔离，没有覆盖参数冲突、initialExpectedGeneration 回退或 confirmedGeneration 优先级。

**Agent 建议的 S5 实施契约**

`RecoveryTask` 应保存至少以下不可变任务参数：

```ts
initialMode: 'attach' | 'takeover'
initialExpectedGeneration: number
initialModeConsumed: boolean
```

其中 `initialExpectedGeneration` 必须保持调用方启动任务时的值，是用户通过详情确认并提供给本次主动操作的合法已知代次。TerminalClient 还应维护独立的 `confirmedGeneration: number | null`（或等价标记），只在合法 `bind_success` 后更新；任务参数身份不能由该可变值回写。自动 attach 的代次选择为：`confirmedGeneration ?? initialExpectedGeneration`。`initialModeConsumed` 只控制首次 Prepare 使用哪个 mode，不能代替任务参数比较。

## 20. mode / generation 任务复用契约

**冻结设计要求**

- 一个 TerminalClient 在同一时刻只能拥有一个有效长期连接/恢复任务。
- 新 mode 入口不能绕过单任务所有权，不能并行创建第二个 WebSocket、第二个恢复循环或第二个 Pane 所有者。
- takeover 只能由明确授权的调用方发起；attach 自动恢复始终 attach。
- D2=A 要求旧任务先停止并完整收尾，再启动新接管任务。

**Agent 建议的 S5 实施契约**

方案 A 下必须同时区分“任务初始 mode”与“当前 attempt mode”：

| Attempt | Prepare mode | Generation 来源 |
|---|---|---|
| 第一次 attach | attach | 任务创建时的 initial expected_generation。 |
| 第一次 takeover | takeover | 用户在确认接管时取得的 expected_generation；只用于这个首次 attempt。 |
| takeover 失败后的后续恢复 | attach | 优先使用最新 confirmedGeneration；尚无新的 bind_success 时使用 initialExpectedGeneration。 |
| takeover 成功后断线恢复 | attach | takeover 成功后通过 bind_success 确认的新 generation。 |
| 后续 attach 恢复失败 | attach | 优先使用最新 confirmedGeneration；尚无 bind_success 时保持 initialExpectedGeneration；遵守原预算和错误分类。 |

`initialExpectedGeneration` 是调用方创建任务时的不可变参数，也是用户通过详情读取并明确确认的合法 generation。它用于任务身份比较、第一次 attempt，以及尚无新 `bind_success` 时后续自动 attach 的代次依据；它不是客户端猜测值。

`confirmedGeneration` 表示当前长期任务最后一次通过合法 `bind_success` 成功确认的 generation，初始为 `null`。自动 attach 选择规则固定为：

```ts
const recoveryGeneration =
  confirmedGeneration ?? initialExpectedGeneration
```

- 首次 takeover 失败且尚无新 `bind_success`：使用 `initialExpectedGeneration` 发起后续 attach。
- 已经收到合法 `bind_success`：使用最新 `confirmedGeneration`。
- Prepare 成功、WebSocket OPEN、bind 已发送、网络错误或超时都不能产生或替换 generation。
- 详情/列表后来出现的新值不得自动替换任务 generation。
- 使用旧 generation attach 时，由服务端原子代次校验决定是否仍有效；`BIND_CONFLICT` 按原规则停止。
- 如果任务在创建时没有合法可用的 initialExpectedGeneration，`start` 应在创建任务前拒绝；长期任务内部不应出现两个来源都为空的情况。

| 当前任务 | 新调用 | 预期行为 |
|---|---|---|
| 无活动任务、无 stopping | attach | 创建 attach 任务。 |
| 无活动任务、无 stopping | takeover | 创建 takeover 任务；真实发起仍须由已授权的调用方触发。 |
| attach 任务运行中 | 相同初始 mode、相同 initial expected_generation | 返回同一个任务及其对应 Promise；不新增 Prepare 或 socket。 |
| takeover 任务运行中 | 相同初始 mode、相同 initial expected_generation | 返回同一个任务及其对应 Promise；不得重复 takeover Prepare。 |
| attach 任务运行中 | takeover | 明确拒绝；不停止 attach，不复用 attach Promise，不并行启动 takeover。 |
| takeover 任务运行中 | attach | 明确拒绝；不停止 takeover，不复用 takeover Promise，不并行启动 attach。 |
| 任意活动任务 | 相同 mode、不同 initial expected_generation | 明确拒绝；不静默复用，也不自动刷新 generation。 |
| stop 已请求但 `stop()` 尚未完成 | 任意 mode | 明确拒绝；包括任务已 stopped、`task.done` 已结束但 pending send/output drain 尚未完成的阶段。 |
| 旧任务已完整 stop/dispose | 新 mode | `await stop()`/`dispose()` 完成后，允许按新参数启动。 |

具体实现顺序必须固定为：

1. 校验 Client 是否 disposed/allowed。
2. 校验 expected_generation 是否为安全非负整数。
3. 校验 mode 是否为字面量 `attach` 或 `takeover`。
4. 检查 stopping/stopped cleanup 状态；未完成则拒绝。
5. 若存在活动任务，比较不可变 `initialMode` 与 `initialExpectedGeneration`；两者完全相同才复用，否则拒绝。
6. 无活动任务时才创建新任务并记录参数。

第 2–3 步必须在活动任务复用之前执行；不能先 `return this.task` 再校验。否则非法 generation 或新 mode 会被伪装成旧任务成功。

拒绝应在调用 `connect`/`run` 时同步抛出，或返回一个立即拒绝且不会与旧任务 Promise 混同的本地错误。推荐最小内部语义为 typed local error，例如 `TerminalClientTaskConflictError`，并带 `mode_mismatch`、`generation_mismatch`、`task_stopping` 等可测试原因。该错误仅是前端内部约定，不新增 HTTP/WS 错误码。具体名称可在编码审查时调整，但“拒绝不能返回旧任务 Promise”是必须验收条件。

不得为接受 takeover 自动执行以下行为：

- 自动取消 attach 后立即并发启动 takeover；
- 自动把 attach 升级为 takeover；
- 把任务的 initialMode 从 takeover 改写为 attach；方案 A 中首次 mode 消费后由同一 loop 发起的独立 attach attempt 不属于改写 initialMode；
- 自动刷新 generation 后重发 takeover；
- 在 stop cleanup 未完成时提前替换 `this.task`。

若调用方需要换 mode 或 generation，责任是：调用 `stop()`，等待其完成并确认 `running=false`，然后以新参数调用 start。S6 的 D2=A 流程必须负责等待该屏障；TerminalClient 只负责拒绝越界启动。

## 21. stop 与新任务启动的先后关系

**当前代码已实现行为**

- `stop()` 首次调用同步标记旧任务 stopped，因而 loop、Prepare、socket handler 和 send 准入会立即失去继续工作的资格。
- `stop()` 会复用同一个 `this.stopping` Promise；新调用在 `this.stopping` 非空时被 `start()` 拒绝。
- `this.stopping` 只有在旧 `task.done`、`sendTask` 和 `output.settle(true)` 都完成后才清空。
- `stop()` 结束前不能安全启动新任务；`stop()` 完成后的新任务具有新的 task ID。
- loop `finally` 使用任务对象身份比较，避免旧任务清除新任务引用。

**当前代码缺失或不完整行为**

- 新调用被拒绝时只有通用“请等待旧任务停止”错误，尚未区分 mode/generation 冲突与 stopping 冲突。
- 现有测试证明重复 stop 和 stop 后迟到凭证隔离，但没有明确验证“stop 已请求但 drain 未完成时不同 mode 被拒绝、完成后新 mode 可启动”。

**Agent 建议的 S5 实施契约**

- 旧任务从 `stop()` 请求开始即封闭所有新任务启动。
- “任务 loop 已结束”与“资源清理已完成”是不同屏障；只有 `stop()`/`dispose()` Promise settle 后才能启动新任务。
- S6 恢复中/已停止 Pane 的接管必须显式执行 `await client.stop()`，再查询/确认新详情并启动 takeover；不得在 stop 后立即无等待地启动。
- 旧 socket、旧 Prepare、旧 finish、旧 xterm write 回调和旧任务 `finally` 都必须通过 task/socket/controller/terminal identity 检查，不能修改新任务状态。
- 当前结构已禁止正常 API 下“新任务已启动而旧 loop finally 尚未执行”的窗口；测试仍应保留 defense-in-depth 的身份断言，防止未来重构移除 `this.task === task` 条件。

## 22. 首次连接 Promise 与长期任务语义

**当前代码已实现行为**

- 每个 `RecoveryTask` 创建一个只 resolve 一次的 `connection` Promise。
- 首次 attempt 在 bind 前缀完成时调用 `task.connected({connected:true,...})`，位置为 `frontend/src/terminal/client.ts:480-490`。
- 首次 attempt 在前缀前失败时，`finish()` 调用 `task.connected({connected:false,...})`，位置为 `frontend/src/terminal/client.ts:395-416`；loop 每轮结束后还会调用一次 false，但 Promise 已 settle 后不会改变。
- 首次失败后，只要结果可重试且预算未耗尽，同一 loop 可以继续；后续 attach 成功会发布 `RecoveryState.phase='online'`，但不会让已经 settle 的首次 connection Promise 再次变为 true。
- `task.done` 由 `loop()` 返回的 `RecoveryState` settle，`completion` 暴露当前任务的 `done`，位置为 `frontend/src/terminal/client.ts:89-96,201-202,270-339`。
- `running` 在 `task` 或 `stopping` 存在时为 true，位置为 `frontend/src/terminal/client.ts:170-172`。
- `stop()` 在 `task.done` 之后还等待 pending send 与 output drain；因此 `completion`/`done` 表示恢复 loop 结束，完整的停止与本地发送/输出清理屏障是 `await stop()`，二者不能混同。
- `TerminalPane.connect()` 只用 `ConnectionResult.connected` 决定是否 layout/focus；Pane phase 和提示由 `RecoveryState` 驱动，位置为 `frontend/src/components/terminal/TerminalPane.vue:91-113,156-172`。

**当前代码缺失或不完整行为**

- 两类 Promise 已在代码中分开，但报告和测试尚未把“首次失败后后续恢复成功”“Promise 只 settle 一次”“done 与 stop cleanup 不等价”明确列为契约。
- takeover 首次失败后的 loop 生命周期已由方案 A 明确；当前尚无 takeover 代码，且缺少 confirmed generation 的独立标记，需在 S5 补齐。

**Agent 建议的 S5 实施契约**

| 结果对象 | 精确语义 | 不代表什么 |
|---|---|---|
| 首次 connection Promise | 仅表示该任务第一次 attempt 是否完成约定的 bind 前缀；成功 resolve true，失败 resolve false；只能 settle 一次。 | 不表示长期任务结束；不表示之后不会自动恢复；不能被后续成功再次改成 true。 |
| 长期 `done` / `completion` | 表示连接/恢复 loop 的终止结果，包含 exhausted、停止原因和最终 `RecoveryState`。 | 单独 settle 不保证 pending send/output drain 已清理完成。 |
| `stop()`/`dispose()` Promise | 完整停止与本地资源清理屏障；resolve 后 `running=false`，才允许新任务。 | 不应被首次 connection Promise 代替。 |

必须逐项落实以下场景：

1. **首次 attach 成功**：首次 Promise 为 true，长期 loop 继续运行，`completion` 仍 pending。
2. **首次 attach 失败**：首次 Promise 为 false；若结果可恢复且预算未耗尽，同一长期任务继续 attach。
3. **首次失败后恢复成功**：`RecoveryState` 变为 `online`，首次 Promise 仍保持 false；不得二次 settle。
4. **首次 Promise 已 settle**：后续任何恢复结果不能再次改变其值。
5. **takeover 首次失败**：该次 attempt 本身绝不能改为 attach；用户已确认方案 A，错误可恢复且预算未耗尽时，同一长期任务进入新的独立 attach attempt。generation 优先使用最新 confirmedGeneration；尚无新确认值时使用 initialExpectedGeneration，后续不得再次 takeover。
6. **takeover 后长期任务状态**：任务是否结束只看 loop/`done`/`stop()` 的真实生命周期，不能从首次 Promise 的 false 推断。
7. **stop/dispose 后迟到结果**：旧首次 Promise 可以按 stop 语义 settle，但旧结果不能改变新任务 connection、RecoveryState、generation 或 notice。
8. **S6 解释边界**：S6 不能仅凭 `ConnectionResult.connected === false` 认定 Pane 长期任务已经结束；必须检查 `running`、`completion`/`RecoveryState`，并在需要彻底释放时等待 `stop()`。

## 23. 相关调用方与 SessionRecovery 职责

**当前代码已实现行为**

- `SessionRecovery.canRecover` 要求 `!client.running`，位置为 `frontend/src/terminal/recovery.ts:31-40`。
- `refresh()` 同样要求 Client 未 running，并用 source task ID、sequence、Abort 和 allowed 防止旧查询回写，位置为 `frontend/src/terminal/recovery.ts:51-94`。
- `recover()` 在第一次 await 前消费候选并设置 acting，随后 `await this.client.stop()`；只有 stop 完成后才 `client.connect(candidate.binding_generation)`，位置为 `frontend/src/terminal/recovery.ts:96-119`。
- SessionRecovery 不创建 socket、不维护第二套恢复 loop，也不选择 takeover mode。
- TerminalPane 只把首次连接 true 用于布局；连接状态、重试、stopped 和诊断来自 `RecoveryState`。
- 现有测试“人工恢复必须刷新再点击”验证 SessionRecovery 只发 attach，并先停止旧任务，位置为 `frontend/tests/terminal.test.mjs:682-724`。

**冻结设计要求**

- TerminalClient 是 Pane 的唯一连接/恢复协调器。
- SessionRecovery 负责人工 attach 候选和详情查询，不得创建第二套长期连接。
- S6 才负责用户接管 UI、Pane 协调和用户确认；S5 不实现 S6。
- D2=A 下，恢复中或已停止 Pane 的接管先停止旧任务并等待完整收尾。

**Agent 建议的 S5 实施契约**

- 现有 attach 调用方继续使用 `connect/run` 的默认 attach，不改变 SessionRecovery 的默认语义。
- S5 提供显式 takeover 连接层入口，但不会从 SessionRecovery、列表刷新、visibility、online 或 Session 状态自动调用。
- S6 发起 takeover 时必须拥有用户动作和确认上下文，调用同一个 Pane 的 TerminalClient；若当前任务仍 running，先 await stop，再以新的详情 generation 和 takeover mode 启动。
- S6 不能把 SessionRecovery 的人工 attach 入口当成 takeover 入口，也不能在 TerminalClient 外再建一个连接所有者。

## 24. 补充测试矩阵

所有测试使用可控 fetch、Mock WebSocket、手动 resolve/reject、显式 task completion 信号或 mock timer；不得用固定 sleep 猜测异步进度。

### 24.1 任务参数与复用

| ID | 场景 | 关键交错与断言 |
|---|---|---|
| M01 | 同 mode、同 initial expected_generation 重复调用 | 第一次调用创建任务后，第二次返回同一 task ID 和同一首次 connection Promise；Prepare/socket 数不增加；原任务状态不变。 |
| M02 | attach 活动时调用 takeover | takeover 调用立即以 `mode_mismatch` 拒绝；不返回 attach Promise；attach socket/Prepare/RecoveryState 保持原样；未创建 takeover socket。 |
| M03 | takeover 活动时调用 attach | 对称拒绝；不得自动取消 takeover 或并行 attach。 |
| M04 | 同 mode、不同 initial expected_generation | 立即以 `generation_mismatch` 拒绝；活动任务、currentGeneration 和请求数不变；不得自动刷新 generation。 |
| M05 | 活动任务时传入非法 generation | 即使已有活动任务也必须先校验并拒绝，不能返回旧任务 Promise。 |
| M06 | stop 已请求、`task.done` 已 settle 但 pending send/output drain 未完成 | 任意新 mode 以 `task_stopping` 拒绝；旧 socket 已禁输入，新 Prepare/socket 数为零。 |
| M07 | stop 完整完成 | 释放最后一个 drain/barrier 后 `running=false`；新 mode 可以创建新 task ID，旧 generation 不泄漏到新任务。 |
| M08 | 旧任务迟到 finally/连接回调 | 先完成受控 stop 再启动新任务；触发旧 finally、旧 open/message/close 和旧凭证 resolve；新 task ID、connection、socket、generation、notice 均不变。测试应直接验证 task identity guard；若正常公共 API 已使 finally 不可能迟到，可使用测试内受控任务对象或最小 harness，不为生产代码增加调试 hook。 |

### 24.2 首次 Promise 与长期任务

| ID | 场景 | 关键交错与断言 |
|---|---|---|
| P01 | 首次 attach 失败但任务仍运行 | 首次 Promise resolve false；`running=true`；`completion` 尚未 settle；按冻结预算产生下一次 Prepare。 |
| P02 | 首次失败后后续 attach 恢复成功 | 首次 Promise 仍 false；后续 `RecoveryState` 发布 online；通过 settle counter 证明首次 Promise 只 settle 一次。 |
| P03 | 首次 attach 成功 | 首次 Promise true；`completion` pending；`running=true`；后续输入和 RecoveryState 继续工作。 |
| P04 | 首次 Promise 已 settle 后 stop | connection 值不再变化；stop 等待 loop、pending send、drain 后完成；`running=false`，completion 为 stopped 结果。 |
| P05 | 首次 takeover 失败后的恢复 | 第一次 Prepare 为 takeover；失败后首次 connection Promise 为 false 且只 settle 一次；可重试且预算未耗尽时 `running=true`。尚无新 bind_success 时，下一次 Prepare 为 attach、expected_generation=initialExpectedGeneration G；已有合法 bind_success 时使用最新 confirmedGeneration C；之后永不再次 takeover。测试同时覆盖 G 已过期导致 BIND_CONFLICT 后停止、不自动 GET 新 generation、后续 attach 成功发布 online 但不改写首次 Promise，以及不可重试错误、无合法初始 generation、预算耗尽时停止。 |
| P06 | 旧任务首次 Promise 在 stop 后迟到 | 新任务的 connection/completion/RecoveryState/generation 不变；旧 Promise 不能二次 settle 或清除新 task 引用。 |
| P07 | `done` 已结束但 drain 未完成 | `completion` 可已 settle，但 `running` 仍 true，新 start 被拒绝；只有 `await stop()` 完成后才允许新任务。 |

### 24.3 S6 集成前调用方契约

| ID | 场景 | 关键断言 |
|---|---|---|
| S6-PRE01 | SessionRecovery 默认恢复 | 仍先 stop 后 connect，只发 attach；不识别 takeover，不创建 socket。 |
| S6-PRE02 | S6 发现首次 Promise false | 不据此认定 Pane 已停止；等待真实 RecoveryState/`running`/`stop()` 屏障。 |
| S6-PRE03 | S6 准备 takeover 时旧任务仍运行 | 只执行明确 stop 并等待完成；完成后重新读取/使用用户确认的 generation；不得自动升级当前 attach。 |

## 25. 对 S5-C1 / S5-C2 编码拆分的影响

**Agent 建议的 S5 实施契约**

S5-C1 的职责应扩展为：

1. 显式 `attach | takeover` 初始 mode；
2. RecoveryTask 保存不可变 initial mode、initial expected_generation 和首次 mode 消费状态；
3. 参数校验先于活动任务复用；
4. 同 mode/同 initial generation 才复用，其余 mode/generation 冲突同步拒绝；
5. stopping/stopped cleanup 未完成时拒绝；
6. stop 完成后才允许新任务；
7. 首次 connection Promise 与长期 done/stop 清理语义明确；
8. 保持单 socket、单恢复 loop、原 generation 校验、offset、预算和输出行为。

S5-C2 的职责应扩展为：

1. M01–M08 的任务参数、stop 和旧回调测试；
2. P01–P07 的首次/长期 Promise 测试；
3. S6-PRE01–03 的调用方边界回归；
4. 原 attach、4001/4002、bind_success 丢失和稳定资格回归。

这两部分可以完成编码准备和测试设计。P05、loop 继续条件和 `done` 结束条件按方案 A 明确：首次 takeover 失败后，可恢复结果在预算内继续，但后续固定 attach；代次使用 `confirmedGeneration ?? initialExpectedGeneration`，不自动查询换代。不可恢复错误、缺少合法已知 generation 或预算耗尽时结束。实际编码仍须等待用户单独授权。

## 26. 首次 takeover 失败后的恢复策略（用户已裁决：方案 A）

**用户裁决**

用户已正式选择方案 A：首次 takeover attempt 失败后，保持原冻结设计，同一长期任务按原有限预算自动 attach，不再次 takeover，不自动更换 generation。

方案 A 只改变同一长期任务首次 attempt 与后续 attempt 的 mode 选择；原错误分类、重试次数、delay、稳定资格、offset/replay、drain 和任务生命周期不变。

**两个 generation 概念**

`initialExpectedGeneration`：

- 由用户通过详情读取并确认，作为本次明确主动操作的 expected_generation；
- 是不可变任务参数，也是客户端已经合法获知的 generation；
- 用于第一次 attempt 和任务参数复用比较；
- 尚无新 `bind_success` 时，后续自动 attach 仍可使用它；
- 不能因 Prepare 成功、WebSocket OPEN、bind 已发送、网络错误或详情刷新而被自动替换。

`confirmedGeneration`：

- 表示当前长期任务最后一次通过合法 `bind_success` 成功确认的 generation；
- 初始为 `null`，只能由 `bind_success` 更新；
- 第一次 takeover 返回 G+1 且通过校验后，更新为 G+1；
- 后续自动 attach 优先使用该值。

自动 attach 的代次选择固定为：

```ts
const recoveryGeneration =
  confirmedGeneration ?? initialExpectedGeneration
```

没有收到 `bind_success` 不等于 initialExpectedGeneration 不可用，也不等于服务端一定没有提交。客户端只使用最后合法确认的 generation 发起 attach，由服务端原子代次校验判断是否仍然有效。

**方案 A 实现契约**

1. RecoveryTask 保存不可变 `initialMode`、`initialExpectedGeneration` 和 `initialModeConsumed`。
2. 第一次 Prepare 使用 `initialMode` 与 `initialExpectedGeneration`；首次 attempt 在 Prepare 前消费 mode 闩锁。
3. 第一次 attach 失败后的自动恢复仍使用 attach。
4. 第一次 takeover attempt 失败后，只要错误属于原可恢复集合且预算未耗尽，同一长期任务进入新的独立 attach attempt。
5. 后续 attach 的 mode 固定 attach；generation 使用 `confirmedGeneration ?? initialExpectedGeneration`。
6. 已收到合法 `bind_success` 时使用最新 confirmedGeneration；尚无新的 bind_success 时使用 initialExpectedGeneration。
7. 不自动查询详情或列表来更换 generation。
8. 不自动把 G 推算成 G+1。
9. 不再次自动 takeover。
10. 如果服务端判定所用 generation 已过期，返回 `BIND_CONFLICT`；客户端按原错误分类停止。
11. `taken_over`、Close 4001、认证/权限失败、协议失败和预算耗尽仍按原规则停止。
12. 后续 attach 成功可发布 `RecoveryState.phase='online'`（回放完成前可为 replaying），但首次 connection Promise 已 settle 后不能再次改变。
13. loop 内 attempt mode 变化不改变任务 `initialMode`；调用方再次 start 时仍按原 mode 与 initialExpectedGeneration 做任务身份比较。

**首次 takeover 失败的两种情况**

| 情况 | 客户端已知信息 | 后续 attach generation | 服务端拒绝时 |
|---|---|---|---|
| A：takeover 未提交或提交结果不确定 | 可重试 Prepare/WS 网络错误；没有新 bind_success；无法确定是否提交 | 使用最后合法确认值，即 `confirmedGeneration ?? initialExpectedGeneration`；通常为 initialExpectedGeneration G | 若 G 已过期，服务端返回 BIND_CONFLICT，客户端停止，不查询或重新 takeover |
| B：takeover 已提交，但 bind_success 丢失 | 服务端可能已完成 G→G+1，但客户端没有确认 G+1 | 仍使用最后合法确认值 G；不得自行采用 G+1 | BIND_CONFLICT 按原规则停止；不自动获取或采用详情新代次 |

若任务此前已经通过其他合法 `bind_success` 确认过 generation C，则两种情况都使用最新 C，而不是机械回退到 initialExpectedGeneration。

**错误分类与预算**

方案 A 不改变第四阶段和冻结协议中的恢复契约：

- 只有原 `retryCodes`/Close code 集合中的错误允许有限重试；
- `BIND_CONFLICT`、taken_over、Close 4001 和其他不可恢复错误立即停止；
- `INVALID_CREDENTIAL` 等明确可重试错误仍按原预算处理；
- `reconnect_max_attempts`、`reconnect_delay`、`reconnect_stable_time`、稳定资格和预算消费不变；
- OUTPUT_GAP/4002 与 takeover/4001 的分类不变；
- offset/replay、drain 和单任务所有权不变。

**与任务参数复用规则的关系**

- 后续 loop 内部由 takeover 切换到 attach 是同一任务策略，不修改 `initialMode`，也不等于调用方发起新的 start。
- 活动任务仍只允许相同 `initialMode` 和相同 `initialExpectedGeneration` 复用。
- 不同 mode/generation 的新调用必须拒绝；stop cleanup 完成前仍不能启动新任务。
- 首次 connection Promise、长期 completion/done、running 和 stop 清理屏障语义不变。

**方案 A 验收条件**

- 任务内 takeover Prepare 至多一次；首次 attempt 使用 takeover，后续自动 Prepare 全部 attach；
- 首次 takeover 失败后，首次 connection Promise 为 false 且长期任务在预算允许时仍 running；
- 尚无新 bind_success 时，后续 attach 使用 initialExpectedGeneration；
- 已有合法 bind_success 时，后续 attach 使用最新 confirmedGeneration；
- 不自动查询详情、不再次 takeover、不推算 G+1；
- G 过期时由 BIND_CONFLICT 停止；
- 后续 attach 成功只更新 RecoveryState，不二次 settle 首次 Promise；
- 不可恢复错误、缺少合法已知 generation 或预算耗尽时停止；
- taken_over/4001 仍停止自动恢复；
- stop 完整收尾前不能启动新任务。

## 27. 补充审查最终结论

- **任务参数复用和 stop 屏障是否已有完整定义？** 是。RecoveryTask 必须保存不可变 initial mode 与 initial expected_generation；参数校验先于复用；不同 mode/generation 明确拒绝；旧 stop/dispose 完整收尾前不得启动新任务。
- **首次 connection Promise 与长期任务语义是否已有完整定义？** 是。首次 Promise 只表示第一次 attempt 是否完成 bind 前缀且只 settle 一次；`done/completion` 表示长期 loop 终止；`stop()/dispose()` 是完整资源清理屏障；`running` 覆盖 task 与 stopping cleanup。
- **takeover 首次失败后的策略是否确定？** 是。用户已正式确认方案 A：首次 takeover attempt 失败后，只要错误可恢复且预算未耗尽，同一长期任务继续运行，后续独立 attempt 固定 attach；generation 使用 `confirmedGeneration ?? initialExpectedGeneration`。没有新的 `bind_success` 时，`initialExpectedGeneration` 仍是合法已知值；若其已过期，由服务端返回 `BIND_CONFLICT` 后停止。客户端不再 takeover、不自动查询或替换 generation；不可恢复错误或预算耗尽同样停止；首次 Promise 不被后续成功改写。
- **是否仍有需要用户新增裁决的 S5 设计问题？** 本轮全文复核后没有发现其他需要新增裁决的问题。冻结文档、协议和用户裁决在本次统一后一致。
- **S5-C1/C2 是否可以完成编码设计准备？** 可以。设计输入、任务参数契约、stop 屏障、Promise 语义和 M/P/S6-PRE 测试矩阵已经闭合；实际编码必须等待用户单独授权。
- **编码验收必须覆盖：**
  1. mode 和 initial expected_generation 在活动任务复用前校验；
  2. 只有相同 mode/相同 initial expected_generation 才复用；
  3. 不同 mode/generation 与 stopping 冲突明确拒绝；
  4. stop 完整完成前不启动新任务；
  5. 任务内 takeover Prepare 至多一次；首次 attempt 使用 takeover，后续自动 attempt 全部使用 attach；
  6. 首次 connection Promise 只 settle 一次；
  7. 首次 takeover 失败时 connection 可为 false，但长期任务在允许恢复时仍 running；
  8. 后续 attach 成功通过 RecoveryState 表达；
  9. `initialExpectedGeneration` 在任务创建后保持不可变，`confirmedGeneration` 只由合法 `bind_success` 更新；恢复使用 `confirmedGeneration ?? initialExpectedGeneration`，不自动查询或替换 generation；
  10. `BIND_CONFLICT`、taken_over、4001 和预算耗尽停止；
  11. 旧 task/socket/Prepare/finally/write 回调不得污染新任务；
  12. 六个 Mermaid 时序图及 M01–M08、P01–P07、S6-PRE01–03 全部按方案 A 验收。

本轮只修改本审查报告，未修改冻结文档、业务代码或测试代码，未开始 S5 编码，未执行 commit、push、merge、rebase、reset、clean 或部署。完成后停止，等待人工审查与后续独立编码授权。
