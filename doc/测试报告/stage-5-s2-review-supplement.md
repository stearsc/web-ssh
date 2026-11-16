# 第五阶段 S2 验收补充：确定性 U09、R01 与原始测试日志

日期：2026-11-17（Asia/Shanghai）。本报告补充 `stage-5-s2-test-report.md` 中 U09 的证据；原报告保留历史记录。本次不修改生产代码，不包含 S3。

## 1. 基线与修改范围

- 分支：`codex/第五阶段`。
- 开始时 HEAD：`10b0c757d4ba3a687ce51bd4f77fad1f82f852f8`；完成时保持相同。
- 开始时状态：` M luan-qi-ba-zao.txt`、`?? web-ssh-stage5-2-2.zip`、`?? web-ssh-stage5-2.zip`，暂存区为空。
- 开始时 `git diff --stat`：用户文本 1 行新增、1 行删除；`git diff --check` 退出码 0。
- 本次修改 `backend/internal/session/takeover_test.go`：U09 真实 worker 失败观测；R01 凭证内容断言与 Attach 入口屏障。两个辅助类型均定义在该测试文件内。
- 新增本报告及 `doc/测试报告/stage-5-s2-review-logs/` 中的原始日志和命令结果。
- 不修改原 S2 报告、binding.go、connection.go、其他生产文件、第五阶段计划或冻结文档。没有改前端、S1、S3、依赖、配置或数据库 schema；SQLite trigger 仅在临时测试数据库中创建。
- 不执行 commit、push、merge、rebase、还原、清理或部署。逐一比对开始时所有已跟踪文件和两个用户 ZIP 的 SHA-256，除本次获准测试文件外均保持一致，包括用户文本已有修改。

## 2. U09 原问题与真实生产路径

原 `metadata failure` 子测试安装 SQLite trigger 后，测试自身额外执行 UPDATE 验证 trigger，再立即检查内存。这证明故障注入能生效，却没有等待 takeover 触发的异步写入完成；内存断言可能早于该次失败。

实际生产路径保持不变：

1. `Session.Attach` 唯一提交点发布新 Binding、递增 generation、置 CONNECTED、更新时间并调用 `markDirty()`。
2. `markDirty` 非阻塞写入容量为 1 的 `s.dirty` 通知槽，允许合并通知。
3. 单一 `Session.metadata` worker 消费 dirty，在 `Session.mu` 内读取状态/时间快照，释放锁。
4. worker 调用真实 `db.ExecContext` 执行 `UPDATE sessions ... WHERE id=?`。
5. ExecContext 返回错误后，worker 调用现有 `manager.log.Error("Session 元数据更新失败", "session_id", s.id)`。

本次只在测试中利用第 5 步，不增加生产 hook 或修改 worker。

## 3. U09 确定性同步与归属证明

测试在尚未创建 Session/metadata worker 时安装 `metadataFailureObserver` 日志 Handler，并安装拒绝 sessions UPDATE 的 trigger；随后真实创建 Session、完成首次 attach。

精确时序：

1. 等待目标 Session 的第一次 metadata 错误日志。Handler 发送 session_id 后，阻塞在测试 release 通道；此时旧 UPDATE 已经返回错误，worker 不可能继续读取下一份快照。
2. 首次 attach 已完成，排空此前创建/attach 留下的 dirty 通知。所有旧通知已处理或清除；没有旧 UPDATE 在数据库内等待完成。
3. Prepare takeover 后断言 dirty 仍为 0；执行真实 Attach 提交并完成旧 Binding 锁外收尾。
4. 在 worker 仍暂停时断言 dirty 恰为 1、错误计数仍为 1。此唯一通知由本次 takeover 提交的 markDirty 产生。旧 Binding 的 Detach 因对象已被替换，不改变当前 Session，也不产生 dirty。
5. 释放 Handler；worker 从第一次日志调用返回，消费唯一 dirty，读取新 CONNECTED 状态和接入时间，再执行真实 UPDATE。trigger 使其失败。
6. 等待目标 Session 第二次错误日志，确认计数恰为 2 且 dirty 已消费。此时才执行 Binding/代次/状态/List 不回退断言。

归属不依靠日志中推测的 generation：测试通过暂停单 worker、清空旧通知、隔离唯一新通知，排除了旧请求和其他生产者。观察期间只有一个 Session、没有 Close、没有当前 Binding Detach，没有测试主动执行 UPDATE。第二次错误必然来自 takeover 后那次真实 worker 写入。

确认失败后的断言：

- `s.binding` 仍为提交结果中的新 Binding；active=true、superseded=false。
- generation=2、status=CONNECTED、离线期限为空。
- Manager.List 返回该 Session 的 CONNECTED、generation=2。
- 旧 Binding 仍无效且 superseded，不恢复旧权限。
- 最后真实 Close 并等待 Done，确认列表移除；原有 Manager/SSH/数据库 cleanup 正常完成。

资源：release 使用 sync.Once + defer，无论成功或 Fatal 都先解除 Handler 等待，再执行 setup 注册的 cleanup。后续错误通知采用有界缓冲及非阻塞发送，最终关闭阶段不会因无人消费日志而阻塞。未新增常驻 goroutine；没有使用 time.Sleep，超时仅用于断言等待上限。

## 4. R01 屏障复核与补强

原测试已有 ready/start 屏障和首个快照 hook，能够安排同轮候选，但 ready 只证明 goroutine 已到 start 等待点，不能证明每个候选已进入 Attach。此次保留原安全断言并增加以下证据：

- 启动前持凭证索引锁，逐个验证 8 个候选的记录存在、Session ID 相同、expected_generation 等于当前代次、loginID 一致且 TTL 有效。覆盖 distinct、same_token、attach_takeover 三组。
- 每个候选传入仅测试使用的 `candidateEntryContext`。Attach 内部建立操作门等待预算时，`context.WithTimeout` 会调用父 context 的 Deadline；该方法发送 entered 并暂停。
- 主测试必须收到 8 次 entered 才释放入口屏障；此时 8 次真实 Attach 都已通过 token 格式检查，进入操作门等待准备阶段，且 generation 仍未变化。
- 统一释放后，所有候选竞争同一个真实操作门；第一个候选在快照 hook 内暂停，其余候选无法越过门提交。再释放快照屏障，收集全部 8 个结果。
- 恰好一个非空成功结果；其余错误只允许 INVALID_CREDENTIAL/BIND_CONFLICT，结果与错误必须相符。收齐失败方后断言赢家仍是当前有效对象、状态 CONNECTED、generation 只增加一次；旧对象不能恢复。
- 随后的新代次 takeover 仍可成功，在线 attach 仍然 BIND_BUSY。

这些屏障保证全部调用在首个提交前已经进入同一轮竞争，不声称所有线程必须在操作系统同一个时间片执行，或所有失败者都必须已经阻塞在 channel send 上。没有使用 sleep，也没有生产 hook 改动。入口与快照释放通道均有 defer 兜底。

## 5. 验证命令和统计

验证结果见下方追加的实际运行记录。完整 Go 命令在 WSL Ubuntu 的 `/home/x/workspaces/web-ssh/backend` 执行。JSON stdout 直接保存为原始文件，未做过滤、重排或摘要替换；stderr 单独保留。运行器记录 subprocess 的真实 returncode，任一检查失败会保留日志并返回非零总状态。

Go 版本：`go version go1.26.5 linux/amd64`。

| 实际命令 | 退出码 | 耗时（秒） | stdout 文件 |
|---|---:|---:|---|
| `go test -race -count=1 -run "TestTakeoverU09CountersOffsetsAndMetadata\|TestTakeoverR01R02CredentialCompetition" -timeout 60s ./internal/session` | 0 | — | 定向检查终端输出 |
| `go test -count=1 ./...` | 0 | 24.0 | `backend-tests-text.log` |
| `go test -race -count=1 ./...` | 0 | 25.79 | `backend-race-text.log` |
| `go test -count=1 -json ./...` | 0 | 23.6 | `backend-tests.json` |
| `go test -race -count=1 -json ./...` | 0 | 25.8 | `backend-race.json` |
| `go vet ./...` | 0 | 0.22 | `vet.log` |
| `go build ./...` | 0 | 0.6 | `build.log` |

JSON 运行 stdout 保存路径相当于以下重定向（stderr 另存同目录对应文件）：

```bash
go test -count=1 -json ./... > ../doc/测试报告/stage-5-s2-review-logs/backend-tests.json
go test -race -count=1 -json ./... > ../doc/测试报告/stage-5-s2-review-logs/backend-race.json
```

| JSON 日志 | 顶层 PASS/FAIL/SKIP | 子测试 PASS/FAIL/SKIP | 有测试包 PASS/FAIL | 无测试文件包 SKIP |
|---|---|---|---|---:|
| `backend-tests.json` | 102/0/0 | 210/0/0 | 10/0 | 4 |
| `backend-race.json` | 102/0/0 | 210/0/0 | 10/0 | 4 |

两次 JSON 运行合计各有 312 个测试节点 PASS（102 顶层 + 210 子测试），FAIL=0、测试 SKIP=0。统计时父测试和子测试分列，不把 4 个无测试文件包的 SKIP 计为通过用例或失败。普通文本运行和 race 文本运行也均退出 0。

在普通及 race JSON 中逐项确认 U09 metadata_failure 与 R01 distinct/same_token/attach_takeover 子测试 PASS；均包含“已观测真实 metadata worker：旧写入失败暂停 → 清空旧通知 → takeover 唯一 dirty → 新 UPDATE 失败”的原始测试输出。

完整检查 race 的文本、JSON 与两份 stderr，`DATA RACE` 和 `WARNING: DATA RACE` 均为 0 次。没有失败日志被删除，没有降低断言或跳过测试。

## 6. 日志交付与 Git 最终检查

所有本轮完整检查的原始输出、stderr、Go 版本和退出码都保存在与本报告同目录的 `stage-5-s2-review-logs/`：

- [backend-tests.json](stage-5-s2-review-logs/backend-tests.json)：普通测试原始 JSON。
- [backend-race.json](stage-5-s2-review-logs/backend-race.json)：Race 测试原始 JSON。
- [command-results.json](stage-5-s2-review-logs/command-results.json)：六条完整命令的真实退出码和耗时。
- `backend-tests-text.log`、`backend-race-text.log`：用户要求的非 JSON 完整运行输出。
- 对应 `*.stderr.log`、`vet.log`、`build.log` 和 `go-version.txt` 保留原样，空文件表示该流无输出。

本次测试文件 diff：115 行新增、8 行删除；另新增本补充报告和日志目录。用户文本原有 diff 仍为 1 行新增、1 行删除，两个 ZIP 仍未跟踪。最终暂存区为空，HEAD 不变。执行前后 `git status --short`、`git diff --stat`、`git diff --check` 均已检查；diff --check 前后退出码均为 0；本次 Go 文件 gofmt 检查通过。

所有开始时的已跟踪文件及两个 ZIP 均经 SHA-256 对比：仅获准的 takeover_test.go 变化。生产代码、原报告、实施计划、冻结设计和用户文件均未变化。

## 7. 结论与边界

U09 已确定性观测 takeover 唯一通知触发的真实 metadata UPDATE 失败，并在失败后验证内存权限/代次/状态不回退。R01 增加同代次凭证断言与真实 Attach 入口屏障，保留并通过唯一赢家及失败方不扰动赢家的断言。

没有修改生产代码或新增生产观测点，没有 S3 业务改动。没有运行真实 WS 接管专项验收，不将本次内部测试当作 S3 通过证据。测试观测只在测试文件内有效；失败快照/回收等本轮未授权的其他行为保持原样。

完成后停止，未提交、未推送、未部署，等待进一步审查与授权。
