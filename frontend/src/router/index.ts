import { createRouter, createWebHistory } from 'vue-router'
import HomePage from '../pages/HomePage.vue'
import LoginPage from '../pages/LoginPage.vue'
import { ApiError } from '../api/client'
import { onAuthExpired } from '../api/management'
import {
  identity,
  checkIdentity,
  clearIdentity,
  authNotice,
  setExpiryHandler,
  logoutPending,
  expireIdentity,
} from '../stores/auth'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: HomePage },
    { path: '/login', component: LoginPage },
  ],
})
router.beforeEach(async (to) => {
  if (logoutPending.value) return to.path === '/login' ? true : '/login'
  try {
    await checkIdentity()
    if (logoutPending.value) return to.path === '/login' ? true : '/login'
    return to.path === '/login' ? '/' : true
  } catch (error) {
    if (logoutPending.value) return to.path === '/login' ? true : '/login'
    clearIdentity()
    if (to.path === '/login') return true
    authNotice.value =
      error instanceof ApiError && error.code === 'AUTH_REQUIRED'
        ? '请登录后继续'
        : '无法验证登录状态，请重试'
    return '/login'
  }
})
const expired = (): void => {
  const wasLoggedIn = identity.value !== null
  if (!expireIdentity()) return
  if (wasLoggedIn && router.currentRoute.value.path !== '/login')
    void router.replace('/login')
}
onAuthExpired(expired)
setExpiryHandler(expired)
