<script setup lang="ts">
import {
  computed,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from 'vue'
import type { ServerView } from '../../api/management.ts'
import {
  listSessions,
  type SessionStatus,
  type SessionView,
} from '../../api/sessions.ts'
import { SessionListController } from '../../session/list.ts'

const props = defineProps<{
  servers: ServerView[]
  canRefresh: boolean
  authToken: unknown
}>()

const serverFilter = ref('')
const statusFilter = ref<SessionStatus | 'ALL'>('ALL')
const statusOptions: Array<{ value: SessionStatus | 'ALL'; label: string }> = [
  { value: 'ALL', label: '全部状态' },
  { value: 'CONNECTING', label: '正在连接' },
  { value: 'CONNECTED', label: '已连接' },
  { value: 'DISCONNECTED', label: '已断开' },
  { value: 'CLOSING', label: '正在关闭' },
]
const statusLabels: Record<SessionStatus, string> = {
  CONNECTING: '正在连接',
  CONNECTED: '已连接',
  DISCONNECTED: '已断开',
  CLOSING: '正在关闭',
}

const controller = new SessionListController({
  load: listSessions,
  isAllowed: () => props.canRefresh,
  currentIdentity: () => props.authToken,
})
const filteredItems = computed(() => {
  if (statusFilter.value === 'ALL') return controller.items.value
  return controller.items.value.filter(
    (item) => item.status === statusFilter.value,
  )
})
const serverNames = computed(
  () => new Map(props.servers.map((server) => [server.id, server.name])),
)
const firstLoading = computed(
  () => controller.loading.value && !controller.hasSnapshot,
)
const firstError = computed(
  () =>
    controller.error.value !== null &&
    !controller.hasSnapshot &&
    !controller.loading.value,
)
const emptyResult = computed(
  () =>
    controller.hasSnapshot &&
    !controller.loading.value &&
    controller.items.value.length === 0,
)
const emptyFilter = computed(
  () =>
    controller.hasSnapshot &&
    !controller.loading.value &&
    controller.items.value.length > 0 &&
    filteredItems.value.length === 0,
)

function updateServerFilter(event: Event): void {
  serverFilter.value = (event.target as HTMLSelectElement).value
}

function updateStatusFilter(event: Event): void {
  statusFilter.value = (event.target as HTMLSelectElement)
    .value as SessionStatus | 'ALL'
}

function refresh(): void {
  void controller.refresh(serverFilter.value)
}

function serverLabel(item: SessionView): string {
  const name = serverNames.value.get(item.server_id)
  const target = `${item.username}@${item.host}:${item.port}`
  return name ? `${name} · ${target}` : target
}

function formatTime(value: string | null): string {
  return value ? new Date(value).toLocaleString() : '—'
}

function formatRemaining(item: SessionView): string {
  return item.remaining_seconds === null
    ? '—'
    : `${item.remaining_seconds} 秒`
}

watch(serverFilter, refresh)
watch(
  () => props.canRefresh,
  (allowed) => {
    if (!allowed) controller.invalidate()
  },
)
watch(
  () => props.authToken,
  (token, previous) => {
    if (!props.canRefresh || Object.is(token, previous)) return
    controller.invalidate()
    refresh()
  },
)

let wasHidden = false
function onVisibilityChange(): void {
  const hidden = document.visibilityState === 'hidden'
  if (wasHidden && !hidden && props.canRefresh) refresh()
  wasHidden = hidden
}

onMounted(() => {
  wasHidden = document.visibilityState === 'hidden'
  document.addEventListener('visibilitychange', onVisibilityChange)
  refresh()
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', onVisibilityChange)
  controller.dispose()
})

defineExpose({
  refresh,
  invalidate: () => controller.invalidate(),
})
</script>

<template>
  <section class="session-manager" aria-labelledby="session-manager-title">
    <header class="session-manager-heading">
      <div>
        <p class="section-kicker">运行时会话</p>
        <h1 id="session-manager-title">活动 Session</h1>
        <p class="muted">
          列表是查询快照，不表示当前设备持有该 Session 的 Binding。
        </p>
      </div>
      <button :disabled="!canRefresh" @click="refresh">刷新</button>
    </header>

    <div class="session-toolbar">
      <label>
        <span>服务器</span>
        <select :value="serverFilter" @change="updateServerFilter">
          <option value="">全部服务器</option>
          <option
            v-for="server in servers"
            :key="server.id"
            :value="server.id"
          >
            {{ server.name }}
          </option>
        </select>
      </label>
      <label>
        <span>连接状态</span>
        <select :value="statusFilter" @change="updateStatusFilter">
          <option
            v-for="option in statusOptions"
            :key="option.value"
            :value="option.value"
          >
            {{ option.label }}
          </option>
        </select>
      </label>
      <div class="session-snapshot-state">
        <span
          v-if="controller.stale.value && controller.hasSnapshot"
          class="snapshot-badge stale"
        >
          旧快照
        </span>
        <span v-else-if="controller.loading.value" class="snapshot-badge">
          查询中
        </span>
        <span v-else-if="controller.hasSnapshot" class="snapshot-badge">
          最新查询
        </span>
        <span v-if="controller.lastLoadedAt.value" class="muted">
          最近查询：{{ controller.lastLoadedAt.value.toLocaleString() }}
        </span>
      </div>
    </div>

    <p v-if="firstLoading" class="session-query-state" role="status">
      正在查询活动 Session…
    </p>
    <div v-else-if="firstError" class="session-first-error" role="alert">
      <p>活动 Session 查询失败：{{ controller.error.value }}</p>
      <button :disabled="!canRefresh" @click="refresh">重试</button>
    </div>
    <div
      v-if="
        controller.hasSnapshot &&
        controller.stale.value &&
        controller.error.value
      "
      class="session-refresh-error"
      role="alert"
    >
      <strong>当前显示的是旧快照，数据可能不是最新的。</strong>
      <span>刷新失败：{{ controller.error.value }}</span>
    </div>
    <p
      v-else-if="controller.hasSnapshot && controller.loading.value"
      class="session-query-state"
      role="status"
    >
      正在刷新，当前仍显示上次成功查询的快照。
    </p>

    <p v-if="emptyResult" class="session-empty">
      当前没有活动 Session。
    </p>
    <p v-else-if="emptyFilter" class="session-empty">
      没有符合当前状态筛选的 Session。
    </p>
    <div
      v-else-if="controller.hasSnapshot"
      class="session-table-wrap"
      :aria-busy="controller.loading.value"
    >
      <table class="session-table">
        <thead>
          <tr>
            <th>Session</th>
            <th>服务器</th>
            <th>状态</th>
            <th>代次</th>
            <th>尺寸</th>
            <th>创建时间</th>
            <th>最近绑定</th>
            <th>断开时间</th>
            <th>离线期限</th>
            <th>剩余</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in filteredItems" :key="item.id">
            <td>
              <strong>{{ item.name }}</strong>
              <span class="mono session-id">{{ item.id }}</span>
            </td>
            <td>{{ serverLabel(item) }}</td>
            <td>
              <span
                class="session-status"
                :class="`status-${item.status.toLowerCase()}`"
              >
                {{ statusLabels[item.status] }}
              </span>
            </td>
            <td>{{ item.binding_generation }}</td>
            <td>{{ item.current_cols }} × {{ item.current_rows }}</td>
            <td>{{ formatTime(item.created_at) }}</td>
            <td>{{ formatTime(item.last_attached_at) }}</td>
            <td>{{ formatTime(item.disconnected_at) }}</td>
            <td>{{ formatTime(item.offline_expires_at) }}</td>
            <td>{{ formatRemaining(item) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
