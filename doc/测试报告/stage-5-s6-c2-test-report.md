# Web SSH S6-C2 测试报告

## 1. 范围、基线与约束

- 授权范围：仅 S6-C2 的 S6-T38 至 S6-T45。
- 分支：`S6`
- 当前 HEAD：`9ba0ba51ec972537085dc1d69c0f2a6454d71d96`（`9ba0ba5 S6-C1`）
- C1 代码和浏览器 UI 验收结果作为本轮基线；本轮未修改任何生产代码。
- 已核对 `doc/stage-5-s6-design-plan.md`、`doc/stage-5-s6-design-review-report.md`、`doc/stage-5-implementation-plan.md`、`doc/测试报告/stage-5-s6-c1-implementation-report.md` 中与 C1/C2、生命周期、候选 Pane、错误反馈和验收矩阵相关的章节。
- 未修改冻结设计文档、S5 的 `client.ts` / `output.ts` / `recovery.ts`、`stores/auth.ts`、`router/index.ts`、`api/*`、`Modal.vue` 或后端生产代码。
- 未执行 commit、push、merge、rebase、reset、clean 或部署；未进入 S7/S8。

## 2. 验收环境

| 项目 | 值 |
|---|---|
| 开发与测试环境 | WSL Ubuntu，`/home/x/workspaces/web-ssh` |
| 前端开发服务器 | Vite，`http://127.0.0.1:5173` |
| 后端 | 本地编译的 `web-ssh`，`http://127.0.0.1:8080` |
| SSH 测试对端 | 本地受控 SSH 服务，`127.0.0.1:34505`，用户 `tester`，密码 `ssh-secret` |
| 浏览器 | Chrome `157.0.8103.79`，headless |
| 浏览器上下文 | 两个隔离 profile / CDP 端口 `9223`、`9224`，分别代表 A、B |
| 窗口尺寸 | `1440 × 900` |
| WSL Node / npm / Go | Node `v24.21.0`、npm `11.19.0`、Go `1.26.5 linux/amd64` |
| Windows CDP 驱动 Node | Node `v24.22.3` |
| 测试数据 | 本地测试账号 `admin-secret`；每轮通过 API 新建独立服务器和两个 Session；不连接生产后端，不使用真实 SSH 凭证 |

说明：

- 真实浏览器脚本位于工作区外的可视化目录：`C:\Users\x\.codex\visualizations\2026\11\18\01a1eeb6-9a1d-76e3-be54-79a2111c8d07\s6-c2-real-e2e.mjs`。
- 截图位于同目录的 `s6-c2-ui-screenshots\`。
- 本地 SSH 对端只做字节回显，不执行 shell 命令；因此 T41 的“可交互”以唯一 token 的端到端回显证明，而不是以命令输出证明。

## 3. T38–T45 逐项结果

| 编号 | 结果 | 证据与说明 |
|---|---|---|
| S6-T38 | 通过 | `frontend/tests/terminal-pane.test.mjs` 新增组合竞态矩阵，共 40 个 T38 用例通过 |
| S6-T39 | 通过 | `frontend/tests/session-list.test.mjs` 新增 HomePage 各阶段卸载用例，共 5 个 T39 用例通过 |
| S6-T40 | 通过 | 新增多个 Pane 并发接管不同 Session 的隔离用例，1 个 T40 用例通过 |
| S6-T41 | 通过 | 真实后端 + 两个独立 Chrome 上下文完成 A/B 接管；A 收到 4001 且不抢回，B 同一 Pane 可交互，列表代次为 2 |
| S6-T42 | 通过 | B 的已有 Pane 进入 `retry_wait 1/10` 后执行 D2；详情 GET 在 `recoveryPhase=stopped`、`takeoverPhase=loading` 时发出，drain 前无详情、无 Prepare |
| S6-T43 | 通过 | 打开第二个候选后取消：网络面板只有详情 GET，零 Prepare、零 WebSocket；取消后候选 Pane 保留 |
| S6-T44 | 通过 | 浅色/深色下表格、工具栏、确认框截图；Modal 位于 1440×900 视口内，尺寸 540×386.9，操作按钮完整 |
| S6-T45 | 通过 | 前端 178/178、类型检查、生产构建、后端 `go test ./...`、`git diff --check` 全部通过 |

### 3.1 S6-T38 组合竞态

覆盖内容：

- 接管阶段 `stopping`、`loading`、`confirming`、`connecting` × 抢占动作 `stop`、`close`、身份失效、`closeRemote`。
- 客户端阶段 `preparing`、`connecting`、`replaying`、`retry_wait`、`stopping` × `stop` / `close`。
- `connecting` 的客户端子阶段 × 上述抢占动作。

断言重点：

- 抢占后不再新增 takeover POST；`connecting` 阶段已发出的那一次 takeover 是合法首次提交。
- 抢占后不再出现“确认接管”按钮，不复活候选。
- 只有成功关闭才 emit `closed`；停止后连接状态回到“未连接”，不卡在中间阶段。
- WebSocket 数量不超过 1，确认前不建立连接。

说明：

- `confirming × closeRemote` 未纳入矩阵，因为确认框使用 `showModal`，会阻止页面其他交互；这与冻结设计 R14/R25 一致。
- `replaying` 阶段按设计不显示接管入口；对应 `identity` / `closeRemote` 子用例只断言入口不存在，`stop` / `close` 由单独用例覆盖。

### 3.2 S6-T39 HomePage 卸载

- 覆盖 `opening`、`ready`、`failed` 三个阶段的 HomePage 卸载。
- 卸载后补发 `ready` / `failed` / `closed` / `changed`，断言零新增请求、零 manager 刷新、零记录变更。
- 列表首次查询进行中卸载时，断言请求 `AbortSignal` 已中止，迟到响应不触发新请求。

### 3.3 S6-T40 多 Pane 并发

- 同一页面为两个不同 Session 创建两个 Pane，断言记录、句柄和事件互不共享。
- 对 A 执行 `stop()` / `close()` 不影响 B 的调用计数。
- A 的 `changed` 只刷新一次列表；A 的 `closed` 只移除 A，B 仍在，切换标签后 B 仍为 active。

## 4. 真实浏览器端到端结果

### 4.1 S6-T41：A 在线，B 接管

步骤与结果：

1. A 从 SessionManager 打开第一个 Session，确认前只有详情 GET，零 takeover POST、零 WebSocket；确认后进入“已连接”。
2. B 在独立浏览器上下文打开同一 Session 并确认接管。
3. A 的 WebSocket 收到 4001；页面状态为“未连接”，提示“此会话已由其他连接接管”。
4. 等待 3.5 秒后复查：A 在 4001 之后没有新的 takeover POST，WebSocket 数量仍为 1，未重新连接。
5. B 在同一个 Pane 中输入唯一 token，SSH 对端回显该 token，证明 xterm → Client → WebSocket → SSH → 回显链路可用。
6. B 刷新 SessionManager 后，该 Session 的代次为 `2`。

关键数据：

- A 的恢复阶段：`stopped`；接管阶段：`idle`；`connected: false`。
- A 在 4001 后的新请求：`[]`。
- B 的交互回显：`S6C2_1795016046144_OK`。
- B 列表代次：`2`。

### 4.2 S6-T42：retry_wait 时执行 D2

受控条件：

- B 的第一个 Pane 先保持在线。
- 测试脚本通过页面内 `client.socket.close(4002, ...)` 触发一次可重试的受控连接中断；4002 属于 S5 冻结的可重试关闭码。
- 同时用 CDP `Network.setBlockedURLs` 阻断 `POST /api/sessions/*/takeover`，使恢复任务稳定停在 `retry_wait`，但不影响详情 GET。

结果：

- Pane 状态：`retry_wait`，连接文案“等待自动恢复 1/10”，诊断“连接已结束（4002）”。
- 点击“停止恢复并准备接管”后，fetch 日志中只有一次详情 GET：
  - `method: GET`
  - `url: /api/sessions/13a229b86113344cfba9676e30fb5872`
  - `recoveryPhase: stopped`
  - `takeoverPhase: loading`
- 没有在 `stopping` 阶段发出的请求，也没有任何 Prepare（`POST .../takeover`）。
- 详情返回后确认框显示“状态：已断开 · 代次 2 · 离线保留 7200 秒（查询时）”。

说明：

- fetch 日志中的 DOM 文案字段可能因 Vue 异步渲染短暂滞后；判断 drain 顺序以组件状态 `recoveryPhase=stopped` 和 `takeoverPhase=loading` 为准。
- 受控 4002 断开和 Prepare 阻断仅为验收注入，不修改生产代码，也不改变冻结的恢复/接管契约。

### 4.3 S6-T43：打开后取消

- B 打开第二个 Session 的候选 Pane。
- 网络面板只有一次详情 GET；零 takeover POST、零 WebSocket。
- 确认框出现后点击“取消”。
- 取消后标签栏仍为 3 项（服务器 + 两个 Session），第二个候选 Pane 保留，工具栏回到“未连接 / 准备接管”可用状态。
- 该 Session 的请求仍全部为 GET。

### 4.4 S6-T44：浅色/深色视觉检查

- 表格：浅色、深色各一张；操作列“打开”按钮可见，列宽和行高稳定，无横向溢出。
- 工具栏：浅色、深色各一张；按钮顺序、禁用状态和间距与现有页面一致。
- 确认框：深色、浅色各一张；标题、说明、Session 名称、`user@host:port`、状态/代次/离线保留信息、取消/确认按钮完整。
- Modal 位置与尺寸：`x=450`、`y=256.53`、`width=540`、`height=386.94`，视口 `1440×900`，完全位于视口内。
- 浏览器控制台：A、B 均无 `error` / `warning` 记录，无未捕获异常。

## 5. 截图清单

截图目录：`C:\Users\x\.codex\visualizations\2026\11\18\01a1eeb6-9a1d-76e3-be54-79a2111c8d07\s6-c2-ui-screenshots\`；
同一组截图也保存在仓库内 `doc/测试报告/stage-5-s6-c2-logs/screenshots/`。

| 文件 | 证明内容 |
|---|---|
| `01-a-takeover-modal-real.png` | A 的接管确认框；确认前未连接，Modal 居中且内容完整 |
| `02-a-online-real.png` | A 确认后进入“已连接” |
| `03-b-takeover-modal-real.png` | B 在独立上下文打开同一 Session 的接管确认框 |
| `04-b-online-real.png` | B 接管成功并进入“已连接” |
| `05-a-4001-real.png` | A 收到 4001，显示“未连接”和“此会话已由其他连接接管” |
| `06-b-second-modal-real.png` | B 打开第二个候选 Session 的确认框 |
| `07-b-cancelled-candidate-real.png` | 取消后候选 Pane 仍保留，工具栏可用 |
| `08-b-session-table-light.png` | 浅色主题下的 SessionManager 表格与操作列 |
| `09-b-session-table-dark.png` | 深色主题下的 SessionManager 表格与操作列 |
| `10-b-toolbar-light.png` | 浅色主题下的 TerminalPane 工具栏 |
| `11-b-toolbar-dark.png` | 深色主题下的 TerminalPane 工具栏 |
| `12-b-takeover-modal-dark-real.png` | 深色主题下的接管确认框 |
| `13-b-takeover-modal-light-real.png` | 浅色主题下的接管确认框 |
| `14-b-retry-wait-real.png` | B 的 Pane 处于“等待自动恢复 1/10”，显示“停止恢复并准备接管” |
| `15-b-drain-confirm-real.png` | drain 完成后的详情确认框，代次 2；底部提示“已停止恢复” |

## 6. 代码修改

本轮没有修改生产代码，只新增/扩展了 C2 测试：

| 文件 | 变更 | 原因 |
|---|---|---|
| `frontend/tests/terminal-pane.test.mjs` | +318 行 | 增加 T38 组合竞态矩阵的 Pane/Client 测试夹具与用例 |
| `frontend/tests/session-list.test.mjs` | +107 / -1 行 | 增加 T39 卸载、T40 多 Pane 并发用例；使测试挂载的 `unmount()` 幂等 |

未修改：

- `frontend/src/terminal/takeover.ts`
- `frontend/src/components/terminal/TerminalPane.vue`
- `frontend/src/pages/HomePage.vue`
- `frontend/src/components/session/SessionManager.vue`
- `frontend/src/styles/base.css`
- S5 的 `client.ts`、`output.ts`、`recovery.ts`
- `Modal.vue`、auth/router/api、后端全部文件
- 所有冻结设计文档

对冻结设计的影响：无。测试仅验证既有状态机、资源所有权和 UI 行为，没有改变确认时机、接管流程、generation 契约或清理顺序。

## 7. 回归验证

在 WSL Ubuntu 执行：

| 命令 | 结果 |
|---|---|
| `node --test --test-name-pattern="S6-T38|S6-T39|S6-T40" tests/terminal-pane.test.mjs tests/session-list.test.mjs` | 46/46 通过（T38 40、T39 5、T40 1） |
| `npm test` | 178/178 通过，0 失败 |
| `npm run typecheck` | 通过，无 TypeScript 错误 |
| `npm run build` | 通过；Vite 转换 58 个模块，产出 `backend/web/dist` |
| `go test -count=1 ./...`（在 `backend/`） | 全部有测试的包通过：`cmd/server`、`auth`、`config`、`crypto`、`database`、`httpapi`、`server`、`session`、`sshclient`、`websocket` |
| `git diff --check` | 退出码 0；仅有 Git 的 LF/CRLF 提示，无空白错误 |

构建产物：

- `backend/web/dist/index.html` 0.35 kB
- `backend/web/dist/assets/index-DcUmYrwA.css` 13.20 kB（gzip 3.43 kB）
- `backend/web/dist/assets/index-DPhHlSHP.js` 490.56 kB（gzip 139.53 kB）

日志目录：`doc/测试报告/stage-5-s6-c2-logs/`，包含 `frontend-tests.log`、`frontend-typecheck.log`、`frontend-build.log`、`backend-tests.log`、`git-diff-check.log`、`e2e-real-browser.json` 和截图；命令与退出码见该目录的 `README.md`。

## 8. 未验证项、Mock 与真实环境差异、残留风险

未验证项：

- 没有连接生产后端、真实 SSH 目标或真实用户设备。
- 没有使用第二台物理设备或真实网络中断；A/B 是同一主机上的两个隔离 Chrome 上下文。
- T42 的 `retry_wait` 由受控 4002 socket 关闭和 Prepare 阻断注入，不是真实网络抖动或服务端故障。
- 没有做长时间运行、持续输出、并发压力或跨地域网络测试。
- 没有在 Firefox、Safari 或移动端浏览器复验。

环境差异：

- 本地 SSH 对端只回显字节，不执行 shell 命令；因此 T41 的交互验证是“输入被对端回显”，不是“命令执行并返回输出”。
- 测试数据库保留了多轮 E2E 创建的服务器和 Session；这些只是本地测试数据，不影响生产。
- 受控 4002 关闭和 Prepare 阻断是测试脚本行为，不是生产代码路径。

残留风险：

- 真实网络抖动、代理、心跳超时和真实 SSH 服务端行为仍可能暴露本地回显对端未覆盖的问题；这些属于后续真实环境验收范围。
- `confirming × closeRemote` 和 `replaying` 的接管入口按冻结设计不存在，测试以“入口不存在”覆盖，未强行构造不可达 UI 组合。
- T42 的 DOM 文案在 fetch 时可能滞后，报告以组件 `recoveryPhase` / `takeoverPhase` 为准；这属于测试观测方式，不是产品缺陷。

## 9. 当前 Git 状态与操作声明

- 分支：`S6`
- HEAD：`9ba0ba51ec972537085dc1d69c0f2a6454d71d96`
- HEAD 提交：`9ba0ba5 S6-C1`
- 工作区：
  - `M frontend/tests/session-list.test.mjs`
  - `M frontend/tests/terminal-pane.test.mjs`
- 临时目录 `backend/.tmp-s6-c2/` 已在验收后删除。
- 未 commit、未 push、未 merge、未 rebase、未 reset、未 clean、未部署。
- 未进入 S7/S8，未实施 T38–T45 之外的任何功能。

## 10. 结论

- S6-T38 至 S6-T45 全部通过，或已在本报告中给出明确的测试条件与限制。
- 本轮未发现需要修改生产代码的 C1/C2 缺陷。
- 建议按 C2 验收标准正式验收 S6-C2；残留风险集中在“本地受控 SSH 对端/受控断线”与“真实生产网络、真实 SSH 目标”的差异，不构成本轮阻塞项。
- 等待人工审查；在另行授权前不开始 S7/S8。
