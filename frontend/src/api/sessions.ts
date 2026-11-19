import { ApiError } from './client.ts'
import { request } from './management.ts'

export type SessionStatus =
  | 'CONNECTING'
  | 'CONNECTED'
  | 'DISCONNECTED'
  | 'CLOSING'

export interface SessionView {
  id: string
  server_id: string
  name: string
  host: string
  port: number
  username: string
  status: SessionStatus
  binding_generation: number
  current_cols: number
  current_rows: number
  created_at: string
  disconnected_at: string | null
  last_attached_at: string | null
  offline_expires_at: string | null
  remaining_seconds: number | null
}
export function createSession(
  id: string,
  cols: number,
  rows: number,
  signal?: AbortSignal,
): Promise<SessionView> {
  return request(
    `/api/servers/${id}/sessions`,
    'POST',
    { cols, rows },
    signal,
  )
}
export function closeSession(
  id: string,
  signal?: AbortSignal,
): Promise<unknown> {
  return request(`/api/sessions/${id}`, 'DELETE', {}, signal)
}

// 详情是人工恢复唯一代次来源；不能用类型断言或旧 props 填补缺失字段。
export function sessionView(value: unknown, id: string): SessionView {
  const fail = (): never => {
    throw new ApiError('INVALID_RESPONSE', 'Session 详情响应非法')
  }
  if (!value || typeof value !== 'object' || Array.isArray(value))
    return fail()
  const v = value as Record<string, unknown>
  const integer = (n: unknown): n is number =>
    typeof n === 'number' && Number.isSafeInteger(n) && n >= 0
  const time = (s: unknown): boolean =>
    typeof s === 'string' &&
    /^\d{4}-\d{2}-\d{2}T.*Z$/.test(s) &&
    Number.isFinite(Date.parse(s))
  if (
    v.id !== id ||
    !/^[a-f0-9]{32}$/.test(id) ||
    typeof v.server_id !== 'string' ||
    !/^[a-f0-9]{32}$/.test(v.server_id) ||
    !['name', 'host', 'username'].every(
      (key) => typeof v[key] === 'string',
    ) ||
    !['CONNECTING', 'CONNECTED', 'DISCONNECTED', 'CLOSING'].includes(
      v.status as string,
    ) ||
    !integer(v.binding_generation) ||
    !integer(v.port) ||
    v.port < 1 ||
    v.port > 65535 ||
    !integer(v.current_cols) ||
    v.current_cols < 1 ||
    v.current_cols > 65535 ||
    !integer(v.current_rows) ||
    v.current_rows < 1 ||
    v.current_rows > 65535 ||
    !time(v.created_at) ||
    !['disconnected_at', 'last_attached_at', 'offline_expires_at'].every(
      (key) => v[key] === null || time(v[key]),
    ) ||
    !(v.remaining_seconds === null || integer(v.remaining_seconds))
  )
    return fail()
  if (
    v.status === 'DISCONNECTED'
      ? !time(v.offline_expires_at) || !integer(v.remaining_seconds)
      : v.offline_expires_at !== null || v.remaining_seconds !== null
  )
    return fail()
  // 只复制冻结的 SessionView 字段，避免响应中的额外字段进入页面状态。
  return {
    id: v.id,
    server_id: v.server_id,
    name: v.name as string,
    host: v.host as string,
    port: v.port,
    username: v.username as string,
    status: v.status as SessionStatus,
    binding_generation: v.binding_generation,
    current_cols: v.current_cols,
    current_rows: v.current_rows,
    created_at: v.created_at as string,
    disconnected_at: v.disconnected_at as string | null,
    last_attached_at: v.last_attached_at as string | null,
    offline_expires_at: v.offline_expires_at as string | null,
    remaining_seconds: v.remaining_seconds as number | null,
  }
}

export function sessionList(value: unknown): SessionView[] {
  const fail = (): never => {
    throw new ApiError('INVALID_RESPONSE', 'Session 列表响应非法')
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) return fail()
  const items = (value as Record<string, unknown>).items
  if (!Array.isArray(items)) return fail()
  const ids = new Set<string>()
  return items.map((item) => {
    if (
      !item ||
      typeof item !== 'object' ||
      Array.isArray(item) ||
      typeof (item as Record<string, unknown>).id !== 'string'
    )
      return fail()
    const id = (item as Record<string, unknown>).id as string
    if (!/^[a-f0-9]{32}$/.test(id) || ids.has(id)) return fail()
    ids.add(id)
    return sessionView(item, id)
  })
}

export async function listSessions(
  serverID: string,
  signal: AbortSignal,
): Promise<SessionView[]> {
  const params = new URLSearchParams()
  if (serverID) params.set('server_id', serverID)
  const query = params.toString()
  const value = await request<unknown>(
    `/api/sessions${query ? `?${query}` : ''}`,
    'GET',
    undefined,
    signal,
  )
  return sessionList(value)
}
