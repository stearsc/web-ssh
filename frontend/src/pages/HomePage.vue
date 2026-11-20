<script setup lang="ts">
import {
  computed,
  markRaw,
  ref,
  onMounted,
  onBeforeUnmount,
  watch,
} from 'vue'
import { useRouter } from 'vue-router'
import {
  request,
  message,
  confirmation,
  type ServerView,
  type Probe,
} from '../api/management'
import {
  logout,
  identity,
  logoutPending,
  authNotice,
} from '../stores/auth'
import { createSession, type SessionView } from '../api/sessions'
import {
  CloseCoordinator,
  type CloseSnapshot,
  type CloseState,
} from '../session/close'
import TerminalPane from '../components/terminal/TerminalPane.vue'
import ThemeButton from '../components/layout/ThemeButton.vue'
import Modal from '../components/layout/Modal.vue'
import ServerForm from '../components/server/ServerForm.vue'
import HostKeyDialog from '../components/server/HostKeyDialog.vue'
import SessionManager from '../components/session/SessionManager.vue'

type PaneStartMode = 'attach' | 'candidate'
interface PaneHandle {
  stop(): Promise<void>
  close(): Promise<void>
}
interface PaneRecord {
  key: number
  session: SessionView
  startMode: PaneStartMode
  handle: PaneHandle | null
}
interface PaneEntry {
  sessionId: string
  handle: PaneHandle
}

const records = ref<PaneRecord[]>([])
let nextPaneKey = 0
let pageDisposed = false
let closeCoordinatorDisposed = false
// 页面级唯一关闭所有者：Pane 与后续无 Pane 入口共用同一状态机。
const closeCoordinator = new CloseCoordinator()
// 协调器不是 Vue 响应式对象；这里只桥接 ended tombstone 供列表禁用入口。
const endedSessionIds = ref<string[]>([])
// 订阅属于页面而非 Pane：Pane 销毁后，迟到的详情 GET 仍要能更新 ended。
const closeUnsubscribes = new Map<string, () => void>()
const manager = ref<{
  refresh(): void
  invalidate(): void
} | null>(null)
const signingOut = ref(false)
const view = ref<'servers' | 'sessions'>('servers')
const activeTab = ref('')
const probeAction = ref<'test' | 'connect'>('test')
const CLOSE_REFRESH_STATES: ReadonlySet<CloseState> = new Set([
  'requested',
  'unknown',
  'failed',
  'ended',
])
let changedQueued = false
let refreshPending = false

function findRecord(sessionId: string): PaneRecord | undefined {
  return records.value.find((record) => record.session.id === sessionId)
}
function focusRecord(sessionId: string): void {
  view.value = 'servers'
  activeTab.value = sessionId
  sidebar.value = false
}
function reserveRecord(
  session: SessionView,
  startMode: PaneStartMode,
): void {
  // 同步预占：push 之后到 Pane ready 之间，重复打开会命中本记录。
  const record: PaneRecord = {
    key: ++nextPaneKey,
    session: { ...session },
    startMode,
    handle: null,
  }
  records.value.push(record)
  trackCloseState(session.id)
  focusRecord(session.id)
}
function markSessionEnded(sessionId: string): void {
  if (!endedSessionIds.value.includes(sessionId))
    endedSessionIds.value = [...endedSessionIds.value, sessionId]
}
function trackCloseState(sessionId: string): void {
  if (closeUnsubscribes.has(sessionId)) return
  closeUnsubscribes.set(
    sessionId,
    closeCoordinator.handle(sessionId).subscribe((snapshot) => {
      if (pageDisposed || logoutPending.value || !identity.value) return
      if (snapshot.state === 'ended') markSessionEnded(sessionId)
      // 关闭状态落地后统一走合并刷新，Pane 与无 Pane 路径共用同一入口。
      if (CLOSE_REFRESH_STATES.has(snapshot.state)) scheduleRefresh()
    }),
  )
}
function openSession(item: SessionView): void {
  if (pageDisposed || !identity.value || logoutPending.value) return
  if (findRecord(item.id)) {
    focusRecord(item.id) // D2：已有记录（任何状态）只定位
    return
  }
  const closeState = closeCoordinator.getCloseState(item.id).state
  if (closeState === 'ended') {
    markSessionEnded(item.id)
    notice.value = 'Session 已结束或正在完成关闭（远端已不可接管）'
    return
  }
  // 协调器非 idle 时创建只读 Pane；Pane 自己读取状态并跳过自动 prepare/connect。
  if (closeState === 'idle') {
    if (item.status === 'CONNECTING') {
      notice.value = 'Session 正在建立，暂不能打开，请稍后刷新列表'
      return
    }
    if (item.status === 'CLOSING') {
      notice.value = 'Session 正在关闭，不能打开'
      return
    }
  }
  notice.value = ''
  reserveRecord(item, 'candidate') // D3
}
function registerPane(key: number, handle: PaneHandle): void {
  if (pageDisposed) return
  const record = records.value.find((item) => item.key === key)
  if (!record || record.handle) return
  record.handle = markRaw(handle)
}
function paneClosed(key: number): void {
  if (pageDisposed) return
  const index = records.value.findIndex((item) => item.key === key)
  if (index < 0) return
  const [record] = records.value.splice(index, 1)
  if (activeTab.value === record.session.id)
    activeTab.value = records.value.at(-1)?.session.id ?? ''
}
function disposeCloseCoordinator(): void {
  if (closeCoordinatorDisposed) return
  closeCoordinatorDisposed = true
  closeCoordinator.dispose()
}
function scheduleRefresh(): void {
  if (changedQueued) return
  changedQueued = true
  queueMicrotask(() => {
    changedQueued = false
    if (pageDisposed || logoutPending.value || !identity.value) return
    if (view.value !== 'sessions') {
      // 隐藏期间只标记；进入列表时由挂载的初始 GET 统一加载。
      refreshPending = true
      return
    }
    refreshPending = false
    manager.value?.refresh()
  })
}
function paneChanged(): void {
  if (pageDisposed || logoutPending.value || !identity.value) return
  if (view.value !== 'sessions') return
  scheduleRefresh()
}
function requestSessionClose(item: SessionView): void {
  if (pageDisposed || !identity.value || logoutPending.value) return
  if (closeCoordinator.getCloseState(item.id).state === 'ended') {
    markSessionEnded(item.id)
    notice.value = 'Session 已结束或正在完成关闭（远端已不可接管）'
    return
  }
  notice.value = ''
  closeTarget.value = item
}
function cancelSessionClose(): void {
  closeTarget.value = null
}
function closeResultNotice(snapshot: CloseSnapshot): string {
  switch (snapshot.state) {
    case 'requested':
      return '关闭请求已提交；服务端仍在回收，尚未完全关闭'
    case 'unknown':
      return '关闭请求结果未知，请刷新列表确认 Session 状态'
    case 'failed':
      return '关闭请求未被服务端接受'
    case 'ended':
      return 'Session 已结束或正在完成关闭（远端已不可接管）'
    case 'pending':
      return '关闭请求仍在进行，请稍后刷新列表确认'
    default:
      return ''
  }
}
async function confirmSessionClose(): Promise<void> {
  const target = closeTarget.value
  closeTarget.value = null
  // 确认期间可能已开始登出；登出后不得再发起关闭请求。
  if (!target || pageDisposed || logoutPending.value) return
  // 有记录时定位到 Pane，由它展示关闭状态；无 Pane 时由页面反馈结果。
  const record = findRecord(target.id)
  // 无 Pane 的关闭也要有页面级订阅，才能把状态变化合并到列表刷新。
  trackCloseState(target.id)
  // 唯一关闭所有者仍是协调器：有 Pane 时由它调用已注册的本地 stop，再发 DELETE。
  const task = closeCoordinator.requestClose(target.id)
  if (record) focusRecord(target.id)
  const snapshot = await task
  // 登出已开始或身份已失效时，迟到的关闭结果不得覆盖退出反馈。
  if (pageDisposed || logoutPending.value || !identity.value) return
  if (snapshot.state === 'ended') markSessionEnded(target.id)
  // 关闭结束时仍无 Pane 才需要页面级反馈；有 Pane 时由其自身展示状态。
  if (!findRecord(target.id)) notice.value = closeResultNotice(snapshot)
}
function registeredPanes(): PaneEntry[] {
  return records.value.flatMap((record) =>
    record.handle
      ? [{ sessionId: record.session.id, handle: record.handle }]
      : [],
  )
}
function rejectedSessions(
  entries: PaneEntry[],
  results: PromiseSettledResult<void>[],
  action: string,
): string[] {
  const failed: string[] = []
  results.forEach((result, index) => {
    if (result.status === 'fulfilled') return
    failed.push(entries[index].sessionId)
    console.error(
      `退出登录时终端${action}失败`,
      entries[index].sessionId,
      result.reason,
    )
  })
  return failed
}
function stopSummary(failed: string[]): string {
  return failed.length > 0 ? `；${failed.length} 个终端未确认停止` : ''
}
// 同步 throw 必须转成 rejected Promise，且调用本身仍留在当前同步调用栈。
function startPaneTask(action: () => Promise<void>): Promise<void> {
  try {
    return action()
  } catch (error) {
    return Promise.reject(error)
  }
}
function cleanupSummary(
  stopFailed: string[],
  closeFailed: string[],
): string {
  const parts: string[] = []
  if (stopFailed.length > 0)
    parts.push(`${stopFailed.length} 个终端未确认停止`)
  if (closeFailed.length > 0)
    parts.push(`${closeFailed.length} 个终端本地资源未释放`)
  return `已退出登录；${parts.join('，')}，建议刷新页面`
}
async function connectServer(id = selected.value?.id): Promise<void> {
  if (!id || busy.value || !identity.value || logoutPending.value) return
  busy.value = true
  notice.value = ''
  probe.value = null
  try {
    const settings = identity.value.client_settings
    const session = await createSession(
      id,
      Math.min(80, settings.max_cols),
      Math.min(24, settings.max_rows),
      operations.signal,
    )
    if (operations.signal.aborted) return
    if (findRecord(session.id)) focusRecord(session.id)
    else reserveRecord(session, 'attach')
  } catch (error) {
    if (operations.signal.aborted) return
    const found = confirmation(error)
    if (found) {
      probeTarget.value = id
      probeAction.value = 'connect'
      probe.value = found
    } else notice.value = message(error)
  } finally {
    busy.value = false
  }
}
function retryProbe(): void {
  if (probeAction.value === 'connect') void connectServer(probeTarget.value)
  else void testServer(probeTarget.value)
}
const router = useRouter()
const items = ref<ServerView[]>([])
const selected = ref<ServerView | null>(null)
const search = ref('')
const group = ref('')
const groups = ref<string[]>([])
let selectionGeneration = 0
const collapsed = ref(new Set<string>())
const sidebar = ref(false)
const editing = ref(false)
const editTarget = ref<ServerView | null>(null)
const deleting = ref(false)
const busy = ref(false)
const notice = ref('')
const closeTarget = ref<SessionView | null>(null)
const loading = ref(false)
const probe = ref<Probe | null>(null)
const probeTarget = ref('')
let query: AbortController | undefined
const operations = new AbortController()
let debounce: ReturnType<typeof setTimeout> | undefined
const grouped = computed(() =>
  [...new Set(items.value.map((s) => s.group_name))].map((name) => ({
    name,
    members: items.value.filter((s) => s.group_name === name),
  })),
)
async function load(): Promise<void> {
  if (logoutPending.value) return
  query?.abort()
  const controller = new AbortController()
  query = controller
  loading.value = true
  const params = new URLSearchParams({ search: search.value })
  if (group.value) params.set('group_name', JSON.parse(group.value) as string)
  try {
    const data = await request<{ items: ServerView[] }>(
      `/api/servers?${params}`,
      'GET',
      undefined,
      controller.signal,
    )
    if (controller.signal.aborted) return
    items.value = data.items
    if (!search.value && !group.value)
      groups.value = [
        ...new Set(data.items.map((s) => s.group_name).filter(Boolean)),
      ]
  } catch (error) {
    if (!controller.signal.aborted) notice.value = message(error)
  } finally {
    if (!controller.signal.aborted) loading.value = false
  }
}
watch([search, group], () => {
  clearTimeout(debounce)
  debounce = setTimeout(() => void load(), 180)
})
watch(view, (current) => {
  if (current !== 'sessions' || !refreshPending) return
  // SessionManager 挂载时的初始 GET 已完成必要刷新，这里只消费标记。
  refreshPending = false
})
onMounted(load)
onBeforeUnmount(() => {
  pageDisposed = true
  query?.abort()
  operations.abort()
  for (const unsubscribe of closeUnsubscribes.values()) unsubscribe()
  closeUnsubscribes.clear()
  disposeCloseCoordinator()
  clearTimeout(debounce)
  probe.value = null
})
function toggleGroup(name: string): void {
  if (collapsed.value.has(name)) collapsed.value.delete(name)
  else collapsed.value.add(name)
}
async function choose(item: ServerView): Promise<void> {
  if (logoutPending.value) return
  const generation = ++selectionGeneration
  try {
    const server = await request<ServerView>(
      `/api/servers/${item.id}`,
      'GET',
      undefined,
      operations.signal,
    )
    if (generation !== selectionGeneration || operations.signal.aborted)
      return
    selected.value = server
    activeTab.value = ''
    view.value = 'servers'
    if (matchMedia('(max-width: 720px)').matches) sidebar.value = false
  } catch (error) {
    if (!operations.signal.aborted) notice.value = message(error)
  }
}
function edit(value: ServerView | null): void {
  if (logoutPending.value) return
  editTarget.value = value
  editing.value = true
}
async function saved(value: ServerView): Promise<void> {
  editing.value = false
  selectionGeneration++
  selected.value = value
  if (value.group_name && !groups.value.includes(value.group_name))
    groups.value.push(value.group_name)
  notice.value = '服务器已保存'
  await load()
}
async function remove(): Promise<void> {
  if (!selected.value || busy.value) return
  busy.value = true
  try {
    await request(
      `/api/servers/${selected.value.id}`,
      'DELETE',
      {},
      operations.signal,
    )
    deleting.value = false
    selected.value = null
    notice.value = '服务器已删除'
    await load()
  } catch (error) {
    if (!operations.signal.aborted) notice.value = message(error)
  } finally {
    busy.value = false
  }
}
async function testServer(id = selected.value?.id): Promise<void> {
  if (!id || busy.value || logoutPending.value) return
  busy.value = true
  notice.value = ''
  probe.value = null
  try {
    const result = await request<{
      host: string
      port: number
      username: string
    }>(`/api/servers/${id}/test`, 'POST', {}, operations.signal)
    if (!operations.signal.aborted)
      notice.value = `SSH 连接测试成功：${result.username}@${result.host}:${result.port}`
  } catch (error) {
    if (operations.signal.aborted) return
    const found = confirmation(error)
    if (found) {
      probeTarget.value = id
      probeAction.value = 'test'
      probe.value = found
    } else notice.value = message(error)
  } finally {
    busy.value = false
  }
}
async function confirmed(): Promise<void> {
  const id = probeTarget.value
  probe.value = null
  try {
    const server = await request<ServerView>(
      `/api/servers/${id}`,
      'GET',
      undefined,
      operations.signal,
    )
    if (selected.value?.id === id) selected.value = server
    await load()
    if (probeAction.value === 'connect') await connectServer(id)
    else await testServer(id)
  } catch (error) {
    if (!operations.signal.aborted) notice.value = message(error)
  }
}
function openSessionManager(): void {
  sidebar.value = false
  view.value = 'sessions'
}
async function signOut(): Promise<void> {
  if (signingOut.value) return
  signingOut.value = true
  logoutPending.value = true // 同步：Pane watcher 调用 stop()，候选接管同步失效
  operations.abort()
  query?.abort()
  clearTimeout(debounce)
  probe.value = null
  manager.value?.invalidate() // 同步撤销列表发布身份，早于 logout 请求
  closeTarget.value = null
  const entries = registeredPanes()
  const stopTask = Promise.allSettled(
    entries.map((entry) => startPaneTask(() => entry.handle.stop())),
  )
  // stop 调用已在当前同步栈发起；dispose 只清理协调器自身，不影响 entries 句柄。
  disposeCloseCoordinator()
  try {
    try {
      await logout() // 与本地排空并行，不等 drain
    } catch (error) {
      const stopFailed = rejectedSessions(entries, await stopTask, '停止')
      notice.value =
        '退出未完成，请重试。' + message(error) + stopSummary(stopFailed)
      return
    }
    const stopFailed = rejectedSessions(entries, await stopTask, '停止')
    const closeFailed = rejectedSessions(
      entries,
      await Promise.allSettled(
        entries.map((entry) => startPaneTask(() => entry.handle.close())),
      ),
      '本地资源释放',
    )
    try {
      await router.replace('/login') // close 失败不阻止跳转
    } catch (error) {
      // 跳转失败不改写已成功的 logout，也不重新 logout。
      console.error('退出登录后跳转失败', error)
    }
    // 跳转时路由守卫可能改写 authNotice，所以清理反馈在跳转尝试后写入。
    if (stopFailed.length > 0 || closeFailed.length > 0)
      authNotice.value = cleanupSummary(stopFailed, closeFailed)
  } finally {
    signingOut.value = false
  }
}
</script>
<template>
  <div class="app" :class="{ 'sidebar-visible': sidebar }">
    <header class="topbar">
      <div class="brand">
        <button
          v-if="view === 'servers'"
          class="icon-button"
          :aria-expanded="sidebar"
          aria-label="显示或隐藏服务器列表"
          @click="sidebar = !sidebar"
        >
          ☰</button
        ><span class="wordmark">Web SSH</span>
      </div>
      <div class="top-actions">
        <button
          v-if="view === 'servers'"
          :disabled="logoutPending"
          @click="openSessionManager"
        >
          会话管理
        </button>
        <button v-else @click="view = 'servers'">返回服务器</button>
        <button
          v-if="view === 'servers'"
          :disabled="logoutPending"
          @click="edit(null)"
        >
          添加服务器</button
        ><ThemeButton /><button :disabled="signingOut" @click="signOut">
          {{ logoutPending ? '重试退出登录' : '退出登录' }}
        </button>
      </div>
    </header>
    <button
      v-if="view === 'servers' && sidebar"
      class="drawer-backdrop"
      aria-label="隐藏服务器列表"
      @click="sidebar = false"
    />
    <aside
      v-if="view === 'servers' && sidebar"
      class="sidebar"
      aria-label="服务器列表"
    >
      <div class="section-heading">
        服务器 <span>{{ items.length }}</span>
      </div>
      <label class="sr-only" for="search">搜索服务器名称</label
      ><input
        id="search"
        v-model="search"
        type="search"
        placeholder="搜索服务器名称…"
      />
      <label class="sr-only" for="group">分组筛选</label
      ><select id="group" v-model="group">
        <option value="">全部分组</option>
        <option :value="JSON.stringify('')">未分组</option>
        <option
          v-for="name in groups"
          :key="name"
          :value="JSON.stringify(name)"
        >
          {{ name }}
        </option>
      </select>
      <section
        v-for="section in grouped"
        :key="section.name"
        class="server-group"
      >
        <button
          class="group-heading"
          :aria-expanded="!collapsed.has(section.name)"
          @click="toggleGroup(section.name)"
        >
          {{ collapsed.has(section.name) ? '›' : '⌄' }}
          {{ section.name || '未分组' }}
          <span>{{ section.members.length }}</span>
        </button>
        <div v-if="!collapsed.has(section.name)">
          <button
            v-for="item in section.members"
            :key="item.id"
            class="server-row"
            :class="{ selected: selected?.id === item.id }"
            @click="choose(item)"
          >
            <strong>{{ item.name }}</strong
            ><small>{{ item.username }}@{{ item.host }}</small>
          </button>
        </div>
      </section>
      <p v-if="!items.length && !loading" class="muted">没有匹配的服务器</p>
      <p v-if="loading" role="status">正在加载…</p>
      <button @click="load">刷新列表</button>
    </aside>
    <main class="main">
      <nav
        v-if="view === 'servers' && records.length"
        class="terminal-tabs"
        aria-label="终端标签"
      >
        <button :class="{ selected: !activeTab }" @click="activeTab = ''">
          服务器
        </button>
        <button
          v-for="record in records"
          :key="record.key"
          :class="{ selected: activeTab === record.session.id }"
          @click="activeTab = record.session.id"
        >
          {{ record.session.name }} · {{ record.session.id.slice(-4) }}
        </button>
      </nav>
      <TerminalPane
        v-for="record in records"
        :key="record.key"
        v-show="view === 'servers' && activeTab === record.session.id"
        :session="record.session"
        :start-mode="record.startMode"
        :active="view === 'servers' && activeTab === record.session.id"
        :close-handle="closeCoordinator.handle(record.session.id)"
        @ready="(handle) => registerPane(record.key, handle)"
        @failed="(handle) => registerPane(record.key, handle)"
        @closed="paneClosed(record.key)"
        @changed="paneChanged"
      />
      <SessionManager
        v-if="view === 'sessions'"
        ref="manager"
        :servers="items"
        :can-refresh="identity !== null && !logoutPending"
        :auth-token="identity"
        :ended-ids="endedSessionIds"
        @open="openSession"
        @close="requestSessionClose"
      />
      <section
        v-else-if="selected && !activeTab"
        class="server-detail"
      >
        <div class="detail-heading">
          <h1>{{ selected.name }}</h1>
          <div class="actions">
            <button
              class="primary"
              :disabled="busy || logoutPending"
              @click="connectServer()"
            >
              连接终端
            </button>
            <button :disabled="busy" @click="testServer()">
              {{ busy ? '处理中…' : '测试连接' }}</button
            ><button @click="edit(selected)">编辑</button
            ><button class="danger" @click="deleting = true">删除</button>
          </div>
        </div>
        <dl class="details">
          <dt>Host</dt>
          <dd>{{ selected.host }}</dd>
          <dt>Port</dt>
          <dd>{{ selected.port }}</dd>
          <dt>Username</dt>
          <dd>{{ selected.username }}</dd>
          <dt>分组</dt>
          <dd>{{ selected.group_name || '未分组' }}</dd>
          <dt>备注</dt>
          <dd>{{ selected.remark || '—' }}</dd>
          <dt>Host Key</dt>
          <dd>{{ selected.host_key ? '已信任' : '尚未信任' }}</dd>
          <template v-if="selected.host_key"
            ><dt>可信目标</dt>
            <dd>
              {{ selected.host_key.host }} : {{ selected.host_key.port }}
            </dd>
            <dt>算法</dt>
            <dd>{{ selected.host_key.algorithm }}</dd>
            <dt>SHA256 指纹</dt>
            <dd class="mono">
              {{ selected.host_key.fingerprint }}
            </dd></template
          >
          <dt>创建时间</dt>
          <dd>{{ new Date(selected.created_at).toLocaleString() }}</dd>
          <dt>更新时间</dt>
          <dd>{{ new Date(selected.updated_at).toLocaleString() }}</dd>
        </dl>
      </section>
      <div v-else-if="!activeTab" class="empty-workspace">
        <button @click="sidebar = true">选择服务器</button
        ><button @click="edit(null)">添加服务器</button>
      </div>
      <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    </main>
    <ServerForm
      v-if="editing"
      :key="editTarget?.id ?? 'new-server'"
      :disabled="logoutPending"
      :server="editTarget"
      @close="editing = false"
      @saved="saved"
    />
    <HostKeyDialog
      v-if="probe"
      :server-id="probeTarget"
      :probe="probe"
      :purpose="probeAction"
      @close="probe = null"
      @confirmed="confirmed"
      @retry="retryProbe"
    />
    <Modal
      v-if="deleting && selected"
      title="删除服务器？"
      @close="deleting = false"
      ><div class="dialog-body">
        <p>将删除这条服务器连接配置。</p>
        <p class="mono">
          {{ selected.name }} / {{ selected.host }} : {{ selected.port }}
        </p>
        <p v-if="notice" role="alert">{{ notice }}</p>
      </div>
      <div class="dialog-footer">
        <button @click="deleting = false">取消</button
        ><button class="primary" :disabled="busy" @click="remove">
          确认删除
        </button>
      </div></Modal
    >
    <Modal
      v-if="closeTarget"
      title="关闭远端 SSH Session？"
      @close="cancelSessionClose"
      ><div class="dialog-body">
        <p>
          将请求服务端关闭这条远端 SSH Session；不是关闭列表、本地终端或浏览器标签。
        </p>
        <p class="mono">
          {{ closeTarget.name }} / {{ closeTarget.id }}
        </p>
      </div>
      <div class="dialog-footer">
        <button @click="cancelSessionClose">取消</button
        ><button
          class="primary"
          :disabled="logoutPending"
          @click="confirmSessionClose"
        >
          确认关闭
        </button>
      </div></Modal
    >
  </div>
</template>
