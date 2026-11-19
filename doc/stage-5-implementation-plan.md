# Web SSH 第五阶段详细实施计划

- 编写日期：2026-11-16。
- 状态：第二轮修订审查稿；S6 方案 B 与 U1–U9 已冻结；S7-0 已冻结，S7-1 尚未获得编码授权。Binding终止和Writer准入设计已修订，等待后续实施验证；未开始 S7-1 编码。
- 2026-11-18 S6 设计修订：用户已冻结候选 Pane 所有权方案 B，并裁决 U1–U9（见 §21.5）。本文中与方案 B 冲突的前端旧规则（§8.1、§8.2、§8.3、§11.3、§12.1、§15 S6/S7 行、§16、F12、§21.2 D3 行）已逐处标注“已被方案 B 替代”，原文保留供追溯；S6 前端细节以 `doc/stage-5-s6-design-plan.md` 为准。后端、协议、S5 连接契约和其他章节不变。
- 2026-11-19 S7-0 冻结：D1、D1-A～D1-E、D2-1、D2-2、D3-1、D4-1、D5-1、D5-2 已全部接受；S7-1～S7-6 的阶段边界、文件范围、测试与验收见 §21.6。S7-0 已授权完成，S7-1 尚未获得编码授权。
- 2026-11-19 S7-0 最终定点修订：DELETE 错误响应在来源不可信时一律 `unknown`，当前链路 `failed` 暂不可达；`idle + CLOSING` 采用本地关闭意图 + 服务端详情双重准入，保持 idle 但拒绝确认、零凭证/WS/takeover，且不产生 `ended`。
- 阶段：会话管理与跨设备接管。
- 首轮代码检查基线：`8dc739d90034abb16bd60009d21e3af944654c7e`。第二轮文档基线：分支 `codex/第五阶段`，HEAD `56ed63809ae8b7420362301bb768a610a2caa166`；该历史轮次开始已有用户修改 `luan-qi-ba-zao.txt` 和未跟踪 `web-ssh-stage5.zip`。S7-0 最终定点修订开始前，分支为 `S6`、HEAD 为 `00816f4`，工作区仅有 `luan-qi-ba-zao.txt` 的用户未暂存修改，暂存区为空，无未跟踪文件。
- S7-0 本轮只修订 `doc/stage-5-s7-design-decision-proposal.md` 与本文件。不修改业务代码、测试、S6 冻结设计、第四阶段计划和历史报告，不安装依赖，不提交、推送或执行分支整合。
- 用户本次确认：第四阶段、UI 状态提示残留修复及最新部署真实环境验证已经完成。第四阶段报告仍是当时“本地完成、生产待验”的历史记录；本计划不擅自改写它，也不将用户确认扩大为第五阶段已验收。

## 1. 阶段目标、非目标与交付边界

交付活动 Session 列表 API、独立会话管理 UI、跨终端标签/浏览器/设备发现与管理、用户明确发起的 `mode=takeover`、唯一有效 Binding 的原子替换、被接管端停止与反馈，以及与关闭、登出、SSH 退出、离线回收、旧连接阻塞的竞争处理。

第五阶段复用原 SSH Connection、Shell、PTY 和 OutputBuffer，不创建替代 SSH。普通 `attach` 用于首次绑定、第四阶段自动恢复和两步人工恢复；`takeover` 是用户确认的主动替换意图。页面刷新、可见性变化、列表刷新、网络 online、自动恢复均不得触发 takeover。

非目标：历史会话归档/重命名、分页或排序 API、服务端状态过滤参数、重启恢复、跨进程协调、屏幕快照、Redis/消息队列、轮询服务、新状态管理库、依赖升级、全套第六阶段视觉和移动端改版。关闭本地标签、关闭远端 Session、主动接管是三项独立操作。

本计划按冻结契约及用户本轮确认的D1–D3 A方案给出实施与验收步骤；终止竞态和发送准入修订仍须人工复审。产品选择已确认不等于编码已获授权。

## 2. 完整阅读依据、术语与权威边界

首轮编写直接分段读完下列Markdown全文，原型检查会话页面、标签及相关交互代码；第二轮定向复核当前Binding.Done/end、Detach、Reader、Writer、Terminate与架构§3.1，并按用户指出的问题修订。代码用于定位实现差距，不覆盖设计。

| 文档 | 实际读取范围与本阶段重点 |
|---|---|
| [核心需求](web-ssh-design-development-plan.md) | 全文 §1–19；重点 §5 认证、§6 Session、§7 配置、§9 会话 UI、§10 UI、§11.1/11.2/11.4–11.7 API/错误、§12 存储、§18 验收 |
| [WebSocket 协议](web-ssh-websocket-protocol.md) | 全文 §1–9，含完整 tracker/协调器参考代码；重点 §2 bind、§3 线序、§4.4/4.5、§5 期限、§6/7 输出与恢复、§8 竞争示例 |
| [架构](web-ssh-architecture-implementation-plan.md) | 全文 §1–12；重点 §3.1 锁、§4 生命周期、§5.1/5.2 凭证与提交、§6 操作门、§7.4/7.5 Writer/Supervisor、§8.1 登出、§9/10 存储/退出 |
| [总体实施阶段](web-ssh-implementation-stages.md) | 全文 §1–10；重点 §5 基础、§6 第四阶段、§7 第五阶段、§10 追踪与交付 |
| [第四阶段计划](stage-4-implementation-plan.md) | 全文 §1–21；重点 §4 所有者、§6–8 恢复、§10/11 Promise/stop/dispose、§13 登出、§15 错误/查询、§21 已解决问题 |
| [第四阶段报告](stage-4-implementation-report.md) | 全文 §1–15；实现位置、测试证据及历史环境边界 |
| [第三阶段报告](stage-3-implementation-report.md) | 全文 §1–14；Session/Binding/WS 基础、attach-only 范围及真实网络测试方法 |
| [v2 原型](web-ssh-prototype-v2.html) | 会话管理布局、状态筛选、已有标签定位、独立关闭确认；演示数组和立即切换状态不作为业务算法 |

术语：Session 是后端托管的远端会话；WebAuthSession 是一次独立登录；Binding 属于一条 WS、固定一个 generation；候选 WS 已升级但尚未提交 Binding；提交点是架构 §5.2 的唯一原子操作；Pane 是本页终端标签组件；TerminalClient 是该 Pane 的唯一连接/恢复协调器；SessionRecovery 是原 Pane 的详情及人工 attach 候选所有者。

本地查询序号、任务 ID、页面生命周期标识、terminalEpoch 均不是 binding_generation。列表/详情中的 generation 只是某次查询快照；自动任务只接受自己校验通过的 bind_success 更新代次。

**标记规则：**“冻结”表示原权威文档已规定；“实施细化”表示计划在既有契约内落实的代码职责；“用户已确认 Dn=A”仅指本轮用户明确选择的D1–D3，依据见§21。内部接口伪代码仍是供复审的实施设计，不冒充已实现代码或新增用户裁决。两个历史BLOCKER不重新开启。

## 3. 当前实现、第五阶段接入点与差异

以下路径相对仓库根目录，符号定位对应上述 HEAD；后续编码先重新检查 Git 和代码，不能照搬历史行号。

| 当前文件/符号 | 已核对事实 | 第五阶段增量 |
|---|---|---|
| `backend/internal/session/session.go`：Manager、Find/Get、viewLocked、Close/Sweep/reap、StopAt | 内存索引；Get 二次排除 CLOSED；关闭/离线/元数据/名额回收已存在；无 List | 增加 List，复用 View 与逐对象锁，不读 SQLite 活动行 |
| `backend/internal/session/binding.go`：credential、Prepare、canAttach、Attach | Prepare 仅接受 attach；credential 没有 mode；canAttach 对 CONNECTED 返回 BIND_BUSY；Attach 已有操作门、两次凭证检查、快照和消费点 | 内存凭证保存 mode，准入区分 attach/takeover，扩展同一提交事务并交出旧 Binding 收尾责任 |
| 同文件：Detach、Execute、Revoke | Detach 按 Binding 指针判断；Execute 在门内校验身份；Revoke 锁外扫描，结束回调在 Session 锁外 | 复用；证明旧输入/resize/Detach 不作用新端；禁止持 Session 锁调用 `end` |
| `backend/internal/websocket/connection.go`：reader、writer、terminate、supervise、run、finish | reader 在 Attach 返回后登记 Binding；每连接单 Writer；独立绝对截止 Supervisor；收尾后归还名额；finish 只有 error/closed 分支 | 处理提交后登记前竞争、旧端通知交接；实现 taken_over 最终消息；验证被替换端发送准入与回收 |
| `backend/internal/websocket/protocol.go`：closeCode、具名 DTO | 没有 taken_over DTO/4001 映射 | 新增既有协议消息的实现，不新增协议字段 |
| `backend/internal/websocket/service.go` | Upgrade 前名额预占；包括升级中/未绑定/关闭中；Revoke/Stop 覆盖所有连接 | 复用；接管需要额外候选名额，不能“借用”旧端 slot |
| `backend/internal/httpapi/sessions.go` | 已有创建、详情、DELETE、凭证 POST、WS；无 GET 列表；请求 mode 已有 | 新列表路由/响应；保留现有 Prepare 路径与请求结构 |
| `backend/internal/auth/auth.go`、`backend/cmd/server/run.go` | 登录读锁准入，Logout 写锁判断持久化结果后释放再扫描；OnRevoke 联动 server/session/websocket | 原样复用，扩大并发测试；不新增管理员/设备权限体系 |
| `frontend/src/api/sessions.ts`、`api/management.ts` | SessionView、运行时详情校验、支持 AbortSignal 的 request；closeSession 尚未接收 signal | 增加列表解析/查询；按关闭所有权需要传递可选 signal |
| `frontend/src/terminal/client.ts` | 单长期任务，connect 首次前缀 Promise，completion 长期 Promise；所有 Prepare 写死 attach | 增加明确的主动初始模式；仅用户任务第一次允许 takeover，以后重试固定 attach |
| `frontend/src/terminal/output.ts` | tracker、偏移、drain、epoch、稳定资格及 taken_over 解析 | 原样复用；不新建第二套输出恢复机制 |
| `frontend/src/terminal/recovery.ts`：SessionRecovery | 私有详情序号/AbortController、候选、两步人工 attach；running 时拒绝刷新 | 原流程保持；与主动动作互斥、失效旧候选，不把列表结果塞入人工候选 |
| `frontend/src/components/terminal/TerminalPane.vue` | 每 Pane 一个 client/tracker/recovery；onMounted 自动 connect；只 expose stop/dispose；已有网络展示与历史诊断分离修复 | 增加受控“创建后 attach/列表待确认接管”的启动入口、主动任务接口、外部关闭协同；不能挂载时误 attach |
| `frontend/src/pages/HomePage.vue` | tabs 数组、panes Map、v-show 保留；signOut 同步封闭准入并停止 Pane | 作为页面内唯一标签索引；新增会话管理视图及操作分派，不承载恢复循环 |
| `frontend/src/stores/auth.ts`、`router/index.ts`、`pages/LoginPage.vue` | logoutPending、单 logoutTask、identityEpoch、退出失败重试 | 沿用；新增页面任务进入相同意图边界 |
| 既有 Go/前端测试 | 包含“拒绝 takeover”的阶段范围断言、恢复/race/真实 TCP 及实际 Pane 测试 | 仅替换已过时范围断言，保留 attach 安全与第四阶段全部行为断言 |

差异分类：

1. 列表、takeover mode、Binding 替换、taken_over 服务端发送缺失，是阶段划分允许的实现待办，不是冻结设计冲突。
2. 核心文档开头/第四阶段计划的“credential_timeout 尚未实现”等措辞是当时状态；当前配置、/me 与前端读取已实现。本阶段不再次增加该字段，不重开 BLOCKER-HTTP。
3. 第四阶段报告 §13 的环境待验与用户本次部署完成确认属于不同时间的证据；可建议后续获准补充报告，本文不修改历史文件。
4. 原型把 CONNECTED 显示为“当前浏览器”、直接改数组状态并按当前服务器资料取目标，这些仅是演示。正式 UI 无设备身份字段，不推断 CONNECTED 属于哪台设备；显示 Session 创建快照，等待真实提交结果。
5. 新 Binding 已在 Session 发布而 `Connection.binding` 尚未登记，是现有两层交接窗口；第五阶段必须覆盖关闭/撤销/再次接管竞争，不能只修改 Prepare 判断就宣称完成。

## 4. 数据、API、配置和依赖

### 4.1 外部接口

所有响应仍为 `{code,message,data}`，表中给出 `data`；认证、Origin、no-store、UTC 毫秒时间、严格字段及安全整数遵守核心 §11。

| 接口 | 职责和数据 | 本阶段处理 |
|---|---|---|
| `GET /api/sessions?server_id=...` | `{items:[SessionView]}`；仅可选 server_id，按创建时间、ID 升序 | 新增实现；空结果 `items:[]`，不能 null |
| `GET /api/sessions/{id}` | 当前单个 SessionView；CLOSED/移除返回 SESSION_NOT_FOUND | 复用；接管确认前读取和失败后诊断 |
| `POST /api/sessions/{id}/takeover` | `{mode:"attach"或"takeover",expected_generation:G}` → credential、expires_at | 扩展 mode；HTTP 200 只表示签发 |
| `GET /ws/sessions/{id}` | 认证/Origin/容量后 Upgrade，首条 bind 带 credential 与 requested_start_offset | 不改 URL、bind 字段；模式取服务端凭证记录 |
| `DELETE /api/sessions/{id}` | `{}` → `{}`，进入幂等关闭，CLOSING 可查询，已不存在也成功 | 复用；明确确认；响应丢失可查询/重试 |
| 创建与认证 API | 创建新 SSH、/me 配置、logout 原有契约 | 不拿创建接口充当打开已有 Session |

SessionView 完整复用：`id, server_id, name, host, port, username, status, created_at, disconnected_at, last_attached_at, offline_expires_at, remaining_seconds, binding_generation, current_cols, current_rows`。只含 CONNECTING/CONNECTED/DISCONNECTED/CLOSING；剩余秒数向上取整不小于零，仅离线非 null；目标来自创建快照。不增加 device_id、is_mine、owner、凭证或关闭历史字段。

server_id 缺省取全部，存在则按核心 ID 规则验证；空值、重复参数、未知参数拒绝 INVALID_REQUEST，复用现有服务器列表的严格 query 风格。合法但没有匹配 Session 的 ID 返回空集合，不需要查询服务器表。没有 `status`、`page`、`limit` 或 `sort` 参数。前端状态筛选只对本次列表数据做本地过滤。

### 4.2 内部结构与持久化

- credential 追加受校验的内存 mode，签发时固定，不能让 WS 或客户端再覆盖。全局哈希索引、TTL/容量/归属/失效规则不变。
- 原 Binding 仍固定所属 WS、登录、不可变 generation；替换产生新 Binding，不把旧对象转交新 WS。
- 提交结果需要保留“新 Binding、待终止旧 Binding”的明确所有权，可用内部具名结果；不成为公开 DTO。任何成功提交后的退出路径都必须处理被替换对象。
- 前端增加列表查询状态与页面内 Session 操作记录；它们是短期本地状态，不写 localStorage，不当服务端事实。
- 不改 SQLite 表、不加迁移/持久化数据、不新增配置或 client_settings、不新增依赖。现有超时、容量和内存对象足够；继续使用 Vue、本地对象、Map 和 AbortController，不安装 Pinia 或其他库。

## 5. 活动列表后端

### 5.1 权威、快照与排序

Manager.List 在管理器索引锁内复制稳定 Session 引用，马上释放索引锁；逐对象取得 Session 锁，检查 CLOSED、应用 server_id 过滤并调用 viewLocked 拷贝 DTO，随后释放。最后在无锁情况下排序和编码。不得嵌套 Manager 索引锁与 Session 锁，不遍历 SQLite，不锁住全体 Session，不等待 WS/SSH。

每行是在该 Session 状态锁内形成的一致快照，不承诺整个集合在同一瞬间的全局事务快照。索引复制之后创建的 Session 可到下次查询才出现；取行快照前已 CLOSED 的对象排除；取完行后关闭或接管的对象可暂时以旧状态/代次返回。返回的对象禁止含 CLOSED，但不能保证响应送到浏览器时每行仍活动。下一次查询、详情和提交重新核实。

排序键为创建时间的 API 毫秒精度与 ID，均升序；实现保存对应数值排序键，不能对本地化时间文字排序。同毫秒按 ID 固定顺序；不依赖 Go map 遍历顺序。列表不隐去 CONNECTING/CLOSING，也不因 remaining_seconds=0 自行删除仍 DISCONNECTED 的对象；签发/提交负责拒绝到期者。

### 5.2 路由及敏感数据

在 sessions.go 增加受保护 GET 路由、具名列表 DTO、query 校验，返回既有安全 session.View。查到记录不代表本登录拥有当前 Binding；单管理员可管理全部 Session，但绑定凭证必须属于当前独立登录。

查询取消不持有对象/网络资源；可用请求 context 在逐行处理边界停止无用工作，不改变快照契约。业务短锁不宣称可被 context 强行抢断。HTTP 响应只含 DTO，禁止序列化 Manager/Session/Binding、连接、密码、完整 Host Key、Cookie、令牌/哈希、凭证/哈希、终端内容。并发元数据写失败不影响内存列表事实。

## 6. 前端列表、刷新和请求所有权

以下刷新策略采用 **用户已确认 D1=A**；取消、迟到响应隔离和快照非权威的约束必须满足。

### 6.1 页面与刷新触发

在 HomePage 内提供独立“会话管理”视图，切换视图不卸载既有终端 Pane；保留 `/` 与 `/login` 路由，不为了列表重建整个 HomePage。列表组件与终端标签集合分别维护。

| 触发 | 已确认行为（D1=A） |
|---|---|
| 首次进入/再次打开会话管理 | GET 一次，带当前 server_id；首次显示 loading，成功为空显示明确空态 |
| 手动刷新、切换服务器筛选 | 取消旧 GET、序号递增、发新 GET；状态筛选只在本地计算 |
| takeover 的成功前缀、最终停止/取消或关闭操作得到结果 | 使列表标为待刷新；页面可见时合并同一轮触发为一次 GET，隐藏时下次进入加载 |
| 页面从 hidden 到 visible | 仅会话管理当前可见且登录有效时刷新一次；终端可见性不触发 takeover/重置重试 |
| 连续刷新 | 最新请求胜出；同一个同步事件批次可用一个 microtask 合并；不追加请求队列/周期轮询 |
| 离开列表视图/卸载/登出/认证失效 | 同步撤销列表发布身份并 Abort；移除 visibility listener；不把取消当网络错误 |

显示“最近查询”的状态、代次和时间。按D1=A保留旧行作为明确标记的历史快照，loading/error 单独显示；失败时不伪造空集合。只读列表可继续展示旧行，但所有接管都需新详情及确认；关闭由独立确认和幂等服务端裁决。状态筛选提供全部及四种活动态，不把 CONNECTING/CLOSING 合并成离线。剩余时间首版显示服务端返回值及查询时刻，不加全局倒计时或到零自动删除；后续若显示倒计时，也不能赋予准入权。

### 6.2 请求状态最小结构

建议 `SessionListController` 持有 items/loading/error/lastLoadedAt、sequence、controller、disposed，以及由宿主提供的 allowed()。每次刷新：先 ++sequence、abort 旧请求，再设置本次 controller/loading；fetch 和 JSON/DTO 校验完成后检查 sequence、signal、页面身份、当前登录及 logoutPending。catch/finally 也必须检查同一身份，旧 finally 不清新 loading。

列表解析验证 data.items 是数组，逐项复用 SessionView 校验，并检查每项 id 格式、ID 不重复；拒绝非法整体响应而非偷偷丢行。列表查询不写 TerminalClient.generation，不创建 SessionRecovery.candidate，不通过列表状态停止/启动终端。

列表 GET 按D1=A与详情 GET 一样只支持主动/生命周期取消，不引入新的本地定时期限（D1）。凭证 POST 独立使用 credential_timeout；bind_timeout 不用于列表或详情。

### 6.3 时序图 1：列表加载、刷新、关闭竞争

```mermaid
sequenceDiagram
    actor U as 用户
    participant V as 会话管理视图
    participant Q as 列表请求所有者
    participant H as HTTP API
    participant M as Manager
    participant S as 各 Session
    U->>V: 打开或手动刷新
    V->>Q: refresh(server_id)
    Q->>Q: 序号加一，Abort旧请求
    Q->>H: GET /api/sessions
    H->>M: 认证通过后 List
    M->>M: 索引锁内复制引用，释放索引锁
    loop 每个引用
        M->>S: Session锁内过滤CLOSED并复制View
        S-->>M: 单行一致快照，释放Session锁
    end
    M->>M: 无锁按创建时间和ID升序
    M-->>H: items
    H-->>Q: no-store响应
    alt 仍是当前序号和页面身份
        Q->>V: 发布最近查询快照
    else 旧响应或登出/卸载
        Q->>Q: 丢弃响应及旧finally
    end
    Note over M,S: 查询后可关闭或接管，不保证响应到达时仍是当前状态
    U->>V: 再次刷新
    V->>Q: 替换请求，不启动任何绑定
```

## 7. Takeover 后端流程与原子提交

### 7.1 冻结规则逐条落实

1. HTTP Prepare 只签发凭证，不撤销旧 Binding。
2. 候选 WebSocket 尚未完成 bind 和服务端验证时，旧 Binding 仍然有效。
3. 只有新连接成功完成全部校验并提交 Binding 时，才允许原子替换旧 Binding。
4. 提交前失败，旧 Binding 保持有效。
5. 提交后失败，不允许回滚或恢复旧 Binding。
6. 同一个 Session 不允许存在两个同时有效的逻辑 Binding。
7. 旧 WebSocket 可能在新 Binding 生效后仍处于物理清理过程中，但不能继续获得新的业务操作权限。
8. 旧连接收到 `taken_over` 后必须停止自动恢复。
9. 新连接的成功状态不能依赖旧连接是否及时收到通知。
10. 旧连接资源未完成清理时，不得提前归还其 WebSocket 资源名额。

“提交前保持有效”指 takeover 本身不提前撤销旧端；不阻止独立的网络故障、登出或 Session 关闭合法撤销它。

### 7.2 Prepare 与 WS 职责

HTTP protected 负责登录/Origin、mode 两值与 expected_generation 必填安全整数。Prepare 查找 Session 后依次取得登录读锁、Session 锁、必要时凭证索引锁。共同准入先检查应用、状态、期限和代次；attach 同代次占用返回 BIND_BUSY，takeover 允许 CONNECTED/DISCONNECTED。不能为了支持 takeover 放宽 attach。

凭证记录绑定本次登录、Session、mode、expected、原到期时间；清过期/容量/插入同一索引临界区。Prepare 不拿操作门、不取消 Binding、不延长离线期限、不预留最终胜利。输出只返回凭证与到期时间，客户端取消 POST 不代表服务端没签发。

WS Service 负责 Cookie/Origin 后升级名额，未绑定/关闭中同样计数。Reader 严格读取首条 bind，mode 从哈希凭证中读；Session 提交入口负责操作门、再次准入和快照；Writer 负责成功前缀与输出；Supervisor 负责绝对期限强关；连接收尾者最后归还 slot。

### 7.3 原子撤权、Connection 终止与取消的分离

冻结架构 §3.1 的事务锁序保持：**远端操作串行门 → 登录读锁 → Session.mu → OutputBuffer.mu → 凭证索引锁**，只取需要的子序列。凭证初查可在 Session 锁下短取索引锁并释放，再取 Buffer；不持凭证索引锁反向获取 Buffer。Prepare 使用登录读锁 → Session 锁 → 凭证锁；普通 Detach/Close 不等待操作门；Manager 索引锁与 Session 锁不嵌套。

第二轮修订删除原提交伪代码中的 `old.cancel()`。必须区分以下五件事：

| 时刻 | 同步边界与事实 |
|---|---|
| ① 原子逻辑撤权 | Session 提交临界区将 old.active=false、记录仅用于内部识别的 `superseded` 标记，并发布新 Binding/G+1；不取消 old context、不调用 old.end；旧 SSH 操作与普通发送准入立即失效 |
| ② 旧 Connection 确定终止原因 | 释放操作门及全部事务锁后，经旧 Connection 的统一 Terminate 入口，在旧 Connection.mu 内检查首因；尚无原因时固定 taken_over 与关闭绝对截止 |
| ③ 取消旧 Binding context | 仍持该 Connection.mu，在已固定原因之后调用 Session.Detach(old)：按对象身份撤权并取消旧 context；取消可能唤醒 Reader/执行器，但它们不能越过该 Connection.mu 覆盖原因 |
| ④ 终止槽发布与 Writer 唤醒 | 在同一 Connection 临界区固定独立终止槽的原因/截止，取消在途 Ping 和 Connection 工作 context、发布 terminated；解锁后唤醒唯一 Writer。终止消息不走普通控制队列，不在此处写网络 |
| ⑤ 物理回收 | Writer 在锁外按预算结束已准入项、至多发送一次最终 taken_over 和 Close 4001；Supervisor 到期强关，收尾者等待全部任务后归还 slot |

②是 Connection 首终止原因的确定点；④是完整终止请求对收尾任务的发布点。两者之间一直持 Connection.mu；先记录原因再 Detach，不表示在撤权之前发网络通知。takeover 的撤权已经在①完成，其他 Terminate 路径也必须在④发布前完成 Detach。`superseded` 不是第二个 Connection 终止槽，不能覆写已经确定的真实故障首因。

#### 7.3.1 提交和锁外交接伪代码

```text
Prepare(login, sessionID, mode, expected):
  校验mode/expected；从Manager取得引用并释放索引锁
  登录读锁 -> Session.mu；检查应用/登录/状态/期限/代次
  attach且已有绑定 => BIND_BUSY；takeover允许CONNECTED/DISCONNECTED
  凭证索引锁内清过期/容量检查/存哈希与mode；释放所有锁
  返回credential/expires_at；原Binding/context/期限均不变

CommitCandidate(ctx, login, token, P):
  校验token/P；有期限取得操作门
  登录读锁 -> Session.mu
  短取凭证索引锁初查并释放；检查归属/目标/mode/TTL/代次/状态
  OutputBuffer.mu内校验P并复制唯一[S,E)快照
  凭证索引锁内再次核对记录/TTL/登录到期/离线期限/候选ctx
  校验失败：释放快照、所有锁和门；不消费仍有效凭证、不改旧Binding
  代次耗尽：退出同步边界后幂等Close(counter_exhausted)，拒绝绑定
  唯一提交点（Session.mu仍持有）：
    消费本凭证，失效该Session其他凭证
    old = s.binding
    若old非空：old.active=false；old.superseded=true
    禁止old.cancel()、old.end()、获取任何Connection.mu
    generation++；创建并发布新Binding；置CONNECTED；更新接入时间/清期限
    非阻塞标记metadata dirty
  释放凭证锁、Buffer锁、Session锁、登录锁、操作门
  返回CommitResult{newBinding, displacedBinding: old}

提交结果的唯一收尾所有者（WS Reader；所有成功提交后的分支都必须执行）：
  安装必执行的锁外收尾：old非空则old.end("taken_over")
  在新Connection.mu内检查是否已有终止原因
  可用则登记newBinding、设置心跳，解锁后唤醒新Writer
  已终止则解锁后Detach(newBinding)，不发布可用连接、不回滚old
  执行old的锁外收尾，不等待通知送达、Close或旧slot释放
```

提交结果不能由候选 ctx 取消而遗失，也不能因新 Writer 启动/登记失败提前返回而跳过旧端收尾。新 Binding 尚未登记到 Connection 时就再次被接管，仍由其登记流程与§7.4准入校验拒绝失效对象；不能只凭 `c.reason==""` 认定该 Binding 可发送。

#### 7.3.2 统一终止入口与五种竞争

```text
Connection.Terminate(reason, optionalApplicationDeadline):
  Lock(c.mu)
  若c.reason已存在：仅按既有规则收紧截止（如有），Unlock；返回
  c.reason = reason                       // 首终止原因确定点
  固定min(now+close_timeout, 应用剩余期限)，保存独立终止槽数据
  若c.binding非空：
    Session.Detach(c.binding)             // c.mu -> Session.mu
      old.active=false；old.cancel()
      只有s.binding==old且s.status==CONNECTED时才清当前Binding/设离线期限
      不改old.superseded，不调用Connection回调
  取消已登记Ping context与Connection工作context
  发布terminated/截止变更信号（非阻塞、一次性），不写网络
  Unlock(c.mu)
  唤醒唯一Writer；未完成Upgrade的底层关闭沿用现有锁外路径
```

当前代码在同一个 `c.mu` 内先 Detach 再写 reason，本就能阻挡同时进入该锁的 defer；真正缺口是**提交路径在锁外通知之前自行取消 old context**。计划将顺序进一步显式化为“先固定原因、再 Detach 取消、最后发布终止”，避免将来把取消挪出受保护范围。不能通过增设 sleep、假定 Reader 调度较慢或强制覆盖 NETWORK_ERROR 修复。

| 情况 | 精确结果 |
|---|---|
| 正常提交 | 提交仅置无效/superseded，old.Done尚未由takeover关闭；锁外old.end进入Terminate，在c.mu内固定taken_over后Detach取消。新Binding早已有效，旧端收尾不影响它 |
| Reader等待Binding.Done | 当前reader只在输入槽等待分支select b.Done，普通readMessage阻塞不是等待b.Done；前者被Detach取消唤醒后退出，defer需取得c.mu，看到taken_over后无操作；后者由Writer关闭/底层强关解除，迟到defer同样无操作 |
| Reader defer与old.end同时竞争 | 正常由这次takeover取消引起的defer不能先于原因确定：取消在该原因确定后的同一c.mu内。若由另一合法路径取消而先唤醒，且该旧Binding已被标记superseded，按下述失效退出分流先提交taken_over再return；不可在测试里伪造无归属的提前cancel后仍声称遵守此顺序 |
| 真实网络故障已经先确定原因 | Terminate(NETWORK_ERROR)先持c.mu确定首因并Detach；随后old.end不得覆盖。若原端已离线，takeover按最新状态/原代次校验可提交；若已取到旧引用才发生故障，迟到收尾也只作用旧对象 |
| 旧Binding替换后迟到Detach | 只将旧对象active=false并取消其context；s.binding!=old，因此不清新Binding、不改generation/CONNECTED/新离线期限。重复Detach幂等 |

**补齐失效退出分流，避免另一条内部错误抢先：** 提交到old.end之间，旧执行器仍可能取得操作门，然后因Binding无效而返回；旧Writer也可能先发现无效；Revoke/Sweep预先捕获的旧引用还可能晚到Detach。只移除提交内cancel不足以覆盖这些情况。

- Session在自身锁内以对象身份/不可变代次验证；若拒绝原因是已标记superseded，向WS返回明确的**内部失效处置**（如`BindingSuperseded`枚举），与普通BIND_CONFLICT/输入背压错误分开。此值绝不作为新的API错误或WS error.code发出，只转换为已有 `Terminate("taken_over")`。
- Reader在`b.Done()`分支、执行器在取消或准入拒绝分支，先于return/defer调用一个短的 `finishBindingExit(b, fallback)`；它在c.mu保护首因，再按c.mu→Session.mu读取该旧对象的superseded。已确定首因即返回；superseded则固定taken_over，否则按既有合法退出原因处理。内部复用不重复取c.mu的`terminateLocked`，其取消/发布步骤与上面完全一致；取完Session锁后释放，再调用该内部过程中的Detach，禁止重入Session锁。
- Writer发现superseded时释放发送准入使用的Session锁和Connection锁，再经统一Terminate入口提交taken_over；重新竞争c.mu时如真实故障已确定首因，保留故障。
- 真正的readMessage错误、TCP失联、Ping失败和已准入Write失败仍走原实际故障入口，不因superseded标记一概改写为接管。正常结束分支先提交具体处置，随后`defer c.Terminate("NETWORK_ERROR")`只是幂等兜底，不承担从Binding取消推断网络故障的职责。

失效退出适配的最小伪代码（仅内部取消/准入拒绝调用，真实网络错误不调用它）：

```text
finishBindingExit(b, fallback):
  Lock(c.mu)
  若c.reason已确定：Unlock(c.mu)；return
  Lock(s.mu)
  superseded = (b属于该Session且b.superseded)  // 只读传入旧对象，不替换为s.binding
  Unlock(s.mu)
  reason = superseded ? "taken_over" : fallback
  terminateLocked(reason)                   // 已持c.mu，不再获取c.mu
    先固定reason/截止，再Detach(c.binding)，取消并发布终止槽
  Unlock(c.mu)；唤醒Writer

Reader输入槽等待：
  select:
    operations入队：继续读取
    b.Done：finishBindingExit(b, 原取消退出原因)；return
    c.ctx.Done：return                       // 该context只在已确定终止后取消
    输入等待超时：按既有INPUT_BACKPRESSURE终止
  defer Terminate(NETWORK_ERROR)             // 所有明确终止之后的幂等兜底

旧Execute准入拒绝/等待取消：
  完成自身门和登录/Session锁的释放
  若是内部BindingSuperseded或Binding取消：finishBindingExit(原b, 原错误码)
  若是实际SSH故障：按原Session.Close/错误路径处理，不假冒接管
```

`terminateLocked`中调用Detach前已释放上一次读取处置的s.mu，仍保留c.mu，因此既不重入Session锁，也不会让另一终止请求插入并覆盖原因。实际连接的归属通过原Binding所属Session/不可变所属WS关系保证，不允许把其他连接的Binding传给适配器。内部处置仅补齐取消/旧权限拒绝的分类，不能把业务错误表中的所有BIND_CONFLICT或INPUT_BACKPRESSURE全局改成taken_over。

最小内部接口建议：`Binding.end func(string)`仍足以把已有taken_over原因送到所属Connection，无需新增对外接口或改变回调签名；需要调整其调用时点和统一终止实现。Session需提供受锁保护的内部失效处置读取/准入返回值，WS需一个共用的`terminateLocked`及失效退出适配，避免直接访问另一包私有字段。旧Connection尚未登记Binding时，end可先固定reason但无对象可Detach；原提交Reader在返回后的登记拒绝分支必须Detach其持有的新Binding，再完成工作任务。这一交接责任由CommitResult明确，不能把end当成已完成所有资源回收。所有名称是内部伪代码建议，未修改业务源码。

### 7.4 Writer 的精确发送准入与锁顺序

#### 7.4.1 发送准入点

**选择并标记下一条普通应用消息，是发送准入点。** 必须同时持有该连接的 `Connection.mu` 与所属 `Session.mu`，完成“终止状态检查→当前Binding身份/active/代次/CONNECTED验证→选定单个不可变发送项→标记该项已准入”。不能返回一个bool、释放Session锁后再选消息。

范围包括成功前缀、历史块、实时块、普通status等控制消息；控制队列入队不是发送准入，不得因为status早已排队就绕过替换后的校验。最终taken_over/closed/error来自独立终止槽，旧Binding已经无效也允许唯一Writer按预算发送，不走普通准入检查。

```text
Writer下一步：
  Lock(c.mu)
  若已有终止原因：取一次最终终止项/截止，Unlock；走锁外finish；return
  b = c.binding
  若b为空：Unlock；可取消地等待wake/terminated；重新开始
  Lock(s.mu)                             // 顺序c.mu -> s.mu
  检查s.binding==b、b.active、s.generation==b.Generation、s.status==CONNECTED
  若无效：
    复制b的内部失效处置；Unlock(s.mu)；Unlock(c.mu)
    若superseded：调用统一Terminate(taken_over)，重新开始
    否则等待已有关闭/撤销流程发布终止，不自行发送closed或继续普通输出
    等待必须可由wake/terminated/连接ctx解除，不持锁、不自旋
  若到期Ping优先：
    在当前短同步边界登记唯一Ping及cancel；Unlock(s.mu)；Unlock(c.mu)
    锁外Ping；失败进入统一终止；不同时准入普通消息；重新开始
  从固定前缀、普通控制FIFO或输出中选择至多一项：
    前缀/控制：非阻塞选取并复制具名消息值
    回放：从该Binding不可变快照取得一个有界块及下一游标值
    实时：保持c.mu与s.mu，Buffer.Read内部获取OutputBuffer.mu
          在同一Buffer临界区检查连续性并复制一个有界块，然后释放Buffer锁
  若取块失败：释放s.mu/c.mu后经统一终止处理；不准入普通消息
  若无消息：释放s.mu/c.mu后按现有缓存唤醒/心跳/终止机制等待
  item = 单个不可变发送描述（含所属Binding及代次、kind、payload、下一游标）
  标记item为唯一已准入项                  // 线性化点，仍同时持c.mu和s.mu
  Unlock(s.mu)；Unlock(c.mu)
  锁外JSON编码（如需要）及唯一一次网络Write，使用既有写入/关闭期限
  结束后清该项/更新本Writer游标；失败经统一终止，成功返回循环重新取两把锁
```

实际可由Session提供狭窄的 `WithSendAdmission(b, selectOne)` 内部方法：调用方已持c.mu，方法持s.mu校验并执行一次有界内存选择回调后才释放。回调只能选/复制/标记一项，可按顺序调用Buffer.Read；不拿c.mu、不取操作门/登录锁/Manager锁、不执行Terminate/Detach/网络/阻塞队列等待。API不得把Session锁泄漏给任意调用者，不能设计成`IsActive()`返回后由调用方无锁选择。`inFlight`可由唯一Writer的局部项表达，是否显式存字段由实现决定，但准入与完成必须有可测试的同步点。

新端Writer发布与旧端通知之间不保证调度先后，因此普通消息检查必须覆盖新Binding也已被再次替换的情况。没有有效Binding而在等待SSH完成关闭时，不能提前发送closed；只有原Session回收流程确认SSH关闭后才提交该终止消息。已准入项可以在takeover提交后才真正调用Write，它仍是**提交前准入**的一项，不得据实际发送时间误判为提交后新准入；单Writer执行完它之前不能预取第二项。

#### 7.4.2 两种严格先后

- Writer先完成准入：持s.mu选定并标记item，释放后takeover才可取得同一s.mu并提交。允许这唯一在途项按既有write_timeout/关闭总期限完成；后续循环重新锁内校验旧Binding失败，普通消息计数不再增加。
- takeover先提交：Writer即使先拿到c.mu，只要尚未拿s.mu并完成准入，随后就看到旧Binding失效；不得出队/选择/标记任何新的普通消息，只转终止处置。两者同时竞争s.mu的结果只能是这两种，不存在检查与选择之间的可插入提交窗口。

#### 7.4.3 锁顺序与无环论证

| 路径 | 实际嵌套顺序 | 明确禁止 |
|---|---|---|
| takeover提交 | 操作门 → 登录读锁 → Session.mu → OutputBuffer.mu → 凭证索引锁 | 持任何事务锁/门调用old.end、获取Connection.mu或写网络 |
| Writer普通发送准入 | Connection.mu → Session.mu → 必要时OutputBuffer.mu | Session有效检查后提前释放锁再选项；持锁Write/Ping/Close/等待任务 |
| Terminate/失效退出适配 | Connection.mu → Session.mu（Detach或短读处置；分别取锁，不重入） | 持Session锁再获取Connection.mu；调用公开Terminate导致Connection锁重入 |
| SSH输出追加 | 仅OutputBuffer.mu；释放后非阻塞通知 | 持Buffer锁取Session/Connection锁 |
| 普通Detach/Session Close | 仅Session及其规定的子锁；回调在释放后执行 | Session.mu → Connection.mu |
| 连接登记和旧通知 | 新Connection短锁释放后，再调用旧end获取旧Connection.mu | 同时持两条Connection.mu；新连接锁跨旧端通知 |

Connection.mu是现有WS调度锁，本次把已有 `Connection.mu→Detach/Session.mu` 的方向用于发送准入；不是在架构事务链中插入新的反向锁。Writer所用Session→Buffer是架构§3.1允许的子序列，且不向前取操作门/登录锁。提交方从不等待Connection.mu，Terminate/Writer都只按Connection→Session方向等待；Buffer持有者不反向取锁，因此这些路径没有锁等待环。Supervisor仍不获取这些锁，到期直接关闭底层net.Conn。短锁竞争可能延迟调度，但不等待Reader/Writer网络或Close完成，不把新端成功绑定到旧端通知送达上。

输入在途与提交继续共用操作门；Writer普通输出准入不额外取得该门。旧已准入SSH调用先完成再允许接管，尚未准入项通过active/身份检查拒绝；context取消只是停止等待/清理手段，不再承担提交点撤权的唯一责任。

## 8. Takeover 前端完整流程

### 8.1 所有者和启动模型

用户已确认 D2=A、D3=A：HomePage 保存 `Session ID → 唯一 Pane/正在准备的记录`，在首次 await 前预占目标操作；Pane 拥有唯一 client/tracker/recovery，另持主动动作的查询/确认状态，所有动作共用一个本地互斥意图，不增加恢复循环。候选详情与 SessionRecovery 的人工 attach 候选分别存储并互相失效。

> **S6 修订（2026-11-18，方案 B）**：HomePage 以单一 `records` 同步预占 Session ID；Pane 初始化成功并 emit ready 后，候选接管动作的所有权移交给 Pane，HomePage 和 SessionManager 不再持有或取消它。上段“所有动作共用一个本地互斥意图”在 S6 中的实现方式为 Pane 级：Recovery 的注入准入包含“takeover 空闲”，takeover 自身用 phase 加 sequence 令牌，不新增页面级或全局互斥对象，不修改 `recovery.ts`。主动动作对象为 `frontend/src/terminal/takeover.ts`。

新建 Session 的 Pane 仍按创建 DTO 首次 attach。列表打开已有 Session 的 Pane 必须是“未自动连接”的候选模式，mount 本身不调用 connect；用户确认时只启动一次 takeover。可以先建立不可操作的待接管标签以容纳唯一 Client，成功前缀后转为正常标签；不能成功后再创建另一个 Client 把候选 WS 搬过去。失败保留该 Pane 和原因供查询/关闭，不自动创建另一个标签（D3）。

建议在 client 保持现有 `connect(expected)`/`run(expected)` 默认 attach 外观，增加明确的用户主动入口或受类型限制的初始模式参数；具体命名不是外部契约。初始 mode 固定在该 RecoveryTask，第一次 Prepare 可为 takeover；下一次循环无论第一次是否拿到凭证，都必须改为 attach。用独立的“首次尝试已消费”标记，不能根据 retries==0 判断模式，因为稳定资格可能把 retries 重置为0。重复点击不重新 start，不延长预算。

### 8.2 从列表到终端的步骤

1. 用户点某行“打开/主动接管”；HomePage 按 Session ID 查当前页记录（S6 方案 B 中为单一 `records`，含尚未 ready 的预占记录；原“tabs、panes 及待创建记录”三处状态已被替代），预占操作，绝不按服务器名称去重。
2. 已有在线 Pane 按 D2 优先定位，结束本次列表动作，不发 takeover；已有恢复/停止 Pane 走 §11 的显式流程。无 Pane 按 D3 建立唯一待接管 Pane，默认不联网。
3. 主动接管准备阶段，封闭原人工候选；若存在旧任务，显式停止并等待 client.stop、发送与 drain 收尾后才继续。按D2=A，恢复中任务在用户点击“停止恢复并准备接管”时先停止，之后取消确认也不自动复活；在线端只定位，不悄悄暂停。
4. 用可取消详情 GET 获取完整 SessionView。检查任务、Pane、页面和登录身份；展示 Session 快照目标、状态、代次、离线期限。CONNECTING/CLOSING/NOT_FOUND 禁止接管；CONNECTED/DISCONNECTED 才可确认。GET 无本地自动超时。
5. 用户单独确认“主动接管；当前操作端将停止”。确认框不是列表刷新自动生成的授权。确认前不申请凭证；刷新详情或目标变化使旧确认候选失效，不自动确认新代次。
6. 确认处理器首次 await 前消费候选、锁住动作；复查旧任务收尾与登录；以详情代次启动该 Pane 唯一任务，第一次 POST mode=takeover。
7. 凭证 POST 使用独立 credential_timeout；迟到/取消响应不创建 WS。通过后新建候选 WS，设置 arraybuffer/handlers，onopen 前再次检查身份，发送首条 bind。
8. 服务端按 §7 依次验证、取操作门、快照与原子提交；HTTP 成功、WS OPEN 或 bind 已发送都不等于已接管。
9. 客户端校验 bind_success 代次与前缀；connection Promise 完整前缀才反馈成功，长期 completion 仍运行。成功后聚焦同一标签、同步有效尺寸；回放未处理完显示“正在回放”，不提前启用输入。
10. 标记列表需刷新（S6 实现为 Pane emit `changed`，HomePage 仅在管理视图时调用列表刷新）；列表数据不能反向驱动终端。旧浏览器收到 taken_over/4001 立即停输入和自动恢复，有限排空，保留明确状态。
11. 初次或后续可重试失败按原预算只 attach、只使用已成功获知代次；BIND_CONFLICT 停止。结果不确定不自动采用详情新代次再 takeover，要求新一轮用户详情/确认。

同实例复用先 drain 再读取 processedOffset；新设备、新页面、新 xterm 或排空失败重建从 0。无论已有 Pane 与否，都不得使用另一设备、列表或服务端发送游标作偏移。

### 8.3 时序图 2：没有已有 Pane 的主动接管（D3=A 已确认）

```mermaid
sequenceDiagram
    actor U as 用户
    participant H as HomePage
    participant P as 唯一候选Pane
    participant C as TerminalClient
    participant API as HTTP API
    participant S as Session提交入口
    participant W as 新WS Writer
    participant O as 旧Binding和WS
    U->>H: 选择Session主动打开
    H->>H: 按ID查重并同步预占
    H->>P: 创建待确认Pane，不自动attach
    P->>API: GET详情（可取消）
    API-->>P: 当前SessionView快照
    P-->>U: 展示状态和接管影响
    alt 用户确认且身份仍有效
        U->>P: 明确确认主动接管
        P->>C: stop完成后启动唯一初始takeover任务
        C->>API: POST mode=takeover, expected=G
        Note over API,O: 登录读锁→Session锁→凭证锁；仅签发，旧端不变
        API-->>C: credential/expires_at
        C->>S: Upgrade后首条bind(P)
        Note over S: 详见图4：门→登录读锁→Session→Buffer→凭证锁
        S->>S: 唯一提交，旧失效但不cancel，新G+1
        S->>W: 锁外登记新Binding并唤醒
        S->>O: 锁外统一终止：先定taken_over，再Detach取消并发布终止槽
        W-->>C: bind_success与必要截断前缀
        C-->>P: 首次连接成功；同一Pane继续回放
        P->>H: 聚焦并标记列表需刷新
    else 取消、详情失败或状态不可绑定
        P->>P: 失效候选；无凭证/WS，显示结果
    end
```

> **S6 修订（2026-11-18，方案 B）**：上图中“先 GET 详情、确认后再 stop”的顺序和“HomePage 创建待确认 Pane 后由 P 直接 GET”的描述，已被以下现行顺序替代（原图保留供追溯）：Pane 初始化成功并 emit ready 后，由 Pane 内的 SessionTakeover 执行 `prepare()`：准入检查 → `client.stop()` 屏障（候选 Pane 无任务，只做本地 settle）→ GET 详情 → 再次准入检查 → 确认框 → 用户确认 → 紧贴调用前的最终准入检查 → `connectTakeover(G)`。“聚焦并标记列表需刷新”实现为 Pane emit `changed`。取消后候选 Pane 保留（U4），不自动关闭。

### 8.4 时序图 3：当前页已有 Pane（D2=A 已确认）

```mermaid
sequenceDiagram
    actor U as 用户
    participant H as HomePage标签索引
    participant P as 原Pane
    participant R as 原恢复任务
    participant API as HTTP API
    participant C as 同一个TerminalClient
    U->>H: 从列表选择相同Session
    H->>P: 优先定位已有标签
    alt 原Pane正常连接
        P-->>U: 保持现有终端，不申请takeover
    else 原Pane恢复中或已停止，用户明确要求接管
        U->>P: 停止原恢复并准备接管
        P->>P: 意图加锁，失效旧查询/人工候选
        P->>R: 同步stop，取消POST/重试/handlers
        R-->>P: 长期任务/发送/drain收尾完成
        P->>API: 新详情GET
        API-->>P: SessionView(G)
        P-->>U: 展示快照和主动接管确认
        alt 用户确认且仍是当前动作
            U->>P: 确认
            P->>C: 唯一新任务，首尝试takeover(G)
            Note over C,API: Prepare/候选bind/提交同图2、图4，不建第二Client
            C-->>P: 成功前缀或失败/有限attach状态
        else 取消或失败
            P->>P: 原任务保持停止，不自动复活
        end
    end
    Note over P,R: 旧finally/回调不能覆盖新任务；tracker原实例有效write可完成drain
```

## 9. Binding 提交、终止与资源生命周期

### 9.1 时序图 4：提交、旧 Writer 准入与原因确定

```mermaid
sequenceDiagram
    participant N as 候选WS提交者
    participant G as 操作门和登录读锁
    participant S as Session.mu
    participant B as Buffer及凭证索引
    participant W as 旧Writer
    participant O as 旧Connection.mu与终止槽
    participant R as 旧Reader
    participant U as 旧浏览器
    participant V as 新Writer及浏览器
    N->>G: 有期限取得门，再取得登录读锁
    alt 旧Writer先准入
        W->>O: 获取旧Connection.mu
        W->>S: 获取Session.mu并验证旧Binding
        W->>B: 必要时仅取Buffer锁复制一块后释放
        W->>W: 持Connection与Session锁选择并标记唯一item
        W->>S: 释放Session.mu
        W->>O: 释放Connection.mu
        Note over W: 锁外执行已准入Write，可以跨越随后提交
        N->>S: 随后取得Session.mu
    else 提交者先持Session锁
        N->>S: 取得Session.mu
        Note over W,S: Writer即便持Connection.mu也须等待Session锁，尚未准入
    end
    N->>B: 短查凭证并释放，再取Buffer快照及凭证锁复查
    Note over N,B: 验证登录/模式/状态/代次/TTL/离线期限/offset/ctx
    N->>S: 唯一提交：old无效且superseded，新Binding和G+1
    Note over S,R: 不取消old context，不调用Connection回调
    N->>B: 释放凭证与Buffer锁
    N->>S: 释放Session.mu
    N->>G: 释放登录读锁和操作门
    N->>V: 新Connection短锁登记，锁外唤醒新Writer
    W->>O: 下次普通发送取得Connection.mu
    W->>S: Session锁内发现old已失效，不选任何普通项
    W->>S: 释放Session.mu
    W->>O: 释放Connection.mu
    Note over W,O: Writer可先提交taken_over；与old.end共用同一首因入口
    N->>O: 锁外old.end(taken_over)
    alt 尚无独立故障首因
        O->>O: 持Connection.mu先固定taken_over和截止
        O->>S: Detach(old)，仅取消旧context，不影响新Binding
        S-->>R: old.Done唤醒（若正在等待该分支）
        O->>O: 发布独立终止槽，取消Ping，解锁唤醒Writer
        R->>O: 迟到defer NETWORK_ERROR，看到已有原因而返回
        W-->>U: taken_over和Close 4001
    else 真实故障已先确定终止原因
        O->>O: 保留首因，不覆盖为taken_over
    end
    V->>V: 独立发送成功前缀和回放，不等旧端通知送达
    Note over O,R: Supervisor强关及全部worker回收后才归还旧slot
```

旧Reader、旧Writer与浏览器是三个独立参与者；§12.5图8补充浏览器本地停止和排空。若Writer先发现superseded，其锁外Terminate可先于old.end固定taken_over；后者幂等。真实read/write/Ping故障先固定首因则保留。提交先持Session锁的分支中，Writer不得在拿到锁后再发送普通消息。新Binding在登记前已失效时按§7.3/7.4收尾，不保证一定发成功前缀。

### 9.2 时序图 5：提交前失败，旧 Binding 保持

```mermaid
sequenceDiagram
    participant C as 新浏览器
    participant API as Prepare API
    participant N as 候选WS
    participant S as Session事务
    participant O as 原Binding
    C->>API: 用户确认后Prepare takeover(G)
    API-->>C: 一次性凭证
    Note over API,O: 只签发；原Binding继续有效
    C->>N: Upgrade并bind
    alt 首条bind非法或超时
        N->>N: 统一终止候选；无需进入提交
    else 操作门等待超时
        N->>S: 在operation_wait_timeout内等待
        S-->>N: BIND_BUSY；没有提交
    else 门/登录/Session/Buffer/凭证检查失败
        N->>S: 按图4锁序验证及准备快照
        S->>S: 发现TTL/offset/代次/状态/归属等失败
        S->>S: 释放候选快照、全部锁和门
        S-->>N: 对应错误，未消费仍有效凭证
    end
    N-->>C: 尽力error/Close（升级失败则HTTP错误）
    N->>N: Supervisor/收尾者完成后归还候选slot
    Note over S,O: 本次失败不撤销原Binding、不清Session缓存、不归还原slot
    Note over C,S: 前端重试仅按原预算attach，不盲目重用token或换代
```

### 9.3 资源清单与提交后失败

| 资源 | 唯一释放者及边界 |
|---|---|
| 候选凭证 | Manager 提交消费/Session其他凭证失效、登录撤销、到期扫描或退出清理；前端 Abort 无远端撤销保证 |
| 候选快照 | 提交前失败即释放；成功交新 Writer；终止释放未发送数据，允许一次在途数据完成 |
| 旧 Binding | 提交点仅逻辑失效/superseded，不cancel；锁外统一终止先定原因再Detach取消，旧回调仅作用该对象，永不复活或改写新代次 |
| 新 Binding | 已提交但前缀写失败时只 Detach 新对象，若仍当前则 DISCONNECTED；不恢复旧端、不重建 SSH |
| WS/Reader/Writer/执行器/Ping/定时器 | 各连接统一终止；取消在途Ping；Supervisor不等调度锁或Close，绝对截止强关底层net.Conn |
| WS slot | Service 的连接收尾者等待 workers、Supervisor 全部完成后一次归还；Close发起、taken_over送达、Session移除均不能提前归还 |
| SSH/PTY/Buffer、服务器名额 | 仍属于 Session；takeover不释放；只由既有Close/reap在相应生命周期释放 |
| Pane/client/tracker/listeners | Pane.stop保留实例；Pane.dispose同步封闭准入并有限排空后销毁；页面卸载捕获清理异常 |

即便 bind_success 写失败，服务端提交已经发生，凭证已消费、generation已递增。若前端未收到该代次，原代次 attach 最终冲突停止；若已经收到并校验 bind_success，后续恢复可用刚获知代次。两种情形均不能通过查询偷偷换代。新成功不依赖旧通知送达；旧连接始终按自己的关闭预算收尾。

当 max_connections=1 且旧端占用时，新候选不能升级，因此无法直接在线接管；这是冻结的全局容量行为，不能提前释放旧 slot 绕过。实际接管测试至少预留一个候选 slot，并另测容量不足失败保持旧端。

## 10. 并发场景处理表

表中 G 为双方取得凭证时的同一 expected_generation。“失败码”描述可观察到的合法服务端结果，不保证首个被检测条件或通知送达。候选均由各自 WS 统一终止/收尾者释放；旧 Binding 物理清理可晚于逻辑失效。所有重试均不能重放输入。

| 编号 / 竞争双方 | 串行化和锁保护范围 | 双方先后、结果与失败方状态 | 资源、重试及逻辑/物理状态 |
|---|---|---|---|
| C01 两个 takeover | 各 Prepare 可同代次签发；提交共用操作门→登录读锁→Session→Buffer→凭证索引 | 第一个有效提交得到 G+1；另一个发现凭证已失效为 INVALID_CREDENTIAL，或代次变化为 BIND_CONFLICT。不能断言只一种错误。同 token 也最多一个成功 | 败者释放快照/候选slot，不撤销赢家。INVALID_CREDENTIAL仅明确code时允许原预算新attach，原代次遇冲突停止；不自动新takeover。旧/败者物理连接可正在关闭。不同代次、用户重新确认后的顺序接管不受“同代次仅一个”断言禁止 |
| C02 takeover 与 attach | Prepare/提交共用状态与凭证检查，提交共用门；mode来自凭证 | CONNECTED同代次attach在Prepare即BIND_BUSY；DISCONNECTED时两者均可候选，先提交者胜，后者同C01。attach先绑定后，旧代次takeover不能无条件覆盖它 | 失败者独立收尾；attach有限原代次重试；旧端/失败端不能拿赢家代次；物理关闭窗口允许 |
| C03 takeover 与用户关闭 Session | Close先拿Session锁撤销权限，不等操作门；提交仍按全锁序 | Close先：Prepare/提交NOT_READY，索引移除后NOT_FOUND，凭证已失效也可能INVALID_CREDENTIAL。提交先：新Binding先成为当前，然后Close立即撤销新端；旧端不复活。DELETE始终幂等 | Session reaper释放SSH/缓存/服务器名额，各WS独立收尾；不自动重建或接管已关闭会话；关闭通知可能丢失，列表/详情确认；CLOSING与物理资源可并存 |
| C04 takeover 与 SSH退出/故障 | monitor/读取/操作超时调用同一Close；Session锁裁决，首个关闭原因获胜 | SSH退出先同C03拒绝；提交先可短暂CONNECTED，随即ssh_exit/ssh_error关闭；不能把网络通知成功当SSH仍健康 | Session reaper负责SSH；新旧WS分别释放；NOT_READY/FOUND停止并诊断；不可回滚或自动创建新SSH；物理关闭可滞后 |
| C05 takeover 与离线自动关闭 | Sweep在Session锁内复查DISCONNECTED、无Binding、期限；提交重新检查当前期限 | 截止前提交先清期限，旧扫描不能关闭；到期后即便Sweep尚未运行也NOT_READY；Sweep先进入CLOSING则拒绝。不能通过过期前签发延长期限 | 到期由Session reaper释放；候选自行收尾；不得自动重试延长保留；列表可短暂显示过时剩余时间，提交才权威 |
| C06 takeover 与管理员登出 | 同一次登录的写锁撤销，与提交/Prepare登录读锁互斥；写锁释放后扫描凭证/Binding/所有WS | 撤销/冻结先则AUTH_REQUIRED或因凭证清除而失败；提交先则新绑定被撤销扫描找到并Detach。旧端与新端不同登录时，只撤销被登出的登录；旧端登出不能拆掉另一次登录的新Binding | 只关闭该登录浏览器通道，不直接Close SSH。已签发凭证失效；不自动恢复退出任务。不同登录的新端可能继续CONNECTED；同登录新端转离线，物理连接待回收 |
| C07 Upgrade成功但首bind失败 | Service登记slot；Reader首bind期限/格式/大小校验；进入事务后仍有完整复查 | 非法首消息/超时/非法偏移/过期/归属错给对应INVALID_MESSAGE/BIND_TIMEOUT/INVALID_OFFSET/INVALID_CREDENTIAL/PERMISSION_DENIED等；全部提交前失败不替换旧端 | 候选快照如已产生则释放；Supervisor/收尾者回收slot。协议明确可重试错误才原预算attach，其他停止；旧逻辑Binding保持，候选物理清理 |
| C08 新候选校验前旧输入在途 | 旧Execute与提交用同一操作门；准入持登录读锁/Session锁，网络在这些短锁外执行但保留门 | 旧输入先准入：唯一SSH调用结束后候选才可提交；门等待超时BIND_BUSY且旧端保持；SSH操作超时关闭Session。候选先提交：旧排队输入/resize再准入失败，以内部superseded处置进入taken_over，不误先发BIND_CONFLICT | 不撤回已准入效果、不补发未执行输入；执行器清理待执行槽，WS回收仍等执行器；短时物理共存但无双逻辑操作权 |
| C09 旧Reader阻塞（含半帧头） | 提交不等待Reader；旧端逻辑失效在Session锁；终止短锁/绝对期限Supervisor独立 | 新端可成功；提交不cancel旧context，Terminate先定原因再取消，Done退出不误抢NETWORK_ERROR。taken_over可能发出但不能依赖其读取/Close回应；期限到raw.Close解除阻塞 | 旧reader/writer/executor/supervisor退出后归还slot；SSH持续读取；被接管端不自动抢回，物理旧端可滞留至预算 |
| C10 旧Writer阻塞/Close握手阻塞 | 提交不持网络锁、不等旧写；短发送准入与失效同步；Supervisor独立 | 按§7.4双锁临界区已准入的一次旧Write可结束；提交先完成则普通输出/status零新准入；终止通知可能写失败，不能回滚新端；CloseNow不能抢断已开始Close | 取消Ping、共用close_timeout并在到期raw.Close；收尾后归还slot，不能用“通知未发出”保留权限；新端恢复按自身预算 |
| C11 旧端正在输出排空 | 服务端单Writer快照/实时调度；客户端单tracker/epoch；终止不进入SSH输出锁等待浏览器 | 提交撤销旧业务准入，旧Writer至多完成在途消息后终止；旧浏览器收到taken_over/4001保留有限已接受队列排空。若原本已停恢复/排空，新意图须等待stop完成 | 服务端释放旧快照余量，不清Session Buffer；客户端write_drain_timeout到则按tracker规则重建或dispose，不联网；新端不等旧排空；物理/画面收尾可滞后 |
| C12 提交成功但bind_success写失败 | Session提交已完成，Writer网络写在锁外；新端Terminate仅按新Binding身份Detach | 新端若仍当前转DISCONNECTED，generation保持G+1；若又被下一次合法接管则旧新端Detach不能影响更后Binding；原端不复活 | 凭证已消费；新WS资源正常收尾；未收到新代次的客户端用G attach冲突停止，收到G+1的可有限attach；通知和逻辑状态暂不同 |
| C13 旧Detach、WS Close、输出/执行器清理迟到 | Session锁检查对象身份/active/不可变代次；客户端task/socket/epoch隔离 | 旧Detach只失效旧对象，不清新Binding/期限；旧WS Close是浏览器通道关闭，不是Session.Close。旧输出回调不更新新实例；旧执行器不能用当前Binding替换自身身份 | 只释放所属资源一次；不触发新恢复/通知覆盖。显式DELETE与真实SSH故障Close仍合法，不能把它们当“旧回调”忽略；物理旧连接可正在回收 |
| C14 多浏览器/设备同时操作 | 后端登录归属、Session门/状态锁/凭证索引为权威；各页面仅本地去重 | 列表/详情可看同一G；同代次仅一提交，其他按C01。任一登录可明确关闭同一Session；独立登录登出只影响自身Binding | 失败端不自动查询换代抢回；可刷新后再次用户确认。跨浏览器不依赖消息总线/本地存储锁，允许物理连接暂存；共享Cookie的浏览器页面受同一次登录撤销 |

补充应用退出：沿用停止接入标志与绝对 shutdown_timeout；Prepare/提交遇 SERVER_SHUTTING_DOWN 不发布，已提交资源统一关闭；未绑定 WS 也登记回收，前端仍只按原预算有限 attach。执行器已准入的 SSH 调用、元数据最终写和 WS 监督任务各自按既有边界完成，不能因第五阶段增加第二套关闭期限。

## 11. 跨标签、跨浏览器和跨设备协调

### 11.1 所有权不变量

- 一个 HomePage 生命周期内，同一 Session ID 至多一个 Pane、一个 TerminalClient、一个 tracker、一个 SessionRecovery、一个有效长期任务；待挂载记录也参与去重。
- Session ID 是稳定业务标识；Pane/任务 ID 是本地所有权；Binding 由服务端提交产生，与本地组件寿命不同。列表项不是 Pane，删除列表项不能直接卸载/关闭终端。
- HomePage 只协调页面内标签和操作意图，调用 Pane 暴露的 stop/主动接管/关闭入口；实际 fetch/WS/重试由各唯一所有者执行。
- 切换服务器、列表和终端时保留 Pane，通过 active/可见性控制 fit/焦点。隐藏组件不能以零尺寸覆盖 PTY，后台恢复不能抢焦点。
- 浏览器顶层网页标签/窗口属于不同 HomePage，不能靠本页 Map 检测它们。它们与另一浏览器/设备一样由后端 Binding/generation 协调；不承诺全浏览器进程只有一个视图，不新增跨窗口选主或持久化标签恢复。浏览器刷新后通过列表发现，再由用户明确打开。

### 11.2 五类场景（D2=A、D3=A，用户已确认）

| 场景 | 已确认交互与所有权 | 失败、取消及迟到结果 |
|---|---|---|
| A 当前页没有该Session的终端标签 | HomePage同步预占Session ID，建立一个不自动连接的候选Pane；详情→确认→唯一Client首尝试takeover；成功前缀后同一Pane变正常标签并聚焦；连续点击定位同一候选 | 不以成功回调再push第二标签；失败保留停止/有限attach中的原Pane；取消前无WS；用户可关闭候选标签，dispose后迟到结果无效 |
| B 已有正常连接Pane | 列表“打开”只定位该标签，不从列表直接自我接管，不停止它，不申请凭证。若用户之后显式停止该终端，它按D进入主动流程 | 不存在同页两个Client或候选WS；取消定位不改变连接。若在检查后端状态时其他设备接管，原Pane自行收到终止/代次冲突；列表不能恢复旧任务 |
| C 已有Pane正在自动恢复 | 定位原Pane，提供明确“停止恢复并准备接管”；首次await前封闭旧查询/候选/重复动作，调用stop；等待旧task、发送、drain完成后GET新详情，再单独确认takeover；复用同一Client/tracker | 从点击停止准备起旧任务不再自动恢复；详情失败/取消确认不复活旧任务。主动任务可按既有协议自动attach有限重试；其最终失败后原Pane停止，用户可重新详情确认takeover，或走第四阶段两步人工attach。旧finally/handler不能覆盖新任务 |
| D 已有停止/被接管/已结束Pane | 停止或taken_over保留的Pane可以显式新详情→确认接管，优先复用原实例及其processedOffset。close已证实或SESSION_NOT_FOUND的Pane仅显示结果/关闭标签，不复活 | 必须先刷新状态；列表快照不能替代。CLOSING/CONNECTING禁用；真正closed不会重用Session ID，不能因列表迟到行清除ended。disposed Pane不可复用，必须等移除完成后方可按A处理一个仍活动的Session |
| E 旧浏览器仍打开终端 | 在线则接收taken_over/4001，立即禁止输入/自动恢复并有限排空；断网/休眠导致通知丢失时，恢复网络仅原代次attach，冲突停止 | 离线/休眠期间不假设已收到通知。即使INVALID_CREDENTIAL先出现，也仅原代次有限attach，不能取最新代次抢回。页面彻底关闭则无本地任务，后端旧通道按心跳/关闭期限回收 |

B按已确认D2=A执行“定位”，不触发无提示attach或自我接管；用户之后明确停止再按既定流程准备主动接管。未采用在线直接主动重连的B方案。

### 11.3 同一目标的关闭与新动作

页面为每个 Session 的接管、人工恢复、明确关闭和标签dispose设置共同的同步意图边界。关闭SSH确认前不停止旧端；确认后关闭意图优先取消正在准备/进行的接管，调用stop并发出DELETE，不等待本地排空。重复takeover不能绕过关闭意图；重复DELETE可在前次结果后幂等重试。关闭标签则取消本地所有者并dispose，不调用DELETE。

> **S6 修订（2026-11-18，方案 B）**：上段“页面为每个 Session 设置共同的同步意图边界”在 S6 中由 Pane 承担，不在 HomePage 设置：Pane 的 `allowed`（生命周期、身份、remoteBusy、closeRequested）同时注入 Client、Recovery 和 SessionTakeover。“关闭 SSH 会话”按钮不因接管进行中而禁用；关闭确认框打开期间接管的准入检查失败，确认关闭后经 Pane `stop()` 使接管失效。关闭意图优先、确认前不停止旧端、DELETE 语义保持不变。

列表更新、详情读取、onMounted、ononline、visibilitychange 均不获得用户操作身份。只有明确按钮确认创建初始takeover任务；可将“候选已确认并消费”的局部对象传给Pane，不提供全局可随意改mode的恢复状态。

## 12. 关闭、登出、页面退出与任务取消

### 12.1 页面关闭和所有权转移（D3=A，用户已确认）

> **S6 修订（2026-11-18，方案 B）**：下一段（历史原文）中“确认前，管理视图拥有查询/确认动作，离开管理页取消查询/确认并移除未启动候选”**已被方案 B 替代，不再有效**。现行规则：所有权边界是“Pane 初始化成功并 emit ready”，而不是“用户确认启动终端任务”。ready 之后候选的详情查询、确认、取消都归 Pane；SessionManager 卸载、切换视图、刷新列表只影响列表 GET，不取消 Pane 的动作，也不移除候选 Pane。用户取消接管后候选 Pane 保留（U4），只能由用户关闭标签；Session ID 预占只在 Pane 清理成功（emit closed）后释放。

（历史原文，已替代）按D3=A以“用户确认启动终端任务”为边界：确认前，管理视图拥有查询/确认动作，离开管理页取消查询/确认并移除未启动候选；确认后任务归该 Pane，管理列表的隐藏/卸载仅取消列表GET，不暗中停止用户已打开的终端。UI明确显示正在接管的目标标签；用户可在Pane停止或关闭标签取消它。

因此“takeover HTTP 已发出后关闭管理页面”有三种明确情况：仅切回工作区时Pane任务继续；关闭对应终端标签时stop/dispose；离开整个HomePage或关闭浏览器网页时所有Pane同步封闭准入、取消请求/handler并dispose。不能把关闭一个列表组件等同于关闭SSH。按方案 B，上述三种情况同样适用于确认前的候选动作：只关闭管理页面时，候选的查询和确认继续由 Pane 持有。

用户关闭网页时无法保证异步drain或网络Close握手实际完成，不依赖beforeunload发送DELETE；服务端靠现有断线/心跳/Supervisor回收Binding与WS，SSH继续离线保留。已提交后即刻关闭网页只Detach新端，不恢复旧端；尚未提交的候选关闭不撤销其他旧Binding。

### 12.2 登出顺序和迟到响应

沿用第四阶段计划 §11/13 与实际 auth store：HomePage 首次await前设置logoutPending，使页面任务身份失效，取消列表/详情/确认、待挂载任务和凭证请求、停止全部Pane。logout HTTP与本地排空并行，不等drain后才请求撤销。

> **S6 补充（2026-11-18，U9）**：S6 只做最小适配，不提前实现 S7 完整登出语义。顺序保持并行：同步设置 logoutPending（Pane watcher 同步 stop，接管动作失效）→ 同步对已登记 Pane 发起 stop → 立即 logout → 等待 stop 结果 → logout 成功后 close 全部 Pane → 跳转登录页。stop 失败（任务可能未完全停止）与 close 失败（本地终端资源未释放）分别统计、按 Session ID 记录日志并反馈给用户；登出成功优先，清理失败不阻止退出。logout 失败时保持“退出未完成”行为，不 close。

Prepare成功但候选未提交时登出：浏览器Abort/stop防止继续发布；服务端登录写锁先使本登录无效，锁外清凭证和所属全部WS。候选如果已提交则扫描Detach它；如果来自另一独立登录的旧Binding尚有效，登出候选登录不能误撤销旧端。过期同样使用当前登录归属检查。

每个await返回、JSON解析后、onopen、onmessage、重试timer、布局nextTick及finally检查本地动作/任务/Pane/登录身份。退出后凭证或成功前缀迟到不能创建WS、改代次、复活标签、覆盖“退出未完成”。老任务的xterm回调只有原epoch仍有效时可完成drain，不操作新实例。

| logout结果 | 继承的处理 |
|---|---|
| 200 | 确认持久化撤销，清身份，销毁页面任务后进入登录页；不发Session DELETE |
| STORAGE_ERROR 确定回滚 | 保留Cookie/身份和logoutPending，提示退出未完成、允许重试；已停任务不自动恢复 |
| SERVER_UNAVAILABLE 提交不确定 | 服务端冻结本登录并撤销所属通道/凭证，Cookie仅供logout重试；前端保持退出意图，不假装成功 |
| 网络/响应丢失 | 结果未知，保持退出意图，幂等重试logout；不新登录覆盖Cookie |
| AUTH_REQUIRED 与退出竞争 | 保留未完成退出含义；无退出意图时才走普通过期清理 |

接管成功后旧端终止通知/Close迟到：服务端仅旧对象Detach；前端同一Pane已移除旧handler，闭包仍检查task/socket；不同浏览器的旧Pane只显示自身停止原因。不能用旧端的closed/taken_over更新新Pane为已结束。

### 12.3 时序图 6：takeover 与 Session 关闭

```mermaid
sequenceDiagram
    participant C as 候选提交
    participant S as Session
    participant D as DELETE或SSH关闭发起者
    participant R as Session reaper
    participant W as 各WS收尾者
    alt 关闭先取得Session锁
        D->>S: Close，不等操作门
        S->>S: CLOSING，撤销当前Binding，清凭证/期限
        S->>R: 锁外关闭SSH及回收
        C->>S: 门→登录读锁→Session锁复查
        S-->>C: NOT_READY或失效凭证；移除后NOT_FOUND
        C->>W: 终止候选，无替换
    else 候选先通过全部检查提交
        C->>S: 门→登录→Session→Buffer→凭证，原子替换
        C->>C: 释放全部锁和门
        C->>W: 启动新Writer并终止旧端，不等通知
        D->>S: Session锁内Close，撤销新Binding
        S->>R: 锁外关闭SSH
        R->>W: SSH关闭后尽力closed
        Note over S,W: 不恢复旧端；通知首因获胜，新端可能尚未收到bind_success
    end
    R->>R: 等SSH任务/最终元数据，释放缓存/服务器名额/索引
    W->>W: 每条WS按独立期限回收全部任务后归还slot
```

### 12.4 时序图 7：takeover 与登出

```mermaid
sequenceDiagram
    actor U as 用户
    participant P as 页面与Pane
    participant A as 登录对象/Logout
    participant C as Prepare或候选提交
    participant M as Manager及WS Service
    U->>P: 退出登录
    P->>P: 同步logoutPending，失效动作，Abort并stop全部Pane
    par 服务端撤销
        P->>A: POST logout
        A->>A: 登录写锁内短事务及结果判断
    and 候选竞争
        C->>A: Prepare或提交申请登录读锁
    end
    alt 撤销或冻结先完成
        A-->>C: AUTH_REQUIRED，不准入
        A->>A: 释放登录写锁
        A->>M: 清该登录凭证/Binding/全部WS
    else 候选持读锁先提交
        C->>C: Session/Buffer/凭证锁内提交后释放全部锁
        A->>A: 随后持写锁撤销/冻结，再释放
        A->>M: 扫描必须包含刚发布的新Binding
        M->>M: 按登录及对象身份Detach，锁外Terminate
    end
    A-->>P: 200或明确失败/不确定
    P->>P: 200才确认退出；失败保留意图与重试
    Note over P,C: 晚到凭证、onopen、bind_success、finally均无旧发布权限
    Note over M: SSH保留；其他独立登录的新Binding不受此登录退出影响
```

确定回滚时登录不被服务端撤销，图中“撤销或冻结”分支不发生；前端仍按第四阶段规则保持停止和未完成退出，不能自动重启。图中的并发竞争以同一个WebAuthSession为对象。

### 12.5 时序图 8：旧 WebSocket 收到 taken_over 后停止

```mermaid
sequenceDiagram
    participant S as Session提交
    participant O as 旧WS终止槽和Writer
    participant B as 旧浏览器Client
    participant T as 旧tracker/xterm
    participant R as 旧WS Supervisor与收尾者
    S->>S: Session锁内旧active=false及superseded，发布新Binding，不cancel
    S->>S: 释放事务锁和操作门
    S->>O: 锁外请求taken_over
    O->>O: 持Connection.mu先固定taken_over和关闭截止
    O->>S: Detach旧对象并cancel，仅按身份操作旧Binding
    O->>O: 发布独立终止槽、取消Ping，解锁后唤醒Writer
    Note over S,O: Done唤醒的Reader在原因确定后退出，迟到defer不能覆盖
    Note over O: 先前普通item可完成；不得再准入新普通项
    O-->>B: taken_over，随后Close 4001
    B->>B: 首因幂等结束，禁输入/resize，停自动恢复，清handlers/timers
    B->>T: settle(true)，有限排空已接受队列
    alt 本地write按时完成
        T-->>B: 保留已处理画面，显示已被接管
    else drain超时或实例异常
        T->>T: epoch隔离，按既有tracker终止/销毁规则收尾
        T-->>B: 不再联网
    end
    R->>R: 到期raw.Close解除Read/Write/Close阻塞
    R->>R: 等workers与Supervisor完成，再一次归还slot
    Note over S,R: 新端成功不等待这条流程；通知丢失时旧端只用原代次attach，冲突停止
```

## 13. 与第四阶段兼容性清单

| 既有能力 | 第五阶段不可改变的结果与验证 |
|---|---|
| 自动恢复 | 永远attach；初始takeover后自动尝试也只attach。列表/详情/online/visibilitychange不产生takeover |
| 两步人工恢复 | 原Pane“刷新Session状态→再次点击手动恢复”保持attach；不能由列表刷新生成candidate，诊断GET也不能生成 |
| 代次 | 初始用户动作可采用明确详情候选G；自动仅bind_success更新，且必须G+1；冲突停止，不查询换代 |
| 偏移/回放 | processedOffset只由当前实例连续write callback推进；新实例从0；同实例drain后读取；S>P时唯一截断前缀重建；每WS一次稳定快照 |
| 重试预算 | 原max/delay/stable_time、稳定资格一次消费、全部错误集合不变；不因接管/列表刷新/成功前缀重复重置 |
| taken_over/4001 | 停输入和自动恢复；裸4001显示接管关闭含义但不伪造已收到业务消息；旧端不能自动抢回 |
| OUTPUT_GAP/4002 | 仍丢未提交队列、等在途write后有限恢复；不与4001混淆，不增加invalid_bind或retryable字段 |
| INPUT_BACKPRESSURE | 停止，不重放输入；普通输入/粘贴/历史查询自动应答继续按回放门禁 |
| UI状态修复 | 当前连接提示与历史异常分离；新有效前缀清当前旧错误；旧事件不污染新任务；网络online仅展示；不因新增列表改回旧提示行为 |
| stop/dispose | stop可保留实例；dispose不可逆，销毁中不建替代实例；后续新任务必须等待旧任务和发送/drain完成 |
| 超时 | 凭证POST仅credential_timeout；详情GET无本地定时器；WS前缀等待仍bind_timeout+operation_wait_timeout+write_timeout；不修改配置和默认值 |
| 独立操作 | 关闭标签仅dispose；DELETE需确认且不等于立即CLOSED；takeover复用SSH，三种操作不混用 |

第一次takeover失败并进入有限attach后，不能因目标仍CONNECTED而反复takeover。第一次已提交但未收到成功前缀则可能很快BIND_CONFLICT；这正是停止并交由用户确认的安全边界，不“修复”为自动采用新代次。

## 14. 安全、日志和边界检查

1. 列表/详情只经当前认证查询；写API和Upgrade严格Origin；不因同一管理员放宽凭证所属独立登录、目标Session、mode或TTL校验。
2. 凭证仅HTTP响应至内存、首条bind；不进入URL、浏览器持久存储、列表、诊断、日志。候选失败不新增撤销接口，不延长TTL。响应/日志禁止密码、密文、nonce、主密钥、Cookie、登录令牌/哈希、完整Host Key与终端输入输出。
3. 全局WS容量包括旧、新、候选、升级中和关闭中；凭证容量原子检查，不淘汰仍有效凭证。无无限候选队列、无后台takeover重试服务。
4. 跨设备只使用Session ID与快照目标，不展示未经服务端定义的设备身份/IP/“我拥有此Binding”。Session名称与目标采用Vue文本插值，不把SSH输出或名称作为HTML。
5. 错误按code分类；同代次失败允许BIND_CONFLICT或INVALID_CREDENTIAL；原生浏览器Upgrade失败只能看到通用网络事件时不伪造HTTP业务码。HTTP BIND_BUSY不等于凭证已消费，DELETE响应丢失也不等于Session仍活着。
6. 新增接管日志仅记录Session ID、旧/新generation、操作种类/安全原因、失败阶段与回收是否完成，不记录凭证或终端内容。普通WS失败不是SSH关闭原因。
7. 代次/偏移安全整数溢出沿用counter_exhausted关闭；处理在退出当前同步边界后发起，不持Buffer/凭证锁反向调用Close。
8. 不改SSH Host Key、Cookie、数据库与部署安全模型。任何验收用秘密只在隔离环境注入，截图/日志脱敏。

## 15. 详细实施顺序与审查单元

本节全部是未来编码授权后的步骤。步骤可以分别审查，但在服务端提交、旧端终止与前端停止流程完整前，不把中间补丁部署给用户。特别是S2直接改变Prepare行为后，必须连同S3完成并验证，不能把尚缺taken_over发送的中间状态当可交付版本。

共同前置：重新检查当前分支/HEAD、工作区/暂存区，确认包含第四阶段与UI修复。分支按总体阶段§1.1及届时用户授权处理；本轮不fetch、不切分支。保存每一步开始时受影响文件的补丁/内容基线，暂存与未暂存分别辨认。回滚仅逐文件/逐hunk撤销本步骤未提交增量，保护前序成果和用户同文件修改；新增文件只有确认仅为本步骤所有且无后续引用才移除。禁止reset --hard、clean或整文件覆盖用户内容。难以安全分离时保留并报告。

| 步骤 | 依赖、修改文件与新增文件 | 实现职责 | 本步测试与通过标准 | 本步失败回滚 |
|---|---|---|---|---|
| S0 基线与裁决登记 | 依赖人工审查及编码授权；只读所有接入点/既有测试；本步不改业务文件；复核本计划§21已有D1–D3=A裁决与第二轮复审结果 | 核对第四阶段基线和D1–D3结果，确认后续新增文件职责；识别已有用户补丁 | 不把历史测试算新结果；基线命令成功或明确列出已有失败；锁序/任务所有者能对应到代码 | 无业务修改可回滚；若计划登记有误仅撤销本次登记，不改冻结文档 |
| S1 活动列表后端 | 依赖S0；修改`backend/internal/session/session.go`、`backend/internal/httpapi/sessions.go`、`backend/internal/httpapi/sessions_test.go`；新增`backend/internal/session/list_test.go` | Manager.List、具名列表响应、严格server_id query、CLOSED过滤、稳定排序、安全View；现有Get/DELETE不变 | U01–U04；HTTP认证、空数组、过滤/排序/字段；并发关闭/接管快照测试可先用已有attach验证，S3补takeover；每行一致，无DB活动查询/锁嵌套 | 撤销List及GET路由与专属测试增量，只移除本步独占list_test.go；保留原详情/DELETE |
| S2 凭证mode与原子替换事务 | 依赖S0/S1；修改`backend/internal/session/binding.go`、`backend/internal/session/session_test.go`；新增`backend/internal/session/takeover_test.go`；如内部提交结果签名变化，最小同步修改`backend/internal/websocket/connection.go`调用适配 | 存mode，共用准入，扩展同一提交点，交出被替换Binding收尾责任；锁外通知接口，不复制一套Attach；更新既有拒绝takeover的范围断言 | U05–U09、R01–R05；同代次单赢家、提交前旧有效、提交后不回滚、操作门/关闭/登录/TTL竞争；race通过。直调回调测试不代替真实WS | 按本步基线成组撤销mode、准入、提交结果及调用适配；恢复原attach-only范围断言，保留S1；本步不单独部署 |
| S3 WebSocket终止及交接 | 依赖S2；修改`backend/internal/websocket/connection.go`、`protocol.go`、`connection_test.go`、`backend/internal/httpapi/sessions_test.go`；新增`backend/internal/websocket/takeover_test.go` | taken_over具名DTO/4001、先定原因再取消、失效退出分流、Writer双锁原子准入、提交登记窗口、slot与Supervisor复用 | W01–W06、R06–R08、T1–T4；真实HTTP/WS/SSH验证新旧端消息和操作；Reader/Writer堵塞不阻挡新端、旧slot不早还；失败允许合法首因 | 先撤销本步WS/HTTP测试及实现hunk；若放弃整个接管后端，还须逆序撤销S2，不能保留缺发送链路的公开takeover能力；S1保持 |
| S4 列表前端及异步查询 | 依赖S1与D1；修改`frontend/src/api/sessions.ts`、`frontend/src/pages/HomePage.vue`、必要的`frontend/src/styles/base.css`；新增`frontend/src/session/list.ts`、`frontend/src/components/session/SessionManager.vue`、`frontend/tests/session-list.test.mjs` | 列表解析/单请求所有者/序号/Abort、服务器和本地状态筛选、独立管理视图、loading/error/empty；保持Pane挂载；接管按钮此步不开放未完成链路 | U10、F01–F03；乱序、旧finally、卸载/登出、可见刷新、失败保留快照；列表任何刷新无POST/WS；实际组件测试 | 撤销列表组件挂载/样式/API增量，移除仅本步文件；原服务器页/终端与后端列表保留，无依赖清单变化 |
| S5 Client初始takeover模式 | 依赖S3，推荐在S4后审查；修改`frontend/src/terminal/client.ts`、`frontend/tests/terminal.test.mjs`；无必需新增生产文件 | 初始用户模式、后续固定attach；保持两种Promise/同一任务/预算/offset；明确连接结果和停止原因 | U11、F04；首POST是takeover、所有重试是attach、未知提交不换代；既有attach/4001/4002/稳定预算/drain测试全部通过 | 撤销mode增量及新增测试，保留原connect/run默认attach；不删除旧恢复实现，不引入输出算法变更 |
| S6 Pane与同页标签协调 | 依赖S4/S5、D2/D3；修改`frontend/src/components/terminal/TerminalPane.vue`、`frontend/src/pages/HomePage.vue`、`frontend/src/components/session/SessionManager.vue`、`frontend/tests/terminal-pane.test.mjs`、`frontend/tests/session-list.test.mjs`；新增`frontend/src/terminal/takeover.ts`、`frontend/tests/session-takeover.test.mjs`（S6 修订 2026-11-18：原“修改`recovery.ts`”“新增`session/takeover.ts`”已被方案 B 替代——`recovery.ts`不修改，通过注入准入实现互斥；接管对象放在`terminal/`，U8；细分为 C1/C2，见 S6 设计） | HomePage以`records`同步预占/候选Pane，ready后动作归Pane；新的小型主动动作所有者只管详情/确认/取消并调用既有Client；mount模式显式；A–E场景、复用实例、停止旧链、失败保留状态 | F05–F09；重复点击/挂载窗口/在线定位/恢复中接管/失败取消/晚回调；同Session一个Pane/Client，旧任务完成前无新任务；两步人工attach原样通过 | 先撤销UI入口，再撤销Pane启动与所有权增量，移除本步专属控制器/测试；恢复原mounted attach，仅保留创建Session入口，S4列表仍可只读 |
| S7-0 设计裁决与计划冻结 | 本次已完成；仅修改 `doc/stage-5-s7-design-decision-proposal.md` 与本计划；前置 S7-0 用户授权 | 登记 D1、D1-A～D1-E、D2-1、D2-2、D3-1、D4-1、D5-1、D5-2；冻结 S7-1～S7-6 的目标、文件、依赖、约束、测试、验收与停止边界 | 12 项裁决无遗漏；S7 提案 §20 与本计划 §21.6 一致；`git diff --check` 通过 | 撤销文档增量；不改代码；不改变 S6 冻结语义 |
| S7-1～S7-6 关闭/登出/视图退出与列表反馈 | 依赖 S6；S7-1 新增 `frontend/src/session/close.ts`，修改 `api/sessions.ts`、`TerminalPane.vue`、`HomePage.vue` 及直接相关测试；S7-2 修改 `SessionManager.vue`/`HomePage.vue`；S7-3 修改 `HomePage.vue`/`SessionManager.vue`；S7-4 修改 `HomePage.vue`；S7-5 修改 `TerminalPane.vue`/`HomePage.vue`；S7-6 测试与报告；详见 §21.6 | S7-1 作为完整强耦合单元落地协调器、状态机、DELETE/详情、Pane/HomePage 接线；S7-2 只加行级关闭意图；S7-3 只做列表失效/合并/tombstone；S7-4 严格沿用 S6 登出顺序；S7-5 隔离页面退出与迟到回调；S7-6 做分层测试、浏览器验收与报告 | F10–F13、B01–B05 与 S7-C01–C30；DELETE/登出成功、失败、不确定；代理同形 JSON 归 unknown；未知/请求中/失败不解除关闭意图；`idle + CLOSING` 双重准入；Pane 销毁不取消任务；登出后不联网；真实浏览器验收 | 逐阶段撤销本阶段增量；S7-1 未完成前不拆出 Pane 中间态；不回退 S6/第四阶段既有 logoutPending/stop/候选 Pane 语义；前序受依赖部分不单独发布 |
| S8 全集成、真实环境与报告 | 依赖S1–S7及环境授权；扩展上述测试；仅发现有证据缺陷时修改对应责任文件；获准后新增`doc/stage-5-implementation-report.md`，临时浏览器脚本/日志用`/tmp/codex/` | 全量Go/race/前端、真实WS/浏览器、Docker/NPM/SSH验收；记录环境/命令/失败修复/未验项；完成后停止 | §17–20所有门槛；无双有效Binding、无泄漏/旧端抢回、真实场景有证据，不能只靠单测 | 仅撤销本步独立失败修复/测试/报告增量，保留前序审查成果；集成失败不宣称完成、不部署中间补丁；外部环境回滚按事先获准部署方案 |

S7-0 已完成设计裁决登记与计划冻结；S7-1 尚未获得编码授权，S0 的编码基线检查随 S7-1 的单独授权一并执行。所有“必要时修改”都以实际失败或接口适配证据为前提，不预先扩大代码范围。

## 16. 文件职责和最小改动控制

| 文件/模块 | 预期责任与明确边界 |
|---|---|
| `backend/internal/session/session.go` | List与现有生命周期快照；不建SQL列表或第二个Session索引 |
| `backend/internal/session/binding.go` | mode、唯一提交/失效与旧引用、superseded内部处置、持Session锁的狭窄发送准入接口；不发送JSON、不等WS关闭、不创建SSH |
| `backend/internal/websocket/{connection,protocol}.go` | taken_over消息、先定原因再取消的统一终止及内部退出适配、Writer双锁选项准入/提交交接；不拥有SSH生命周期、不提前减slot |
| `backend/internal/httpapi/sessions.go` | 新列表路由、具名响应/query校验；原Prepare请求形状不变 |
| `frontend/src/api/sessions.ts` | List DTO运行时校验、查询/关闭HTTP包装；不做UI所有权或绑定循环 |
| `frontend/src/session/list.ts`（建议新增） | 列表请求身份/Abort/快照状态；不拥有任何TerminalClient |
| `frontend/src/session/close.ts`（S7-1 新增） | `CloseCoordinator`/`CloseTask`/`CloseSnapshot`/`CloseHandle`；按 Session ID 管理关闭状态、DELETE/query controller、epoch/queryEpoch、订阅、dispose、本地 stop 注册；当前链路除合法 200 外一律 `unknown`，不凭业务码伪造 `failed`；不渲染 UI，不管理 takeover/recovery，不建立全局 Store/事件总线/定时轮询 |
| `frontend/src/terminal/takeover.ts`（新增；S6 修订：原建议路径`frontend/src/session/takeover.ts`已被替代，U8） | 单Pane主动动作的详情/候选/确认身份和取消；依赖已有Client，绝不自建WS/重试循环 |
| `frontend/src/components/session/SessionManager.vue`（建议新增） | 列表展示、筛选、刷新、独立关闭/打开意图；S7-2 只 emit `close`，不直接 DELETE、不维护关闭状态；把动作交给页面/目标Pane，不在每行创建Client |
| `frontend/src/pages/HomePage.vue` | 同页唯一ID登记、视图切换、Pane引用、页面停止/销毁；S7-1 起持有并 dispose `CloseCoordinator`，按 Session ID 路由关闭意图；列表行数据不能替换运行中Pane对象 |
| `frontend/src/components/terminal/TerminalPane.vue` | 唯一client/tracker/recovery、主动动作控制器、mount入口、焦点/尺寸/提示；S7 中只通过 `CloseHandle` 读取/操作关闭状态，`close()` 只退订，不 abort 协调器任务 |
| `frontend/src/terminal/client.ts` | 明确初始mode、原有限循环、Promise与socket所有权；自动分支固定attach |
| `frontend/src/terminal/recovery.ts` | 保持原两步attach候选；不把主动takeover塞进recover默认路径。S6 修订：原“只补主动意图互斥/失效协作”已被替代——S6 不修改本文件，Pane 在注入的`allowed`中加入“takeover 空闲”实现互斥，接管准备时调用既有`invalidate()`使人工候选失效 |
| `frontend/src/terminal/output.ts` | 预期无生产改动；只通过现有算法复用与回归验证；发现真实缺陷再说明最小修复 |
| auth/router、config、migrations、go.mod、package锁文件 | S7 不修改 auth/router（D3-1 已接受 A）；config/migrations/go.mod/package 锁文件预期不改。auth 测试可扩展，不能借机改登出成功边界 |

新增控制器为可测试的小型页面所有者，不建立全局store、事件总线或任务队列。若S6实际能在现有Pane内保持职责清晰且完整测试，可裁减文件数量；不能同时保留两套所有者。生产注释继续中文，测试按真实职责命名。

## 17. 分层测试设计与实施门槛

本节是未来执行清单；本次没有运行或宣称第五阶段测试通过。已有第四阶段测试全部保留作为回归基线，历史数量不作为当前结果。

### 17.1 单元与模块测试

| ID | 实际被测对象与输入 | 必须断言 / 对应步骤 |
|---|---|---|
| U01 | Manager.List空索引、四活动态、CLOSED短窗口、SQLite残留活动行 | items空数组；CLOSED不返回；无SQLite复活；S1 |
| U02 | 同毫秒/不同创建时间、随机map插入、多server_id、合法未知ID | 创建时间/ID确定升序，过滤准确，目标快照不被服务器编辑覆盖；S1 |
| U03 | HTTP GET列表、匿名/过期登录、未知/重复/空/非法query | 信封/状态码/no-store/DTO与核心一致，无分页或状态参数；S1 |
| U04 | 列表字段/时间/剩余秒数、敏感字段探针 | 仅冻结字段，离线秒数向上取整非负，非离线null，无凭据/连接对象；S1 |
| U05 | Prepare attach/takeover × CONNECTED/DISCONNECTED/CONNECTING/CLOSING/到期 | attach占用仍忙，takeover仅两态，代次变化优先于同代次忙；HTTP Prepare不改旧active/代次/期限；S2 |
| U06 | 凭证mode、归属、目标、TTL、容量、相同token重复提交 | mode不可由bind篡改；只在提交消费，其他凭证失效；非法输入拒绝；S2 |
| U07 | P>E、快照复制后凭证过期/清除、登录到期、离线期限到达、ctx取消 | 失败释放快照，原Binding/generation保持；凭证仅在仍有效且无外部失效时可用；S2 |
| U08 | 直接提交后人工触发新端失败/旧Detach | 原Binding永不恢复；新端若当前则离线且代次不退；旧Detach不改新期限；S2 |
| U09 | generation/offset边界、元数据UPDATE失败 | 不回绕，既有counter_exhausted路径；元数据失败不恢复权限、不使列表回退；S2 |
| U10 | 实际列表控制器/DTO解析配合可控fetch | 序号+Abort+身份+finally保护，错误/空/加载独立；无POST/WS副作用；S4 |
| U11 | 实际TerminalClient/tracker配合可控fetch/WS/时钟 | 首尝试明确mode，后续一律attach；两Promise边界、原预算/稳定资格/偏移/401等不变；S5 |

### 17.2 并发与竞态测试

| ID | 受控交错 | 必须断言 |
|---|---|---|
| R01 | 多个不同凭证同G、同token并发；均在提交门前汇合 | 有条件允许提交时恰一赢家，generation只增一；失败码集合含INVALID_CREDENTIAL/BIND_CONFLICT，不能断言唯一冲突码 |
| R02 | takeover/attach在DISCONNECTED竞争；在线attach与takeover | 同代次不产生双有效Binding；attach不替换；串行新一轮不同G不误判为重复提交 |
| R03 | 旧SSH Write/resize准入后阻塞；候选等待门；旧排队项尚未准入 | 在途完成后才提交；等待超时不先撤销旧端；旧排队项零SSH效果；SSH自身超时按原关闭规则 |
| R04 | Close、SSH退出、Sweep与提交分别先后；快照准备后到期 | 两种先后均有效，CLOSING不回退/不重设期限；旧扫描不关闭新绑定；首关闭原因保持 |
| R05 | 同登录Prepare/提交与真实Logout；不同登录旧端退出/新端退出 | 撤销扫描不漏刚发布资源，不误删不同登录Binding；持久化回滚/不确定区分；SSH保留 |
| R06 | Commit返回后暂停Reader登记，触发Close、Revoke或又一次合法takeover | 即使Connection.binding尚空也收尾已提交Binding；旧引用通知责任不丢失；没有发布已失效的新操作权 |
| R07 | 按T3固定Writer已准入、提交已完成、双方同时竞争Session.mu三个交错；按T1交错Reader Done/defer、执行器失效退出与old.end | 准入时同时持Connection.mu/Session.mu，提交后零新增普通准入；只允许提交前唯一item完成；正常taken_over/4001，独立首因按T2保留；结合T4断言身份与资源 |
| R08 | 列表复制引用后关闭/移除/接管/新建；100轮接管/停止竞争 | 单行一致、无CLOSED行/重复ID/数据竞态；索引/slot/worker/定时器恢复基线，无全局锁阻塞其他Session |

Go必须运行 `go test -race ./...`；受控同步点用于固定交错，生产默认不安装。不要通过调低断言、只sleep或只断言无panic替代权限/完成信号验证。

### 17.2.1 第二轮专项 T1–T4：固定交错与实际证据

四项归入S2的内部准入/处置测试与S3的真实WS集成测试，S8统一回归，不改变S0–S8边界。建议扩展 `backend/internal/session/takeover_test.go`、`backend/internal/websocket/takeover_test.go` 及既有HTTP/WS夹具；本轮只设计测试，未新增测试源码或执行测试。

共用夹具：实际Session Manager、HTTP/1.1 Upgrade、固定coder/websocket、真实环回SSH及输入/resize调用计数。旧端A、新端B分别消费真实WS消息，记录type、普通二进制块、最终控制消息和Close code。同步hook只定位锁/任务交错，不替换真实Reader/Writer/Terminate；生产不安装。记录commit和send-admit事件序号、Binding指针/不可变代次/active、Session状态/期限、远端调用数、Connection.reason及workers/supervisor/done/slot。测试事件保存在有限测试记录中，不增加生产日志中的终端内容。

#### T1：Reader取消与takeover通知竞争

1. 建立A并完成bind/前缀消费，确认一个有效Binding及SSH输入可达。将A Reader固定到两个独立子场景：
   - **网络读取等待**：Reader已进入下一次readMessage；此处没有直接select Binding.Done。B提交后由统一终止、Writer Close及必要的raw.Close解除读取，检查随后defer的幂等结果。
   - **Binding.Done等待**：在不持Session操作门、Session锁或Connection锁的测试屏障暂缓旧执行器，填满容量1操作槽，让Reader持有下一条有界输入并已进入 `select operations / b.Done / c.ctx / timer`。不得阻塞一个已经持操作门的SSH调用后又要求takeover立即提交。
2. 发起B正常takeover，暂停在提交已完成、old.end尚未调用的位置（此子例同时在锁外屏障暂停旧Writer/执行器的失效处理，排除其他合法终止入口先行）：断言old.active=false/superseded、新Binding/G+1已发布，**old.Done尚未由本次提交关闭**，此时不能用提前cancel“制造”新的正常路径。释放该屏障后，在旧Terminate固定taken_over、尚未Detach取消且仍持c.mu的位置确认reason已确定。
3. 允许Detach取消，令Reader Done分支醒来、失效退出适配与defer尝试进入，同时old.end仍在c.mu内完成终止发布；解除全部屏障。重复捕获的迟到defer NETWORK_ERROR，必须仍为taken_over。
4. 追加合法迟到Detach子场景：提交后、old.end前，触发一个此前捕获旧Binding的Detach；Done分支通过§7.3.2内部失效退出适配请求taken_over，再执行defer。令适配与old.end同时竞争c.mu，两种获胜次序都固定相同taken_over，不能由普通NETWORK_ERROR或旧执行器BIND_CONFLICT抢先。
5. 至少100轮有界重复，穿插旧执行器恢复、Writer先发现superseded、正常old.end先到的调度；每轮清理后再下一轮。使用足够的测试配置期限和有限调度容差，不让测试屏障耗尽关闭预算再要求消息必达。

**严格断言：**无独立故障且旧客户端正常读取/响应关闭时，真实线上必须收到一次 `{"type":"taken_over","message":...}` 和Close **4001**，不存在替代它的error NETWORK_ERROR/BIND_CONFLICT，终止后无普通消息。旧未准入输入/resize远端执行次数为0；B输入执行一次、仍是当前Binding、generation不回退、状态CONNECTED/离线期限为空。旧Binding.Done最终关闭；旧worker/监督器全部完成后slot只归还一次，新slot保留；最终关闭B后资源回到基线。普通readMessage场景允许通过既有关闭机制解除，不错误断言cancel Binding本身会直接取消网络读取。

#### T2：旧连接真实网络故障先发生

1. A完成绑定后实际关闭/重置客户端TCP，使用完成信号确认服务端已经在c.mu内确定NETWORK_ERROR并按旧身份Detach；不能只凭客户端发起close推定服务端已经终止。
2. B对尚活动的同一Session发起takeover，读取实际代次并提交；SSH仍为同一个连接/Shell。额外子场景先完成B Prepare，令A真实故障确定首因后再提交B；以及B已捕获old并提交、但old.end被暂停时，真实故障先固定原因，再放行old.end。
3. 旧连接物理故障通常无法再收到最终消息，不能要求taken_over/4001必达；检查服务端原因仍为NETWORK_ERROR，不在old.end或失效适配中覆写。

**严格断言：**故障前已收消息与服务端发送记录符合顺序，不伪造故障后的通知送达；旧端无法继续输入；B真实前缀/回放/后续输入正常且远端只执行B一次。新Binding身份/代次/状态/期限不被旧故障或迟到Detach改变，旧资源最终回收、slot一次归还、SSH保留。再用“taken_over先固定、真实故障后到”的对照子例验证首因不变；此对照不保证断网后的通知送达。

#### T3：Writer发送准入与提交竞争

分别以历史二进制块、实时块、普通status控制项作为下一项；成功前缀也执行同一准入规则。终止项另测，不把普通队列入队当作发送准入。

| 固定交错 | 同步点和调度 | 必须断言 |
|---|---|---|
| Writer先准入 | Writer已在c.mu+s.mu内选定并标记item，然后释放全部锁，在实际网络Write前暂停；放行B完整提交并完成旧终止原因登记，再恢复该Write | admit序号严格先于commit；只有该一个已准入普通item可在commit后完成，payload不变；实际线上接着taken_over/4001，不能再发第二个普通项。不是要求提交后的普通字节数绝对为0 |
| takeover先提交 | 暂停旧Writer于取得s.mu之前，允许B提交；再放行Writer，包含Writer已持c.mu但还没取得s.mu的交错 | 检查到失效，不从FIFO/快照/缓存新选取或标记项，commit之后旧普通admit计数增量0；等待c.mu的old.end最终进入或Writer先提交taken_over；无死锁 |
| 同时竞争s.mu | 两个任务在尚未持s.mu的独立屏障汇合，分别控制两种获锁次序，另做多轮自由竞争；不能让持s.mu的hook等待对方提交而人为死锁 | 每轮只有“admit先于commit并允许该项”或“commit先、无普通admit”两种序列；不存在检查旧active成功、commit插入、再选item的第三种序列 |

**共同断言：**同时记录发送准入序号和真实网络消息顺序，不能仅凭Write开始/返回时间推导准入；旧终止槽不受普通控制队列满影响、最多一次终止消息、仍只有一个应用Writer。锁外在途Write可被真实堵写以验证close_timeout强关；堵写子例允许通知失败，仍检查不再准入和完整资源回收。队列中旧SSH输入/resize不再执行，新端输入计数正常；新Binding身份/G/状态/期限稳定。记录旧slot在worker和Supervisor未完成前仍占用。终止路径与Writer都按c.mu→s.mu，不接受仅“无panic”作为通过。

#### T4：新旧Binding隔离及迟到回调

1. A→B成功接管，记录B的Binding引用、generation、CONNECTED、offline_expires_at为空和SSH远端输入基线；A正常场景已收到taken_over/4001。
2. 保存A的旧引用/回调，在B持续输入输出时依次及并发释放：旧Detach、Reader Done/迟到defer、Writer已准入Write完成或失败、重复Terminate/Close收尾、取消执行器、旧resize、旧输出通知。所有动作携带A固定Binding，禁止测试回调改从Session读取B再操作。
3. 追加B也普通断开后的子场景：记录其新离线期限，继续触发A迟到Detach/Close回调，证明不会重新计时或改变generation。随后用户正常attach得到C，再重复A/B旧回调，证明对象隔离可跨多代。
4. 追加“B提交但Connection.binding尚未登记时就被C接管/关闭”的交接子场景：每个成功结果都履行被替换对象的old.end责任；B登记拒绝或Writer准入失败后Detach自身，不遗漏context取消/资源释放。

**严格断言：**迟到旧事件不改变当前Binding引用、generation、CONNECTED或已记录的离线期限；旧输入/resize远端调用数不增加，当前端测试输入恰一次；旧Writer无新的普通准入、最终消息不串到新WS。每条连接的context、worker、Supervisor、快照和slot按所属对象一次收尾，不清共享Session缓存、不杀SSH；最终显式关闭测试Session后Lease/索引归零。此处“旧Close回调”指旧WS物理关闭/清理，**不包括用户显式DELETE或真实SSH故障的Session.Close**，后两者按C03/C04仍应合法关闭当前Session。

### 17.3 WebSocket集成

| ID | 真实HTTP/1.1 Upgrade、固定库与实际Session/SSH场景 | 验收 |
|---|---|---|
| W01 | A在线，B Prepare后不连接/首bind超时/非法offset/过期/权限错 | A仍可完成新输入，generation未变；B全资源回收；未提交凭证规则可核对 |
| W02 | A在线，B成功提交，持续SSH输出；A继续输入/resize | B收到正确前缀/回放/实时；A正常路径taken_over+4001，提交后未准入操作不进SSH；只有一个原Shell |
| W03 | 提交后拦截/阻断B的bind_success发送 | 记录服务端实际G+1，不恢复A；新Binding断开后仅离线；后续恢复按已知/未知代次分支 |
| W04 | A原始TCP四类半帧头、Writer真实堵写、Close握手堵塞、在途Ping | B成功不等待A；旧底层强关按总预算；取消Ping；worker/Supervisor完成后slot才归还 |
| W05 | 上限1无候选余量；上限2有旧端和候选且旧端关闭中 | 前者拒绝升级保持A；后者新端有效而A仍占slot，第三连接被拒绝；A回收后仅归还一次 |
| W06 | 高输出/小缓存/慢新端；前缀前终止；应用退出 | 单次回放/OUTPUT_GAP保持；终止可先于成功前缀；SSH与WS故障隔离；全部登记资源按退出预算收尾 |

使用实际`session.Manager`、`websocket.Service`、固定`coder/websocket v1.8.15`和真实TCP；半帧头确认服务端已读注入字节后开始计时。mock Reader/只看浏览器close不能代替此层。重用现有testutil与WS测试夹具，不添加测试服务依赖。

### 17.4 前端逻辑、组件与浏览器测试

| ID | 场景 | 必须断言 |
|---|---|---|
| F01 | 首次进入、重复刷新、切服务器/本地状态筛选、错误/空列表 | 查询次数符合D1；先前响应/旧finally不能覆盖；本地状态筛选不加API参数 |
| F02 | 列表卸载、隐藏/重新可见、登出、Abort后响应迟到 | 请求可取消，监听清理；无新任务/凭证；隐藏终端实例不销毁 |
| F03 | 列表状态过时、刚被关闭/接管、剩余0 | 详情/提交复查；不把列表CONNECTED当本设备在线，不在客户端到零DELETE |
| F04 | 首次takeover的HTTP/WS失败、成功后掉线、bind_success丢失 | 自动重试全部attach；G不被列表/详情悄悄更新；原预算耗尽后停止 |
| F05 | 无Pane重复点击，挂载尚未完成，多个行不同Session | 每ID一个待建记录/Pane/Client；不同Session可独立；确认前零Prepare/WS |
| F06 | 在线Pane从列表打开 | 只定位，不创建第二Client、不自我接管；其他Pane与连接继续 |
| F07 | 自动恢复处于drain、POST、握手、replay、retry_wait时准备接管 | stop同步封准入；旧完成后才新详情/确认/任务；旧回调/old finally不能污染新状态 |
| F08 | 接管详情失败、用户取消、Prepare失败、提交结果未知 | 原任务不复活；可重试失败仅原预算attach；最终失败可由用户重新查询；真正ended禁止恢复 |
| F09 | taken_over业务消息、裸4001、通知丢失1006、旧端休眠醒来 | 禁输入/自动抢回；通知丢失只原代次attach遇冲突；终止原因不被迟到close覆写 |
| F10 | 接管时关闭SSH/取消关闭确认、DELETE失败/响应丢失、代理返回同形JSON | 确认前不停止；确认后封接管、停任务并DELETE；成功不伪报CLOSED；当前链路除合法200外全部归unknown；失败查询/幂等重试，不自动恢复 |
| F11 | Prepare后登出、提交瞬间登出、退出失败/不确定/迟到AUTH_REQUIRED | 保留退出意图与重试，无凭证/WS/标签复活；其他登录Session权限正确 |
| F12 | 确认前离开列表、确认后关闭列表、关闭Pane、整个Home卸载 | 方案 B（S6 修订）：确认前离开列表**不取消**已归 Pane 的候选动作，候选 Pane 和确认状态保留/确认后继续Pane任务/dispose本地/停止全部；均无隐式Session DELETE。原“确认前离开列表取消未确认动作”已被替代 |
| F13 | 同实例processedOffset、新页面0、截断重建、隐藏Pane/焦点、历史提示 | 字节连续性/输入门禁/尺寸不变；恢复成功清当前提示、历史诊断明确；旧任务不抢焦点 |

F测试先使用实际类/SFC与可控fetch/WS/TerminalPort隔离异步交错。浏览器集成另列：

- **B01**：实际HomePage→会话管理→详情确认→真实WS→真实xterm，覆盖A/B/C/D，不使用未编译的伪组件。
- **B02**：两个独立浏览器登录上下文A/B、第三竞争者C，同一Session接管、同代次竞争；记录Session ID、服务端代次和原SSH任务连续性。
- **B03**：A离线/休眠后B接管，A回网不抢回；同Cookie顶层页退出与独立登录退出分别验证，不能混作一类。
- **B04**：重复点击/旧请求乱序、确认取消、列表/Pane/页面退出、登出失败重试；检查无重复xterm实例或Client、无未处理Promise。
- **B05**：中文/ANSI/交互程序、持续输出、回放门禁、resize、两步人工attach、UI提示残留修复回归；窄屏只记视口测试，真设备另列。

## 18. 失败注入、资源观察与验证命令

### 18.1 注入位置与证据要求

- 凭证：签发后等待TTL、快照之后清除/过期、错误登录/Session、容量满。复用已有提交hook，不在Session锁里等待另一个必须取得同锁的任务造成测试自死锁。
- 操作门：复用operationAdmitted同步点，让真实SSH调用阻塞；分别释放门与让候选超时。必须统计实际远端调用次数，证明旧未准入项没有副作用。
- WS：真实Listener包装记录半帧头读取；读端停收导致TCP堵写；记录一次实际write失败；在提交后成功前缀前断开；不把“客户端没看到成功”当服务端未提交。
- 登录：真实Logout短事务回滚/提交不确定，以及HTTP响应丢失；后两者不能混为同一种服务端状态。
- 前端：可控fetch在Abort后仍resolve/reject、可控旧socket handler、write callback延迟、task finally迟到、nextTick后Pane销毁；另模拟代理返回同形JSON业务码、非JSON 5xx和响应丢失，断言当前全部归unknown且不产生failed。测试实际身份检查，而不是只断言调用了abort。
- 资源：每轮记录Manager活动数、服务器Lease、WS Count、worker/supervisor/done、凭证数量/TTL、终端/监听器实例数；协程/内存观察允许运行时噪声，但必须有实际完成信号证明回收。
- 重复100轮接管/半帧头/阻塞/关闭，末轮与应用Stop竞争；旧、新slot分别核对，不仅等待总Count最后归零。

正常接管通知测试排除独立故障，必须看到taken_over/4001；故障竞争测试接受合法第一终止原因，同时严格检查失效权限、有限恢复与资源回收。不能借“通知尽力”跳过正常消息实现，也不能强行要求故障时每条通知必达。

### 18.2 获准编码后的命令

全部在WSL Ubuntu、对应Linux目录执行。前端package.json已确认`test`和`build`存在，build包含typecheck；不虚构其他npm脚本。

```bash
# 工作目录 /home/x/workspaces/web-ssh/backend
# 定向测试名以S1–S7实际落地名称为准，再执行完整检查
go vet ./...
go test ./...
go test -race ./...
go build ./...

# 工作目录 /home/x/workspaces/web-ssh/frontend
npm test
npm run build

# 工作目录 /home/x/workspaces/web-ssh/backend
# 在前端最终构建后确认嵌入应用可构建
go build -o /tmp/codex/web-ssh-stage5/server ./cmd/server

# 工作目录 /home/x/workspaces/web-ssh
git diff --check
git status --short
git diff --stat
git diff --cached --name-only
```

临时输出目录先在授权环境准备；命令是未来步骤，不表示本轮已经创建或执行。必要的重复race限定新增竞争测试，全部通过且代码未再变化时不反复全量运行。浏览器脚本需启动实际应用、隔离SQLite与SSH测试环境，测试后停止临时进程/端口；不因历史`/tmp/codex/`工具曾存在就假设现在可用，不无故安装替代环境。

## 19. Docker、Nginx Proxy Manager与真实SSH验收

未来编码完成并获准部署后，由用户/ops在隔离或明确授权的真实环境执行。此处NPM指Nginx Proxy Manager；前端npm test/build不是代理验收。第四阶段用户确认可作为部署基础，但第五阶段必须按新增路径重新验证。

| 步骤 | 操作与观察 | 通过标准/记录 |
|---|---|---|
| E01 部署基线 | 使用现有Dockerfile/Compose、持久化目录、HTTPS public_origin、独立秘密；记录镜像/代码版本、Docker/代理/OpenSSH/浏览器版本 | 无迁移/新增配置需求；健康检查、登录、/me和WSS正常；Compose停止宽限大于应用预算 |
| E02 活动发现 | A创建真实Session并启动持续可观察任务，关闭本地标签；B独立登录，从列表按服务器筛选并读详情 | 同一Session ID/创建目标；列表来自当前运行事实；关闭标签不杀SSH；名称编辑不改变旧快照 |
| E03 在线接管 | A在线输入，B详情→明确确认→takeover，期间持续输出 | Prepare阶段A仍可用；提交后仅B有新输入权；正常A显示被接管且不恢复；原Shell/进程继续，不重建SSH |
| E04 离线接管与旧端回网 | A断网或后台休眠，B接管；恢复A网络/前台 | B正常；A仅原代次attach并停止，不自动抢回；新设备xterm从0恢复保留后缀，必要截断提示 |
| E05 同页协调 | B已有在线/恢复中/停止Pane时从列表选择；重复点接管；取消确认 | 按已裁决D2执行，只有一个Pane/Client；无隐藏第二任务；取消后的状态可解释 |
| E06 竞争与失败 | A/B/C同G接管；接管时DELETE、远端exit、独立登录登出；受控阻断新前缀/旧通知 | 合法失败码集合、不双操作、不回滚旧端；DELETE与登出语义独立；响应丢失可显式查询 |
| E07 输出与终端 | 中文/ANSI、vim/top、stty size、大段输出与粘贴、小缓存和慢浏览器 | 回放/输入门禁、尺寸、有限恢复不变；被接管端输入不补发；不声称后缀完整恢复全屏画面 |
| E08 关闭与退出 | 新端提交后关网页、关列表、关Pane、logout、SIGTERM/Compose stop分别执行 | 对应范围正确；关闭浏览器不直接DELETE；服务端退出收尾在预算内，重启后旧Session不能恢复 |
| E09 长时与真设备 | 两设备不同网络，后台挂起/唤醒、网络切换，重复接管并观察资源 | 记录实际设备与时长；无持续抢占/资源增长；未测IME/真机等保留待验，不把桌面窄屏当真机 |

记录每项操作者、版本、时间、配置差异、步骤、实际结果、截图/脱敏日志、失败原因及重测结果。BIND_CONFLICT与INVALID_CREDENTIAL均可为同代次竞争合法结果，不能为对齐文案改服务端契约。生产环境不随意跑破坏性负载；失败注入优先隔离环境，用户真实任务不作为测试耗材。

## 20. 验收标准、阶段完成条件与交付报告

### 20.1 编码启动门槛

- [ ] 第二轮修订经人工复审；D1–D3=A已有明确用户裁决记录，不能将该裁决当作编码授权。
- [ ] S7-0 已冻结：D1、D1-A～D1-E、D2-1、D2-2、D3-1、D4-1、D5-1、D5-2 均有正式记录，见 §21.6。
- [ ] S7-1 尚未获得编码授权；S7-0 的文档冻结不等于 S7-1 授权。
- [ ] 明确编码授权、当前基线及文件保护完成；若仅授权某一阶段则只执行该子集。
- [ ] 无新的冻结设计冲突；一旦发现实质冲突标记BLOCKER并暂停受影响部分，不改冻结文档绕过。

### 20.2 第五阶段功能和验证完成门槛

- [ ] GET列表严格契约、Manager权威、排序/筛选/空集合/CLOSED/快照并发和敏感字段全部通过。
- [ ] 列表独立于标签；刷新、取消、迟到响应、卸载/登出、可见性按已裁决方案可测，无后台takeover。
- [ ] Prepare不替换，提交前失败原端保持，同G唯一成功，凭证mode/归属/TTL/容量与消费正确。
- [ ] 新旧Binding原子替换，提交不提前cancel；统一终止先确定原因再取消，Reader/执行器失效退出不抢占；T1–T4实测通过。旧操作准入即时失效，已准入SSH调用遵守操作门，提交后失败绝不恢复旧端。
- [ ] Writer在Connection.mu→Session.mu→必要时Buffer.mu边界内检查并选择/标记单项；与提交在同一Session锁上线性化；锁外Write/Close，提交后无新的旧端普通准入。
- [ ] 正常taken_over消息/4001送达；丢失通知仍因代次保护不能抢回；INPUT_BACKPRESSURE、OUTPUT_GAP规则不变。
- [ ] 提交到Connection登记窗口、旧Reader/Writer/Ping/Close阻塞和旧回调均有真实网络/竞态证据；旧slot不提前归还。
- [ ] A–E全部覆盖；同页重复点击/已有Pane/恢复中接管不创建第二Client或恢复链；失败取消不复活旧任务。
- [ ] 关闭、登出、列表退出、Pane关闭与网页关闭区分清楚；未完成退出始终保留意图和重试。
- [ ] 第四阶段attach、两步人工恢复、所有重试预算、偏移/截断/drain、网络UI提示修复回归通过。
- [ ] Go普通/race/vet/build、前端实际测试与构建、真实WS/浏览器测试有结果；不能将单元测试当接管全验收。
- [ ] 第19节真实Docker/NPM/SSH与跨设备验收关键路径有记录；缺环境只能交付“代码/本地验证完成，真实环境待验”，不能宣称本阶段全部完成。
- [ ] 无新增表/持久化/config/dependency，无秘密泄漏、无未说明范围变更；用户文件保留。

### 20.3 未来实施报告结构

列出实现文件与职责、冻结条款对应、裁决落实、实际命令/环境/结果、故障注入与真实验收分别的证据、失败修复、未验事项及影响、资源基线、分支/HEAD/工作区/暂存区。报告需区分“服务端已提交”“前端收到完整前缀”“回放已处理可输入”“旧WS已物理回收”四个时刻。

不得把本计划的测试勾选为已完成，不沿用第四阶段历史测试次数作为第五阶段统计。完成后停止并等待下一步授权；提交、推送、部署及第六阶段不是本计划自动授权。

## 21. 用户裁决、第二轮修订与复审边界

### 21.1 已冻结，不再征求重新设计

活动Manager权威、GET列表形状与排序、四活动态/CLOSED排除、SessionView、凭证API、attach/takeover区分、Prepare不撤销、唯一提交、提交前保持/提交后不回滚、锁序/操作门、旧回调隔离、taken_over/4001、OUTPUT_GAP/4002、slot实际回收、偏移/一次回放与有限attach预算、两步人工恢复、logoutPending/stop/dispose及独立credential_timeout均已有权威依据。

**BLOCKER-PROTOCOL、BLOCKER-HTTP已解决，不重新引入。** 本次未发现需要修改冻结文档的新BLOCKER。当前代码未实现takeover不构成设计冲突；新增的内部mode和taken_over DTO只是落实已定义协议。

### 21.2 已确认的用户裁决：D1=A、D2=A、D3=A

确认依据：用户在本次“第五阶段实施计划第二轮修订”要求§五第1项明确指示：“D1、D2、D3 全部采用 A 方案，正式作为第五阶段已确认的用户裁决。”以下仅据这一明确授权登记，不将其他内部接口建议或未经确认的推荐升级为用户裁决。

| ID / 状态 | 已确认A方案 | 未采用的原B方案及实施落点 |
|---|---|---|
| D1 `用户已确认：A` | 进入、手动、操作结果、重新可见时按需刷新；序号+Abort；保留明确标注旧快照；GET仅主动/生命周期取消；无轮询/新配置；状态本地过滤四态、剩余时间先显示查询值 | 不采用仅进入/手动刷新B；落实§6/图1/U10/F01–F03、S4 |
| D2 `用户已确认：A` | 在线已有Pane只定位；恢复中/停止的原Pane显式停止准备→新详情→确认，复用原Client/tracker；取消/最终失败不自动复活原任务；协议规定的有限attach重试保持 | 不采用在线直接主动重连B，不建立第二Pane/Client；落实§8/图3/§11/F06–F08、S6 |
| D3 `用户已确认：A` | 建立唯一不自动连接的候选Pane承载详情/确认；确认后任务归Pane，成功成为正常标签，失败保留原因；关闭整个HomePage时stop/dispose。S6 修订（方案 B）：原“离开列表取消未确认动作”已被替代——Pane ready 后候选动作即归 Pane，离开列表不取消 | 不采用管理视图先确认、再挂载Pane的B；原“未启动候选取消可移除”已被替代——取消后候选Pane保留，只由用户关闭（U4）；原有Pane不因取消删除；落实§8/图2/§12.1/F05/F12、S6/S7 |

当前没有D1–D3待裁决项。上述选择补齐产品交互，不修改服务端冻结契约，也不等于授权编码。具体CSS、类名和内部函数名仍为常规实现细节；§7内部接口及并发伪代码作为本轮修订提交人工复审。

### 21.3 本次补齐的实施细节

补齐逐对象一致而非全局事务列表快照、查询序号/Abort/finally保护、标签ID预占与挂载入口、同一Client初始mode而后续attach、原恢复任务停止屏障、查询候选与人工attach候选隔离、提交后Reader登记窗口、旧Writer失效准入与锁外通知、关闭/登出/列表卸载所有权、14项竞争处理、8张时序图、9个审查步骤及分层验收矩阵。D1–D3按本轮明确用户裁决采用A；第二轮新增五个终止时刻的分离、失效退出处置、Writer双锁发送准入与T1–T4固定交错，全部仍待实现验证。

如后续确需修订其他文档，只提出建议并单独获得授权：可补充第四阶段报告的真实环境验收与UI修复记录，更新历史“尚未实现”措辞的状态说明；本轮未修改它们。不得以本计划覆盖冻结协议或新增未批准API。

### 21.4 本次交付结论

1. 第五阶段详细计划已覆盖任务要求，结构、实现入口、并发处理、文件职责、测试和真实验收均可审查。
2. 冻结Binding事务、锁序、API/协议字段、错误码/关闭码和第四阶段恢复/登出语义保持。修正的是原计划提前取消及发送准入未明确同步边界的问题，不以修改冻结设计绕过。
3. D1、D2、D3=A已确认，S7-0 的 D1-A～D1-E、D2-1、D2-2、D3-1、D4-1、D5-1、D5-2 已正式登记；本次未发现其他必须用户裁决的设计空白或冻结文档冲突BLOCKER。内部失效处置/发送准入接口及交接窗口已给出设计，正确性仍须人工复审及未来T1–T4实测，不能宣称当前业务代码已修复。
4. S7-0 完成后停止，等待人工复审与 S7-1 的单独编码授权；不再次要求既有裁决，也不因 S7-0 完成自行进入编码。S0–S8总体边界和依赖顺序保持，仅细化S2/S3内部职责、接口与测试。
5. S7-0 已冻结后，S7 的第一项业务改动应为 S7-1：新增 `frontend/src/session/close.ts`，把协调器、DELETE/详情状态机与 Pane/HomePage 接线作为一个完整强耦合单元落地；仍须用户单独授权。
6. 最终定点修订确认：当前链路除合法 200 外的 DELETE 错误归 `unknown`，`failed` 暂不可达；`idle + CLOSING` 保持 idle 但拒绝确认、零凭证/WS/takeover 且不产生 ended。两项均不改变 D1–D5 的既有裁决。

本次仅修订指定计划文档，完成后停止等待人工复审；不修改业务代码/冻结文档，不执行commit、push或破坏性Git操作。

### 21.5 S6 设计修订：方案 B 与 U1–U9 用户裁决（2026-11-18）

术语说明：此处“方案 B”指 S6 候选 Pane 所有权方案，与 §21.2 表中各决策“未采用的原 B 方案”无关；D1、D2、D3 仍为 A。

**方案 B（已冻结）**：SessionManager 只负责列表展示、筛选、刷新和触发打开事件；HomePage 同步预占 Session ID，创建并登记候选 Pane；Pane mount 不自动连接、不申请凭证、不创建 WebSocket；Pane 初始化成功并 emit ready 后，候选接管流程归 Pane；管理页面卸载、SessionManager 卸载、列表刷新不取消已移交给 Pane 的动作；Pane 关闭、HomePage 卸载、登出、身份失效时使动作失效、取消请求、停止任务并隔离迟到回调；用户确认后调用 S5 已有的 `connectTakeover()`，成功后继续使用同一 Pane、Client 和 Tracker，后续自动恢复遵循 S5 的 attach 规则。

**被替代的旧规则**：§8.1 页面级共同互斥意图的实现方式、§8.2 第 1 步的 tabs/panes/待创建记录、§8.3 图 2 的“先 GET 后 stop”顺序、§11.3 页面级意图边界、§12.1“离开管理页取消未确认动作并移除未启动候选”、§15 S6 行的`recovery.ts`修改和`session/takeover.ts`、S7 行的`session/takeover.ts`、§16 两行、F12、§21.2 D3 行的相关表述。各处原文保留并标注，现行规则以方案 B 和 `doc/stage-5-s6-design-plan.md` 为准。

| 编号 | 用户裁决 |
|---|---|
| U1 | dispose 失败后保留 Pane 记录和 Session ID 预占，不提供原地重试；提示用户刷新页面重新开始 |
| U2 | 候选 Pane ready 后自动 GET 详情并显示确认框；GET 不申请凭证、不创建 WebSocket |
| U3 | 取消 takeover 后保留原有人工两步 attach 能力，但不自动连接 |
| U4 | 取消 takeover 后保留候选 Pane，不自动关闭 |
| U5 | 初始化失败不提供原地重试；关闭成功后重新打开创建新实例 |
| U6 | S6 不修改 auth/router，记录现有网络错误与 identity 清理行为的风险，留待独立处理 |
| U7 | 修订本计划中与方案 B 冲突的旧规则（即本节及上述标注） |
| U8 | 使用独立的 `frontend/src/terminal/takeover.ts` |
| U9 | 登出成功优先，本地清理失败不阻止退出，但必须记录和反馈失败 |

本次修订只涉及前端 S6/S7 相关描述；后端、协议、错误码、S5 连接契约、S0–S8 依赖顺序不变。本修订不构成编码授权。

### 21.6 S7-0 设计裁决与实施计划冻结（2026-11-19）

**授权状态**：S7-0 已获得用户授权并完成；S7-1 尚未获得编码授权。本节与 `doc/stage-5-s7-design-decision-proposal.md` 共同构成 S7 后续实施的冻结依据。

#### 21.6.1 正式裁决

| 编号 | 裁决 | 约束摘要 |
|---|---|---|
| D1 | 接受 | HomePage 持有按 Session ID 管理的 `CloseCoordinator`；Pane 只负责关闭 UI；Pane 销毁只退订、不 abort；Pane 与无 Pane 共用状态机；非 idle 禁止 takeover/手动恢复；接受旧 DELETE 与新 takeover 竞态；新增 `frontend/src/session/close.ts` |
| D1-A | 接受 | 详情 GET 只更新服务端状态，不解除 `requested/unknown/failed`；`unknown/failed + CLOSING` 升格为 `requested`；`idle + CLOSING` 保持 idle，但拒绝确认且不产生 ended |
| D1-B | 接受 | `ended` 只接受详情 `SESSION_NOT_FOUND` 或服务端 `closed` 控制消息；不表示服务端资源已回收完成 |
| D1-C | 接受 | 同步建立 `pending` → 同步执行本地 stop 失效部分 → 异步 DELETE；同步抛错或 reject 记 `localStopFailed`，不恢复准入 |
| D1-D | 接受 | DELETE 200 → `requested`；网络/超时/abort/响应丢失/网关/非 JSON/5xx/未知业务码 → `unknown`；仅可信来源且可证明发生在 `Sessions.Close` 之前的拒绝 → `failed`。当前链路无来源证据，除合法 200 外的响应暂归 `unknown`，`failed` 暂不可达 |
| D1-E | 接受 | 本地 abort 只停止等待；不新增本地关闭超时；DELETE 200 不等于 `CLOSED`；`failed` 保留关闭意图 |
| D2-1 | 接受 | SessionManager 增加行级关闭；CONNECTING/CLOSING 允许；重复关闭复用同一协调器流程；SessionManager 不直接 DELETE、不维护关闭状态 |
| D2-2 | 接受 | 非 idle 时打开只读关闭状态 Pane；不申请凭证、不创建 WebSocket、不执行 takeover；关闭只读 Pane 不影响协调器任务 |
| D3-1 | 接受 | `logoutPending` 不覆盖本地 close；`auth.ts` 不改；signOut 同步 `manager.invalidate()`，从 `records[].handle` 同步 stop 并取得共享 stop Promise，再 dispose 协调器，最后立即 logout；成功 close/导航，失败停留 |
| D4-1 | 接受 | SessionManager 直接读取 `logoutPending`；`queueMicrotask` 合并刷新；ended tombstone 过滤旧列表行；不改变协调器关闭状态所有权 |
| D5-1 | 接受 | DELETE 错误显示在关闭确认框并保留状态；localStop 失败独立提示，不混淆两类失败 |
| D5-2 | 接受 | `router.replace()` reject 记录日志并继续写清理反馈；不改变已成功的 logout，不重新登出 |

#### 21.6.2 冻结阶段计划

| 阶段 | 实施目标 | 允许修改 | 明确禁止 | 前置依赖 | 关键约束 | 测试与验收 | 完成边界 |
|---|---|---|---|---|---|---|---|
| S7-0 | 落地裁决、冻结计划 | S7 设计提案与本实施计划 | 生产代码、测试、后端、API、配置、S6 冻结文档 | S7-0 用户授权 | 12 项裁决逐项登记；阶段边界、文件范围、测试与验收可执行 | 全文交叉检查、路径/编号/引用检查、S7 提案 §20 与本节一致、`git diff --check` | 停止并等待用户审查，再决定是否授权 S7-1 |
| S7-1 | 统一关闭协调器、DELETE 状态机与详情查询 | 新增 `frontend/src/session/close.ts`；修改 `api/sessions.ts`、`TerminalPane.vue`、`HomePage.vue` 及直接相关测试 | 拆成先 Pane 后 Coordinator；让 Pane/SessionManager 持有 DELETE；修改 S5 Client/Recovery 语义；修改 auth/router/后端；新增响应来源字段/签名/请求 ID 或代理配置 | S7-0 完成；S7-1 单独授权 | 协调器按 Session ID 唯一；状态机、详情判定、双重准入、DELETE 分类、epoch/queryEpoch、dispose、本地 stop 注册必须同一单元落地；当前链路除合法 200 外一律 unknown，不得伪造 failed；Pane `close()` 只退订 | S7-C01–C07、C10、C15–C30；DELETE 200/不确定/abort/代理同形 JSON；详情不解除关闭意图；`idle + CLOSING` 零凭证/WS/takeover；ended 只认服务端证据；关闭标签零 DELETE；`npm test` 相关文件 | 停止并等待用户验收 S7-1，不自动进入 S7-2～S7-5 |
| S7-2 | SessionManager 行级关闭入口 | `SessionManager.vue`、`HomePage.vue`、`session-list.test.mjs` | 新增第二套关闭状态机；SessionManager 直接 DELETE；修改 Pane 候选/takeover 状态机 | S7-1 完成 | 只 emit `close`；HomePage 路由到同一协调器；CONNECTING/CLOSING 允许；重复点击幂等 | 行按钮 emit；有 Pane 时 HomePage 零直接 DELETE；无 Pane 时恰好一次 DELETE；取消零 DELETE；S7-C08/C09 | 停止并等待用户验收 S7-2 |
| S7-3 | 列表同步失效与 changed 合并 | `HomePage.vue`、`SessionManager.vue`、`session-list.test.mjs`；`session/list.ts` 仅在真实缺陷时最小修改 | 绕过协调器改变关闭状态；重写既有 sequence/Abort；引入定时器/轮询 | S7-1 完成 | signOut 同步 `manager.invalidate()`；`paneChanged` 守卫；`queueMicrotask` 合并；`isAllowed` 同步检查 `logoutPending`；ended tombstone | 登出同步零新 GET；paneChanged 零 GET；同批 changed 一次 GET；旧响应不覆盖新快照；S7-C22 | 停止并等待用户验收 S7-3 |
| S7-4 | 完整登出语义 | `HomePage.vue`、`session-list.test.mjs`、`auth.test.mjs`（仅扩展测试） | 修改 `auth.ts`/`router`；改变 S6 stop/logout/close 顺序；在 logout 前等待 stop drain | S7-1、S7-3 完成；D3-1 已接受 | 同步 `manager.invalidate()`；同步 `records[].handle.stop()` 形成共享 stop barrier；dispose 协调器；logout 与 stop drain 并行；失败停留、成功 close/导航；router reject 记录并继续反馈 | logout 成功/失败/不确定；stop/close reject 与同步 throw；router.replace reject；多 Pane 混合状态；S7-C14 | 停止并等待用户验收 S7-4 |
| S7-5 | 页面退出与迟到回调隔离 | `TerminalPane.vue`、`HomePage.vue`、对应测试 | 因 Pane 销毁 abort/清除协调器任务；修改 `session/close.ts` 状态机；修改 S5 Client/Recovery 语义 | S7-1 完成 | Pane 退订并注销本地 stop；协调器 dispose/epoch/queryEpoch 隔离迟到响应；页面重进按 S6 候选 Pane 流程确认，不自动恢复 | 关闭标签/卸载时 DELETE 继续；迟到 200/错误不更新 UI；takeover loading/recovery retry_wait 卸载；零新 fetch/WS；S7-C11/C12/C13/C15/C21 | 停止并等待用户验收 S7-5 |
| S7-6 | 自动化测试、浏览器验收与报告 | 测试文件、`doc/测试报告/stage-5-s7-*-report.md`、日志/截图目录 | 未经单独授权修改生产代码；伪造真实环境结论；部署 | S7-1～S7-5 完成 | 分层覆盖状态转换、并发竞争、资源回收、错误提示和真实浏览器；Mock 与真实环境分开记录 | `npm test`、`npm run typecheck`、`npm run build`、`git diff --check`；浏览器覆盖行级关闭、失败/取消、详情刷新、登出、Pane 重开 | 停止并等待用户验收 S7-6 与 S7 全阶段，不进入 S8、不部署 |

#### 21.6.3 关键边界

- **所有权**：HomePage 的 `CloseCoordinator` 是关闭状态与 DELETE 的唯一所有者；Pane 只通过 `CloseHandle` 读取/操作；Pane 销毁只退订和注销本地 stop，不 abort、不清除协调器任务。
- **状态机**：`idle / pending / requested / unknown / failed / ended` 只能向更强的关闭意图转换；详情 GET 不得把非 idle 改回 idle；`ended` 只由 `SESSION_NOT_FOUND` 或服务端 `closed` 产生。
- **双重准入**：`closeState === 'idle'` 只通过本地关闭意图层，不构成接管许可；还必须满足 S6 详情状态 `CONNECTED/DISCONNECTED`。`idle + CLOSING` 保持 idle，但拒绝确认、零凭证、零 WebSocket、零 takeover，且不产生 ended。
- **DELETE 来源**：当前后端没有请求 ID、签名或其他来源标识，仓库也没有可审计的 NPM/Nginx 配置；代理可返回同形 JSON。除合法 200 外的 DELETE 响应暂归 `unknown`，`failed` 保留但暂不可达；不得用状态码、JSON 格式或业务码单独推断 `failed`。
- **同步顺序**：`requestClose` 同步建立 `pending` 并执行本地 stop 的同步部分，之后才发 DELETE；禁止用 Promise 调度延迟这两步。signOut 中 watcher 与 HomePage `handle.stop()` 必须在同一同步调用栈；共享 Promise 由 `TerminalClient.stopping` 提供，`manager.invalidate()` 必须在 logout 前完成。
- **S6 兼容**：候选 Pane 所有权方案 B、用户确认前零凭证/零 WebSocket、健康 Session 只定位、恢复中/已停止 Session 必须显式接管、S5 连接/ generation / takeover / attach / BIND_CONFLICT 语义、signOut 的 stop barrier 与 logout 并行顺序均不改变。
- **已接受残余风险**：页面刷新后不持久化 unknown；旧 DELETE 可能在新页面 takeover 成功后到达并关闭新连接；DELETE/logout 无本地超时。上述风险按 D1/D1-E 保留，不虚构客户端彻底解决方案。

#### 21.6.4 未决项

当前没有未裁决的 BLOCKER 或 MAJOR。后续若发现新的冻结设计冲突，应停止受影响阶段并报告，不得自行修改 S6 冻结文档或扩大 S7 授权范围。
