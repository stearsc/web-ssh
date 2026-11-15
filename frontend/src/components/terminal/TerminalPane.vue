<script setup lang="ts">
import {
  ref,
  shallowRef,
  computed,
  onMounted,
  onBeforeUnmount,
  watch,
  nextTick,
} from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { SessionRecovery } from '../../terminal/recovery'
import {
  TerminalClient,
  clientSettings,
  type RecoveryState,
} from '../../terminal/client'
import { identity, logoutPending } from '../../stores/auth'
import { message, notifyAuthExpired } from '../../api/management'
import { closeSession, type SessionView } from '../../api/sessions'
import Modal from '../layout/Modal.vue'
const props = defineProps<{ session: SessionView; active: boolean }>()
const emit = defineEmits<{ closed: [] }>()
const host = ref<HTMLElement>()
const notice = ref('')
const connected = ref(false)
const busy = ref(false)
const remoteBusy = ref(false)
const closeRequested = ref(false)
const phase = ref('idle')
const retryLabel = ref('')
const recovery = shallowRef<SessionRecovery | null>(null)
const revision = ref(0)
const detail = computed(() => {
  revision.value
  const r = recovery.value
  return {
    loading: r?.loading ?? false,
    acting: r?.acting ?? false,
    candidate: r?.candidate,
    snapshot: r?.snapshot,
    error: r?.error,
    canRecover: r?.canRecover ?? false,
  }
})
const closing = ref(false)
const ended = ref(false)
let client: TerminalClient | null = null
let terminal: Terminal | null = null
let fit: FitAddon | null = null
let resize: ResizeObserver | null = null
let theme: MutationObserver | null = null
let disposed = false
let disposeTask: Promise<void> | null = null
let localCols = props.session.current_cols
let localRows = props.session.current_rows
const live = (): boolean =>
  !disposed && !logoutPending.value && identity.value !== null
function stoppedState(state: RecoveryState): void {
  if (disposed || state.taskId !== client?.taskId) return
  phase.value = state.phase
  retryLabel.value =
    state.phase === 'retry_wait'
      ? `等待自动恢复 ${state.attempt}/${state.max}`
      : ''
  if (state.message && live()) notice.value = state.message
  revision.value++
  if (state.phase === 'stopped') {
    if (state.code === 'closed') {
      ended.value = true
      if (recovery.value) recovery.value.ended = true
    }
    if (state.code === 'AUTH_REQUIRED' && !logoutPending.value)
      notifyAuthExpired()
    if (state.diagnostic && live())
      void recovery.value?.refresh(false, state.taskId)
  }
}
async function layout(focus = false): Promise<void> {
  const instance = terminal
  const taskId = client?.taskId
  await nextTick()
  if (!live() || instance !== terminal || taskId !== client?.taskId) return
  fitTerminal()
  // 后台恢复仅同步尺寸；只有显式连接/切回当前标签可以请求焦点。
  if (focus && props.active && document.visibilityState === 'visible')
    terminal?.focus()
}
function stop(): Promise<void> {
  busy.value = false
  return recovery.value?.stop() ?? client?.stop() ?? Promise.resolve()
}
defineExpose({ stop, dispose })
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
  if (busy.value || ended.value || !live()) return
  busy.value = true
  try {
    const result = await client?.connect()
    if (!live()) return
    if (result && !result.connected) notice.value = result.message
    if (result?.connected) await layout(true)
  } catch (error) {
    if (live()) notice.value = message(error)
  } finally {
    busy.value = false
  }
}
async function recover(): Promise<void> {
  const result = await recovery.value?.recover()
  if (result?.connected && live()) await layout(true)
}
async function inspect(): Promise<void> {
  await recovery.value?.refresh()
}
async function closeRemote(): Promise<void> {
  if (remoteBusy.value || !live()) return
  remoteBusy.value = true
  closeRequested.value = true
  const stopping = stop()
  try {
    // DELETE 与本地排空并行，不能等待终端回调才向服务端提交关闭。
    await closeSession(props.session.id)
    if (disposed) return
    if (recovery.value) recovery.value.ended = true
    closing.value = false
    notice.value = '已请求关闭 SSH 会话，正在回收；可刷新状态确认'
  } catch (error) {
    closeRequested.value = false
    if (live()) notice.value = message(error)
  } finally {
    try {
      await stopping
    } catch (error) {
      if (live()) notice.value = message(error)
    }
    remoteBusy.value = false
    revision.value++
  }
}
function dispose(): Promise<void> {
  if (disposeTask) return disposeTask
  disposed = true
  recovery.value?.invalidate()
  resize?.disconnect()
  theme?.disconnect()
  disposeTask = (client?.dispose() ?? Promise.resolve()).catch((error) => {
    notice.value = error instanceof Error ? error.message : '终端清理失败'
  })
  return disposeTask
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
          cols: localCols,
          rows: localRows,
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
        const size = term.onResize((value) => {
          if (terminal !== term) return
          localCols = value.cols
          localRows = value.rows
          client?.resize(value.cols, value.rows)
        })
        void layout()
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
        if (live()) notice.value = value
      },
      (value) => {
        connected.value = value
        if (value && live()) {
          client?.resize(localCols, localRows)
          void layout()
        }
      },
      stoppedState,
      () => live() && !closeRequested.value,
    )
    recovery.value = new SessionRecovery(
      props.session.id,
      client,
      () => live() && !remoteBusy.value,
      () => {
        revision.value++
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
      await layout(true)
    }
  },
)
watch(
  [logoutPending, identity],
  () => {
    if (!live())
      void stop().catch((error) => {
        notice.value = message(error)
      })
  },
  { flush: 'sync' },
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
        retryLabel ||
        (phase === 'replaying'
          ? '正在回放'
          : connected
            ? '已连接'
            : phase === 'stopping'
              ? '正在停止'
              : '未连接')
      }}</span>
      <button
        :disabled="
          !detail.canRecover || closeRequested || ended || logoutPending
        "
        @click="recover"
      >
        手动恢复
      </button>
      <button
        :disabled="
          logoutPending ||
          detail.acting ||
          !['idle', 'stopped'].includes(phase)
        "
        @click="inspect"
      >
        刷新 Session 状态
      </button>
      <button v-if="detail.loading" @click="recovery?.invalidate()">
        取消查询
      </button>
      <button
        v-if="!['idle', 'stopped', 'disposed'].includes(phase)"
        @click="stop"
      >
        停止恢复
      </button>
      <button @click="closeLocal">关闭标签</button>
      <button
        class="danger"
        :disabled="ended || closeRequested || logoutPending"
        @click="closing = true"
      >
        关闭 SSH 会话
      </button>
    </div>
    <p v-if="detail.snapshot" class="notice" role="status">
      会话状态：{{ detail.snapshot.status }} · 代次
      {{ detail.snapshot.binding_generation }}
      <span v-if="detail.snapshot.remaining_seconds !== null">
        · 离线保留 {{ detail.snapshot.remaining_seconds }} 秒（查询时）</span
      >
      <span v-if="detail.snapshot.status === 'CONNECTED'">
        · 当前存在绑定，普通恢复不会接管</span
      >
    </p>
    <p v-if="detail.error" class="notice" role="alert">{{ detail.error }}</p>
    <div ref="host" class="terminal-host" />
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <Modal v-if="closing" title="关闭 SSH 会话？" @close="closing = false">
      <div class="dialog-body">
        <p>正在运行的前台命令可能中断。关闭后无法恢复此会话。</p>
        <p>{{ session.name }}</p>
      </div>
      <div class="dialog-footer">
        <button @click="closing = false">取消</button
        ><button
          class="danger"
          :disabled="remoteBusy || logoutPending"
          @click="closeRemote"
        >
          确认关闭
        </button>
      </div>
    </Modal>
  </section>
</template>
