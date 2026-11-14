# Web SSH WebSocket 协议规范

- 协议版本：1.0，客户端和服务端随应用一起发布，不做版本协商。
- 修订日期：2026-11-14。
- 本文定义消息、传输顺序和客户端处理算法；HTTP API、错误码、配置和数据库以[核心需求](web-ssh-design-development-plan.md)为准，内部并发和资源管理以[架构文档](web-ssh-architecture-implementation-plan.md)为准，阶段安排见[实施指南](web-ssh-implementation-stages.md)。文档修订不代表业务代码已经实现或验证。

<a id="transport"></a>
## 1. 传输与数值约定

| 内容 | 传输方式 |
|---|---|
| 终端输入与输出 | WebSocket 二进制消息，原始字节，无 JSON、Base64 或自定义偏移头 |
| 应用控制消息 | WebSocket 文本消息，一个完整 JSON 对象 |
| 连接心跳与关闭 | WebSocket 协议 Ping/Pong/Close，由服务端统一协调 |

每条连接只有一个 Reader 和一个应用 Writer。多个业务协程不得直接写连接；发送仲裁和关闭职责见[架构：输出缓存与发送](web-ssh-architecture-implementation-plan.md#output-buffer)。下文“消息”均指分片重组后的完整消息。第一版不启用 WebSocket 压缩；仍须按实际读取字节限制容量，不能靠分片绕过上限。

- 浏览器在安装消息处理器前设置 `binaryType = "arraybuffer"`，二进制输出直接构造 `Uint8Array` 交给 xterm.js。不能异步转换 Blob 后自行改变消息顺序。
- 偏移是 Session 自创建起收到的 SSH 原始输出字节数，范围为 `[0, 9007199254740991]` 的整数。范围采用左闭右开；每个 UTF-8、CR、LF、ANSI 字节均占一个偏移。所有偏移相加先校验安全整数，不允许浮点、负数或回绕。
- `binding_generation` 同为非负安全整数，Session 初始为 0，仅成功绑定时递增；成功消息中的值至少为 1。服务端计数即将溢出时按核心的 `counter_exhausted` 关闭原因结束 Session。
- JSON 字段名区分大小写，字段必须满足对应消息定义。服务端对客户端 JSON 拒绝缺失、未知或重复字段、错误类型和未知消息类型；客户端校验服务端控制消息的字段集合、类型和顺序。不可识别协议帧由 WebSocket 库拒绝。
- xterm.js 的 `onData` 用 `TextEncoder` 编码；`onBinary` 将每个字符码值直接映射成字节，不能再 UTF-8 编码。历史回放期间禁止所有终端输入，包括解析历史查询序列触发的自动应答。

<a id="bind"></a>
## 2. 凭证与绑定

### 2.1 HTTP 准备与连接建立

使用[核心：Session API](web-ssh-design-development-plan.md#api-sessions)的 `POST /api/sessions/{id}/takeover` 获取一次性凭证。`mode=attach` 用于首次接入和自动恢复；`mode=takeover` 只用于用户主动接管。两者都携带 `expected_generation`。凭证格式、有效期、容量、消费点和失败重试由[架构：绑定](web-ssh-architecture-implementation-plan.md#binding)定义；HTTP 请求不替换旧绑定。

随后建立 `GET /ws/sessions/{session_id}`，携带有效登录 Cookie并通过 Origin 校验。未升级请求使用核心 HTTP 错误响应；升级后使用本文控制消息。凭证只能放在首条 `bind`，不进入 URL、日志或本地持久存储。连接进入关闭流程后不再接受任何业务操作。

### 2.2 `bind`：浏览器 → 服务端

```json
{"type":"bind","credential":"一次性凭证","requested_start_offset":0}
```

| 字段 | 类型 | 必填 | 含义 |
|---|---|---|---|
| `type` | string | 是 | 固定为 `bind` |
| `credential` | string | 是 | HTTP 返回的一次性绑定凭证 |
| `requested_start_offset` | number | 是 | 当前 xterm.js 实例最后连续处理完成的结束偏移，或明确截断后的基线 |

首条完整消息必须在 `websocket.bind_timeout` 内收到且为 bind。绑定前输入、resize、重复 bind 均为协议错误。服务端在提交时重新检查登录、凭证、Session 状态、预期代次和输出偏移；提交前任何失败都不替换原绑定。原子提交成功后凭证已消费，后续通知失败不会复活旧绑定。

恢复偏移属于具体终端实例：

| 场景 | 请求偏移 |
|---|---|
| 新建视图、页面刷新、新设备 | `0` |
| 同实例连续重连 | 停止旧连接、丢弃未提交写入、等待已提交写入完成后读取 `processedOffset` |
| 旧实例排空失败后重建 | `0` |
| 其他设备曾处理的游标 | 不可用于当前新实例 |

设请求为 `P`、绑定瞬间缓存范围为 `[A,E)`：`P>E` 拒绝 `INVALID_OFFSET`；否则实际回放起点 `S=max(P,A)`，取得一次不可变快照 `[S,E)`。空快照允许 `S=E`。偏移、代次和快照必须在同一次绑定提交中确定，提交前不能为了发送消息先撤销旧绑定。

### 2.3 `bind_success`：服务端 → 浏览器

```json
{"type":"bind_success","binding_generation":3,"replay_start_offset":1024,"replay_end_offset":2048,"current_cols":120,"current_rows":32}
```

| 字段 | 类型 | 必填 | 含义 |
|---|---|---|---|
| `type` | string | 是 | `bind_success` |
| `binding_generation` | number | 是 | 本连接不可变的绑定代次 |
| `replay_start_offset` | number | 是 | `S`，本连接唯一快照的起点 |
| `replay_end_offset` | number | 是 | `E`，快照结束位置，实时输出从这里连续衔接 |
| `current_cols` / `current_rows` | number | 是 | 当前远端 PTY 尺寸，正整数且不超过核心配置上限 |

每条连接最多一次 bind_success。客户端验证 `P<=S<=E`。`S=P` 时沿用原终端与解析状态；`S>P` 时必须等待下一条 history_truncated，不能先处理二进制。记录代次只用于本连接操作和以后的 attach 请求，不能让旧 socket 从全局状态拿到新代次。

### 2.4 `history_truncated`：仅绑定前缀出现一次

仅 `S>P` 时发送，并且必须紧跟 bind_success、位于任何其他应用消息之前：

```json
{"type":"history_truncated","requested_start_offset":800,"replay_start_offset":1024,"replay_end_offset":2048,"lost_bytes":224}
```

| 字段 | 类型 | 必填 | 含义 |
|---|---|---|---|
| `type` | string | 是 | `history_truncated` |
| `requested_start_offset` | number | 是 | 本次 bind 的 `P` |
| `replay_start_offset` / `replay_end_offset` | number | 是 | 必须与 bind_success 的 `S` / `E` 完全相同 |
| `lost_bytes` | number | 是 | 严格等于 `S-P`，且大于零 |

客户端验证该消息后，销毁旧 xterm.js 并创建新实例，清除旧画面、滚动历史及 UTF-8/ANSI 解析状态，以 `S` 设置接收和处理基线，在终端外显示“较早输出已截断，已重建终端”。这表示 `[P,S)` 已明确丢失，不表示这些字节已经被 xterm 处理。不得把提示写入 SSH 字节流。

OutputBuffer 不解码 UTF-8、不解析 ANSI、不移动到所谓字符边界、不改写任何字节。原始后缀仍可能从字符或控制序列中间开始；重建终端避免把旧未完成解析状态拼到不连续数据，但不能补回丢失信息或保证恢复 vim/top 画面。正常连续流跨消息的 UTF-8/ANSI 序列由同一 xterm 实例自然衔接。

<a id="output-order"></a>
## 3. 唯一发送顺序与输出缺口

成功绑定后的应用消息顺序固定为：

```text
bind_success
  → 必要时一次 history_truncated
  → 一次稳定历史快照 [S,E) 的二进制块
  → 从 E 开始的连续实时二进制块
```

Writer 在发送完整绑定前缀前不发送 status 或二进制输出。控制通知只出现在完整二进制消息之间，不插入某条二进制消息内部；历史块与实时块不混合为同一消息。可选 status 在前缀后发出，不改变偏移或回放状态。

- SSH 读取只追加有界 OutputBuffer 并发出容量 1 的合并通知，不等待浏览器。Writer 只持有一次稳定快照和至多一个实时块，不建立按输出块累积的发送队列。
- 快照复制后不再依赖环形缓存，不持有缓存锁进行网络写入。正常缓存淘汰不修改快照，也不通知持续连续消费的浏览器。
- 快照发完后，Writer 按发送游标复制下一块；在同一缓存锁内检查游标及取得字节。若游标早于当前缓存起点，发送终止错误 `OUTPUT_GAP` 并关闭浏览器通道。**当前 WebSocket 不重新取历史快照，不再发送 history_truncated，不跳过缺口继续二进制输出。**
- 前端以最后实际处理完成的偏移重新申请 attach。若新绑定仍有缺口，通过该新连接唯一的绑定前缀声明损失并重建终端；自动恢复有次数上限。
- 终止事件可在消息边界中断回放：已获准的当前一次写入可以完成，此后取消普通输出/控制事件，只尝试一次终止通知及 Close。终止通知之后不再发送应用数据。终止与关闭统一由 Writer 协调，硬截止到达可直接关闭底层连接。
- `taken_over`、`closed`、`error` 由首个确定的终止原因择一发送，不能全部排队。若绑定提交后尚未发出成功前缀就发生终止，可直接发送终止通知；客户端不能据此假定绑定没有提交。通知可能因断网而丢失。
- SSH 最终退出时，尾部输出和关闭通知尽力送达，不承诺先排空全部缓存。客户端收到 `closed`/`taken_over` 后仍有限时地处理本地已经接受的队列，以尽量保留最后几行。

<a id="messages"></a>
## 4. 输入、尺寸和状态消息

### 4.1 终端输入与输出

二进制消息不带偏移头。客户端从本次回放起点建立 `receiveCursor`，每块 `startOffset=receiveCursor`、`endOffset=startOffset+byteLength`。只有该块的 xterm write 回调完成，才推进 `processedOffset`。接收游标不能代替处理游标。

服务端从当前 socket 自己的 BrowserBinding 捕获身份与代次；操作执行时再次检查 active、登录状态、对象身份和代次，具体顺序见[架构：操作串行化](web-ssh-architecture-implementation-plan.md#operation-order)。普通断开也立即撤销旧 Binding；队列中的旧操作不得继续写 SSH。已进入在途远端操作的输入不能撤回。容量为 1 的待执行输入槽已满时，Reader 最多等待 `session.operation_wait_timeout`；仍无法入队则以 `INPUT_BACKPRESSURE` 终止浏览器通道且停止自动恢复。没有输入确认协议，客户端不能推测哪些输入已执行，也不得自动重发。

仅当绑定前缀完整且 `processedOffset>=replay_end_offset` 时允许终端输入。回放产生的 xterm 自动应答同样丢弃。失联、被接管或关闭后立即禁用输入；没有发送的输入不跨连接保存或自动补发。

浏览器发送也必须有界：较大粘贴按 `max_input_message_size` 分块；每次发送前检查 `socket.bufferedAmount + 本块字节数 <= max_input_message_size`，不满足就暂停该次粘贴，至多等待 `write_timeout`，期间检查 socket 身份、输入权限和关闭状态。每终端只允许一个发送任务，不再接受第二批粘贴排队；超时或断线丢弃剩余字节并提示用户，不自动重放。resize 仅保留最新一项待发送尺寸，所有 send 都遵守相同 bufferedAmount 上限。单个事件输入转换产生的临时数据在任务结束后释放。

### 4.2 `resize`：浏览器 → 服务端

```json
{"type":"resize","cols":120,"rows":35,"binding_generation":3}
```

所有字段必填。cols/rows 为配置范围内正整数；generation 必须同时匹配消息所属 socket 的代次和当前有效绑定。绑定成功后即可同步尺寸，不必等历史显示完成。旧 socket 不能通过提交新代次取得权限；无效旧回调忽略，当前连接主动提交错误代次以 BIND_CONFLICT 终止。尺寸改变只操作原有 PTY，不创建 Session。

### 4.3 `status`：服务端 → 浏览器

```json
{"type":"status","status":"CONNECTED"}
```

两个字段必填，status 只允许 `CONNECTED`，是绑定前缀之后可选的当前状态通知。它不能重新启用回放期间的输入。离线状态通过 HTTP 查询；SSH 实际关闭使用 closed，不向已经断开的 socket 推送 DISCONNECTED。

### 4.4 `error`：服务端 → 浏览器

```json
{"type":"error","code":"OUTPUT_GAP","message":"输出超过浏览器处理速度，请重新连接"}
```

三个字段必填且为字符串。所有 error 都终止当前浏览器通道，不代表 SSH Session 关闭。code 只允许下表和[核心错误码](web-ssh-design-development-plan.md#error-codes)中的同名值；message 用于展示，不用于程序分支，也不能包含敏感字段。

| 错误码 | WebSocket Close code | 客户端处理 |
|---|---:|---|
| `INVALID_MESSAGE`、`INVALID_OFFSET`、`INVALID_SIZE`、`BIND_CONFLICT`、`ALREADY_BOUND`、`AUTH_REQUIRED`、`PERMISSION_DENIED`、`INPUT_BACKPRESSURE` | 1008 | 停止自动恢复；显示具体错误 |
| `SESSION_NOT_FOUND`、`SESSION_NOT_READY` | 1008 | 停止恢复并刷新会话状态 |
| `MESSAGE_TOO_LARGE` | 1009 | 停止恢复 |
| `INVALID_CREDENTIAL`、`BIND_TIMEOUT`、`BIND_BUSY` | 1008 | 仅在收到明确 code 时按有限预算重新申请 attach；代次不变 |
| `OUTPUT_GAP` | 4002 | 丢弃未提交输出、等待在途写入，再有限恢复 |
| `SLOW_CLIENT` | 1013 | 同上 |
| `SERVER_SHUTTING_DOWN` | 1012 | 有限恢复，最终交由用户重试 |
| `INTERNAL_ERROR` | 1011 | 有限恢复，不重复提交旧凭证 |

客户端队列过载主动使用 Close code `4003` 并执行有限恢复。浏览器的 `WebSocket.close` 只允许 1000 或 3000–4999，不能从浏览器调用 `close(1008)`。

### 4.5 `taken_over` 与 `closed`

```json
{"type":"taken_over","message":"会话已被其他设备接管"}
```

```json
{"type":"closed","reason":"ssh_exit","message":"远端 Shell 已退出"}
```

所有示例字段必填且为字符串。taken_over 使用 Close code `4001`；closed 使用 `1000`，reason 只允许[核心关闭原因](web-ssh-design-development-plan.md#close-reasons)中的值。closed 仅用于 SSH Session 实际关闭；浏览器协议错误、输出缺口和慢客户端不得发送 closed。

收到任一消息后立即停止输入和自动恢复，限时排空本地已接受输出并显示提示。taken_over 后只有用户主动操作才允许重新接管。即使通知丢失，4001 也停止恢复；1006 等异常断线只能走携带原代次的 attach，不能悄悄改为 takeover。

<a id="limits"></a>
## 5. 限制、Reader、Writer 与关闭

默认值、单位、必填性、启动校验及生效方式只在[核心配置](web-ssh-design-development-plan.md#configuration)定义。协议使用以下配置，不另设隐藏默认值：

| 规则 | 配置键 |
|---|---|
| 升级连接总容量，含未绑定和关闭中连接 | `websocket.max_connections` |
| 完整控制消息、二进制输入、输出块大小 | `websocket.max_control_message_size`、`max_input_message_size`、`output_chunk_size` |
| 首条完整 bind 等待 | `websocket.bind_timeout` |
| 取得消息读取器后，整条消息累计读取时间 | `websocket.message_read_timeout` |
| 每次消息写入、关闭通知加握手总预算 | `websocket.write_timeout`、`close_timeout` |
| Ping 周期、单个在途 Ping 的 Pong 期限 | `websocket.ping_interval`、`pong_timeout` |
| 业务控制事件队列容量 | `websocket.control_queue_limit` |
| 客户端队列与在途 write 合计字节、排空期限 | `terminal.write_queue_bytes`、`write_drain_timeout` |
| 客户端恢复次数、间隔、成功运行后重置预算的时间 | `terminal.reconnect_max_attempts`、`reconnect_delay`、`reconnect_stable_time` |

Reader 使用流式读取，在分配超过限制前拒绝；message_read_timeout 从取得读取器后开始，跨分片不重置。它不覆盖 Reader 返回前不完整首帧头的等待；绑定前受 bind_timeout 限制，绑定后的失联由 Ping/Pong 回收，正常没有输入不是超时理由。

Writer 为应用消息唯一发送者。业务协程提交有界控制事件，输出只提交合并通知；队列已满时请求统一终止，不能再阻塞 SSH 输出读取等待队列空位。Reader 只解析消息和协议控制帧，不另行发送应用 JSON；库自动处理 Pong 等协议帧不计为第二个应用 Writer。主动 Ping/Close 必须受同一关闭协调约束，单连接最多一个在途 Ping，库版本固定后验证其 Reader/Ping 并发要求。

通知、Close 握手及正在进行的写入共享关闭期限；超过期限关闭底层连接。任何关闭路径先撤销对应操作权，不能等通知送达后才解除绑定。未绑定连接也登记并回收。应用退出时取剩余总退出时间与连接关闭预算中较小值，详见[架构：退出](web-ssh-architecture-implementation-plan.md#shutdown)。

<a id="client-tracker"></a>
## 6. 前端输出跟踪参考实现

以下 TypeScript 是可执行的算法参考，不是业务文件。`TerminalPort` 对应 xterm.js 已有的 write/dispose API；`createTerminal` 必须实际创建、挂载一个新实例并重新安装输入/尺寸监听，旧实例 dispose 时清理其监听。`notice` 只更新终端外的 UI。设置来自 `GET /api/auth/me` 的 `client_settings`，时间值单位仍为秒，在计时器边界转毫秒。

同一个实例最多一个已提交 write；收到数据只推进 receiveCursor，write 回调才推进 processedOffset。普通断线清空未提交队列、保留在途 write；最终终止保留有限队列继续排空。排空超时或 write 抛错时销毁旧实例，旧实例的迟到回调通过 terminalEpoch 失效，新实例从 0 恢复。

```typescript
interface ClientSettings {
  max_control_message_size: number;
  max_input_message_size: number;
  output_chunk_size: number;
  bind_timeout: number;
  operation_wait_timeout: number;
  write_timeout: number;
  max_cols: number;
  max_rows: number;
  write_queue_bytes: number;
  write_drain_timeout: number;
  reconnect_max_attempts: number;
  reconnect_delay: number;
  reconnect_stable_time: number;
}
interface TerminalPort {
  write(data: Uint8Array, callback: () => void): void;
  dispose(): void;
}
interface BindSuccess {
  type: "bind_success";
  binding_generation: number;
  replay_start_offset: number;
  replay_end_offset: number;
  current_cols: number;
  current_rows: number;
}
interface HistoryTruncated {
  type: "history_truncated";
  requested_start_offset: number;
  replay_start_offset: number;
  replay_end_offset: number;
  lost_bytes: number;
}
type ServerControl = BindSuccess | HistoryTruncated
  | { type: "status"; status: "CONNECTED" }
  | { type: "error"; code: string; message: string }
  | { type: "taken_over"; message: string }
  | { type: "closed"; reason: string; message: string };
interface OutputItem {
  data: Uint8Array;
  startOffset: number;
  endOffset: number;
}
class ClientFailure extends Error {
  kind: "protocol" | "overload";
  constructor(kind: "protocol" | "overload", message: string) {
    super(message);
    this.kind = kind;
  }
}
function ensure(condition: boolean, message: string): asserts condition {
  if (!condition) throw new ClientFailure("protocol", message);
}
function safeOffset(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

class TerminalOutputTracker {
  private terminal: TerminalPort | null;
  private terminalEpoch = 0;
  private isDisposing = false;
  private isDisposed = false;
  private disposeTask: Promise<void> | null = null;
  private queue: OutputItem[] = [];
  private inFlight: OutputItem | null = null;
  private bufferedBytes = 0;
  private receiveCursor = 0;
  private processedOffset = 0;
  private requestedOffset = 0;
  private replayStart = 0;
  private replayEnd = 0;
  private phase: "idle" | "binding" | "truncated" | "stream" = "idle";
  private readySince: number | null = null;
  private pumpScheduled = false;
  private idleWaiters = new Set<() => void>();
  private drainTask: Promise<void> | null = null;
  private createTerminal: () => TerminalPort;
  private settings: ClientSettings;
  private notice: (message: string) => void;
  private onTerminalFailure: () => void;

  constructor(createTerminal: () => TerminalPort, settings: ClientSettings,
              notice: (message: string) => void, onTerminalFailure: () => void) {
    this.createTerminal = createTerminal;
    this.settings = settings;
    this.notice = notice;
    this.onTerminalFailure = onTerminalFailure;
    this.terminal = createTerminal();
  }

  requestedStartOffset(): number {
    ensure(!this.isDisposing && !this.isDisposed, "终端视图已经销毁");
    ensure(this.inFlight === null && this.queue.length === 0,
      "必须等待旧终端写入排空后读取恢复偏移");
    return this.processedOffset;
  }
  get inputEnabled(): boolean {
    return this.phase === "stream" && this.processedOffset >= this.replayEnd;
  }
  get prefixComplete(): boolean { return this.phase === "stream"; }
  consumeStability(now: number): boolean {
    const wasStable = this.readySince !== null &&
      now - this.readySince >= this.settings.reconnect_stable_time * 1000;
    // 稳定资格只消费一次，后续 HTTP 凭证失败不能反复重置恢复预算。
    this.readySince = null;
    return wasStable;
  }

  begin(requested: number): void {
    ensure(safeOffset(requested) && requested === this.requestedStartOffset(),
      "恢复偏移与当前终端不匹配");
    this.requestedOffset = requested;
    this.receiveCursor = requested;
    this.phase = "binding";
    this.readySince = null;
  }
  bind(message: BindSuccess): void {
    ensure(this.phase === "binding", "重复或顺序错误的绑定响应");
    ensure(safeOffset(message.replay_start_offset) &&
      safeOffset(message.replay_end_offset) &&
      this.requestedOffset <= message.replay_start_offset &&
      message.replay_start_offset <= message.replay_end_offset, "非法回放范围");
    this.replayStart = message.replay_start_offset;
    this.replayEnd = message.replay_end_offset;
    this.receiveCursor = this.replayStart;
    this.phase = this.replayStart > this.requestedOffset ? "truncated" : "stream";
    this.updateReady();
  }
  truncated(message: HistoryTruncated): void {
    ensure(this.phase === "truncated" &&
      message.requested_start_offset === this.requestedOffset &&
      message.replay_start_offset === this.replayStart &&
      message.replay_end_offset === this.replayEnd &&
      message.lost_bytes === this.replayStart - this.requestedOffset &&
      message.lost_bytes > 0, "截断通知与绑定范围不匹配");
    this.replaceTerminal(this.replayStart);
    this.phase = "stream";
    this.notice("较早输出已截断，已重建终端");
    this.updateReady();
  }
  binary(data: Uint8Array): void {
    ensure(this.phase === "stream", "绑定前缀完成前收到二进制输出");
    ensure(data.byteLength > 0 && data.byteLength <= this.settings.output_chunk_size,
      "输出块大小非法");
    const startOffset = this.receiveCursor;
    const endOffset = startOffset + data.byteLength;
    ensure(safeOffset(endOffset), "输出偏移超出安全整数范围");
    ensure(startOffset >= this.replayEnd || endOffset <= this.replayEnd,
      "单块输出跨越历史与实时边界");
    if (this.bufferedBytes + data.byteLength > this.settings.write_queue_bytes) {
      throw new ClientFailure("overload", "浏览器处理输出过慢");
    }
    this.receiveCursor = endOffset;
    this.bufferedBytes += data.byteLength;
    this.queue.push({ data, startOffset, endOffset });
    this.schedulePump();
  }

  freeze(keepQueued: boolean): void {
    if (this.isDisposed) return;
    this.phase = "idle";
    if (!keepQueued) {
      for (const item of this.queue) this.bufferedBytes -= item.data.byteLength;
      this.queue = [];
    }
    this.schedulePump();
  }
  settle(keepQueued: boolean): Promise<void> {
    if (this.isDisposed) return Promise.resolve();
    this.freeze(keepQueued);
    if (this.drainTask !== null) return this.drainTask;
    if (this.inFlight === null && this.queue.length === 0) return Promise.resolve();
    this.drainTask = new Promise<void>((resolve) => {
      let finished = false;
      let timer: ReturnType<typeof setTimeout>;
      const finish = (): void => {
        if (finished) return;
        finished = true;
        clearTimeout(timer);
        this.idleWaiters.delete(finish);
        resolve();
      };
      this.idleWaiters.add(finish);
      timer = setTimeout(() => {
        this.replaceTerminal(0);
        if (!this.isDisposing) this.notice("终端写入未能及时完成，已重建终端");
        finish();
      }, this.settings.write_drain_timeout * 1000);
    }).finally(() => { this.drainTask = null; });
    return this.drainTask;
  }

  dispose(): Promise<void> {
    if (this.disposeTask !== null) return this.disposeTask;
    this.isDisposing = true;
    this.disposeTask = this.settle(true).finally(() => {
      this.isDisposed = true;
      this.phase = "idle";
      this.replaceTerminal(0);
      for (const finish of [...this.idleWaiters]) finish();
      this.idleWaiters.clear();
    });
    return this.disposeTask;
  }

  private schedulePump(): void {
    if (this.isDisposed || this.pumpScheduled) return;
    this.pumpScheduled = true;
    queueMicrotask(() => {
      this.pumpScheduled = false;
      this.pump();
    });
  }
  private pump(): void {
    if (this.isDisposed) return;
    if (this.inFlight !== null) return;
    const item = this.queue.shift();
    if (item === undefined) {
      for (const finish of [...this.idleWaiters]) finish();
      return;
    }
    const epoch = this.terminalEpoch;
    this.inFlight = item;
    try {
      ensure(item.startOffset === this.processedOffset, "终端写入队列不连续");
      ensure(this.terminal !== null, "终端实例不存在");
      this.terminal.write(item.data, () => {
        if (epoch !== this.terminalEpoch) return;
        if (this.inFlight !== item) {
          this.failTerminal();
          return;
        }
        this.processedOffset = item.endOffset;
        this.bufferedBytes -= item.data.byteLength;
        this.inFlight = null;
        this.updateReady();
        this.schedulePump();
      });
    } catch {
      this.failTerminal();
    }
  }
  private failTerminal(): void {
    this.phase = "idle";
    this.replaceTerminal(0);
    if (!this.isDisposing) {
      this.notice("终端处理失败，已重建终端");
      this.onTerminalFailure();
    }
    this.schedulePump();
  }
  private updateReady(): void {
    if (this.inputEnabled && this.readySince === null) this.readySince = Date.now();
  }
  private replaceTerminal(baseOffset: number): void {
    this.terminalEpoch += 1;
    this.terminal?.dispose();
    this.terminal = this.isDisposing || this.isDisposed ? null : this.createTerminal();
    this.queue = [];
    this.inFlight = null;
    this.bufferedBytes = 0;
    this.processedOffset = baseOffset;
    this.receiveCursor = baseOffset;
    this.readySince = null;
  }
}
```

<a id="reconnect"></a>
## 7. 单任务恢复协调器

下面代码与上一代码块一起使用。它完整规定 HTTP 凭证申请、消息解析、连接事件、终端排空及有限重试。界面创建一个协调器对应一个本地 Session 标签；首次调用 run 使用当前 HTTP Session DTO 的代次。新的用户主动动作必须先 stop 并等待其完成，再重新 run，不能并发启动第二条恢复链。每个标签同时最多一个 CONNECTING/OPEN 的当前 socket 和一个恢复任务；旧 socket 可以短暂处于 CLOSING，但其事件处理器已移除、操作权已失效，不再接受应用事件，仍计入服务端连接上限并受关闭期限回收。不能把请求 close 描述为底层连接已立即消失。输入及 resize 的 UI 监听使用当前协调器的 socket/代次及上述发送门禁，不能捕获可复用的全局 socket。

配置 `reconnect_max_attempts` 表示首次用户发起的连接尝试之外，最多允许的自动恢复次数；每次自动恢复前等待 `reconnect_delay`，两者不包含 HTTP/网络自身耗时。该预算应覆盖正常失联判定窗口，默认值以核心配置为准。自动恢复只使用 attach 和最后成功获知的代次。HTTP/WS `BIND_CONFLICT` 停止恢复；不能自动重新查询新代次并继续抢占。绑定提交成功但 bind_success 丢失时，下次 attach 可能得到冲突，交由用户主动操作。只有历史已处理完成且连接保持 `reconnect_stable_time` 后，下一次断线才重置重试预算；每次 bind_success 不重置预算；一次稳定连接只提供一次预算重置资格，后续 HTTP 凭证申请失败不能重复使用该资格。

```typescript
const wsErrorCodes = new Set([
  "INVALID_MESSAGE", "MESSAGE_TOO_LARGE", "BIND_TIMEOUT", "INVALID_CREDENTIAL",
  "INVALID_OFFSET", "INVALID_SIZE", "BIND_BUSY", "BIND_CONFLICT", "ALREADY_BOUND",
  "SESSION_NOT_FOUND", "SESSION_NOT_READY", "AUTH_REQUIRED", "PERMISSION_DENIED",
  "OUTPUT_GAP", "SLOW_CLIENT", "SERVER_SHUTTING_DOWN", "INTERNAL_ERROR",
  "INPUT_BACKPRESSURE"
]);
const retryCodes = new Set([
  "BIND_BUSY", "INVALID_CREDENTIAL", "BIND_TIMEOUT", "OUTPUT_GAP", "SLOW_CLIENT",
  "SERVER_SHUTTING_DOWN", "INTERNAL_ERROR", "CLIENT_OVERLOAD",
  "CLIENT_TERMINAL_ERROR", "NETWORK_ERROR", "CLIENT_BIND_TIMEOUT",
  "CREDENTIAL_LIMIT", "CONNECTION_LIMIT_REACHED"
]);
const closeReasons = new Set([
  "user_closed", "idle_timeout", "ssh_exit", "ssh_error", "keepalive_timeout",
  "server_shutdown", "create_failed", "process_restarted", "counter_exhausted"
]);
function objectValue(value: unknown): Record<string, unknown> {
  ensure(typeof value === "object" && value !== null && !Array.isArray(value),
    "响应必须是对象");
  return value as Record<string, unknown>;
}
function exactFields(value: Record<string, unknown>, names: string[]): void {
  ensure(Object.keys(value).length === names.length &&
    names.every((name) => Object.hasOwn(value, name)), "字段不完整或包含未知字段");
}
function parseControl(text: string, settings: ClientSettings): ServerControl {
  ensure(new TextEncoder().encode(text).byteLength <= settings.max_control_message_size,
    "控制消息超限");
  const value = objectValue(JSON.parse(text));
  const type = value.type;
  if (type === "bind_success") {
    exactFields(value, ["type", "binding_generation", "replay_start_offset",
      "replay_end_offset", "current_cols", "current_rows"]);
    ensure(safeOffset(value.binding_generation) && value.binding_generation > 0 &&
      safeOffset(value.replay_start_offset) && safeOffset(value.replay_end_offset) &&
      safeOffset(value.current_cols) && value.current_cols > 0 &&
      value.current_cols <= settings.max_cols && safeOffset(value.current_rows) &&
      value.current_rows > 0 && value.current_rows <= settings.max_rows, "绑定字段非法");
  } else if (type === "history_truncated") {
    exactFields(value, ["type", "requested_start_offset", "replay_start_offset",
      "replay_end_offset", "lost_bytes"]);
    ensure(safeOffset(value.requested_start_offset) && safeOffset(value.replay_start_offset) &&
      safeOffset(value.replay_end_offset) && safeOffset(value.lost_bytes), "截断字段非法");
  } else if (type === "status") {
    exactFields(value, ["type", "status"]);
    ensure(value.status === "CONNECTED", "非法状态通知");
  } else if (type === "error") {
    exactFields(value, ["type", "code", "message"]);
    ensure(typeof value.code === "string" && wsErrorCodes.has(value.code) &&
      typeof value.message === "string", "非法错误通知");
  } else if (type === "taken_over") {
    exactFields(value, ["type", "message"]);
    ensure(typeof value.message === "string", "非法接管通知");
  } else if (type === "closed") {
    exactFields(value, ["type", "reason", "message"]);
    ensure(typeof value.reason === "string" && closeReasons.has(value.reason) &&
      typeof value.message === "string", "非法关闭通知");
  } else {
    throw new ClientFailure("protocol", "未知控制消息");
  }
  return value as unknown as ServerControl;
}
class ApiFailure extends Error {
  code: string;
  constructor(code: string, message: string) { super(message); this.code = code; }
}
interface AttemptResult { code: string; retry: boolean; keepQueued: boolean; }
type BindingMode = "attach" | "takeover";

class SessionTerminalClient {
  readonly output: TerminalOutputTracker;
  private sessionId: string;
  private settings: ClientSettings;
  private notice: (message: string) => void;
  private generation = 0;
  private socket: WebSocket | null = null;
  private task: Promise<void> | null = null;
  private isStopped = true;
  private isDisposed = false;
  private disposeTask: Promise<void> | null = null;
  private cancelAttempt: ((result: AttemptResult) => void) | null = null;
  private cancelDelay: (() => void) | null = null;
  private credentialRequest: AbortController | null = null;

  constructor(sessionId: string, settings: ClientSettings,
              createTerminal: () => TerminalPort, notice: (message: string) => void) {
    this.sessionId = sessionId;
    this.settings = settings;
    this.notice = notice;
    this.output = new TerminalOutputTracker(createTerminal, settings, notice, () => {
      this.cancelAttempt?.({ code: "CLIENT_TERMINAL_ERROR", retry: true, keepQueued: false });
    });
  }
  get currentSocket(): WebSocket | null { return this.socket; }
  get currentGeneration(): number { return this.generation; }
  get canSendInput(): boolean {
    return !this.isStopped && this.socket?.readyState === WebSocket.OPEN &&
      this.output.inputEnabled;
  }

  run(mode: BindingMode, expectedGeneration: number): Promise<void> {
    ensure(!this.isDisposed, "终端视图已经销毁，不可重新连接");
    if (this.task !== null) return this.task;
    ensure(safeOffset(expectedGeneration), "非法预期绑定代次");
    this.generation = expectedGeneration;
    this.isStopped = false;
    this.task = this.loop(mode).finally(() => {
      this.isStopped = true;
      this.task = null;
    });
    return this.task;
  }
  async stop(): Promise<void> {
    this.isStopped = true;
    this.credentialRequest?.abort();
    this.cancelDelay?.();
    this.cancelAttempt?.({ code: "USER_STOPPED", retry: false, keepQueued: true });
    try {
      if (this.task !== null) await this.task;
    } finally {
      await this.output.settle(true);
    }
  }
  dispose(): Promise<void> {
    if (this.disposeTask !== null) return this.disposeTask;
    this.isDisposed = true;
    // 同步停止网络后立即标记销毁；排空超时也不能创建无用的新终端。
    const stopping = this.stop();
    const disposing = this.output.dispose();
    this.disposeTask = Promise.allSettled([stopping, disposing]).then((results) => {
      for (const result of results) {
        if (result.status === "rejected") throw result.reason;
      }
    });
    return this.disposeTask;
  }

  private async loop(initialMode: BindingMode): Promise<void> {
    let mode = initialMode;
    let retryCount = 0;
    while (!this.isStopped) {
      let result: AttemptResult;
      try {
        await this.output.settle(false);
        if (this.isStopped) return;
        const requested = this.output.requestedStartOffset();
        const credential = await this.prepareCredential(mode);
        if (this.isStopped) return;
        result = await this.attempt(credential, requested);
      } catch (error) {
        if (this.isStopped) return;
        const code = error instanceof ApiFailure ? error.code
          : error instanceof ClientFailure ? "INVALID_MESSAGE" : "NETWORK_ERROR";
        this.notice(error instanceof Error ? error.message : "连接失败");
        result = { code, retry: retryCodes.has(code), keepQueued: false };
      }
      const wasStable = this.output.consumeStability(Date.now());
      await this.output.settle(result.keepQueued);
      if (this.isStopped || !result.retry) return;
      if (wasStable) retryCount = 0;
      if (retryCount >= this.settings.reconnect_max_attempts) {
        this.notice("自动恢复已停止，请检查网络后手动重新连接");
        return;
      }
      retryCount += 1;
      mode = "attach";
      await this.waitForRetry();
    }
  }

  private async prepareCredential(mode: BindingMode): Promise<string> {
    const controller = new AbortController();
    this.credentialRequest = controller;
    const timer = setTimeout(() => controller.abort(), this.settings.bind_timeout * 1000);
    try {
      const response = await fetch(`/api/sessions/${encodeURIComponent(this.sessionId)}/takeover`, {
        method: "POST", credentials: "same-origin", signal: controller.signal,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode, expected_generation: this.generation })
      });
      const body = objectValue(await response.json());
      if (!response.ok || body.code !== 0) {
        throw new ApiFailure(typeof body.code === "string" ? body.code : "INTERNAL_ERROR",
          typeof body.message === "string" ? body.message : "无法取得绑定凭证");
      }
      const data = objectValue(body.data);
      ensure(typeof data.credential === "string" && /^[A-Za-z0-9_-]{43}$/.test(data.credential) &&
        typeof data.expires_at === "string" && Number.isFinite(Date.parse(data.expires_at)),
        "绑定凭证响应非法");
      return data.credential;
    } finally {
      clearTimeout(timer);
      if (this.credentialRequest === controller) this.credentialRequest = null;
    }
  }

  private attempt(credential: string, requested: number): Promise<AttemptResult> {
    this.output.begin(requested);
    return new Promise<AttemptResult>((resolve) => {
      const url = new URL(`/ws/sessions/${encodeURIComponent(this.sessionId)}`, location.href);
      url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
      const socket = new WebSocket(url);
      socket.binaryType = "arraybuffer";
      this.socket = socket;
      let finished = false;
      let bindTimer: ReturnType<typeof setTimeout>;
      const finish = (result: AttemptResult, closeCode = 1000): void => {
        if (finished) return;
        finished = true;
        clearTimeout(bindTimer);
        this.output.freeze(result.keepQueued);
        socket.onopen = null;
        socket.onmessage = null;
        socket.onerror = null;
        socket.onclose = null;
        if (this.socket === socket) this.socket = null;
        if (this.cancelAttempt === cancel) this.cancelAttempt = null;
        if (socket.readyState < WebSocket.CLOSING) socket.close(closeCode);
        resolve(result);
      };
      const cancel = (result: AttemptResult): void => finish(result);
      this.cancelAttempt = cancel;
      bindTimer = setTimeout(() => finish({ code: "CLIENT_BIND_TIMEOUT", retry: true,
        keepQueued: false }), (this.settings.bind_timeout +
          this.settings.operation_wait_timeout + this.settings.write_timeout) * 1000);
      socket.onopen = () => {
        if (finished) return;
        socket.send(JSON.stringify({ type: "bind", credential,
          requested_start_offset: requested }));
      };
      socket.onmessage = (event: MessageEvent) => {
        if (finished || this.socket !== socket) return;
        try {
          if (event.data instanceof ArrayBuffer) {
            this.output.binary(new Uint8Array(event.data));
            return;
          }
          ensure(typeof event.data === "string", "非法 WebSocket 数据类型");
          const message = parseControl(event.data, this.settings);
          if (message.type === "bind_success") {
            ensure(message.binding_generation === this.generation + 1, "绑定代次不连续");
            this.output.bind(message);
            this.generation = message.binding_generation;
          } else if (message.type === "history_truncated") {
            this.output.truncated(message);
          } else if (message.type === "status") {
            ensure(this.output.prefixComplete, "绑定前缀完成前收到状态通知");
          } else {
            this.notice(message.message);
            if (message.type === "error") {
              finish({ code: message.code, retry: retryCodes.has(message.code), keepQueued: false });
            } else {
              finish({ code: message.type, retry: false, keepQueued: true });
            }
          }
          if (this.output.prefixComplete) clearTimeout(bindTimer);
        } catch (error) {
          const isOverload = error instanceof ClientFailure && error.kind === "overload";
          this.notice(error instanceof Error ? error.message : "终端协议错误");
          finish({ code: isOverload ? "CLIENT_OVERLOAD" : "INVALID_MESSAGE",
            retry: isOverload, keepQueued: false }, isOverload ? 4003 : 1000);
        }
      };
      socket.onerror = () => finish({ code: "NETWORK_ERROR", retry: true, keepQueued: false });
      socket.onclose = (event: CloseEvent) => {
        const isFinal = event.code === 1000 || event.code === 4001;
        const canRetry = [1006, 1011, 1012, 1013, 4002].includes(event.code);
        finish({ code: `WS_${event.code}`, retry: canRetry, keepQueued: isFinal });
      };
    });
  }

  private waitForRetry(): Promise<void> {
    return new Promise<void>((resolve) => {
      const finish = (): void => {
        clearTimeout(timer);
        if (this.cancelDelay === finish) this.cancelDelay = null;
        resolve();
      };
      const timer = setTimeout(finish, this.settings.reconnect_delay * 1000);
      this.cancelDelay = finish;
    });
  }
}
```

`stop()` 停止联网并排空输出，保留终端供用户稍后主动恢复。关闭标签调用并等待 `dispose()`：它同步停止接入、取消凭证请求和重试等待、移除 socket handler，有限排空后销毁终端及其监听，并拒绝后续 run；重复 dispose 复用同一任务。销毁期间排空超时或 write 抛错直接释放旧实例，不创建替代终端。`run` 返回本次连接及恢复任务结束的 Promise；界面须捕获最终异常并显示，不能为每次 render 再调用 run。API 或服务器通知中的 code 原样用于决策，不用 message 推断。上述 `CLIENT_*`、`NETWORK_ERROR`、`WS_*` 仅为浏览器本地原因，不是服务器新增 API/WS 错误码。

应用层队列上限覆盖尚未提交和唯一在途 write 的总字节；当前收到、尚未检查的一块最多另占 output_chunk_size。浏览器自身网络缓冲由浏览器实现管理，应用不能宣称限制它的所有内存；超过应用队列上限立即关闭当前通道。任何恢复都先清理旧 handler，等待旧写入完成或重建终端，再计算新请求偏移。

<a id="examples"></a>
## 8. 必须验证的时序

| 场景 | 正常、失败与并发结果 |
|---|---|
| A 正在操作，B 取得 takeover 凭证后连接失败 | A 保持有效；签发凭证不会踢人 |
| B/C 均针对 generation 3 获得凭证 | 一个 WS 提交为 generation 4；另一个 BIND_CONFLICT 或已失效凭证错误；并发“签发成功”不等于“接管成功” |
| A 普通断线，已有输入在队列 | A 立即 active=false；队列项执行前拒绝，SSH 继续；已经开始的在途操作不可撤回 |
| A 被接管且 taken_over 通知丢失 | A 自动 attach 仍使用原代次，得到冲突后停；不自动查询新代次并抢回 |
| 快照 `[1024,4096)` 发送中，缓存头推进到 5000 | 快照本身不变；发完后发现 4096<5000，终止 OUTPUT_GAP，不发送第二快照 |
| 上述连接前端已处理到 3072，另有 `[3072,3584)` 在途、其余仅排队 | 断线丢队列，等待在途完成后用 3584 申请；若缓存头为 5000，新连接声明丢失 1416 字节、重建后从 5000 回放 |
| 同实例已处理到 3584，缓存仍含该位置 | 新连接从 3584 回放，保留终端与解析器；不重复 `[0,3584)` |
| 同一块的 UTF-8 或 ANSI 跨消息分割 | 正常连续流逐字节不改写；单实例顺序 write 正确衔接 |
| 新回放起点处于 UTF-8 或 ANSI 中间 | 不猜测修剪；重建与外部截断提示；承认后缀可能残缺 |
| xterm 回调在 onclose 之后完成 | 仍推进旧实例 processedOffset；恢复任务先等该回调，不能拿 onclose 瞬间偏移直接重连 |
| xterm 写入抛错或 drain 超时 | 销毁旧实例，迟到回调不再修改状态；新实例从 0，受原重试预算约束 |
| 普通网络切换后旧绑定尚未被服务端判离线 | BIND_BUSY 在有限预算内重试，始终使用原代次；旧绑定 detach 后可恢复，代次变更立即停止 |
| 关闭标签时尚有在途 write | dispose 有限排空、取消重试、清 handler 并销毁实例；超时不创建新终端，旧回调不能再改状态 |
| 连续绑定成功但每次立即 OUTPUT_GAP | 不因 bind_success 重置次数，达到上限后停止 |
| closed/taken_over 到达时已有有限未提交输出 | 停输入、停重连，限时排空本地队列；超时重建并结束，不再次联网 |
| bind 提交成功但 bind_success 丢失 | 凭证已消费；使用原代次自动 attach 得到 BIND_CONFLICT 后停止，由用户主动恢复 |
| 旧连接的断开回调晚于新绑定 | 回调只操作自己所属 Binding，不能把新绑定改为离线 |
| Session 已进入 CLOSING/CLOSED 后到达旧连接断开回调 | 仅撤销旧 Binding 权限，不把 Session 回退为 DISCONNECTED，不重设离线期限 |

## 9. 实现验收

- 服务端严格执行唯一 Reader/Writer、首条 bind、身份校验、代次校验、一次消费、消息上限及关闭硬期限。
- 字段和错误码只使用本文及核心定义；history_truncated 只在新连接前缀，OUTPUT_GAP 后不再输出。
- 输出快照、实时游标、控制队列、升级连接数和前端队列均有界；慢端不阻塞 SSH 读取。
- tracker 的起止偏移、回调完成、断线排空、终端重建、迟到回调隔离和单任务有限恢复均覆盖自动测试。
- 真实 xterm.js 验证中文、ANSI、历史查询自动应答抑制、vim/top、前台命令尾输出及截断提示；仅文档伪代码测试不能替代这些集成验收。
- Cookie、Origin、登录撤销和过期均执行权限校验；日志不得记录凭证、密码、完整终端输入或输出。
