import { ref } from 'vue'
import { request, type AuthView } from '../api/management'

export const identity = ref<AuthView | null>(null)
export const authNotice = ref('')
export const logoutPending = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
let expire = (): void => {}
export function setExpiryHandler(callback: () => void): void {
  expire = callback
}
export function clearIdentity(): void {
  clearTimeout(timer)
  identity.value = null
}
export function acceptIdentity(value: AuthView): void {
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
  acceptIdentity(await request<AuthView>('/api/auth/me'))
}
export async function logout(): Promise<void> {
  logoutPending.value = true
  await request('/api/auth/logout', 'POST', {})
  logoutPending.value = false
  clearIdentity()
}
