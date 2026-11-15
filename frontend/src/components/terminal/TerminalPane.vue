<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { TerminalClient, clientSettings } from '../../terminal/client'
import { identity } from '../../stores/auth'
import { request, message } from '../../api/management'
import { closeSession, type SessionView } from '../../api/sessions'
import Modal from '../layout/Modal.vue'
const props = defineProps<{ session: SessionView; active: boolean }>()
const emit = defineEmits<{ closed: [] }>()
const host = ref<HTMLElement>()
const notice = ref('')
const connected = ref(false)
const busy = ref(false)
const closing = ref(false)
const ended = ref(false)
let client: TerminalClient | null = null
let terminal: Terminal | null = null
let fit: FitAddon | null = null
let resize: ResizeObserver | null = null
let theme: MutationObserver | null = null
let disposed = false
function themeOptions() {
  const style = getComputedStyle(document.documentElement)
  return {
    background: style.getPropertyValue('--panel').trim(),
    foreground: style.getPropertyValue('--text').trim(),
    cursor: style.getPropertyValue('--text').trim(),
  }
}
function fitTerminal(): void {
  if (
    !props.active ||
    !terminal ||
    !fit ||
    !host.value?.clientWidth ||
    disposed
  )
    return
  const dimensions = fit.proposeDimensions()
  const values = identity.value?.client_settings
  if (dimensions && values)
    terminal.resize(
      Math.max(1, Math.min(dimensions.cols, values.max_cols)),
      Math.max(1, Math.min(dimensions.rows, values.max_rows)),
    )
  client?.resize(terminal.cols, terminal.rows)
}
async function connect(): Promise<void> {
  if (busy.value || ended.value || disposed) return
  busy.value = true
  notice.value = ''
  try {
    await client?.connect()
    await nextTick()
    fitTerminal()
    terminal?.focus()
  } catch (error) {
    notice.value = error instanceof Error ? error.message : '连接失败'
  } finally {
    busy.value = false
  }
}
async function inspect(): Promise<void> {
  try {
    const view = await request<SessionView>(`/api/sessions/${props.session.id}`)
    notice.value = `会话状态：${view.status}`
  } catch (error) {
    notice.value = message(error)
  }
}
async function closeRemote(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await closeSession(props.session.id)
    ended.value = true
    closing.value = false
    notice.value = '已请求关闭 SSH 会话'
    await client?.stop()
  } catch (error) {
    notice.value = message(error)
  } finally {
    busy.value = false
  }
}
async function dispose(): Promise<void> {
  disposed = true
  resize?.disconnect()
  theme?.disconnect()
  try {
    await client?.dispose()
  } catch (error) {
    notice.value = error instanceof Error ? error.message : '终端清理失败'
  }
}
async function closeLocal(): Promise<void> {
  await dispose()
  emit('closed')
}
onMounted(() => {
  try {
    if (!identity.value || !host.value)
      throw new Error('登录状态或终端配置不可用')
    const settings = clientSettings(identity.value.client_settings)
    client = new TerminalClient(
      props.session.id,
      props.session.binding_generation,
      settings,
      () => {
        const term = new Terminal({
          cols: props.session.current_cols,
          rows: props.session.current_rows,
          convertEol: false,
          cursorBlink: true,
          fontSize: 14,
          fontFamily: 'Consolas, "Liberation Mono", monospace',
          theme: themeOptions(),
        })
        const addon = new FitAddon()
        term.loadAddon(addon)
        term.open(host.value!)
        terminal = term
        fit = addon
        const data = term.onData((value) => {
          if (client?.canInput)
            void client.send(new TextEncoder().encode(value))
        })
        const binary = term.onBinary((value) => {
          if (client?.canInput)
            void client.send(
              Uint8Array.from(value, (char) => char.charCodeAt(0)),
            )
        })
        const size = term.onResize((value) =>
          client?.resize(value.cols, value.rows),
        )
        return {
          write: (bytes, callback) => term.write(bytes, callback),
          dispose: () => {
            data.dispose()
            binary.dispose()
            size.dispose()
            term.dispose()
            if (terminal === term) {
              terminal = null
              fit = null
            }
          },
        }
      },
      (value) => {
        notice.value = value
      },
      (value) => {
        connected.value = value
      },
    )
    resize = new ResizeObserver(fitTerminal)
    resize.observe(host.value)
    theme = new MutationObserver(() => {
      if (terminal) terminal.options.theme = themeOptions()
    })
    theme.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme'],
    })
    void connect()
  } catch (error) {
    notice.value = error instanceof Error ? error.message : '终端初始化失败'
  }
})
watch(
  () => props.active,
  async (value) => {
    if (value) {
      await nextTick()
      fitTerminal()
      terminal?.focus()
    }
  },
)
onBeforeUnmount(() => {
  void dispose()
})
</script>
<template>
  <section class="terminal-pane" :aria-label="session.name + ' 终端'">
    <div class="terminal-actions">
      <span class="mono"
        >{{ session.username }}@{{ session.host }}:{{ session.port }}</span
      >
      <span class="connection-state">{{
        connected ? '已连接' : '未连接'
      }}</span>
      <button :disabled="busy || connected || ended" @click="connect">
        手动恢复
      </button>
      <button @click="inspect">查询状态</button>
      <button @click="closeLocal">关闭标签</button>
      <button class="danger" :disabled="ended" @click="closing = true">
        关闭 SSH 会话
      </button>
    </div>
    <div ref="host" class="terminal-host" />
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <Modal v-if="closing" title="关闭 SSH 会话？" @close="closing = false">
      <div class="dialog-body">
        <p>正在运行的前台命令可能中断。关闭后无法恢复此会话。</p>
        <p>{{ session.name }}</p>
      </div>
      <div class="dialog-footer">
        <button @click="closing = false">取消</button
        ><button class="danger" :disabled="busy" @click="closeRemote">
          确认关闭
        </button>
      </div>
    </Modal>
  </section>
</template>
