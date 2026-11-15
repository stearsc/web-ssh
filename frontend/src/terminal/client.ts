import { request } from '../api/management.ts'
import {
  TerminalOutputTracker,
  ClientFailure,
  parseControl,
  type ClientSettings,
  type TerminalPort,
} from './output.ts'
export function clientSettings(values: Record<string, number>): ClientSettings {
  const keys = [
    'max_control_message_size',
    'max_input_message_size',
    'output_chunk_size',
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
  return values as unknown as ClientSettings
}
// 只在用户调用 connect 时申请 attach；没有自动重连、代次刷新或 takeover 分支。
export class TerminalClient {
  readonly output: TerminalOutputTracker
  private settings: ClientSettings
  private sessionId: string
  private generation: number
  private notice: (value: string) => void
  private changed: (connected: boolean) => void
  private socket: WebSocket | null = null
  private controller: AbortController | null = null
  private finish: ((keep: boolean, code?: number) => void) | null = null
  private task: Promise<void> | null = null
  private drain: Promise<void> = Promise.resolve()
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
  ) {
    this.sessionId = id
    this.generation = generation
    this.settings = settings
    this.notice = notice
    this.changed = changed
    this.output = new TerminalOutputTracker(create, settings, notice, () => {
      this.finish?.(false)
      this.notice('终端处理失败，请手动恢复')
    })
  }
  get canInput(): boolean {
    return (
      this.socket?.readyState === WebSocket.OPEN && this.output.inputEnabled
    )
  }
  connect(): Promise<void> {
    if (this.disposed) return Promise.reject(new Error('终端已关闭'))
    if (this.task) return this.task
    this.task = this.open().finally(() => {
      this.task = null
    })
    return this.task
  }
  private async open(): Promise<void> {
    this.finish?.(false)
    await this.drain
    await this.output.settle(false)
    if (this.disposed) return
    const controller = new AbortController()
    this.controller = controller
    const timer = setTimeout(
      () => controller.abort(),
      this.settings.bind_timeout * 1000,
    )
    let credential: string
    try {
      const value = await request<{ credential: string; expires_at: string }>(
        `/api/sessions/${this.sessionId}/takeover`,
        'POST',
        { mode: 'attach', expected_generation: this.generation },
        controller.signal,
      )
      if (
        !/^[A-Za-z0-9_-]{43}$/.test(value.credential) ||
        !Number.isFinite(Date.parse(value.expires_at))
      )
        throw new Error('绑定凭证响应非法')
      credential = value.credential
    } finally {
      clearTimeout(timer)
      if (this.controller === controller) this.controller = null
    }
    if (controller.signal.aborted || this.disposed) return
    const offset = this.output.requestedStartOffset()
    this.output.begin(offset)
    const url = new URL(`/ws/sessions/${this.sessionId}`, location.href)
    url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    await new Promise<void>((resolve) => {
      const socket = new WebSocket(url)
      socket.binaryType = 'arraybuffer'
      this.socket = socket
      let ended = false
      const finish = (keep: boolean, code = 1000): void => {
        if (ended) return
        ended = true
        clearTimeout(bindTimer)
        socket.onopen =
          socket.onmessage =
          socket.onclose =
          socket.onerror =
            null
        if (this.socket === socket) this.socket = null
        if (this.finish === finish) this.finish = null
        this.pendingSize = null
        this.output.freeze(keep)
        this.drain = this.output.settle(keep)
        this.changed(false)
        if (socket.readyState < WebSocket.CLOSING) socket.close(code)
        resolve()
      }
      this.finish = finish
      const bindTimer = setTimeout(
        () => {
          this.notice('绑定超时，请手动恢复')
          finish(false)
        },
        (this.settings.bind_timeout +
          this.settings.operation_wait_timeout +
          this.settings.write_timeout) *
          1000,
      )
      socket.onopen = () =>
        socket.send(
          JSON.stringify({
            type: 'bind',
            credential,
            requested_start_offset: offset,
          }),
        )
      socket.onmessage = (event: MessageEvent) => {
        if (ended || this.socket !== socket) return
        try {
          if (event.data instanceof ArrayBuffer) {
            this.output.binary(new Uint8Array(event.data))
            return
          }
          if (typeof event.data !== 'string') throw new Error('非法终端消息')
          const message = parseControl(event.data, this.settings)
          if (message.type === 'bind_success') {
            if (message.binding_generation !== this.generation + 1)
              throw new Error('绑定代次不连续')
            this.output.bind(message)
            this.generation = message.binding_generation
          } else if (message.type === 'history_truncated')
            this.output.truncated(message)
          else if (message.type === 'status') {
            if (!this.output.prefixComplete) throw new Error('消息顺序错误')
          } else {
            this.notice(message.message)
            finish(message.type !== 'error')
            return
          }
          if (this.output.prefixComplete) {
            clearTimeout(bindTimer)
            this.changed(true)
            this.flushSize()
            resolve()
          }
        } catch (error) {
          this.notice(error instanceof Error ? error.message : '终端协议错误')
          finish(
            false,
            error instanceof ClientFailure && error.kind === 'overload'
              ? 4003
              : 1000,
          )
        }
      }
      socket.onerror = () => {
        this.notice('连接已断开，请手动恢复')
        finish(false)
      }
      socket.onclose = (event: CloseEvent) => {
        this.notice('连接已结束，可查询状态或手动恢复')
        finish(event.code === 1000 || event.code === 4001)
      }
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
      this.notice(error instanceof Error ? error.message : '输入发送失败')
    } finally {
      this.sending = false
      this.flushSize()
    }
  }
  resize(cols: number, rows: number): void {
    if (
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
    })
  }
  private async sendSize(): Promise<void> {
    const socket = this.socket
    const size = this.pendingSize
    if (
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
      this.notice('尺寸同步失败')
    } finally {
      this.sending = false
    }
  }
  async stop(): Promise<void> {
    this.controller?.abort()
    this.finish?.(true)
    if (this.task) await this.task.catch(() => {})
    await this.sendTask
    await this.drain
    await this.output.settle(true)
  }
  dispose(): Promise<void> {
    if (this.disposeTask) return this.disposeTask
    this.disposed = true
    const stopping = this.stop()
    const disposing = this.output.dispose()
    this.disposeTask = Promise.allSettled([stopping, disposing]).then(
      (results) => {
        for (const r of results) if (r.status === 'rejected') throw r.reason
      },
    )
    return this.disposeTask
  }
}
