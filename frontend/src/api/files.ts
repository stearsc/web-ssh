import { ApiError } from './client.ts'
import { notifyAuthExpired, request } from './management.ts'

export const MAX_UPLOAD_SIZE = 512 * 1024 * 1024
export const SFTP_CANCEL_MESSAGE =
  'SFTP 操作无法正常取消，已关闭 SSH 连接以释放资源。'
export interface FileCheck {
  exists: boolean
  path: string
}
export interface FileUploadResult {
  path: string
  size: number
}

export async function checkFile(
  sessionId: string,
  directory: string,
  filename: string,
  signal?: AbortSignal,
): Promise<FileCheck> {
  const value = await request<FileCheck>(
    `/api/sessions/${sessionId}/files/check`,
    'POST',
    { directory, filename },
    signal,
  )
  if (
    !value ||
    typeof value.exists !== 'boolean' ||
    typeof value.path !== 'string'
  )
    throw new ApiError('INVALID_RESPONSE', '文件检查响应无效')
  return value
}

// 进度仅表示 HTTP 请求体发送量；远端提交成功只由 200/{code:0} 确认。
export function uploadFile(
  sessionId: string,
  directory: string,
  file: File,
  overwrite: boolean,
  signal: AbortSignal,
  progress: (percent: number) => void,
): Promise<FileUploadResult> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException('已取消', 'AbortError'))
      return
    }
    const xhr = new XMLHttpRequest()
    const abort = (): void => xhr.abort()
    const finish = (error?: unknown, result?: FileUploadResult): void => {
      signal.removeEventListener('abort', abort)
      xhr.onload = xhr.onerror = xhr.onabort = null
      xhr.upload.onprogress = null
      if (error !== undefined) reject(error)
      else resolve(result!)
    }
    xhr.open('POST', `/api/sessions/${sessionId}/files`)
    xhr.withCredentials = true
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable)
        progress(
          Math.min(100, Math.round((event.loaded * 100) / event.total)),
        )
    }
    xhr.onabort = () => finish(new DOMException('已取消', 'AbortError'))
    xhr.onerror = () =>
      finish(
        new ApiError('NETWORK_ERROR', '上传连接中断，远端提交结果可能未知'),
      )
    xhr.onload = () => {
      let value: unknown
      try {
        value = JSON.parse(xhr.responseText)
      } catch {
        finish(new ApiError('INVALID_RESPONSE', '上传响应无效'))
        return
      }
      if (
        !value ||
        typeof value !== 'object' ||
        !('code' in value) ||
        !('message' in value) ||
        typeof value.message !== 'string' ||
        !('data' in value)
      ) {
        finish(new ApiError('INVALID_RESPONSE', '上传响应无效'))
        return
      }
      if (xhr.status !== 200 || value.code !== 0) {
        if (value.code === 'AUTH_REQUIRED') notifyAuthExpired()
        finish(
          new ApiError(
            typeof value.code === 'string' ? value.code : 'INVALID_RESPONSE',
            value.message,
          ),
        )
        return
      }
      const data = value.data as Partial<FileUploadResult> | null
      if (
        !data ||
        typeof data.path !== 'string' ||
        !Number.isSafeInteger(data.size) ||
        data.size !== file.size
      ) {
        finish(new ApiError('INVALID_RESPONSE', '上传结果的路径或字节数无效'))
        return
      }
      finish(undefined, data as FileUploadResult)
    }
    signal.addEventListener('abort', abort, { once: true })
    const form = new FormData()
    form.append('directory', directory)
    form.append('overwrite', String(overwrite))
    form.append('file', file, file.name)
    try {
      xhr.send(form)
    } catch (error) {
      finish(error)
    }
  })
}
