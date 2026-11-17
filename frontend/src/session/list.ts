import { ref, type Ref } from 'vue'
import { message } from '../api/management.ts'
import type { SessionView } from '../api/sessions.ts'

export type SessionListLoader = (
  serverID: string,
  signal: AbortSignal,
) => Promise<SessionView[]>

interface SessionListControllerOptions {
  load: SessionListLoader
  isAllowed: () => boolean
  currentIdentity: () => unknown
}

export class SessionListController {
  readonly items: Ref<SessionView[]> = ref([])
  readonly loading = ref(false)
  readonly error = ref<string | null>(null)
  readonly lastLoadedAt: Ref<Date | null> = ref(null)
  readonly stale = ref(false)

  private sequence = 0
  private controller: AbortController | null = null
  private disposed = false

  private readonly options: SessionListControllerOptions

  constructor(options: SessionListControllerOptions) {
    this.options = options
  }

  get hasSnapshot(): boolean {
    return this.lastLoadedAt.value !== null
  }

  async refresh(serverID: string): Promise<void> {
    if (this.disposed || !this.options.isAllowed()) return

    const sequence = ++this.sequence
    const identity = this.options.currentIdentity()
    this.controller?.abort()
    const controller = new AbortController()
    this.controller = controller
    this.loading.value = true
    this.error.value = null
    // 刷新开始后，已有行仍是上次成功查询的快照，直到本次请求成功。
    if (this.hasSnapshot) this.stale.value = true

    try {
      const items = await this.options.load(serverID, controller.signal)
      if (!this.current(sequence, controller, identity)) return
      this.items.value = items
      this.lastLoadedAt.value = new Date()
      this.stale.value = false
    } catch (error) {
      if (!this.current(sequence, controller, identity)) return
      // 失败不能把已有的成功快照伪装成空列表或最新结果。
      this.stale.value = this.hasSnapshot
      this.error.value = message(error)
    } finally {
      // 旧请求的 finally 不能关闭新请求的 loading。
      if (!this.current(sequence, controller, identity)) return
      this.loading.value = false
      this.controller = null
    }
  }

  invalidate(): void {
    this.sequence++
    this.controller?.abort()
    this.controller = null
    this.loading.value = false
    if (this.hasSnapshot) this.stale.value = true
  }

  dispose(): void {
    this.disposed = true
    this.sequence++
    this.controller?.abort()
    this.controller = null
    this.loading.value = false
  }

  private current(
    sequence: number,
    controller: AbortController,
    identity: unknown,
  ): boolean {
    return (
      !this.disposed &&
      sequence === this.sequence &&
      !controller.signal.aborted &&
      this.options.isAllowed() &&
      Object.is(this.options.currentIdentity(), identity)
    )
  }
}
