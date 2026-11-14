<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'
import { request, message, type Probe } from '../../api/management'
import Modal from '../layout/Modal.vue'
const props = defineProps<{ serverId: string; probe: Probe }>()
const emit = defineEmits<{ close: []; confirmed: []; retry: [] }>()
const busy = ref(false)
const error = ref('')
const controller = new AbortController()
onBeforeUnmount(() => controller.abort())
async function confirm(): Promise<void> {
  if (busy.value) return
  if (Date.now() >= Date.parse(props.probe.expires_at)) {
    error.value = '探测凭证已过期，请重新测试'
    return
  }
  busy.value = true
  error.value = ''
  try {
    await request(
      `/api/servers/${props.serverId}/host-key/confirm`,
      'POST',
      { probe_id: props.probe.probe_id },
      controller.signal,
    )
    if (!controller.signal.aborted) emit('confirmed')
  } catch (cause) {
    if (controller.signal.aborted) return
    error.value = message(cause) + '；若无法确认结果，请重新测试连接。'
  } finally {
    busy.value = false
  }
}
</script>
<template>
  <Modal
    :title="
      probe.reason === 'key_changed'
        ? 'SSH 主机密钥已变化'
        : '首次信任 SSH 主机'
    "
    @close="emit('close')"
  >
    <div class="dialog-body">
      <p>
        {{
          probe.reason === 'key_changed'
            ? '当前公钥与已信任记录不同，请核实目标服务器的指纹后再继续。'
            : '请通过可信渠道核对目标服务器的指纹，再确认信任。'
        }}
      </p>
      <dl class="details">
        <dt>Host</dt>
        <dd>{{ probe.host }}</dd>
        <dt>Port</dt>
        <dd>{{ probe.port }}</dd>
        <dt>算法</dt>
        <dd>{{ probe.algorithm }}</dd>
        <dt>SHA256 指纹</dt>
        <dd class="mono">{{ probe.fingerprint }}</dd>
        <template v-if="probe.previous_fingerprint"
          ><dt>原指纹</dt>
          <dd class="mono">{{ probe.previous_fingerprint }}</dd></template
        >
        <dt>有效期至</dt>
        <dd>{{ new Date(probe.expires_at).toLocaleString() }}</dd>
      </dl>
      <p v-if="error" role="alert" class="error">{{ error }}</p>
    </div>
    <div class="dialog-footer">
      <button :disabled="busy" @click="emit('retry')">重新测试</button
      ><button @click="emit('close')">取消</button
      ><button class="primary" :disabled="busy" @click="confirm">
        {{ busy ? '确认中…' : '确认信任并测试' }}
      </button>
    </div>
  </Modal>
</template>
