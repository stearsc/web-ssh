# Web SSH 第五阶段 S2 实施与测试报告

日期：2026-11-17（Asia/Shanghai）。范围：凭证 mode 与原子替换事务；不包含 S3 真实 WebSocket 接管链路。

## 1. Git 状态与基线

- 分支：`codex/第五阶段`。
- 开始时 HEAD：`637c6232a873ed2fef97229e16cc4172b2ef0d0f`。
- 完成时 HEAD：同上；本次没有 commit、push、merge、rebase、reset、clean 或部署。
- 当前 HEAD 已包含 S1 提交 `1907bfe3b1ef3b71dd709381028a6e8643d83546`。
- 开始时只有 `luan-qi-ba-zao.txt` 的用户未提交修改；暂存区为空。
- 完成时新增/修改本报告第 2 节列出的六个文件，用户文件原样保留，暂存区仍为空。
- 用户文件 SHA-256 前后一致：`853949d24831b7705dcbfaac5dff71f41b7c4e98023ee3a9fcf035055892023d`。
- 第五阶段实施计划 SHA-256 前后一致：`568598b5c589f613ae2dd4a14db92e7e8aee55c20e07399cc4cdbe9ca5b5f570`。
- 实施前重读计划 §3–5、§7、§10、§17、§21 及冻结架构事务/锁序、协议 bind 契约，按当前代码核对调用点。历史“无 List”描述由已完成的 S1 实现补齐，不回退 S1。

## 2. 修改文件及职责

| 文件（相对仓库根目录） | 关键位置 | 职责及范围 |
|---|---|---|
| `backend/internal/session/binding.go` | credential、CommitResult、Prepare、canBind、Attach | 增加服务端 mode；共用准入与唯一提交点；返回新旧 Binding；锁外收尾；不改变 List、Detach、Execute、Revoke 实现 |
| `backend/internal/session/session_test.go` | attachBinding、原隔离/attach 测试 | 测试辅助函数适配结果签名并收尾；将历史“拒绝 takeover”范围断言改为“Prepare 成功且旧状态不变”；保留原 attach 安全断言 |
| `backend/internal/session/takeover_test.go`（新增） | 12 项 TestTakeover 测试 | U05–U09、R01–R05；确定性时钟、channel 屏障、现有快照/操作准入 hook、真实 SSH 与真实 Logout |
| `backend/internal/websocket/connection.go` | reader、registerBinding | 仅适配 CommitResult；Reader 为唯一所有者；登记失败也 defer 旧对象收尾；不改终止入口、Reader 失效分流或 Writer |
| `backend/internal/httpapi/sessions_test.go` | 既有 takeover Prepare 阶段范围断言 | 必要测试契约适配：400 改为 200，并检查凭证字段、Session 状态/代次/离线期限不变；没有改 HTTP 业务代码和 S1 列表测试 |
| `doc/测试报告/stage-5-s2-test-report.md`（新增） | 本文 | 按用户指定目录记录证据、验证结果和阶段边界 |

HTTP 测试文件超出初始优先清单的原因已在修改前说明：其旧断言明确禁止本轮授权实现的 mode，必须更新才能验证新契约；不是删除测试或降低安全要求。没有修改前端、数据库 schema、配置、依赖、冻结文档或 S1 的 Manager.List/路由实现。

## 3. 核心实现

### 3.1 凭证与准入

凭证索引仍以随机 token 的 SHA-256 为键；记录 loginID、sessionID、mode、expected、原始 expires。只接受 attach/takeover 和合法安全整数代次。提交时只能从服务端记录读取 mode，Attach 没有客户端 mode 参数，WS bind DTO 未改变。

- attach：DISCONNECTED 才允许；CONNECTED 或已有 Binding 返回 BIND_BUSY。
- takeover：CONNECTED/DISCONNECTED 均可；CONNECTING/CLOSING/CLOSED、离线到期拒绝。
- 活动状态下先比较代次，再判断 attach 忙；不同代次返回 BIND_CONFLICT。
- Prepare 仅签发；不取操作门，不撤权、不取消 context、不增代、不改变状态或离线期限，也不预留赢家。
- TTL、全局容量、不同登录/目标归属和清理机制继续复用原实现。

### 3.2 统一提交与失败处理

唯一 Attach 路径按以下顺序工作：

1. 校验 offset/token 格式，有预算取得远端操作门。
2. 登录读锁 → Session.mu；短取凭证索引锁初查后释放。
3. 检查应用、凭证 TTL/身份/目标/mode/代次、Session 状态和离线期限。
4. 取得 OutputBuffer.mu，复制唯一 `[max(P,A),E)` 回放快照；P>E 拒绝。
5. 取得凭证索引锁，复查记录存在且未改变、TTL、登录到期、状态/代次/离线期限和候选 context。
6. 唯一提交点内清理该 Session 全部凭证；旧 Binding 置 active=false、superseded=true；generation++；创建发布新 Binding、置 CONNECTED、更新 attached、清离线期限、非阻塞标记 dirty。
7. 按逆序释放锁和操作门，返回 `*CommitResult{NewBinding, DisplacedBinding}`。

提交临界区不调用 old.cancel/old.end，不获取 Connection.mu，不进行网络操作。代次耗尽在全部锁及操作门释放后调用既有 Close(counter_exhausted)，不回绕。

提交前失败返回 nil 结果，不发布快照，也不保留候选快照引用：局部字节副本由 Go GC 回收，不涉及池或显式资源释放接口；没有断言 GC 必须在某个时刻运行。仍有效且未被外部清理的凭证保留，旧 Binding/代次/状态/离线期限不因候选失败变化。真实关闭、登出、应用退出仍可独立改变生命周期。

提交后即使候选取消、登记失败或后续发送失败，也不回滚旧权限/代次。按指针 Detach 新对象；若仍是当前 Binding 则进入 DISCONNECTED，否则不影响更晚的新 Binding。

### 3.3 唯一锁外收尾责任

生产调用方只有 WebSocket Reader。成功 Attach 后无条件进入 registerBinding；这个短函数入口立即 defer `result.FinishDisplaced()`，不把 defer 挂到整个 Reader 生命周期。

registerBinding 在新 Connection.mu 内查看终止原因：可用则登记新 Binding/心跳，解锁后 signal；已终止则先解锁，再按对象 Detach 新 Binding。两条路径返回前都执行旧对象 end("taken_over")。没有新旧 Connection 锁嵌套，不等待旧网络通知、Close 或 slot 回收。结果内部 sync.Once 防止同一结果重复执行收尾回调。

现有直接 attach 测试通过薄辅助函数适配；S2 专项测试直接取得 CommitResult，检查提交/收尾两个时刻。没有复制第二套 takeover Attach 实现。

### 3.4 锁序与 S1

提交沿用：操作门 → 登录读锁 → Session.mu → OutputBuffer.mu → 凭证索引锁。初查凭证锁在获取 Buffer 锁前已释放。锁外 end 可走现有 Connection.mu → Session.Detach，提交方不持 Session 锁等待 Connection，因此未增加反向锁边。

S1 的内存权威、List 快照边界、排序、过滤、DTO 和 HTTP 行为未修改；原 S1 测试完整保留并参与普通/race 回归。

## 4. U05–U09、R01–R05 覆盖与结果

下表所有列出的新测试均在全量普通和 race 运行中通过。新增 12 个顶层测试、52 个子测试，失败 0。

| 编号 | 测试函数 | 固定交错与主要断言 | 结果 |
|---|---|---|---|
| U05 | TestTakeoverU05PrepareMatrix | 两 mode × CONNECTED/DISCONNECTED/CONNECTING/CLOSING/CLOSED/到期；非法 mode/代次；占住操作门仍可 Prepare；代次冲突先于忙；View、旧 active/context、代次与期限不变 | 通过 |
| U06 | TestTakeoverU06CredentialIdentityAndConsumption、TestTakeoverU06TTLAndCleanup | 保存 mode/身份/目标/代次/TTL；错误登录/Session 不消费；容量拒绝；精确 TTL 边界、过期清理；消费同 Session 所有凭证、保留其他 Session 凭证；同 token 重用失败；服务端 attach 记录不能替换在线 Binding | 通过 |
| U07 | TestTakeoverU07PrecommitFailures | 在线/离线候选；P>E、快照后精确 TTL 到期、记录清除/变化、登录到期、离线期限到达、ctx 取消、应用取消；nil 结果、不改旧身份/状态/代次/期限/缓存；有效凭证保留，可重试的失败随后成功 | 通过 |
| U08 | TestTakeoverU08HandoffAndLateDetach | 提交后先检查旧 Done 尚未取消但已无旧 SSH/resize 准入；模拟候选取消、登记/Writer 不可用、更晚再接管；defer 一次回调；检查 Session/Buffer/凭证锁及门已释放；新旧迟到 Detach 不改更晚对象或新离线期限 | 通过（内部调用模型） |
| U09 | TestTakeoverU09CountersOffsetsAndMetadata | offset 0/MaxCounter-2/MaxCounter/越界、回放范围/长度；generation 耗尽不回绕、counter_exhausted 完成；真实 SQLite UPDATE 失败后新权限/List 代次不回退且可回收 | 通过 |
| R01 | TestTakeoverR01R02CredentialCompetition 的 distinct/same_token | 8 个候选在门前汇合，第一快照暂停；不同凭证/同 token 均恰好一赢家；代次仅增一次；错误集合允许 INVALID_CREDENTIAL/BIND_CONFLICT | 通过 |
| R02 | 同测试 attach_takeover 及 U05/U06 | 离线 attach/takeover 混合候选；在线 attach 忙；随后新代次合法 takeover 可继续；无双有效 Binding | 通过 |
| R03 | TestTakeoverR03OperationGate | 真实 Write/resize 准入 hook 暂停；候选等待操作门或实际预算超时；超时不撤旧权限且 token 保留；候选快照暂停期间发起旧排队输入/resize；只发生一项远端准入，实际回显/resize 请求数正确，禁止输入不出现，Shell 不重建 | 通过 |
| R04 | TestTakeoverR04CloseSSHAndSweep、TestTakeoverR04OldSweepAfterCommit，另 U07 离线期限 | 分别固定 Close、真实 SSH exit-status/通道关闭、Sweep 先于提交及提交先于生命周期操作；检查首关闭原因、CLOSED 不回退/不重设期限、索引回收；时钟屏障暂停旧 Sweep，接管后继续扫描及超过旧期限再扫描仍保留新对象 | 通过 |
| R05 | TestTakeoverR05RealLogoutOrdering、TestTakeoverR05DifferentLogins | 真实 Logout 先于 Prepare/提交；提交快照内持登录读锁时启动 Logout，释放屏障后提交的新 Binding 被撤销扫描找到；不同登录旧端登出不拆新端、新端登出只 Detach；真实数据库 DELETE 回滚保留权限；SSH 不关闭 | 通过 |

既有测试补充证据：

- `TestSSHOperationTimeoutAndKeepaliveReclaim` 继续验证 SSH 操作自身超时按 ssh_error 关闭并回收；本轮没有改操作超时语义。
- `TestLogoutTransactionFailure` 通过，覆盖确定回滚保持身份及不确定事务结果冻结身份并触发撤销回调；`TestRealDatabaseLogoutRollback` 同样通过。本轮新增的是真实 Logout/真实数据库回滚与 takeover 的组合，不把既有认证单元故障注入描述为真实数据库“不确定提交”集成试验。
- 原 attach/offset/恢复/关闭、真实 WS 生命周期、S1 列表测试均随全量普通/race 运行通过；它们不等于新增真实 WS takeover 验收。
- 新增并发测试没有用 sleep 排列交错；time.After 仅作失败诊断上限，操作门超时使用产品真实预算。

## 5. 验证命令、退出码与日志

环境：WSL Ubuntu，Go `go1.26.5 linux/amd64`。开发和文件操作均通过 WSL 执行。下列 Go 命令工作目录为 `backend/`。

| 实际命令 | 退出码 | 关键结果 |
|---|---:|---|
| `go test ./internal/session ./internal/httpapi`（修改前） | 0 | 基线通过 |
| `go test ./internal/session ./internal/httpapi`（修改后最终） | 0 | 两个相关包通过 |
| `go test -count=1 -run TestTakeover -timeout 90s ./internal/session` | 0 | 首轮 S2 专项通过 |
| `go test -race -count=1 -run TestTakeover -timeout 90s ./internal/session` | 0 | 首轮 S2 race 通过 |
| `go test -count=1 -run TestTakeover ./internal/session`（补强后） | 0 | S2 专项通过 |
| `go test -count=1 -json ./...` | 0 | 102 顶层测试、210 子测试通过，0 失败 |
| `go test -race -count=1 -json ./...` | 0 | 102 顶层测试、210 子测试通过，0 失败；扫描完整日志无 DATA RACE |
| `go vet ./...` | 0 | 通过 |
| `go build ./...` | 0 | 通过 |

全量涉及 10 个有测试的包、4 个无测试文件的包；没有跳过测试用例。`-json` 仅增加可统计日志，不缩小测试范围。最终完整命令日志在 WSL `/tmp/codex/s2-validation/`：`related.log`、`backend-tests.log`、`backend-race.log`、`vet.log`、`build.log`；`results.json` 保存各项命令、退出码及耗时。

没有测试失败需要绕过。全仓库 `git diff --check` 报告用户原有 `luan-qi-ba-zao.txt` 尾随空白，本轮未修整该文件。本轮代码/报告单独检查通过；Go 文件 gofmt 检查通过。没有修改前端，本次授权要求的验证集中于后端，未将前轮前端结果作为本轮重新验证结果。

## 6. S3 边界核查

本次没有实现：

1. taken_over WS 具名 DTO 或 Close 4001 映射。
2. OUTPUT_GAP/4002 新行为。
3. Terminate 原因确定顺序改造、Reader/执行器 finishBindingExit 分流。
4. Writer 的 Connection.mu → Session.mu → OutputBuffer.mu 发送准入或普通消息调度修改。
5. Supervisor、连接 slot、Close 握手与物理回收改造。
6. 真实 WS 接管通知/网络竞态、T1–T4 或完整登记窗口 R06 验收。
7. 前端、S4–S8、真实部署或跨设备验收。

已提供 S3 接入点：服务端凭证 mode、唯一原子替换、Session 锁保护的 superseded 标记、CommitResult 与 registerBinding 的必执行锁外收尾。当前只能保证 Session 层旧 SSH 操作准入被撤销；旧 Writer 的发送准入与通知原因/关闭码仍须 S3 落实。已有 end("taken_over") 回调调用不能当作 taken_over 消息/4001 已实现的证据。

## 7. 遗留事项与停止条件

- S2 范围内没有未完成的编码项，没有发现需要修改冻结设计的冲突。
- U08 使用内部交接模型验证候选取消/登记与启动失败的处置责任；实际 registerBinding 的真实 WS 窗口与网络通知竞态留在 S3。没有声称 S2 已可作为完整跨设备接管链路验收或部署。
- 失败快照没有被发布或持久引用；内存实际返还由 Go GC 决定，不承诺同步归还物理内存。
- 认证不确定事务结果沿用并通过既有故障注入单元测试；本轮未新增真实数据库“不确定提交”与 takeover 的集成故障环境。
- 用户原有未提交修改及其空白问题均保留。所有本次成果未暂存、未提交。
- 完成本报告后停止在 S2，等待人工复审和下一阶段授权。
