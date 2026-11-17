# Web SSH 第五阶段 S3 定点修复补充报告

日期：2026-11-17。环境：WSL Ubuntu。状态：**S3 定点修复完成，等待第二轮人工复审。未开始 S4，未 commit、push 或 deploy。**

## 1. 开始基线与用户文件

- 分支：`codex/第五阶段`。
- 开始时 HEAD：`b91546aa5739a80468d7ba5d02ac864b28ed79cd`，提交标题 `5-S3`。
- 开始暂存区为空；未暂存修改只有用户文件 `luan-qi-ba-zao.txt`，另有用户 ZIP `web-ssh-stage5-3.zip`。
- `luan-qi-ba-zao.txt` 开始 SHA256：`64d3829a0205694db5cfee00139aaec3b2d27ea98b5b351b79afb6a1813441b0`。
- 原 `web-ssh-stage5-3.zip` 开始 SHA256：`c10e8a9a334f1699bc558402f84b0d57f8dc1437e63fb0646d139546218bdd5b`，大小 693779 字节。
- 修复期间，上述两个用户文件被外部改变：`luan-qi-ba-zao.txt` 最终哈希变为 `f6f84a80124746d4a537fa2dc739bc714e12dae6680c8bcfba465fec3349357a`，原 ZIP 被移出工作区。当前差异只把 ZIP 命令从 `stage5-2` 改成 `stage5-3-2`。本轮没有编辑、移动、删除或恢复这两个用户文件；详见 `stage-5-s3-fix-logs/user-files.json`。
- S2 `binding.go`、`session.go`、`send_admission.go`、第五阶段计划和四份冻结文档均与 HEAD 逐字节相同，详见 `protected-files.json`。

## 2. 修复前问题与实际竞态

`Connection.finishBindingExit` 原先把所有非 Superseded 状态都传给 fallback。当 Session Close、Manager Revoke 或 Sweep 已经执行 Detach/取消 Binding、但尚未调用 `Binding.end` 时，Reader 的 Done 分支会看到 Inactive，并把 `NETWORK_ERROR` 抢先固定为首因。随后生命周期所有者调用的 `closed:*` 或 `AUTH_REQUIRED` 已无法覆盖，客户端可能收到错误原因和错误关闭码。

Reader 还存在第二层问题：只修改 `finishBindingExit` 不足以消除问题，因为 Reader 原来的无条件 `defer c.Terminate("NETWORK_ERROR")` 仍会再次提交 fallback。反向地，如果简单让 Reader defer 按 Binding 状态统一分类，又会在“B 已提交 supersede A、old.end 尚未执行”时把真实 TCP RST 错记成 `taken_over`。首次全量测试实际捕获了后者：`TestS3T2RealNetworkFirstReason/committed_then_network` 得到 `taken_over`，期望 `NETWORK_ERROR`。原始失败日志保存在 `stage-5-s3-fix-logs/development/attempt-1/`。

修复后的实际顺序：

1. `readMessage` 返回真实网络错误时，Reader 仍通过原有 defer 提交 `NETWORK_ERROR`；即使 Binding 已 superseded，真实网络故障先发生仍保留首因。
2. Reader 因 `Binding.Done`、Connection context 或登记拒绝等内部原因退出时，先标记抑制网络 fallback，再走受锁处置读取。
3. `finishBindingExit` 在 `Connection.mu → Session.mu` 内取得 BindingDisposition：
   - `BindingSuperseded`：固定 `taken_over`。
   - `BindingCurrent`：仅此时允许调用方 fallback，例如真实 Current `INPUT_BACKPRESSURE`。
   - `BindingInactive`：不终止、不写 fallback，只唤醒 Writer；由生命周期所有者稍后调用 `Binding.end` 发布真实原因。
4. 所有首因仍只在 Connection.mu 内首次确定；迟到 Reader、Writer、执行器或重复 end 不能覆盖。

## 3. Inactive 生产路径与终止责任

| 产生 Inactive 的路径 | 最终责任方与原因 | 唤醒/回收保证 |
|---|---|---|
| 正常 takeover 将旧 Binding 置 active=false、superseded=true | `CommitResult.FinishDisplaced` 锁外调用旧 `end("taken_over")`；sync.Once 防重复 | 旧 end 固定原因，取消 context，Writer 发 taken_over/4001 |
| `Session.Close` / `closeLocked` | 在 Session 锁内撤权并启动一次 reap；`reap` 在 SSH/在途操作收尾后调用 `old.end("closed:"+reason)` | Reader Done 只等待；end 最终固定 closed 并关闭 Connection |
| `Manager.Revoke` | 先 `Detach`，锁外立即 `b.end("AUTH_REQUIRED")` | end 固定 AUTH_REQUIRED；Service.Revoke 即使并行也只能得到同一首因 |
| `Manager.Sweep` 的登录过期分支 | 先 `Detach`，锁外 `b.end("AUTH_REQUIRED")` | 与 Revoke 相同；`Detach` 清除 s.binding 后后续扫描不会重复取得该 Binding |
| `Manager.Sweep` 的凭证过期 | 只删除尚未消费的凭证，不使当前 Binding Inactive | 当前 Binding 保持 Current；新增测试验证旧端仍可输入 |
| `Sweep` 的 DISCONNECTED idle timeout | 此时 s.binding 已为空；`closeLocked` 的 old 为 nil，无 Binding.end 需要等待 | Session reap 正常释放 |
| Connection 自身真实故障或协议错误 | 统一 Terminate 先在 c.mu 内固定原因，再 Detach/cancel；它本身就是最终责任方 | 无需外部 Binding.end 再确定原因 |
| 提交已成功但 Connection 尚未登记 | `registerBinding` 登记清理归属；已终止则 Detach 新 Binding；Inactive 则履行旧端收尾并等待原生命周期 end；Superseded 则固定 taken_over | 不在 c.mu 内等待网络/Close；每个 CommitResult 由 defer 履行 FinishDisplaced |
| Reader 等待操作槽时 Binding.Done | 走 `finishBindingExit`；Inactive 不终止 | 由上表外部所有者 end，无永久等待 |
| execute 得到 BIND_CONFLICT / INPUT_BACKPRESSURE | 走 `finishBindingExit`；Superseded/Current/Inactive 三分流 | Current 使用真实 fallback；Inactive 等外部 end |
| Writer 普通准入发现 Inactive | 不选普通消息，等待 wake/terminated | 外部 end 唤醒并发布终止槽 |
| 迟到 Detach/回调作用于 superseded 对象 | 仍由原 CommitResult 的 once 收尾 | 不影响新 Binding/generation |

生产代码中直接调用 `Session.Detach` 的非测试位置只有 `Manager.Revoke` 和 `Manager.Sweep`，两者随后都调用 Binding.end。`Session.Close` 自己执行撤权并在 reap 中 end。因此当前 S3 生产路径不存在“Inactive 但无人 end”的情况；registerBinding 与 Writer 的等待有明确发布者。多个责任方可能同时 Terminate，但 Connection.mu 的首因规则和 CommitResult.sync.Once 保证只确定一次原因、不重复清理。

## 4. 新增与补强测试

源码位置：

- `backend/internal/websocket/connection.go:129`：三分流与测试同步点。
- `backend/internal/websocket/connection.go:271`：按真实 Reader 退出原因抑制或保留网络 fallback。
- `backend/internal/websocket/takeover_test.go:395`：真实并发 old.end / Binding.Done。
- `backend/internal/websocket/takeover_test.go:494`：Close 与 Manager.Revoke。
- `backend/internal/websocket/takeover_test.go:586`：登录过期 Sweep。
- `backend/internal/websocket/takeover_test.go:644`：凭证过期 Sweep 不撤当前 Binding。
- `backend/internal/websocket/takeover_test.go:677`：Current Binding 的真实 INPUT_BACKPRESSURE。

T1 当前测试不再在旧 Connection 已终止后调用 FinishDisplaced。两个 goroutine 分别执行：

- 路径 A：`result.FinishDisplaced()` → `old.end("taken_over")`。
- 路径 B：Session Detach 取消 superseded Binding → Reader Done → `finishBindingExit`。

两个路径都到达旧 Connection 的 `beforeTermination` 屏障后，单个 gate 只关闭一次，两者真实竞争同一个 Connection.mu。测试交替让 A/B 先启动，直到两种路径各胜出至少 100 次。最终普通测试实际为 `end=106, binding_exit=100`（206 轮），Race 为 `end=102, binding_exit=100`（202 轮）。每轮均验证：

- 首因始终 `taken_over`，真实网络收到 taken_over DTO 与 Close 4001，无 fallback error 消息。
- 旧 Binding 不再发送普通消息，排队输入未进入 SSH，resize 请求为 0。
- 新 Binding 仍 Current，同一 Shell 正常回显，旧 worker/supervisor/done 与 slot 恰一次回收。

Close 测试使用相同 Reader Done 屏障，先让 Reader 完成 Inactive 处置并断言 Connection.reason 仍为空，再释放生命周期 end；最终 reason 为 `closed:user_closed`，收到 closed 消息和 Close 1000，Session/slot 全部回收。

Revoke 测试覆盖 Detach 与 end 之间窗口：Reader 不抢 fallback，最终 reason 为 `AUTH_REQUIRED`，收到 error 和 Close 1008。测试同时创建另一登录的真实 Session，确认其状态、Shell 和列表项不受影响。

Sweep 测试让受控登录在真实期限后过期，并只在测试 hook 中禁用该连接自身的登录定时器，然后由 Manager.Sweep 完成 Detach/end。Reader 先看到 Inactive 且 reason 为空，Sweep end 后固定 AUTH_REQUIRED。另一个测试等待真实凭证期限，调用 Sweep，确认当前 Binding 仍有效且旧端继续回显。

Current 故障测试让执行器阻塞、操作槽填满，Reader 等待超时后以 Current Binding 的 INPUT_BACKPRESSURE fallback 终止，确认修复没有忽略真实当前故障。

## 5. 修改文件

| 文件 | 修改 |
|---|---|
| `backend/internal/websocket/connection.go` | Inactive 三分流；Reader 区分真实网络错误与内部退出；nil 默认测试同步点；Sweep 测试仅禁用自身登录 timer |
| `backend/internal/websocket/takeover_test.go` | 替换 T1 为单 gate 真实锁竞争；新增 Close/Revoke/Sweep/Current 测试及辅助函数 |
| `doc/测试报告/stage-5-s3-fix-report.md` | 本补充报告 |
| `doc/测试报告/stage-5-s3-fix-logs/` | 最终原始日志、失败过程、统计、哈希、源码补丁与 Git 证据 |

未修改 `send_admission.go`、S2 Binding/Attach/CommitResult 事务、冻结设计、协议 DTO/关闭码、Writer 唯一发送结构、slot/Supervisor 生命周期或任何前端源文件。

## 6. 最终验证

所有命令在 WSL 对应目录执行，stdout/stderr 分开保存，Python subprocess 记录真实退出码。Go 版本：`go version go1.26.5 linux/amd64`。

| 工作目录 | 命令 | 退出码 | 秒 |
|---|---|---:|---:|
| backend | `go test -count=1 -json ./...` | 0 | 38.54 |
| backend | `go test -race -count=1 -json ./...` | 0 | 48.41 |
| backend | `go vet ./...` | 0 | 0.18 |
| backend | `go build ./...` | 0 | 0.57 |
| frontend | `npm test` | 0 | 0.69 |
| frontend | `npm run build` | 0 | 2.07 |

| 最终日志 | 顶层 PASS/FAIL/SKIP | 子测试 PASS/FAIL/SKIP | 包 PASS/FAIL/SKIP |
|---|---|---|---|
| 普通 | 118 / 0 / 0 | 451 / 0 / 0 | 10 / 0 / 4 |
| Race | 118 / 0 / 0 | 447 / 0 / 0 | 10 / 0 / 4 |

顶层与子测试分开统计，不将父测试和子测试相加冒充顶层数量。4 个无测试文件包只计 SKIP，不算失败或通过测试。Race stdout 和 stderr 中 `DATA RACE` / `WARNING: DATA RACE` 均为 0。S3 相关顶层测试 16 个；普通子测试 241、Race 子测试 237。前端 40 PASS、0 FAIL、0 SKIP。

第一次全量失败记录保留在 `development/attempt-1/`；单 gate 调整前已通过但非最终版本的日志保留在 `development/pre-common-gate-pass/`。没有删除失败日志或降低断言。

## 7. 日志与哈希

最终日志目录：`doc/测试报告/stage-5-s3-fix-logs/`。其中：

- `backend-tests.json` / `backend-race.json`：最终原始 Go JSON。
- `frontend-tests.log` / `frontend-build.log`：前端测试和类型检查+构建的完整 stdout。
- 同名 `.stderr.log`：stderr。
- `command-results.json`：六条命令真实退出码与耗时。
- `environment.json`：Go 版本和平台。
- `statistics.json`：测试及 Race 标记统计、T1 胜出次数。
- `s3-source-review.patch`：本轮源码完整 diff。
- `SHA256SUMS`：交付包内所有文件的逐字节哈希；ZIP 外另有归档自身 `.sha256`。

## 8. ZIP comment 与 HEAD 不一致原因

- 当前实际 HEAD：`b91546aa5739a80468d7ba5d02ac864b28ed79cd`。
- 当前分支：`codex/第五阶段`。
- 原 ZIP 的实际命令记录于 `~/.bash_history`：`git archive --format=zip --output=web-ssh-stage5-3.zip b91546aa`。
- ZIP 文件时间为 2026-11-17 01:41:46 +0800，ZIP comment 为标准库可读的 `b91546aa5739a80468d7ba5d02ac864b28ed79cd`，共 178 项。该 comment 由 `git archive` 的目标 commit 产生。
- 用户于 2026-11-17 01:41:26 +0800 创建提交 `b91546a`，标题 `5-S3`。随后在 01:41:46 从该提交归档。
- 旧报告在用户提交前写成并记录了当时 HEAD `47e08712...`；提交后归档的源码正确来自 `b91546a`，但报告内没有随之更新。因此两者差异是“报告写作时 HEAD”与“用户之后提交并归档的 HEAD”不同，不是另有一次隐藏提交。
- 原 ZIP 不含 `frontend-tests.log` 或 `frontend-build.log`，这与第一轮审查发现一致。本轮新交付 ZIP 单独生成，包含完整前端 stdout。

## 9. 最终 Git 与归档

本轮没有 commit、push、merge、rebase、reset、clean 或部署。HEAD 仍为 `b91546a`，暂存区仍为空。工作区包含：

- 本轮修改：`connection.go`、`takeover_test.go`。
- 本轮新增：本报告、修复日志、源码补丁、最终交付 ZIP 与外部哈希文件。
- 用户原有：当前状态的 `luan-qi-ba-zao.txt`，保持外部修改后的内容。

最终 ZIP 只包含本轮源码、补丁、报告和日志，不包含用户 ZIP 或 `luan-qi-ba-zao.txt`。`SHA256SUMS` 在 ZIP 内部按实际归档路径生成；随后解压到临时目录执行 `sha256sum -c` 验证，避免 CRLF/LF 转换造成不可复核。

## 10. 未完成与限制

- 没有进行 S4 UI、浏览器主动接管或部署验收。
- S3 仍等待第二轮独立审查；并发测试提供实际交错证据，不等同于形式化证明。
- 原用户 ZIP 在修复期间被外部移出，因而无法重新验证其当前字节；其开始哈希、comment、条目和缺失日志已记录。
- 除上述外部用户文件变化外，没有已知未修复的 S3 失败项。
