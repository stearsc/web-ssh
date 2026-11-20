<script setup lang="ts">
import { computed, reactive, ref, watch, onBeforeUnmount } from 'vue'
import {
  request, message, confirmation,
  type ServerView, type ServerInput, type Probe,
} from '../../api/management'
import { ApiError } from '../../api/client'
import Modal from '../layout/Modal.vue'
import HostKeyDialog from './HostKeyDialog.vue'
const props = defineProps<{ server: ServerView | null; disabled?: boolean }>()
const emit = defineEmits<{ close: []; saved: [ServerView] }>()
const form = reactive<ServerInput>({
  name: props.server?.name ?? '', host: props.server?.host ?? '',
  port: props.server?.port ?? 22, username: props.server?.username ?? 'root',
  auth_type: props.server?.auth_type ?? 'password', password: '',
  group_name: props.server?.group_name ?? '', remark: props.server?.remark ?? '',
})
const initial = JSON.stringify(form)
const selectedFile = ref<File | null>(null)
const passphrase = ref('')
const busy = ref(false)
const testing = ref(false)
const error = ref('')
const testNotice = ref('')
const probe = ref<Probe | null>(null)
const controller = new AbortController()
let testController: AbortController | null = null
let testTask = 0
const dirty = computed(() => JSON.stringify(form) !== initial || selectedFile.value !== null || passphrase.value !== '')
const needsPassword = computed(() => !props.server || props.server.auth_type === 'private_key')
function cancelTest(): void {
  testTask++
  testController?.abort()
  testController = null
  testing.value = false
  probe.value = null
  testNotice.value = ''
}
watch(dirty, (value) => { if (value) cancelTest() })
watch(() => props.disabled, (value) => { if (value) cancelTest() })
onBeforeUnmount(() => {
  form.password = ''
  passphrase.value = ''
  selectedFile.value = null
  controller.abort()
  cancelTest()
})
function changeAuthentication(): void {
  form.password = ''
  passphrase.value = ''
  selectedFile.value = null
  error.value = ''
  cancelTest()
}
function changeFile(event: Event): void {
  // 每次选择/替换（包括选择同一文件）都重新输入口令，不借用旧值。
  passphrase.value = ''
  const input = event.target as HTMLInputElement
  selectedFile.value = input.files?.[0] ?? null
  input.value = ''
  error.value = ''
  cancelTest()
}
async function save(): Promise<void> {
  if (busy.value || testing.value || props.disabled) return
  busy.value = true
  error.value = ''
  try {
    const input: ServerInput = {
      name: form.name, host: form.host, port: form.port, username: form.username,
      auth_type: form.auth_type, group_name: form.group_name, remark: form.remark,
    }
    let payload: ServerInput | FormData = input
    const encoder = new TextEncoder()
    if (form.auth_type === 'password') {
      if (encoder.encode(form.password ?? '').length > 4096) throw new ApiError('INVALID_REQUEST', '密码不能超过 4096 UTF-8 字节')
      input.password = form.password ?? ''
    } else if (selectedFile.value) {
      if (!selectedFile.value.size || selectedFile.value.size > 65536) throw new ApiError('PRIVATE_KEY_INVALID', '请选择 1～65536 字节的私钥文件')
      if (encoder.encode(passphrase.value).length > 4096) throw new ApiError('INVALID_REQUEST', 'Passphrase 不能超过 4096 UTF-8 字节')
      input.passphrase = passphrase.value
      const multipart = new FormData()
      // 空 filename 保持 metadata 为 JSON part；boundary 始终由浏览器生成。
      multipart.append('metadata', new Blob([JSON.stringify(input)], { type: 'application/json' }), '')
      multipart.append('private_key', selectedFile.value)
      payload = multipart
    } else if (!props.server || props.server.auth_type !== 'private_key') {
      throw new ApiError('INVALID_REQUEST', '请选择私钥文件')
    }
    const value = await request<ServerView>(
      props.server ? `/api/servers/${props.server.id}` : '/api/servers',
      props.server ? 'PUT' : 'POST', payload, controller.signal,
    )
    form.password = ''
    passphrase.value = ''
    selectedFile.value = null
    if (!controller.signal.aborted && !props.disabled) emit('saved', value)
  } catch (cause) {
    if (!controller.signal.aborted && !props.disabled) error.value = message(cause)
  } finally { busy.value = false }
}
async function testConnection(): Promise<void> {
  if (!props.server || dirty.value || busy.value || testing.value || props.disabled || controller.signal.aborted) return
  const id = props.server.id
  const task = ++testTask
  testController = new AbortController()
  const signal = testController.signal
  const isCurrent = (): boolean => task === testTask && !signal.aborted && !controller.signal.aborted && !props.disabled && !dirty.value
  testing.value = true
  error.value = ''
  testNotice.value = ''
  probe.value = null
  try {
    await request(`/api/servers/${id}/test`, 'POST', {}, signal)
    if (isCurrent()) testNotice.value = 'SSH 连接测试成功'
  } catch (cause) {
    if (isCurrent()) {
      const found = confirmation(cause)
      if (found) probe.value = found
      else error.value = message(cause)
    }
  } finally { if (task === testTask) testing.value = false }
}
function hostConfirmed(): void {
  probe.value = null
  // 确认后仍测试已保存配置，不创建 Session 或自动保存草稿。
  void testConnection()
}
</script>
<template>
  <Modal :title="server ? '编辑服务器' : '添加服务器'" @close="emit('close')">
    <form @submit.prevent="save">
      <div class="dialog-body">
        <fieldset class="server-credentials" :disabled="busy || disabled">
          <div class="form-grid">
            <label>名称<input v-model="form.name" required maxlength="128" /></label>
            <label>分组<input v-model="form.group_name" maxlength="128" placeholder="未分组" /></label>
            <label>Host<input v-model="form.host" required maxlength="253" placeholder="IP 或域名" autocapitalize="none" /></label>
            <label>Port<input v-model.number="form.port" type="number" required min="1" max="65535" /></label>
            <label>Username<input v-model="form.username" required maxlength="128" autocomplete="off" /></label>
            <label>认证方式<select v-model="form.auth_type" @change="changeAuthentication">
              <option value="password">密码</option><option value="private_key">SSH 私钥</option>
            </select></label>
            <label v-if="form.auth_type === 'password'" class="full">Password
              <input v-model="form.password" type="password" :required="needsPassword" maxlength="4096" autocomplete="new-password" :placeholder="needsPassword ? 'SSH 密码' : '留空保留原密码'" />
            </label>
            <template v-else>
              <label class="full">私钥文件<input type="file" aria-label="私钥文件" @change="changeFile" /></label>
              <p v-if="selectedFile" class="full">已选择：{{ selectedFile.name }}（{{ selectedFile.size }} 字节）</p>
              <p v-else-if="server?.auth_type === 'private_key'" class="full">已保存私钥；不选择新文件时保留原凭据。</p>
              <label v-if="selectedFile" class="full">Passphrase（可选）
                <input v-model="passphrase" type="password" autocomplete="new-password" placeholder="无口令私钥请留空" />
              </label>
            </template>
            <label class="full">备注<textarea v-model="form.remark" maxlength="1024" /></label>
          </div>
        </fieldset>
        <p v-if="error" role="alert" class="error">{{ error }}</p>
        <p v-if="testNotice" role="status">{{ testNotice }}</p>
        <p v-if="!server || dirty">请先保存，退出后重新编辑以测试已保存配置。</p>
      </div>
      <div class="dialog-footer">
        <button type="button" :disabled="!server || dirty || busy || testing || disabled" @click="testConnection">{{ testing ? '测试中…' : '测试连接' }}</button>
        <button type="button" @click="emit('close')">取消</button>
        <button class="primary" :disabled="busy || testing || disabled">{{ busy ? '保存中…' : '保存服务器' }}</button>
      </div>
    </form>
  </Modal>
  <HostKeyDialog v-if="probe && server && !disabled" :server-id="server.id" :probe="probe" purpose="test" @close="probe = null" @confirmed="hostConfirmed" />
</template>
<style scoped>
.server-credentials { border: 0; margin: 0; padding: 0; min-width: 0; }
.full { overflow-wrap: anywhere; }
</style>
