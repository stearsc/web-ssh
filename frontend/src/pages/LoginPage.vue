<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { request, message } from '../api/management'
import { authNotice, logoutPending, logout } from '../stores/auth'
import ThemeButton from '../components/layout/ThemeButton.vue'
const password = ref('')
const busy = ref(false)
const router = useRouter()
const controller = new AbortController()
onBeforeUnmount(() => {
  password.value = ''
  controller.abort()
})
async function submit(): Promise<void> {
  if (busy.value || logoutPending.value) return
  busy.value = true
  authNotice.value = ''
  try {
    await request(
      '/api/auth/login',
      'POST',
      { password: password.value },
      controller.signal,
    )
    password.value = ''
    await router.replace('/')
  } catch (error) {
    if (!controller.signal.aborted) authNotice.value = message(error)
  } finally {
    password.value = ''
    busy.value = false
  }
}
async function retryLogout(): Promise<void> {
  try {
    await logout()
    authNotice.value = '已退出登录'
  } catch (error) {
    authNotice.value = '退出未完成，请重试。' + message(error)
  }
}
</script>
<template>
  <main class="login-page">
    <ThemeButton class="login-theme" />
    <form class="login-card" @submit.prevent="submit">
      <h1 class="wordmark">Web SSH</h1>
      <label class="sr-only" for="password">管理密码</label>
      <input
        id="password"
        v-model="password"
        type="password"
        autocomplete="current-password"
        placeholder="管理密码"
        required
        :disabled="busy || logoutPending"
      />
      <button class="primary login-submit" :disabled="busy || logoutPending">
        {{ busy ? '正在登录…' : '登录' }}
      </button>
      <p v-if="authNotice" role="alert" class="error">{{ authNotice }}</p>
      <button v-if="logoutPending" type="button" @click="retryLogout">
        重试退出登录
      </button>
    </form>
  </main>
</template>
