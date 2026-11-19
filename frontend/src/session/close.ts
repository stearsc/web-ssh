import { ApiError } from '../api/client.ts'
import { message, request } from '../api/management.ts'
import {
  closeSession,
  sessionView,
  type SessionStatus,
} from '../api/sessions.ts'

export type CloseState =
  | 'idle'
  | 'pending'
  | 'requested'
  | 'unknown'
  | 'failed'
  | 'ended'

export type CloseServerStatus =
  | 'CONNECTING'
  | 'CONNECTED'
  | 'DISCONNECTED'
  | 'CLOSING'
  | 'NOT_FOUND'
  | null

export interface CloseSnapshot {
  sessionId: string
  state: CloseState
  message: string
  serverStatus: CloseServerStatus
  localStopFailed: boolean
  disposed: boolean
  updatedAt: number
}

// Pane 只通过该窄接口读取/操作关闭状态；DELETE 与查询任务始终归协调器所有。
export interface CloseHandle {
  readonly sessionId: string
  snapshot(): CloseSnapshot
  requestClose(): Promise<CloseSnapshot>
  retryClose(): Promise<CloseSnapshot>
  cancelWaiting(): void
  refreshClose(): Promise<CloseSnapshot>
  subscribe(listener: (snapshot: CloseSnapshot) => void): () => void
  registerLocalStop(stop: () => Promise<void>): () => void
  // 服务端 closed 控制消息或详情 SESSION_NOT_FOUND 的权威 ended 证据入口。
  markEnded(): void
}

const CLOSE_MESSAGES = {
  pending: '正在请求关闭',
  requested: '已请求关闭，正在回收',
  unknown: '关闭结果未知',
  cancelled: '已停止等待',
  failed: '关闭请求未被服务端接受',
  ended: '会话已结束或正在完成关闭（远端已不可接管）',
} as const

const QUERYABLE_STATES: ReadonlySet<CloseState> = new Set([
  'requested',
  'unknown',
  'failed',
])

// 每个 Session 唯一的关闭任务；协调器是 DELETE、详情查询和状态发布的唯一所有者。
class CloseTask {
  readonly sessionId: string
  state: CloseState = 'idle'
  message = ''
  serverStatus: CloseServerStatus = null
  localStopFailed = false
  updatedAt = 0
  // epoch 隔离 DELETE/重试，queryEpoch 隔离详情 GET；两者不能合并。
  epoch = 0
  queryEpoch = 0
  deleteController: AbortController | null = null
  queryController: AbortController | null = null
  promise: Promise<CloseSnapshot> | null = null
  localStop: (() => Promise<void>) | null = null
  localStopToken = 0
  readonly listeners = new Set<(snapshot: CloseSnapshot) => void>()

  constructor(sessionId: string) {
    this.sessionId = sessionId
  }
}

export class CloseCoordinator {
  private readonly tasks = new Map<string, CloseTask>()
  private readonly handles = new Map<string, CloseHandle>()
  private disposed = false

  getCloseState(sessionId: string): CloseSnapshot {
    return this.snapshotOf(this.tasks.get(sessionId), sessionId)
  }

  // 返回绑定到具体 Session 的稳定句柄，HomePage 注入给 Pane。
  handle(sessionId: string): CloseHandle {
    const existing = this.handles.get(sessionId)
    if (existing) return existing
    const handle: CloseHandle = {
      sessionId,
      snapshot: () => this.getCloseState(sessionId),
      requestClose: () => this.requestClose(sessionId),
      retryClose: () => this.retryClose(sessionId),
      cancelWaiting: () => this.cancelWaiting(sessionId),
      refreshClose: () => this.refreshClose(sessionId),
      subscribe: (listener) => this.subscribeClose(sessionId, listener),
      registerLocalStop: (stop) => this.registerLocalStop(sessionId, stop),
      markEnded: () => this.markEnded(sessionId),
    }
    this.handles.set(sessionId, handle)
    return handle
  }

  requestClose(sessionId: string): Promise<CloseSnapshot> {
    if (this.disposed) return Promise.resolve(this.getCloseState(sessionId))
    const task = this.taskOf(sessionId)
    // pending 复用同一个 Promise；requested/unknown/failed/ended 只返回当前快照。
    if (task.state === 'pending')
      return task.promise ?? Promise.resolve(this.snapshotOf(task, sessionId))
    if (task.state !== 'idle')
      return Promise.resolve(this.snapshotOf(task, sessionId))
    return this.startClose(task)
  }

  retryClose(sessionId: string): Promise<CloseSnapshot> {
    if (this.disposed) return Promise.resolve(this.getCloseState(sessionId))
    const task = this.taskOf(sessionId)
    if (task.state === 'pending')
      return task.promise ?? Promise.resolve(this.snapshotOf(task, sessionId))
    if (!QUERYABLE_STATES.has(task.state))
      return Promise.resolve(this.snapshotOf(task, sessionId))
    return this.startClose(task)
  }

  cancelWaiting(sessionId: string): void {
    if (this.disposed) return
    const task = this.tasks.get(sessionId)
    if (!task || task.state !== 'pending') return
    // 本地停止等待不是撤销服务端关闭；递增 epoch 让在途 DELETE 结果静默丢弃。
    task.epoch++
    task.deleteController?.abort()
    task.deleteController = null
    task.state = 'unknown'
    task.message = CLOSE_MESSAGES.cancelled
    task.updatedAt = Date.now()
    this.publish(task)
  }

  async refreshClose(sessionId: string): Promise<CloseSnapshot> {
    if (this.disposed) return this.getCloseState(sessionId)
    const task = this.tasks.get(sessionId)
    if (!task) return this.getCloseState(sessionId)
    // idle 没有关闭意图；pending 由 DELETE 唯一决定；ended 是 tombstone。
    if (!QUERYABLE_STATES.has(task.state))
      return this.snapshotOf(task, sessionId)

    const queryEpoch = ++task.queryEpoch
    task.queryController?.abort()
    const controller = new AbortController()
    task.queryController = controller
    try {
      const value = await request<unknown>(
        `/api/sessions/${encodeURIComponent(sessionId)}`,
        'GET',
        undefined,
        controller.signal,
      )
      if (!this.isQueryCurrent(task, queryEpoch, controller))
        return this.snapshotOf(task, sessionId)
      const view = sessionView(value, sessionId)
      task.serverStatus = view.status
      this.applyServerStatus(task, view.status)
      task.updatedAt = Date.now()
      this.publish(task)
    } catch (error) {
      if (!this.isQueryCurrent(task, queryEpoch, controller))
        return this.snapshotOf(task, sessionId)
      if (error instanceof ApiError && error.code === 'SESSION_NOT_FOUND') {
        task.serverStatus = 'NOT_FOUND'
        this.end(task)
      } else {
        // 查询失败不提供任何服务端事实，只记录错误并保持原关闭状态。
        task.message = message(error)
        task.updatedAt = Date.now()
        this.publish(task)
      }
    } finally {
      if (task.queryController === controller) task.queryController = null
    }
    return this.snapshotOf(task, sessionId)
  }

  subscribeClose(
    sessionId: string,
    listener: (snapshot: CloseSnapshot) => void,
  ): () => void {
    if (this.disposed) return () => {}
    const task = this.taskOf(sessionId)
    task.listeners.add(listener)
    this.notify(listener, this.snapshotOf(task, sessionId))
    return () => {
      task.listeners.delete(listener)
    }
  }

  registerLocalStop(
    sessionId: string,
    stop: () => Promise<void>,
  ): () => void {
    if (this.disposed) return () => {}
    const task = this.taskOf(sessionId)
    const token = ++task.localStopToken
    task.localStop = stop
    // last-wins：旧注销函数只有在仍持有当前 token 时才生效。
    return () => {
      if (task.localStopToken !== token) return
      task.localStop = null
      task.localStopToken++
    }
  }

  // 服务端 closed 控制消息是关闭不可逆证据；只由 Pane 的 closed 状态回调触发。
  markEnded(sessionId: string): void {
    if (this.disposed) return
    const task = this.taskOf(sessionId)
    if (task.state === 'ended') return
    // 使所有在途请求失效，避免迟到结果覆盖 ended tombstone。
    task.epoch++
    task.queryEpoch++
    task.deleteController?.abort()
    task.deleteController = null
    task.queryController?.abort()
    task.queryController = null
    this.end(task)
  }

  // 页面卸载：只停止本地等待并隔离迟到响应，不声称撤销服务端关闭。幂等。
  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    for (const task of this.tasks.values()) {
      task.epoch++
      task.queryEpoch++
      task.deleteController?.abort()
      task.deleteController = null
      task.queryController?.abort()
      task.queryController = null
      task.listeners.clear()
      task.localStop = null
      task.localStopToken++
    }
    this.handles.clear()
  }

  private taskOf(sessionId: string): CloseTask {
    const existing = this.tasks.get(sessionId)
    if (existing) return existing
    const task = new CloseTask(sessionId)
    this.tasks.set(sessionId, task)
    return task
  }

  private startClose(task: CloseTask): Promise<CloseSnapshot> {
    const epoch = ++task.epoch
    // 关闭意图优先于详情快照：在途 GET 立即失效。
    task.queryEpoch++
    task.queryController?.abort()
    task.queryController = null
    task.deleteController?.abort()
    const controller = new AbortController()
    task.deleteController = controller

    // 同步建立 pending 并发布，早于本地 stop 与 DELETE。
    task.state = 'pending'
    task.message = CLOSE_MESSAGES.pending
    task.updatedAt = Date.now()
    this.publish(task)

    // 同步调用本地 stop 的失效部分；DELETE 在同步部分返回后才启动。
    this.startLocalStop(task)
    const promise = this.runDelete(task, epoch, controller)
    task.promise = promise
    return promise
  }

  private startLocalStop(task: CloseTask): void {
    const stop = task.localStop
    if (!stop) return
    let result: Promise<void>
    try {
      result = stop()
    } catch (error) {
      // 本地停止同步抛错不能跳过关闭意图或 DELETE，也不能恢复接管准入。
      task.localStopFailed = true
      task.updatedAt = Date.now()
      this.publish(task)
      console.error('本地终端停止同步失败', task.sessionId, error)
      return
    }
    Promise.resolve(result).catch((error: unknown) => {
      if (this.disposed) return
      task.localStopFailed = true
      task.updatedAt = Date.now()
      this.publish(task)
      console.error('本地终端停止异步失败', task.sessionId, error)
    })
  }

  private async runDelete(
    task: CloseTask,
    epoch: number,
    controller: AbortController,
  ): Promise<CloseSnapshot> {
    try {
      await closeSession(task.sessionId, controller.signal)
      if (!this.isCurrent(task, epoch))
        return this.snapshotOf(task, task.sessionId)
      // 当前链路只有合法 HTTP 200 {code:0} 能证明服务端处理了关闭请求。
      task.state = 'requested'
      task.message = CLOSE_MESSAGES.requested
      task.updatedAt = Date.now()
      this.publish(task)
    } catch {
      if (!this.isCurrent(task, epoch))
        return this.snapshotOf(task, task.sessionId)
      // 网络、abort、网关、非 JSON、5xx、已知业务码都无法证明服务端未接受关闭。
      task.state = 'unknown'
      task.message = CLOSE_MESSAGES.unknown
      task.updatedAt = Date.now()
      this.publish(task)
    } finally {
      if (task.deleteController === controller) task.deleteController = null
      if (task.state !== 'pending') task.promise = null
    }
    return this.snapshotOf(task, task.sessionId)
  }

  private applyServerStatus(task: CloseTask, status: SessionStatus): void {
    if (status === 'CLOSING') {
      // CLOSING 只可能来自服务端 closeLocked，证明关闭已被接受。
      if (task.state === 'unknown' || task.state === 'failed') {
        task.state = 'requested'
        task.message = CLOSE_MESSAGES.requested
      } else if (task.state === 'requested') {
        task.message = CLOSE_MESSAGES.requested
      }
      return
    }
    // CONNECTED/DISCONNECTED/CONNECTING 只是查询瞬间的快照，不能解除关闭意图。
    if (task.state === 'requested')
      task.message = '服务端尚未显示关闭中'
  }

  private end(task: CloseTask): void {
    task.state = 'ended'
    task.message = CLOSE_MESSAGES.ended
    task.updatedAt = Date.now()
    this.publish(task)
  }

  private snapshotOf(
    task: CloseTask | undefined,
    sessionId: string,
  ): CloseSnapshot {
    if (!task)
      return {
        sessionId,
        state: 'idle',
        message: '',
        serverStatus: null,
        localStopFailed: false,
        disposed: this.disposed,
        updatedAt: 0,
      }
    return {
      sessionId: task.sessionId,
      state: task.state,
      message: task.message,
      serverStatus: task.serverStatus,
      localStopFailed: task.localStopFailed,
      disposed: this.disposed,
      updatedAt: task.updatedAt,
    }
  }

  private isCurrent(task: CloseTask, epoch: number): boolean {
    return (
      !this.disposed &&
      this.tasks.get(task.sessionId) === task &&
      task.epoch === epoch
    )
  }

  private isQueryCurrent(
    task: CloseTask,
    queryEpoch: number,
    controller: AbortController,
  ): boolean {
    return (
      !this.disposed &&
      this.tasks.get(task.sessionId) === task &&
      task.queryEpoch === queryEpoch &&
      !controller.signal.aborted
    )
  }

  private publish(task: CloseTask): void {
    if (this.disposed) return
    const snapshot = this.snapshotOf(task, task.sessionId)
    for (const listener of [...task.listeners])
      this.notify(listener, snapshot)
  }

  private notify(
    listener: (snapshot: CloseSnapshot) => void,
    snapshot: CloseSnapshot,
  ): void {
    try {
      listener(snapshot)
    } catch (error) {
      console.error('关闭状态订阅回调失败', snapshot.sessionId, error)
    }
  }
}
