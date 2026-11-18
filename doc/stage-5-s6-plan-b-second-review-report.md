# Web SSH S6 方案 B 冻结设计文档第二轮只读交叉审查报告

- 审查日期：2026-11-18
- 审查对象：`doc/stage-5-implementation-plan.md` 的 S6 方案 B 修订版
- 实际代码基线：分支 `codex/第五阶段`，HEAD `feb0ac5`
- 审查性质：只读交叉审查；未修改生产代码、测试代码、CSS、配置、依赖或既有设计文档
- 最终结论：**BLOCKED**

## A. 审查范围

本次实际读取并核对以下文件。

### 设计文档

- `doc/stage-5-implementation-plan.md`
- `doc/stage-5-s6-plan-b-revision-report.md`
- `doc/stage-5-s6-precheck-report.md`

### 生产源码

- `frontend/src/pages/HomePage.vue`
- `frontend/src/components/terminal/TerminalPane.vue`
- `frontend/src/components/session/SessionManager.vue`
- `frontend/src/terminal/client.ts`
- `frontend/src/terminal/recovery.ts`
- `frontend/src/terminal/output.ts`
- `frontend/src/api/sessions.ts`
- `frontend/src/api/management.ts`
- `frontend/src/api/client.ts`
- `frontend/src/stores/auth.ts`
- `frontend/src/session/list.ts`
- `frontend/src/components/layout/Modal.vue`
- `frontend/src/styles/base.css`
- `frontend/src/router/index.ts`

### 现有测试

- `frontend/tests/terminal.test.mjs`
- `frontend/tests/stage-5-c2.test.mjs`
- `frontend/tests/terminal-pane.test.mjs`
- `frontend/tests/session-list.test.mjs`
- `frontend/tests/auth.test.mjs`
- `frontend/tests/management.test.mjs`
- `frontend/tests/client.test.mjs`

当前仓库没有 `frontend/src/session/takeover.ts`，也没有
`frontend/tests/session-takeover.test.mjs`。因此 S6 方案 B 的主动接管控制器和专项测试
仍属于设计或待实现内容。

本轮未执行测试或构建。当前 `frontend/node_modules` 不存在，且本次任务为只读审查。

## B. 所有权模型核对

| 组件 | 实际代码职责 | 修订文档职责 | 核对结果 |
|---|---|---|---|
| `SessionManager` | 列表展示、服务器筛选、本地状态筛选、刷新、可见性刷新；`onBeforeUnmount` 只释放列表控制器（`SessionManager.vue:128-137`） | 只展示、筛选、刷新并 emit `open`；不查详情、不创建 Pane、不持有确认状态 | 边界一致；行“打开”事件尚未实现 |
| `HomePage` | 当前只有 `tabs`、`panes`、`activeTab`，没有 `pending/closing`；`connectServer` 创建新 Session 后直接 push（`HomePage.vue:19-54`） | 同步预占 Session ID，维护 `tabs/panes/pending/closing`，创建候选 Pane，负责页面生命周期 | 设计目标正确，但当前代码没有唯一登记状态 |
| `TerminalPane` | `onMounted` 无条件创建 Client 并 `void connect()`（`TerminalPane.vue:218-308`）；只 expose `stop/dispose` | 增加候选模式，mount 不自动连接；创建并绑定 Pane 内唯一 `SessionTakeover` | 方向正确，但候选模式、ready/failed 握手和接管控制器均未实现 |
| `SessionTakeover` | 不存在 | 候选 Pane 主动接管唯一所有者：详情、确认、取消、sequence/Abort、调用 `connectTakeover()` | 文档职责清晰；缺少与 HomePage/TerminalPane 的可执行绑定协议 |
| `TerminalClient` | 已实现 `connectTakeover()`、首次 mode 消费、后续 attach、Promise 分离、stop 屏障（`client.ts:240-245`、`399-412`、`750-768`） | 复用既有连接入口；不得另建 WebSocket/RecoveryTask | 代码与 S5 方案 A 一致；但主文档 §3 的现状描述已过期 |
| `SessionRecovery` | 当前只处理人工 attach 候选和两步恢复；`refresh()`、`recover()` 不识别 takeover（`recovery.ts:51-119`） | 保持人工 attach；增加主动意图互斥/失效协作 | 只有原则，没有明确的 `blocked` 接口或等价门禁 |
| `SessionListController` | sequence、Abort、身份、旧 `finally` 隔离完整（`list.ts:37-97`） | 复用列表请求身份和取消规则 | 一致 |
| `OutputTracker` | epoch、in-flight、offset、drain、有限超时完整（`output.ts:105-249`） | 原样复用 | 一致 |

## C. 生命周期和竞态审查

| # | 场景 | 审查结果 |
|---|---|---|
| 1 | 详情 GET 后用户取消 | 设计覆盖。`SessionTakeover` 递增 sequence、abort、校验请求身份；迟到成功或失败不能发布确认。 |
| 2 | 详情 GET 后再次查询 | 设计覆盖。旧请求失效，新请求胜出；旧 `finally` 不能清除新 loading。 |
| 3 | 旧请求成功、失败、`finally` 迟到 | 设计覆盖。§8.2.1 明确 success/error/finally 均检查 sequence、请求身份和 Pane 身份。 |
| 4 | Abort 后底层请求仍迟到返回 | 设计覆盖。文档明确 `abort()` 不等于底层完成，必须结合 sequence/request identity；当前 `request()` 也支持 AbortSignal。 |
| 5 | 详情完成，用户确认前 Pane 已关闭 | 规则覆盖，但关闭路径的可观察屏障不足。Pane 关闭必须 invalidate takeover；当前 `closeLocal()` 会等待 `dispose()` 后 emit `closed`（`TerminalPane.vue:214-217`），但 HomePage 发起的移除没有等价的“dispose 完成”回调定义。 |
| 6 | 用户连续双击确认 | 设计覆盖：首次 await 前消费候选并设置 `acting`。但文档没有明确 `connectTakeover()` 同步抛错时的 catch 和状态回滚，测试应覆盖同步异常和异步拒绝。 |
| 7 | 确认时快照已过期 | 设计覆盖。确认前校验快照、sequence、Pane、任务、页面和登录身份。 |
| 8 | HomePage 正在卸载，详情 GET 同时返回 | 设计规则覆盖，但实现闭环不足。`HomePage.onBeforeUnmount` 只 abort 查询（`HomePage.vue:129-134`）；Pane 的 `onBeforeUnmount` 只是 `void dispose()`（`TerminalPane.vue:331-333`），页面卸载无法等待 `dispose` 的 stop/drain 完成。需要明确这是非阻塞清理，还是提供可等待的页面 teardown 屏障。 |
| 9 | 登出和 takeover 确认同时发生 | 设计覆盖。`HomePage.signOut()` 在首次 await 前设置 `logoutPending`（`HomePage.vue:240-264`），Client 的 `allowed()` 会封闭新任务。 |
| 10 | 身份失效和 Prepare 同时发生 | 部分覆盖。文档要求检查“登录身份”，但未定义身份 epoch 或对象身份；当前 `TerminalPane.live()` 只判断 `identity.value !== null`（`TerminalPane.vue:63-64`）。若身份被替换而不是先置空，旧任务可能继续被允许。 |
| 11 | D2 stop 等待 drain 时用户触发其他动作 | **设计缺口。** §8.1 声称所有主动动作共用本地互斥意图，但没有定义 `SessionRecovery` 的 `blocked` 谓词或等价接口。当前 `SessionRecovery.refresh()` 在 `client.running=false` 时仍可启动详情查询并生成人工 candidate；`recover()` 也可启动 attach。若发生在 takeover 详情或确认期间，可能产生第二个详情候选，随后 `connectTakeover()` 因已有 attach 任务而抛 `mode_mismatch`。 |
| 12 | stop 已发起但尚未完成时启动新任务 | 覆盖。`TerminalClient.start()` 在 `stopping` 或 `task.stopped` 时抛 `task_stopping`（`client.ts:249-264`）；现有 S5-C2 测试已覆盖。 |
| 13 | SessionManager 卸载后 Pane 详情继续 | 覆盖。HomePage 的 Pane 是独立 `v-for`，SessionManager 通过 `v-if` 卸载；现有 HomePage 测试证明 Pane 不卸载（`session-list.test.mjs:638-672`）。 |
| 14 | SessionManager 重新挂载后再次打开同一 Session | 设计原则覆盖，但测试矩阵没有明确覆盖“重新挂载后再次打开仍定位原 Pane”。S6-B06 只覆盖卸载，不覆盖 remount 后同一 ID 的定位行为。 |
| 15 | Pane 关闭后立即重新打开同一 Session | 设计覆盖 `closing` 和 `reopenRequested`，但“closing 何时释放”仍依赖 Pane 的异步 `dispose` 完成。文档没有定义 HomePage 如何获得这个完成信号；只靠 `setPane(id, null)` 不能证明底层 Client 已完成 stop/drain。 |

## D. S5 方案 A 兼容性

| 契约 | 实际代码依据 | 结论 |
|---|---|---|
| 首次 mode 只作用于第一次 attempt | `initialModeConsumed`、`nextAttemptGeneration()`（`client.ts:112-118`、`399-406`） | 满足 |
| 首次 takeover 最多一次 | 任务级 `initialMode` 固定；后续 attempt 固定 attach | 满足 |
| 后续 attempt 固定 attach | `client.ts:408-412` | 满足 |
| `initialExpectedGeneration` 不可变 | `readonly initialExpectedGeneration`（`client.ts:113-114`） | 满足 |
| `confirmedGeneration` 只由合法 `bind_success` 更新 | `client.ts:116-117`、`540-545` | 满足 |
| 后续 generation 使用 `confirmedGeneration ?? initialExpectedGeneration` | `client.ts:410-411` | 满足 |
| bind_success 丢失不推算 G+1 | 无 G+1 推算逻辑；测试 `terminal.test.mjs:808-853` | 满足 |
| `BIND_CONFLICT` 停止、不自动查询、不换代 | 不在 `retryCodes` 中（`client.ts:129-143`）；测试 `terminal.test.mjs:891-934` | 满足 |
| 首次连接 Promise 与长期 completion 分离 | `client.ts:246-248`、`265-280`；测试 `stage-5-c2.test.mjs:371-400` | 满足 |
| stop 是完整清理屏障 | `client.ts:750-768` 等待 `task.done`、`sendTask`、`output.settle(true)` | 满足 |
| 旧任务、Socket、Prepare、迟到回调隔离 | `client.ts:470-474`、`435-436`、`377-388`；测试 `stage-5-c2.test.mjs:243-323` | 满足 |
| S6 不得绕过 TerminalClient 另建连接 | 文档 §8.1、§16、S6-B15 明确禁止 | 设计一致 |

但主文档 §3 仍写 `frontend/src/terminal/client.ts`“所有 Prepare 写死 attach”，并把
“S5 Client 初始 takeover 模式”列为未来步骤（`stage-5-implementation-plan.md:56`、
`957`、`978`）。这与当前 `client.ts:240-245`、`399-412` 以及 S5-C2 测试不一致。
该问题不会改变 S5 方案 A 本身，但会造成 S6-C1 的实施范围和基线误判。

## E. D1–D3 核对

| 规则 | 核对结果 |
|---|---|
| D1：进入管理页、手动刷新、操作完成、重新可见时刷新；无定时轮询 | 基本一致。`SessionManager` 首次挂载、手动刷新、筛选、可见性刷新均存在；没有定时器。当前“操作完成刷新”依赖管理页重新挂载，尚未定义显式 `ref.refresh()`。 |
| D2：在线 Pane 只定位；恢复中或停止 Pane 明确停止恢复后再查详情；取消后不自动恢复 | 主流程一致，但有两个缺口：一是“正在回放 replaying”是否算在线没有明确映射；二是 `SessionRecovery` 的人工恢复或诊断刷新没有明确的 takeover 互斥门禁。 |
| D3：无 Pane 先创建唯一候选 Pane；mount 不自动连接；确认后才 takeover；失败保留 Pane 和原因 | 设计方向一致。当前 `TerminalPane` mount 必然自动连接，S6 必须增加显式候选模式；候选初始化失败后的 ready/failed 回调、`pending` 释放和同一标签重试机制尚未定义。 |

## F. S6-C1 / S6-C2 划分审查

现有映射：

- S6-C1：S6-B01–B17、B21、B22、B24；F05–F09。
- S6-C2：S6-B18–B20、B23、B25；F02、F10–F13；B01–B05。

发现的问题：

1. **C1 不是完整的生产业务纵切。** C1 引入了候选 Pane、详情、确认和 takeover，但把
   候选关闭、HomePage 卸载、登出、身份失效、closing 后重开（B18–B20）全部放到 C2。
   即使计划声明“中间补丁不部署”，C1 作为可审查的编码单元仍缺少新增异步控制器最基本的
   终止和清理路径。
2. **C2 不只是验证。** 按 §15，C2 对应 S7，而 S7 仍要修改 HomePage、TerminalPane、
   SessionManager、`session/takeover.ts`、`api/sessions.ts`。因此 C2 是“实现 + 验证”，
   不是单纯的竞态或生命周期回归阶段。
3. **UI 样式验证错位。** S6-C1 已经新增候选或确认 UI，但 S6-B25 被放在 C2。样式复用
   应在 UI 首次进入 C1 时验证，C2 再做集成回归，否则 C1 可能先形成独立视觉体系。
4. **缺少关键测试项：**
   - D2 stop/drain 期间用户点击“刷新 Session 状态”或“手动恢复”；
   - SessionManager 卸载后重新挂载，再次打开同一 Session；
   - `replaying` 阶段的 D2 分类；
   - 候选 Pane 初始化失败后的 `pending` 释放、失败状态和同一标签重试；
   - 身份对象被替换而非先置空时的旧任务隔离；
   - HomePage 卸载后异步 `dispose` 未完成时，新页面或新 Pane 打开同一 Session 的竞争。

S7 的远端关闭和完整登出重做没有被明显提前纳入 S6 主体；但 C1/C2 与 S6/S7 的边界
目前不够清晰，需要重新定义“C1 完成什么、C2 验证什么”。

## G. UI 样式复用审查

现有可复用依据充分：

- 组件：`frontend/src/components/layout/Modal.vue`，支持 `dialog-header`、
  `dialog-body`、`dialog-footer` 和现有 `icon-button`。
- 设计令牌：`--page`、`--panel`、`--hover`、`--text`、`--secondary`、`--line`、
  `--strong`、`--on-strong`、`--red`、`--overlay`（`base.css:2-31`）。
- 按钮和提示：`.primary`、`.danger`、`.notice`、`.muted`、`.error`
  （`base.css:60-68`、`106-116`、`289-296`）。
- 会话列表：`.session-manager`、`.session-toolbar`、`.session-table`、
  `.session-status`、`.snapshot-badge`（`base.css:474-624`）。
- 终端区域：`.terminal-tabs`、`.terminal-actions`、`.connection-state`
  （`base.css:418-470`）。
- 弹窗布局：`dialog`、`::backdrop`、`.dialog-header`、`.dialog-body`、
  `.dialog-footer`（`base.css:297-330`）。

结论：S6 不需要新增全局 CSS，也不需要新的颜色、按钮、弹窗或状态标识体系。候选摘要和
确认弹窗可以完全沿用现有 `Modal`、`.details`、`.notice`、`.session-status` 和
`.terminal-actions`。当前设计满足“沿用现有页面风格、不做视觉改版”的约束。

## H. 问题清单

### BLOCKER

1. **所有权移交点没有可执行的 ready/failed 握手。**

   文档要求 `pending` 覆盖 Pane 创建、mount、初始化和 `SessionTakeover` 绑定
   （`stage-5-implementation-plan.md:405`、`411`、`424`），但又把释放时机写成
   `setPane(id, handle)` 成功登记。`setPane` 只是 HomePage 的模板 ref 回调，不能证明
   `TerminalPane.onMounted` 已完成、Client 已创建、SessionTakeover 已绑定。当前
   `TerminalPane` 只 emit `closed`（`TerminalPane.vue:25`），初始化异常被内部 catch
   （`TerminalPane.vue:309-311`），HomePage 没有 `ready` 或 `failed` 事件可依赖。

   结果是：`pending` 可能过早释放；初始化失败后同一标签没有明确的重试或重建协议；
   HomePage 无法准确判断所有权何时移交。需要补充明确的 Pane `ready/failed` 回调、
   失败状态和 `pending` 释放规则。

2. **人工恢复与主动接管没有真正的互斥接口。**

   文档多处要求“所有主动动作共用一个本地互斥意图”“封闭原人工候选”
   （`stage-5-implementation-plan.md:394`、`425`、`743`、`755`），但没有定义
   `SessionRecovery` 如何被 takeover 阻塞。当前 `SessionRecovery.refresh()` 在
   `acting=false`、`client.running=false` 时仍可发起详情查询并生成 candidate
   （`recovery.ts:51-95`），`recover()` 也可启动 attach（`recovery.ts:96-119`）。

   在 D2 的 `client.stop()` 完成之后、takeover 确认之前，用户仍可点击“刷新 Session
   状态”或“手动恢复”，形成第二个详情候选或先启动 attach 任务；随后 takeover 的
   `connectTakeover()` 会因任务 mode 不同而失败。该竞态没有被测试矩阵覆盖，也没有
   `blocked` 谓词或等价门禁设计。

### MAJOR

3. **S5 基线与实际代码不一致。**

   `stage-5-implementation-plan.md:56` 仍称 `client.ts`“所有 Prepare 写死 attach”，
   `957` 仍把“S5 Client 初始 takeover 模式”列为未来步骤，`978` 仍把 `client.ts`
   列为需要修改的文件。实际 `client.ts:240-245`、`399-412`、`750-768` 已实现
   takeover、mode latch、generation 规则和 stop 屏障，S5-C2 测试也已覆盖。该矛盾
   会导致 S6-C1 误改已冻结的 S5 代码或错误扩大范围。

4. **D2 对 `replaying` 没有明确分类。**

   `client.ts:567` 会在 `bind_success` 完整前缀后发布 `replaying`；
   `TerminalPane.vue:86` 显示“正在回放”。`replaying` 是已成功连接、仅输入门禁未开的
   状态，不应被当作“恢复中”而允许“停止恢复并准备接管”。当前 §11.2 只写“在线、
   恢复中、已停止、关闭中”，没有明确 `replaying` 属于在线还是恢复中。该歧义会直接
   影响 D2 的“只定位”行为。

5. **C1/C2 边界把核心生命周期清理推迟到 C2。**

   S6-C1 引入候选 Pane 和 takeover，但 B18–B20（Pane 关闭、HomePage、登出或身份失效、
   closing 后重开）在 C2。C1 因此不是完整可用的生产纵切；至少应把新增控制器的
   invalidate、abort、stop/dispose 基本清理放在 C1，C2 只做集成竞态和 S7 关闭反馈。
   另外 B25 样式验证在 C2，但 C1 已经引入 UI，样式检查也应前移。

6. **身份失效只检查非空，缺少身份 epoch 或对象身份规则。**

   文档要求确认时校验“登录身份”（`stage-5-implementation-plan.md:440`、`447`），
   但当前 `TerminalPane.live()` 只判断 `identity.value !== null`
   （`TerminalPane.vue:63-64`）。如果身份对象被替换而没有先置空，旧 Pane 的
   `allowed()` 可能继续为真。S6 需要明确捕获身份对象或 epoch，并在确认和迟到回调中
   比较同一身份，而不是只判断“已登录”。

### MINOR

7. **`closing` 的释放缺少可观察的 dispose 完成信号。**

   文档要求 `closing` 保持到 Pane 完成 `stop`/`dispose` 并卸载
   （`stage-5-implementation-plan.md:406`），但当前 `TerminalPane.onBeforeUnmount`
   只是 `void dispose()`，HomePage 只能观察模板 ref 变为 `null`，不能确认底层 Client
   的异步 stop/drain 已完成。应明确 `closing` 是由 Pane 的 `disposed` Promise 释放，
   还是由 HomePage 的显式 close 方法等待后释放。

8. **候选 Pane 初始化失败后的“同一标签重试”没有实现路径。**

   文档允许“用户可显式重试同一标签的初始化”
   （`stage-5-implementation-plan.md:414`），但当前 Pane 的初始化只在 `onMounted`
   执行一次，失败后没有重新初始化入口；重新 mount 需要新的 key 或显式 reset 协议。
   应定义失败状态、重试方法和是否保留同一 Client 实例。

9. **测试矩阵缺少 SessionManager remount 后同一 Session 的定位测试。**

   S6-B06 只覆盖卸载；没有覆盖“重新挂载列表后再次打开同一 Session，仍定位原 Pane”
   的场景。该场景是用户要求的第 14 项竞态。

10. **测试矩阵缺少 D2 stop/drain 期间的其他用户动作。**

    S6-B08 只验证 stop 屏障，没有验证在 stop 未完成时点击“刷新 Session 状态”、
    “手动恢复”或关闭确认的行为。该场景正是当前 `SessionRecovery` 与 takeover 互斥
    缺口的触发条件。

### INFO

11. 主文档文件头写第二轮文档基线 HEAD `56ed638`
    （`stage-5-implementation-plan.md:6`），而修订报告和 precheck 使用 `feb0ac5`。
    这不是本次代码与 ZIP 的差异问题，但会造成基线阅读混乱，建议统一。

12. 当前没有 `frontend/src/session/takeover.ts` 和
    `frontend/tests/session-takeover.test.mjs`，S6 专项测试尚未存在；这符合“未开始
    S6-C1”的状态，不应被误报为已完成。

## I. 最终结论

**BLOCKED**

原因不是 S5 方案 A 本身不兼容；`TerminalClient` 的 takeover、mode latch、generation、
`BIND_CONFLICT`、Promise 分离和 stop 屏障均已实际实现。阻塞点在于 S6 文档仍有两个
直接涉及所有权和异步安全的设计缺口：

- 候选 Pane 的初始化或绑定完成没有可观察的 ready/failed 协议，`pending` 释放与所有权
  移交点不一致；
- `SessionRecovery` 与 `SessionTakeover` 没有明确的互斥接口，D2 stop 后仍可能启动
  人工恢复或第二个详情候选。

此外，S5 现状描述过期、D2 的 `replaying` 分类不明确、C1/C2 边界不完整，均需要先修订
文档，不能据此授权 S6-C1 编码。

## J. 未修改确认

本轮只执行读取和 Git 状态检查，没有修改生产代码、测试代码、CSS、配置或依赖，没有执行
commit、push、部署，也没有开始 S6-C1。

当前工作区仍显示上一轮留下的：

- `M doc/stage-5-implementation-plan.md`
- `?? doc/stage-5-s6-plan-b-revision-report.md`
- `?? doc/stage-5-s6-precheck-report.md`

这些不是本轮产生的改动。本轮新增内容仅为本文档
`doc/stage-5-s6-plan-b-second-review-report.md`。
