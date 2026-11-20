import { ApiError } from '../api/client.ts'
import {
  checkFile,
  uploadFile,
  MAX_UPLOAD_SIZE,
  SFTP_CANCEL_MESSAGE,
  type FileCheck,
  type FileUploadResult,
} from '../api/files.ts'

export type UploadState =
  | 'waiting'
  | 'uploading'
  | 'confirming'
  | 'done'
  | 'skipped'
  | 'canceled'
  | 'failed'
export interface UploadItem {
  key: number
  file: File
  state: UploadState
  progress: number
  error: string
  path: string
}
interface UploadAPI {
  check: (
    id: string,
    directory: string,
    filename: string,
    signal?: AbortSignal,
  ) => Promise<FileCheck>
  upload: (
    id: string,
    directory: string,
    file: File,
    overwrite: boolean,
    signal: AbortSignal,
    progress: (percent: number) => void,
  ) => Promise<FileUploadResult>
}
const sessionGone = (error: unknown): boolean =>
  error instanceof ApiError &&
  [
    'SESSION_NOT_FOUND',
    'SFTP_SESSION_CLOSED',
    'SFTP_CANCEL_FAILED',
    'AUTH_REQUIRED',
  ].includes(error.code)

// 每次打开窗口创建一个绑定 Session 的队列；没有轮询、重试或页面活动标签依赖。
export class UploadQueue {
  readonly sessionId: string
  directory = ''
  readonly items: UploadItem[] = []
  running = false
  notice = ''
  confirming: UploadItem | null = null
  private current: UploadItem | null = null
  private controller: AbortController | null = null
  private confirmation: ((overwrite: boolean) => void) | null = null
  private task: Promise<void> | null = null
  private canceled = false
  private ended = false
  private nextKey = 0
  private readonly changed: () => void
  private readonly api: UploadAPI
  constructor(
    sessionId: string,
    changed: () => void,
    api: UploadAPI = { check: checkFile, upload: uploadFile },
  ) {
    this.sessionId = sessionId
    this.changed = changed
    this.api = api
  }
  get pending(): boolean {
    return (
      this.running ||
      this.items.some((item) =>
        ['waiting', 'uploading', 'confirming'].includes(item.state),
      )
    )
  }
  setDirectory(value: string): void {
    if (!this.running) {
      this.directory = value
      this.changed()
    }
  }
  add(files: Iterable<File>): void {
    if (this.running || this.ended) return
    for (const file of files)
      this.items.push({
        key: ++this.nextKey,
        file,
        state: file.size > MAX_UPLOAD_SIZE ? 'failed' : 'waiting',
        progress: 0,
        error:
          file.size > MAX_UPLOAD_SIZE
            ? '单文件不能超过 512 MB（512 MiB）'
            : '',
        path: '',
      })
    this.changed()
  }
  confirm(overwrite: boolean): void {
    this.confirmation?.(overwrite)
  }
  start(): Promise<void> {
    if (this.task) return this.task
    if (
      this.ended ||
      !this.directory.startsWith('/') ||
      !this.items.some((item) => item.state === 'waiting')
    )
      return Promise.resolve()
    const directory = this.directory
    this.canceled = false
    this.running = true
    this.changed()
    this.task = this.run(directory).finally(() => {
      this.running = false
      this.current = null
      this.controller = null
      this.task = null
      this.changed()
    })
    return this.task
  }
  cancelCurrent(): void {
    if (
      !this.current ||
      !['waiting', 'uploading', 'confirming'].includes(this.current.state)
    )
      return
    this.current.state = 'canceled'
    this.confirmation?.(false)
    this.controller?.abort()
    this.changed()
  }
  cancelAll(): Promise<void> {
    this.canceled = true
    for (const item of this.items)
      if (item.state === 'waiting') item.state = 'canceled'
    this.cancelCurrent()
    this.changed()
    return this.task ?? Promise.resolve()
  }
  stopSession(): Promise<void> {
    this.ended = true
    if (!this.notice)
      this.notice = 'Session 已结束或正在关闭，剩余文件已取消。'
    return this.cancelAll()
  }
  private failSession(error: unknown, item: UploadItem): void {
    this.ended = true
    this.canceled = true
    if (error instanceof ApiError && error.code === 'SFTP_CANCEL_FAILED') {
      item.state = 'failed'
      item.error = this.notice = SFTP_CANCEL_MESSAGE
    } else
      this.notice =
        error instanceof Error
          ? error.message
          : 'Session 不可用，剩余文件已取消。'
    for (const other of this.items)
      if (other.state === 'waiting') other.state = 'canceled'
  }
  private async run(directory: string): Promise<void> {
    for (const item of this.items) {
      if (this.canceled || this.ended) break
      if (item.state !== 'waiting') continue
      this.current = item
      this.controller = new AbortController()
      let aborted = false
      try {
        const target = await this.api.check(
          this.sessionId,
          directory,
          item.file.name,
          this.controller.signal,
        )
        if (this.controller.signal.aborted)
          throw new DOMException('已取消', 'AbortError')
        item.path = target.path
        let overwrite = false
        if (target.exists) {
          item.state = 'confirming'
          this.confirming = item
          overwrite = await new Promise<boolean>((resolve) => {
            this.confirmation = resolve
            this.changed()
          })
          this.confirmation = null
          this.confirming = null
          if (
            (item.state as UploadState) === 'canceled' ||
            this.canceled ||
            this.ended
          )
            continue
          if (!overwrite) {
            item.state = 'skipped'
            this.changed()
            continue
          }
        }
        item.state = 'uploading'
        this.changed()
        await this.api.upload(
          this.sessionId,
          directory,
          item.file,
          overwrite,
          this.controller.signal,
          (percent) => {
            item.progress = percent
            this.changed()
          },
        )
        item.state = 'done'
        item.progress = 100
      } catch (error) {
        aborted = this.controller.signal.aborted
        item.state = aborted ? 'canceled' : 'failed'
        item.error = aborted
          ? ''
          : error instanceof Error
            ? error.message
            : '上传失败'
        if (sessionGone(error)) this.failSession(error, item)
      } finally {
        this.confirmation = null
        this.confirming = null
        if (aborted) {
          // XHR abort 不证明后端已回收。复用 check 的完成屏障等待；不是进度轮询或文件重试。
          try {
            await this.api.check(this.sessionId, directory, item.file.name)
          } catch (error) {
            if (sessionGone(error)) this.failSession(error, item)
            else
              item.error = `取消后的远端状态检查失败：${error instanceof Error ? error.message : '连接不可用'}`
          }
        }
        this.changed()
      }
    }
  }
}
