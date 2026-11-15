import { ref } from 'vue'
import { request, type AuthView } from '../api/management.ts'

export const identity = ref<AuthView | null>(null)
export const authNotice = ref('')
export const logoutPending = ref(false)
let logoutTask: Promise<void> | null = null
let identityEpoch = 0
let timer: ReturnType<typeof setTimeout> | undefined
let expire = (): void => {}
export function setExpiryHandler(callback: () => void): void {
  expire = callback
}
export function clearIdentity(): void {
  identityEpoch++
  clearTimeout(timer)
  identity.value = null
}
export function acceptIdentity(value: AuthView): void {
  if (logoutPending.value) return
  clearTimeout(timer)
  identity.value = value
  const schedule = (): void => {
    const remaining = Date.parse(value.expires_at) - Date.now()
    if (!Number.isFinite(remaining) || remaining <= 0) {
      expire()
      return
    }
    timer = setTimeout(schedule, Math.min(remaining, 86400000))
  }
  schedule()
}
export async function checkIdentity(): Promise<void> {
  if (logoutPending.value) return
  const epoch = identityEpoch
  const value = await request<AuthView>('/api/auth/me')
  if (epoch === identityEpoch && !logoutPending.value) acceptIdentity(value)
}
export function logout(): Promise<void> {
  logoutPending.value = true
  identityEpoch++
  if (logoutTask) return logoutTask
  logoutTask = request('/api/auth/logout', 'POST', {})
    .then(() => {
      clearIdentity()
      logoutPending.value = false
    })
    .finally(() => {
      logoutTask = null
    })
  return logoutTask
}
// 主动退出未确认时，其他请求的 AUTH_REQUIRED 不能将它覆盖成成功退出。
export function expireIdentity(): boolean {
  if (logoutPending.value) return false
  clearIdentity()
  authNotice.value = '登录已过期，请重新登录'
  return true
}
