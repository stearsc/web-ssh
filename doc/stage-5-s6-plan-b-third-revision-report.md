# Web SSH S6 方案 B 第三轮定点修订报告

- 修订日期：2026-11-18
- 文档基线：分支 `codex/第五阶段`，HEAD `1084e02`
- 修订对象：`doc/stage-5-implementation-plan.md`
- 审查依据：第三轮定点修订任务附件及最新独立代码/设计交叉审查结论
- 修订性质：仅设计文档修订；未开始 S6-C1 编码，未修改生产代码、测试代码、CSS、API/协议、配置或依赖。
- 结论：第三轮三个问题均已在冻结实施计划中形成可执行的统一规则；未发现仍未解决的文档规则冲突。该修订只完成文档一致性，不构成编码授权。

## A. 修改文件清单

| 文件 | 状态 | 说明 |
|---|---|---|
| `doc/stage-5-implementation-plan.md` | 已修改，未暂存、未提交 | 第三轮定点修订主文档 |
| `doc/stage-5-s6-plan-b-third-revision-report.md` | 新增，未暂存、未提交 | 本报告 |
| `doc/stage-5-s6-plan-b-second-revision-report.md` | 未修改 | 第二轮修订历史记录 |
| `doc/stage-5-s6-plan-b-second-review-report.md` | 未修改 | 第二轮只读审查报告 |
| `doc/stage-5-s6-plan-b-revision-report.md` | 未修改 | 第一轮修订报告 |
| `doc/stage-5-s6-precheck-report.md` | 未修改 | S6 编码前检查报告 |

本轮没有修改 `frontend/`、`backend/`、测试、`package.json`、构建配置、CSS、数据库或部署文件。工作区中用户已有修改 `luan-qi-ba-zao.txt` 和未跟踪文件 `web-ssh-stage5-6.zip` 均未处理。

## B. 修改章节清单

| 章节 | 修改内容 | 原因 |
|---|---|---|
| 文件头、§1 | 更新为第三轮修订状态和 `1084e02` 基线 | 明确本轮边界和历史基线 |
| §2 术语 | 增加 `PaneLifecycle`、`PaneDisposeResult`、`loginEpoch` | 统一三个新契约的术语 |
| §3 当前实现表 | 修正 `TerminalPane.dispose()` 吞异常、`HomePage` 无唯一登记、`auth.ts` epoch 语义 | 让设计目标对应真实代码差距 |
| §8.1 | 明确 `PaneLifecycle` 先于组件创建，模板 ref 不是所有权/取消/清理依据 | 关闭无句柄分支，建立创建取消屏障基础 |
| §8.1.1 | 重写 `panes`、`pending`、`closing`、tombstone；新增 lifecycle 字段、实例状态与 closing 释放表；重写规则 2/5/6/7/8/9/11/12 | 解决无句柄关闭、迟到 mount、dispose 失败和重试 |
| §8.1.2 | props 增加 `lifecycle`；重写初始化顺序和每步取消检查；新增 `PaneDisposeResult` 契约与重复调用规则 | 解决 dispose 失败传播和资源创建屏障 |
| §8.1.3 | 门禁规则增加同步取消、实例/登录 token 检查、dispose 结果判定 | 保证关闭不绕过门禁，也不把 terminating 当清理完成 |
| §8.1.4 | 完全重写为登录身份生命周期；新增 epoch 推进表、auth 模块职责、Pane token 组成和校验时机 | 解决 `/me` 刷新误推进 epoch、AuthView 对象替换误停终端 |
| §8.2、§8.2.1 | 预占步骤增加 `PaneLifecycle`；详情/确认/连接启动校验 `loginEpoch` | 正文与身份契约一致 |
| §8.3、§8.4、§8.5 | 时序图增加 lifecycle、`loginEpoch` 检查 | 图示与正文一致 |
| §11.1、§11.2.1 | 所有权不变量增加 lifecycle/tombstone；closing 行增加 disposing/disposeFailed | 统一跨标签所有权 |
| §12.1、§12.1.1 | 关闭段落改为等待 dispose 结果对象；时序图增加成功/失败分支、迟到 mount、页面卸载结果记录 | 解决清理结果不可观察和页面卸载伪成功 |
| §12.2 | 身份代次推进规则改为登录生命周期语义；网络错误不视为失效 | 与 §8.1.4 一致 |
| §15 | S6-C1 增加 lifecycle、dispose 结果、登录生命周期和 auth/router/LoginPage；S6-C2 增加 B47 | 保证 C1 是完整安全纵切 |
| §16 | 更新 HomePage、TerminalPane、auth、router/LoginPage 职责；新增可选 `pane-lifecycle.ts` | 固定最小文件边界 |
| §17.4 | F05/F12 增加 dispose 失败、迟到 mount、`/me` 刷新 | 测试矩阵同步 |
| §17.4.1 | 修订 B03/B18/B19/B20/B26/B27/B28/B33；新增 B39–B47；范围改为 B01–B47 | 覆盖第三轮全部行为 |
| §18.1 | 增加 dispose 失败重试、各初始化阶段取消、`/me` 同 epoch、网络错误 vs `AUTH_REQUIRED` 注入 | 保证竞态可复现 |
| §19 E05 | 增加 dispose 失败重试、关闭后重开、`/me` 刷新和路由导航 | 真实验收同步 |
| §20.1、§20.2 | 增加第三轮复审门槛、dispose/创建屏障/登录生命周期验收项 | 把修复纳入验收 |
| §21.3、§21.4 | 增加第三轮修订摘要和结论 | 保留决策记录和停止边界 |

## C. dispose 成功/失败传播契约

采用显式结果对象，全文不混用 Promise reject 语义：

```ts
type PaneDisposeResult =
  | { status: 'succeeded' }
  | { status: 'failed'; error: string; retryable: boolean }
```

契约：

1. `PaneLifecycle.dispose(): Promise<PaneDisposeResult>` 始终 resolve 结果对象，不以 reject 表示清理失败。
2. 内部可以捕获异常以展示错误文本，但不得把失败转成正常完成或 `void`。
3. HomePage 只以 `status === 'succeeded'` 判定清理成功；`retryable` 只作 UI 建议，不决定 closing 是否释放。
4. 只有真实成功才进入 `disposed`、发出 `closed` 并释放 closing。
5. `disposeRequested`、`disposing`、`disposeFailed`、`disposed` 必须可区分；开始清理、组件卸载、`nextTick()` 完成都不算成功。
6. 内部错误提示只影响 UI，不改变返回给 HomePage 的失败结果。

## D. dispose 失败后的 closing 保留与重试

```text
closing
  -> cancelCreation()（同步、不可逆）
  -> disposeRequested
  -> disposing
       -> succeeded: disposed -> 释放 closing -> 保留 tombstone
       -> failed: disposeFailed -> 保留 closing/lifecycle/错误 -> 允许重试
```

规则：

- 失败时保留旧 `PaneLifecycle`、必要资源句柄和失败原因，不创建替代 Pane。
- 进行中的重复调用共享同一个底层 Promise，不并行执行多个 `client.dispose()`/stop 链。
- 成功后重复调用幂等返回 `{ status: 'succeeded' }`，不重新执行清理。
- 失败后不永久缓存失败 Promise；下一次调用从 `disposeFailed` 发起新的清理尝试，跳过已确认完成的资源，继续处理剩余资源。S5 `client.dispose()` 成功后自身幂等，S6 不修改其缓存语义，只重跑尚未完成的 lifecycle 清理。
- 首次失败后第二次成功时，仍只有第二次的真实成功才能释放 closing。
- HomePage 卸载时逐 Pane 调用并记录结果；未完成或失败不得报告清理成功，也不能把组件卸载当作成功信号。

## E. 无句柄关闭的完整状态转换

第三轮取消了“尚无句柄”的独立分支：HomePage 在同步预占时先创建 `PaneLifecycle`，因此关闭时永远有可取消、可清理、可查询结果的句柄。模板 ref 只附加 mount 事实，不参与所有权判断。

| 实例状态 | 长期资源 | 关闭动作 | closing |
|---|---|---|---|
| 仅创建 lifecycle，组件尚未创建 | 无 | `cancelCreation()`；`dispose()` 返回真实成功 | 成功后才释放 |
| 组件已创建但尚未 mount | 无 | 同上；迟到 mount 被取消屏障拒绝 | 成功后才释放 |
| 已 mount，初始化未完成 | 可能有部分资源 | 取消并清理已登记资源 | 成功后才释放 |
| 模板 ref 已注册，尚未 ready | 可能有部分资源 | 同上；ref 不是清理完成 | 成功后才释放 |
| 已 ready | 是 | invalidate/abort/stop/dispose | 成功后才释放 |
| `disposeRequested`/`disposing` | 保留 | 共享进行中 Promise | 保持 |
| `disposed` | 无 | 幂等成功 | 释放 |
| `disposeFailed` | 保留必要句柄 | 允许重试清理 | 保持 |

## F. 创建取消屏障机制

`PaneLifecycle` 由 HomePage 在同步调用栈内创建，至少包含：

- 不可变身份：`paneKey`、`paneInstanceId`、`pageToken`、捕获时的 `loginEpoch` 和身份 token。
- 不可逆 `cancelled` 标记；`cancelCreation()` 只能从 `false` 置为 `true`。
- 阶段：`created`、`mounting`、`ready`、`failed`、`disposeRequested`、`disposing`、`disposed`、`disposeFailed`。
- 长期资源登记表：Client、Terminal、Recovery、Takeover、监听器、observer、timer 等。
- `registerResource(kind, resource, cleanup)`：同步检查 `cancelled`/`disposed`，拒绝时调用方不得创建资源；接受时登记资源身份和清理入口。创建抛错时调用方在同一 `catch` 撤销登记。
- `dispose(): Promise<PaneDisposeResult>`。

HomePage 同时维护 `retiredPaneKeys` tombstone。关闭时同步 `cancelCreation()`；旧实例的 `setPane`、`ready`、`failed`、`closed`、取消确认和 dispose 结果按 tombstone 丢弃。tombstone 在 HomePage 生命周期内只增不减，不能用 `nextTick()`、模板 ref 消失或超时清除。

## G. 迟到 mount 如何在创建长期资源前被阻止

TerminalPane 的初始化入口固定为：

1. `setup` 最早阶段读取不可替换的 `lifecycle`，立即检查 `cancelled`、`disposed`、`pageToken`、`loginEpoch`。
2. 已失效时直接进入取消路径，不创建任何长期资源、不 emit `ready`。
3. 创建 Client、Gate、Recovery、Takeover、Terminal、监听器时，必须先通过 `lifecycle.registerResource` 同步登记；取消后登记被拒绝，调用方不得继续创建。
4. 每个跨 await 的步骤（创建 Client、Recovery、Takeover、安装监听、等待 `nextTick()`）之后重新检查 `cancelled` 和 token。
5. 已登记资源由旧 lifecycle 负责清理；迟到结果不得注册为当前有效 Pane。

因此以下窗口都被覆盖：

- Pane 创建后、模板 ref 注册前关闭。
- 模板 ref 注册后、`onMounted` 初始化前关闭。
- `onMounted` 开始但 Client 尚未创建时关闭。
- Client 已创建、Recovery 尚未创建时关闭。
- Takeover 已创建但尚未 ready 时关闭。
- 旧 Pane 迟到 mount，新 Pane 已有创建意图。

释放 closing 不是因为猜测旧组件不会 mount，而是因为 lifecycle 已永久 `cancelled + disposed`，迟到 mount 已失去创建资源的能力。

## H. closing 全部释放条件

closing 只在同时满足以下条件时释放：

1. 该 Session 的当前 `paneKey` 与 closing 记录完全匹配。
2. `PaneLifecycle.cancelCreation()` 已生效。
3. `PaneLifecycle.dispose()` 返回 `{ status: 'succeeded' }`。
4. 该 `paneKey` 已进入 tombstone。

以下情况一律不释放 closing：

- 仅从 `tabs` 删除、模板 ref 为 null、`nextTick()` 完成或组件卸载。
- `disposeRequested`、`disposing` 或 `terminating` 阶段。
- `dispose()` 返回 `failed`，包括 HomePage 卸载期间的失败。
- 旧实例迟到 `ready`/`failed`/`closed`/dispose 结果。
- 仍有 `reopenRequested` 但旧实例未成功清理。

HomePage 卸载是唯一不要求等待 UI 的场景：它同步取消全部 lifecycle 并尽力异步清理，但每个结果仍必须被记录，未完成或失败不得报告成功，也不得重建 `tabs`/`pending`。

## I. identityEpoch 的准确语义

`identityEpoch` 表示登录生命周期（WebAuthSession），不是 AuthView 对象引用，也不是页面生命周期。

四个概念必须区分：

| 概念 | 表示 | 失效方式 |
|---|---|---|
| 登录生命周期 | `identityEpoch` | 新登录、登出、服务端确认身份失效时推进 |
| 身份展示数据 | AuthView 对象 | `/me` 刷新可替换；不推进 epoch |
| 页面生命周期 | `pageToken` | HomePage 销毁；不推进 epoch |
| Pane 实例 | `paneKey`/`paneInstanceId` | 关闭、重试、重开；不推进 epoch |

## J. identityEpoch 各类事件推进规则

| 事件 | 是否推进 | 说明 |
|---|---|---|
| 应用首次通过 `/me` 确认已有登录 | 否 | 发现已有登录，作为页面基线 |
| 同一登录生命周期内重新请求 `/me` | 否 | 只刷新展示数据 |
| 同一登录生命周期内 AuthView 对象引用替换 | 否 | 对象引用不是登录生命周期 |
| 路由导航触发 `checkIdentity()` | 否 | 只刷新展示数据 |
| 用户成功登录，建立新登录生命周期 | 是 | 登录成功后、导航前推进一次 |
| 用户开始登出 | 是 | 同步推进，旧 Pane token 立即失效 |
| 同一登出意图的成功清理、失败或重试 | 否 | 一次登出只推进一次 |
| 服务端明确确认身份失效/过期 | 是 | 不是普通网络错误 |
| 登出后重新登录，即使是同一管理员 | 是 | 每次成功登录都是新生命周期 |
| HomePage 销毁 | 否 | 由 `pageToken` 失效处理 |
| `/me` 网络错误、超时或服务暂不可用 | 否 | 不视为确定身份失效 |

## K. acceptIdentity() 与 checkIdentity() 职责划分

- `acceptIdentity(value)`：只刷新身份展示数据、更新过期定时器；永不推进 epoch。同一生命周期内重复调用和 AuthView 对象替换都不改变 epoch。
- `beginLoginLifecycle()`（或等价命名）：`POST /api/auth/login` 成功后、`router.replace('/')` 之前同步调用，推进 epoch 一次。登录后的 `/me` 仍走 `acceptIdentity()`，不重复推进。
- `checkIdentity()`：捕获调用时 epoch，请求 `/me`；epoch 未变化且未 logoutPending 时才 `acceptIdentity(value)`。不建立新登录生命周期，不因网络错误推进 epoch 或清除身份。
- `clearIdentity()`：清除展示数据并推进 epoch；用于登出成功清理和服务端确认失效。登出意图已推进时不重复推进。
- `expireIdentity()`：只在服务端明确返回身份失效/过期时调用。
- `router.beforeEach()`：`AUTH_REQUIRED` 才按失效处理；网络错误且已有身份时保留身份、epoch、当前页面和 Pane，显示可重试提示且不强制跳转登录页；没有既有身份时才回登录页。
- `TerminalPane`：监听 `identityEpoch` 或派生登录 token，不监听 `identity` 对象引用；普通 `/me` 刷新不得触发 stop。

## L. Pane 身份 token 组成及校验时机

```ts
type PaneIdentityToken = {
  loginEpoch: number
  pageToken: string
  paneKey: string
  paneInstanceId: string
}
```

有效条件：`loginEpoch` 等于当前 epoch、`pageToken` 是当前 HomePage、`paneKey`/`paneInstanceId` 是当前有效实例、`logoutPending=false`、Pane 未 terminating/cancelled/disposed。不得用 AuthView 对象引用相等作为条件。

校验时机：

- TerminalPane 初始化及每个跨 await 的初始化步骤之后。
- 详情 GET 完成、发布快照、展示确认内容之前。
- 用户确认首次 await 之前。
- `connectTakeover()` 启动之前。
- 凭证响应、WebSocket `onopen`、`bind_success`、长期任务回调和所有 `finally`。
- TerminalClient 的 `allowed()` 回调。

旧 epoch 的迟到详情、确认、凭证、onopen、bind_success 和 finally 一律丢弃。

## M. 新增/调整测试编号

新增：

| ID | 验证边界 | 阶段 |
|---|---|---|
| S6-B39 | dispose 成功/失败传播、closing 保留、页面卸载不伪报成功 | S6-C1 |
| S6-B40 | dispose 并发共享、幂等成功、失败后重新清理 | S6-C1 |
| S6-B41 | 各初始化阶段关闭的创建取消屏障 | S6-C1 |
| S6-B42 | 迟到 mount/ready/failed/closed/dispose 结果与 tombstone | S6-C1 |
| S6-B43 | HomePage 卸载时未 mount Pane 的清理结果 | S6-C1 |
| S6-B44 | 同一登录生命周期 `/me` 刷新与 AuthView 替换 | S6-C1 |
| S6-B45 | 登出/重登录推进 epoch，旧 epoch 迟到回调丢弃 | S6-C1 |
| S6-B46 | 网络错误 vs `AUTH_REQUIRED` | S6-C1 |
| S6-B47 | closing 未释放时重开、取消确认前后重开的复杂交错 | S6-C2 |

调整：

- `S6-B03`：`panes` 改为 `PaneLifecycle` 句柄，`setPane` 只附加 ref。
- `S6-B18`/`S6-B19`：关闭/页面终止增加同步取消 lifecycle 和 dispose 结果记录。
- `S6-B20`：重开条件改为旧 `dispose()` 返回 `succeeded`。
- `S6-B26`：同标签重试必须等待 `succeeded`；失败不生成新 Pane。
- `S6-B27`：迟到事件扩展为 setPane/ready/failed/closed/dispose 结果和 tombstone。
- `S6-B28`：改为显式结果对象、并发共享、幂等和失败重试。
- `S6-B33`：由“身份对象替换使旧 token 失效”改为“同一 epoch 内对象替换不失效；新登录/登出/确认失效才失效”。
- `F05`/`F12`：增加 dispose 失败/重试、迟到 mount、`/me` 刷新。

S6-C1 覆盖 S6-B01–B22、B24–B33、B35、B37、B39–B46；S6-C2 覆盖复杂交错、集成回归、B23、B25、B34、B36、B38、B47 和 B01–B05 浏览器项。C2 不能补 C1 缺失的基本安全清理。

S6-C1 对 `auth.ts`/`router`/`LoginPage` 的改动仅限登录生命周期 epoch、`/me` 刷新区分和 token 失效；完整登出失败/不确定重做仍属于 S7，不在 C1 提前实现。

## N. 状态机、时序图、正文、测试矩阵一致性检查

| 位置 | 检查结果 |
|---|---|
| §8.1.1 状态表 | 已覆盖 created/mounting/ready/failed/disposeRequested/disposing/disposed/disposeFailed 与 closing 关系 |
| §8.1.2 初始化顺序 | 每一步都包含 lifecycle 检查和资源登记；dispose 契约与 §8.1.1 一致 |
| §8.1.3 门禁 | terminating、取消、dispose 结果三者语义分离，无绕过 |
| §8.1.4 身份 | epoch 推进表、auth 职责、token 组成和校验时机一致 |
| §8.3/§8.4/§8.5 时序图 | 已加入 PaneLifecycle、loginEpoch 和身份 token 检查 |
| §11.2.1 分类表 | closing/disposing/disposeFailed/terminating 合并为不可操作状态，但保留 dispose 重试 |
| §12.1.1 时序图 | 增加 dispose 成功/失败分支、迟到 mount 拒绝、页面卸载结果记录 |
| §15 C1/C2 | C1 包含 lifecycle、dispose、身份和创建屏障基础；C2 只做复杂交错 |
| §17.4.1 测试矩阵 | B01–B47 连续，无重复编号；B39–B47 与正文一一对应 |
| §19/§20 | E05 和验收项覆盖 dispose 失败、迟到 mount、`/me` 刷新 |

## O. 全文搜索关键词及冲突检查

使用的关键词包括：

`disposeTask`、`dispose()`、`dispose settle`、`dispose Promise`、`closed`、`closing`、`pending`、`尚无句柄`、`PaneLifecycle`、`PaneDisposeResult`、`registerResource`、`cancelled`、`tombstone`、`identityEpoch`、`loginEpoch`、`acceptIdentity`、`checkIdentity`、`身份对象替换`、`身份代次`、`S6-B38`、`S6-B47`。

检查结论：

- 不存在“dispose 失败被内部 catch 后返回成功”的旧规则。§3 仅把当前代码的吞异常行为记为待修正事实；§8.1.2 明确禁止。
- 不存在“无句柄关闭直接释放 closing”的旧规则。§8.1.1 明确取消该分支，关闭一律等待 `PaneDisposeResult`。
- 不存在“`/me` 刷新或 AuthView 对象替换推进 identityEpoch”的旧规则。§8.1.4、§12.2、§16、B33/B44 均已统一。
- 不存在“确认后才把主动接管交给 Pane”的旧规则。所有权移交仍以匹配 ready 为界。
- 不存在“管理页拥有详情查询/确认”或“离开管理页取消候选”的旧规则。
- 不存在“Pane 创建/关闭窗口允许重复 Pane”的旧规则；tombstone 和 lifecycle 登记共同阻止。
- 不存在 S5 方案 A 与 S6 主动接管冲突的规则；`client.ts` 仍为冻结基线，S6 只调用既有入口。
- 第二轮历史摘要中的“身份代次”是历史记录，已被第三轮 §8.1.4 和 §21.3 的新语义覆盖，不构成当前规则冲突。

## P. 未解决的问题

无未解决文档规则冲突。

仍属于未来实现验证、而非本轮文档缺口的事项：

- `PaneLifecycle`、`PaneDisposeResult`、`SessionTakeover`、`PaneOperationGate` 尚未实现。
- `auth.ts` 尚未暴露只读 epoch，尚未拆分登录生命周期入口。
- `router/index.ts` 尚未区分 `AUTH_REQUIRED` 与网络错误。
- 所有 S6-B 测试尚未执行。

这些事项按 §15 的 S6-C1/S6-C2 范围在获得明确编码授权后实施。

## Q. 未修改范围确认

- 仅修改设计文档。
- 未修改生产代码。
- 未修改测试代码。
- 未修改 CSS、Vue、TypeScript、Go 源码。
- 未修改 API、HTTP 请求格式、WebSocket 协议、配置或依赖。
- 未修改 S5 已冻结的 `client.ts` 契约。
- 未修改 D1、D2、D3 的用户裁决。
- 未修改 S7、S8 的既定范围。
- 未开始 S6-C1。
- 未执行 `git commit`、`git push`、合并、变基或部署。

## R. 自检结果

- `git diff --check` 通过。
- 主文档全文搜索未发现第三轮禁止的旧规则。
- 本轮只修改设计文档并新增本报告；工作区未暂存、未提交。
- 本轮未执行测试或构建，因为只修改设计文档；未将任何 S6 测试或构建勾选为已通过。

完成本报告后停止，等待人工复审和另行编码授权；不进入 S6-C1。
