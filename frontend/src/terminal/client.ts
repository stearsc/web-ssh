import { request } from '../api/management.ts'
import { ApiError } from '../api/client.ts'
import {
  TerminalOutputTracker,
  ClientFailure,
  parseControl,
  type ClientSettings,
  type TerminalPort,
} from './output.ts'
export function clientSettings(
  values: Record<string, number>,
): ClientSettings {
  const keys = [
    'max_control_message_size',
    'max_input_message_size',
    'output_chunk_size',
    'credential_timeout',
    'bind_timeout',
    'operation_wait_timeout',
    'write_timeout',
    'max_cols',
    'max_rows',
    'write_queue_bytes',
    'write_drain_timeout',
    'reconnect_max_attempts',
    'reconnect_delay',
    'reconnect_stable_time',
  ]
  for (const key of keys)
    if (!Number.isSafeInteger(values[key]) || values[key] <= 0)
      throw new Error('终端配置无效')
  const timers = [
    'credential_timeout',
    'bind_timeout',
    'operation_wait_timeout',
    'write_timeout',
    'write_drain_timeout',
    'reconnect_delay',
    'reconnect_stable_time',
  ]
  if (
    timers.some((key) => values[key] * 1000 > 2147483647) ||
    (values.bind_timeout +
      values.operation_wait_timeout +
      values.write_timeout) *
      1000 >
      2147483647 ||
    values.reconnect_max_attempts > 10 ||
    values.reconnect_stable_time < values.reconnect_delay ||
    values.max_cols > 65535 ||
    values.max_rows > 65535
  )
    throw new Error('终端配置超出范围')
  return values as unknown as ClientSettings
}

export type RecoveryPhase =
  | 'idle'
  | 'preparing'
  | 'connecting'
  | 'replaying'
  | 'online'
  | 'retry_wait'
  | 'stopping'
  | 'stopped'
  | 'disposed'
export interface ConnectionResult {
  connected: boolean
  code: string
  message: string
}
export interface RecoveryState {
  taskId: number
  phase: RecoveryPhase
  attempt: number
  max: number
  code: string
  message: string
  diagnostic: boolean
  exhausted: boolean
}
export type ConnectionMode = 'attach' | 'takeover'
export type TerminalClientTaskConflictReason =
  | 'mode_mismatch'
  | 'generation_mismatch'
  | 'task_stopping'
function taskConflictMessage(reason: TerminalClientTaskConflictReason): string {
  if (reason === 'mode_mismatch') return '活动终端任务使用了不同的连接模式'
  if (reason === 'generation_mismatch')
    return '活动终端任务使用了不同的初始绑定代次'
  return '请等待旧任务停止'
}
export class TerminalClientTaskConflictError extends Error {
  readonly reason: TerminalClientTaskConflictReason
  constructor(reason: TerminalClientTaskConflictReason) {
    super(taskConflictMessage(reason))
    this.name = 'TerminalClientTaskConflictError'
    this.reason = reason
  }
}
interface AttemptResult {
  code: string
  message: string
  retry: boolean
  keepQueued: boolean
  stable: boolean
}
interface RecoveryTask {
  id: number
  stopped: boolean
  retries: number
  // 初始参数定义任务身份；循环内由 takeover 转入 attach 不改变它们。
  readonly initialMode: ConnectionMode
  readonly initialExpectedGeneration: number
  initialModeConsumed: boolean
  // 只能由当前任务收到并通过校验的 bind_success 更新。
  confirmedGeneration: number | null
  connection: Promise<ConnectionResult>
  connected: (value: ConnectionResult) => void
  done: Promise<RecoveryState>
}
interface AttemptPlan {
  mode: ConnectionMode
  expectedGeneration: number
}
interface PreparedCredential extends AttemptPlan {
  credential: string
}
export const retryCodes = new Set([
  'BIND_BUSY',
  'INVALID_CREDENTIAL',
  'BIND_TIMEOUT',
  'OUTPUT_GAP',
  'SLOW_CLIENT',
  'SERVER_SHUTTING_DOWN',
  'INTERNAL_ERROR',
  'CLIENT_OVERLOAD',
  'CLIENT_TERMINAL_ERROR',
  'NETWORK_ERROR',
  'CLIENT_BIND_TIMEOUT',
  'CREDENTIAL_LIMIT',
  'CONNECTION_LIMIT_REACHED',
])
// 一个标签只拥有一个长期任务；连接 Promise 仅反馈该任务的首次绑定前缀。
export class TerminalClient {
  readonly output: TerminalOutputTracker
  private settings: ClientSettings
  private sessionId: string
  private generation: number
  private notice: (value: string) => void
  private changed: (connected: boolean) => void
  private notify: (state: RecoveryState) => void
  private allowed: () => boolean
  private received: () => void
  private socket: WebSocket | null = null
  private controller: AbortController | null = null
  private finish:
    | ((result: AttemptResult, closeCode?: number) => void)
    | null = null
  private cancelDelay: (() => void) | null = null
  private task: RecoveryTask | null = null
  private sequence = 0
  private stopping: Promise<void> | null = null
  private disposed = false
  private disposeTask: Promise<void> | null = null
  private sending = false
  private sendTask: Promise<void> | null = null
  private pendingSize: { cols: number; rows: number } | null = null
  constructor(
    id: string,
    generation: number,
    settings: ClientSettings,
    create: () => TerminalPort,
    notice: (value: string) => void,
    changed: (connected: boolean) => void,
    notify: (state: RecoveryState) => void = () => {},
    allowed: () => boolean = () => true,
    received: () => void = () => {},
  ) {
    this.sessionId = id
    this.generation = generation
    this.settings = settings
    this.notice = notice
    this.changed = changed
    this.notify = notify
    this.allowed = allowed
    this.received = received
    this.output = new TerminalOutputTracker(
      create,
      settings,
      notice,
      () => {
        this.finish?.(
          this.result('CLIENT_TERMINAL_ERROR', '终端处理失败，正在恢复'),
        )
      },
      () => {
        if (this.task && this.admitted()) this.publish(this.task, 'online')
      },
    )
  }
  get running(): boolean {
    return this.task !== null || this.stopping !== null
  }
  get taskId(): number {
    return this.sequence
  }
  get currentGeneration(): number {
    return this.generation
  }
  private admitted(task = this.task): boolean {
    return (
      task !== null &&
      this.task === task &&
      !task.stopped &&
      !this.disposed &&
      this.allowed()
    )
  }
  get canInput(): boolean {
    return (
      this.admitted() &&
      this.socket?.readyState === WebSocket.OPEN &&
      this.output.inputEnabled
    )
  }
  private defaultExpectedGeneration(): number {
    return this.task?.initialExpectedGeneration ?? this.generation
  }
  connect(
    expectedGeneration = this.defaultExpectedGeneration(),
  ): Promise<ConnectionResult> {
    return this.start(expectedGeneration, 'attach').connection
  }
  run(
    expectedGeneration = this.defaultExpectedGeneration(),
  ): Promise<RecoveryState> {
    return this.start(expectedGeneration, 'attach').done
  }
  connectTakeover(expectedGeneration: number): Promise<ConnectionResult> {
    return this.start(expectedGeneration, 'takeover').connection
  }
  runTakeover(expectedGeneration: number): Promise<RecoveryState> {
    return this.start(expectedGeneration, 'takeover').done
  }
  get completion(): Promise<RecoveryState> | null {
    return this.task?.done ?? null
  }
  private start(expected: number, mode: ConnectionMode): RecoveryTask {
    if (this.disposed || !this.allowed())
      throw new Error('终端已关闭或登录操作正在结束')
    if (!Number.isSafeInteger(expected) || expected < 0)
      throw new Error('非法预期绑定代次')
    if (mode !== 'attach' && mode !== 'takeover')
      throw new Error('非法连接模式')
    if (this.stopping || this.task?.stopped)
      throw new TerminalClientTaskConflictError('task_stopping')
    if (this.task) {
      if (this.task.initialMode !== mode)
        throw new TerminalClientTaskConflictError('mode_mismatch')
      if (this.task.initialExpectedGeneration !== expected)
        throw new TerminalClientTaskConflictError('generation_mismatch')
      return this.task
    }
    let connected!: (value: ConnectionResult) => void
    const connection = new Promise<ConnectionResult>((resolve) => {
      connected = resolve
    })
    const task: RecoveryTask = {
      id: ++this.sequence,
      stopped: false,
      retries: 0,
      initialMode: mode,
      initialExpectedGeneration: expected,
      initialModeConsumed: false,
      confirmedGeneration: null,
      connection,
      connected,
      done: null!,
    }
    this.generation = expected
    this.task = task
    // 推迟第一处异步执行，先发布任务身份；stop 可封闭尚未创建 controller 的窗口。
    task.done = Promise.resolve().then(() => this.loop(task))
    return task
  }
  private publish(
    task: RecoveryTask,
    phase: RecoveryPhase,
    code = '',
    message = '',
    exhausted = false,
  ): RecoveryState {
    const state: RecoveryState = {
      taskId: task.id,
      phase,
      attempt: task.retries,
      max: this.settings.reconnect_max_attempts,
      code,
      message,
      exhausted,
      diagnostic:
        phase === 'stopped' &&
        ['SESSION_NOT_FOUND', 'SESSION_NOT_READY'].includes(code),
    }
    if (
      task.id === this.sequence &&
      !this.disposed &&
      (this.admitted(task) || phase === 'stopping' || phase === 'stopped')
    )
      this.notify(state)
    return state
  }
  private result(
    code: string,
    message = code,
    keepQueued = false,
  ): AttemptResult {
    return {
      code,
      message,
      keepQueued,
      retry: retryCodes.has(code),
      stable: false,
    }
  }
  private async loop(task: RecoveryTask): Promise<RecoveryState> {
    let result = this.result('USER_STOPPED', '已停止恢复', true)
    let exhausted = false
    try {
      while (this.admitted(task)) {
        // 每一轮在首个 await 前确定并消费初始 mode；任何首轮失败都只能转 attach。
        const attempt = this.nextAttemptGeneration(task)
        try {
          await this.output.settle(false)
          await this.sendTask
          if (!this.admitted(task)) break
          const offset = this.output.requestedStartOffset()
          this.publish(task, 'preparing')
          const prepared = await this.prepareCredential(task, attempt)
          if (!this.admitted(task)) break
          result = await this.attempt(task, prepared, offset)
        } catch (error) {
          if (!this.admitted(task)) break
          const code =
            error instanceof ApiError
              ? error.code
              : error instanceof ClientFailure
                ? 'INVALID_MESSAGE'
                : 'NETWORK_ERROR'
          result = this.result(
            code,
            error instanceof Error ? error.message : '连接失败',
          )
        }
        task.connected({
          connected: false,
          code: result.code,
          message: result.message,
        })
        await this.output.settle(result.keepQueued)
        await this.sendTask
        if (!this.admitted(task) || !result.retry) break
        if (result.stable) task.retries = 0
        if (task.retries >= this.settings.reconnect_max_attempts) {
          exhausted = true
          break
        }
        task.retries++
        this.publish(task, 'retry_wait', result.code, result.message)
        await this.waitForRetry(task)
      }
    } catch (error) {
      result = this.result(
        'CLIENT_TERMINAL_ERROR',
        error instanceof Error ? error.message : '终端清理失败',
      )
    } finally {
      if (task.stopped || !this.allowed())
        result = this.result('USER_STOPPED', '已停止恢复', true)
      task.connected({
        connected: false,
        code: result.code,
        message: result.message,
      })
      this.pendingSize = null
      if (this.task === task) this.task = null
    }
    return this.publish(
      task,
      'stopped',
      result.code,
      exhausted
        ? `自动恢复次数已用尽（${result.code}），请刷新 Session 状态后手动恢复`
        : result.message,
      exhausted,
    )
  }
  private nextAttemptGeneration(task: RecoveryTask): AttemptPlan {
    if (!task.initialModeConsumed) {
      // 在任何 Prepare 异步边界前消费；即使该请求失败，后续也只能 attach。
      task.initialModeConsumed = true
      return {
        mode: task.initialMode,
        expectedGeneration: task.initialExpectedGeneration,
      }
    }
    return {
      mode: 'attach',
      expectedGeneration:
        task.confirmedGeneration ?? task.initialExpectedGeneration,
    }
  }
  private async prepareCredential(
    task: RecoveryTask,
    attempt: AttemptPlan,
  ): Promise<PreparedCredential> {
    if (!this.admitted(task)) throw new Error('已停止')
    const controller = new AbortController()
    this.controller = controller
    const timer = setTimeout(
      () => controller.abort(),
      this.settings.credential_timeout * 1000,
    )
    try {
      const value = await request<{ credential: string; expires_at: string }>(
        `/api/sessions/${encodeURIComponent(this.sessionId)}/takeover`,
        'POST',
        {
          mode: attempt.mode,
          expected_generation: attempt.expectedGeneration,
        },
        controller.signal,
      )
      if (controller.signal.aborted || !this.admitted(task))
        throw new Error('凭证请求已取消')
      if (
        !value ||
        typeof value.credential !== 'string' ||
        !/^[A-Za-z0-9_-]{43}$/.test(value.credential) ||
        typeof value.expires_at !== 'string' ||
        !Number.isFinite(Date.parse(value.expires_at))
      )
        throw new ClientFailure('protocol', '绑定凭证响应非法')
      return { ...attempt, credential: value.credential }
    } finally {
      clearTimeout(timer)
      if (this.controller === controller) this.controller = null
    }
  }
  private attempt(
    task: RecoveryTask,
    prepared: PreparedCredential,
    offset: number,
  ): Promise<AttemptResult> {
    if (!this.admitted(task))
      return Promise.resolve(this.result('USER_STOPPED'))
    this.output.begin(offset)
    this.publish(task, 'connecting')
    const expected = prepared.expectedGeneration
    return new Promise<AttemptResult>((resolve) => {
      const url = new URL(
        `/ws/sessions/${encodeURIComponent(this.sessionId)}`,
        location.href,
      )
      url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const socket = new WebSocket(url)
      socket.binaryType = 'arraybuffer'
      this.socket = socket
      let ended = false
      let prefixed = false
      let bindTimer: ReturnType<typeof setTimeout>
      const valid = (): boolean =>
        !ended && this.admitted(task) && this.socket === socket
      const finish = (result: AttemptResult, closeCode = 1000): void => {
        if (ended) return
        ended = true
        // 在断线时消费；排空过程不能为已经断线的连接补足稳定时长。
        result.stable = this.output.consumeStability(Date.now())
        clearTimeout(bindTimer)
        this.output.freeze(result.keepQueued)
        socket.onopen =
          socket.onmessage =
          socket.onclose =
          socket.onerror =
            null
        if (this.socket === socket) this.socket = null
        if (this.finish === finish) this.finish = null
        this.pendingSize = null
        this.changed(false)
        if (!prefixed)
          task.connected({
            connected: false,
            code: result.code,
            message: result.message,
          })
        try {
          if (socket.readyState < WebSocket.CLOSING) socket.close(closeCode)
        } finally {
          resolve(result)
        }
      }
      this.finish = finish
      if (!valid()) {
        finish(this.result('USER_STOPPED', '已停止', true))
        return
      }
      bindTimer = setTimeout(
        () => finish(this.result('CLIENT_BIND_TIMEOUT', '绑定超时')),
        (this.settings.bind_timeout +
          this.settings.operation_wait_timeout +
          this.settings.write_timeout) *
          1000,
      )
      socket.onopen = () => {
        if (!valid()) return
        try {
          socket.send(
            JSON.stringify({
              type: 'bind',
              credential: prepared.credential,
              requested_start_offset: offset,
            }),
          )
        } catch {
          finish(this.result('NETWORK_ERROR', '绑定发送失败'))
        }
      }
      socket.onmessage = (event: MessageEvent) => {
        if (!valid()) return
        try {
          if (event.data instanceof ArrayBuffer) {
            this.output.binary(new Uint8Array(event.data))
            this.received()
            return
          }
          if (typeof event.data !== 'string')
            throw new ClientFailure('protocol', '非法终端消息')
          const message = parseControl(event.data, this.settings)
          if (message.type === 'bind_success') {
            if (message.binding_generation !== expected + 1)
              throw new ClientFailure('protocol', '绑定代次不连续')
            this.output.bind(message)
            task.confirmedGeneration = message.binding_generation
            this.generation = message.binding_generation
          } else if (message.type === 'history_truncated')
            this.output.truncated(message)
          else if (message.type === 'status') {
            if (!this.output.prefixComplete)
              throw new ClientFailure('protocol', '消息顺序错误')
          } else {
            finish(
              this.result(
                message.type === 'error' ? message.code : message.type,
                message.message,
                message.type !== 'error',
              ),
            )
            return
          }
          if (this.output.prefixComplete && !prefixed) {
            prefixed = true
            clearTimeout(bindTimer)
            this.changed(true)
            this.publish(
              task,
              this.output.inputEnabled ? 'online' : 'replaying',
            )
            this.flushSize()
            task.connected({ connected: true, code: '', message: '' })
          }
          this.received()
        } catch (error) {
          const overload =
            error instanceof ClientFailure && error.kind === 'overload'
          finish(
            this.result(
              overload ? 'CLIENT_OVERLOAD' : 'INVALID_MESSAGE',
              error instanceof Error ? error.message : '终端协议错误',
            ),
            overload ? 4003 : 1000,
          )
        }
      }
      socket.onerror = () => {
        if (valid()) finish(this.result('NETWORK_ERROR', '连接中断'))
      }
      socket.onclose = (event: CloseEvent) => {
        if (!valid()) return
        const result = this.result(
          `WS_${event.code}`,
          event.code === 4001
            ? '连接已因其他连接接管而关闭（4001）'
            : `连接已结束（${event.code}）`,
          [1000, 4001].includes(event.code),
        )
        result.retry = [1006, 1011, 1012, 1013, 4002].includes(event.code)
        finish(result)
      }
    })
  }
  private waitForRetry(task: RecoveryTask): Promise<void> {
    return new Promise((resolve) => {
      const finish = (): void => {
        clearTimeout(timer)
        if (this.cancelDelay === finish) this.cancelDelay = null
        resolve()
      }
      const timer = setTimeout(finish, this.settings.reconnect_delay * 1000)
      this.cancelDelay = finish
      if (!this.admitted(task)) finish()
    })
  }
  send(data: Uint8Array): Promise<void> {
    if (this.sending) {
      this.notice('上一批输入尚未发送完成，本次输入未发送')
      return Promise.resolve()
    }
    const task = this.sendData(data)
    this.sendTask = task
    void task.finally(() => {
      if (this.sendTask === task) this.sendTask = null
      this.flushSize()
    })
    return task
  }
  private async sendData(data: Uint8Array): Promise<void> {
    if (!this.canInput || data.byteLength === 0) return
    if (this.sending) {
      this.notice('上一批输入尚未发送完成，本次输入未发送')
      return
    }
    this.sending = true
    const socket = this.socket!
    try {
      for (let offset = 0; offset < data.byteLength; ) {
        const chunk = data.subarray(
          offset,
          Math.min(
            data.byteLength,
            offset + this.settings.max_input_message_size,
          ),
        )
        const deadline = Date.now() + this.settings.write_timeout * 1000
        while (
          socket.bufferedAmount + chunk.byteLength >
          this.settings.max_input_message_size
        ) {
          if (
            this.socket !== socket ||
            !this.canInput ||
            Date.now() >= deadline
          )
            throw new Error('输入未全部发送，请核对远端结果；不会自动补发')
          await new Promise((resolve) => setTimeout(resolve, 16))
        }
        if (this.socket !== socket || !this.canInput)
          throw new Error('输入未全部发送，请核对远端结果')
        socket.send(chunk)
        offset += chunk.byteLength
      }
    } catch (error) {
      if (this.admitted())
        this.notice(error instanceof Error ? error.message : '输入发送失败')
    } finally {
      this.sending = false
      this.flushSize()
    }
  }
  resize(cols: number, rows: number): void {
    if (
      !this.admitted() ||
      !Number.isInteger(cols) ||
      !Number.isInteger(rows) ||
      cols < 1 ||
      rows < 1 ||
      cols > this.settings.max_cols ||
      rows > this.settings.max_rows
    )
      return
    this.pendingSize = { cols, rows }
    this.flushSize()
  }
  private flushSize(): void {
    if (this.sending || this.sendTask) return
    const task = this.sendSize()
    this.sendTask = task
    void task.finally(() => {
      if (this.sendTask === task) this.sendTask = null
      // 空发送结束与 resize 到达可能交错，仍要发送此时最新的一项。
      if (
        this.pendingSize &&
        this.admitted() &&
        this.output.prefixComplete &&
        this.socket?.readyState === WebSocket.OPEN
      )
        this.flushSize()
    })
  }
  private async sendSize(): Promise<void> {
    const socket = this.socket
    const size = this.pendingSize
    if (
      !this.admitted() ||
      this.sending ||
      !size ||
      !socket ||
      socket.readyState !== WebSocket.OPEN ||
      !this.output.prefixComplete
    )
      return
    this.sending = true
    const deadline = Date.now() + this.settings.write_timeout * 1000
    try {
      while (
        this.admitted() &&
        this.pendingSize &&
        this.socket === socket &&
        this.output.prefixComplete &&
        socket.readyState === WebSocket.OPEN
      ) {
        const current = this.pendingSize
        const data = JSON.stringify({
          type: 'resize',
          ...current,
          binding_generation: this.generation,
        })
        if (
          socket.bufferedAmount + new TextEncoder().encode(data).byteLength <=
          this.settings.max_input_message_size
        ) {
          this.pendingSize = null
          socket.send(data)
          return
        }
        if (Date.now() >= deadline) {
          this.pendingSize = null
          this.notice('尺寸同步未完成，请重新调整窗口')
          return
        }
        await new Promise((resolve) => setTimeout(resolve, 16))
      }
    } catch {
      this.pendingSize = null
      if (this.admitted()) this.notice('尺寸同步失败')
    } finally {
      this.sending = false
    }
  }
  stop(): Promise<void> {
    const task = this.task
    // 必须先封闭准入；此时可能尚无 controller、socket 或 finish。
    if (task) task.stopped = true
    this.pendingSize = null
    this.controller?.abort()
    this.cancelDelay?.()
    this.finish?.(this.result('USER_STOPPED', '已停止恢复', true))
    if (this.stopping) return this.stopping
    if (task) this.publish(task, 'stopping', 'USER_STOPPED', '正在停止')
    const stopping = (async () => {
      if (task) await task.done
      await this.sendTask
      await this.output.settle(true)
    })()
    this.stopping = stopping.finally(() => {
      this.stopping = null
    })
    return this.stopping
  }
  dispose(): Promise<void> {
    if (this.disposeTask) return this.disposeTask
    this.disposed = true
    const stopping = this.stop()
    const disposing = this.output.dispose()
    this.disposeTask = Promise.allSettled([stopping, disposing]).then(
      (results) => {
        for (const result of results)
          if (result.status === 'rejected') throw result.reason
      },
    )
    return this.disposeTask
  }
}
