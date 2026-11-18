import { request, message } from '../api/management.ts'
import { ApiError } from '../api/client.ts'
import { sessionView, type SessionView } from '../api/sessions.ts'
import type { TerminalClient, ConnectionResult } from './client.ts'

export type TakeoverPhase =
  | 'idle'
  | 'stopping'
  | 'loading'
  | 'confirming'
  | 'connecting'

export interface SessionTakeoverOptions {
  id: string
  client: Pick<TerminalClient, 'running' | 'stop' | 'connectTakeover'>
  invalidateRecovery: () => void
  allowed: () => boolean
  canBegin: () => boolean
  isEnded: () => boolean
  markEnded: () => void
  changed: () => void
  finished: (result: ConnectionResult) => void
}

// 每个 Pane 私有的主动接管动作所有者；不建 WebSocket，不重试，不持有 Client 生命周期。
export class SessionTakeover {
  phase: TakeoverPhase = 'idle'
  candidate: SessionView | null = null
  error = ''
  message = ''
  private sequence = 0
  private controller: AbortController | null = null
  private options: SessionTakeoverOptions

  constructor(options: SessionTakeoverOptions) {
    this.options = options
  }

  get canPrepare(): boolean {
    return this.phase === 'idle' && this.admissionError() === null
  }

  // 仅供确认按钮禁用使用；安全性不依赖 UI，confirm() 自己会再做最终检查。
  get canConfirm(): boolean {
    return (
      this.phase === 'confirming' &&
      this.candidate !== null &&
      this.startError() === null
    )
  }

  // 用户取消：只在尚未启动连接的阶段可用。
  cancel(): void {
    if (this.phase === 'idle' || this.phase === 'connecting') return
    this.invalidate()
  }

  // 只允许 Pane.stop()/close() 调用，调用方负责同时停止 Client。
  invalidate(): void {
    this.sequence++
    this.controller?.abort()
    this.controller = null
    this.candidate = null
    this.phase = 'idle'
    this.options.changed()
  }

  async prepare(): Promise<void> {
    // 检查点 1：prepare 开始时；此时允许 Client 有运行中的任务（随后 stop）。
    if (this.phase !== 'idle') return
    const rejected = this.admissionError()
    if (rejected !== null) {
      this.fail(rejected)
      return
    }
    const sequence = ++this.sequence
    this.options.invalidateRecovery()
    this.candidate = null
    this.error = ''
    this.message = ''
    this.phase = 'stopping'
    this.options.changed()
    try {
      await this.options.client.stop() // 完整 stop/drain 屏障
    } catch (error) {
      if (this.isCurrent(sequence)) this.fail(message(error))
      return
    }
    if (!this.isCurrent(sequence)) return
    if (this.options.client.running) {
      this.fail('原终端任务尚未停止，请稍后重新准备接管')
      return
    }
    const controller = new AbortController()
    this.controller = controller
    this.phase = 'loading'
    this.options.changed()
    try {
      const value = await request<unknown>(
        `/api/sessions/${encodeURIComponent(this.options.id)}`,
        'GET',
        undefined,
        controller.signal,
      )
      if (!this.isCurrent(sequence)) return
      const view = sessionView(value, this.options.id)
      if (view.status !== 'CONNECTED' && view.status !== 'DISCONNECTED') {
        this.fail('Session 当前状态不能接管')
        return
      }
      // 检查点 2：GET 返回后、进入 confirming 前；不通过时不展示看似有效的确认状态。
      if (this.startError() !== null) {
        this.fail('Session 或终端状态已变化，请重新准备接管')
        return
      }
      this.candidate = view
      this.phase = 'confirming'
      this.options.changed()
    } catch (error) {
      if (!this.isCurrent(sequence)) return
      if (error instanceof ApiError && error.code === 'SESSION_NOT_FOUND')
        this.options.markEnded()
      this.fail(message(error))
    } finally {
      if (this.sequence === sequence) this.controller = null
    }
  }

  async confirm(): Promise<void> {
    const candidate = this.candidate
    if (this.phase !== 'confirming' || candidate === null) return
    const sequence = ++this.sequence // 首次 await 前消费候选，重复点击无效
    this.candidate = null
    // 检查点 3：最终检查，紧贴 connectTakeover 之前，二者之间没有 await；
    // 不重新查询详情、不重新获取代次，仍使用候选快照的 binding_generation。
    const rejected = this.startError()
    if (rejected !== null) {
      this.fail(`${rejected}，请重新准备接管`)
      return
    }
    this.phase = 'connecting'
    this.error = ''
    this.options.changed()
    let result: ConnectionResult
    try {
      // start() 的同步异常也在此捕获。
      result = await this.options.client.connectTakeover(
        candidate.binding_generation,
      )
    } catch (error) {
      if (this.sequence === sequence) this.fail(message(error))
      return
    }
    if (this.sequence !== sequence) return
    this.phase = 'idle'
    this.message = result.connected
      ? '已接管该 Session'
      : `接管未完成：${result.message}`
    this.options.changed()
    this.options.finished(result)
  }

  // 三个检查点共用的 Pane 级准入；只读取注入的判断，不复制 Pane 状态。
  private admissionError(): string | null {
    if (!this.options.allowed()) return '当前终端不允许新操作'
    if (this.options.isEnded()) return 'Session 已结束'
    if (!this.options.canBegin()) return '当前终端状态不允许接管'
    return null
  }

  // stop 屏障之后使用：在 Pane 级准入之外，还要求 Client 没有运行中的任务。
  private startError(): string | null {
    const rejected = this.admissionError()
    if (rejected !== null) return rejected
    return this.options.client.running ? 'Client 仍有运行中的任务' : null
  }

  // sequence 已变化说明已被失效；准入条件消失但尚未失效时就地复位，避免卡在中间阶段。
  private isCurrent(sequence: number): boolean {
    if (this.sequence !== sequence) return false
    if (this.options.allowed()) return true
    this.invalidate()
    return false
  }

  private fail(text: string): void {
    this.phase = 'idle'
    this.candidate = null
    this.error = text
    this.options.changed()
  }
}
