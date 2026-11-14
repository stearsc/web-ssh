<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, getHealth } from '../api/client'

const state = ref<'loading' | 'ready' | 'error'>('loading')
const error = ref('')
let request: AbortController | undefined

async function checkHealth(): Promise<void> {
  request?.abort()
  const current = new AbortController()
  request = current
  state.value = 'loading'
  error.value = ''
  try {
    await getHealth(current.signal)
    if (current.signal.aborted) return
    state.value = 'ready'
  } catch (cause: unknown) {
    if (current.signal.aborted) return
    state.value = 'error'
    error.value = cause instanceof ApiError ? cause.message : '无法连接服务，请重试'
  }
}

onMounted(checkHealth)
onBeforeUnmount(() => request?.abort())
</script>

<template>
  <main class="workspace" aria-label="工作区">
    <div v-if="state === 'error'" class="error-message" role="alert">
      <span>{{ error }}</span><button @click="checkHealth">重试</button>
    </div>
  </main>
  <footer class="status-bar" role="status" aria-live="polite">
    {{ state === 'loading' ? '正在连接服务…' : state === 'ready' ? '服务已就绪' : '服务不可用' }}
  </footer>
</template>
