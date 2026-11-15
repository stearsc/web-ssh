# Web SSH 第四阶段详细实施计划

- 修订日期：2026-11-15。
- 状态：凭证 HTTP 超时拆分设计已修订，两个历史 BLOCKER 已按用户确认解除；待人工审查。本次不授权编码，不表示实现或测试通过。
- 阶段名称：Session 生命周期与输出缓存。
- 本轮仅修订核心需求、WebSocket 协议和本计划三份文档，保留 21 节结构；架构、阶段指南、历史报告、业务代码及实际配置文件均不修改。
- 本轮基线分支：`codex/第三阶段`；HEAD：`f7fd93af48c7c80715fa70c9f324392f3cd0244d`。
- 本轮开始时工作区和暂存区均为空，本计划已进入 HEAD；保留 `luan-qi-ba-zao.txt` 及所有非授权文件的原始内容，不清理或覆盖用户文件。

## 1. 阶段目标、非目标与授权边界

本阶段在第三阶段既有 Session、attach、Buffer、WebSocket Supervisor 和 xterm tracker 上实现当前终端标签的有限自动恢复、人工恢复闭环，并完善输出异常、生命周期竞争和资源回收。

必须交付：单标签串行恢复协调器、按配置限制的重试预算、结构化恢复状态、断线排空和终端重建、原标签内“刷新 Session 状态 → 再次点击手动恢复”、登出/关闭/退出清理，以及相应自动化和真实环境验收。

明确不交付：活动 Session 列表 API、独立会话管理页面、跨设备发现/管理/打开、页面刷新后重新发现标签、`mode=takeover`、主动替换有效 Binding、绕过 BIND_BUSY、SSH 重建恢复、进程重启恢复、屏幕快照或服务端终端模拟器。

人工恢复仅作用于页面内已经打开且仍保留的原标签，不提供输入任意 Session ID 的入口，不从本地持久存储恢复标签。多设备 attach 测试验证现有后端约束，不构成跨设备产品功能交付。

后续实施须另获明确授权；不得因本文件完成而自动开始编码、修改冻结文档、安装依赖或提交 Git。本文件中的文件清单和测试均为未来实施要求。

## 2. 冻结依据与解释边界

协议依据为本轮基线 HEAD 的四份设计及本次明确批准的核心配置、client_settings 和协议参考实现修订；不混用其他版本。关闭码、错误分类及 Binding 规则不变。第三阶段报告仅作历史记录，代码仅作实现证据，原型仅作布局参考。文中已有代码及带行号的历史文档证据对应本轮基线 HEAD；修订后的设计以章节/锚点定位。本次解除两项历史设计阻塞，不授权编码。

| 依据 | 对应章节与权威内容 | 本计划的落实位置 |
|---|---|---|
| [核心需求](web-ssh-design-development-plan.md) | §5、§6、§7、§11.2、§11.4–11.7、§12、§17：认证、产品行为、参数、API、错误、存储、阶段 | §5、§7–9、§13–15 |
| [架构](web-ssh-architecture-implementation-plan.md) | §3–7、§9–10：所有权、锁、生命周期、提交、操作门、缓存/WS、元数据、退出 | §3–5、§11–14、§16 |
| [协议](web-ssh-websocket-protocol.md) | §2 绑定；§4.4 error；§4.5 taken_over/closed；§5 限制；§6 tracker；§7 单任务恢复协调器；§8 时序 | §6–13、§15–18 |
| [阶段指南](web-ssh-implementation-stages.md) | §5、§6、§7、§10：第三阶段基础、第四阶段任务与验收、第五阶段边界 | §1、§18–20 |
| [第三阶段报告](stage-3-implementation-report.md) | 开头提交说明、§4–8、§9–13：已实现机制及历史验证限制 | §3、§18–19 |
| [v2 原型](web-ssh-prototype-v2.html) | 终端标签、独立会话管理、关闭确认；仅布局参考 | §1、§17 |

本轮人工决定明确了 B1：第四阶段必须完成原标签内的人工恢复；用户主动刷新详情后，还必须再次点击手动恢复。该决定落实核心 §11.4 和协议 §7、§8 已要求的“冲突停止后由用户主动操作”，不改变 attach 的服务端准入条件，也不提前实现第五阶段列表或 takeover。

协议参考协调器是重试和输出算法依据；其未展开的 Vue 状态通知、详情查询所有者和现有 connect Promise 适配由本计划明确。不得修改其错误分类、次数、代次保护或单次回放规则。发现新的实质冲突时，记录双方条款、最小建议并停止受影响实现，不以测试替代决策。

措辞校准：协议 §7 提及关闭旧 socket 后操作权失效，本地只能同步禁止旧任务发送和处理事件；服务端 Binding 何时失效由 Detach、心跳和超时决定。同节 BIND_BUSY 规则仍适用。请求 close 不等于远端已离线，也不等于全局 WS 名额已经归还。

### 2.1 已确认的设计依据与本次变更

- 协议 §7 是“单任务恢复协调器”；§6 定义 tracker 稳定资格，阶段指南 §6 是第四阶段、§7 是第五阶段。恢复参数、次数和稳定规则保持原样。
- 协议 §4.4/§4.5 使用大写业务 code：OUTPUT_GAP 对应4002并有限恢复，taken_over/4001停止，1000停止，1006/1011/1012/1013按既有预算有限恢复；不引入其他版本的错误码或字段。
- 本次唯一配置增量是 `http.credential_timeout`，通过 `/api/auth/me` 的 `client_settings.credential_timeout` 下发；凭证 POST 改读此字段，WS 的 bind_timeout 来源和用途保持独立。详情 GET 仅主动/生命周期取消，无本地自动期限。
- 两个历史审查问题的最终处理见 §21.2，均已解除；新配置尚未实现属于后续获准编码任务，不再作为待裁决设计阻塞。

## 3. 第三阶段现状与复用点

下列路径均相对于仓库根目录；实现前须再次检查实际内容，不能将历史报告当成当前测试结果。

| 模块 / 现有位置 | 已有能力 | 第四阶段增量 |
|---|---|---|
| `backend/internal/session/session.go`：Create、Sweep、Close、reap、metadata、StopAt | 应用派生生命周期、内存索引、离线期限、统一关闭、最终元数据、总期限 | 完善竞争/故障路径和诊断，保持所有权 |
| `backend/internal/server/session.go`：Reserve、Lease.Release | 服务器锁内快照和名额预占，幂等释放 | 验证创建/关闭/退出交错，无新增名额体系 |
| `backend/internal/sshclient/terminal.go`：Open、Close、Keepalive | Shell 前安装输出、真实 SSH/PTY、底层关闭解除阻塞 | 阻塞和退出回归，保持 WS/SSH 解耦 |
| `backend/internal/session/binding.go`：Prepare、Attach、Detach、Execute、Revoke | attach-only、一次性凭证、代次、唯一提交、操作门、旧权限撤销 | 多登录竞争及恢复场景回归，不加入替换有效 Binding 分支 |
| `backend/internal/session/buffer.go` | 双上限原始字节环、绝对偏移、稳定快照、缺口检测 | 边界、淘汰、连续性和释放验收 |
| `backend/internal/websocket/{service,connection,protocol}.go` | 升级预占、Reader/Writer/执行器、Ping、独立 Supervisor、资源收尾 | 重复异常、退出期限、名额基线和诊断 |
| `backend/internal/httpapi/sessions.go` | 创建、详情、关闭、attach 凭证、WS 路由 | 复用原接口，无列表路由 |
| `backend/cmd/server/run.go` | 登录撤销联动、ConnContext、共同退出预算、关库顺序 | 恢复/绑定/创建/关闭中的退出集成验收 |
| `frontend/src/terminal/client.ts` | 手动 attach、代次记忆、输入分块/背压、resize 合并、发送任务 | 唯一长期恢复任务、可靠 stop、结果/状态通知 |
| `frontend/src/terminal/output.ts` | 单在途 write、回调偏移、队列上限、drain、epoch、重建 | 协议 readySince/consumeStability 和恢复集成 |
| `frontend/src/components/terminal/TerminalPane.vue` | 终端 factory、手动连接、查询、尺寸/焦点、独立关闭确认 | 详情请求所有权、人工闭环、独立 UI 状态 |
| `frontend/src/pages/HomePage.vue` | SessionView 标签、v-show 保留组件、登出入口 | 协调本页标签停止及登出，不增加会话列表 |
| `frontend/src/{api/sessions.ts,api/management.ts,stores/auth.ts,router/index.ts,pages/LoginPage.vue}` | SessionView、可传 AbortSignal 的请求、认证、logoutPending、退出重试 | 复用认证流程，补齐取消、意图保持和过期分流 |

已确认的实现差距：client 的 connect Promise 在绑定前缀完成即结束；stop 在 controller/finish 尚未创建的 drain/settle 窗口没有同步停止标志；inspect 只显示文字，不保存供用户确认的最新详情；tracker 尚无稳定资格；UI 没有长期恢复状态。第四阶段修正这些差距，不宣称本次已经修复代码。

## 4. 整体结构与唯一所有者

保持每标签一个 TerminalClient 和一个 TerminalOutputTracker，由 TerminalPane 创建并持有。HomePage 只拥有标签集合和页面级登出操作，不执行每个 Session 的恢复循环。后端不增加恢复协调器。

- TerminalClient：唯一恢复任务、单次连接尝试、记住的绑定代次、凭证请求、当前 WS、重试等待、输入/resize 发送、stop/dispose。
- TerminalOutputTracker：具体 xterm 实例的接收/处理游标、队列、在途 write、排空、终端 epoch、稳定资格。
- TerminalPane：详情查询及其取消、人工操作候选快照、UI 状态、尺寸/焦点、远端关闭动作；不复制第二套重试循环。
- HomePage 与现有 auth store：保持退出意图，在首次 await 前禁止本页所有标签的新恢复/刷新并调用停止入口；成功后销毁标签并离开页面。

只使用必要的本地任务身份和取消标记，不增加全局任务队列、事件总线、持久化恢复状态或依赖。任务 ID、查询序号、terminalEpoch 均是本地隔离手段，不是服务端 binding_generation，三者不得互换。

## 5. Session 生命周期与状态转换

| 当前状态 | 触发与后续状态 | 恢复约束 |
|---|---|---|
| CONNECTING | SSH/PTY/Shell 就绪到 DISCONNECTED；失败到 CLOSING | 不可签发/提交绑定，仍占服务器名额 |
| DISCONNECTED | attach 提交到 CONNECTED；到期/故障/手动关闭到 CLOSING | 无有效 Binding；只有未到期且校验通过才可绑定 |
| CONNECTED | 当前 Binding 的 Detach 到 DISCONNECTED；关闭到 CLOSING | attach 不替换；同代次 BIND_BUSY，异代次 BIND_CONFLICT |
| CLOSING | 资源和必要清理完成到 CLOSED | 不可绑定；旧 Detach 不改变状态或重设离线期限 |
| CLOSED | 从活动索引移除；短暂留在索引也不向详情返回 | 详情 SESSION_NOT_FOUND，重复 DELETE 幂等成功 |

浏览器断开、协议错误、慢端、输出缺口、登出只解除对应浏览器权限，不直接 Close SSH。离线期间 SSH 读取和缓存继续，远端命令按其自身行为运行。SSH 错误、远端退出、Keepalive 阈值、离线期限、明确手动关闭及应用退出才走既有统一关闭。

Sweep 提交关闭时在 Session 锁内重新校验 DISCONNECTED、无当前 Binding、当前期限已到；若设计保存过扫描候选，须确认同一期限。现有锁内直接检查再 closeLocked 的实现可复用，不引入锁外无条件关闭。成功绑定清期限，旧回调不能撤销新绑定或延长旧期限。

## 6. 自动恢复协调器

### 6.1 启动、任务和尝试

首次创建标签以创建响应 SessionView 的代次启动。后续用户启动仅经 §8 的人工流程。每次用户启动创建一条长期任务和独立预算；自动重试始终留在同一任务内，不创建新的用户任务。

每标签最多一个有效长期任务、一个当前 CONNECTING/OPEN WS、一项当前凭证请求和一项重试等待。旧 WS 可以处于 CLOSING，移除 handler 后不再处理其应用事件；服务端仍登记并回收它。

每次尝试按协议执行：冻结旧接收并等待排空 → 检查任务身份/停止/登出/销毁 → 读取该实例 processedOffset → 申请新 attach 凭证 → 复查有效性 → 创建 WS、先设 arraybuffer 再装 handler → 首条 bind → 验证成功前缀 → 一次回放 → 连续实时输出。

各层期限按 §6.3 执行。凭证 HTTP POST 只使用 client_settings.credential_timeout 定时 Abort，不能读取 bind_timeout；这不保证服务端停止处理。详情 GET 仅支持用户主动和组件生命周期取消，无本地自动超时。WS 完整前缀总等待保持既有算式，不能当成 HTTP 期限。

### 6.2 结束、代次和重复事件

一次尝试只有一个幂等结束入口；业务 error、网络 error、close、tracker 失败和用户停止竞争时，首个确定结果负责冻结输出、撤销本地发送许可、清 handler/计时器并结束该尝试。后到事件不得再调度重试或覆盖原因。

代次的唯一自动更新来源是本尝试通过校验的 bind_success，必须等于本次 expected_generation + 1。即使后续 history_truncated 未到而连接失败，已获知成功代次仍有效；若 bind_success 未收到，则不猜测是否提交，不自增，不采用诊断查询的新代次。

INVALID_CREDENTIAL 的合并语义、重新申请及停止边界见 §15.4，不能猜成另一版本错误或自动换代；BIND_BUSY 的凭证和代次变化见 §7.2。BIND_CONFLICT 终止长期任务并保留标签。INPUT_BACKPRESSURE 停止自动恢复，明确输入可能未全部发送，绝不自动补发。closed/taken_over 禁用输入并有限排空已接受队列；本阶段不新增任何主动接管入口。

### 6.3 分层超时与已确认的配置拆分

下表默认值以修订后的核心 §7.1 为准。新增 http.credential_timeout 默认5秒；其余字段与基线 `backend/internal/config/defaults.yaml` 一致。部署值可覆盖默认值。设计要求 Go 配置为正整数秒、前端 number 秒仅在 setTimeout 边界乘1000；新增字段同样校验 JS 安全整数和2147483647毫秒计时器上限。“已有”只指当前代码，新字段尚待实现。

| 层 / 配置（默认值） | 起始 → 结束条件 | 取消和边界 | 代码证据 / 实现状态 |
|---|---|---|---|
| HTTP凭证POST服务端：server.request_timeout（30秒） | handler建立请求context → 返回；net/http另管理读写期限 | defer cancel、客户端断开或应用取消；不能当作浏览器收响应保证 | `backend/cmd/server/run.go:90,115–134`已有。Prepare未接收context，锁等待不能被该context抢断，不承诺30秒强停所有内部操作 |
| HTTP凭证POST浏览器：http.credential_timeout → client_settings.credential_timeout（5秒） | 请求前启动timer → request及响应读取/校验结束后finally清timer | 超时AbortController.abort；用户停止/stop/dispose取消；不保证服务端停止或未签发 | 协议§6 ClientSettings及§7 prepareCredential参考代码已修订；实际 `frontend/src/terminal/client.ts:84–108` 仍为旧实现，配置/DTO/读取需后续编码迁移 |
| HTTP详情GET服务端：server.request_timeout（30秒）；浏览器不设置本地定时超时 | 服务端同上；浏览器fetch/JSON完成、失败或显式取消，无本地定时上界 | 用户主动取消及组件生命周期取消；request支持AbortSignal，当前inspect未传，后续按§11/§15.3接入 | `backend/cmd/server/run.go:132–134`、`frontend/src/api/management.ts:51–84`、`frontend/src/components/terminal/TerminalPane.vue:66–72`；不得借用bind_timeout |
| Upgrade后首条完整bind：websocket.bind_timeout（5秒），继续下发client_settings.bind_timeout | 升级成功记录upgraded → 收到并解析合法首条bind、关闭bindReceived | timer.Stop、连接ctx取消；超时Terminate(BIND_TIMEOUT) | `backend/internal/websocket/service.go:79–82`、`connection.go:104–114,202–213`已有；协议§2.2/§5、架构§7.5 |
| 服务端绑定操作许可：session.operation_wait_timeout（5秒） | Attach等待gate前WithTimeout → 取得gate或期限/父ctx到达 | defer cancel；失败BIND_BUSY，未消费凭证 | `backend/internal/session/binding.go:102–109,153–158`已有；仅界定gate等待，不是整个提交事务的可抢断总预算 |
| WS单次应用消息写：websocket.write_timeout（10秒） | 每次conn.Write前建context → Write返回 | cancel；错误进入统一终止，底层关闭可更早解除阻塞 | `backend/internal/websocket/connection.go:388–394,415–417`已有；终止通知取min(剩余关闭预算,write_timeout) |
| WS Ping/Pong：websocket.ping_interval（30秒）、pong_timeout（10秒） | 绑定后设nextPing；每次Ping完成后设下一周期；单次调用前建context → Ping返回 | 返回后cancel；首次终止取消在途Ping，失败走终止 | `connection.go:225,325–340,85–86`已有；pong_timeout覆盖写Ping及等Pong，不再叠加写期限 |
| WS关闭总预算：websocket.close_timeout（10秒） | 首次terminate确定绝对截止 → 工作任务退出；到期由监督器强关 | 取应用退出剩余期限较小者；不重置预算；raw.Close解除阻塞，完成停止timer | `connection.go:61–95,139–161,406–424`已有；通知/在途写/握手共享，期限到达不等于名额已归还 |
| 本地terminal.write排空：terminal.write_drain_timeout（10秒） | settle首次有队列/在途write时计时 → 排空或超时 | 完成清timer；超时隔离旧epoch并重建，dispose中不建替代实例 | `frontend/src/terminal/output.ts:185–220`已有；多次settle共用drainTask，不按每块重新计时 |
| 客户端等WS完整前缀：bind_timeout+operation_wait_timeout+write_timeout（默认20秒） | new WebSocket后安装bindTimer → 完整前缀、失败或取消 | finish/前缀完成clearTimeout，结束当前socket | `frontend/src/terminal/client.ts:114–146,178–182`已有；协议§7:710–712,743有同一算式。仅此等待有相加依据，不代表服务端各时钟严格串行 |

表中简写 connection.go 均指 `backend/internal/websocket/connection.go`。补充分层：`websocket.message_read_timeout`默认10秒，`connection.go:165–187`在Reader返回消息读取器之后计时，读完停表，到期取消读取context并终止；不覆盖Reader返回前半帧头，也不等于HTTP或bind等待。

配置职责已确定：http.credential_timeout 只约束浏览器凭证 POST 本地等待，websocket.bind_timeout 只保留 WS 绑定用途；两者默认值相同但来源独立，不互相回退。server.request_timeout 仍是服务端 HTTP 预算，session.operation_wait_timeout 仍是 Session 操作等待，其他期限不变。不增加详情 GET 超时字段或计时器，也不把服务端预算描述为浏览器完成保证。

实际代码迁移边界：`backend/internal/config/{types.go,defaults.yaml,config.go}`、`config.example.yaml`、`backend/internal/httpapi/management.go` 和前端 ClientSettings/配置校验/凭证定时器仍需在未来编码时落实新字段、默认值、范围与下发来源。严格配置解析下，当前程序尚不支持新增 http 配置组；不能把文档 YAML 已更新当成可直接部署。本轮不修改上述任何文件，也不让前端静默回退到旧字段。

## 7. 重试预算与稳定资格

### 7.1 参数来源与既有算法

恢复算法来自当前协议§7:480–484、625–655、760–769；稳定资格来自§6:311–316、462–474；阶段指南§6.2第1–3项要求落实。当前 `frontend/src/terminal/client.ts:30` 仍声明没有自动重连，参数存在不等于协调器已实现。

| 字段 | 类型、单位、默认值 | 配置来源 |
|---|---|---|
| reconnect_max_attempts | Go int64→JSON number；正整数次数，1～10；默认10 | `backend/internal/config/types.go:73`、`defaults.yaml:72`，键terminal.reconnect_max_attempts |
| reconnect_delay | Go int64→JSON number；正整数秒；默认5 | 同目录 `types.go:74`、`defaults.yaml:74`，键terminal.reconnect_delay |
| reconnect_stable_time | Go int64→JSON number；正整数秒且不小于delay；默认30 | 同目录 `types.go:75`、`defaults.yaml:75`，键terminal.reconnect_stable_time |

`config.example.yaml:72–75`同样定义三项；`backend/internal/config/config.go:124–158,244–248`校验范围。HTTP字段/组装/路由在 `backend/internal/httpapi/management.go:49–55,61–64,131–134`。前端由 `frontend/src/stores/auth.ts:29–30` 请求/me保存identity，`components/terminal/TerminalPane.vue:107`调用 `terminal/client.ts:9–28` 的clientSettings检查正安全整数；这两个路径前缀均为 `frontend/src/`，ClientSettings类型见 `frontend/src/terminal/output.ts:13–15`。这三个恢复参数无需替代或新增；本次独立新增的 credential_timeout 见§6.3/§9.2。

参数只读认证client_settings；10/5/30是既有默认配置，不是本计划定义；不得另设前端默认值、指数退避、抖动或开关。

1. 每次用户启动令 retryCount=0，首次尝试不占自动次数。
2. 尝试因可重试原因结束，先在结束时刻消费一次稳定资格，再按协议排空；排空等待时间不能让已断线连接补足稳定时长。
3. 若曾稳定，retryCount 重置为 0；资格立即清空，包括后续 HTTP 请求失败也不能再次使用。
4. 若retryCount已达到max则停止；否则加1，再启动reconnect_delay定时器。间隔从本次结果settle完成、确认允许继续之后起算，不从断线事件、HTTP发起或bind_success起算；排空及HTTP/网络耗时不计入该间隔。等待被用户取消后不再联网，也不转成可重试网络失败。
5. 稳定起点是完整前缀且 processedOffset >= replay_end_offset 的时刻；从该时刻连续保持连接达到 stable_time 才合格。bind_success、收到全部历史但回调未完成、排空期间均不算稳定。
6. begin 新尝试、终端重建及终端失败按协议清稳定状态。反复绑定后立刻 OUTPUT_GAP 必须耗尽，不能每次绑定恢复预算。
7. 同一用户任务内 HTTP 失败、WS 未绑定失败、绑定后故障共用预算。手动开始的新任务有新预算，但必须经过用户刷新、再次点击及旧任务收尾，不得在后台伪造用户动作。

人工恢复的首次失败若属当前冻结契约的可重试类型，同样使用新任务预算及原代次；UI显示失败及等待。不可重试或预算耗尽后为任务最终失败。等待中想重新查询，须先显式停止再刷新，不并行启动第二条链。默认每段未重新获得稳定资格的预算最多10次自动尝试，初始用户尝试另计；新连接真正稳定后可再次获得一次重置资格，不是长期任务永远只能重置一次。实施仍须另获编码授权。

### 7.2 BIND_BUSY的签发、消费和代次

1. `backend/internal/session/binding.go:39–74` 的Prepare先canAttach，再生成/保存凭证。同代次仍有Binding在76–92行返回BIND_BUSY（HTTP409，`backend/internal/httpapi/response.go:101–103`）；该请求未签发凭证，不创建WS。canAttach先检查代次，异代次优先BIND_CONFLICT。
2. Attach的gate等待超时/父ctx取消（binding.go:102–109）、canAttach（128–129、153–155）及提交前ctx取消（156–158）也可返回BIND_BUSY；都在159–173唯一提交点之前，凭证未被该失败尝试消费，generation未被该尝试增加。其他提交、撤销或TTL仍可使该凭证失效，不能保证它继续可用。
3. 当前协议§7:631–636要求每次尝试先Prepare再WS。失败通道收尾后重新申请新凭证，不重复提交旧token；未消费token按TTL/撤销清理，不增加回滚接口。重复签发可能遇CREDENTIAL_LIMIT，仍共用预算。
4. BIND_BUSY重试保持相同expected_generation；只有已校验bind_success自动更新本地代次。成功Attach在159–173行删除本Session全部凭证并generation++；Detach在211–225行只撤销原Binding、置DISCONNECTED并设置离线期限，**不递增代次**。
5. 查询CONNECTED后同代次Prepare先忙，不应不断创建WS；旧Binding自然Detach且代次未变后才可能签发。gate忙路径可能已有失败WS，须移除handler并结束尝试后重试，旧物理连接可短暂CLOSING，仍占容量；不替换旧有效Binding。
6. 预算耗尽停止任务，已消费人工候选不能复用；按§8重新显式查询，再由用户单独点击恢复，不得用旧快照重启预算。竞态变为INVALID_CREDENTIAL/BIND_CONFLICT时按§15.4处理，不猜新代次。


## 8. 人工恢复完整流程（B1）

### 8.1 明确的两次用户动作

自动任务停止后保留原标签、Session ID、终端及其处理偏移，展示停止原因。不得因 BIND_CONFLICT 自动请求凭证、自动采用新代次或改为 takeover。

1. 用户点击“刷新 Session 状态”。TerminalPane 确认旧长期任务已结束或先等待已启动的 stop 完成，随后发起一次详情 GET。
2. 返回通过验证的 SessionView 后展示状态、代次和离线信息，保存仅属于本标签/当前身份/本次显式查询的候选快照。成功查询不创建任务、不申请凭证、不打开 WS，也不修改旧客户端记住的代次。
3. 用户必须再次点击“手动恢复”。处理器在首次 await 前锁定本次人工动作、取出并消费候选快照，禁止重复点击；等待旧任务、发送任务和排空收尾，重新检查标签/登出/任务身份。
4. 使用候选快照的 binding_generation 作为新任务起始 expected_generation，执行既有普通 attach 和 bind。偏移来自 tracker，绝不来自详情、缓存末尾或其他终端。
5. 服务端重新判断状态、期限、身份、代次、Binding、凭证和 offset。查询快照不是预留、锁或保证。
6. 任务最终失败后保留结果，要求用户重新点击刷新取得新候选，再点击手动恢复；不得复用上次已消费候选反复启动新的预算。

### 8.2 状态与可操作性

| 查询或本地结果 | 显示与人工操作 |
|---|---|
| DISCONNECTED，服务端尚未到离线期限 | 可点击普通手动恢复；是否到期仍由签发/提交决定 |
| CONNECTED，本地已无有效恢复任务 | 展示“当前存在绑定，普通恢复不会接管”；允许用户显式发起普通 attach 的有限等待，服务端同代次返回 BIND_BUSY，只有旧绑定自然解除后才可能成功 |
| CONNECTING / CLOSING | 展示状态，不启用手动恢复，只允许后续用户刷新或已有关闭操作 |
| SESSION_NOT_FOUND | 标记不存在/已结束，禁止恢复；不从 SQLite 或旧 props 复活 |
| 查询失败或响应非法 | 展示查询失败，候选为空；禁止用旧快照或自算代次恢复 |
| 本地收到 closed | 保留终端尾输出及结束原因，禁止恢复已关闭 Session |
| 本地收到taken_over或Close4001 | 按协议§4.5停止当前连接和自动恢复；裸4001不伪造为已收到taken_over业务消息。保留提示/状态查询，不引入输出缺口解释、兼容分支或接管入口 |
| 身份失效、退出意图未完成、标签销毁中 | 禁止刷新/启动恢复；按 §13 处理 |

CONNECTED行仅允许发起受严格校验的普通请求，不允许在CONNECTED签发或提交attach。Prepare忙未签发、Attach忙未消费，后续原代次新申请见§7.2；INVALID_CREDENTIAL不代表可以采用新代次，见§15.4。不会主动关闭原设备、替换 Binding 或使用 takeover。当前标签本来仍在线时不显示这个等待入口，防止重复创建本地客户端。

### 8.3 候选和查询的生命周期

用户刷新开始即清空旧候选；重复刷新取消前请求并增加查询序号，只接受最后请求。人工动作进行中禁用刷新/重复恢复；停止动作可以中断。新任务启动、登出、关闭远端意图或销毁使旧查询/候选失效。

不为快照发明 TTL 或保证“最新”的时间窗口；“最新”只表示该 GET 的服务端快照。每次人工动作仅用最后一次成功显式查询、未被后续动作失效的快照，服务端最终复查保证正确性。

协议要求的自动诊断刷新与此显式刷新分开：诊断结果只展示，不自动生成可供手动恢复消费的候选。用户仍须亲自刷新并再次点击，满足两次动作约束。

## 9. API 契约：复用接口，补充凭证超时下发字段

实际代码 `session.View.Generation` 的 JSON 名称已经是 binding_generation；SessionView TypeScript 也已有该字段，详情通过 Manager.Get 获取当前内存快照。Session及凭证请求/响应字段、API路径、错误码和数据库结构均不变。本次唯一DTO设计增量是 /api/auth/me.client_settings.credential_timeout，对应新增配置 http.credential_timeout；当前代码尚未下发该字段，详见§9.2。

| 接口 | 请求 | 成功及用途 |
|---|---|---|
| GET /api/auth/me | 有效 Cookie | 200，expires_at与client_settings；设计补充credential_timeout，其余字段不变 |
| GET /api/sessions/{id} | 无业务请求体，有效 Cookie | 200，SessionView；人工查询及终止后诊断 |
| POST /api/sessions/{id}/takeover | `{"mode":"attach","expected_generation":G}` | 200，credential、expires_at；路径名称不表示启用 takeover |
| GET /ws/sessions/{id} | Cookie、合法 Origin、容量准入 | HTTP/1.1 Upgrade 后发送 `{"type":"bind","credential":"…","requested_start_offset":P}` |
| DELETE /api/sessions/{id} | `{}` 或无请求体；独立用户确认 | 200、data={} 表示进入幂等关闭；不保证已回收完毕 |
| POST /api/auth/logout | `{}` 或无请求体、合法 Origin | 200 才确认退出完成；无效/过期/冻结 Cookie 仍可幂等重试 |

创建接口 POST /api/servers/{id}/sessions 保持第三阶段用途，不用于恢复。GET /api/sessions 列表虽在核心总契约中定义，本阶段不实现、不调用。

SessionView 字段沿用核心 §11.4：id、server_id、name、host、username、port、status、created_at、disconnected_at、last_attached_at、offline_expires_at、remaining_seconds、binding_generation、current_cols、current_rows。空时间和非离线剩余秒数返回 null；客户端不能把缺字段默认为 0。

binding_generation 是服务端当前绑定代次，非负 JS 安全整数，初始 0，仅成功绑定递增。CONNECTING/CONNECTED/DISCONNECTED/CLOSING 的详情都带此字段；CLOSED 不返回详情而报 SESSION_NOT_FOUND。它不是凭证、设备 ID、权限证明或输出 offset。

返回统一 `{code,message,data}`，时间 UTC RFC3339 毫秒精度，Cache-Control: no-store。详情必须验证对象、目标 id、允许状态、代次安全整数及所用时间/尺寸字段；类型断言不能代替验证。响应非法作为本地协议/数据错误停止该动作，不猜测新代次。

错误沿用核心注册表：AUTH_REQUIRED(401)、PERMISSION_DENIED(403)、SESSION_NOT_FOUND(404)、SESSION_NOT_READY/BIND_BUSY/BIND_CONFLICT(409)、CREDENTIAL_LIMIT/CONNECTION_LIMIT_REACHED(429)、SERVER_SHUTTING_DOWN(503)，以及已有通用错误。INVALID_CREDENTIAL、INVALID_OFFSET 等为已升级 WS 错误，不新增同名 HTTP 分支。失败 data 通常是 {}，不能假设 BIND_CONFLICT 响应含最新代次。

单管理员可访问本应用 Session，但凭证绑定签发时的独立登录和目标，非仅管理员身份。写请求与 Upgrade 严格校验 Origin；详情 GET 按既有认证规则，不擅自修改 Cookie/Origin 模型。凭证只进入首条 bind，不进入 URL、日志、持久存储或状态快照。

### 9.1 字段位置、详情状态与校验现状

| 字段 | 服务端定义及取值 | 前端定义/读取 |
|---|---|---|
| SessionView.binding_generation | `backend/internal/session/session.go:34` uint64；viewLocked:290取锁内s.generation | `frontend/src/api/sessions.ts:10` number；`TerminalPane.vue:110`初始取props |
| SessionView.status | 同文件28行string；290行取s.status | `sessions.ts:9`四态TS联合类型，非运行时枚举校验 |
| SessionView.remaining_seconds | 同文件33行*int64；293–296行仅DISCONNECTED按剩余时间向上取整且不小于0，其他状态null | `sessions.ts:17` number或null |
| SessionView.offline_expires_at | 同文件32行*string；293–294行仅DISCONNECTED输出s.expires，其他状态null | `sessions.ts:16` string或null |
| /api/auth/me.client_settings | `backend/internal/httpapi/management.go:38–55` Settings/AuthView，61–64从Config组装，131–134返回 | `frontend/src/api/management.ts:36–39` Record<string,number>；auth.ts:29–30读取，Pane:107交clientSettings校验 |
| reconnect_max_attempts/delay/stable_time | `management.go:49–51`原名JSON字段；配置、默认值及校验链详见§7.1 | `frontend/src/terminal/client.ts:21–28`已有正安全整数检查，但自动循环和稳定资格未实现 |

表内sessions.ts均指 `frontend/src/api/sessions.ts`，TerminalPane.vue/Pane指 `frontend/src/components/terminal/TerminalPane.vue`，auth.ts指 `frontend/src/stores/auth.ts`，末行management.go指 `backend/internal/httpapi/management.go`。

详情路由 `backend/internal/httpapi/sessions.go:37–44` 在protected认证后调用Manager.Get。`session.go:182–210` 的Find/Get拒绝已移除或CLOSED，Get再持Session锁调用viewLocked，所以CONNECTING、CONNECTED、DISCONNECTED、CLOSING都能返回当前内存快照；CLOSED返回SESSION_NOT_FOUND/HTTP404，不从SQLite读旧代次。响应发出后状态仍会变化；remaining_seconds=0不保证可绑定，Prepare/Attach复查期限。

现有 `frontend/src/api/management.ts:66–84` 只检查信封，最终返回body.data as T；`TerminalPane.vue:66–72` 的inspect没有AbortSignal、序号或字段校验，仅更新提示。因此§9所要求的详情运行时校验是未来增量，不能称已存在。当前没有人工候选闭环，不能声称已观察到错误自动换代；但迟到响应可覆盖提示，未来直接复用props/旧候选会有风险。§8.3要求刷新开始清候选，失败或响应丢失不得回退旧快照；只有字段及身份校验通过才保存候选，最终仍由服务端裁决。取消与定时分别见§15.3、§6.3。

### 9.2 client_settings 的最终来源与实现差距

| 字段 | 配置唯一来源 | 类型/单位/默认值 | 用途及当前实现状态 |
|---|---|---|---|
| credential_timeout | http.credential_timeout | 正整数秒，默认5；JSON number | 浏览器凭证POST本地等待；当前Config、Settings DTO和前端读取尚未增加，未来需一起实现 |
| bind_timeout | websocket.bind_timeout | 正整数秒，默认5；JSON number | 服务端升级后首条完整bind期限；继续原样下发，不能用作HTTP期限 |

其余client_settings字段保持核心§11.2既有定义，除credential_timeout外不新增字段。前端从/me读取并校验正安全整数及计时器范围，不自行推算、不用另一字段兜底。绑定凭证HTTP的请求参数和成功响应仍为原契约，不在凭证响应中新加超时值；详情GET不新增超时字段。

## 10. 前端任务状态、结果和 Promise（I2）

### 10.1 两种 Promise

- 一次连接 Promise：表示一次凭证申请及初始绑定前缀的结果。完整 bind_success 加必要的 history_truncated 处理完成后成功结束；此前失败或取消也必须结束并给出明确结果。它不等待整段历史 write 完成，更不等待长期连接退出。
- 长期任务 Promise：覆盖初次尝试、在线等待、断开、有限重试及最终排空收尾。仅在该任务真正终止并完成本地清理后结束；stop/dispose 等待它。内部单次 attempt 的连接结束信号与“绑定已建立”的 Promise 不得混为一个完成点。

现有 connect 的初始绑定反馈可以保留为一次连接 Promise 的外观；内部另持有长期任务。不得复制协议类直接用长期 run Promise 替换 TerminalPane 的 await connect。每次自动重连也发同样的建立/回放状态通知，尺寸适配不只依赖首次 await。

一次连接失败不一定等于长期任务停止：例如 BIND_BUSY 首次失败后长期任务可能进入有限重试。UI 应同时表达本次失败和后续等待。所有 Promise 的取消、失败及 finally 都有消费者，无悬挂 Promise 或未处理拒绝。

### 10.2 最小必要状态与通知

恢复阶段可用明确枚举表达：idle、preparing、connecting、replaying、online、retry_wait、stopping、stopped、disposed。它们仅是本地状态，不写入 Session.status 或数据库。

分别维护：连接阶段忙状态、恢复阶段及 attempt/max/停止原因、详情查询状态、人工动作锁、远端关闭请求状态、退出意图/登出请求状态。长期任务存在不意味着所有按钮 busy；始终允许停止和关闭标签，明确远端关闭操作不受长期 online 任务阻挡。

协调器向所属 Pane 提供带任务身份的结构化通知：阶段、自动尝试计数及上限、原始业务或本地原因 code、展示 message、是否需要诊断刷新、终止类别。不是新增 WS 消息。不得解析 message 做控制决策。

旧任务通知在 Pane 的身份校验失败时丢弃；当前 stop 动作可发布 stopping/stopped，旧尝试的 finally 不得清空新任务状态。服务端详情快照独立展示，不能用本地 online 或 socket open 冒充服务端权威事实。

## 11. stop / dispose 与所有异步边界（I1）

stop 在首次 await 之前同步设置停止标记、使旧任务发布权限失效、禁用输入/resize、取消重试等待和凭证请求、结束当前尝试并移除 socket handler。Pane 的停止入口同时取消该标签的详情查询及人工候选。即使 controller/finish 尚为空，也已禁止旧流程继续创建资源。

新任务只能在旧长期任务、发送任务、必要 drain 和取消清理完成之后启动。重复 stop 汇合到同一轮停止完成过程；stop 保留终端供后续人工操作，不能将永久 disposed 标记当作普通停止。

| 异步位置 | 恢复执行前必须检查 | 停止后处理 |
|---|---|---|
| drain / settle 返回 | 当前任务身份、stopped、disposed、退出意图 | 不申请凭证；完成已经提交的本地 write 收尾 |
| fetch 发起前、响应/JSON 解析后 | 同上及 AbortSignal | 即使 abort 后成功响应晚到，也丢弃凭证，不创建 WS |
| WS 构造前及 handler 安装后 | 同上、当前 socket 身份 | 不发布新当前 socket；已取得候选资源立即关闭 |
| onopen / bind send 前 | 任务和 socket 仍当前、未停止 | 不发送 bind，不从全局读取新任务代次 |
| onmessage / 控制消息处理 | 当前尝试、socket、终端 epoch 适用 | 不接收新输出或推进任务代次，不发布旧 UI 状态 |
| xterm write 回调 | tracker 的实例 epoch 和 inFlight 身份 | 同一旧实例的已提交 write 仍可在 drain 中完成偏移；不得污染新实例或重新启用输入 |
| 重试定时器完成 | 任务身份及停止/退出标志 | 不申请下一凭证，清计时器和取消函数 |
| 详情响应与 finally | Pane 生命周期、查询序号、任务关联、退出意图 | 不更新快照、候选、错误或新请求的 loading |
| nextTick / 发送背压等待 | 当前终端/任务/视图有效 | 不聚焦已销毁或隐藏实例，不发送旧输入/尺寸 |

允许 tracker 在停止时处理已提交 write，是偏移正确性要求；“旧任务不得更新 UI”不等于丢掉该回调。只有当前停止所有者发布收尾状态。输入和待发送粘贴绝不跨连接重发。

dispose 在首次 await 前不可逆标记销毁，同时停止网络和通知、取消详情、标记 tracker 正在销毁。有限排空后销毁实例、监听器、Observer；排空超时或 write 异常不得创建替代终端。重复 dispose 返回同一任务，此后任何 connect/run/人工恢复都拒绝。页面卸载即使不能阻塞路由，也必须同步完成准入封闭并捕获异步清理结果。

## 12. 输出缓存、排空、重建与终端交互（I4）

### 12.1 字节与偏移

任务说明中的 receivedOffset 在现有代码和协议中对应 receiveCursor，表示已接收末端；processedOffset 仅表示同一 xterm 实例连续完成 write 回调的末端。不得为了命名统一新增第三套游标或改变语义。

单次 bind 请求 P；绑定快照为 S=max(P,buffer.start)、E=buffer.end。P>E 拒绝。S=P 保留实例及解析器；S>P 等唯一 history_truncated 校验后重建，以 S 为明确损失后的基线。后续实时输出从 E 连续衔接；任何块不得跨历史/实时边界混合。

后端不解码 UTF-8/ANSI、不移动字符边界、不补字节。双上限、LF/尾行定义和安全整数上界沿用架构 §7。当前 WS 出现 OUTPUT_GAP 就终止，不取第二快照。截断提示位于终端外，并明确原始后缀不能恢复全部颜色、光标或 vim/top 画面。

### 12.2 排空及稳定判定

普通断线/可恢复故障：立即 freeze(false)，丢未提交队列，保留唯一在途 write；在 write_drain_timeout 内等待后再读取 processedOffset。超时或 write 抛错销毁旧实例，隔离旧回调，新实例从 0 请求，仍受原任务预算约束。

closed/taken_over/用户 stop 使用有限 keepQueued 排空；终止原因一旦确定不能因晚到 onclose 改成另一种排空策略。dispose 中超时仅释放，不重建。队列和在途总字节不超过 write_queue_bytes；当前待检查一块至多另占 output_chunk_size，不声称控制浏览器内部全部网络缓冲。

tracker 接入协议 readySince/updateReady/consumeStability；回调完成才可能开始稳定计时，稳定资格只消费一次。队列、实例、偏移与稳定状态共用同一 tracker，不在 Vue 组件另算偏移或稳定时长。

### 12.3 尺寸、焦点、输入顺序

1. WS OPEN 不启用输入；成功前缀完成后进入 replaying。无需截断的空回放可立即 online；有截断的空回放也必须先处理 history_truncated。
2. bind_success 后已具备同步 resize 的协议许可。UI 可在完整前缀处理完成后统一触发尺寸同步，避免向即将销毁的旧实例适配；不等待历史排空或长期任务结束。
3. 活动可见标签在当前实例挂载/nextTick 后 fit，夹紧配置尺寸并通过现有有界发送入口发送最新 resize。仅保留最新待发送尺寸，不恢复旧 socket 的尺寸队列。
4. 隐藏标签不按零宽容器 fit，不抢焦点；保留最近有效本地尺寸，新建实例无本地尺寸时采用有效初始化尺寸。恢复前缀后同步该有效尺寸；重新显示时再 fit 并同步当前可视尺寸。
5. 终端 factory 每次重建须安装 onData/onBinary/onResize，旧实例 dispose 清这些监听。重建通知关联当前任务/实例，nextTick 后再检查身份。
6. 输入仅在前缀完整且 processedOffset>=E 时启用；回放期间普通输入、粘贴及历史查询触发的自动应答全部丢弃，不缓存待发送。resize 可以先于历史完成，不改变输出偏移或创建 SSH。
7. 首次连接或用户人工恢复完成后，仅在标签仍活动、可见且未退出/销毁时执行初始焦点处理；后台自动恢复不强制抢焦点。用户切回标签按既有交互 fit 并聚焦。

## 13. 登录、登出与恢复协调（I5）

复用现有 auth store、logoutPending、request 的 AUTH_REQUIRED 通知及登录页退出重试，不新增认证协议。logoutPending 表示退出意图未完成，不能兼作一次 HTTP 请求的 busy；登出请求另有串行状态。

### 13.1 主动登出顺序

1. 用户点击登出，在首次 await 前设置退出意图。该意图同步阻止所有标签新建任务、人工恢复、详情刷新和继续自动尝试；HomePage 对持有的 Pane 调用停止入口，不能仅等路由卸载。
2. 取消已有凭证/详情、重试、发送及 WS 尝试，有限排空终端。登出 HTTP 可与本地收尾并行发起，不能因 drain 延迟服务端撤销；最终页面清理等待本地收尾结果，异常必须捕获。
3. 凭证签发与登出并发由登录读写锁裁决：撤销先完成则拒绝，提交先完成则撤销扫描解除它；浏览器取消请求不能撤销已经签发或已提交的服务端事实。
4. 本地即使收到晚到凭证、onopen、bind_success 或详情，也因退出意图/任务身份失效而不创建连接或更新当前 UI。已签发未使用凭证按现有登录撤销或 TTL 清理，无新增撤销 API。
5. 只有 logout 成功确认后清身份、销毁标签并进入登录页。已注销的本地任务不因其他请求成功而恢复。

### 13.2 失败、过期与重试

| 结果 | 身份与 UI | 恢复任务 |
|---|---|---|
| logout 200 | 按既有契约清 Cookie/身份；确认退出完成 | 本地停止/销毁完成后离开；旧回调无效 |
| STORAGE_ERROR，确定回滚 | 保留 Cookie、原身份，留在未完成退出状态，显示“退出未完成，请重试” | 保留退出意图，继续禁止新恢复，不自动重新联网 |
| SERVER_UNAVAILABLE，提交不确定 | 不宣称退出成功；服务端冻结身份，Cookie 只供 logout 重试 | 保持停止，保留原退出意图及重试入口，不通过 me 成功/失败自行恢复权限 |
| 网络错误/响应丢失 | 结果未知，保留退出意图，允许用户幂等重试 logout | 不自动重启终端，不盲目重新登录覆盖原 Cookie |
| 无主动退出意图时 AUTH_REQUIRED/到期 | 停止任务，按现有认证流程提示需登录并清理页面 | 不将认证错误当普通网络失败，不自动重试 |
| 主动退出尚未完成时 AUTH_REQUIRED/到期回调 | 不覆盖“退出未完成”为“退出成功/普通登录过期”；保留 logout 重试能力 | 继续停止；认证/路由通知不得清掉退出意图 |

重复登出点击只使用一个在途请求；失败后用户可再次请求。为了不覆盖原 Cookie，退出意图未完成时登录入口应引导先重试退出，不以新登录替代原退出。此为现有失败契约的 UI 衔接，不添加新认证状态到服务器。

不自动撤销用户退出意图或恢复已停止的标签。本阶段不增加“取消退出并自动重连”流程。普通登出不调用 Session DELETE，SSH 按离线保留规则继续运行。硬刷新后的 Session 发现不属于本阶段。

## 14. 后端资源释放与应用退出

| 资源/操作 | 所有者、释放时机与禁止事项 |
|---|---|
| SSH Connection、Channel、PTY、读取/退出/Keepalive | Session；统一 Close 先撤销权限、取消任务，底层连接关闭解除阻塞，独立 reaper 等待；不能由 WS 故障直接关闭 SSH |
| 服务器 Session 名额 | ServerState/Lease；覆盖创建、在线、离线、关闭中；完成回收或契约清理期限处理后幂等释放，不用 DB 行数代替 |
| Buffer | Session；SSH 读取持续追加；关闭收尾释放。被终止 Writer 释放自己的快照，不能误清仍运行 Session 缓存 |
| Binding / 凭证 | Session Manager；Detach 对象身份校验，提交消费/其他凭证失效，登录撤销/Session Close/退出清理，TTL 有界 |
| WS 升级名额 | websocket.Service；Upgrade 前预占；失败释放；成功后 Reader/Writer/执行器/计时器/Supervisor 完成才归还 |
| WS 通道 | 单 Reader、单应用 Writer、独立 Supervisor；首次终止确定绝对期限并取消在途 Ping，到期 raw.Close；不依赖通知送达或 CloseNow 抢断已开始 Close |
| 远端输入/resize | Binding 执行器和 Session 操作门；未准入旧项拒绝，已准入一次调用可完成；SSH 操作超时经 Session Close 解除阻塞，不重试输入 |
| 运行元数据 | 每 Session 单写入器、容量 1 dirty 合并、独立清理 context；失败记录但不回滚内存/权限，不积累无限重试，最终快照不被旧写覆盖 |
| 全应用退出 | 原子停止接入；同一 shutdown_timeout 绝对期限内取消建连/探测/测试、关闭 Session/全部 WS、等待任务后关库；到期强关并记录未完成项退出 |

第三阶段已有上述机制；后续编码先用定向测试识别差距再最小修正，不为“第四阶段”重写正常实现。Keepalive 至多一个在途请求，拒绝回复也算有响应，连续超时等同一请求并最终强关；WS Ping 与之独立。

应用退出不是前端 dispose：服务端关闭真实资源，浏览器收到 SERVER_SHUTTING_DOWN 可按协议有限尝试；不能要求退出后的服务端去管理浏览器重试，也不能无限等待浏览器通知确认。

诊断沿用日志，记录 Session ID、操作/关闭原因、限制命中、阶段、截止时间和未完成清理对象；不得记录 Cookie、凭证、probe、认证材料、完整终端输入输出。无需新增监控服务或公开诊断 API。

## 15. 错误分类与状态刷新所有权（I3）

### 15.1 明确错误 code

下表以协议§4.4和§7 retryCodes为依据；不引入其他版本的错误码或retryable字段。关闭码及重试分类经本次确认保持不变；现有第三阶段客户端尚无自动循环，本计划不授权编码。

| 分类 | code | 动作 |
|---|---|---|
| 有限重试 | BIND_BUSY、INVALID_CREDENTIAL、BIND_TIMEOUT、OUTPUT_GAP、SLOW_CLIENT、SERVER_SHUTTING_DOWN、INTERNAL_ERROR、CREDENTIAL_LIMIT、CONNECTION_LIMIT_REACHED | 结束当前尝试，按 §7 原任务预算申请新 attach；不重复旧凭证、不自动换代 |
| 本地可重试 | CLIENT_OVERLOAD、CLIENT_TERMINAL_ERROR、NETWORK_ERROR、CLIENT_BIND_TIMEOUT | 同上，保持 tracker 的丢队列/重建规则；CLIENT_* 等不是新增服务器错误 |
| 必须停止 | BIND_CONFLICT、AUTH_REQUIRED、PERMISSION_DENIED、INPUT_BACKPRESSURE、INVALID_MESSAGE、INVALID_OFFSET、INVALID_SIZE、ALREADY_BOUND、MESSAGE_TOO_LARGE | 停自动恢复、展示明确原因；认证按 §13；其余用户操作按 §8 |
| 停止并刷新详情 | SESSION_NOT_FOUND、SESSION_NOT_READY | 协调器结束并发出一次结构化终止结果，Pane 按 §15.3 查询 |
| 最终通知 | closed、taken_over | 停输入/自动恢复，有限保留队列排空；closed 不恢复，taken_over 不借本阶段入口主动接管 |
| 其他明确 HTTP 业务错误 | 如 INVALID_REQUEST、SERVER_UNAVAILABLE、STORAGE_ERROR | 不在协议 retryCodes 中则停止；保留原 code，不因 5xx/429 就一律重试 |
| HTTP 传输失败或无法完成响应解析 | 按协议产生本地 NETWORK_ERROR；可识别的契约字段校验失败为协议错误 | 前者有限预算，后者停止；用户主动取消始终为停止，不包装成网络重试 |

浏览器错误可观察性：原生WebSocket API在Upgrade被服务端拒绝时，不向JavaScript暴露完整HTTP状态码和JSON错误响应体。即使服务端返回CONNECTION_LIMIT_REACHED，onerror也不能据此直接识别该code；客户端可能只能观察到NETWORK_ERROR，仍按原有限预算恢复，不推测业务原因。凭证POST使用fetch，明确HTTP code仍按上表解析分类。新增凭证本地期限到达而任务未停止时，沿用参考循环的NETWORK_ERROR路径；用户主动取消/stop/dispose始终停止，不转成重试。不得增加错误码、额外探测接口或改变预算。

### 15.2 Close code 回退

已收到明确业务终止结果时不由后续Close覆盖。当前协议§4.4:158、§4.5:175–177、§7:752–755规定：无业务结果时1006、1011、1012、1013、4002可有限重试；1000、4001停止并保留有限队列；1008、1009及其他未列码不自动重试。`backend/internal/websocket/protocol.go:68–84`将OUTPUT_GAP映射为4002；`frontend/src/terminal/client.ts:198–200`对1000/4001保留队列，但尚无自动循环。

taken_over按明确业务消息处理；4001保持其taken_over关闭语义，停止连接和自动恢复，不伪造已收到业务消息或Session状态。1000停止自动恢复，但不能单凭该码证明SSH已关闭，必要时用详情确认。4002保持OUTPUT_GAP含义并允许有限恢复，不兼容另一套解释。

1008 同时承载可重试和不可重试业务错误，没有明确 code 就不能猜成 BIND_BUSY。CLIENT_OVERLOAD 主动 close(4003) 后由本地原因决定有限恢复；不能把任意收到的 4003 自动纳入原生 close 重试集合。浏览器只调用允许的 1000 或 3000–4999 关闭码。

### 15.3 谁停止、谁查询

协调器负责立即停输入、停止该任务并完成通道/排空收尾；Pane 接收带任务身份的终止结果，对 SESSION_NOT_FOUND/SESSION_NOT_READY 各终止任务至多发起一次诊断 GET，不递归重试查询、不轮询。

详情请求由Pane单独持有AbortController、查询序号、loading/error/snapshot；发起前等待任务收尾，检查标签尚在、无新任务/退出意图。提供用户主动取消和组件生命周期取消，不建立本地自动超时计时器；不能复用credential_timeout或bind_timeout。服务端request_timeout不是浏览器本地完成保证。取消时同步使查询身份和候选失效，晚到结果不得覆盖新状态；失败仅显示结果并等待用户刷新。

结果只有在查询序号、Pane 生命周期、触发任务身份及认证状态都仍匹配时才能更新展示。用户显式刷新可取消并替代诊断查询；旧 finally 不能清新请求 loading。GET 返回 SESSION_NOT_FOUND 即记录不存在，不因同名错误再次 GET。SESSION_NOT_READY 后查到 DISCONNECTED 也只展示，不继续恢复。

诊断查询不消耗或重置恢复预算，不修改客户端代次，不生成 §8 的人工候选。标签 stop/dispose、登出、人工恢复、新任务或远端关闭动作均取消旧查询。未知查询结果不等于 Session 已关闭，不调用 DELETE。

### 15.4 后端错误、协议与凭证映射

`backend/internal/fault/fault.go:3–6`定义字符串fault.Error，会话错误多为字符串值，并非各有独立命名常量。HTTP的 `backend/internal/httpapi/response.go:52–126` 保留已注册大写code；WS Reader在 `backend/internal/websocket/connection.go:213–216` 将Attach错误交Terminate，finish:406–417生成type/code/message。没有小写映射或retryable字段，message不能用于猜测子原因。

| 后端实际错误及位置 | 当前WS协议错误 | 是否自动重试（当前冻结基线） | 凭证是否重新申请 | 是否需要用户操作 |
|---|---|---|---|---|
| 过期：binding.go:122–123、147–148 → INVALID_CREDENTIAL | INVALID_CREDENTIAL / 1008 | 仅明确code下有限预算；不重连旧凭证 | 是，原expected_generation | 耗尽/后续冲突后按§8 |
| 已消费或被赢家失效：159–163删除，122–123找不到 → INVALID_CREDENTIAL | 同上 | 同上；不推测赢家代次 | 是；原代次Prepare可被BIND_CONFLICT拒绝 | 冲突后刷新并人工恢复 |
| 不存在或格式无效：98–100、122–123 → INVALID_CREDENTIAL | 同上 | 同上；无法区分不存在/消费/过期子因 | 是，不重复token | 有限失败后需要 |
| 凭证登录/目标不匹配：125–126 → PERMISSION_DENIED；登录失效由login.Acquire返回AUTH_REQUIRED | PERMISSION_DENIED / AUTH_REQUIRED，1008 | 否 | 不自动申请 | 权限/认证处理，不以新凭证规避 |
| 预期代次不符：canAttach:86–87 → BIND_CONFLICT；Prepare为HTTP409 | BIND_CONFLICT / 1008 | 否 | 不自动申请 | 必须显式刷新再操作 |
| 有效Binding：89–90；或gate/ctx等待失败102–107、156–157 → BIND_BUSY；Prepare为HTTP409 | BIND_BUSY / 1008 | 有限预算、原代次 | 是；Prepare忙未签发，Attach忙未消费 | 耗尽后刷新再操作 |
| CONNECTING/CLOSING、离线到期：80–84；计数耗尽131–133 → SESSION_NOT_READY | SESSION_NOT_READY / 1008；Prepare为HTTP409 | 否，停止并诊断查询 | 否 | 按查询状态处理，关闭/到期不能靠旧快照恢复 |
| 已移除/CLOSED：session.go:182–210 → SESSION_NOT_FOUND；Upgrade路由先Find | SESSION_NOT_FOUND为协议允许码；当前常见HTTP404，已取得目标的Attach可能返回NOT_READY | 否，停止并诊断查询 | 否 | 展示不存在/结束，不恢复 |

表内binding.go和session.go分别指 `backend/internal/session/binding.go` 和 `backend/internal/session/session.go`。当前INVALID_CREDENTIAL本来就合并多种原因，服务端没有可供前端区分的字段；“过期/消费/不存在”不得编造成不同重试路径。另一版本的invalid_bind在当前协议/实现无定义，不能与INVALID_CREDENTIAL建立别名；当前§4.4:157明确的是有限重新申请，并非无限重试。本次已确认沿用该映射，不引入另一版本规则或新增子错误；Upgrade前错误的浏览器可见性限制见§15.1，表中列出服务端可能返回的错误不等于原生WS事件都能读到业务code。

## 16. 并发场景及具体裁决

| 两个竞争操作 | 裁决点 / 必须结果 |
|---|---|
| error与close、缺口与终端错误 | 当前attempt幂等结束；明确业务结果不被后续Close覆盖；4001停止、4002有限恢复，严格按§15.2处理，不猜其他映射 |
| 自动重试与人工恢复 | Pane 人工动作锁 + 旧任务 stop 完成；禁止以第二个 run 覆盖当前任务 |
| 重复状态刷新与响应乱序 | 新查询取消旧查询并推进序号；仅最后响应可发布，旧快照不能成为候选 |
| 重复人工点击 | 首次 await 前消费候选并设置动作锁；后续点击不重置预算、不发第二请求 |
| 查询成功后其他客户端 attach | 详情非预留；代次已变则 BIND_CONFLICT，停止；凭证阶段竞争可返回 INVALID_CREDENTIAL |
| 查询CONNECTED后普通attach | Prepare同代次BIND_BUSY未签发，不建WS；原Binding自然Detach不改代次，下次原代次新Prepare；Attach忙未消费，详见§7.2，不替换Binding |
| 查询成功后 Close / 离线到期 | Prepare/Attach 重新检查；CLOSING/期限已到 SESSION_NOT_READY，已移除 SESSION_NOT_FOUND；不恢复旧快照 |
| 两个独立登录同时 attach | 操作门 → 登录读锁 → Session/Buffer/凭证同步边界；一个提交成功，其他不消费赢家操作权 |
| 凭证快照准备与清理/到期 | 唯一提交前重查凭证仍存在、未到期、登录/离线期限；失败释放候选快照，不提交 |
| attach 与登出 | 登录读写锁决定先后；撤销冻结先完成则拒绝，提交先完成则撤销扫描回收；不遗漏并发发布 |
| 旧 Detach 与新绑定 | 对象身份匹配才改当前状态；旧 active 撤销不影响新 Binding，不重设新期限 |
| 旧输入/resize 与 Detach/新绑定 | 捕获所属 Binding；操作门内准入重新验证，排队旧项拒绝，已准入项仅一次执行 |
| 离线扫描与 attach | 同一 Session 状态锁复查期限及绑定；先 Close 则 attach 拒绝，先 attach 则旧扫描不能关闭 |
| stop 与尚未创建 controller 的 open | 同步停止标记和异步边界检查先封准入；不是只 abort 现有对象 |
| 查询中关闭标签/登出 | 同步使 Pane/请求/身份发布资格失效并 abort；迟到结果不更新 UI 或创建任务 |
| 新绑定成功与旧 WS 收尾 | 允许关闭中资源短暂共存；唯一有效 Binding，旧 WS 名额等待实际任务退出后归还 |
| 恢复失败与凭证残留 | 未提交凭证 TTL/撤销清理，提交后通道失败 Detach；容量不足有限重试，不另建凭证回滚 API |
| Session 回收与本地旧详情 | GET 返回不存在；UI 停止恢复并显示该事实，候选失效，不从 DB/props 补出活动 Session |
| stop/dispose 与 xterm 回调 | 仅原实例有效在途 write 可完成 drain；epoch 不同即忽略；销毁不产生替代实例 |
| 应用停止与升级/创建/提交 | 停止接入标志、发布前复查及统一截止；已登记包括升级中资源全部收尾，不等待自身 |

## 17. 前端 UI 与独立操作

保留当前终端工具栏和必要事件提示，不新增会话管理页面或常驻说明面板。展示本地连接/回放/等待/停止状态与原因，可在等待时显示本任务次数及上限；服务端详情标注为最近查询快照，不能把 CONNECTED 解释成“当前设备在线”。

- “停止恢复”停止当前任务，保留标签；停止完成后才能显式刷新、再手动恢复。
- “刷新 Session 状态”只查询；查询中禁用人工恢复。新查询失败即无候选，不能沿用旧代次。
- “手动恢复”仅在有效显式候选、旧任务完成、无退出/关闭意图且标签未终结时可用，按 §8 状态表处理。
- “关闭标签”始终可触发本地 dispose，不执行 DELETE；删除本地标签前等待可等待的收尾，卸载兜底同步封准入。
- “关闭 SSH 会话”保持独立二次确认。用户确认后同步阻止恢复并停止旧任务/取消查询，再发 DELETE；取消确认不停止原任务。
- DELETE 成功只显示“已请求关闭/关闭中”，不冒充 SSH 已回收。通过一次详情查询或明确 closed 确认；仍 CLOSING 时可由用户再刷新，不后台无限轮询。
- DELETE 失败/响应丢失保留停止状态和明确结果，允许用户查询实际状态或重试幂等关闭，不能自动恢复未知结果。若以后显式查询证实仍可绑定，用户可按 §8 开始新的人工动作。
- 远端关闭请求 busy 与连接阶段、详情查询、恢复阶段分别维护；不能因长期任务存在而无法关闭远端。

## 18. 测试计划（本轮不执行）

以下为未来获准编码后的测试计划；按已确认的独立凭证HTTP期限和现有关闭码规则验证，不能混入其他版本预期。优先扩展现有测试文件，新增文件仅在职责清晰时使用既有测试工具；不为测试升级依赖。测试必须调用实际 TerminalClient/tracker/Manager/WS 实现。可控时钟、TerminalPort 和 fetch/WS 替身用于固定交错，真实网络专项不能用 mock Reader 代替。

### 18.1 自动恢复和预算

- 普通断网恢复，Session ID、SSH Connection/Shell 数不增加。
- 多次 error/close/OUTPUT_GAP/终端失败同时触发，只有一条有效任务和一个当前 socket。
- 逐项覆盖§15 retryCodes、停止码和Close回退；1008无业务code不猜重试。当前基线OUTPUT_GAP/4002有限恢复、裸4001停止、taken_over业务消息停止；明确业务结果不被后续Close覆盖。不把4001改成缺口测试；Upgrade拒绝只暴露通用失败时按NETWORK_ERROR有限恢复，不能断言onerror可直接读取服务端HTTP业务码。
- 验证/me参数与Config一致、类型/单位/默认10次5秒30秒及读取链；初次之外最多N次自动尝试，间隔从settle完成后起算，HTTP与WS失败共同计数。
- 历史收到但 write 未回调时不稳定；历史处理完成并保持 stable_time 后下一断线重置一次。
- 一次稳定之后连续 HTTP 失败不能重复重置；每次绑定立刻缺口必须耗尽；耗尽后不再联网。
- BIND_BUSY分别验证Prepare未签发/未建WS、Attach gate忙/提交前取消未消费/未增代；重试原代次新Prepare，Detach不增代，耗尽后无后台请求。BIND_CONFLICT停止且不自动详情换代；SESSION_NOT_FOUND/READY停止并诊断一次。
- 分别注入凭证过期、消费、失效、不存在/格式错误，按实际合并INVALID_CREDENTIAL断言，不猜子因；身份不符为PERMISSION_DENIED。重新Prepare不复用失败token，赢家代次变化后BIND_CONFLICT停止，不无限循环。

### 18.2 人工恢复和查询

- 服务端已提交但 bind_success 丢失：旧代次自动冲突停止；显式刷新返回实际代次；不再点击则无 POST/WS。
- 再点击手动恢复使用查询代次，新任务独立预算，仍只发 attach。
- 查询后其他登录先 attach、Session Close、离线期限到达、代次变化：断言实际服务端拒绝及旧 Binding 保持，不用前端预检替代。
- 查询 CONNECTED 后发普通 attach 得到 BIND_BUSY，不替换；预算内旧端自然解除且代次未变可恢复。
- 两凭证同代次竞争只一个成功，失败允许 INVALID_CREDENTIAL/BIND_CONFLICT；不能自动取赢家代次。
- 查询响应乱序、连续刷新、连续恢复点击、旧查询 finally、任务切换：候选和 UI 不被旧结果覆盖。
- 查询中关闭标签/登出/stop、响应丢失、晚到成功/失败均不能启动连接或修改当前状态；覆盖SessionView运行时校验及失败不能沿用旧props/候选。详情GET不安装本地超时计时器；用户取消、关闭标签或组件销毁必须abort且隔离晚到结果。
- 查询失败清候选，失败任务结束后可重新显式查询再恢复；诊断查询不自动启用人工候选。

### 18.3 stop / dispose 和 Promise

- 在 drain、settle、凭证申请前/中/后、WS 构造前、握手、前缀、回放、重试等待、详情刷新逐处停止。
- 必测 connect 调用后 controller/finish 尚为空就 stop：之后无新 fetch/WS，所有 Promise 可结束。
- 旧任务 finally 不清新任务状态，旧通知不覆盖新 UI；停止后不继续发送输入/resize。
- 重复 stop/dispose 幂等；dispose 后 connect/run 拒绝，排空超时/异常不创建新实例。
- 一次连接 Promise 在完整前缀结束，长期 Promise 仍在运行；busy 已清，尺寸适配正常，远端关闭按钮可操作。
- 绑定失败和用户取消时两个Promise分别按职责结束；长期重试不悬挂首次连接反馈。凭证POST按credential_timeout取消，任务仍有效时沿用网络失败有限预算；详情GET无本地自动期限，只验证显式/生命周期取消，不宣称所有请求都有自动超时。
- 分层验证§6.3起止/取消：HTTP服务端预算、凭证POST本地期限、升级首bind、Attach gate、单次写、Ping/Pong、共享关闭截止、xterm drain、客户端前缀等待，不任意相加。
- 独立设置credential_timeout与bind_timeout为不同测试值，断言POST仅随前者、服务端首bind仅随后者变化；这不是改变默认值。验证缺项默认、正整数/溢出及JS计时器边界、/me独立下发、前端不回退旧字段。Abort后即使服务端已签发或响应晚到也不能创建旧任务WS；本轮只列测试、不执行。

### 18.4 登出和远端关闭

- 自动恢复、凭证请求、WS 建立、回放、重试等待、详情查询期间登出，立即封闭准入。
- logout 成功后旧响应/onopen/通知到达，不创建资源；SSH 保留而非 DELETE。
- 确定回滚、提交不确定、网络响应丢失分别保持正确文案/身份/退出意图，重试可用，不自动恢复。
- AUTH_REQUIRED 与主动退出竞争，普通到期流程不覆盖未完成退出；重复登出不发并发请求。
- 明确关闭远端与恢复竞争，DELETE 成功不是 CLOSED 证明；失败后可查询/幂等重试，取消确认不误停。

### 18.5 输出、尺寸和焦点

- 空缓存、单字节、空行、多 LF、CR、跨读取 CRLF、未换行尾部、无 LF 长输出及两个上限单独/同时触发。
- 多次淘汰偏移不重编号，计数安全上界不回绕；快照复制后淘汰不改变已有快照。
- UTF-8/ANSI 任意分割原始字节保持；连续在线端不因历史淘汰收到误截断；回放后缺口结束该 WS。
- 首块前、回放中、实时块后断线：等待在途回调后使用 processedOffset，未提交队列不冒充已处理。
- 队列含在途总量有界，慢端/排空超时/写异常重建，旧回调不影响新实例；截断提示在字节流外。
- 回放查询自动应答/输入/粘贴禁止，历史处理完成后才启用输入；resize 可先于回放完成。
- 重建监听安装/旧监听清理、隐藏恢复不聚焦、切回 fit/resize，长期 Promise 不阻塞这些动作。

### 18.6 后端竞争、真实网络及资源

- attach 与 Close/登出/离线扫描/凭证清理/过期/应用停止固定交错，验证权限、状态、期限及单赢家。
- 旧输入、resize、Detach 与新绑定竞争；已准入调用只有一次，未准入旧项不进入 SSH。
- TCP/认证/PTY/Shell 建立与取消、自然退出、操作超时、Keepalive 多窗口超时，实际任务退出。
- 所有半帧头类型、非法拼接 Pong、正常拆包、无输入六轮以上心跳、持续输出不饿死 Ping、堵写、控制队列满、消息体超时和 Close 阻塞。
- close_timeout 小于在途 pong_timeout 时显式取消 Ping；名额上限为 1 时关闭中拒绝、新连接仅在旧任务退出后成功。
- 连续 100 轮连接/阻塞/关闭及与应用退出竞争，检查实际 worker/Supervisor 完成信号、计时器、WS 名额、服务器名额及缓存释放；不能仅断言错误或客户端 close。
- 元数据 INSERT/UPDATE 失败、最终清理 context、旧写不能覆盖最终 CLOSED、无无限重试；启动历史清理失败不接入。
- 全应用退出覆盖未绑定/升级中/已绑定/关闭中 WS、创建/探测/测试/SSH 阻塞；总期限不逐步重置，无自等待，关库顺序正确。
- 连续创建/关闭 Session 后活动索引和名额恢复基线，goroutine/内存观察区分正常运行缓存与真正遗留。

### 18.7 命令与真实环境记录

获准编码后的检查命令：在 backend 执行 go vet ./...、go test ./...、go test -race ./...、go build ./...；在 frontend 执行 npm test、npm run build（已包含 typecheck）。按项目规则在 WSL Ubuntu 内运行；本轮不运行这些命令。

真实浏览器+xterm+Linux PTY/Bash 验证普通命令、中文、ANSI、vim/top、历史查询自动应答、断网恢复、高输出缺口、尾部输出和 SIGTERM。双登录仅验证 attach，不开放 takeover。真实生产 SSH、代理 HTTPS/WSS、网络切换/后台挂起及实际移动设备结果与环回/替身结果分开记录。

第三阶段报告中的 Race/Chromium/PTY 成功是历史基线，不是本阶段结果；未执行的环境保留待验，不把窄屏截图当真机，不把新增测试名称当验证完成。

## 19. 验收标准与交付门槛

- [ ] 落实已确认的独立credential_timeout配置、/me下发和浏览器计时，bind_timeout职责不变；详情GET只有主动/生命周期取消。历史阻塞已解除，实施仍须另获编码授权。

- [ ] B1 两次用户动作闭环可用，查询绝不自动恢复；代次来自详情，服务端校验不可绕过。
- [ ] I1 所有异步窗口 stop 有效，dispose 不可逆；旧任务/查询/终端回调不污染新状态。
- [ ] I2 一次连接与长期任务语义分离，busy、状态、尺寸/焦点和远端关闭均不被长期 Promise 阻塞。
- [ ] I3 NOT_FOUND/NOT_READY 终止并一次诊断刷新，所有者明确、可取消，不自动采用新代次。
- [ ] I4 字节/偏移/回放/重建正确，resize 与输入门禁分离，隐藏标签不抢焦点。
- [ ] I5 登出意图同步封闭恢复，成功/确定失败/不确定/网络失败分别正确，失败重试入口保留。
- [ ] 重试有限，稳定资格一次消费，持续缺口不能无限重置，所有错误分类及 Close 回退符合协议。
- [ ] SSH 保留、唯一有效 Binding、凭证消费、任务退出、名额及缓存释放均有实际断言。
- [ ] 100 轮故障/回收、Race、前端测试及真实 SSH/浏览器验收有可复核结果，未验环境明确列出。
- [ ] 无列表/跨设备打开/takeover/主动替换能力；仅增加已批准的http.credential_timeout及client_settings同名字段，无新接口、其他配置、数据库结构或依赖。
- [ ] 实施报告记录最终 Git、命令/环境/结果、修复、复用接口和未完成项；未经授权不提交、不进入第五阶段。

## 20. 实施顺序、输入输出与依赖

前置门槛：本轮设计修订完成后仍须明确编码授权。后续先打通新配置默认值/校验、/me下发及前端读取，再让恢复协调器使用独立凭证期限；其余步骤依赖保持不变。

| 步骤 | 输入与依赖 | 涉及文件（根目录相对路径） | 输出/关键行为 | 验收 |
|---|---|---|---|---|
| 1 后端边界与已批准配置 | 本计划、第三阶段代码、最新Git、核心§7/§11.2 | backend/internal/session、server/session.go、sshclient/terminal.go、websocket、cmd/server/run.go；config、httpapi/management.go、config.example.yaml | 复核资源边界；未来编码落实http.credential_timeout默认值/校验与/me下发 | 无新状态/接口；新字段来源独立，锁序与名额边界可追踪 |
| 2 前端任务和停止契约 | 步骤1、§6.3/§9.2/§10–11 | frontend/src/terminal/client.ts、output.ts的ClientSettings、TerminalPane.vue、既有前端测试 | 新字段读取/校验及凭证定时器；长短Promise、同步停止、任务身份、通知和取消所有者 | 两期限独立，stop空controller窗口、重复取消、旧通知隔离通过 |
| 3 自动协调与有限预算 | 步骤2、协议 §7 | client.ts、output.ts 的稳定资格入口、前端测试 | 单任务循环、错误分类、次数/间隔、原代次保护 | 首次/自动次数、稳定一次消费、冲突停止通过 |
| 4 输出专项完善 | 步骤3、现有 tracker/Buffer | output.ts、终端 factory、buffer.go 及相关测试 | drain/重建/回调隔离与恢复循环完整衔接 | 边界字节、队列、持续缺口、超时和旧回调通过 |
| 5 详情与人工闭环 | 步骤2–4、现有详情 API | frontend/src/api/sessions.ts、TerminalPane.vue、client.ts、前端及 HTTP 测试 | 独立可取消详情请求、候选、两次动作、新任务预算、I3诊断 | 查询不联网恢复、服务端竞态拒绝、重复点击/乱序通过 |
| 6 UI/尺寸/焦点/输入 | 步骤3–5 | TerminalPane.vue、HomePage.vue、相关测试；必要时现有样式 | 各操作状态独立、终端监听重建、可见性和输入门禁 | 长期任务不占连接 busy，隐藏恢复/回放/resize 正确 |
| 7 登出/关闭/退出 | 步骤2–6、现有认证/应用退出 | auth store、HomePage、router、LoginPage、api/management.ts；后端已有资源模块及测试 | 退出意图、失败重试、明确关闭及应用总期限衔接 | 登出/关闭与恢复并发、未完成退出文案、真实收尾通过 |
| 8 全面验证与报告 | 步骤1–7 | 既有 Go/前端测试；获准后的阶段报告 | 自动化、Race、100轮、真实 SSH/浏览器及环境待验记录 | 满足 §19，报告可复核后停止 |

表内简写 TerminalPane.vue 均指 frontend/src/components/terminal/TerminalPane.vue；backend/internal/session 等表示现有模块，具体改动仍须最小化。步骤3依赖稳定资格，因此先接入 output.ts 的该入口，步骤4再完成完整输出故障矩阵；不能把稳定预算留作最后补丁。步骤2先于人工恢复和登出，防止两者建立在有取消窗口的客户端上。

## 21. 本轮问题闭环、一致性与遗留事项

| 审查项 | 本计划定义 | 是否改变冻结契约 |
|---|---|---|
| B1 | §8 两次用户动作、详情代次、原标签、新任务及候选失效；§9确认接口已有字段 | 否；落实本轮人工决定和既有 attach |
| I1 | §11同步停止、异步检查、查询取消、epoch和不可逆销毁 | 否；落实协议 stop/dispose |
| I2 | §10一次建立反馈与长期任务分离、独立 UI 状态 | 否；适配现有 connect 外观 |
| I3 | §15.3协调器停止、Pane一次诊断、独立取消和发布身份 | 否；补齐协议正文的详情刷新 |
| I4 | §12.3前缀/resize/回放/输入时序、监听、可见性和焦点 | 否；保持协议 resize 可先于回放完成 |
| I5 | §13退出意图、立即停止、并行撤销、失败分类与重试 | 否；保持核心 logout 成功/失败边界 |

### 21.1 本轮设计修订结果

| 项目 | 最终决定与位置 |
|---|---|
| 配置与客户端下发 | 核心§7/§11.2新增http.credential_timeout→client_settings.credential_timeout，正整数秒、默认5；本计划§6.3/§9.2区分设计与代码现状 |
| 协议参考实现 | ClientSettings增加credential_timeout，prepareCredential定时器改读它；bind_timeout及WS前缀等待算式不变 |
| 详情GET | §6.3/§15.3明确用户主动和组件生命周期取消，无本地自动期限、不增加字段 |
| 关闭码与错误分类 | §8/§15/§18沿用4001停止、4002有限恢复及原retryCodes；补充Upgrade失败不能直接读取HTTP业务code的限制 |
| 其他阶段设计 | 人工恢复、Promise、stop/dispose、代次、有限预算、输出、登出和资源所有权保持原样；不进入第五阶段 |

### 21.2 已解决的历史审查问题与实现待办

- **BLOCKER-HTTP：已解决。** 用户已确认以独立http.credential_timeout控制凭证POST本地等待，避免与WS首条绑定期限混用；默认5秒、经/me独立下发、超时Abort不保证服务端停止。详情GET保留无本地定时超时设计，只支持主动/生命周期取消。原“取消凭证本地期限”等候选不再适用，不需要再次裁决。
- **BLOCKER-PROTOCOL：已解决。** 用户已确认采用当前协议：4001为taken_over并停止恢复，4002为OUTPUT_GAP并有限恢复，1000停止，1006/1011/1012/1013按原预算恢复；无invalid_bind或retryable字段。历史相反审查意见不再作为设计要求，不迁移或兼容另一套码表。

当前无上述未决设计问题。新增配置/默认值/校验、/me字段、前端读取和定时器尚未在代码实现，详情取消及原有第四阶段功能也仍是未来任务；这属于实现待办而非待确认设计。当前严格配置解析尚不接受新http组，不能宣称文档配置已可部署。本轮不运行测试、不修复代码，历史验证不替代未来验收。

本轮仅修改核心需求、WebSocket协议和本计划三份文档。架构、阶段指南、历史报告、代码、实际配置文件、数据库和用户文件内容保持不变；不暂存、提交或推送。设计阻塞解除不等于编码授权。

本轮结论：设计文档修订完成，两个历史BLOCKER均已解除，可以提交人工审查。停止于文档交付，等待明确的第四阶段编码授权。
