<script setup lang="ts">
import { reactive, ref, onBeforeUnmount } from 'vue'
import {
  request,
  message,
  type ServerView,
  type ServerInput,
} from '../../api/management'
import Modal from '../layout/Modal.vue'
const props = defineProps<{ server: ServerView | null }>()
const emit = defineEmits<{ close: []; saved: [ServerView] }>()
const form = reactive<ServerInput>({
  name: props.server?.name ?? '',
  host: props.server?.host ?? '',
  port: props.server?.port ?? 22,
  username: props.server?.username ?? 'root',
  password: '',
  group_name: props.server?.group_name ?? '',
  remark: props.server?.remark ?? '',
})
const busy = ref(false)
const error = ref('')
const controller = new AbortController()
onBeforeUnmount(() => {
  form.password = ''
  controller.abort()
})
async function save(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const value = await request<ServerView>(
      props.server ? `/api/servers/${props.server.id}` : '/api/servers',
      props.server ? 'PUT' : 'POST',
      form,
      controller.signal,
    )
    form.password = ''
    emit('saved', value)
  } catch (cause) {
    if (!controller.signal.aborted) error.value = message(cause)
  } finally {
    busy.value = false
  }
}
</script>
<template>
  <Modal :title="server ? '编辑服务器' : '添加服务器'" @close="emit('close')">
    <form @submit.prevent="save">
      <div class="dialog-body">
        <div class="form-grid">
          <label
            >名称<input v-model="form.name" required maxlength="128"
          /></label>
          <label
            >分组<input
              v-model="form.group_name"
              maxlength="128"
              placeholder="未分组"
          /></label>
          <label
            >Host<input
              v-model="form.host"
              required
              maxlength="253"
              placeholder="IP 或域名"
              autocapitalize="none"
          /></label>
          <label
            >Port<input
              v-model.number="form.port"
              type="number"
              required
              min="1"
              max="65535"
          /></label>
          <label
            >Username<input
              v-model="form.username"
              required
              maxlength="128"
              autocomplete="off"
          /></label>
          <label
            >Password<input
              v-model="form.password"
              type="password"
              :required="!server"
              maxlength="4096"
              autocomplete="new-password"
              :placeholder="server ? '留空保留原密码' : 'SSH 密码'"
          /></label>
          <label class="full"
            >备注<textarea v-model="form.remark" maxlength="1024" />
          </label>
        </div>
        <p v-if="error" role="alert" class="error">{{ error }}</p>
      </div>
      <div class="dialog-footer">
        <button type="button" @click="emit('close')">取消</button
        ><button class="primary" :disabled="busy">
          {{ busy ? '保存中…' : '保存服务器' }}
        </button>
      </div>
    </form>
  </Modal>
</template>
