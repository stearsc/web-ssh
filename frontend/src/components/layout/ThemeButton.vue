<script setup lang="ts">
import { ref } from 'vue'
const dark = ref(document.documentElement.dataset.theme === 'dark')
function toggle(): void {
  dark.value = !dark.value
  document.documentElement.dataset.theme = dark.value ? 'dark' : 'light'
  try {
    localStorage.setItem('web-ssh-theme', dark.value ? 'dark' : 'light')
  } catch {
    /* 存储不可用时保留本次选择。 */
  }
}
</script>
<template>
  <button
    class="icon-button"
    type="button"
    :aria-label="dark ? '切换白色主题' : '切换深色主题'"
    @click="toggle"
  >
    ◐
  </button>
</template>
