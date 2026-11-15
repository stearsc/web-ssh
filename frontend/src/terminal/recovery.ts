import { request, message } from '../api/management.ts'
import { ApiError } from '../api/client.ts'
import { sessionView, type SessionView } from '../api/sessions.ts'
import type { TerminalClient, ConnectionResult } from './client.ts'

// 每个 Pane 私有的详情/人工候选所有者；不执行自动恢复，不维护第二套绑定代次。
export class SessionRecovery {
  snapshot: SessionView | null = null
  candidate: SessionView | null = null
  loading = false
  acting = false
  ended = false
  error = ''
  private sequence = 0
  private controller: AbortController | null = null
  private client: TerminalClient
  private id: string
  private allowed: () => boolean
  private changed: () => void
  constructor(
    id: string,
    client: TerminalClient,
    allowed: () => boolean,
    changed: () => void,
  ) {
    this.id = id
    this.client = client
    this.allowed = allowed
    this.changed = changed
  }
  get canRecover(): boolean {
    return (
      this.allowed() &&
      !this.ended &&
      !this.acting &&
      !this.loading &&
      !this.client.running &&
      this.candidate !== null &&
      ['DISCONNECTED', 'CONNECTED'].includes(this.candidate.status)
    )
  }
  invalidate(): void {
    this.sequence++
    this.controller?.abort()
    this.controller = null
    this.candidate = null
    this.loading = false
    this.acting = false
    this.changed()
  }
  async refresh(
    manual = true,
    sourceTask = this.client.taskId,
  ): Promise<void> {
    if (!this.allowed() || this.acting || this.client.running) return
    this.invalidate()
    const sequence = this.sequence
    const controller = new AbortController()
    this.controller = controller
    this.snapshot = null
    this.error = ''
    this.loading = true
    this.changed()
    const current = (): boolean =>
      this.sequence === sequence &&
      !controller.signal.aborted &&
      this.allowed() &&
      this.client.taskId === sourceTask &&
      !this.client.running
    try {
      // 无本地定时器；用户/生命周期取消负责使查询和候选同步失效。
      const value = await request<unknown>(
        `/api/sessions/${encodeURIComponent(this.id)}`,
        'GET',
        undefined,
        controller.signal,
      )
      if (!current()) return
      const view = sessionView(value, this.id)
      this.snapshot = view
      if (manual && !this.ended) this.candidate = view
    } catch (error) {
      if (!current()) return
      this.error = message(error)
      this.candidate = null
      if (error instanceof ApiError && error.code === 'SESSION_NOT_FOUND')
        this.ended = true
    } finally {
      if (this.sequence === sequence) {
        this.controller = null
        this.loading = false
        this.changed()
      }
    }
  }
  async recover(): Promise<ConnectionResult | null> {
    if (!this.canRecover) return null
    const candidate = this.candidate!
    this.invalidate() // 首次 await 前消费候选；重复点击不重置预算。
    const sequence = this.sequence
    this.acting = true
    this.error = ''
    this.changed()
    try {
      await this.client.stop()
      if (sequence !== this.sequence || !this.allowed() || this.ended)
        return null
      return await this.client.connect(candidate.binding_generation)
    } catch (error) {
      if (sequence === this.sequence && this.allowed())
        this.error = message(error)
      return null
    } finally {
      if (sequence === this.sequence) {
        this.acting = false
        this.changed()
      }
    }
  }
  stop(): Promise<void> {
    this.invalidate()
    return this.client.stop()
  }
}
