import { ApiError } from './client.ts'

export interface HostKeyView {
  host: string
  port: number
  algorithm: string
  fingerprint: string
}
export interface ServerView {
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
export async function request<T>(
  path: string,
  method = 'GET',
  data?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(path, {
    method,
    credentials: 'same-origin',
    cache: 'no-store',
    signal,
    headers:
      data === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: data === undefined ? undefined : JSON.stringify(data),
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
