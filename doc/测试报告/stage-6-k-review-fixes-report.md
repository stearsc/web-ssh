# Web SSH 6-K 实现复审：定点修复与补充验证

日期：2026-11-20。工作分支：`S6`；基线：`d41909f`（stage-6）。本轮不提交、不推送、不合并、不变基。

## 1. 结论与根因

| 事项 | 根因 | 本轮结果 |
| --- | --- | --- |
| 强制退出时维护锁提前释放 | `serve()` 在业务、Session 或 WebSocket 未排空时保留 SQLite 并返回，但外层 `run()` 无条件 defer 关闭维护锁，留下返回到 `main()` 真正退出之间的窗口 | 将运行期锁所有权交给 `serve()`；仅全部排空且 SQLite 关闭成功后显式解锁；未排空时由内核在进程退出时回收 |
| 同一数据库的路径别名取得不同锁 | `filepath.Abs` 只转绝对路径，没有统一数据库文件和父目录的符号链接身份 | 解析父目录和数据库链接后生成锁名，并在持锁后复核数据库及锁目录项身份；不修改实际数据库路径或配置 |
| GC claim 后卷故障缺少失败类别 | claim 已持久化次数和时间，随后目录重开失败或身份改变直接 return，跳过结果记录事务 | 两个出口统一记录固定类别，保留 `delete_claimed`、次数、时间和原始候选时间，再暂停本轮；原卷恢复后下一轮安全重试 |
| OpenSSH RSA 依赖保护是否存在 | 需要核实实际锁定源码及调用顺序，仅看注释无法形成证据 | `x/crypto v0.59.0` 实际存在全部既定限制，且位于 `Validate/Precompute` 前；无需升级或修改上限 |

## 2. 修改文件

| 文件 | 修改目的 |
| --- | --- |
| `backend/cmd/server/run.go` | 区分启动期持锁与运行期所有权；启动失败先关闭 SQLite；运行期由 `serve()` 排空任务、关闭 SQLite 后释放锁；数据库关闭失败也保守持锁 |
| `backend/cmd/server/main.go` | 适配维护锁类型，说明错误退出时描述符随进程退出回收 |
| `backend/internal/maintenance/lock.go` | 用没有终结器的 `Lock` 持有原始内核 fd；幂等 Close；统一路径身份、拒绝不安全对象并复核获取期间的变化 |
| `backend/internal/privatekey/store.go` | claim 后卷失败也进入结果记录事务；新增固定错误类别；日志使用 claim 实际持久化的尝试时间；测试可在真实目录 IO 前同步故障 |
| `backend/cmd/server/run_test.go` | 仅适配 `serve()` 的锁参数，保留已有 HTTP/SSH 退出断言 |
| `backend/cmd/server/lock_lifecycle_test.go`（新增） | 子进程中的真实 HTTP 阻塞任务、SQLite 访问、GC、管道退出同步和真实维护锁争抢；另覆盖数据库打开前后的启动失败 |
| `backend/internal/maintenance/lock_test.go`（新增） | 数据库存在/不存在、文件/父目录链接别名、缺失多层目录、链接目标内的 `..`、硬链接/循环/不安全身份测试 |
| `backend/internal/privatekey/store_test.go` | 真实目录移走/替换及恢复；验证 claim 排他、失败记录、24 小时宽限和下一轮重试 |
| `backend/internal/privatekey/parse_test.go` | 直接调用锁定依赖的 OpenSSH 私钥解析入口，验证各 RSA 拒绝边界和允许上限处的调用顺序 |
| 本报告（新增） | 独立记录本次复审，不修改历史报告 |

## 3. 维护锁生命周期时序

### 启动与正常关闭

1. 在任何 SQLite 打开之前，取得统一数据库身份对应的排他维护锁。
2. 启动失败：若尚未打开数据库，直接解锁；已打开数据库则先关闭数据库再解锁。关闭失败时保守保留锁，交由错误退出回收。
3. 启动成功：`run()` 将锁交给 `serve()`，其外层 defer 不再释放运行期锁。
4. 退出时停止准入、取消请求和清理任务，并让 HTTP、Session、WebSocket 共用原有 shutdown 总截止时间。
5. HTTP Handler/清理任务、Session 和 WebSocket 全部排空。
6. 关闭 SQLite；关闭成功后才显式关闭维护锁；随后返回 `main()`。

### 超时强制退出

1. 任一业务任务、Session 或 WebSocket 在总截止时间内尚未结束，`serve()` 返回错误。
2. 此路径不关闭仍可能被业务使用的 SQLite，也不关闭维护锁。
3. `run()` 返回后，其 defer 不释放已交出的运行期锁。
4. 锁通过原始 fd 持有，不依赖 Go 对象存活；即使对象不可达且执行 GC，也没有 `os.File` 终结器提前关闭 fd。
5. `main()` 停止信号订阅、记录退出错误并执行 `os.Exit(1)`。直至内核真正终止进程、回收 fd，其他服务/离线维护才能持锁。

子进程测试调用生产 `serve()`，在真实 Handler 的文件系统接口中用通道阻塞业务任务，保持原有 1 秒 shutdown 配置。服务函数返回后，子进程由控制管道继续存活，并执行 GC；父进程申请同一个维护锁必须失败。父进程再放行业务任务，任务使用原 SQLite 连接执行 `SELECT 1` 成功；此时维护锁仍须申请失败。最后允许子进程执行 `os.Exit(1)`，等待其真正退出后再次申请锁成功。正常模式则验证数据库已关闭、旧子进程尚存活时锁已可申请。

上述成功依据是通道、管道、SQL 查询、进程 Wait 和实际锁结果；没有用 sleep 或缩短配置超时来证明生命周期。

## 4. 数据库锁身份

- 先按原行为转换绝对路径并创建配置路径的父目录，再解析父目录和最终数据库符号链接。
- 数据库文件尚未创建时，使用已确认父目录身份及最终文件名。指向尚不存在数据库的链接，只要目标父目录可确认，仍可统一到同一锁。
- 链接目标中的 `..` 由文件系统在解析前序符号链接后处理，不能先 Clean 而偏离实际目标。
- 锁取得后再次确认配置路径解析结果、数据库设备号/inode，以及锁 fd 与锁目录项的设备号/inode和单链接条件。
- 非普通数据库文件、多硬链接、循环链接、目标父目录无法确认及路径身份变化均拒绝；不退化为按别名另建锁。
- 原有锁文件的 `O_NOFOLLOW`、0600、当前 UID、普通文件及单链接校验继续保留。未改写 `c.Database.Path` 或 SQLite 打开逻辑。

## 5. GC claim 失败状态与重试时序

设原始候选时刻为 C，本次尝试时刻为 T，原次数为 N：

| 阶段 | state | candidate_at | delete_attempts | last_delete_attempt_at | last_delete_error |
| --- | --- | --- | --- | --- | --- |
| 未满 24 小时 | 原状态 | C | N | 原值 | 原值 |
| 宽限到期，claim 提交 | delete_claimed | C | N+1 | T | 原值 |
| 重开卷失败并记录结果 | delete_claimed | C | N+1 | T | volume_unavailable |
| 或卷身份变化并记录结果 | delete_claimed | C | N+1 | T | volume_changed |
| 卷恢复，下一轮 claim | delete_claimed | C | N+2 | T2 | 上次类别 |
| 确认归属且删除成功 | deleted | C | N+2 | T2 | NULL |

- claim 一旦提交，新引用和 lease 均被持久化状态拒绝；记录卷失败后仍保持 claim。
- 卷不可确认时不调用删除逻辑，不删除旧卷对象，也不删除替换卷中同名但无归属证据的文件。
- 卷持续不可打开时，下一轮在初始目录打开阶段暂停，不增加删除尝试次数。
- 原卷恢复后，下一轮仍检查候选时间、引用、lease、不确定事务和对象设备号/inode，成功后保留墓碑并清空失败类别。
- 测试精确覆盖 `C+24h-1ms` 不尝试、`C+24h` 才 claim，失败和成功后均断言 C 不变。还用新建 Store 验证准入依据来自持久化 claim；这不替代整个服务启动对账流程的测试。
- 若结果记录事务本身不能确认提交，仍按既有规则安全暂停并返回 `SERVER_UNAVAILABLE`，不声称失败类别已提交；此前已确认提交的 claim、次数和时间继续提供保护。本轮未改变启动对账规则。

## 6. OpenSSH RSA 源码证据

精确模块：`golang.org/x/crypto v0.59.0`。`go list -m -json golang.org/x/crypto` 确认实际目录：

`/Users/x/go/pkg/mod/golang.org/x/crypto@v0.59.0`

模块内容校验和：`h1:xS2jhqYbzNrpvkWP2YTDf4TJwBy3ykR03i8vFDxEc7c=`。`go mod verify` 实际返回 `all modules verified`。

实际源码 `ssh/keys.go` 的 `parseOpenSSHPrivateKey` → `case KeyAlgoRSA`：

| 位置 | 实际执行内容 |
| --- | --- |
| 1485–1486 | `key.N.BitLen() > 16384` 时拒绝 |
| 1488–1489 | `key.P.BitLen() > 8192 || key.Q.BitLen() > 8192` 时拒绝 |
| 1491–1492 | `key.E.BitLen() > 24` 时拒绝，保留既定指数位数上限 |
| 1494–1496 | 指数转 int64 后要求不小于 3 且为奇数；24 bit 下最大允许值为 16,777,215 |
| 1499–1506 | 通过上述检查后构造 `rsa.PrivateKey` |
| 1508 | 才调用 `pk.Validate()` |
| 1512 | 才调用 `pk.Precompute()` |

新增测试直接断言依赖返回的具体错误：N=16,385 bit、P=8,193 bit、Q=8,193 bit、E=25 bit、偶数 E、E<3 都在对应检查处拒绝；N=16,384 bit、P/Q=8,192 bit、E=16,777,215 的不完整密钥进入 RSA Validate 后因密钥不成立而失败。该上限测试不声称生成了有效的最大 RSA 密钥。

未修改模块版本、go.mod、go.sum、RSA 上限或解析协议。

## 7. 新增测试及实际验证

所有 Go 验证使用本机 `go version go1.26.5 darwin/arm64`；`CGO_ENABLED=1`。

新增测试：

- `TestMaintenanceLockCoversProcessExit`：blocked、normal 两种子进程模式；另有 `TestMaintenanceLockProcessHelper` 作为子进程入口。
- `TestRunStartupFailureReleasesMaintenanceLock`：数据库打开前、打开后两条启动失败路径。
- `TestMaintenanceLockDatabaseAliases`：存在与不存在数据库，每个真实路径/文件链接/父目录链接轮流先持锁，其余入口均争抢失败。
- `TestMaintenanceLockMissingNestedDirectoryAndSymlinkDotDot`：多层目录创建、父目录链接和目标内 `..`。
- `TestMaintenanceLockRejectsUnsafeIdentity`：硬链接、循环、目标父目录缺失、目录冒充数据库。
- `TestSweepPersistsPostClaimVolumeFailureAndRetries`：volume_unavailable、volume_changed；持久化字段、排他准入、原文件及未知文件保留、恢复后下一轮重试。
- `TestLockedOpenSSHRSAFieldGuards`：7 个依赖边界子测试。

| 实际命令（backend 目录） | 结果 |
| --- | --- |
| `go test -count=1 -v ./cmd/server ./internal/maintenance ./internal/privatekey` | 通过 |
| `go test -count=1 ./...` | 失败；唯一失败为原有慢写 writer 子测试，见下文 |
| `go test -count=1 -race ./... -skip 'TestBlockedRealTCPWriterAndBoundFailureRelease/writer'` | 通过；明确排除该已知失败子测试，不等同全量通过 |
| `go test -count=1 -race ./cmd/server ./internal/maintenance` | 最后完善强制退出码断言及锁身份判断后的定点竞争检测通过 |
| `go vet ./...` | 通过 |
| `go build ./...` | 通过 |
| `go list -m -json golang.org/x/crypto`、`go mod verify` | 精确版本如上，全部模块校验通过 |
| `git diff --check`、变更 Go 文件 gofmt 检查 | 通过 |

全量测试保留的失败原文：

```text
--- FAIL: TestBlockedRealTCPWriterAndBoundFailureRelease
    --- FAIL: TestBlockedRealTCPWriterAndBoundFailureRelease/writer
        connection_test.go:517: 未验证真实写超时 NETWORK_ERROR
```

`backend/internal/websocket/connection_test.go` 与 HEAD 字节一致，未削弱该测试，也未修改 WebSocket 实现或协议。本轮没有执行前端、部署、迁移或远程写操作。

验证日志：`/tmp/codex/web-ssh-6k-review/targeted.log`、`full-test.log`、`race-test.log`、`final-lock-race.log`。临时目录仅用于本轮证据，不改变项目正常构建输出和缓存位置。

## 8. Git 与受保护文件检查

- 保持 `S6`，HEAD 仍为 `d41909f`；没有 Git 提交、推送、合并、变基、reset、clean、暂存或丢弃操作。
- 本任务改动为上述 9 个后端文件和 1 个新报告；以实际补丁及未跟踪文件列表审查，不将用户文件纳入任务补丁。
- 对基线已有的 151 个受保护文件逐字节/SHA-256 核对 HEAD：145 个 doc 文件、3 个 migration 文件、go.mod、go.sum、冻结 writer 所在测试文件，全部一致。本报告是新文件。
- 第五阶段冻结文档、S6/S7 冻结设计、WebSocket 协议正文、6-K.0 契约及历史报告均未修改；001 和其他 migration 均未修改；未改变 K02～K06 或开展其他 UI 工作。
- 7 个修改过的已有 Go 文件原本均为 LF，本轮仍为 LF，CRLF 和裸 CR 均为 0；没有无关文件换行符转换。
- 用户原有 `luan-qi-ba-zao.txt` 的修改继续保留，与本轮采集的基线 SHA-256 相同；原有未跟踪 `web-ssh-d41909fa.zip` 继续保留。
- 完整任务补丁和文件校验清单保存于 `/tmp/codex/web-ssh-6k-review/`，未写入受保护历史报告。

## 9. 仍保留的限制

本轮唯一未解决的测试失败是用户明确要求保留的慢写 writer 子测试。锁身份检查针对受信任宿主上的路径别名和获取期间的变化，不赋予其他进程并发移动/替换数据库、锁文件或私钥卷的操作权限。卷故障后的结果记录若遇到独立数据库故障，继续安全暂停，由原持久化 claim 保底；不会把未确认提交写成成功。
