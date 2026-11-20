<script setup lang="ts">
import { computed, ref } from 'vue'
import Modal from '../layout/Modal.vue'
import type { UploadQueue, UploadState } from '../../files/upload'
const props = defineProps<{
  queue: UploadQueue
  revision: number
  label: string
}>()
const emit = defineEmits<{ close: [] }>()
const confirmClosing = ref(false)
const labels: Record<UploadState, string> = {
  waiting: '等待中',
  uploading: '上传中',
  confirming: '等待确认',
  done: '已完成',
  skipped: '已跳过',
  canceled: '已取消',
  failed: '失败',
}
const counts = computed(() => {
  void props.revision
  return ['done', 'failed', 'skipped', 'canceled']
    .map(
      (state) =>
        `${labels[state as UploadState]} ${props.queue.items.filter((item) => item.state === state).length}`,
    )
    .join(' · ')
})
function selectFiles(event: Event): void {
  const input = event.target as HTMLInputElement
  props.queue.add(input.files ?? [])
  input.value = ''
}
function close(): void {
  if (props.queue.pending) confirmClosing.value = true
  else emit('close')
}
async function cancelAndClose(): Promise<void> {
  await props.queue.cancelAll()
  emit('close')
}
</script>
<template>
  <Modal title="上传文件" @close="close">
    <div class="file-upload" :data-revision="revision">
      <p>{{ label }} · Session {{ queue.sessionId }}</p>
      <label
        >远程服务器目录（绝对路径）<input
          :value="queue.directory"
          @input="
            queue.setDirectory(($event.target as HTMLInputElement).value)
          "
          :disabled="queue.running"
          placeholder="例如 /home/ubuntu/deploy"
      /></label>
      <p class="muted">
        这是 SSH 服务器上的目录；不会自动创建。单文件最大 512 MB（512 MiB）。
      </p>
      <label
        >选择本地文件<input
          type="file"
          multiple
          :disabled="queue.running"
          @change="selectFiles"
      /></label>
      <div class="dialog-actions">
        <button
          :disabled="
            queue.running ||
            !queue.directory.startsWith('/') ||
            !queue.items.some((item) => item.state === 'waiting')
          "
          @click="queue.start()"
        >
          开始上传
        </button>
        <button :disabled="!queue.running" @click="queue.cancelCurrent()">
          取消当前文件
        </button>
        <button :disabled="!queue.pending" @click="queue.cancelAll()">
          取消整个队列
        </button>
      </div>
      <p role="status">{{ counts }}</p>
      <p v-if="queue.notice" role="alert">{{ queue.notice }}</p>
      <ol class="file-upload-list">
        <li v-for="item in queue.items" :key="item.key">
          <strong>{{ item.file.name }}</strong> · {{ labels[item.state] }}
          <template v-if="item.state === 'uploading'"
            ><progress :value="item.progress" max="100" />
            {{ item.progress }}%<span v-if="item.progress === 100">
              · 等待远端提交</span
            ></template
          >
          <p v-if="item.error" role="alert">{{ item.error }}</p>
        </li>
      </ol>
      <div v-if="queue.confirming" class="notice" role="alert">
        <p>
          远程文件已存在：<span class="mono">{{
            queue.confirming.path
          }}</span>
        </p>
        <button @click="queue.confirm(false)">跳过此文件</button>
        <button class="danger" @click="queue.confirm(true)">确认覆盖</button>
      </div>
      <div v-if="confirmClosing" class="notice" role="alert">
        <p>队列尚未结束。取消当前及剩余文件后关闭？已成功上传的文件保留。</p>
        <button @click="confirmClosing = false">继续上传</button>
        <button class="danger" @click="cancelAndClose">取消上传并关闭</button>
      </div>
    </div>
  </Modal>
</template>
<style scoped>
.file-upload {
  width: min(44rem, 80vw);
}
.file-upload label {
  display: grid;
  gap: 0.4rem;
  margin-block: 0.8rem;
}
.file-upload-list {
  max-height: 35vh;
  overflow: auto;
  padding-left: 1.5rem;
  overflow-wrap: anywhere;
}
.file-upload-list li {
  margin-block: 0.6rem;
}
.file-upload progress {
  max-width: 10rem;
  vertical-align: middle;
}
.file-upload .notice {
  overflow-wrap: anywhere;
}
</style>
