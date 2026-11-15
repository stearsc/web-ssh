import { request } from './management.ts'
export interface SessionView {
  id: string
  server_id: string
  name: string
  host: string
  port: number
  username: string
  status: 'CONNECTING' | 'CONNECTED' | 'DISCONNECTED' | 'CLOSING'
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
  return request(`/api/servers/${id}/sessions`, 'POST', { cols, rows }, signal)
}
export function closeSession(id: string): Promise<unknown> {
  return request(`/api/sessions/${id}`, 'DELETE', {})
}
