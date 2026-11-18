# Web SSH S6-C1 实施报告

## 1. 范围与基线

- 授权范围：仅 S6-C1 前端纵切实现。
- 分支：`S6`
- 基线提交：`efad5e1de345309df251ba76cc4e9046762a8d5c`（`方案完善`）
- 未执行提交、推送、合并、部署或分支变更。
- 未修改冻结设计文档、后端文件、`client.ts`、`output.ts`、`recovery.ts`、`stores/auth.ts`、`router/index.ts`、`api/*`、`Modal.vue`。

## 2. 实现内容

### 2.1 新增 `frontend/src/terminal/takeover.ts`

- 实现 `SessionTakeover` 的 `idle`、`stopping`、`loading`、`confirming`、`connecting` 阶段。
- `prepare()` 先停止原恢复任务，再 GET Session 详情；不发起 Prepare、凭证请求或 WebSocket。
- 三个准入检查点分别位于 prepare 开始、GET 返回后、confirm 最终提交前。
- `confirm()` 在最终准入检查与 `connectTakeover()` 之间没有 await；候选只消费一次。
- 候选详情严格通过 `sessionView()` 校验，不使用旧 props 补齐缺失字段。
- `cancel()` 仅取消准备/查询阶段；`invalidate()` 只由 Pane 的 `stop()`/`close()` 调用。

### 2.2 `frontend/src/components/terminal/TerminalPane.vue`

- 增加 `startMode: 'attach' | 'candidate'`、`life` 状态和 `ready`/`failed`/`closed`/`changed` 事件。
- 初始化拆为前置校验与资源创建两段；构造链最后一步才创建 xterm，工厂任一步失败均回滚已创建资源。
- 记录终端工厂回滚错误；清理失败时不 emit `closed`，HomePage 保留记录和预占。
- `stop()` 使接管动作失效并停止恢复/Client；`close()` 为不可逆清理，成功后才 emit `closed`。
- 增加候选接管按钮、准备/取消/确认流程和确认 Modal；候选确认前零 Prepare、零凭证、零 WebSocket。
- 保留原有手动恢复、刷新状态、关闭 SSH 会话、网络状态展示和诊断提示行为。

### 2.3 `frontend/src/pages/HomePage.vue`

- 以 `records` 统一承载标签、预占和 Pane 句柄；`key` 单调递增，避免同一 Session 在同一 flush 内复用旧实例。
- `openSession()` 同步预占；已有记录只定位；`CONNECTING`/`CLOSING` 显示提示且不创建 Pane。
- `ready`/`failed` 都登记句柄；只有 `closed` 移除记录并释放预占。
- `changed` 仅在管理视图调用 `manager.refresh()`。
- `connectServer()` 仍使用 attach 创建，并按 Session ID 去重。
- `signOut()` 同步封闭准入并先发起所有 `stop()`，与 `logout()` 并行；stop 屏障完成后才 `close()`。
- stop 与 close 失败分别统计、分别 `console.error`，登出成功时在跳转后写入 `authNotice`。

### 2.4 `frontend/src/components/session/SessionManager.vue`

- 表格新增“操作”列和“打开”按钮。
- 按钮 `emit('open', item)`，在 `!canRefresh` 时禁用；不按 Session 状态禁用。

### 2.5 测试

- 新增 `frontend/tests/session-takeover.test.mjs`，覆盖 S6-T01 至 T12、T48、T49。
- 扩展 `frontend/tests/terminal-pane.test.mjs`，覆盖 S6-T13 至 T26、T46、T46-A、T46-B、T47。
- 扩展 `frontend/tests/session-list.test.mjs`，覆盖 S6-T27 至 T37、T50，并升级 HomePage 替身 Pane 的 ready/failed/closed/changed、句柄和 signOut 行为。

## 3. 验证结果

在 WSL Ubuntu 的 `/home/x/workspaces/web-ssh/frontend` 执行：

| 命令 | 结果 |
|---|---|
| `node --test tests/terminal-pane.test.mjs` | 49/49 通过 |
| `node --test tests/session-list.test.mjs` | 22/22 通过 |
| `node --test tests/session-takeover.test.mjs` | 14/14 通过 |
| `npm test` | 132/132 通过 |
| `npm run typecheck` | 通过 |
| `npm run build` | 通过，Vite 构建成功 |
| `git diff --check` | 无空白错误 |

## 4. 未执行项

- 未执行浏览器端到端验收和截图检查；候选接管 Modal、操作列和视觉布局未做真实浏览器目视确认。
- 未连接真实后端/SSH 目标执行主动 takeover；C1 验证使用替身 Pane、替身 Client 和受控 fetch。
- S6-C2 范围（T38 至 T45）未执行，未越权实现。
- 未提交、推送、合并或部署。

## 5. 设计偏差与说明

- 生产行为未发现与冻结设计不一致之处。
- 测试中将原 T22 拆为 T22-A（身份失效清空候选）和 T22-B（acceptIdentity 替换对象不 stop），仅为隔离测试清理顺序，不改变生产行为。
- 测试替身的 xterm 模块改为构造时解析全局替身，以正确注入 T21/T46/T47 的失败场景；仅影响测试。
- `base.css` 未修改。

## 6. 变更文件

- 新增：`frontend/src/terminal/takeover.ts`
- 新增：`frontend/tests/session-takeover.test.mjs`
- 修改：`frontend/src/components/terminal/TerminalPane.vue`
- 修改：`frontend/src/pages/HomePage.vue`
- 修改：`frontend/src/components/session/SessionManager.vue`
- 修改：`frontend/tests/terminal-pane.test.mjs`
- 修改：`frontend/tests/session-list.test.mjs`

## 7. 当前状态与残留风险

- 当前工作树包含上述 7 个预期文件变更，尚未提交。
- 主要残留风险是未做真实浏览器和真实 SSH/后端 takeover 验收；单元与集成替身测试不能替代真实网络、跨设备接管和视觉验收。
