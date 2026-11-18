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
import { SessionTakeover } from '../../terminal/takeover'
import { identity, logoutPending } from '../../stores/auth'
import { message, notifyAuthExpired } from '../../api/management'
import {
  closeSession,
  type SessionStatus,
  type SessionView,
} from '../../api/sessions'
import Modal from '../layout/Modal.vue'

type PaneStartMode = 'attach' | 'candidate'
type PaneLife =
  | 'mounting'
  | 'ready'
  | 'init_failed'
  | 'disposing'
  | 'dispose_failed'
  | 'closed'
interface PaneHandle {
  stop(): Promise<void>
  close(): Promise<void>
}
type DisposableLike = { dispose(): void }

const props = defineProps<{
  session: SessionView
  active: boolean
  startMode: PaneStartMode
}>()
const emit = defineEmits<{
  ready: [handle: PaneHandle]
  failed: [handle: PaneHandle]
  closed: []
  changed: []
}>()
const host = ref<HTMLElement>()
const notice = ref('')
const connectionNotice = ref('')
const lastConnectionDiagnostic = ref('')
const connected = ref(false)
const networkOnline = ref(navigator.onLine)
const awaitingNetworkConfirmation = ref(!navigator.onLine)
const busy = ref(false)
const remoteBusy = ref(false)
const closeRequested = ref(false)
const phase = ref('idle')
const retryLabel = ref('')
const recovery = shallowRef<SessionRecovery | null>(null)
const takeover = shallowRef<SessionTakeover | null>(null)
const revision = ref(0)
const closing = ref(false)
const ended = ref(false)
const life = ref<PaneLife>('mounting')
let client: TerminalClient | null = null
let terminal: Terminal | null = null
let fit: FitAddon | null = null
let resize: ResizeObserver | null = null
let theme: MutationObserver | null = null
let closeTask: Promise<void> | null = null
// 终端工厂回滚自身资源失败时记录的错误；非 null 表示可能有 xterm 残留，close() 必须失败。
let terminalCleanupError: unknown = null
let localCols = props.session.current_cols
let localRows = props.session.current_rows

const live = (): boolean =>
  life.value === 'ready' && !logoutPending.value && identity.value !== null
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
const takeoverView = computed(() => {
  revision.value
  const t = takeover.value
  return {
    phase: t?.phase ?? 'idle',
    candidate: t?.candidate ?? null,
    error: t?.error ?? '',
    message: t?.message ?? '',
    canPrepare: t?.canPrepare ?? false,
    canConfirm: t?.canConfirm ?? false,
  }
})
const takeoverLabel = computed(() =>
  ['preparing', 'connecting', 'retry_wait', 'stopping'].includes(phase.value)
    ? '停止恢复并准备接管'
    : '准备接管',
)
const statusLabels: Record<SessionStatus, string> = {
  CONNECTING: '正在连接',
  CONNECTED: '已连接',
  DISCONNECTED: '已断开',
  CLOSING: '正在关闭',
}
function statusLabel(status: SessionStatus): string {
  return statusLabels[status]
}
// 网络事件仅影响展示，不终止 socket、不启动或重置恢复任务。
function networkOffline(): void {
  networkOnline.value = false
  awaitingNetworkConfirmation.value = true
}
function networkRestored(): void {
  if (!networkOnline.value) awaitingNetworkConfirmation.value = true
  networkOnline.value = true
}
function confirmNetworkConnection(): void {
  if (live() && networkOnline.value && connected.value)
    awaitingNetworkConfirmation.value = false
}
const connectionLabel = computed(() => {
  if (life.value === 'disposing') return '正在关闭标签'
  if (life.value === 'dispose_failed') return '终端清理失败'
  if (life.value === 'init_failed') return '终端初始化失败'
  if (ended.value) return '会话已结束'
  if (takeoverView.value.phase === 'stopping') return '正在停止原恢复任务'
  if (takeoverView.value.phase === 'loading') return '正在查询 Session 状态'
  if (takeoverView.value.phase === 'confirming') return '等待确认接管'
  if (phase.value === 'stopping') return '正在停止'
  if (['idle', 'stopped', 'disposed'].includes(phase.value)) return '未连接'
  if (!networkOnline.value) return '网络已断开，等待网络恢复'
  if (retryLabel.value) return retryLabel.value
  if (awaitingNetworkConfirmation.value)
    return '网络已恢复，等待连接确认'
  if (phase.value === 'replaying') return '正在回放'
  if (connected.value) return '已连接'
  if (phase.value === 'preparing') return '正在申请连接凭证'
  return '正在连接'
})
function stoppedState(state: RecoveryState): void {
  if (life.value !== 'ready' || state.taskId !== client?.taskId) return
  phase.value = state.phase
  retryLabel.value =
    state.phase === 'retry_wait'
      ? `等待自动恢复 ${state.attempt}/${state.max}`
      : ''
  if (live()) {
    connectionNotice.value = state.message
    if (state.phase === 'retry_wait')
      lastConnectionDiagnostic.value = state.message
  }
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
  takeover.value?.invalidate()
  return recovery.value?.stop() ?? client?.stop() ?? Promise.resolve()
}
function close(): Promise<void> {
  if (closeTask) return closeTask
  life.value = 'disposing' // live() 立即为假，所有回调、按钮、准入同步失效
  closing.value = false
  takeover.value?.invalidate()
  recovery.value?.invalidate()
  window.removeEventListener('offline', networkOffline)
  window.removeEventListener('online', networkRestored)
  resize?.disconnect()
  theme?.disconnect()
  // 初始化失败现场已启动 client.dispose() 时，这里拿到的是同一个缓存 Promise。
  const cleanup = (client?.dispose() ?? Promise.resolve()).then(() => {
    // 工厂回滚失败意味着可能有 xterm 残留，不能视为清理成功。
    if (terminalCleanupError !== null) throw terminalCleanupError
  })
  closeTask = cleanup.then(
    () => {
      life.value = 'closed'
      emit('closed')
    },
    (error: unknown) => {
      life.value = 'dispose_failed'
      throw error
    },
  )
  return closeTask
}
const handle: PaneHandle = { stop, close }
defineExpose({ stop, close })
function showCloseFailure(error: unknown): void {
  notice.value = `终端清理失败，请刷新页面后再打开该 Session：${message(error)}`
}
// 初始化失败现场的清理失败只展示，不改变 life；用户关闭时 close() 会得到同一失败。
function showCleanupFailure(error: unknown): void {
  notice.value = `${notice.value}；终端资源清理失败，请刷新页面：${message(error)}`
}
function closeLocal(): void {
  void close().catch(showCloseFailure)
}
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
    life.value !== 'ready' ||
    !props.active ||
    !terminal ||
    !fit ||
    !host.value?.clientWidth
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
function rollbackTerminal(
  term: Terminal,
  subscriptions: DisposableLike[],
): void {
  const steps = [
    ...subscriptions.reverse().map((item) => () => item.dispose()),
    () => term.dispose(),
  ]
  for (const step of steps) {
    try {
      step()
    } catch (cleanupError) {
      terminalCleanupError ??= cleanupError
      console.error('终端创建失败后的回滚失败', props.session.id, cleanupError)
    }
  }
}
function createTerminalPort() {
  const container = host.value
  if (!container) throw new Error('终端容器不可用') // 前置检查，尚未创建资源
  const term = new Terminal({
    cols: localCols,
    rows: localRows,
    convertEol: false,
    cursorBlink: true,
    fontSize: 14,
    fontFamily: 'Consolas, "Liberation Mono", monospace',
    theme: themeOptions(),
  })
  const subscriptions: DisposableLike[] = []
  let addon: FitAddon
  try {
    addon = new FitAddon()
    term.loadAddon(addon)
    term.open(container)
    subscriptions.push(
      term.onData((value) => {
        if (client?.canInput)
          void client.send(new TextEncoder().encode(value))
      }),
    )
    subscriptions.push(
      term.onBinary((value) => {
        if (client?.canInput)
          void client.send(
            Uint8Array.from(value, (char) => char.charCodeAt(0)),
          )
      }),
    )
    subscriptions.push(
      term.onResize((value) => {
        if (terminal !== term) return
        localCols = value.cols
        localRows = value.rows
        client?.resize(value.cols, value.rows)
      }),
    )
  } catch (error) {
    rollbackTerminal(term, subscriptions)
    throw error // 重新抛出原错误，Tracker 和 Client 构造随之失败
  }
  // 提交：以下步骤不会同步抛错。
  terminal = term
  fit = addon
  void layout()
  return {
    write: (bytes: Uint8Array, callback: () => void) =>
      term.write(bytes, callback),
    dispose: () => {
      for (const item of subscriptions) item.dispose()
      term.dispose()
      if (terminal === term) {
        terminal = null
        fit = null
      }
    },
  }
}
async function connect(): Promise<void> {
  if (busy.value || ended.value || !live()) return
  busy.value = true
  try {
    const result = await client?.connect()
    if (!live()) return
    if (result?.connected) await layout(true)
  } catch (error) {
    if (live()) notice.value = message(error)
  } finally {
    busy.value = false
  }
}
async function recover(): Promise<void> {
  if (!live()) return
  const result = await recovery.value?.recover()
  if (result?.connected && live()) await layout(true)
}
async function inspect(): Promise<void> {
  if (!live() || takeoverView.value.phase !== 'idle') return
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
    if (life.value !== 'ready') return
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
    emit('changed')
  }
}
function rollbackInitialization(): void {
  window.removeEventListener('offline', networkOffline) // 对未注册的监听是空操作
  window.removeEventListener('online', networkRestored)
  resize?.disconnect()
  theme?.disconnect()
  client?.dispose().catch(showCleanupFailure) // 不是空 catch：展示清理失败原因
}
function failInitialization(error: unknown): void {
  life.value = 'init_failed'
  notice.value = error instanceof Error ? error.message : '终端初始化失败'
  emit('failed', handle)
}
onMounted(() => {
  if (life.value !== 'mounting') return
  // 第一段：只做可能失败的校验和纯计算，不创建任何资源，不发请求。
  let settings: ReturnType<typeof clientSettings>
  try {
    if (logoutPending.value || !identity.value || !host.value)
      throw new Error('登录状态或终端容器不可用')
    settings = clientSettings(identity.value.client_settings)
  } catch (error) {
    failInitialization(error)
    return
  }
  // 第二段：按顺序创建；每个资源创建后立即赋给 Pane 字段，任一步失败由失败现场回滚。
  try {
    // 构造链最后一步才调用工厂创建 xterm：构造抛错时只可能遗留工厂内部资源。
    client = new TerminalClient(
      props.session.id,
      props.session.binding_generation,
      settings,
      createTerminalPort,
      (value) => {
        if (live()) notice.value = value
      },
      (value) => {
        connected.value = value
        if (value && live()) {
          confirmNetworkConnection()
          client?.resize(localCols, localRows)
          void layout()
        }
      },
      stoppedState,
      () => live() && !closeRequested.value,
      confirmNetworkConnection,
    )
    // 以下两个对象没有长期资源。
    recovery.value = new SessionRecovery(
      props.session.id,
      client,
      () =>
        live() &&
        !remoteBusy.value &&
        (takeover.value?.phase ?? 'idle') === 'idle',
      () => {
        revision.value++
      },
    )
    takeover.value = new SessionTakeover({
      id: props.session.id,
      client,
      invalidateRecovery: () => recovery.value?.invalidate(),
      allowed: () => live() && !remoteBusy.value && !closeRequested.value,
      canBegin: () =>
        !connected.value &&
        !['online', 'replaying'].includes(phase.value) &&
        !closing.value,
      isEnded: () => ended.value,
      markEnded: () => {
        ended.value = true
        if (recovery.value) recovery.value.ended = true
      },
      changed: () => {
        revision.value++
      },
      finished: (result) => {
        if (result.connected && live()) void layout(true)
        emit('changed')
      },
    })
    resize = new ResizeObserver(fitTerminal)
    resize.observe(host.value)
    theme = new MutationObserver(() => {
      if (terminal) terminal.options.theme = themeOptions()
    })
    theme.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme'],
    })
    networkOnline.value = navigator.onLine
    window.addEventListener('offline', networkOffline)
    window.addEventListener('online', networkRestored)
  } catch (error) {
    rollbackInitialization()
    failInitialization(error)
    return
  }
  life.value = 'ready'
  emit('ready', handle)
  if (!live()) return
  if (props.startMode === 'attach') void connect()
  else void takeover.value.prepare() // 只做 stop 屏障加 GET 详情，不连接
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
  void close().catch(showCloseFailure)
})
</script>
<template>
  <section class="terminal-pane" :aria-label="session.name + ' 终端'">
    <div class="terminal-actions">
      <span class="mono"
        >{{ session.username }}@{{ session.host }}:{{ session.port }}</span
      >
      <span class="connection-state">{{ connectionLabel }}</span>
      <button v-if="takeoverView.canPrepare" @click="takeover?.prepare()">
        {{ takeoverLabel }}
      </button>
      <button
        v-if="['stopping', 'loading'].includes(takeoverView.phase)"
        @click="takeover?.cancel()"
      >
        取消准备
      </button>
      <button
        :disabled="
          life !== 'ready' ||
          !detail.canRecover ||
          closeRequested ||
          ended ||
          logoutPending ||
          takeoverView.phase !== 'idle'
        "
        @click="recover"
      >
        手动恢复
      </button>
      <button
        :disabled="
          life !== 'ready' ||
          logoutPending ||
          detail.acting ||
          takeoverView.phase !== 'idle' ||
          !['idle', 'stopped'].includes(phase)
        "
        @click="inspect"
      >
        刷新 Session 状态
      </button>
      <button
        v-if="detail.loading && life === 'ready'"
        @click="recovery?.invalidate()"
      >
        取消查询
      </button>
      <button
        v-if="
          life === 'ready' &&
          !['idle', 'stopped', 'disposed'].includes(phase)
        "
        @click="stop"
      >
        停止恢复
      </button>
      <button
        :disabled="life === 'disposing' || life === 'dispose_failed'"
        @click="closeLocal"
      >
        关闭标签
      </button>
      <button
        class="danger"
        :disabled="
          life !== 'ready' || ended || closeRequested || logoutPending
        "
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
    <p v-if="takeoverView.error" class="notice" role="alert">
      {{ takeoverView.error }}
    </p>
    <p v-if="takeoverView.message" class="notice" role="status">
      {{ takeoverView.message }}
    </p>
    <div ref="host" class="terminal-host" />
    <p v-if="connectionNotice" class="notice" role="status">
      {{ connectionNotice }}
    </p>
    <p
      v-if="connected && lastConnectionDiagnostic"
      class="notice connection-diagnostic"
      role="status"
    >
      <span>最近一次连接异常（历史记录）：{{ lastConnectionDiagnostic }}</span>
      <button
        type="button"
        class="dismiss-diagnostic"
        aria-label="关闭历史连接提示"
        title="关闭历史连接提示"
        @click="lastConnectionDiagnostic = ''"
      >
        ×
      </button>
    </p>
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
    <Modal
      v-if="
        active &&
        takeoverView.phase === 'confirming' &&
        takeoverView.candidate
      "
      title="接管 Session？"
      @close="takeover?.cancel()"
    >
      <div class="dialog-body">
        <p>
          如果其他浏览器或设备正在连接该 Session，它将被断开并停止自动恢复；本标签将成为唯一连接端。
        </p>
        <p>{{ takeoverView.candidate.name }}</p>
        <p class="mono">
          {{ takeoverView.candidate.username }}@{{
            takeoverView.candidate.host
          }}:{{ takeoverView.candidate.port }}
        </p>
        <p>
          状态：{{ statusLabel(takeoverView.candidate.status) }} · 代次
          {{ takeoverView.candidate.binding_generation }}
          <span v-if="takeoverView.candidate.remaining_seconds !== null">
            · 离线保留 {{ takeoverView.candidate.remaining_seconds }}
            秒（查询时）</span
          >
        </p>
      </div>
      <div class="dialog-footer">
        <button @click="takeover?.cancel()">取消</button
        ><button
          class="primary"
          :disabled="!takeoverView.canConfirm"
          @click="takeover?.confirm()"
        >
          确认接管
        </button>
      </div>
    </Modal>
  </section>
</template>

<style scoped>
.connection-diagnostic {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.connection-diagnostic > span {
  min-width: 0;
}
.dismiss-diagnostic {
  flex-shrink: 0;
  padding: 0 8px;
}
</style>
