# Web SSH 第五阶段 S7-6 最终集成验收与边界测试报告

日期：2026-11-19（Asia/Shanghai）。结论：**有条件通过**。

当前可达前端路径未发现阻塞缺陷：全量 Node 测试 **309/309**，真实 Chromium 浏览器组合测试 **7/7**，typecheck、build、diff check 通过。保留两个非阻塞审查项：S7-C29 的 `failed` 行在冻结的当前链路不可达，未执行；首次 takeover 失败而后续 attach 成功时的结果提示存在 F13/S6 文字语义歧义，详见第六节。不能将本报告解释为第五阶段整体、真实后端/SSH 或部署验收通过。

## 1. 当前基线、范围与修改

开始与结束基线均为分支 `S6`、HEAD `c4a577a`（提交标题 `S7-4&5`）。开始时已有用户修改 `luan-qi-ba-zao.txt` 和未跟踪 `web-ssh-c4a577a3.zip`，本任务未编辑这两个文件。

实际核对了指定的两份冻结文档、HomePage、SessionManager、TerminalPane、Client、Recovery、CloseCoordinator、两份指定组件测试，并补查了 `session-close.test.mjs`、`SessionTakeover`、auth/router 和 S6 冻结计划。文档开头“尚未授权 S7-1”的文字属于 S7-0 历史快照；本次按用户明确的 S7-1～S7-5 已完成、仅授权 S7-6 测试的范围执行，未改写文档。

| 文件 | 修改目的 |
|---|---|
| `frontend/tests/session-close.test.mjs` | 追加详情矩阵、ended 抢占旧请求的组合测试；加强原 C27 的 dispose 后零回调、零发布断言 |
| `frontend/tests/terminal-pane.test.mjs` | 复用原 SFC 编译器、renderer、fetch/Socket/Terminal/Promise 辅助函数，追加 stop/close/unmount、迟到 DELETE、候选详情矩阵、C23 完整同步顺序、C26 真实 Pane 关闭后重开测试 |
| `frontend/tests/browser/stage-5-s7-6.mjs` | 独立浏览器验收入口：实际编译的 HomePage、SessionManager、TerminalPane、Modal、router、auth、Client/Recovery/Coordinator 和 xterm；HTTP 与 WebSocket 对端使用 Playwright 夹具 |
| 本报告 | 冻结要求、分层证据、缺口与风险 |
| `doc/测试报告/stage-5-s7-6-logs/` | 原始日志、命令退出码、浏览器请求/状态记录、10 张最终截图，以及单独归档的测试夹具调试记录 |

**没有修改生产代码、API、后端、认证、路由、配置、协议、依赖锁文件或冻结文档。没有删除或弱化原有断言。** 没有 commit、push、merge、rebase、部署或重启现有服务。只启动并清理了本次脚本自己的临时 Vite/Chromium 测试进程。

## 2. 冻结要求与依赖核对

权威范围：`doc/stage-5-s7-design-decision-proposal.md` §12 S7-6、§11.3、§15.2；`doc/stage-5-implementation-plan.md` §21.6.2、§17.4。

依赖关系为 S7-1 提供统一关闭状态机和所有权；S7-2 提供行级意图入口；S7-3 提供同步失效、合并刷新和 tombstone；S7-4 在 S7-1/S7-3 上建立 stop/logout/close 顺序；S7-5 隔离卸载与迟到结果；本次组合验证这些行为，不改变前序契约。

| S7-6 冻结要求 | 实际证据与结果 |
|---|---|
| 单元/组件/浏览器分层 | Node 测试实际调用状态机与实际 SFC；浏览器运行真实 Vue/router/xterm。各层替身边界分别记录，通过 |
| Pane 销毁、重开及关闭任务所有权 | C01–C07、C15、C26；浏览器 B01/B02。关闭标签不取消协调器 DELETE，非 idle 重开不 prepare，通过 |
| 页面刷新、详情失败、CLOSING 双重准入 | C11–C13、C29 当前可达行；浏览器 B04/B05/B07。没有确认前凭证或 WS，通过；failed 行见缺口 |
| 登出与关闭并发、失败重试 | C14、S7-4 既有测试及浏览器 B03。manager invalidate → records stop barrier → coordinator dispose → logout；logout 与 drain 并行，成功后等待 barrier 再 close/导航，失败停留，通过 |
| 详情查询与 DELETE 重试并发 | C07/C18/C22、新增 ended 抢占测试；旧响应不改 message/updatedAt、不增加发布，通过 |
| 浏览器至少覆盖行级关闭、DELETE 失败/取消、详情刷新、登出失败/成功、列表退出、Pane 销毁后重开 | 浏览器 B01–B05/B07，全部通过；失败和取消使用有意注入的响应/Abort |
| F10：关闭、取消确认、DELETE 未知、代理同形 JSON | 既有组件/协调器分类测试及浏览器 B01，通过当前链路所有错误归 unknown 的路径 |
| F11：登出期间停止新准入 | 组件测试及真实 router 的浏览器 B03，通过前端子集；独立后端登录权限不在本次证据内 |
| F12：候选已归 Pane 时离开列表继续、关闭 Pane/整个页面各自清理 | 浏览器 B02/B05/B07 及既有 loading/retry_wait 卸载测试，通过 |
| F13：输出、历史提示、尺寸与旧任务隔离的 S7 回归子集 | 既有 tracker/client/1006 回归及浏览器 B06 输出、resize 通过；首次 takeover 结果提示歧义未作无条件通过，见第六节 |
| B01–B05 的 S7 子集 | 完成七个浏览器组合场景；真实后端 WS/SSH、多独立登录上下文、断网休眠与交互程序未执行，不声称完整覆盖 B01–B05 |
| npm test/typecheck/build/diff check；报告与截图 | 已执行，见第五节；截图与场景对应 |
| 完成后停止 | 本报告交付后等待人工审查，不进入 S8 |

### S7-C01–C30 对照

下表的“通过”限所列层级。U=实际协调器/类+可控请求；P=实际 Pane SFC+浏览器/xterm 替身；H=实际 HomePage/SessionManager SFC（既有 HomePage 测试的子组件为替身）；B=真实浏览器内的实际组件，API/WS 对端为夹具。

| 编号 | 核对内容 | 证据与结果 |
|---|---|---|
| C01 | 标签关闭不 DELETE、不 ended，Pane 退订不取消关闭任务 | U/P、B01/B06，通过 |
| C02 | pending 取消等待 → unknown，销毁后保留，迟到 200 无覆盖 | U/P、B01，通过 |
| C03 | 同 Session 非 idle 重开只读 Pane | H/P、B01，通过 |
| C04 | unknown 新 Pane 不 recover/prepare | P、B01，通过 |
| C05 | unknown 重试只有唯一有效 DELETE | U、B01，通过 |
| C06 | 旧 Pane 销毁后迟到 DELETE 仍可更新协调器，不通知旧 Pane | U/P 新增 accepted/lost 两路径，通过 |
| C07 | 旧响应 epoch 隔离 | U 新增 ended 抢占旧 GET/DELETE，通过 |
| C08 | 行级与 Pane 关闭竞争去重 | 既有 H（同 Session 一个 DELETE）、B02 实际行级关闭链，通过 |
| C09 | 无 Pane 关闭与创建竞争 | H/P、B01，pending 下新 Pane 零详情/凭证/WS，通过 |
| C10 | 两个 Session 关闭互不影响 | U/H、B02，每 ID 一个 DELETE，通过 |
| C11 | HomePage 卸载时在途 DELETE、销毁后零新请求 | H、B07；不能据 abort 宣称撤销服务端关闭，通过前端隔离 |
| C12 | 新页面 idle + CLOSING 不确认、不 ended | P、B04/B07，零凭证/WS，通过 |
| C13 | 新页面详情网络错误保留候选、不确认 | P、B04，通过 |
| C14 | logout 与 pending DELETE；完整 stop barrier 顺序和共享 Promise | H/P、B03，通过 |
| C15 | dispose/close/unmount 幂等，单次资源释放 | U/H/P 新增交错测试，通过 |
| C16 | requested + CONNECTED/DISCONNECTED 不回 idle | U、既有 P 准入测试，通过 |
| C17 | unknown + CLOSING → requested | U、B01，通过 |
| C18 | 查询在途时 retryClose；GET abort/旧结果丢弃 | U 新增三方竞争，通过 |
| C19 | recovery retry_wait 关闭后停止重试 | 既有实际 P+Client 与可控计时器，通过 |
| C20 | localStop 同步 throw 后继续 DELETE | U/H 既有测试，通过 |
| C21 | init_failed/dispose_failed 仍参与登出；远端 ended 证据不混用 | H/P 既有测试，通过其模拟层级 |
| C22 | 最后一次 refresh 胜出；旧 message/updatedAt/订阅不变化；tombstone 阻旧行 | U/H、B01，通过 |
| C23 | takeover.invalidate、recovery.stop、client.stop 同步部分均先于 DELETE | 新增 P 精确调用顺序断言，通过 |
| C24 | localStop 同步异常不恢复准入 | U 既有测试，通过 |
| C25 | localStop Promise reject 保留关闭状态、独立失败标记 | U 既有测试，通过 |
| C26 | 旧 Pane 真正 closed 后注册新 Pane，旧注销不删除新 stop | 新增 P 实际双次挂载；同一协调器只调用新 stop，通过 |
| C27 | 重复注销、dispose 后注册无调用/发布 | U 原测试加强断言，通过 |
| C28 | taken_over/AUTH_REQUIRED/1006/本地 stop 不产生 ended | P 既有服务端消息与 socket 回调测试，通过 |
| C29 | 双重准入矩阵 | 当前可达 idle/requested/unknown 行由原有测试+新增矩阵覆盖，通过；`failed × 详情` 当前不可达，**未执行**，不标完整矩阵通过 |
| C30 | 代理同形 JSON、非 JSON、5xx、未知码、网络/Abort/响应丢失归 unknown | U/P、B01，通过；AUTH_REQUIRED 既有 onExpired 副作用单独计数，不作为 failed 证据 |
| C31 | 可信来源前置拒绝 → failed | 冻结的未启用条件项，**未执行、不计入通过数** |

事件口径：`Pane.closed` 是本地标签清理完成事件，清理成功且组件仍挂载时一次；组件已卸载时 Vue 抑制该事件。它与服务端协议 `closed`、协调器 `ended` 不是同一个事件。DELETE 200 自身不触发 Pane.closed、不释放预占、不证明远端完整回收。此区分沿用既有 Pane 生命周期和 S7 §4.6。

## 3. 本次新增组合测试

复用现有辅助函数，未直接赋值生产对象内部状态。竞态由可控 Promise/请求屏障和真实公开操作建立。

| 场景 | 关键执行顺序 | 关键断言 | 结果 / 冻结对应 |
|---|---|---|---|
| requested/unknown + CONNECTING/非法详情/网络失败 | 真 requestClose 建立状态 → refreshClose | DELETE/GET 各一次、正确路径、DELETE `{}`、不 idle/failed；错误不伪造 serverStatus | 6 子场景通过；C29/§4.4 |
| ended 与查询/重试三方竞争 | requested → GET pending → retry DELETE → markEnded → 旧 GET reject/DELETE 200 | 旧 GET/DELETE 均 abort；message/updatedAt/snapshot 不变化；ended 发布一次，重试/刷新不新增请求 | 通过；C07/C18/C22 |
| stop + 重复 close + unmount + 旧 socket/write 回调 | 在线真实 Client+延迟终端 write → stop → close 两次 → 卸载 → 释放 write → 回放旧事件 | close Promise 相同；socket.close/xterm.dispose/订阅 dispose 各一次；零 DELETE、零新 WS、零迟到 changed/closed/ended | 通过；C15/C21 |
| DELETE 完成与 Pane close 交错 | pending → close 同步退订 → DELETE accepted/lost | 协调器 requested/unknown；DELETE signal 未被 Pane abort；DELETE 一次；旧 Pane changed 不增加、资源一次释放 | 两子场景通过；C01/C06 |
| idle 候选详情四态 | 候选挂载 → 实际 GET 返回 CONNECTED/DISCONNECTED/NOT_FOUND/非法 DTO | 合法两态仅确认；NOT_FOUND ended；非法仍 idle；全部确认前凭证/WS 为零 | 四子场景通过；C29 |
| 三个同步失效步骤 | 实际关闭确认 → takeover.invalidate → recovery.stop → client.stop → DELETE | 精确调用顺序、同步 pending、异步 requested | 通过；C23 |
| 旧 Pane closed 后新注册 | 实际 A.close/closed/unmount → 实际 B.mount → 再次 A 注销 → requestClose | 旧 stop 0、新 stop 1、DELETE 1、WS 0；A 资源已释放 | 通过；C26 |
| dispose 后注册 | 实际 coordinator.dispose → subscribe/register/request/retry/refresh | 注册回调和订阅发布均 0、disposed 保持 true | 原 C27 加强通过 |

原有 290 项 Node 测试保留，本次最终总数增加到 309（Node 统计包含父测试和子测试，不能将其当作 309 个业务场景）。浏览器另有 7 个组合测试。

## 4. 浏览器集成结果和截图

环境：Ubuntu 26.04.1 WSL、Node v24.21.0、Playwright 1.65.1、Chromium 159.0.8154.6 headless。Vite 使用实际 Vue 插件编译当前源码；测试专用配置不启用后端代理。没有伪造组件、没有直接设置内部状态。Vue 实例仅用于只读采集生命周期/关闭/候选状态和 xterm buffer。

**夹具边界：HTTP 是 Playwright route 可控响应；WebSocket 是 Playwright routeWebSocket 对端模拟，不是实际 Go WS/TCP/SSH。** WS 计数是在 Playwright 注入后的当前页面 WebSocket API 上包装构造/close 记录，代表浏览器前端调用次数，不代表后端名额或真实连接回收。每个场景单独浏览器 context，未测试 context 之间的服务端权限。

| 场景 | 顺序与核心断言 | 结果 / 截图 |
|---|---|---|
| S7-B01 | 无 Pane 行级关闭 pending → 打开只读 Pane（详情 GET/POST/WS 全 0）→ 取消等待 unknown → 关闭标签重开仍 unknown → 重试收代理同形 INVALID_REQUEST 仍 unknown → 详情 CLOSING requested → NOT_FOUND ended → 旧列表打开/关闭禁用；总 DELETE 2、POST/WS 0 | 通过；01、02 |
| S7-B02 | 打开 A 时列表自动卸载，候选确认仍归 Pane → 确认 takeover（mode takeover/G5、凭证 1、xterm/WS 1）→ 打开 B 候选并取消确认 → 从列表关闭 A，A socket.close 1、B idle 不受影响 → 关闭 B；两个 ID 的 DELETE 各 1、无 B 凭证 | 通过；03 |
| S7-B03 | A 在线 → 行关闭 pending → signOut logout STORAGE_ERROR → 留在 `/`、保留 Pane/重试按钮 → 注入公共 auth-expired 通知 → 不提前导航 → 重试 logout 成功 → 实际 router `/me` 返回 AUTH_REQUIRED → `/login`；登出后仅两次 logout 和一次 `/me`，无额外 DELETE/凭证/WS，Pane 清除、socket.close 1 | 通过；04、05 |
| S7-B04 | 两次刷新后分别返回 CLOSING/网络错误详情 | 各保留 ready Pane、closeState idle、零 confirming/POST/WS；通过；06 两张 |
| S7-B05 | 候选 GET pending → 再进入并退出列表，Pane 仍 loading → 详情成功确认；再以 GET pending 刷新整个页面 → 新页面重新 GET 确认 → 旧 NOT_FOUND 迟到 | 新候选仍 confirming/idle、只有一个 Pane、总详情 GET 3、POST/WS 0；通过；07 |
| S7-B06 | 首次 takeover 凭证 BIND_BUSY → 原任务下一轮 attach/G5 → bind G6 → 实际 xterm 中文/ANSI 输出 → 改变视口触发 resize → 关标签 | 凭证模式严格 `[takeover/G5, attach/G5]`，详情 1，WS/xterm 各 1，零 DELETE；通过连接与资源断言；提示语义另记风险；08 |
| S7-B07 | 旧页面无 Pane DELETE pending → 页面刷新 → 新候选详情 CLOSING → 旧 DELETE 200 | 新页面 closeState idle，不确认、不自动 takeover；DELETE 1、POST/WS 0；通过；09 |

B03 的 AUTH_REQUIRED 副作用通过现有公开 `notifyAuthExpired()` 通知入口注入，执行真实 router/expireIdentity 逻辑；不是将被浏览器 Abort 的旧响应伪装成已送达 HTTP。实际 auth 请求竞态另外由原有 auth 单元测试覆盖。

截图索引（均在 `stage-5-s7-6-logs/`）：

- `01-requested-after-proxy-unknown.png`：代理错误后显式详情刷新升 requested。
- `02-ended-tombstone-stale-row.png`：旧列表行被 tombstone 禁用。
- `03-multiple-panes-isolated.png`：两个 Pane 的关闭状态隔离。
- `04-logout-failure-retry.png` / `05-logout-success-real-router.png`：失败停留与成功登录页。
- `06-reentry-CLOSING.png` / `06-reentry-network-error.png`：新页面拒绝自动接管。
- `07-new-page-candidate.png`：旧详情不能污染新页面。
- `08-retry-attach-xterm-output.png`：同代次 attach 恢复、中文/ANSI 与提示语义观察。
- `09-reload-old-delete-closing.png`：刷新后旧 DELETE 不构成本地自动接管许可。

已目视复核登出失败和 xterm 输出截图。最初 WSL 缺少中文字形，最终截图通过临时 fontconfig 使用已有微软雅黑重新生成；未修改应用样式。

## 5. 验证命令、工具准备与复现

| 命令 | 工作目录 | 实际结果 |
|---|---|---|
| `node -v` | frontend | **v24.21.0**，符合 package.json `>=24.0.0 <25`，退出 0 |
| `npm test` | frontend | **309 tests / 309 pass / 0 fail / 0 cancelled / 0 skipped / 0 todo**，退出 0 |
| `node --test tests/browser/stage-5-s7-6.mjs` | frontend | **7 tests / 7 pass / 0 fail / 0 cancelled / 0 skipped / 0 todo**，退出 0；它不在 `tests/*.test.mjs` 的默认 npm test glob 中，必须单独执行 |
| `npm run typecheck` | frontend | 退出 0 |
| `npm run build` | frontend | 退出 0，内含 typecheck；59 modules，JS 500.78 kB / gzip 142.48 kB |
| `git diff --check` | 项目根 | 退出 0；另检查本次未跟踪文本文件的空白差异 |
| 生产与冻结文档 `git diff --exit-code HEAD -- frontend/src backend ...` | 项目根 | 无差异，退出 0 |
| Go/race/Docker/真实 SSH/代理部署检查 | — | **未执行**，属于 S8 全集成或真实环境范围，本次不扩展 |

构建有非阻塞的 Vite >500 kB chunk 警告；记录而未修改 chunk 配置或应用结构。Git 按仓库行尾设置提示部分测试文件 LF 将转换为 CRLF；不是 diff check 失败。

工具准备均在 WSL `/tmp/codex/web-ssh-s7-6-tools/`：从本地 npm 缓存离线安装 Playwright 1.65.1；复用已有 Chromium，不安装/升级项目依赖。Chromium 缺少的 `libnspr4`、`libnss3`、`libasound2t64` 只 apt download 后 dpkg-deb 解包到 `libs/`，未系统安装。`fonts/msyh.ttc` 来自本机 Windows 字体，只供临时测试使用，不纳入交付文件。脚本支持 `S7_PLAYWRIGHT_MODULE`、`S7_CHROMIUM`、`S7_BROWSER_LIBS`、`S7_FONTCONFIG` 四个环境变量替换这些路径。

当前已准备好的环境可直接复现：

```text
wsl.exe -d Ubuntu --cd "/home/x/workspaces/web-ssh/frontend" -- bash -ic 'npm test'
wsl.exe -d Ubuntu --cd "/home/x/workspaces/web-ssh/frontend" -- bash -ic 'node --test tests/browser/stage-5-s7-6.mjs'
wsl.exe -d Ubuntu --cd "/home/x/workspaces/web-ssh/frontend" -- bash -ic 'npm run typecheck'
wsl.exe -d Ubuntu --cd "/home/x/workspaces/web-ssh/frontend" -- bash -ic 'npm run build'
wsl.exe -d Ubuntu --cd "/home/x/workspaces/web-ssh" -- bash -ic 'git diff --check'
```

浏览器脚本每次会重写自己的 `browser-results.json` 和同名截图；其他文件不受影响。原始 `.log` 文件受仓库 `*.log` 忽略规则影响，但已经保存在交付目录；结构化结果 JSON 和报告提供可检查的结果。

过程中的失败没有隐藏成生产通过：初次 Chromium 缺少共享库；新浏览器夹具曾将 `/src/api/*.ts` 错误拦截为 API JSON；成功 logout 后 `/me` 的夹具没有转未登录；原生 modal 阻挡背后按钮；WS 计数器被 Playwright 注入覆盖；双次挂载的 Mock teardown 顺序错误。这些均定点修正测试环境/新增测试，未改生产实现。辅助调试截图/日志在 `diagnostics/`，不作为最终验收截图。初次 baseline 外层 shell 退出码采集也曾失败；最终验证由 subprocess 直接采集命令退出码，结果见 `verification-results.json`。

## 6. 未覆盖项、发现与残余风险

### 6.1 确定缺陷与待审查提示语义

**本轮未确认违反冻结设计的生产缺陷，没有实施生产修复。**

观察项 R-S7-6-01（潜在 UI 语义风险，非阻塞；不是已确认的新缺陷）：

1. 打开已有 Session 候选并确认接管，首次 `/takeover` 返回 BIND_BUSY。
2. 同一个 TerminalClient 任务按 S5 自动执行 attach，仍用 G5，bind_success 确认 G6，连接已恢复。
3. 页面当前连接状态为“已连接”，同时保留“接管未完成：BIND_BUSY”；历史区另有 BIND_BUSY。

证据：浏览器 S7-B06、`08-retry-attach-xterm-output.png`、`browser-results.json`。归属为 S6 `frontend/src/terminal/takeover.ts:156` 的首次前缀结果保存及 `frontend/src/components/terminal/TerminalPane.vue:683` 的展示，不是 S7 Coordinator/关闭竞态，也不影响请求次数、mode、generation、输入准入或资源清理。

冻结依据存在需解释的边界：S6 计划 §9 的算法在首次连接 Promise resolve 后保存 `takeover.message`（示例行约 1020），S6 §8.3/R19 接受后续 attempt 只 attach；第五阶段计划 F13（行 958）又要求“恢复成功清当前提示、历史诊断明确”。当前截图符合前者的动作结果保存，但若将 F13 的“当前提示”包含该结果文字，则存在歧义。故本报告仅记录可复现现象和两处依据，不擅自将结果消息重新设计为历史提示，也不把它断言为已解决。等待人工审查其期望语义；任何生产调整需另行授权。

### 6.2 未覆盖 / 未执行

- **C29 的 failed × 六类详情行未执行**。当前 DELETE 来源无可信证据，生产状态机不能通过真实操作到达 failed；没有直接赋值私有状态、伪造可信字段或放宽断言来凑齐矩阵。今后该路径获独立授权实现后，应通过真实可信入口验证。C31 当前明确未启用，不是失败项。
- 浏览器只证明前端与模拟 HTTP/WS 对端的组合；不证明真实 Go WS/TCP、SSH 进程、服务端 slot/lease/凭证回收、COOKIE 清除或真实持久化回滚。
- B02/B03 的跨独立登录权限、第三竞争者、真实断网/休眠、多设备及共享 Cookie 全链路没有执行；不宣称 B01–B05 全集通过。
- 真实高频/长时间输出、vim/top、真实反向代理、物理移动设备和实际浏览器关闭的服务端收尾未执行；属于后续 S8/环境验收。
- 异步排空、初始化/清理故障主要由实际 Client/Pane 加终端替身注入；浏览器没有使真实 xterm 库自身抛 dispose 异常。替身证据不能证明库内部的所有资源清理。

### 6.3 冻结已接受风险（非新缺陷）

- HomePage 刷新会丢失 unknown；服务端详情只是瞬时快照，不能证明旧 DELETE 已失效。旧 DELETE 可能在新页面用户确认 takeover 后生效并关闭新连接，属于 D1 接受条件，本轮未消除、也不声称消除。
- DELETE/logout 无本地定时期限；abort 只停止等待，不能撤销服务端动作。
- 当前链路除合法 200 外全部 unknown；failed 暂不可达。
- last-wins 注册依赖同 Session 单 Pane 生命周期；token 只隔离注销身份，不替代旧 Pane 的真正停止。
- 服务端 closed/SESSION_NOT_FOUND 只证明当前不可接管，不证明 metadata/buffer/lease 全部回收。
- S6 U1 接受 dispose 失败后缓存失败、保留预占，无法原地重试；U9 接受成功登出优先，清理失败也导航并提示刷新。
- U6 路由认证网络错误属于独立冻结问题，本轮未更改。

## 7. 最终结论与停止状态

**有条件通过**：当前可达的 S7-1～S7-5 前端组合路径及本次新增竞态断言通过，309 项 Node 测试和 7 项真实浏览器测试全部通过，未发现阻塞性生产缺陷或尚未解决的测试失败。

保留条件是：人工审查 R-S7-6-01 的提示语义；接受 C29 failed 行当前不可达、未执行的明确边界。真实后端/SSH 和跨设备全集成没有被本次结果代替。不能宣称完整 C29、完整 B01–B05 或 S8 已通过。

本任务至此停止，保留测试、报告与证据供审查。不继续开发、不修改生产代码、不进入 S8、不做 Git 提交/分支整合，也不执行部署。
