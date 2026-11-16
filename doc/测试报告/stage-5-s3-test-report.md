# 第五阶段 S3 测试与实施报告

日期：2026-11-17。工作目录：`/home/x/workspaces/web-ssh`。执行环境：WSL Ubuntu。

S3 WebSocket 终止及交接实现与下列回归已经完成，等待人工独立审查。未开始 S4–S8，未提交、推送或部署。本报告替换此前“等待最小文件范围授权”的前置检查报告；用户回复“继续”后，按已提出的最小方案增加 Session 发送准入接口文件。

## 1. 基线、授权和文件保护

- 当前分支：`codex/第五阶段`。
- 开始和完成时 HEAD 均为 `47e08712a8eff1b3550ff0134b1cd5209b351e83`（`5-S2-2`）。没有切换分支或创建提交。
- 开始时 `git status --short` 为空；暂存区、未暂存差异和未跟踪文件均为空。
- 编码前保存了相关源码副本、SHA256、暂存/未暂存差异到 `/tmp/codex/stage-5-s3-baseline/`。空状态文件及哈希清单已复制到本报告旁的 `stage-5-s3-logs/baseline-*`。
- `binding.go`、`session.go`、既有 `connection_test.go`、`httpapi/sessions_test.go`、第五阶段计划及四份冻结文档与 HEAD 逐字节相同，详见 `protected-files.json`。没有修改前端源文件、依赖或 S2 测试。
- 未操作用户的 `luan-qi-ba-zao.txt`，未丢弃或覆盖用户文件。没有执行 reset、restore、clean、merge、rebase、commit、push 或部署。

最小范围补充的原因：WebSocket 包无法访问 Session 的私有状态锁与 Binding 权限字段。单独读取 active/View 后锁外选消息不能满足冻结计划的同步边界。因此新增 `send_admission.go`，只提供 §7.3/§7.4 已设计的内部处置读取与锁内选择能力；没有改写 S2 的 Prepare、Attach、Detach、Execute 或原子事务。

## 2. 文件清单与关键位置

行号对应本次完成时源码。

| 文件 | 位置 | 职责 |
|---|---|---|
| `backend/internal/session/send_admission.go`（新增） | 12、25、34 | 受 Session.mu 保护的 BindingDisposition；WithSendAdmission 持锁覆盖身份检查、选择及标记 |
| `backend/internal/websocket/connection.go` | 99、117 | 首终止原因先确定，再 Detach/cancel；内部失效分流 |
| 同上 | 228、318、350 | Reader Done、登记窗口、执行器失效路径与成功提交结果收尾责任 |
| 同上 | 388、531 | 所有普通消息原子准入；唯一 Writer 从独立终止槽发送 taken_over/4001 |
| 同上 | 25、130 | 默认 nil 的测试同步点；安装和读取指针受 c.mu 保护，不在生产默认启用 |
| `backend/internal/websocket/protocol.go` | 68、121 | taken_over 对应 4001，具名 DTO 仅含 type/message |
| `backend/internal/websocket/takeover_test.go`（新增） | 212 起 | 12 个顶层真实 WS/SSH 测试，133 个具名子测试；固定交错及资源检查 |
| `doc/测试报告/stage-5-s3-test-report.md` | 本文件 | 实施、测试、边界及审查说明 |
| `doc/测试报告/stage-5-s3-logs/` | 目录 | 最终原始日志、历史失败日志、统计、退出码、哈希、Git 状态和完整源码补丁 |

既有 `connection_test.go` 和 `httpapi/sessions_test.go` 复用并运行，没有必要修改。

## 3. 终止、准入与资源生命周期

### 3.1 首因及 Reader 竞态

`terminateLocked` 的调用者持有 `Connection.mu`，顺序为：固定 reason 和绝对关闭期限 → 按 Connection 自己的 Binding Detach → 取消在途 Ping 和 Connection context → 关闭 terminated。随后释放 c.mu，再唤醒 Writer。Reader defer 的迟到 `Terminate("NETWORK_ERROR")` 只能看到已固定的首因，不能覆盖 taken_over。

S2 提交仍只把旧 Binding 标记为 active=false/superseded，并发布新 Binding；不取消旧 Done，不取旧 Connection.mu。`FinishDisplaced` 在所有事务锁和操作门释放后调用旧统一终止入口。

Reader 在操作槽等待中收到 Binding.Done，或执行器收到 BIND_CONFLICT/INPUT_BACKPRESSURE 时，通过 `finishBindingExit` 在 c.mu → s.mu 顺序下读取处置，仅 superseded 内部退出映射成 taken_over。真实 Read/Ping 错误、真实 Write 错误及其他独立故障沿用原路径，不统一重分类。真实 NETWORK_ERROR 已先确定时，后到的 takeover 保留该原因。

网络 Reader 和 Binding.Done 等待是两条不同路径；Binding context 取消不会被当作能够直接解除底层网络读取。网络读最终仍由 Writer Close、读 context 生命周期及独立 Supervisor 的 raw.Close 收尾。

### 3.2 普通 Writer 准入的临界区

每次 Writer 循环：

1. 获取 `Connection.mu`，先检查终止槽；终止消息不需要有效 Binding。
2. 对普通消息，保持 c.mu，调用 `Session.WithSendAdmission` 获取 s.mu。
3. 同一 s.mu 临界区内校验所属 Session、当前 Binding 指针、active、generation、CONNECTED；通过后选择恰一条不可变消息，并更新 Writer 局部游标/前缀位置。此处是准入点，`admitted` 测试事件也在两把锁内记录。
4. 历史快照、成功前缀、实时输出、普通 status 均走此边界；实时读取按 `c.mu → s.mu → OutputBuffer.mu` 短暂复制一块。
5. 释放 s.mu，再释放 c.mu；JSON 编码、实际 Write、Ping、Close 均在业务锁外。唯一 Writer 的局部消息就是唯一在途项，不预取第二项。
6. takeover 先取得 s.mu 并提交：旧端校验失败，不出队、不读下一块、不新增普通准入。Writer 先选中一项：提交只能发生在它释放 s.mu 之后，允许这一项按原预算完成，之后重新检查失败。

终止使用 reason/deadline 独立槽，普通控制 FIFO 已满也不阻断 taken_over。`finish` 只由现有 Writer 执行，最多一次终止消息，然后 Close 4001；不存在第二个应用 Writer。

### 3.3 锁顺序与无环条件

| 路径 | 顺序/边界 |
|---|---|
| S2 提交（未修改） | 操作门 → 登录读锁 → Session.mu → Buffer.mu → 凭证锁；锁外 FinishDisplaced |
| Writer | Connection.mu → Session.mu → 必要时 Buffer.mu |
| Terminate | Connection.mu → Session.mu（Detach） |
| 内部失效适配 | Connection.mu → Session.mu 短读，释放 Session.mu 后再调用 terminateLocked/Detach；不重入 s.mu |
| 登记及旧端通知 | 释放新 Connection.mu 后才执行旧回调；不同时持有两条 Connection.mu |
| Supervisor | 不获取上述业务锁；根据既有原子绝对期限关闭真正的 net.Conn |

准入回调不取操作门、登录锁、Manager 锁，不调用 Terminate/Detach，不执行网络或阻塞队列等待。Buffer 持有者不反向获取 Connection/Session 锁。因此未加入 Session.mu → Connection.mu 的反向边；新端提交不等待旧网络发送或 Close 握手。

测试用 barrier 有意暂停任务以构造交错，生产 hook 默认 nil；测试不会在持 s.mu 时等待必须取得 s.mu 的对方完成提交。

### 3.4 提交后登记窗口

Reader 的短函数 `registerBinding` 是成功结果的收尾所有者，入口即 `defer result.FinishDisplaced()`，避免把收尾拖到 Reader 整个生命周期结束。

- Connection 已终止：释放 c.mu，Detach 本次结果的 NewBinding，随后收尾 DisplacedBinding。
- 新 Binding 已被再次接管：登记自身清理归属，固定 taken_over，Detach 自己，不触碰后来者。
- Close/Revoke 已撤权：不发布普通发送权，不伪造提前的 closed，也不让 Reader defer 抢成 NETWORK_ERROR；锁外先履行旧端收尾，再等待原生命周期发布终止。重复 FinishDisplaced 由 S2 的 sync.Once 保证幂等。
- 仍有效：完成登记并唤醒新 Writer，释放新端 c.mu 后收尾旧端。

Session.Detach 仍按对象身份决定是否改变当前 Session；旧引用只取消自己的 context，不能修改新 Binding、generation 或离线期限。

### 3.5 Slot、关闭与物理回收

复用原有 worker wait group、Supervisor、workersDone/supervisorDone/done 与 Service.remove。taken_over 不提前归还 slot；旧 Reader/Writer/执行器/Ping 相关工作和 Supervisor 完成后才移除自己的 Connection。新端可以先发布前缀和继续同一 SSH Shell。真实堵写、半帧头与 Close/Pong 不响应由原关闭预算强关，不新增生命周期管理器。

## 4. 测试覆盖与交错

全部新测试复用真实 HTTP/1.1 Upgrade、实际 Manager/Service、`github.com/coder/websocket v1.8.15` 和真实环回 SSH。hook 只定位时序，实际发送、读取和终止仍走生产路径。

| ID | 测试/位置 | 本次证据 |
|---|---|---|
| W01 | `TestS3W01CandidateFailuresKeepOld`：749 | Prepare 后不连接、bind 超时、非法 offset、过期凭证、错误登录、非法 token；明确错误原因，旧端 generation/Binding 保留并可回显 |
| W02/T1 | `TestS3T1W02HundredTakeovers`：212 | 连续 100 次真实交接，每轮旧端一次 taken_over 和 4001、新端前缀代次递增并回显、旧 done/worker/supervisor 完成、slot 剩 1、始终一个 Shell；并发 List |
| T1/R07 | `TestS3T1ReaderDoneAndCommitHandoff`：362 | 100 个具名轮次；执行器暂停、容量 1 槽填满、Reader 等待第三项。提交后登记暂停、旧 Writer 暂停；检查旧 Done 尚开、逻辑已撤权、两个 slot 仍占用。50 轮迟到 Detach 先触发 Done，50 轮执行器失效先结束；迟到 defer/FinishDisplaced 不覆盖原因 |
| T2 | `TestS3T2RealNetworkFirstReason`：284 | 四种真实 TCP RST 顺序：故障后 Prepare、Prepare 后故障、Commit 后但 old.end 前故障、takeover 首因固定后故障。等待服务端 terminated 确认原因，再放行后续；同一 Shell 保留 |
| T3/R07 | `TestS3T3AdmissionOrdering`：576 | prefix/history/live/status × 3 种屏障，共 12 子例：已准入后暂停 Write；提交先完成；Writer 持 c.mu 尚未取 s.mu 时提交。真实准入事件、commit 完成观测序号和网络消息写入日志；提交后准入增量 0；填满 FIFO 后仍恰一次 taken_over/4001 |
| T3/R07 | `TestS3T3SessionLockCompetition`：702 | 20 轮同一 Session 锁自由竞争；事先签发凭证，固定锁屏障、Reader bindReceived 与旧 Writer 尝试点，释放后检查序号与最终回收 |
| R06/T4/W06 | `TestS3R06T4RegistrationWindow`：457 | B 已提交未登记时 Close、Revoke、C 再次 takeover、B 自身已终止。检查登记失败清理、后继身份；Close/takeover 分支实际读取 B 的 closed/1000 或 taken_over/4001，通知可先于成功前缀 |
| T4 | `TestS3T4LateCallbacksAcrossGenerations`：529 | A→B→C，旧 Detach、终止、失效分流和输出通知迟到；旧输入/resize 拒绝，B 离线期限不变，C 代次 3/尺寸/slot/回显正常。旧 Writer 已准入完成/失败由 T3/W04 交叉覆盖 |
| W03 | `TestS3W03PrefixFailureNoRollback`：815 | B 前缀已准入但写前暂停，A 已收到 taken_over/4001；重置 B TCP 后放行写，代次 2 保留并转离线；C 可正常 attach |
| W04/W05 | `TestS3W04W05PhysicalReclamation`：849 | 四种真实半帧头（Listener 确认读到注入字节）、Close 不响应、在途 Ping 不回 Pong；新端先完成前缀，旧端仍占 slot，第三连接 429，旧端强关后只剩新 slot。上限 1 拒绝候选且旧端可用 |
| W04/T3 | `TestS3W04RealBlockedWriterTakeover`：927 | SSH 预填 4 MiB；旧端历史块 4 MiB、TCP 两端缓冲 1 KiB、客户端停止读取。新端提交和前缀不等待旧 Write；旧端实际 writeFailures > 0，worker/Supervisor 完成后回收，SSH 未重建 |
| W06 | `TestS3W06SmallBufferAndShutdown`：971 | 1024 字节缓存、128 字节块，真实 SSH 连续输出；新端截断前缀、历史发送暂停后再产生输出，触发 OUTPUT_GAP/4002；SSH 保留，C 恢复后 Service/Manager Stop 收尾 |
| R08 | 100 次 takeover 并发 List，加既有 `TestListReleasesIndexLockBeforeRowSnapshot`、`TestListConcurrentLifecycle`，以及 W06 Stop | 分别覆盖列表引用快照、创建/关闭/移除、接管时 List、退出回收；所有这些测试均包含在全量普通及 Race 结果中 |

T1 的严格远端断言：旧排队输入标记在真实 SSH 回显缓存中出现 0 次；新端 `after-done-race` 出现恰 1 次；SSH 对端 `window-change` 请求计数为 0；Shell 数为 1。这里计数的是逻辑输入标记和真实 SSH 请求，未将可能拆包/合包的 TCP Read 次数冒充输入操作次数。

所有正常通知断言读取真实网络帧，要求终止消息恰一次，紧随其后的应用消息被拒绝，Close code 精确匹配。真实 RST、真实堵写/不响应等独立故障场景不声称终止通知必达，仍核对服务端首因、绑定身份和回收。

T3 的 `commit_observed` 是 Attach 成功返回、登记前的完成观测点；不是在 S2 唯一提交语句内新增的 hook。固定屏障保证先后关系，准入事件本身记录于实际 c.mu+s.mu 临界区。自由竞争序号不被描述为精确记录了事务内部每一条指令。

## 5. 最终验证命令、退出码与统计

Go 版本：`go version go1.26.5 linux/amd64`，匹配 `backend/go.mod` 的 `go 1.26.5`。固定 WS 库版本未修改。所有命令在 WSL 的对应子目录执行，原始 stdout/stderr 分开保存，Python subprocess 逐条记录真实 returncode；整个验证任务退出码为 0。

| 工作目录 | 完整命令 | 退出码 | 秒 | 输出文件（均位于 stage-5-s3-logs） |
|---|---|---:|---:|---|
| backend | `go test -count=1 -json ./...` | 0 | 34.20 | backend-tests.json / backend-tests.stderr.log |
| backend | `go test -race -count=1 -json ./...` | 0 | 41.36 | backend-race.json / backend-race.stderr.log |
| backend | `go vet ./...` | 0 | 0.24 | vet.log / vet.stderr.log |
| backend | `go build ./...` | 0 | 0.60 | build.log / build.stderr.log |
| frontend | `npm test` | 0 | 0.71 | frontend-tests.log / frontend-tests.stderr.log |
| frontend | `npm run build` | 0 | 2.25 | frontend-build.log / frontend-build.stderr.log |

`-count=1` 禁用测试结果缓存；`-json` 仅改变输出格式，同样执行完整 ./...。`npm run build` 实际执行 `vue-tsc --noEmit` 后运行 Vite build；前端源文件未修改。

| 最终 Go JSON | 顶层 PASS/FAIL/SKIP | 子测试 PASS/FAIL/SKIP | 包 PASS/FAIL/SKIP |
|---|---|---|---|
| 普通 | 114 / 0 / 0 | 343 / 0 / 0 | 10 / 0 / 4 |
| Race | 114 / 0 / 0 | 343 / 0 / 0 | 10 / 0 / 4 |

顶层与子测试分别统计，合计 457 个 test pass 事件，不把父测试与子测试混称为 457 个顶层用例。S3 新增部分为 12 个顶层、133 个子测试；循环中的 100 次正常交接、20 次锁竞争没有伪造为额外具名子测试。4 个无测试文件包只记包级 SKIP，不计测试通过数，也不算失败。

Race 最终 stdout 与 stderr 中 `DATA RACE` 和 `WARNING: DATA RACE` 均为 0。前端为 40 PASS、0 FAIL、0 SKIP、0 cancelled。机器可读统计和退出码分别见 `statistics.json`、`command-results.json`。

## 6. 开发期间失败与保留记录

未删除失败测试或降低断言获取通过。以下文件按原始字节保存在 `stage-5-s3-logs/development/`；它们属于开发中间状态，不能替代第 5 节最终结果。

| 原始日志 | 结果/原因 | 修正 |
|---|---|---|
| initial-tests.json | 失败：W06 等待缓存通知超时 | Changed 是单消费者提示，测试可能与 Writer 抢通知；改为有界复查实际偏移 |
| second-race.json | 失败：新增持 c.mu 屏障场景测试自死锁，测试整体超时 | 测试线程不再调用需要同一 c.mu 的 NotifyStatus；直接写并发安全 channel 构造满 FIFO |
| admission-race.json | 通过 | 上述 12 个准入子例的定向 Race 复验 |
| third-race.json | 失败：堵写测试等待“第二条 live 写”，第一条就可能已堵住 | 改成预填单个 4 MiB 历史块，最终断言实际写失败和强关 |
| blocked-race.json | 失败：观察者反复复制大缓存，Race 下等偏移超时 | 改为仅探测目标末字节，20ms ticker 只观察已发生状态，不安排关键竞态 |
| blocked-race-retry.json | 通过 | 真实堵写定向 Race 复验，之后进一步改为单字节观察 |
| final-review-race.json | 失败：新加的 ownership 精确断言误写 INVALID_CREDENTIAL | 按冻结协议和原实现修正为 PERMISSION_DENIED；没有改生产错误码 |
| full-before-review/ | 六项完整检查均通过 | 最后补强精确错误码、登记前通知、TCP 故障交错及远端次数前的完整日志；最终版本已全部重跑 |

早期另有一次未使用 import 的编译错误，已移除该测试 import；该次终端输出未另存 JSON，不声称有对应原始日志。开发失败日志未发现 DATA RACE。最终没有遗留失败项。

## 7. 原始日志、哈希及审查补丁

所有交付证据均在：`doc/测试报告/stage-5-s3-logs/`。

- `backend-tests.json` SHA256：`ad8a59b8f5a486a359eac5ff511c318f0ff65e5add5eb82677cd1405f021baef`
- `backend-race.json` SHA256：`0c06081879294ab30489750579ed35af11fb2d28ab9e9fd37ccf222b39864c7a`
- `command-results.json` SHA256：`bfd815d2cf3c0c4614d31880413ee6919ccb6cdcd271403dca695441d500c20c`
- `SHA256SUMS` 包含目录中所有证据文件的哈希（不包含清单自身）；在该目录运行 `sha256sum -c SHA256SUMS` 可复核。
- `s3-source-review.patch` 包含 4 个生产/测试源码文件的完整 diff，**包括两份未跟踪的新源码文件**；不将大体积日志内容塞入源码补丁。
- `git-final-status.txt`、`git-final-diff-stat.txt`、`git-final-diff-check.txt`、`git-final-staged.txt` 保存最终 Git 观测；`protected-files.json` 保存未修改文件的比较结果和当前哈希。

## 8. 最终 Git 差异

暂存区为空。已跟踪源码差异为 2 个文件，181 行新增、58 行删除；另有 `send_admission.go` 42 行、`takeover_test.go` 1015 行新增。常规 `git diff --stat` 不包括未跟踪文件，完整源码范围应结合上述 review patch 查看。

```text
 M backend/internal/websocket/connection.go
 M backend/internal/websocket/protocol.go
?? backend/internal/session/send_admission.go
?? backend/internal/websocket/takeover_test.go
?? doc/测试报告/stage-5-s3-logs/
?? doc/测试报告/stage-5-s3-test-report.md
```

`git diff --check` 退出码 0。Git 的 LF→CRLF 配置提示不是空白错误；未进行全仓换行格式化。新增/修改 Go 文件已 gofmt；报告与证据不包含密码、真实终端业务数据或线上凭证。

## 9. 审查重点和实际限制

- 没有发现需要修改 S2 事务语义或冻结协议的冲突；新增内部接口是用户继续授权后的最小必要扩展。建议人工重点审查 `WithSendAdmission` 回调约束、reason-before-Detach、登记无效分支等待原关闭责任人以及锁外 FinishDisplaced。
- 普通网络等待的 100 次交接、Done/执行器 100 个固定轮次、20 次自由锁竞争、各物理故障场景及最终全量 Race 都已运行。没有把每种半帧头/堵写/Stop 组合各重复 100 次，也没有声称穷尽全部调度。
- R08 的列表创建/关闭/接管/退出证据来自表中多个测试的组合；没有把独立的 Stop 场景描述为在 100 次交接的每一轮同时触发 Stop。
- 回显与 SSH 请求提供实际远端效果证据，不是生产主机或交互式 PTY 程序验收；测试夹具是环回 SSH 回显 Shell。
- 真实堵写、半帧、Close/Pong 不响应的回收有有限等待容差；没有承诺每个异常连接能收到 taken_over。正常读取并响应关闭的接管场景必须且已经验证 taken_over/4001。
- 未进行 S8 浏览器、跨设备或部署验收。前端只运行既有 40 项回归和构建；既有 taken_over/4001 停止恢复测试通过，不代表前端主动接管 UI 已实现。
- 当前没有已知未修复的 S3 实现问题或最终测试失败；并发测试不等同于形式证明，待独立代码审查。S4–S8 保持未授权、未实施状态。

本次到 S3 为止，等待用户和独立审查方复核，不继续编码后续阶段。
