<script setup lang="ts">
import { computed, ref, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  request,
  message,
  confirmation,
  type ServerView,
  type Probe,
} from '../api/management'
import { logout, identity, logoutPending } from '../stores/auth'
import { createSession, type SessionView } from '../api/sessions'
import TerminalPane from '../components/terminal/TerminalPane.vue'
import ThemeButton from '../components/layout/ThemeButton.vue'
import Modal from '../components/layout/Modal.vue'
import ServerForm from '../components/server/ServerForm.vue'
import HostKeyDialog from '../components/server/HostKeyDialog.vue'
import SessionManager from '../components/session/SessionManager.vue'
const tabs = ref<SessionView[]>([])
const signingOut = ref(false)
const view = ref<'servers' | 'sessions'>('servers')
const panes = new Map<
  string,
  { stop(): Promise<void>; dispose(): Promise<void> }
>()
function setPane(id: string, pane: unknown): void {
  if (pane)
    panes.set(id, pane as { stop(): Promise<void>; dispose(): Promise<void> })
  else panes.delete(id)
}
const activeTab = ref('')
const probeAction = ref<'test' | 'connect'>('test')
function removeTab(id: string): void {
  tabs.value = tabs.value.filter((tab) => tab.id !== id)
  if (activeTab.value === id) activeTab.value = tabs.value.at(-1)?.id ?? ''
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
    tabs.value.push(session)
    activeTab.value = session.id
    view.value = 'servers'
    sidebar.value = false
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
onMounted(load)
onBeforeUnmount(() => {
  query?.abort()
  operations.abort()
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
  logoutPending.value = true
  operations.abort()
  query?.abort()
  clearTimeout(debounce)
  probe.value = null
  const stopped = Promise.allSettled(
    [...panes.values()].map((pane) => pane.stop()),
  )
  try {
    await logout()
    await stopped
    await Promise.allSettled(
      [...panes.values()].map((pane) => pane.dispose()),
    )
    tabs.value = []
    await router.replace('/login')
  } catch (error) {
    notice.value = '退出未完成，请重试。' + message(error)
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
        v-if="view === 'servers' && tabs.length"
        class="terminal-tabs"
        aria-label="终端标签"
      >
        <button :class="{ selected: !activeTab }" @click="activeTab = ''">
          服务器
        </button>
        <button
          v-for="tab in tabs"
          :key="tab.id"
          :class="{ selected: activeTab === tab.id }"
          @click="activeTab = tab.id"
        >
          {{ tab.name }} · {{ tab.id.slice(-4) }}
        </button>
      </nav>
      <TerminalPane
        v-for="tab in tabs"
        :key="tab.id"
        v-show="view === 'servers' && activeTab === tab.id"
        :ref="(pane) => setPane(tab.id, pane)"
        :session="tab"
        :active="view === 'servers' && activeTab === tab.id"
        @closed="removeTab(tab.id)"
      />
      <SessionManager
        v-if="view === 'sessions'"
        :servers="items"
        :can-refresh="identity !== null && !logoutPending"
        :auth-token="identity"
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
  </div>
</template>
