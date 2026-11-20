import { ApiError } from './client.ts'

export interface HostKeyView {
  host: string
  port: number
  algorithm: string
  fingerprint: string
}
export interface ServerView {
  auth_type: 'password' | 'private_key'
  credential_configured: boolean
  id: string
  name: string
  host: string
  port: number
  username: string
  group_name: string
  remark: string
  host_key: HostKeyView | null
  created_at: string
  updated_at: string
}
export interface ServerInput {
  auth_type?: 'password' | 'private_key'
  passphrase?: string
  name: string
  host: string
  port: number
  username: string
  password?: string
  group_name: string
  remark: string
}
export interface Probe extends HostKeyView {
  probe_id: string
  expires_at: string
  previous_fingerprint: string | null
  reason: 'first_trust' | 'key_changed'
}
export interface AuthView {
  expires_at: string
  client_settings: Record<string, number>
}
export class ManagementError extends ApiError {
  readonly data: unknown
  constructor(code: string, message: string, data: unknown) {
    super(code, message)
    this.data = data
  }
}
let onExpired = (): void => {}
export function onAuthExpired(callback: () => void): void {
  onExpired = callback
}
export function notifyAuthExpired(): void {
  onExpired()
}
export interface RequestOptions {
  // 仅用于需要严格校验 HTTP 状态码的调用方；未设置时保持原有 response.ok 语义。
  expectedStatus?: number
}
export async function request<T>(
  path: string,
  method = 'GET',
  data?: unknown,
  signal?: AbortSignal,
  options?: RequestOptions,
): Promise<T> {
  const response = await fetch(path, {
    method,
    credentials: 'same-origin',
    cache: 'no-store',
    signal,
    headers:
      data === undefined || data instanceof FormData ? undefined : { 'Content-Type': 'application/json' },
    body: data === undefined ? undefined : data instanceof FormData ? data : JSON.stringify(data),
  })
  const body: unknown = await response.json()
  if (
    typeof body !== 'object' ||
    body === null ||
    !('code' in body) ||
    !('message' in body) ||
    typeof body.message !== 'string' ||
    !('data' in body)
  )
    throw new ApiError('INVALID_RESPONSE', '服务返回了无法识别的响应')
  if (
    options?.expectedStatus !== undefined &&
    response.status !== options.expectedStatus
  ) {
    // 严格模式只影响显式传入 expectedStatus 的调用方；AUTH_REQUIRED 副作用保持不变。
    if (body.code === 'AUTH_REQUIRED') onExpired()
    throw new ManagementError(
      typeof body.code === 'string' ? body.code : 'INVALID_RESPONSE',
      body.message,
      body.data,
    )
  }
  if (!response.ok || body.code !== 0) {
    if (body.code === 'AUTH_REQUIRED') onExpired()
    throw new ManagementError(
      typeof body.code === 'string' ? body.code : 'INVALID_RESPONSE',
      body.message,
      body.data,
    )
  }
  return body.data as T
}
export function confirmation(error: unknown): Probe | null {
  if (
    !(error instanceof ManagementError) ||
    error.code !== 'HOST_KEY_CONFIRMATION_REQUIRED'
  )
    return null
  const p = error.data as Partial<Probe> | null
  if (
    !p ||
    typeof p.probe_id !== 'string' ||
    !/^[A-Za-z0-9_-]{43}$/.test(p.probe_id) ||
    typeof p.host !== 'string' ||
    !Number.isInteger(p.port) ||
    typeof p.algorithm !== 'string' ||
    typeof p.fingerprint !== 'string' ||
    typeof p.expires_at !== 'string' ||
    !Number.isFinite(Date.parse(p.expires_at)) ||
    (p.previous_fingerprint !== null &&
      typeof p.previous_fingerprint !== 'string') ||
    !['first_trust', 'key_changed'].includes(p.reason ?? '')
  )
    return null
  return p as Probe
}
export function message(error: unknown): string {
  return error instanceof ApiError ? error.message : '无法连接服务，请重试'
}
