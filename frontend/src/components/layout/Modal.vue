<script setup lang="ts">
import { onMounted, ref, useId } from 'vue'
defineProps<{ title: string }>()
const emit = defineEmits<{ close: [] }>()
const dialog = ref<HTMLDialogElement>()
const titleId = useId()
onMounted(() => dialog.value?.showModal())
</script>
<template>
  <dialog
    ref="dialog"
    :aria-labelledby="titleId"
    @cancel.prevent="emit('close')"
  >
    <div class="dialog-header">
      <h2 :id="titleId">{{ title }}</h2>
      <button
        type="button"
        class="icon-button"
        aria-label="关闭弹窗"
        @click="emit('close')"
      >
        ×
      </button>
    </div>
    <slot />
  </dialog>
</template>
