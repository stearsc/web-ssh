# Web SSH 第五阶段 S6 设计审查报告

- 编写日期：2026-11-18
- 审查对象：`doc/stage-5-s6-design-plan.md`
- 代码基线：分支 `S6`，HEAD `feb0ac5 S5-C2`
- 结论：第三轮定点修订已完成，本报告包含第三轮修订后的内容。第二轮已处理 BLOCKER-1/2、MAJOR-1/2/3，U1–U9 已按用户裁决登记；第三轮补充了 T46-A（Terminal 构造失败）、T46-B（FitAddon 构造失败）的构造阶段异常安全（§0A），未发现新的阻塞项，建议进入最终人工审查。**没有开始编码，没有执行 S6-C1，没有 commit、push 或部署。**

---

## 0. 第二轮定点修订记录（2026-11-18）

### 0.1 修订范围

只修改三个文档：`doc/stage-5-s6-design-plan.md`、本报告、`doc/stage-5-implementation-plan.md`（仅与方案 B 冲突的部分）。未修改生产代码、测试、CSS、API。未引入 PaneLifecycle、PaneOperationGate、资源注册表或其他通用抽象。

### 0.2 本轮新增核对的真实代码

- `frontend/src/terminal/client.ts`：类字段只初始化字面量；构造函数先做 9 个字段赋值，最后一条语句是 `this.output = new TerminalOutputTracker(...)`。
- `frontend/src/terminal/output.ts`：类字段只初始化字面量、`[]`、`new Set()`；构造函数先做 5 个字段赋值，最后一条语句是 `this.terminal = createTerminal()`；`replaceTerminal()` 在运行期也调用同一工厂。
- `frontend/src/components/terminal/TerminalPane.vue`：现有工厂在 `open` 后立即写入 `terminal`/`fit`，订阅注册失败时没有回滚；window 监听在初始化第一步注册。
- `frontend/src/pages/HomePage.vue` `signOut`：stop 与 logout 并行，`allSettled` 结果未检查。
- `frontend/src/stores/auth.ts`、`router/index.ts`、`LoginPage.vue`、`api/management.ts`：`authNotice` 已导出且由 LoginPage 展示；logout 成功后 `logoutPending` 复位，跳转时守卫调用 `checkIdentity()`，`request()` 遇到 AUTH_REQUIRED 会调用过期处理，可能改写 `authNotice`（按代码推断，未运行验证）。

### 0.3 问题修复对照表

| 审查项 | 修改位置 | 解决方式 |
|---|---|---|
| BLOCKER-1 构造期资源所有权 | 设计 §3.2、§3.3、§4.2、§4.3、§7.2、§7.4、R27–R29 | 按真实代码证明 `createTerminal()` 是构造链最后一步：构造抛错时 Client、Tracker 均未持有外部资源，只可能遗留工厂内部资源，由工厂逆序回滚并重新抛出原错误；回滚失败记入 `terminalCleanupError`，使 `close()` 失败。Client 构造返回后立即归 Pane。资源表增加“创建失败回滚者”列。构造顺序作为 C1 前置不变量写入停止条件 |
| BLOCKER-2 权威实施计划冲突 | 实施计划头部、§8.1、§8.2 第 1/10 步、§8.3 图 2 后、§11.3、§12.1、§12.2、§15 S6/S7 行、§16 两行、F12、§21.2 D3 行、新增 §21.5 | 旧规则保留原文并标注“已被方案 B 替代”，在同处写明现行规则；§21.5 汇总方案 B、被替代规则清单和 U1–U9 |
| MAJOR-1 三个时点准入 | 设计 §8.1、§8.2、§9、§10.2、§10.3、§14、R30、R31 | 抽出 `admissionError()`（allowed、isEnded、canBegin）和 `startError()`（另加 `!client.running`）；在 prepare 开始、GET 返回后、`connectTakeover` 紧前三处检查；检查点 3 与调用之间无 await，仍用候选快照代次；新增 `canConfirm` 仅供按钮禁用 |
| MAJOR-2 初始化部分失败 | 设计 §5、§7.1–§7.4、§12.1、§12.2、§12.4 | 情况 A 零资源零请求；情况 B 由失败现场同步回滚监听器和 Observer，并立即启动 `client.dispose()`（失败用 `showCleanupFailure` 展示）；failed 与 closed 分离，failed 保留记录和预占；`close()` 等待同一 dispose 结果并检查 `terminalCleanupError` |
| MAJOR-3 signOut 失败语义 | 设计 §6.5、R32、R33 | 同步 logoutPending → 同步 stop → 立即 logout（与排空并行，保持实施计划 §12.2）→ 等 stop → 成功后 close → 跳转；stop 失败与 close 失败分别统计、按 Session ID 写日志、跳转后写入 `authNotice`；logout 失败不 close、不跳转 |
| U1–U9 | 设计 §19.1；实施计划 §21.5 | 全部改为“已裁决”，并列出落点；残留风险单列于设计 §19.2 |
| S6-T46 | 设计 §17 | 工厂各步骤抛错与回滚抛错 |
| S6-T47 | 设计 §17 | Observer 创建或 observe 失败后的现场回滚与关闭 |
| S6-T48 | 设计 §17 | confirming 中 canBegin 变假，confirm 拒绝 |
| S6-T49 | 设计 §17 | GET 返回前准入失效或结束，不进入 confirming |
| S6-T50 | 设计 §17 | signOut 时 stop/close 失败及 logout 失败的行为与反馈 |

### 0.4 对人工补充思路的处理

| 思路 | 处理 |
|---|---|
| 构造过程自身保证异常安全，不引入全局注册表 | 采纳。差异：Client 构造返回后立即赋给 Pane，而不是全部成功后才移交；原因是 `client.dispose()` 异步且结果缓存，init_failed 的 Pane 必须持有句柄才能在关闭时判断清理结果。构造返回的 Client 是完整对象，不是半成品 |
| ready、failed、closed 三个概念分开 | 采纳，见设计 §5.1 |
| 最终检查紧贴 `connectTakeover` | 采纳，见设计 §10.3 |
| signOut 先等 stop 屏障再 logout | **部分采纳**：保留 stop 与 close 失败分开处理；但 logout 与本地排空保持并行，不先等 stop。理由：实施计划 §12.2 继承第四阶段冻结语义，准入已同步封闭，先等 drain 不增加安全性，只会推迟服务端撤销最多 `write_drain_timeout` |
| 额外调整 | “关闭 SSH 会话”不再因接管进行中禁用，以符合实施计划 §11.3“关闭意图优先”；两个 Modal 的互斥由准入检查和 `showModal` 保证 |

---

## 0A. 第三轮定点修订记录：T46 构造阶段异常安全（2026-11-18）

### 0A.1 修订范围

只修改设计方案 §7.4（新增 §7.4.1、更新 C1 前置不变量；后续定点修正开头的回滚表述：工厂要么返回完整端口，要么在尽最大努力回滚后抛出原始初始化错误，回滚失败时记录清理错误并使 Pane 关闭失败）、§15.1 测试文件说明、§17（新增 S6-T46-A、S6-T46-B，原 T46 保留不变）、§18.1、§19.2，以及本节。未修改实施计划、生产代码或测试代码；未运行测试或构建。

### 0A.2 实际核对的代码和依赖

- `frontend/package.json` 第 16–17 行、`frontend/package-lock.json` 约第 650–660 行：`@xterm/addon-fit` 0.11.0、`@xterm/xterm` 6.0.0。项目目录没有 `node_modules`；使用临时副本 `/tmp/codex/s6-ui/frontend/node_modules` 中的同版本包，锁文件 version 与 integrity 一致。
- `@xterm/xterm/src/browser/public/Terminal.ts` 第 32–54 行：构造 `CoreBrowserTerminal` 和 `AddonManager`，并设置选项代理。
- `@xterm/xterm/src/browser/CoreBrowserTerminal.ts` 第 149–181 行（构造函数）、第 234–238 行（`_setup`）、第 393 行起（`open`，DOM 创建与监听注册都在这里）、第 177–180 行（dispose 时移除 element）。
- `@xterm/xterm/src/common/CoreTerminal.ts` 第 99–143 行（构造函数）、第 240–242 行（`_setup`）。
- 构造链中各服务、InputHandler、WriteBuffer、DecorationService、LinkProviderService、OscLinkProvider、AddonManager 的构造函数：未发现 DOM、全局监听、计时器或 Worker。
- `@xterm/xterm/src/common/services/OptionsService.ts` 第 73–99 行：选项校验错误被捕获，不向外抛出。
- `@xterm/xterm/src/vs/base/common/lifecycle.ts` 第 21–22 行、第 468–498 行：全局 disposable 跟踪默认关闭；`Disposable` 构造不登记全局表。
- `@xterm/addon-fit/src/FitAddon.ts` 第 26–33 行：无构造函数，`dispose()` 为空操作。
- `frontend/src/components/terminal/TerminalPane.vue` 第 230–275 行：当前工厂没有 try/catch。
- `frontend/tests/terminal-pane.test.mjs` 第 28–36 行：`Terminal` 替身经 `globalThis` 注入，`FitAddon` 是固定替身。

### 0A.3 T46-A：Terminal 构造失败

- **结论**：调用方拿不到实例，无从释放；xterm 6.0.0 构造函数**不会**自行回滚已注册的内部对象，但构造期间只创建内存对象，没有 DOM、全局监听、计时器或全局登记，可被垃圾回收。因此工厂把 `new Terminal()` 放在 try 之前是正确的，资源所有权已闭合。
- **未确认边界**：结论来自随包源码，未与运行时加载的 `lib/xterm.mjs` 逐行比对；xterm 没有文档承诺。升级 xterm 时必须重新核对，已写入 C1 前置不变量。测试替身不能证明 xterm 内部行为。
- **验收断言**：见设计 §17 S6-T46-A。

### 0A.4 T46-B：FitAddon 构造失败

- **结论**：此时 Terminal 已创建并由工厂持有；`new FitAddon()` 位于 try 内，catch 中执行 `term.dispose()` 后重新抛出原错误。`term.dispose()` 失败时，错误记入 `terminalCleanupError`，`close()` 因此 reject，不会误报清理成功，Pane 保留记录和预占。FitAddon 本身无需释放。
- **验收断言**：见设计 §17 S6-T46-B（B1 正常回滚，B2 回滚失败）。
- **C1 测试前提**：`FitAddon` 替身需改为可经 `globalThis` 替换。

### 0A.5 阻塞项结论

**没有发现新的设计阻塞项。** 当前真实代码中的工厂缺少回滚，是已知的现有缺陷，由第二轮的 §7.4 设计在 C1 修复；本轮没有改变资源所有权模型、Client 移交时机、ready/failed/closed 语义、预占规则或 S5 连接契约。

---

## 1. 实际检查的文件

### 1.1 生产代码（S6 分支）

- `frontend/src/terminal/client.ts`：`start`、`stop`、`dispose`、`running`、`connectTakeover`、`loop` 的 finally、`nextAttemptGeneration`、`publish`
- `frontend/src/terminal/output.ts`：`settle`、`dispose`、`replaceTerminal`
- `frontend/src/terminal/recovery.ts`：全文
- `frontend/src/components/terminal/TerminalPane.vue`：全文
- `frontend/src/pages/HomePage.vue`：全文
- `frontend/src/components/session/SessionManager.vue`：全文
- `frontend/src/session/list.ts`
- `frontend/src/api/sessions.ts`、`frontend/src/api/management.ts`（`request` 的 AUTH_REQUIRED 处理）、`frontend/src/api/client.ts`
- `frontend/src/stores/auth.ts`、`frontend/src/router/index.ts`、`frontend/src/pages/LoginPage.vue`
- `frontend/src/components/layout/Modal.vue`、`frontend/src/styles/base.css`（`.notice`、`.dialog-*`、`.terminal-*`、`.session-*`、`.primary`、`.danger`）
- `backend/internal/session/binding.go`（`canBind` 的错误码）、`backend/internal/session/session.go`（SESSION_NOT_FOUND）、`backend/internal/httpapi/response.go`

### 1.2 测试

- `frontend/tests/terminal-pane.test.mjs`（SFC 编译 harness、`.vue` 子组件替身、`mountPane`）
- `frontend/tests/session-list.test.mjs`（列表控制器、SessionManager、HomePage harness）
- `frontend/tests/terminal.test.mjs`、`stage-5-c2.test.mjs`、`auth.test.mjs`、`management.test.mjs`、`client.test.mjs`（确认覆盖范围）
- `frontend/package.json`（测试和构建命令）

### 1.3 文档

- `doc/stage-5-implementation-plan.md`（§8.1、§8.2、§12.1、§12.2、§16、S6/S7 阶段行、F05–F09、D1–D3 决策表）
- `doc/测试报告/` 目录（S2–S5 报告命名惯例）
- 旧 S6 材料（仅 `codex/第五阶段` 分支，用 `git show 9e0b69b:doc/...` 读取）：`stage-5-s6-precheck-report.md`、`stage-5-s6-plan-b-revision-report.md`、`stage-5-s6-plan-b-second-review-report.md`、`stage-5-s6-plan-b-second-revision-report.md`、`stage-5-s6-plan-b-third-revision-report.md`

### 1.4 页面

- 在临时副本 `/tmp/codex/s6-ui/frontend` 中，用 mock API（`/tmp/codex/s6-ui/mock/server.mjs`）加 Vite 启动页面并截图检查：会话管理表格、终端工具栏、Modal 样式。mock 和 Vite 进程已停止。

---

## 2. 当前代码的真实状态

1. **Pane 初始化**全程同步，没有 ready/failed 报告；初始化失败只写入提示。
2. **关闭缺陷**：`dispose()` 吞掉错误，`closeLocal()` 仍然 `emit('closed')`，导致清理失败时标签被移除、Session ID 预占被释放。S6 设计修复此问题。
3. **唯一性缺失**：`HomePage.connectServer` 直接 push，没有去重；SessionManager 没有打开入口。
4. **HomePage 的 key** 使用 session id，存在“同一 flush 内关闭再打开会复用旧实例”的隐患（目前没有重新打开入口，所以尚未暴露）。
5. **`client.dispose()`、`output.dispose()` 缓存失败结果**，同一实例无法重试清理。
6. **SessionRecovery** 通过注入 `allowed` 就能与 takeover 互斥，无需修改。
7. **身份**：`/me` 只替换 identity 对象，Pane 不会被误停；`request()` 只在 AUTH_REQUIRED 时调用过期处理；路由守卫把网络错误也当作 `clearIdentity`（现有风险）。
8. **测试 harness**：Pane 测试中所有 `.vue` 子组件替身都不渲染，确认框插槽无法断言，C1 需要调整。新的 `.ts` 模块会被 Node 直接加载，需要可擦除的 TS 语法和 `.ts` 后缀导入。
9. **基线**：在临时副本执行 `npm ci` 后，`npm test` 68/68 通过（项目本身没有 `node_modules`，不在项目目录安装）。

---

## 3. 旧文档与真实代码的差异

| 旧材料 | 内容 | 与代码或冻结决策的关系 |
|---|---|---|
| 实施计划 §8.1 | 建议在 client 增加主动入口 | S5 已实现 `connectTakeover`，S6 直接使用 |
| 实施计划 §12.1 | 离开管理页时取消未确认动作并移除未启动候选 | 与方案 B 第 6 条冲突（管理页卸载不得取消 Pane 动作），已被替代 |
| 实施计划 S6 行 | 修改 `recovery.ts` | 不需要：通过注入 `allowed` 实现互斥 |
| 实施计划 §16 | `session/takeover.ts` | 本设计放在 `terminal/takeover.ts`（U8） |
| 旧 S6 修订 | PaneLifecycle 加 registerResource | 初始化是同步的，屏障解决的是不存在的问题；“构造后 register”是伪屏障 |
| 旧 S6 修订 | PaneOperationGate | 与 SessionRecovery 自身的任务控制重复；单线程下同步标志加 sequence 已足够 |
| 旧 S6 修订 | 可重试 dispose、PaneDisposeResult 结果对象 | `client.dispose` 缓存失败，不可实现；且与 Promise 语义混用 |
| 旧 S6 修订 | 导出 identityEpoch 并改造 auth/router | HomePage 生命周期已嵌套在登录周期内，不需要 |
| 旧 S6 修订 | tombstone 集合 | 记录在 closed 之前保留即可，不需要额外集合 |

---

## 4. 采用的设计及原因

| 设计 | 原因 |
|---|---|
| TerminalPane 直接用 `life` 枚举管理生命周期，不引入独立生命周期对象 | 初始化同步；资源所有者唯一；只有一套状态 |
| 两段式初始化：先校验后创建；工厂局部回滚；失败现场回滚同步资源并启动 Client 清理 | 消除“先创建后检查”；任何时刻每个资源都有明确所有者，不留无主资源 |
| SessionTakeover 三个检查点 | 异步边界前后的状态都可能变化，最终检查与调用之间不留 await |
| HomePage 只用一个 `records` 数组；`handle === null` 表示 opening | 去掉 tabs、panes、pending、closing 的重复状态；同步 push 即预占 |
| 单调 key 加按 key 路由事件 | 区分 Vue 实例身份与 Session ID；隔离旧实例回调 |
| ready、failed 都登记句柄；只有 closed 移除记录 | 初始化失败的标签也能关闭；清理失败不释放预占 |
| 新增 SessionTakeover，与 SessionRecovery 平级 | 接管有独立阶段（stop、查询、确认、连接），放进 Pane 会让组件过于臃肿；无长期资源，不形成第二套生命周期 |
| `prepare()` 以 `await client.stop()` 作为屏障 | 满足 D2“完整 stop/drain 之后才查询” |
| takeover 可抢占 recovery，recovery 不能抢占 takeover；`takeover.invalidate()` 只在 stop、close 内调用 | 互斥规则可穷举，无死锁；connecting 阶段的失效一定伴随 Client 停止 |
| 统一使用 Promise reject 表示清理失败 | 单一语义，错误链清晰 |
| 不修改 auth、router | 四层生命周期天然嵌套，不需要新增 epoch |
| D1 通过 Pane 的 `changed` 事件刷新列表 | 不让 SessionManager 持有 Pane 状态；不轮询 |
| C1 是完整纵切，C2 负责验证深度 | 基本安全能力不能拆到 C2 |

---

## 5. 旧设计的保留、替换、废弃

| 类别 | 项目 |
|---|---|
| 保留 | ready/failed 握手思路；D2 把 replaying 视为在线；C1 必须包含清理能力；复用 Modal 和现有样式；Pane 私有动作对象的定位 |
| 替换 | PaneLifecycle 和 registerResource → Pane `life` 加两段式初始化；PaneOperationGate → 注入 `allowed` 加 sequence；PaneDisposeResult → Promise reject；identityEpoch 改造 → 依赖生命周期嵌套；tombstone 集合 → 记录保留到 closed |
| 废弃 | 可重试 dispose（与 S5 冻结实现冲突）；离开管理页取消候选（与方案 B 冲突）；修改 `recovery.ts` |

---

## 6. 反向一致性审查

| 问题 | 结论 |
|---|---|
| 状态是否有遗漏 | Pane `life` 6 个状态、Takeover 5 个阶段、HomePage 记录 3 种状态，均已列出转换；登出或身份失效通过 `live()` 外部条件处理，不另设状态 |
| 是否存在无法退出的状态 | `dispose_failed` 只能通过页面卸载退出，这是 S5 缓存决定的，已列为 U1；takeover 的 connecting 必然退出（connection Promise 一定 resolve，或被 stop/close 失效）；stopping 和 loading 在准入消失时就地复位 |
| 是否有两个对象同时拥有同一资源 | 否，见设计 §4.2；xterm 返回前归工厂、返回后归 Tracker，Pane 只持有引用 |
| 初始化失败后是否有无人负责的资源 | 否：构造抛错只可能遗留工厂资源，工厂自行回滚；构造返回后的失败由 Pane 失败现场回滚；回滚失败记入 `terminalCleanupError`，使 close 失败、保留预占 |
| 关闭与异步初始化竞态 | 初始化无 await；挂载前登出由前置校验拦截 |
| stop 与 takeover 竞态 | stop 是 prepare 的屏障；三处准入检查；检查点 3 包含 `running`；connecting 阶段的 stop 会使 takeover 失效 |
| confirm 是否依赖 UI 禁用 | 否：`confirm()` 自身执行检查点 3，`canConfirm` 只是辅助 |
| GET 返回后状态已变是否仍显示确认框 | 否：检查点 2 失败回到 idle |
| 旧身份与新身份交叉 | 新登录只出现在新的 HomePage 中；旧 Pane 的 `life` 和 sequence 均已失效 |
| 是否存在 dispose 失败却释放 Pane 的路径 | 否：只有 closed 移除记录，而 closed 只在 dispose 成功且无工厂回滚错误时发出；signOut 场景因页面跳转卸载，属于 U9 已裁决的代价，并有日志和反馈 |
| signOut 是否静默丢弃失败 | 否：stop 与 close 失败分别统计、记录和反馈 |
| 权威实施计划是否仍有与方案 B 冲突的有效规则 | 否：已逐处标注替代并在 §21.5 汇总；§6 列表卸载、§11.1、§11.2 A–E 经检查无冲突 |
| 用户取消后旧任务是否会复活 | 否：cancel 和 invalidate 不调用 Client 的任何启动方法；Recovery 不会自动重启 |
| 是否存在确认前连接的路径 | 候选 Pane 只执行 `stop()` 和 GET；`connectTakeover` 只在 `confirm()` 中调用；Pane 的 `connect()` 只在 attach 模式调用；人工 attach 需要用户两次显式点击（U3） |
| 是否与 S5 方案 A 冲突 | 否：只调用 `connectTakeover(G)`；不自动换代；首次之后只 attach；不修改 `client.ts`、`output.ts`；BLOCKER-1 只改 Pane 内工厂 |
| 是否与 D1–D3 冲突 | 否：D1 用 changed 加挂载和可见性刷新；D2 的按钮、屏障、取消规则已落实；D3 候选、失败保留、单 Pane 已落实 |
| 是否违反 UI 风格约束 | 复用 Modal、`.notice`、`.terminal-actions`、默认和 `.primary` 按钮；CSS 默认不改 |

---

## 7. 用户裁决（U1–U9 已裁决）

U1–U9 已由用户裁决，详见设计方案 §19.1 和实施计划 §21.5，不再列为待决。与真实代码没有无法兼容的冲突。残留风险见设计 §19.2：路由守卫网络错误（U6）、跳转后 `authNotice` 可能被改写（S6 在跳转完成后写反馈，C1 测试确认）、signOut 时 close 失败可能残留 xterm（U9 代价）、构造顺序前置不变量。

---

## 8. 验证状态

| 项目 | 状态 |
|---|---|
| 只读阅读代码、测试、文档 | 已执行 |
| 在临时副本执行 `npm ci` 后运行基线 `npm test` | 已执行，68/68 通过（仅作基线，不涉及 S6 改动） |
| mock 页面截图检查现有 UI | 已执行 |
| 第二轮：只读核对构造链、signOut、auth/router 代码 | 已执行 |
| S6-T01 至 S6-T50（含 T46-A、T46-B） | **均为计划，尚未执行** |
| 第三轮：只读核对 xterm 6.0.0、addon-fit 0.11.0 源码及 Pane 工厂、测试替身 | 已执行 |
| `npm run build`、后端测试、端到端 | 未执行 |
| 第二轮修订后的任何测试或构建 | 未执行（本轮只修改文档） |

---

## 9. 声明

- 第一轮新增 `doc/stage-5-s6-design-plan.md` 和本报告；第二轮修订这两份文档，并按 U7 修订 `doc/stage-5-implementation-plan.md` 中与方案 B 冲突的部分。未修改生产代码、测试、CSS、配置、API、S5 实现和其他文档。
- 未开始 S6-C1，未 commit、push 或部署。
- 等待最终人工审查和明确的编码授权。
