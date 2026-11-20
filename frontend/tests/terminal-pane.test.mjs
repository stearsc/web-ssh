import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import {
  createRenderer,
  defineComponent,
  h,
  nextTick,
  reactive,
} from 'vue'
import {
  acceptIdentity,
  clearIdentity,
  logoutPending,
} from '../src/stores/auth.ts'
import { CloseCoordinator } from '../src/session/close.ts'
import { TerminalClient } from '../src/terminal/client.ts'
import { SessionRecovery } from '../src/terminal/recovery.ts'
import { SessionTakeover } from '../src/terminal/takeover.ts'

// 挂载实际 SFC 和 TerminalClient，仅替换浏览器与 xterm 外部接口。
const sourceURL = new URL(
  '../src/components/terminal/TerminalPane.vue',
  import.meta.url,
)
const vueModuleURL = import.meta.resolve('vue')
const { descriptor } = parse(await readFile(sourceURL, 'utf8'))
const compiled = compileScript(descriptor, {
  id: 'terminal-pane-test',
  inlineTemplate: true,
})
const moduleURL = (source) =>
  `data:text/javascript,${encodeURIComponent(source)}`
const code = ts
  .transpileModule(compiled.content, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.ESNext,
    },
  })
  .outputText.replace(/import ['"]@xterm\/xterm\/css\/xterm.css['"];?/g, '')
  .replace(/from ['"]([^'"]+)['"]/g, (_match, specifier) => {
    let url
    if (specifier === '@xterm/xterm')
      url = moduleURL(
        'export class Terminal { constructor(...args) { return new globalThis.__paneTerminal(...args) } }',
      )
    else if (specifier === '@xterm/addon-fit')
      url = moduleURL(
        'export class FitAddon { constructor(...args) { return new globalThis.__paneFitAddon(...args) } }',
      )
    else if (specifier.endsWith('Modal.vue'))
      url = moduleURL(`
        import { defineComponent, h } from ${JSON.stringify(vueModuleURL)}
        export default defineComponent({
          props: ['title'],
          emits: ['close'],
          setup(props, { slots }) {
            return () => h('dialog', { class: 'modal-stub' }, [
              h('h2', { class: 'modal-title' }, props.title),
              slots.default ? slots.default() : null,
            ])
          },
        })
      `)
    else if (specifier.endsWith('.vue'))
      url = moduleURL('export default { render() { return null } }')
    else if (specifier.startsWith('.'))
      url = new URL(
        specifier + (specifier.endsWith('.ts') ? '' : '.ts'),
        sourceURL,
      ).href
    else url = import.meta.resolve(specifier)
    return `from ${JSON.stringify(url)}`
  })

const settings = {
  max_control_message_size: 16384,
  max_input_message_size: 64,
  output_chunk_size: 64,
  credential_timeout: 1,
  bind_timeout: 1,
  operation_wait_timeout: 1,
  write_timeout: 1,
  max_cols: 500,
  max_rows: 300,
  write_queue_bytes: 128,
  write_drain_timeout: 1,
  reconnect_max_attempts: 10,
  reconnect_delay: 1,
  reconnect_stable_time: 30,
}
const flush = async () => {
  for (let i = 0; i < 40; i++) await Promise.resolve()
  await nextTick()
}
const node = (type, text = '') => ({
  type,
  text,
  props: {},
  children: [],
  parent: null,
})
const teleportTargets = new Map()
const renderer = createRenderer({
  querySelector: (selector) => teleportTargets.get(selector),
  createElement: (type) =>
    Object.assign(node(type), {
      clientWidth: 800,
      style: {},
      focus() {},
      getBoundingClientRect: () => ({ bottom: 45, right: 800 }),
      showPopover() {
        this.isPopoverOpen = true
      },
      hidePopover() {
        this.isPopoverOpen = false
      },
    }),
  createText: (text) => node('#text', text),
  createComment: () => node('#comment'),
  setText: (el, text) => {
    el.text = text
  },
  setElementText: (el, text) => {
    el.text = text
    el.children = []
  },
  patchProp: (el, key, _old, value) => {
    el.props[key] = value
  },
  insert(el, parent, anchor = null) {
    if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1)
    el.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, el)
  },
  remove(el) {
    if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1)
    el.parent = null
  },
  parentNode: (el) => el.parent,
  nextSibling: (el) =>
    el.parent?.children[el.parent.children.indexOf(el) + 1] ?? null,
})
function textContent(el) {
  return (
    String(el.text ?? '') +
    el.children.map((child) => textContent(child)).join('')
  ).trim()
}
function findAll(root, predicate) {
  const found = []
  const visit = (el) => {
    if (predicate(el)) found.push(el)
    el.children.forEach(visit)
  }
  visit(root)
  return found
}
function texts(root, className) {
  return findAll(root, (el) =>
    String(el.props.class ?? '').split(/\s+/).includes(className),
  ).map(textContent)
}
function byType(root, type) {
  return findAll(root, (el) => el.type === type)
}
async function button(root, label) {
  // 工具栏迁移后，查询操作入口前先通过真实触发器展开菜单。
  await root.openMenu?.()
  return byType(root, 'button').find((el) => textContent(el) === label)
}
function click(el) {
  assert.ok(el)
  el.props.onClick?.({ type: 'click', stopPropagation() {} })
}

const session = {
  id: 'a'.repeat(32),
  server_id: 'b'.repeat(32),
  name: 'test',
  host: 'localhost',
  port: 22,
  username: 'test',
  status: 'CONNECTED',
  binding_generation: 0,
  current_cols: 80,
  current_rows: 24,
  created_at: '2026-11-17T00:00:00Z',
  disconnected_at: null,
  last_attached_at: null,
  offline_expires_at: null,
  remaining_seconds: null,
}
function detail(overrides = {}) {
  return {
    ...session,
    binding_generation: 5,
    status: 'CONNECTED',
    ...overrides,
  }
}

class TerminalStub {
  static instances = []
  constructor(options) {
    this.cols = 80
    this.rows = 24
    this.options = options
    this.selection = ''
    this.disposed = 0
    this.loaded = 0
    this.opened = 0
    this.subscriptions = []
    TerminalStub.instances.push(this)
  }
  loadAddon() {
    this.loaded++
    if (this.loadAddonThrows) throw this.loadAddonThrows
  }
  open() {
    this.opened++
    if (this.openThrows) throw this.openThrows
  }
  focus() {}
  dispose() {
    this.disposed++
    if (this.disposeThrows) throw this.disposeThrows
  }
  resize(cols, rows) {
    this.cols = cols
    this.rows = rows
  }
  subscription(kind) {
    const item = {
      kind,
      disposed: 0,
      dispose() {
        this.disposed++
      },
    }
    this.subscriptions.push(item)
    return item
  }
  attachCustomKeyEventHandler(callback) {
    this.keyHandler = callback
  }
  hasSelection() {
    return this.selection !== ''
  }
  getSelection() {
    return this.selection
  }
  onSelectionChange(callback) {
    this.selectionChanged = callback
    return this.subscription('selection')
  }
  onData(callback) {
    this.data = callback
    if (this.onDataThrows) throw this.onDataThrows
    return this.subscription('data')
  }
  onBinary(callback) {
    this.binary = callback
    if (this.onBinaryThrows) throw this.onBinaryThrows
    return this.subscription('binary')
  }
  onResize() {
    if (this.onResizeThrows) throw this.onResizeThrows
    return this.subscription('resize')
  }
  write(_bytes, callback) {
    callback()
  }
}
class FitAddonStub {
  static instances = []
  constructor() {
    if (globalThis.__paneFitAddonThrows) throw globalThis.__paneFitAddonThrows
    FitAddonStub.instances.push(this)
  }
  proposeDimensions() {
    return { cols: 80, rows: 24 }
  }
}
class ObserverStub {
  static instances = []
  constructor(callback) {
    if (globalThis.__paneObserverThrows) throw globalThis.__paneObserverThrows
    this.callback = callback
    this.disconnected = 0
    this.observed = 0
    ObserverStub.instances.push(this)
  }
  observe() {
    this.observed++
    if (globalThis.__paneObserveThrows) throw globalThis.__paneObserveThrows
  }
  disconnect() {
    this.disconnected++
  }
}
class TrackedWindow extends EventTarget {
  added = []
  removed = []
  addEventListener(type, callback, options) {
    this.added.push(type)
    return super.addEventListener(type, callback, options)
  }
  removeEventListener(type, callback, options) {
    this.removed.push(type)
    return super.removeEventListener(type, callback, options)
  }
}
class Socket {
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  readyState = 0
  bufferedAmount = 0
  sent = []
  constructor() {}
  send(value) {
    this.sent.push(value)
  }
  close() {
    this.readyState = 3
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  message(value) {
    this.onmessage?.({ data: JSON.stringify(value) })
  }
}
Socket.instances = []

async function mountPane(t, options = {}) {
  TerminalStub.instances = []
  FitAddonStub.instances = []
  ObserverStub.instances = []
  const keys = [
    'fetch',
    'WebSocket',
    'location',
    'document',
    'window',
    'navigator',
    'getComputedStyle',
    'ResizeObserver',
    'MutationObserver',
    '__paneTerminal',
    '__paneFitAddon',
    '__paneFitAddonThrows',
    '__paneObserverThrows',
    '__paneObserveThrows',
  ]
  const originals = keys.map((key) => [
    key,
    Object.getOwnPropertyDescriptor(globalThis, key),
  ])
  const sockets = []
  const requests = []
  const window = new TrackedWindow()
  globalThis.window = window
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: { onLine: options.online ?? true },
  })
  const ResizeObserverClass = options.ResizeObserver ?? ObserverStub
  const MutationObserverClass = options.MutationObserver ?? ObserverStub
  globalThis.ResizeObserver = ResizeObserverClass
  globalThis.MutationObserver = MutationObserverClass
  globalThis.document = {
    visibilityState: 'visible',
    documentElement: {},
  }
  globalThis.getComputedStyle = () => ({ getPropertyValue: () => '' })
  const TerminalClass = options.Terminal ?? TerminalStub
  globalThis.__paneTerminal = TerminalClass
  globalThis.__paneFitAddon = options.FitAddon ?? FitAddonStub
  globalThis.__paneFitAddonThrows = options.fitAddonThrows ?? null
  globalThis.__paneObserverThrows = options.observerThrows ?? null
  globalThis.__paneObserveThrows = options.observeThrows ?? null
  Socket.instances = sockets
  globalThis.WebSocket = class extends Socket {
    constructor(url) {
      super(url)
      sockets.push(this)
    }
  }
  globalThis.WebSocket.OPEN = Socket.OPEN
  globalThis.WebSocket.CLOSING = Socket.CLOSING
  globalThis.WebSocket.CLOSED = Socket.CLOSED
  globalThis.location = { href: 'http://localhost/', protocol: 'http:' }
  globalThis.fetch = async (path, init = {}) => {
    const record = {
      path,
      method: init.method ?? 'GET',
      body: init.body === undefined ? undefined : JSON.parse(init.body),
      signal: init.signal,
    }
    requests.push(record)
    if (options.fetch) {
      const custom = await options.fetch(record)
      if (custom !== undefined) return custom
    }
    if (record.method === 'GET' && path.startsWith('/api/sessions/'))
      return Response.json({
        code: 0,
        message: 'ok',
        data: options.detail ?? detail(),
      })
    if (record.method === 'POST' && path.endsWith('/takeover'))
      return Response.json({
        code: 0,
        message: 'ok',
        data: {
          credential: 'a'.repeat(43),
          expires_at: '2099-01-01T00:00:00Z',
        },
      })
    if (record.method === 'DELETE')
      return Response.json({ code: 0, message: 'ok', data: {} })
    throw new Error(`unexpected request: ${record.method} ${path}`)
  }
  if (options.identity === false) clearIdentity()
  else
    acceptIdentity({
      expires_at: '2099-01-01T00:00:00Z',
      client_settings: options.settings ?? settings,
    })
  const { default: Pane } = await import(moduleURL(code))
  const closeCoordinator = options.closeCoordinator ?? new CloseCoordinator()
  const closeHandle =
    options.closeHandle ??
    closeCoordinator.handle((options.session ?? session).id)
  const events = { ready: [], failed: [], closed: 0, changed: 0 }
  const props = reactive({
    active: options.active ?? true,
    session: options.session ?? session,
    startMode: options.startMode ?? 'attach',
    paneKey: 1,
    menuOpen: options.menuOpen ?? true,
  })
  let pane
  const Root = defineComponent({
    setup() {
      return () =>
        h(Pane, {
          ...props,
          closeHandle,
          isCurrentPane: () =>
            props.active &&
            !logoutPending.value &&
            (options.isCurrentPane?.() ?? true),
          menuAllowed: () =>
            props.active &&
            props.menuOpen &&
            !logoutPending.value &&
            (options.isCurrentPane?.() ?? true),
          onToggleMenu: () => {
            props.menuOpen = !props.menuOpen
          },
          onCloseMenu: () => {
            props.menuOpen = false
          },
          ref: (value) => {
            pane = value
          },
          onReady: (handle) => events.ready.push(handle),
          onFailed: (handle) => events.failed.push(handle),
          onClosed: () => {
            events.closed++
          },
          onChanged: () => {
            events.changed++
          },
        })
    },
  })
  const root = node('root')
  root.openMenu = async () => {
    if (!props.active || props.menuOpen) return
    const trigger = byType(root, 'button').find(
      (el) => el.props['aria-label'] === '当前终端操作',
    )
    if (trigger) {
      click(trigger)
      await flush()
    }
  }
  teleportTargets.clear()
  for (const selector of ['#terminal-tools', '#terminal-tab-close-1']) {
    const target = node('target')
    root.children.push(target)
    target.parent = root
    teleportTargets.set(selector, target)
  }
  const app = renderer.createApp(Root)
  app.mount(root)
  let mounted = true
  const unmount = () => {
    if (!mounted) return
    mounted = false
    app.unmount()
  }
  t.after(async () => {
    try {
      await pane?.close()
    } catch {}
    closeCoordinator.dispose()
    unmount()
    clearIdentity()
    for (const [key, descriptor] of originals) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else delete globalThis[key]
    }
  })
  await flush()
  return {
    sockets,
    requests,
    root,
    pane,
    events,
    props,
    window,
    terminalInstances: TerminalStub.instances,
    fitInstances: FitAddonStub.instances,
    observers: ObserverStub.instances,
    closeCoordinator,
    closeHandle,
    unmount,
  }
}
function bind(socket, generation, start = 0, end = 0) {
  socket.open()
  socket.message({
    type: 'bind_success',
    binding_generation: generation,
    replay_start_offset: start,
    replay_end_offset: end,
    current_cols: 80,
    current_rows: 24,
  })
}

test('实际 Pane：1006 恢复成功清除当前结束提示，历史诊断保留，旧事件不能覆盖', async (t) => {
  for (const initialBound of [true, false]) {
    await t.test(
      initialBound ? '已绑定后断开' : '首次绑定前断开',
      async (t) => {
        t.mock.timers.enable({ apis: ['setTimeout'] })
        const h = await mountPane(t)
        if (initialBound) {
          bind(h.sockets[0], 1)
          await flush()
          assert.deepEqual(texts(h.root, 'connection-state'), ['已连接'])
        }
        const old = {
          close: h.sockets[0].onclose,
          error: h.sockets[0].onerror,
          message: h.sockets[0].onmessage,
          open: h.sockets[0].onopen,
        }
        old.close({ code: 1006 })
        await flush()
        assert.deepEqual(texts(h.root, 'connection-state'), [
          '等待自动恢复 1/10',
        ])
        assert.ok(texts(h.root, 'notice').includes('连接已结束（1006）'))
        t.mock.timers.tick(1000)
        await flush()
        bind(h.sockets[1], initialBound ? 2 : 1)
        await flush()
        assert.deepEqual(
          h.requests.map((r) => r.body),
          [
            { mode: 'attach', expected_generation: 0 },
            { mode: 'attach', expected_generation: initialBound ? 1 : 0 },
          ],
        )
        assert.deepEqual(texts(h.root, 'connection-state'), ['已连接'])
        assert.equal(
          texts(h.root, 'notice').includes('连接已结束（1006）'),
          false,
        )
        assert.match(
          texts(h.root, 'connection-diagnostic').join(),
          /历史.*1006/,
        )
        const before = texts(h.root, 'notice')
        old.close({ code: 4001 })
        old.error()
        old.open()
        old.message({
          data: JSON.stringify({
            type: 'taken_over',
            message: '旧连接迟到通知',
          }),
        })
        await flush()
        assert.deepEqual(texts(h.root, 'connection-state'), ['已连接'])
        assert.deepEqual(texts(h.root, 'notice'), before)
        assert.equal(h.sockets.length, 2)
        assert.equal(h.sockets[1].readyState, 1)
      },
    )
  }
})

test('实际 Pane：恢复截断提示与历史异常分开，前缀不完整不显示已连接', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const h = await mountPane(t)
  bind(h.sockets[0], 1)
  await flush()
  h.sockets[0].onclose({ code: 1006 })
  await flush()
  t.mock.timers.tick(1000)
  await flush()
  bind(h.sockets[1], 2, 8, 8)
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['正在连接'])
  h.sockets[1].message({
    type: 'history_truncated',
    requested_start_offset: 0,
    replay_start_offset: 8,
    replay_end_offset: 8,
    lost_bytes: 8,
  })
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['已连接'])
  assert.match(texts(h.root, 'notice').join(), /截断/)
  assert.equal(h.terminalInstances.length, 2)
  const rebuilt = h.terminalInstances[1]
  assert.equal(typeof rebuilt.keyHandler, 'function')
  rebuilt.selection = '重建后仍可复制'
  rebuilt.selectionChanged()
  await flush()
  assert.equal((await button(h.root, '复制选中文本')).props.disabled, false)
  assert.match(texts(h.root, 'connection-diagnostic').join(), /1006/)
})

test('实际 Pane：绑定校验失败、1000、4001 和明确 taken_over 仍停止并保留提示', async (t) => {
  for (const kind of ['invalid', 1000, 4001, 'taken_over']) {
    await t.test(String(kind), async (t) => {
      t.mock.timers.enable({ apis: ['setTimeout'] })
      const h = await mountPane(t)
      bind(h.sockets[0], kind === 'invalid' ? 2 : 1)
      await flush()
      if (typeof kind === 'number') h.sockets[0].onclose({ code: kind })
      if (kind === 'taken_over') {
        const lateClose = h.sockets[0].onclose
        h.sockets[0].message({
          type: 'taken_over',
          message: '会话已被其他设备接管',
        })
        lateClose({ code: 1006 })
      }
      await flush()
      t.mock.timers.tick(20000)
      await flush()
      assert.deepEqual(texts(h.root, 'connection-state'), ['未连接'])
      assert.equal(h.sockets.length, 1)
      const expected =
        kind === 'invalid'
          ? /绑定代次不连续/
          : kind === 'taken_over'
            ? /会话已被其他设备接管/
            : new RegExp(String(kind))
      assert.match(texts(h.root, 'notice').join(), expected)
    })
  }
})

test('网络状态仅影响展示：离线立即提示，online 不冒充恢复，当前有效消息才确认', async (t) => {
  const h = await mountPane(t)
  bind(h.sockets[0], 1)
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['已连接'])
  window.dispatchEvent(new Event('offline'))
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '网络已断开，等待网络恢复',
  ])
  // 即使旧 socket 仍有消息到达，也不能覆盖系统报告的离线。
  h.sockets[0].message({ type: 'status', status: 'CONNECTED' })
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '网络已断开，等待网络恢复',
  ])
  window.dispatchEvent(new Event('online'))
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '网络已恢复，等待连接确认',
  ])
  assert.equal(h.requests.length, 1)
  assert.equal(h.sockets.length, 1)
  h.sockets[0].message({ type: 'status', status: 'CONNECTED' })
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['已连接'])
  await h.pane.stop()
  window.dispatchEvent(new Event('offline'))
  window.dispatchEvent(new Event('online'))
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['未连接'])
  assert.equal(h.requests.length, 1)
})

test('离线期间旧连接关闭：保持真实重试预算，迟到旧消息不能确认网络恢复', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const h = await mountPane(t)
  bind(h.sockets[0], 1)
  await flush()
  const oldMessage = h.sockets[0].onmessage
  window.dispatchEvent(new Event('offline'))
  h.sockets[0].onclose({ code: 1011 })
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '网络已断开，等待网络恢复',
  ])
  window.dispatchEvent(new Event('online'))
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['等待自动恢复 1/10'])
  t.mock.timers.tick(1000)
  await flush()
  oldMessage({ data: JSON.stringify({ type: 'status', status: 'CONNECTED' }) })
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '网络已恢复，等待连接确认',
  ])
  bind(h.sockets[1], 2)
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['已连接'])
  assert.deepEqual(
    h.requests.map((r) => r.body),
    [
      { mode: 'attach', expected_generation: 0 },
      { mode: 'attach', expected_generation: 1 },
    ],
  )
  await h.pane.close()
  const before = texts(h.root, 'connection-state')
  window.dispatchEvent(new Event('offline'))
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), before)
})

test('挂载时已经离线，联网事件本身不恢复已连接显示', async (t) => {
  const h = await mountPane(t, { online: false })
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '网络已断开，等待网络恢复',
  ])
  bind(h.sockets[0], 1)
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '网络已断开，等待网络恢复',
  ])
  window.dispatchEvent(new Event('online'))
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '网络已恢复，等待连接确认',
  ])
  h.sockets[0].onmessage({ data: Uint8Array.of(65).buffer })
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['已连接'])
})

function deferredValue() {
  let resolve
  let reject
  const promise = new Promise((ok, fail) => {
    resolve = ok
    reject = fail
  })
  return { promise, resolve, reject }
}
function takeoverPosts(h) {
  return h.requests.filter(
    (item) =>
      item.method === 'POST' &&
      item.path.endsWith('/takeover') &&
      item.body?.mode === 'takeover',
  )
}
function modalText(h) {
  return texts(h.root, 'modal-stub').join('\n')
}

class HeldWriteTerminal extends TerminalStub {
  constructor() {
    super()
    this.pendingWrite = null
  }
  write(_bytes, callback) {
    this.pendingWrite = callback
  }
  releaseWrite() {
    const callback = this.pendingWrite
    this.pendingWrite = null
    callback?.()
  }
}

async function mountRacePane(t, clientPhase) {
  const initialCredential = deferredValue()
  const takeoverCredential = deferredValue()
  const detailResponse = deferredValue()
  const h = await mountPane(t, {
    Terminal:
      clientPhase === 'stopping' ? HeldWriteTerminal : TerminalStub,
    fetch: (record) => {
      if (record.method === 'GET' && record.path.startsWith('/api/sessions/'))
        return detailResponse.promise
      if (
        record.method === 'POST' &&
        record.path.endsWith('/takeover') &&
        record.body?.mode === 'attach' &&
        clientPhase === 'preparing'
      )
        return initialCredential.promise
      if (
        record.method === 'POST' &&
        record.path.endsWith('/takeover') &&
        record.body?.mode === 'takeover'
      )
        return takeoverCredential.promise
      return undefined
    },
  })
  const releaseCredential = async () => {
    initialCredential.resolve(
      Response.json({
        code: 0,
        message: 'ok',
        data: {
          credential: 'a'.repeat(43),
          expires_at: '2099-01-01T00:00:00Z',
        },
      }),
    )
    for (const terminal of h.terminalInstances)
      if (terminal instanceof HeldWriteTerminal) terminal.releaseWrite()
    await flush()
  }
  const releaseTakeoverCredential = async () => {
    takeoverCredential.resolve(
      Response.json({
        code: 0,
        message: 'ok',
        data: {
          credential: 'b'.repeat(43),
          expires_at: '2099-01-01T00:00:00Z',
        },
      }),
    )
    await flush()
  }
  const releaseDetail = async () => {
    detailResponse.resolve(
      Response.json({ code: 0, message: 'ok', data: detail() }),
    )
    await flush()
  }
  const release = async () => {
    await releaseCredential()
    await releaseTakeoverCredential()
    await releaseDetail()
  }
  t.after(release)

  if (clientPhase === 'preparing') {
    assert.deepEqual(texts(h.root, 'connection-state'), [
      '正在申请连接凭证',
    ])
  } else if (clientPhase === 'connecting') {
    assert.equal(h.sockets.length, 1)
    assert.deepEqual(texts(h.root, 'connection-state'), ['正在连接'])
  } else if (clientPhase === 'replaying') {
    bind(h.sockets[0], 1, 0, 2)
    await flush()
    assert.deepEqual(texts(h.root, 'connection-state'), ['正在回放'])
  } else if (clientPhase === 'retry_wait') {
    bind(h.sockets[0], 1)
    h.sockets[0].onclose({ code: 1006 })
    await flush()
    assert.deepEqual(texts(h.root, 'connection-state'), [
      '等待自动恢复 1/10',
    ])
  } else if (clientPhase === 'stopping') {
    bind(h.sockets[0], 1)
    h.sockets[0].onmessage({ data: Uint8Array.of(1).buffer })
    await flush()
    assert.equal(h.terminalInstances[0].pendingWrite !== null, true)
    void h.pane.stop()
    await flush()
    assert.deepEqual(texts(h.root, 'connection-state'), ['正在停止'])
  } else {
    throw new Error(`unknown client phase: ${clientPhase}`)
  }

  return {
    ...h,
    release,
    releaseCredential,
    releaseTakeoverCredential,
    releaseDetail,
  }
}

async function enterTakeoverPhase(h, phase) {
  if (phase === 'stopping') {
    const prepare = await button(h.root, '停止恢复并准备接管')
    assert.ok(prepare, textContent(h.root))
    click(prepare)
    await nextTick()
    assert.deepEqual(texts(h.root, 'connection-state'), [
      '正在停止原恢复任务',
    ])
    return
  }

  const prepare = await button(h.root, '停止恢复并准备接管')
  assert.ok(prepare, textContent(h.root))
  click(prepare)
  await h.releaseCredential()
  await flush()
  if (phase === 'loading') {
    assert.deepEqual(texts(h.root, 'connection-state'), [
      '正在查询 Session 状态',
    ])
    return
  }

  await h.releaseDetail()
  if (phase === 'confirming') {
    assert.match(modalText(h), /接管 Session/)
    return
  }

  click(await button(h.root, '确认接管'))
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '正在申请连接凭证',
  ])
}

function networkRequests(h) {
  return h.requests.map((item) => `${item.method} ${item.path}`)
}

test('S6-T13 候选 Pane：ready 一次，仅 GET 详情，零 POST takeover、零 WS', async (t) => {
  const h = await mountPane(t, { startMode: 'candidate' })
  assert.equal(h.events.ready.length, 1)
  assert.equal(h.events.failed.length, 0)
  assert.equal(h.requests.length, 1)
  assert.equal(h.requests[0].method, 'GET')
  assert.equal(takeoverPosts(h).length, 0)
  assert.equal(h.sockets.length, 0)
  assert.match(modalText(h), /接管 Session/)
  assert.ok(await button(h.root, '确认接管'))
})

test('S6-T14 attach Pane：ready 一次，首请求仍为 attach', async (t) => {
  const h = await mountPane(t)
  assert.equal(h.events.ready.length, 1)
  assert.deepEqual(h.requests[0].body, {
    mode: 'attach',
    expected_generation: 0,
  })
  assert.equal(h.sockets.length, 1)
})

test('S6-T15 前置校验失败：failed 一次，零资源、零请求，关闭成功', async (t) => {
  const cases = [
    {
      name: '非法 client_settings',
      options: { settings: { max_cols: 80 } },
    },
    { name: '无 identity', options: { identity: false } },
  ]
  for (const item of cases) {
    await t.test(item.name, async (t) => {
      const h = await mountPane(t, item.options)
      assert.equal(h.events.ready.length, 0)
      assert.equal(h.events.failed.length, 1)
      assert.equal(h.requests.length, 0)
      assert.equal(h.sockets.length, 0)
      assert.equal(h.terminalInstances.length, 0)
      await h.pane.close()
      assert.equal(h.events.closed, 1)
    })
  }
  await t.test('logoutPending', async (t) => {
    clearIdentity()
    logoutPending.value = true
    t.after(() => {
      logoutPending.value = false
    })
    const h = await mountPane(t, { identity: false })
    assert.equal(h.events.ready.length, 0)
    assert.equal(h.events.failed.length, 1)
    assert.equal(h.requests.length, 0)
    assert.equal(h.terminalInstances.length, 0)
    await h.pane.close()
    assert.equal(h.events.closed, 1)
  })
})

test('S6-T16 候选确认：POST 为 takeover 加详情代次，xterm 只构造一次', async (t) => {
  const h = await mountPane(t, { startMode: 'candidate' })
  click(await button(h.root, '确认接管'))
  await flush()
  assert.equal(takeoverPosts(h).length, 1)
  assert.deepEqual(takeoverPosts(h)[0].body, {
    mode: 'takeover',
    expected_generation: 5,
  })
  assert.equal(h.sockets.length, 1)
  assert.equal(h.terminalInstances.length, 1)
  bind(h.sockets[0], 6)
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['已连接'])
})

test('S6-T17 接管 BIND_CONFLICT：Pane 保留，展示原因，不再发 takeover', async (t) => {
  const h = await mountPane(t, {
    startMode: 'candidate',
    fetch: (record) =>
      record.method === 'POST' && record.path.endsWith('/takeover')
        ? Response.json(
            { code: 'BIND_CONFLICT', message: '绑定代次冲突', data: {} },
            { status: 409 },
          )
        : undefined,
  })
  click(await button(h.root, '确认接管'))
  await flush()
  assert.equal(takeoverPosts(h).length, 1)
  assert.equal(h.sockets.length, 0)
  assert.match(texts(h.root, 'notice').join(), /绑定代次冲突/)
  assert.equal(await button(h.root, '确认接管'), undefined)
  await flush()
  assert.equal(takeoverPosts(h).length, 1)
})

test('S6-T18 D2：retry_wait 时停止恢复后再 GET，取消后无 Prepare/WS', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const h = await mountPane(t)
  bind(h.sockets[0], 1)
  await flush()
  h.sockets[0].onclose({ code: 1006 })
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), ['等待自动恢复 1/10'])
  click(await button(h.root, '停止恢复并准备接管'))
  await flush()
  const gets = h.requests.filter((item) => item.method === 'GET')
  assert.equal(gets.length, 1)
  assert.equal(takeoverPosts(h).length, 0)
  assert.match(modalText(h), /接管 Session/)
  click(await button(h.root, '取消'))
  await flush()
  assert.equal(takeoverPosts(h).length, 0)
  assert.equal(h.sockets.length, 1)
})

test('S6-T19 在线或回放的 Pane 不显示接管按钮', async (t) => {
  const h = await mountPane(t)
  bind(h.sockets[0], 1)
  await flush()
  assert.equal(await button(h.root, '准备接管'), undefined)
  assert.equal(await button(h.root, '停止恢复并准备接管'), undefined)
})

test('S6-T20 loading 阶段关闭标签：GET 被 abort，dispose 成功后才 closed', async (t) => {
  const pending = deferredValue()
  const h = await mountPane(t, {
    startMode: 'candidate',
    fetch: () => pending.promise,
  })
  assert.equal(h.requests.length, 1)
  await h.pane.close()
  assert.equal(h.requests[0].signal.aborted, true)
  assert.equal(h.events.closed, 1)
  pending.resolve(
    Response.json({ code: 0, message: 'ok', data: detail() }),
  )
  await flush()
})

test('S6-T21 dispose 失败：不 closed，进入清理失败，再次关闭返回同一失败', async (t) => {
  class FailingDispose extends TerminalStub {
    constructor() {
      super()
      this.disposeThrows = new Error('dispose failed')
    }
  }
  const h = await mountPane(t, { Terminal: FailingDispose })
  await assert.rejects(h.pane.close(), /dispose failed/)
  assert.equal(h.events.closed, 0)
  assert.deepEqual(texts(h.root, 'connection-state'), ['终端清理失败'])
  await assert.rejects(h.pane.close(), /dispose failed/)
  assert.equal(h.events.closed, 0)
})

test('S6-T22-A confirming 时身份失效清空候选', async (t) => {
  const h = await mountPane(t, { startMode: 'candidate' })
  assert.match(modalText(h), /接管 Session/)
  clearIdentity()
  await flush()
  assert.equal(await button(h.root, '确认接管'), undefined)
  assert.equal(takeoverPosts(h).length, 0)
})

test('S6-T22-B acceptIdentity 替换对象不 stop', async (t) => {
  const h2 = await mountPane(t)
  bind(h2.sockets[0], 1)
  await flush()
  acceptIdentity({
    expires_at: '2099-01-01T00:00:00Z',
    client_settings: settings,
  })
  await flush()
  assert.deepEqual(texts(h2.root, 'connection-state'), ['已连接'])
  assert.equal(h2.sockets.length, 1)
})

test('S6-T23 takeover 进行中：手动恢复与刷新状态禁用，提供取消准备', async (t) => {
  const pending = deferredValue()
  const h = await mountPane(t, {
    startMode: 'candidate',
    fetch: () => pending.promise,
  })
  assert.deepEqual(texts(h.root, 'connection-state'), ['正在查询 Session 状态'])
  assert.equal((await button(h.root, '手动恢复')).props.disabled, true)
  assert.equal((await button(h.root, '刷新 Session 状态')).props.disabled, true)
  assert.ok(await button(h.root, '取消准备'))
  pending.resolve(Response.json({ code: 0, message: 'ok', data: detail() }))
  await flush()
})

test('S6-T24 closeRemote 与 takeover 互斥：关闭意图优先，零 takeover POST', async (t) => {
  const pending = deferredValue()
  const h = await mountPane(t, {
    startMode: 'candidate',
    fetch: () => pending.promise,
  })
  assert.equal((await button(h.root, '关闭 SSH 会话')).props.disabled, false)
  click(await button(h.root, '关闭 SSH 会话'))
  await flush()
  assert.ok(await button(h.root, '确认关闭'))
  pending.resolve(Response.json({ code: 0, message: 'ok', data: detail() }))
  await flush()
  assert.equal(await button(h.root, '确认接管'), undefined)
  click(await button(h.root, '确认关闭'))
  await flush()
  assert.equal(
    h.requests.some((item) => item.method === 'DELETE'),
    true,
  )
  assert.equal(takeoverPosts(h).length, 0)
  assert.ok(h.events.changed >= 1)
})

test('S6-T25 接管结束与 closeRemote 结束各 emit changed 一次', async (t) => {
  await t.test('接管结束', async (t) => {
    const h = await mountPane(t, { startMode: 'candidate' })
    click(await button(h.root, '确认接管'))
    await flush()
    bind(h.sockets[0], 6)
    await flush()
    assert.equal(h.events.changed, 1)
  })
  await t.test('closeRemote 结束', async (t) => {
    const h = await mountPane(t)
    bind(h.sockets[0], 1)
    await flush()
    click(await button(h.root, '关闭 SSH 会话'))
    await flush()
    click(await button(h.root, '确认关闭'))
    await flush()
    assert.equal(h.events.changed, 1)
  })
})

test('S6-T26 非 active 不渲染确认框，active 后渲染', async (t) => {
  const h = await mountPane(t, {
    startMode: 'candidate',
    active: false,
  })
  assert.equal(modalText(h), '')
  h.props.active = true
  await flush()
  assert.match(modalText(h), /接管 Session/)
})

test('S6-T38 接管阶段 × 抢占动作：无复活、无确认前连接、无状态卡死', async (t) => {
  const phases = ['stopping', 'loading', 'confirming', 'connecting']
  const actions = ['stop', 'close', 'identity', 'closeRemote']

  for (const takeoverPhase of phases) {
    for (const action of actions) {
      // confirming 使用 showModal 独占界面，用户无法同时打开关闭 SSH 确认框。
      if (takeoverPhase === 'confirming' && action === 'closeRemote')
        continue
      await t.test(`${takeoverPhase} × ${action}`, async (t) => {
        const clientPhase =
          takeoverPhase === 'stopping'
            ? 'stopping'
            : takeoverPhase === 'loading'
              ? 'preparing'
              : 'connecting'
        const h = await mountRacePane(t, clientPhase)
        await enterTakeoverPhase(h, takeoverPhase)
        const before = networkRequests(h)
        const allowedTakeoverPosts = takeoverPhase === 'connecting' ? 1 : 0

        if (action === 'stop') {
          const stopping = h.pane.stop()
          await h.release()
          await stopping
          assert.equal(
            takeoverPosts(h).length,
            allowedTakeoverPosts,
          )
        } else if (action === 'close') {
          const closing = h.pane.close()
          await h.release()
          await closing
          assert.equal(h.events.closed, 1)
          assert.equal(
            takeoverPosts(h).length,
            allowedTakeoverPosts,
          )
        } else if (action === 'identity') {
          clearIdentity()
          await h.release()
          await flush()
          assert.equal(await button(h.root, '确认接管'), undefined)
          assert.equal(takeoverPosts(h).length, allowedTakeoverPosts)
        } else {
          click(await button(h.root, '关闭 SSH 会话'))
          await flush()
          click(await button(h.root, '确认关闭'))
          await h.release()
          await flush()
          assert.equal(
            h.requests.filter((item) => item.method === 'DELETE').length,
            1,
          )
          assert.equal(
            takeoverPosts(h).length,
            allowedTakeoverPosts,
          )
        }

        const after = networkRequests(h)
        const newTakeoverPosts = after
          .slice(before.length)
          .filter(
            (item) =>
              item === `POST /api/sessions/${session.id}/takeover`,
          )
        assert.equal(newTakeoverPosts.length, 0)
        assert.equal(h.sockets.length <= 1, true)
        assert.equal(await button(h.root, '确认接管'), undefined)
      })
    }
  }
})

test('S6-T38 客户端阶段 × stop/close：停止屏障和资源所有权一致', async (t) => {
  for (const clientPhase of [
    'preparing',
    'connecting',
    'replaying',
    'retry_wait',
    'stopping',
  ]) {
    await t.test(`${clientPhase} × stop`, async (t) => {
      const h = await mountRacePane(t, clientPhase)
      if (clientPhase === 'replaying') {
        // 回放中按设计不显示接管入口，只能验证直接 stop 屏障。
        assert.equal(await button(h.root, '停止恢复并准备接管'), undefined)
        const stopping = h.pane.stop()
        await h.release()
        await stopping
      } else {
        await enterTakeoverPhase(h, 'stopping')
        const stopping = h.pane.stop()
        await h.release()
        await stopping
      }
      assert.equal(takeoverPosts(h).length, 0)
      assert.equal(await button(h.root, '确认接管'), undefined)
      assert.deepEqual(texts(h.root, 'connection-state'), ['未连接'])
    })

    await t.test(`${clientPhase} × close`, async (t) => {
      const h = await mountRacePane(t, clientPhase)
      const closing = h.pane.close()
      await h.release()
      await closing
      assert.equal(h.events.closed, 1)
      assert.equal(takeoverPosts(h).length, 0)
    })
  }
})

test('S6-T38 connecting 的客户端子阶段 × 抢占动作：迟到回调被隔离', async (t) => {
  for (const clientPhase of ['preparing', 'connecting', 'replaying']) {
    for (const action of ['stop', 'close', 'identity', 'closeRemote']) {
      await t.test(`${clientPhase} × ${action}`, async (t) => {
        const h = await mountRacePane(t, clientPhase)
        if (clientPhase === 'replaying') {
          // 回放态不显示 takeover 入口，由上面的直接 stop/close 用例覆盖。
          assert.equal(await button(h.root, '停止恢复并准备接管'), undefined)
          return
        }
        await enterTakeoverPhase(h, 'connecting')
        const before = takeoverPosts(h).length

        if (action === 'stop') {
          const stopping = h.pane.stop()
          await h.release()
          await stopping
        } else if (action === 'close') {
          const closing = h.pane.close()
          await h.release()
          await closing
          assert.equal(h.events.closed, 1)
        } else if (action === 'identity') {
          clearIdentity()
          await h.release()
          await flush()
        } else {
          click(await button(h.root, '关闭 SSH 会话'))
          await flush()
          click(await button(h.root, '确认关闭'))
          await h.release()
          await flush()
        }
        assert.equal(takeoverPosts(h).length, before)
        assert.equal(await button(h.root, '确认接管'), undefined)
      })
    }
  }
})

test('S6-T46 终端工厂各步骤失败均回滚已创建资源', async (t) => {
  t.mock.method(console, 'error', () => {})
  const cases = [
    ['loadAddon', { loadAddonThrows: new Error('load failed') }, 0],
    ['open', { openThrows: new Error('open failed') }, 0],
    ['onData', { onDataThrows: new Error('data failed') }, 1],
    ['onBinary', { onBinaryThrows: new Error('binary failed') }, 2],
    ['onResize', { onResizeThrows: new Error('resize failed') }, 3],
  ]
  for (const [name, patch, subscriptions] of cases) {
    await t.test(name, async (t) => {
      class FailingTerminal extends TerminalStub {
        constructor() {
          super()
          Object.assign(this, patch)
        }
      }
      const h = await mountPane(t, { Terminal: FailingTerminal })
      assert.equal(h.events.failed.length, 1)
      assert.equal(h.events.ready.length, 0)
      assert.equal(h.requests.length, 0)
      assert.equal(h.sockets.length, 0)
      assert.equal(h.terminalInstances.length, 1)
      const term = h.terminalInstances[0]
      assert.equal(term.disposed, 1)
      assert.equal(term.subscriptions.length, subscriptions)
      assert.equal(
        term.subscriptions.every((item) => item.disposed === 1),
        true,
      )
      await h.pane.close()
      assert.equal(h.events.closed, 1)
    })
  }
  await t.test('回滚本身失败', async (t) => {
    class FailingTerminal extends TerminalStub {
      constructor() {
        super()
        this.openThrows = new Error('open failed')
        this.disposeThrows = new Error('dispose failed')
      }
    }
    const h = await mountPane(t, { Terminal: FailingTerminal })
    assert.equal(h.events.failed.length, 1)
    assert.match(texts(h.root, 'notice').join(), /open failed/)
    await assert.rejects(h.pane.close(), /dispose failed/)
    assert.equal(h.events.closed, 0)
    assert.deepEqual(texts(h.root, 'connection-state'), ['终端清理失败'])
    await assert.rejects(h.pane.close(), /dispose failed/)
  })
})

test('S6-T46-A Terminal 构造失败：无实例可释放，零资源、零请求，close 成功', async (t) => {
  t.mock.method(console, 'error', () => {})
  class ThrowingTerminal {
    constructor() {
      throw new Error('terminal ctor failed')
    }
  }
  class CountingFitAddon {
    static count = 0
    constructor() {
      CountingFitAddon.count++
    }
    proposeDimensions() {
      return { cols: 80, rows: 24 }
    }
  }
  CountingFitAddon.count = 0
  const h = await mountPane(t, {
    Terminal: ThrowingTerminal,
    FitAddon: CountingFitAddon,
  })
  assert.equal(CountingFitAddon.count, 0)
  assert.equal(h.events.failed.length, 1)
  assert.equal(h.events.ready.length, 0)
  assert.equal(h.requests.length, 0)
  assert.equal(h.sockets.length, 0)
  assert.match(texts(h.root, 'notice').join(), /terminal ctor failed/)
  await h.pane.close()
  assert.equal(h.events.closed, 1)
})

test('S6-T46-B FitAddon 构造失败：Terminal 回滚一次；回滚失败则 close 失败', async (t) => {
  t.mock.method(console, 'error', () => {})
  await t.test('B1 回滚成功', async (t) => {
    class ThrowingFitAddon {
      constructor() {
        throw new Error('fit ctor failed')
      }
    }
    const h = await mountPane(t, { FitAddon: ThrowingFitAddon })
    assert.equal(h.events.failed.length, 1)
    assert.match(texts(h.root, 'notice').join(), /fit ctor failed/)
    const term = h.terminalInstances[0]
    assert.equal(term.disposed, 1)
    assert.equal(term.loaded, 0)
    assert.equal(term.opened, 0)
    assert.equal(term.subscriptions.length, 0)
    await h.pane.close()
    assert.equal(h.events.closed, 1)
  })
  await t.test('B2 回滚失败', async (t) => {
    class ThrowingFitAddon {
      constructor() {
        throw new Error('fit ctor failed')
      }
    }
    class FailingDispose extends TerminalStub {
      constructor() {
        super()
        this.disposeThrows = new Error('dispose failed')
      }
    }
    const h = await mountPane(t, {
      FitAddon: ThrowingFitAddon,
      Terminal: FailingDispose,
    })
    assert.match(texts(h.root, 'notice').join(), /fit ctor failed/)
    await assert.rejects(h.pane.close(), /dispose failed/)
    assert.equal(h.events.closed, 0)
    assert.deepEqual(texts(h.root, 'connection-state'), ['终端清理失败'])
    await assert.rejects(h.pane.close(), /dispose failed/)
  })
})

test('S6-T47 Observer 创建或 observe 失败：回滚 Client、移除监听', async (t) => {
  t.mock.method(console, 'error', () => {})
  const cases = [
    [
      'ResizeObserver 构造',
      {
        ResizeObserver: class {
          constructor() {
            throw new Error('resize ctor failed')
          }
        },
      },
      'resize ctor failed',
    ],
    [
      'ResizeObserver observe',
      {
        ResizeObserver: class {
          constructor() {
            this.disconnected = 0
          }
          observe() {
            throw new Error('resize observe failed')
          }
          disconnect() {
            this.disconnected++
          }
        },
      },
      'resize observe failed',
    ],
    [
      'MutationObserver 构造',
      {
        MutationObserver: class {
          constructor() {
            throw new Error('theme ctor failed')
          }
        },
      },
      'theme ctor failed',
    ],
    [
      'MutationObserver observe',
      {
        MutationObserver: class {
          constructor() {
            this.disconnected = 0
          }
          observe() {
            throw new Error('theme observe failed')
          }
          disconnect() {
            this.disconnected++
          }
        },
      },
      'theme observe failed',
    ],
  ]
  for (const [name, options, text] of cases) {
    await t.test(name, async (t) => {
      const h = await mountPane(t, options)
      assert.equal(h.events.failed.length, 1)
      assert.match(texts(h.root, 'notice').join(), new RegExp(text))
      assert.equal(h.requests.length, 0)
      assert.equal(h.sockets.length, 0)
      assert.equal(h.terminalInstances[0].disposed, 1)
      assert.equal(h.window.removed.includes('offline'), true)
      assert.equal(h.window.removed.includes('online'), true)
      await h.pane.close()
      assert.equal(h.events.closed, 1)
    })
  }
  await t.test('回滚 dispose 失败', async (t) => {
    class FailingDispose extends TerminalStub {
      constructor() {
        super()
        this.disposeThrows = new Error('dispose failed')
      }
    }
    class BadResize {
      constructor() {
        this.disconnected = 0
      }
      observe() {
        throw new Error('resize observe failed')
      }
      disconnect() {
        this.disconnected++
      }
    }
    const h = await mountPane(t, {
      Terminal: FailingDispose,
      ResizeObserver: BadResize,
    })
    await assert.rejects(h.pane.close(), /dispose failed/)
    assert.equal(h.events.closed, 0)
    assert.deepEqual(texts(h.root, 'connection-state'), ['终端清理失败'])
  })
})

test('S7-C01 关闭标签不发 DELETE、不置 ended，只释放本地资源', async (t) => {
  const h = await mountPane(t)
  bind(h.sockets[0], 1)
  await flush()
  await h.pane.close()
  assert.equal(h.requests.filter((item) => item.method === 'DELETE').length, 0)
  assert.equal(h.closeHandle.snapshot().state, 'idle')
  assert.equal(h.events.closed, 1)
})

test('S7-4/C14 watcher 与 handle.stop 取得同一个 client.stopping', async (t) => {
  const seen = []
  const originalStop = TerminalClient.prototype.stop
  t.mock.method(TerminalClient.prototype, 'stop', function (...args) {
    const result = originalStop.apply(this, args)
    seen.push(result)
    return result
  })
  t.after(() => {
    logoutPending.value = false
  })
  const h = await mountPane(t)
  bind(h.sockets[0], 1)
  await flush()

  logoutPending.value = true
  const fromHandle = h.pane.stop()
  assert.equal(seen.length, 2)
  assert.equal(seen[0], seen[1])
  assert.equal(seen[1], fromHandle)
  await fromHandle
})

test('S7-C23 确认关闭：同步 pending，localStop 先于 DELETE', async (t) => {
  const order = []
  const h = await mountPane(t, {
    fetch: (record) => {
      if (record.method === 'DELETE') order.push('delete')
    },
  })
  bind(h.sockets[0], 1)
  await flush()
  const socket = h.sockets[0]
  const originalClose = socket.close.bind(socket)
  socket.close = (code) => {
    order.push('stop')
    originalClose(code)
  }
  click(await button(h.root, '关闭 SSH 会话'))
  await flush()
  click(await button(h.root, '确认关闭'))
  // 同步调用栈内：先 stop（takeover/recovery/client 失效），再发 DELETE。
  assert.deepEqual(order, ['stop', 'delete'])
  assert.equal(h.closeHandle.snapshot().state, 'pending')
  await flush()
  assert.equal(h.closeHandle.snapshot().state, 'requested')
})

test('S7-C01/C15 Pane 销毁不 abort 协调器 DELETE，状态保留', async (t) => {
  const pending = deferredValue()
  const h = await mountPane(t, {
    fetch: (record) =>
      record.method === 'DELETE' ? pending.promise : undefined,
  })
  bind(h.sockets[0], 1)
  await flush()
  click(await button(h.root, '关闭 SSH 会话'))
  await flush()
  click(await button(h.root, '确认关闭'))
  await flush()
  const deleteRequest = h.requests.find((item) => item.method === 'DELETE')
  assert.equal(h.closeHandle.snapshot().state, 'pending')
  const closing = h.pane.close()
  assert.equal(deleteRequest.signal.aborted, false)
  assert.equal(h.closeHandle.snapshot().state, 'pending')

  pending.resolve(Response.json({ code: 0, message: 'ok', data: {} }))
  await flush()
  await closing
  assert.equal(h.closeHandle.snapshot().state, 'requested')
  assert.equal(h.events.closed, 1)
})

test('S7-C01/C15 Pane close 只退订并注销本地 stop，不清协调器任务', async (t) => {
  const coordinator = new CloseCoordinator()
  const base = coordinator.handle(session.id)
  let unsubscribed = 0
  let unregistered = 0
  const closeHandle = {
    ...base,
    subscribe(listener) {
      const unsubscribe = base.subscribe(listener)
      return () => {
        unsubscribed++
        unsubscribe()
      }
    },
    registerLocalStop(stop) {
      const unregister = base.registerLocalStop(stop)
      return () => {
        unregistered++
        unregister()
      }
    },
  }
  const pending = deferredValue()
  const h = await mountPane(t, {
    closeCoordinator: coordinator,
    closeHandle,
    fetch: (record) =>
      record.method === 'DELETE' ? pending.promise : undefined,
  })
  bind(h.sockets[0], 1)
  await flush()
  click(await button(h.root, '关闭 SSH 会话'))
  await flush()
  click(await button(h.root, '确认关闭'))
  await flush()
  const deletion = h.requests.find((item) => item.method === 'DELETE')
  assert.equal(closeHandle.snapshot().state, 'pending')

  await h.pane.close()
  assert.equal(unsubscribed, 1)
  assert.equal(unregistered, 1)
  assert.equal(deletion.signal.aborted, false)
  assert.equal(closeHandle.snapshot().state, 'pending')
  assert.equal(h.events.closed, 1)

  pending.resolve(Response.json({ code: 0, message: 'ok', data: {} }))
  await flush()
  assert.equal(closeHandle.snapshot().state, 'requested')
})

test('S7-C02 pending 取消等待后关闭 Pane，unknown 保留且迟到 200 不覆盖', async (t) => {
  const pending = deferredValue()
  const h = await mountPane(t, {
    fetch: (record) =>
      record.method === 'DELETE' ? pending.promise : undefined,
  })
  bind(h.sockets[0], 1)
  await flush()
  click(await button(h.root, '关闭 SSH 会话'))
  await flush()
  click(await button(h.root, '确认关闭'))
  await flush()
  click(await button(h.root, '取消等待'))
  await flush()
  assert.equal(h.closeHandle.snapshot().state, 'unknown')
  assert.match(texts(h.root, 'close-state').join(), /已停止等待/)

  await h.pane.close()
  assert.equal(h.closeHandle.snapshot().state, 'unknown')
  pending.resolve(Response.json({ code: 0, message: 'ok', data: {} }))
  await flush()
  assert.equal(h.closeHandle.snapshot().state, 'unknown')
})

test('S7-C03/C04 非 idle 新 Pane：零 Prepare/WS，禁止手动恢复', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(session.id)
  const original = globalThis.fetch
  try {
    globalThis.fetch = async () => {
      throw new TypeError('network down')
    }
    await handle.requestClose()
  } finally {
    globalThis.fetch = original
  }
  assert.equal(handle.snapshot().state, 'unknown')

  const h = await mountPane(t, {
    closeCoordinator: coordinator,
    startMode: 'candidate',
  })
  assert.equal(h.requests.length, 0)
  assert.equal(h.sockets.length, 0)
  assert.equal(takeoverPosts(h).length, 0)
  assert.equal(await button(h.root, '确认接管'), undefined)
  assert.equal((await button(h.root, '手动恢复')).props.disabled, true)
  assert.match(texts(h.root, 'close-state').join(), /关闭结果未知/)
  assert.ok(await button(h.root, '重试关闭'))
  assert.ok(await button(h.root, '刷新状态'))
})

test('S7-C29/C12 idle + CLOSING：保持 idle，零凭证/WS/takeover，不产生 ended', async (t) => {
  const h = await mountPane(t, {
    startMode: 'candidate',
    detail: detail({
      status: 'CLOSING',
      disconnected_at: null,
      offline_expires_at: null,
      remaining_seconds: null,
    }),
  })
  assert.equal(h.closeHandle.snapshot().state, 'idle')
  assert.equal(h.requests.length, 1)
  assert.equal(h.requests[0].method, 'GET')
  assert.equal(h.requests[0].path, `/api/sessions/${session.id}`)
  assert.equal(h.sockets.length, 0)
  assert.equal(takeoverPosts(h).length, 0)
  assert.equal(h.requests.filter((item) => item.method === 'POST').length, 0)
  assert.equal(await button(h.root, '确认接管'), undefined)
  assert.equal(texts(h.root, 'close-state').length, 0)
  assert.notEqual(h.closeHandle.snapshot().state, 'ended')
})

test('S7-C13 页面重新进入详情网络错误：保留候选 Pane，不自动恢复', async (t) => {
  const h = await mountPane(t, {
    startMode: 'candidate',
    fetch: (record) => {
      if (record.method === 'GET')
        return Promise.reject(new TypeError('network down'))
    },
  })
  assert.equal(h.events.ready.length, 1)
  assert.ok(h.pane)
  assert.equal(h.requests.length, 1)
  assert.equal(h.requests[0].method, 'GET')
  assert.equal(h.requests[0].path, `/api/sessions/${session.id}`)
  assert.equal(takeoverPosts(h).length, 0)
  assert.equal(h.sockets.length, 0)
  assert.equal(await button(h.root, '确认接管'), undefined)
  assert.match(texts(h.root, 'notice').join(), /无法连接服务/)
  assert.equal(h.closeHandle.snapshot().state, 'idle')

  await flush()
  assert.equal(h.requests.length, 1)
  assert.equal(h.sockets.length, 0)
})

test('S7-C29 idle + CONNECTING：保持 idle，拒绝确认', async (t) => {
  const h = await mountPane(t, {
    startMode: 'candidate',
    detail: detail({ status: 'CONNECTING' }),
  })
  assert.equal(h.closeHandle.snapshot().state, 'idle')
  assert.equal(h.sockets.length, 0)
  assert.equal(takeoverPosts(h).length, 0)
  assert.equal(await button(h.root, '确认接管'), undefined)
})

test('S7-C21/C28 ended 只认服务端证据', async (t) => {
  await t.test('本地 stop 不置 ended', async (t) => {
    const h = await mountPane(t)
    bind(h.sockets[0], 1)
    await flush()
    await h.pane.stop()
    assert.equal(h.closeHandle.snapshot().state, 'idle')
  })
  await t.test('本地关闭标签不置 ended', async (t) => {
    const h = await mountPane(t)
    bind(h.sockets[0], 1)
    await flush()
    await h.pane.close()
    assert.equal(h.closeHandle.snapshot().state, 'idle')
  })
  await t.test('WS 1006 不置 ended', async (t) => {
    t.mock.timers.enable({ apis: ['setTimeout'] })
    const h = await mountPane(t)
    bind(h.sockets[0], 1)
    await flush()
    h.sockets[0].onclose({ code: 1006 })
    await flush()
    assert.equal(h.closeHandle.snapshot().state, 'idle')
  })
  await t.test('taken_over 不置 ended', async (t) => {
    const h = await mountPane(t)
    bind(h.sockets[0], 1)
    await flush()
    h.sockets[0].message({ type: 'taken_over', message: '被接管' })
    await flush()
    assert.equal(h.closeHandle.snapshot().state, 'idle')
  })
  await t.test('AUTH_REQUIRED 不置 ended', async (t) => {
    const h = await mountPane(t)
    bind(h.sockets[0], 1)
    await flush()
    h.sockets[0].message({
      type: 'error',
      code: 'AUTH_REQUIRED',
      message: '需要登录',
    })
    await flush()
    assert.equal(h.closeHandle.snapshot().state, 'idle')
  })
  await t.test('服务端 closed 置 ended', async (t) => {
    const h = await mountPane(t)
    bind(h.sockets[0], 1)
    await flush()
    h.sockets[0].message({
      type: 'closed',
      reason: 'user_closed',
      message: '会话已关闭',
    })
    await flush()
    assert.equal(h.closeHandle.snapshot().state, 'ended')
    assert.match(texts(h.root, 'close-state').join(), /已结束|不可接管/)
    assert.equal(
      texts(h.root, 'close-state').join().includes('资源已回收'),
      false,
    )
  })
})

test('S7-C30 Pane DELETE 失败归 unknown，不显示 failed', async (t) => {
  const h = await mountPane(t, {
    fetch: (record) =>
      record.method === 'DELETE'
        ? Response.json(
            { code: 'PERMISSION_DENIED', message: '拒绝', data: {} },
            { status: 403 },
          )
        : undefined,
  })
  bind(h.sockets[0], 1)
  await flush()
  click(await button(h.root, '关闭 SSH 会话'))
  await flush()
  click(await button(h.root, '确认关闭'))
  await flush()
  assert.equal(h.closeHandle.snapshot().state, 'unknown')
  assert.match(texts(h.root, 'close-state').join(), /关闭结果未知/)
  assert.equal(
    texts(h.root, 'close-state').join().includes('未被服务端接受'),
    false,
  )
  assert.equal(h.events.closed, 0)
  assert.ok(await button(h.root, '重试关闭'))
})

test('S7-C19 recovery retry_wait 时确认关闭：停止重试，不自动 attach', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const h = await mountRacePane(t, 'retry_wait')
  const before = h.requests.length
  click(await button(h.root, '关闭 SSH 会话'))
  await flush()
  click(await button(h.root, '确认关闭'))
  await h.release()
  await flush()
  assert.equal(
    h.requests.filter((item) => item.method === 'DELETE').length,
    1,
  )
  assert.equal(
    h.requests
      .slice(before)
      .filter(
        (item) =>
          item.method === 'POST' && item.path.endsWith('/takeover'),
      ).length,
    0,
  )
  assert.equal(h.sockets.length, 1)
  assert.equal(h.closeHandle.snapshot().state, 'requested')
})

test('S7-5 takeover loading 时卸载：零新 WebSocket，迟到详情不发布', async (t) => {
  const detailResponse = deferredValue()
  const h = await mountPane(t, {
    startMode: 'candidate',
    fetch: (record) =>
      record.method === 'GET' ? detailResponse.promise : undefined,
  })
  assert.deepEqual(texts(h.root, 'connection-state'), [
    '正在查询 Session 状态',
  ])
  const before = h.requests.length

  h.unmount()
  await h.pane.close()
  assert.equal(h.requests.length, before)
  assert.equal(h.sockets.length, 0)
  detailResponse.resolve(
    Response.json({ code: 0, message: 'ok', data: detail() }),
  )
  await flush()
  assert.equal(h.requests.length, before)
  assert.equal(h.sockets.length, 0)
  assert.equal(takeoverPosts(h).length, 0)
  // 组件已卸载，close 完成后的 closed 事件不能再送达已销毁的父级。
  assert.equal(h.events.closed, 0)
  assert.equal(h.closeHandle.snapshot().state, 'idle')
})

test('S7-5 takeover loading 阶段卸载：真实 prepare 流程的迟到请求隔离', async (t) => {
  const h = await mountRacePane(t, 'connecting')
  await enterTakeoverPhase(h, 'loading')
  assert.deepEqual(texts(h.root, 'connection-state'), ['正在查询 Session 状态'])
  assert.ok(await button(h.root, '取消准备'))
  assert.equal(await button(h.root, '确认接管'), undefined)

  const beforeRequests = h.requests.length
  const beforeSockets = h.sockets.length
  const beforeTakeoverPosts = takeoverPosts(h).length
  const beforeChanged = h.events.changed

  h.unmount()
  await h.pane.close()
  await h.releaseDetail()
  await flush()

  assert.equal(h.requests.length, beforeRequests)
  assert.equal(h.sockets.length, beforeSockets)
  assert.equal(takeoverPosts(h).length, beforeTakeoverPosts)
  assert.equal(h.events.changed, beforeChanged)
  // 卸载后才完成的 close 不能把 closed 事件送达已销毁的父级。
  assert.equal(h.events.closed, 0)
  assert.equal(h.closeHandle.snapshot().state, 'idle')
})

test('S7-5 recovery retry_wait 时卸载：不自动恢复', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const h = await mountRacePane(t, 'retry_wait')
  const before = h.requests.length

  h.unmount()
  await h.pane.close()
  await flush()
  t.mock.timers.tick(5000)
  await flush()
  assert.equal(h.requests.length, before)
  assert.equal(h.sockets.length, 1)
  assert.equal(takeoverPosts(h).length, 0)
  assert.equal(h.closeHandle.snapshot().state, 'idle')
})

test('S7-6/C15 stop、重复 close 与卸载共享清理，旧 socket/write 回调不复活', async (t) => {
  const h = await mountPane(t, { Terminal: HeldWriteTerminal })
  const socket = h.sockets[0]
  bind(socket, 1)
  socket.onmessage({ data: Uint8Array.of(65).buffer })
  await flush()
  const terminal = h.terminalInstances[0]
  assert.equal(typeof terminal.pendingWrite, 'function')
  const old = { message: socket.onmessage, close: socket.onclose, open: socket.onopen }
  const closed = t.mock.method(socket, 'close')
  const stopping = h.pane.stop()
  const closing = h.pane.close()
  assert.equal(h.pane.close(), closing)
  const events = { ...h.events }
  const requests = h.requests.length
  h.unmount()
  terminal.releaseWrite()
  await Promise.all([stopping, closing])
  old.open()
  old.close({ code: 1006 })
  old.message({ data: JSON.stringify({ type: 'closed', message: 'late closed' }) })
  await flush()
  assert.equal(closed.mock.callCount(), 1)
  assert.equal(terminal.disposed, 1)
  assert.ok(terminal.subscriptions.every((s) => s.disposed === 1))
  assert.ok(h.observers.every((o) => o.disconnected >= 1))
  assert.equal(h.requests.length, requests)
  assert.equal(h.sockets.length, 1)
  assert.equal(h.events.changed, events.changed)
  // Vue suppresses events after unmount; resource cleanup still completes once.
  assert.equal(h.events.closed, 0)
  assert.equal(h.closeHandle.snapshot().state, 'idle')
  assert.equal(h.requests.filter((r) => r.method === 'DELETE').length, 0)
})

test('S7-6/C06 DELETE 完成与 Pane close 同步交错，不向已退订 Pane 发布', async (t) => {
  for (const outcome of ['accepted', 'lost']) {
    await t.test(outcome, async (t) => {
      const pending = deferredValue()
      const h = await mountPane(t, {
        fetch: (r) => (r.method === 'DELETE' ? pending.promise : undefined),
      })
      bind(h.sockets[0], 1)
      click(await button(h.root, '关闭 SSH 会话'))
      await flush()
      click(await button(h.root, '确认关闭'))
      assert.equal(h.closeHandle.snapshot().state, 'pending')
      const signal = h.requests.find((r) => r.method === 'DELETE').signal
      const closing = h.pane.close()
      const changed = h.events.changed
      if (outcome === 'accepted') pending.resolve(Response.json({ code: 0, message: 'ok', data: {} }))
      else pending.reject(new TypeError('lost response'))
      await closing
      await flush()
      assert.equal(signal.aborted, false)
      assert.equal(h.closeHandle.snapshot().state, outcome === 'accepted' ? 'requested' : 'unknown')
      assert.equal(h.events.changed, changed)
      assert.equal(h.events.closed, 1)
      assert.equal(h.requests.filter((r) => r.method === 'DELETE').length, 1)
      assert.equal(h.sockets.length, 1)
      assert.equal(h.terminalInstances[0].disposed, 1)
    })
  }
})

test('S7-6/C29 idle 候选详情准入：有效状态需确认，NOT_FOUND/非法详情拒绝且零凭证', async (t) => {
  for (const status of ['CONNECTED', 'DISCONNECTED', 'NOT_FOUND', 'INVALID']) {
    await t.test(status, async (t) => {
      const h = await mountPane(t, {
        startMode: 'candidate',
        fetch: (r) => {
          if (r.method !== 'GET') return undefined
          if (status === 'NOT_FOUND')
            return Response.json({ code: 'SESSION_NOT_FOUND', message: '不存在', data: {} }, { status: 404 })
          if (status === 'INVALID')
            return Response.json({ code: 0, message: 'ok', data: { status: 'SURPRISE' } })
          return Response.json({ code: 0, message: 'ok', data: detail({
            status,
            disconnected_at: status === 'DISCONNECTED' ? session.created_at : null,
            offline_expires_at: status === 'DISCONNECTED' ? '2099-01-01T00:00:00Z' : null,
            remaining_seconds: status === 'DISCONNECTED' ? 300 : null,
          }) })
        },
      })
      assert.equal(h.events.ready.length, 1)
      assert.equal(h.requests.length, 1)
      assert.equal(h.requests[0].method, 'GET')
      assert.equal(h.sockets.length, 0)
      assert.equal(takeoverPosts(h).length, 0)
      const confirm = await button(h.root, '确认接管')
      if (['CONNECTED', 'DISCONNECTED'].includes(status)) {
        assert.ok(confirm)
        assert.equal(confirm.props.disabled, false)
      } else assert.equal(confirm, undefined)
      assert.equal(h.closeHandle.snapshot().state, status === 'NOT_FOUND' ? 'ended' : 'idle')
      assert.doesNotMatch(textContent(h.root), /资源已回收完成/)
    })
  }
})

test('S7-6/C23 takeover、recovery、client 同步失效全部先于 DELETE', async (t) => {
  const order = []
  for (const [prototype, method, label] of [
    [SessionTakeover.prototype, 'invalidate', 'takeover.invalidate'],
    [SessionRecovery.prototype, 'stop', 'recovery.stop'],
    [TerminalClient.prototype, 'stop', 'client.stop'],
  ]) {
    const original = prototype[method]
    t.mock.method(prototype, method, function (...args) {
      order.push(label)
      return original.apply(this, args)
    })
  }
  const h = await mountPane(t, {
    fetch: (r) => {
      if (r.method === 'DELETE') order.push('DELETE')
    },
  })
  bind(h.sockets[0], 1)
  click(await button(h.root, '关闭 SSH 会话'))
  await flush()
  order.length = 0
  click(await button(h.root, '确认关闭'))
  assert.deepEqual(order, [
    'takeover.invalidate',
    'recovery.stop',
    'client.stop',
    'DELETE',
  ])
  assert.equal(h.closeHandle.snapshot().state, 'pending')
  await flush()
  assert.equal(h.closeHandle.snapshot().state, 'requested')
})

test('S7-6/C26 真实旧 Pane closed 后才能注册新 Pane，旧注销不移除新 stop', async (t) => {
  const coordinator = new CloseCoordinator()
  const unregisters = [], stopCalls = []
  const original = coordinator.registerLocalStop
  t.mock.method(coordinator, 'registerLocalStop', function (id, stop) {
    const index = stopCalls.push(0) - 1
    const unregister = original.call(this, id, () => {
      stopCalls[index]++
      return stop()
    })
    unregisters.push(unregister)
    return unregister
  })
  const a = await mountPane(t, { closeCoordinator: coordinator, startMode: 'candidate' })
  assert.equal(a.events.closed, 0)
  await a.pane.close()
  assert.equal(a.events.closed, 1)
  assert.equal(a.terminalInstances[0].disposed, 1)
  a.unmount()
  const b = await mountPane(t, { closeCoordinator: coordinator, startMode: 'candidate' })
  assert.equal(b.events.ready.length, 1)
  assert.equal(unregisters.length, 2)
  unregisters[0]() // 重复旧注销仍必须为空操作，不得清掉 B 的新注册。
  await b.closeHandle.requestClose()
  await flush()
  assert.deepEqual(stopCalls, [0, 1])
  assert.equal(b.requests.filter((r) => r.method === 'DELETE').length, 1)
  assert.equal(b.sockets.length, 0)
  assert.equal(b.closeHandle.snapshot().state, 'requested')
  // Two mounts nest browser-global mocks; finish B before A restores its globals.
  await b.pane.close()
  b.unmount()
})

test('UI 菜单实时身份失效：旧 props 下动作拒绝，切换不重建终端，卸载无 Teleport 残留', async (t) => {
  let current = true
  const h = await mountPane(t, { isCurrentPane: () => current })
  const oldClose = await button(h.root, '关闭 SSH 会话')
  const term = h.terminalInstances[0]
  const host = findAll(h.root, (el) => el.props.class === 'terminal-host')[0]
  current = false // 页面状态同步变化，Pane 的 active props 尚未刷新。
  click(oldClose)
  await flush()
  assert.equal(await button(h.root, '确认关闭'), undefined)
  assert.equal(h.requests.filter((r) => r.method === 'DELETE').length, 0)
  h.props.active = false
  await flush()
  assert.equal(
    findAll(h.root, (el) => el.props.class === 'terminal-menu').length,
    0,
  )
  assert.equal(h.props.menuOpen, false)
  current = true
  h.props.active = true
  await flush()
  assert.equal(h.props.menuOpen, false)
  assert.equal(h.terminalInstances.length, 1)
  assert.equal(h.terminalInstances[0], term)
  assert.equal(
    findAll(h.root, (el) => el.props.class === 'terminal-host')[0],
    host,
  )
  h.unmount()
  await flush()
  for (const target of teleportTargets.values())
    assert.equal(byType(target, 'button').length, 0)
  assert.deepEqual(
    h.window.added.slice().sort(),
    h.window.removed.slice().sort(),
  )
})

test('UI 复制不依赖输入门禁；失活后迟到反馈丢弃；无选区与重复键零复制', async (t) => {
  const h = await mountPane(t)
  let resolveCopy
  const copied = []
  navigator.clipboard = {
    writeText(text) {
      copied.push(text)
      return new Promise((resolve) => {
        resolveCopy = resolve
      })
    },
  }
  const term = h.terminalInstances[0] // 未 bind，输入门禁仍关闭。
  term.selection = '中文\n\nANSI output'
  term.selectionChanged()
  await flush()
  click(await button(h.root, '复制选中文本'))
  assert.deepEqual(copied, [term.selection])
  h.props.active = false
  await flush()
  resolveCopy()
  await flush()
  assert.equal(
    texts(h.root, 'notice').some((value) => value.includes('已复制')),
    false,
  )
  h.props.active = true
  await flush()
  const event = {
    key: 'C',
    type: 'keydown',
    ctrlKey: true,
    shiftKey: true,
    repeat: false,
    preventDefault() {
      this.prevented = true
    },
    stopPropagation() {
      this.stopped = true
    },
  }
  term.selection = ''
  assert.equal(term.keyHandler(event), false)
  assert.equal(event.prevented, true)
  assert.equal(event.stopped, true)
  term.selection = 'selected'
  assert.equal(term.keyHandler({ ...event, repeat: true }), false)
  assert.equal(copied.length, 1)
  assert.equal(
    h.sockets
      .flatMap((socket) => socket.sent)
      .filter((value) => value instanceof Uint8Array).length,
    0,
  )
})

test('UI 菜单保留停止、查询、取消查询、人工恢复的业务条件和调用', async t => {
  let pending = null
  const h = await mountPane(t, { fetch: record => record.method === 'GET' && pending ? pending.promise : undefined })
  bind(h.sockets[0], 1)
  await flush()
  assert.equal((await button(h.root, '刷新 Session 状态')).props.disabled, true)
  click(await button(h.root, '停止恢复'))
  await flush()
  assert.equal(h.sockets[0].readyState, Socket.CLOSED)
  pending = deferredValue()
  click(await button(h.root, '刷新 Session 状态'))
  await flush()
  const query = h.requests.at(-1)
  assert.equal(query.method, 'GET')
  click(await button(h.root, '取消查询'))
  assert.equal(query.signal.aborted, true)
  pending.resolve(Response.json({code:0,message:'ok',data:detail()}))
  pending = null
  await flush()
  click(await button(h.root, '刷新 Session 状态'))
  await flush()
  assert.equal((await button(h.root, '手动恢复')).props.disabled, false)
  const snapshots = byType(h.root, 'details')
  assert.equal(snapshots.length, 1)
  assert.equal(snapshots[0].props.open, undefined)
  click(await button(h.root, '手动恢复'))
  await flush()
  assert.equal(h.sockets.length, 2)
  assert.equal(h.requests.at(-1).body.mode, 'attach')
})

test('UI 菜单保留重试关闭和刷新关闭状态，错误继续展示在终端附近', async t => {
  let deletes = 0
  const h = await mountPane(t, {fetch: record => {
    if(record.method === 'DELETE' && ++deletes === 1) throw new TypeError('network down')
  }})
  bind(h.sockets[0], 1)
  await flush()
  click(await button(h.root, '关闭 SSH 会话'))
  await flush()
  click(await button(h.root, '确认关闭'))
  await flush()
  assert.equal(h.closeHandle.snapshot().state, 'unknown')
  assert.match(texts(h.root, 'close-state').join(), /未知/)
  const before = h.requests.filter(r=>r.method==='GET').length
  click(await button(h.root, '刷新状态'))
  await flush()
  assert.equal(h.requests.filter(r=>r.method==='GET').length, before+1)
  click(await button(h.root, '重试关闭'))
  await flush()
  assert.equal(deletes, 2)
  assert.equal(h.closeHandle.snapshot().state, 'requested')
})

test('UI 常驻 × 在清理失败时不发 closed 或 DELETE，且不可重复关闭', async t => {
  t.mock.method(console, 'error', () => {})
  class FailingDispose extends TerminalStub {
    dispose() { throw new Error('dispose failed') }
  }
  const h = await mountPane(t, {Terminal:FailingDispose})
  const cross = byType(h.root,'button').find(el=>el.props['aria-label']==='关闭标签：test')
  click(cross)
  await flush()
  assert.equal(h.events.closed, 0)
  assert.equal(cross.props.disabled, true)
  assert.match(texts(h.root,'notice').join(), /终端清理失败.*刷新页面/)
  assert.equal(h.requests.filter(r=>r.method==='DELETE').length, 0)
})

test('UI 菜单取消准备沿用 takeover.cancel；菜单关闭标签沿用本地 close', async t => {
  const pending = deferredValue()
  const h = await mountPane(t, {startMode:'candidate',fetch:()=>pending.promise})
  const query = h.requests[0]
  click(await button(h.root, '取消准备'))
  assert.equal(query.signal.aborted, true)
  pending.resolve(Response.json({code:0,message:'ok',data:detail()}))
  await flush()
  assert.equal(await button(h.root, '确认接管'), undefined)
  assert.equal(h.sockets.length, 0)
  click(await button(h.root, '关闭标签'))
  await flush()
  assert.equal(h.events.closed, 1)
  assert.equal(h.requests.filter(r=>r.method==='DELETE').length, 0)
})
