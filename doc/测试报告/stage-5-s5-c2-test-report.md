# Web SSH 第五阶段 S5-C2 测试与回归报告

## 1. 基线与范围

- 分支：`codex/第五阶段`
- 基线 HEAD：`ed918382823274259449ec7dd9d6073e06012ec6`
- 开始前工作区干净，暂存区为空；`luan-qi-ba-zao.txt` 无未提交修改。
- 本轮只增加 S5-C2 专项测试、原始验证日志和本报告。
- 未修改生产代码、测试辅助生产接口、冻结设计、协议、API、后端业务代码或 S6–S8 内容。
- 未执行 commit、push、merge、rebase、cherry-pick、reset、clean 或部署。

## 2. 修改文件

| 文件 | 状态 | 用途 |
|---|---|---|
| `frontend/tests/stage-5-c2.test.mjs` | 新增 | M01–M08、P01–P07、稳定资格 mode 闩锁和 S6-PRE01–03 边界的确定性专项测试。 |
| `doc/测试报告/stage-5-s5-c2-test-report.md` | 新增 | 本轮测试矩阵、结论、命令、统计、证据和 Git 状态。 |
| `doc/测试报告/stage-5-s5-c2-logs/` | 新增 | 前端、后端、Race、Vet、Build 原始日志及退出码。 |

未修改生产文件。专项测试使用可控 fetch、Mock WebSocket、Deferred Promise、明确任务/socket 身份和 mock timer；没有使用固定 sleep。

## 3. 测试实现说明

### 3.1 任务身份与生命周期

专项测试直接断言：

- 同 mode、同 initial generation 返回同一 connection Promise、同一 completion 和同一 task ID；
- attach/takeover 双向 mode 冲突均在启动新任务前同步拒绝；
- 同 mode、不同 initial generation 同步拒绝；
- 活动任务存在时，非法 generation 和非法 mode 仍先校验；
- stop 请求后立即封闭新任务准入；
- pending output drain 未完成时 `running=true`，新 start 返回 `task_stopping`；
- stop 完成、输出排空和旧任务退出后才允许新任务，新任务具有独立 task ID 和 socket。

### 3.2 旧回调隔离

测试保存旧任务在 stop 前的 WebSocket `open/message/error/close` handler 和旧 xterm write callback。新 takeover 任务建立并更新 generation 后，再触发全部旧回调：

- 新任务 generation 不变；
- 新 socket 保持 OPEN；
- 新 socket 不产生额外发送；
- RecoveryState 不新增旧事件状态；
- 旧 write callback 不推进新任务 offset；
- 旧 Prepare 在 stop 后迟到时不创建 WS，不二次 settle，不影响随后新任务。

公共 API 下，旧 loop 的 `finally` 不能在新任务启动后迟到，因为 `stop()` 等待旧 `task.done`、`sendTask` 和 output drain 后才结束，且 `stopping` 屏障在收尾完成前拒绝新任务。该顺序由 M06-M07/P07 测试固定，没有为测试增加生产 hook。

### 3.3 首次 Promise 与长期任务

专项测试明确区分：

- `connection` Promise 只表示首次连接前缀结果；
- `completion` 只在长期任务结束时完成；
- 首次可恢复失败使 connection 返回 `connected=false`，但 `running=true`、completion 保持 pending，随后仍产生下一轮 attach；
- 首次成功使 connection 返回 true，completion 仍 pending；
- connection 已经 settle 后，stop 只结束长期任务，不改变已 settle 的 connection 结果；
- 旧任务 connection 不因新任务或迟到回调再次 settle。

### 3.4 方案 A

测试确认：

- 首次任务若为 takeover，只有第一次 Prepare 使用 takeover；
- 第一次失败后，同一长期任务的所有后续 Prepare 固定 attach；
- `initialModeConsumed` 与 retries/稳定资格完全分离；
- 尚未获得合法 `bind_success` 时，后续 attach 使用不可变 `initialExpectedGeneration`；
- 收到合法 `bind_success` 后，`confirmedGeneration` 更新，后续 attach 使用该值；
- 不根据 takeover 自动推算 G+1；
- 不自动 GET 详情或列表；
- 不自动再次 takeover；
- bind_success 丢失后，服务端返回 `BIND_CONFLICT` 时停止；
- 不可恢复首败立即停止；
- 预算耗尽后停止，期间没有重新 takeover；
- 稳定资格重置 retries 后，后续仍 attach。

generation 选择保持：

```text
first attempt: initialExpectedGeneration
later attempts: confirmedGeneration ?? initialExpectedGeneration
```

### 3.5 S6 调用方边界

S5-C2 没有实现 S6 UI。测试只验证现有调用层边界：

- `SessionRecovery.recover()` 仍通过默认 `client.connect(generation)` 发送 attach；
- 首次 connection 返回 false 而长期任务仍 running 时，`SessionRecovery.canRecover` 为 false；
- 旧任务未结束前不能开始新 takeover；
- 显式 `client.stop()` 完成后，才能通过显式 `connectTakeover(newGeneration)` 启动新任务；
- 自动恢复、首次 false 和详情查询均不触发 takeover。

## 4. M01–M08 测试矩阵

| 编号 | 测试场景 | 测试文件/测试名称 | 结果 | 关键断言 |
|---|---|---|---|---|
| M01 | 同 mode、同 initial generation 复用 | `frontend/tests/stage-5-c2.test.mjs`：`M01-M05` | PASS | 同一 connection、completion、task ID；只有一次 Prepare。 |
| M02 | attach 活动中调用 takeover | 同上 | PASS | `mode_mismatch`；不创建 takeover 请求。 |
| M03 | takeover 活动中调用 attach | 同上 | PASS | 对称 `mode_mismatch`；不取消或并行启动。 |
| M04 | 同 mode、不同 initial generation | 同上 | PASS | `generation_mismatch`；请求数和 generation 不变。 |
| M05 | 活动任务时非法 generation/mode 先校验 | 同上 | PASS | 不因任务存在而返回旧 Promise。 |
| M06 | stopping 且输出排空未完成 | `M06-M07/P07` | PASS | `running=true`；`task_stopping`；无新 Prepare/socket。 |
| M07 | stop 完整完成后启动新任务 | `M06-M07/P07` | PASS | 新 task ID、新 socket；旧 generation 不泄漏。 |
| M08 | 旧 Prepare/WS/write 回调迟到 | `M08：旧 socket...`、`M08/P06：旧 Prepare...` | PASS | 旧事件不改变新 generation、state、socket、发送数或 offset；旧 finally 由 stop 屏障阻止跨任务迟到。 |

## 5. P01–P07 测试矩阵

| 编号 | 测试场景 | 测试文件/测试名称 | 结果 | 关键断言 |
|---|---|---|---|---|
| P01 | 首次 attach 可恢复失败但任务继续 | `P01-P02` | PASS | connection=false；running=true；completion pending；产生下一轮 Prepare。 |
| P02 | 首次失败后后续 attach 成功 | `P01-P02` | PASS | RecoveryState=online；首次 Promise 只 settle 一次。 |
| P03 | 首次连接成功 | `P03-P04` | PASS | connection=true；completion pending；running=true。 |
| P04 | 首次 Promise settle 后 stop | `P03-P04`、`M06-M07/P07` | PASS | connection 值不再变化；stop 等待旧任务和 drain；running 最终 false。 |
| P05 | takeover 首败后的方案 A | `P05-A` 至 `P05-E` | PASS | takeover 至多一次；后续 attach；initial/confirmed generation 选择正确；冲突、不可恢复错误和预算耗尽停止；无 GET 或 G+1 推算。 |
| P06 | 旧任务首次 Promise 在 stop 后迟到 | `M08/P06` | PASS | 旧 Prepare 不建 WS；旧 Promise 不二次 settle；新任务状态不变。 |
| P07 | stop 屏障与新任务准入 | `M06-M07/P07` | PASS | stop 完成前拒绝新任务；完成前 running=true；完成后才允许新 task ID。 |

P05 的覆盖细分：

| 分支 | 测试 | 结果 |
|---|---|---|
| 首次 takeover 可恢复失败，后续 attach 使用 G，最终合法确认 G+1 并 online | `P05-A` | PASS |
| 未收到 bind_success，后续 attach 仍使用 G，冲突停止 | `P05-B` | PASS |
| 已收到合法 bind_success，断线后 attach 使用 confirmed generation | `P05-C` | PASS |
| 后续 attach 仍失败时继续 attach，预算耗尽停止 | `P05-D` | PASS |
| 不可恢复首败停止；缺失合法 generation 不构造请求 | `P05-E` | PASS |

## 6. S6-PRE01–03 测试矩阵

| 编号 | 场景 | 测试文件/测试名称 | 结果 | 说明 |
|---|---|---|---|---|
| S6-PRE01 | SessionRecovery 默认恢复只 attach | `S6-PRE01-03` | PASS | GET 详情后 `recover()` 只发 `{mode:"attach"}`。 |
| S6-PRE02 | 首次 Promise false 不表示长期任务结束 | 同上 | PASS | client 仍 running，Recovery 不允许新动作。 |
| S6-PRE03 | 旧任务停止后才允许显式 takeover | 同上 | PASS | stop 完成前无 takeover；完成后显式 takeover 使用用户给定 generation。 |

S6 UI、Pane 协调和用户确认尚未实现，属于后续阶段；本轮没有把该边界测试描述为 S6 功能验收。

## 7. 既有行为回归

完整前端测试继续覆盖并通过：

- 普通 attach 首次连接和有限恢复；
- takeover 首次成功、失败后 attach、bind_success 丢失和 BIND_CONFLICT；
- `taken_over` 与裸 Close 4001；
- `WS_4001`、接管提示、不自动恢复和有限排空；
- Close 4002 有限恢复；
- OUTPUT_GAP、processedOffset、OutputTracker、output drain；
- 输入门禁、稳定资格、retries；
- 页面/Pane 卸载、dispose、网络状态展示和旧事件隔离；
- SessionRecovery 详情查询、候选消费和人工 attach；
- 登出和 HomePage/Pane 生命周期既有测试。

## 8. 验证命令与结果

### 8.1 前端

| 命令 | 退出码 | 结果 |
|---|---:|---|
| `node --test --test-timeout=10000 tests/stage-5-c2.test.mjs` | 0 | 13 tests，13 PASS，0 FAIL，0 SKIP。 |
| `npm test` | 0 | 68 tests，68 PASS，0 FAIL，0 SKIP，0 cancelled，0 todo。 |
| `npm run typecheck` | 0 | `vue-tsc --noEmit` 通过。 |
| `npm run build` | 0 | typecheck 通过；Vite 转换 57 modules，生产构建成功。 |

### 8.2 后端

Go 版本：

```text
go version go1.26.5 linux/amd64
```

| 命令 | 退出码 | 结果 |
|---|---:|---|
| `go test -count=1 ./...` | 0 | 10 个有测试包的包通过；4 个无测试文件包为 `[no test files]`。 |
| `go test -race -count=1 ./...` | 0 | 与普通测试相同的包级结果；未发现数据竞争。 |
| `go vet ./...` | 0 | 无输出、无告警。 |
| `go build ./...` | 0 | 构建成功。 |
| `go test -count=1 -json ./...` | 0 | 118 顶层测试 + 446 子测试，全部 PASS；SKIP=0；10 包 PASS，4 包无测试文件。 |
| `go test -race -count=1 -json ./...` | 0 | 118 顶层测试 + 446 子测试，全部 PASS；SKIP=0；10 包 PASS，4 包无测试文件。 |

Race 日志检查：

```text
DATA RACE 匹配数：0
WARNING: DATA RACE 匹配数：0
```

4 个无测试文件包没有被计入通过测试数，也没有被误报为测试失败。

## 9. 原始证据

日志目录：

`doc/测试报告/stage-5-s5-c2-logs/`

主要内容：

- `frontend-tests.txt` / `.exit`
- `frontend-typecheck.txt` / `.exit`
- `frontend-build.txt` / `.exit`
- `backend-tests.txt` / `.exit`
- `backend-tests.json`
- `backend-race.txt` / `.exit`
- `backend-race.json`
- `backend-vet.txt` / `.exit`
- `backend-build.txt` / `.exit`
- `backend-stats.txt`
- `go-version.txt`

所有已执行命令退出码均为 0。

## 10. 并发与生命周期结论

- start 参数冲突在任务复用之前完成校验。
- 同一任务只能有一个初始 mode 和一个 initial expected generation。
- stopping 状态封闭新任务准入。
- stop 等待 task、send 和 output drain；只有完整收尾后才允许新任务。
- connection Promise 只描述首次连接前缀，completion 描述长期任务结束。
- 旧 Prepare 和旧 socket handler 由 task/socket identity 隔离。
- 旧 write callback 由 terminal epoch 隔离。
- 旧 loop finally 在公共生命周期中不会跨新任务迟到，因为新任务被 stop 屏障阻止到旧 task 完成之后。
- 新任务具有独立 task ID、socket 和状态发布身份。

## 11. 方案 A 结论

- 首次 takeover 严格至多一次。
- 首次 takeover 失败后，后续自动 attempt 固定 attach。
- `initialExpectedGeneration` 不可变。
- `confirmedGeneration` 只由当前任务的合法 `bind_success` 更新。
- generation 来源为 `confirmedGeneration ?? initialExpectedGeneration`。
- bind_success 丢失时不推算 G+1；服务端冲突时停止。
- 无自动 GET 详情、无自动更换 generation、无自动再次 takeover。
- 稳定资格重置 retries 不会重置 initial mode 消费闩锁。

## 12. Git 状态

本轮修改前：

```text
工作区：clean
暂存区：empty
```

本轮完成后预期工作区新增：

```text
?? doc/测试报告/stage-5-s5-c2-logs/
?? doc/测试报告/stage-5-s5-c2-test-report.md
?? frontend/tests/stage-5-c2.test.mjs
```

暂存区为空。没有生产文件 diff，没有冻结文档 diff，没有范围外修改。用户原有 `luan-qi-ba-zao.txt` 保持未修改。

## 13. 剩余边界与结论

本轮没有发现需要修复的 S5 生产代码缺陷。S6 用户接管 UI、Pane 协调和真实用户确认流程尚未实现，因此 S6-PRE01–03 只验证当前调用边界；这是既定阶段边界，不构成本轮阻塞。

最终结论：

**S5-C2 测试和回归全部通过，可以提交人工整体验收。**
