// 单在途 write、回调偏移、有限排空和一次性稳定资格共用同一终端 epoch。
export interface ClientSettings {
  credential_timeout: number
  max_control_message_size: number
  max_input_message_size: number
  output_chunk_size: number
  bind_timeout: number
  operation_wait_timeout: number
  write_timeout: number
  max_cols: number
  max_rows: number
  write_queue_bytes: number
  write_drain_timeout: number
  reconnect_max_attempts: number
  reconnect_delay: number
  reconnect_stable_time: number
}
export interface TerminalPort {
  write(data: Uint8Array, callback: () => void): void
  dispose(): void
}
export interface BindSuccess {
  type: 'bind_success'
  binding_generation: number
  replay_start_offset: number
  replay_end_offset: number
  current_cols: number
  current_rows: number
}
export interface HistoryTruncated {
  type: 'history_truncated'
  requested_start_offset: number
  replay_start_offset: number
  replay_end_offset: number
  lost_bytes: number
}
type ServerControl =
  | BindSuccess
  | HistoryTruncated
  | { type: 'status'; status: 'CONNECTED' }
  | { type: 'error'; code: string; message: string }
  | { type: 'taken_over'; message: string }
  | { type: 'closed'; reason: string; message: string }
export interface OutputItem {
  data: Uint8Array
  startOffset: number
  endOffset: number
}
export class ClientFailure extends Error {
  kind: 'protocol' | 'overload'
  constructor(kind: 'protocol' | 'overload', message: string) {
    super(message)
    this.kind = kind
  }
}
function ensure(condition: boolean, message: string): asserts condition {
  if (!condition) throw new ClientFailure('protocol', message)
}
function safeOffset(value: unknown): value is number {
  return (
    typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
  )
}

export class TerminalOutputTracker {
  private terminal: TerminalPort | null
  private terminalEpoch = 0
  private isDisposing = false
  private isDisposed = false
  private disposeTask: Promise<void> | null = null
  private queue: OutputItem[] = []
  private inFlight: OutputItem | null = null
  private bufferedBytes = 0
  private receiveCursor = 0
  private processedOffset = 0
  private readySince: number | null = null
  private onReady: () => void
  private requestedOffset = 0
  private replayStart = 0
  private replayEnd = 0
  private phase: 'idle' | 'binding' | 'truncated' | 'stream' = 'idle'
  private pumpScheduled = false
  private idleWaiters = new Set<() => void>()
  private drainTask: Promise<void> | null = null
  private createTerminal: () => TerminalPort
  private settings: ClientSettings
  private notice: (message: string) => void
  private onTerminalFailure: () => void

  constructor(
    createTerminal: () => TerminalPort,
    settings: ClientSettings,
    notice: (message: string) => void,
    onTerminalFailure: () => void,
    onReady: () => void = () => {},
  ) {
    this.createTerminal = createTerminal
    this.settings = settings
    this.notice = notice
    this.onTerminalFailure = onTerminalFailure
    this.onReady = onReady
    this.terminal = createTerminal()
  }

  requestedStartOffset(): number {
    ensure(!this.isDisposing && !this.isDisposed, '终端视图已经销毁')
    ensure(
      this.inFlight === null && this.queue.length === 0,
      '必须等待旧终端写入排空后读取恢复偏移',
    )
    return this.processedOffset
  }
  get inputEnabled(): boolean {
    return this.phase === 'stream' && this.processedOffset >= this.replayEnd
  }
  get prefixComplete(): boolean {
    return this.phase === 'stream'
  }

  consumeStability(now: number): boolean {
    const stable =
      this.readySince !== null &&
      now - this.readySince >= this.settings.reconnect_stable_time * 1000
    this.readySince = null
    return stable
  }
  private updateReady(): void {
    if (this.inputEnabled && this.readySince === null) {
      this.readySince = Date.now()
      this.onReady()
    }
  }
  begin(requested: number): void {
    ensure(
      safeOffset(requested) && requested === this.requestedStartOffset(),
      '恢复偏移与当前终端不匹配',
    )
    this.requestedOffset = requested
    this.receiveCursor = requested
    this.phase = 'binding'
    this.readySince = null
  }
  bind(message: BindSuccess): void {
    ensure(this.phase === 'binding', '重复或顺序错误的绑定响应')
    ensure(
      safeOffset(message.replay_start_offset) &&
        safeOffset(message.replay_end_offset) &&
        this.requestedOffset <= message.replay_start_offset &&
        message.replay_start_offset <= message.replay_end_offset,
      '非法回放范围',
    )
    this.replayStart = message.replay_start_offset
    this.replayEnd = message.replay_end_offset
    this.receiveCursor = this.replayStart
    this.phase =
      this.replayStart > this.requestedOffset ? 'truncated' : 'stream'
    this.updateReady()
  }
  truncated(message: HistoryTruncated): void {
    ensure(
      this.phase === 'truncated' &&
        message.requested_start_offset === this.requestedOffset &&
        message.replay_start_offset === this.replayStart &&
        message.replay_end_offset === this.replayEnd &&
        message.lost_bytes === this.replayStart - this.requestedOffset &&
        message.lost_bytes > 0,
      '截断通知与绑定范围不匹配',
    )
    this.replaceTerminal(this.replayStart)
    this.phase = 'stream'
    this.notice('较早输出已截断，已重建终端；保留的后缀不能恢复全部历史画面')
    this.updateReady()
  }
  binary(data: Uint8Array): void {
    ensure(this.phase === 'stream', '绑定前缀完成前收到二进制输出')
    ensure(
      data.byteLength > 0 &&
        data.byteLength <= this.settings.output_chunk_size,
      '输出块大小非法',
    )
    const startOffset = this.receiveCursor
    const endOffset = startOffset + data.byteLength
    ensure(safeOffset(endOffset), '输出偏移超出安全整数范围')
    ensure(
      startOffset >= this.replayEnd || endOffset <= this.replayEnd,
      '单块输出跨越历史与实时边界',
    )
    if (
      this.bufferedBytes + data.byteLength >
      this.settings.write_queue_bytes
    ) {
      throw new ClientFailure('overload', '浏览器处理输出过慢')
    }
    this.receiveCursor = endOffset
    this.bufferedBytes += data.byteLength
    this.queue.push({ data, startOffset, endOffset })
    this.schedulePump()
  }

  freeze(keepQueued: boolean): void {
    if (this.isDisposed) return
    this.phase = 'idle'
    if (!keepQueued) {
      for (const item of this.queue)
        this.bufferedBytes -= item.data.byteLength
      this.queue = []
    }
    this.schedulePump()
  }
  settle(keepQueued: boolean): Promise<void> {
    if (this.isDisposed) return Promise.resolve()
    this.freeze(keepQueued)
    if (this.drainTask !== null) return this.drainTask
    if (this.inFlight === null && this.queue.length === 0)
      return Promise.resolve()
    this.drainTask = new Promise<void>((resolve) => {
      let finished = false
      let timer: ReturnType<typeof setTimeout>
      const finish = (): void => {
        if (finished) return
        finished = true
        clearTimeout(timer)
        this.idleWaiters.delete(finish)
        resolve()
      }
      this.idleWaiters.add(finish)
      timer = setTimeout(() => {
        this.replaceTerminal(0)
        if (!this.isDisposing) this.notice('终端写入未能及时完成，已重建终端')
        finish()
      }, this.settings.write_drain_timeout * 1000)
    }).finally(() => {
      this.drainTask = null
    })
    return this.drainTask
  }

  dispose(): Promise<void> {
    if (this.disposeTask !== null) return this.disposeTask
    this.isDisposing = true
    this.disposeTask = this.settle(true).finally(() => {
      this.isDisposed = true
      this.phase = 'idle'
      this.replaceTerminal(0)
      for (const finish of [...this.idleWaiters]) finish()
      this.idleWaiters.clear()
    })
    return this.disposeTask
  }

  private schedulePump(): void {
    if (this.isDisposed || this.pumpScheduled) return
    this.pumpScheduled = true
    queueMicrotask(() => {
      this.pumpScheduled = false
      this.pump()
    })
  }
  private pump(): void {
    if (this.isDisposed) return
    if (this.inFlight !== null) return
    const item = this.queue.shift()
    if (item === undefined) {
      for (const finish of [...this.idleWaiters]) finish()
      return
    }
    const epoch = this.terminalEpoch
    this.inFlight = item
    try {
      ensure(item.startOffset === this.processedOffset, '终端写入队列不连续')
      ensure(this.terminal !== null, '终端实例不存在')
      this.terminal.write(item.data, () => {
        if (epoch !== this.terminalEpoch) return
        if (this.inFlight !== item) {
          this.failTerminal()
          return
        }
        this.processedOffset = item.endOffset
        this.bufferedBytes -= item.data.byteLength
        this.inFlight = null
        this.updateReady()
        this.schedulePump()
      })
    } catch {
      this.failTerminal()
    }
  }
  private failTerminal(): void {
    this.phase = 'idle'
    this.replaceTerminal(0)
    if (!this.isDisposing) {
      this.notice('终端处理失败，已重建终端')
      this.onTerminalFailure()
    }
    this.schedulePump()
  }
  private replaceTerminal(baseOffset: number): void {
    this.readySince = null
    this.terminalEpoch += 1
    this.terminal?.dispose()
    this.terminal =
      this.isDisposing || this.isDisposed ? null : this.createTerminal()
    this.queue = []
    this.inFlight = null
    this.bufferedBytes = 0
    this.processedOffset = baseOffset
    this.receiveCursor = baseOffset
  }
}
const wsErrorCodes = new Set([
  'INVALID_MESSAGE',
  'MESSAGE_TOO_LARGE',
  'BIND_TIMEOUT',
  'INVALID_CREDENTIAL',
  'INVALID_OFFSET',
  'INVALID_SIZE',
  'BIND_BUSY',
  'BIND_CONFLICT',
  'ALREADY_BOUND',
  'SESSION_NOT_FOUND',
  'SESSION_NOT_READY',
  'AUTH_REQUIRED',
  'PERMISSION_DENIED',
  'OUTPUT_GAP',
  'SLOW_CLIENT',
  'SERVER_SHUTTING_DOWN',
  'INTERNAL_ERROR',
  'INPUT_BACKPRESSURE',
])
const closeReasons = new Set([
  'user_closed',
  'idle_timeout',
  'ssh_exit',
  'ssh_error',
  'keepalive_timeout',
  'server_shutdown',
  'create_failed',
  'process_restarted',
  'counter_exhausted',
])
function objectValue(value: unknown): Record<string, unknown> {
  ensure(
    typeof value === 'object' && value !== null && !Array.isArray(value),
    '响应必须是对象',
  )
  return value as Record<string, unknown>
}
function exactFields(value: Record<string, unknown>, names: string[]): void {
  ensure(
    Object.keys(value).length === names.length &&
      names.every((name) => Object.hasOwn(value, name)),
    '字段不完整或包含未知字段',
  )
}
export function parseControl(
  text: string,
  settings: ClientSettings,
): ServerControl {
  ensure(
    new TextEncoder().encode(text).byteLength <=
      settings.max_control_message_size,
    '控制消息超限',
  )
  const value = objectValue(JSON.parse(text))
  const type = value.type
  if (type === 'bind_success') {
    exactFields(value, [
      'type',
      'binding_generation',
      'replay_start_offset',
      'replay_end_offset',
      'current_cols',
      'current_rows',
    ])
    ensure(
      safeOffset(value.binding_generation) &&
        value.binding_generation > 0 &&
        safeOffset(value.replay_start_offset) &&
        safeOffset(value.replay_end_offset) &&
        safeOffset(value.current_cols) &&
        value.current_cols > 0 &&
        value.current_cols <= settings.max_cols &&
        safeOffset(value.current_rows) &&
        value.current_rows > 0 &&
        value.current_rows <= settings.max_rows,
      '绑定字段非法',
    )
  } else if (type === 'history_truncated') {
    exactFields(value, [
      'type',
      'requested_start_offset',
      'replay_start_offset',
      'replay_end_offset',
      'lost_bytes',
    ])
    ensure(
      safeOffset(value.requested_start_offset) &&
        safeOffset(value.replay_start_offset) &&
        safeOffset(value.replay_end_offset) &&
        safeOffset(value.lost_bytes),
      '截断字段非法',
    )
  } else if (type === 'status') {
    exactFields(value, ['type', 'status'])
    ensure(value.status === 'CONNECTED', '非法状态通知')
  } else if (type === 'error') {
    exactFields(value, ['type', 'code', 'message'])
    ensure(
      typeof value.code === 'string' &&
        wsErrorCodes.has(value.code) &&
        typeof value.message === 'string',
      '非法错误通知',
    )
  } else if (type === 'taken_over') {
    exactFields(value, ['type', 'message'])
    ensure(typeof value.message === 'string', '非法接管通知')
  } else if (type === 'closed') {
    exactFields(value, ['type', 'reason', 'message'])
    ensure(
      typeof value.reason === 'string' &&
        closeReasons.has(value.reason) &&
        typeof value.message === 'string',
      '非法关闭通知',
    )
  } else {
    throw new ClientFailure('protocol', '未知控制消息')
  }
  return value as unknown as ServerControl
}
