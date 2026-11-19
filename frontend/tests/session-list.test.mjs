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
  ref,
} from 'vue'
import { ApiError } from '../src/api/client.ts'
import { listSessions, sessionList } from '../src/api/sessions.ts'
import { CloseCoordinator } from '../src/session/close.ts'
import { SessionListController } from '../src/session/list.ts'
import {
  acceptIdentity,
  authNotice,
  clearIdentity,
  logoutPending,
} from '../src/stores/auth.ts'

const sourceURL = new URL(
  '../src/components/session/SessionManager.vue',
  import.meta.url,
)
const { descriptor } = parse(await readFile(sourceURL, 'utf8'))
const compiled = compileScript(descriptor, {
  id: 'session-manager-test',
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
  .outputText.replace(/from ['"]([^'"]+)['"]/g, (_match, specifier) => {
    let url
    if (specifier.startsWith('.'))
      url = new URL(specifier, sourceURL).href
    else url = import.meta.resolve(specifier)
    return `from ${JSON.stringify(url)}`
  })
const { default: SessionManager } = await import(moduleURL(code))

const homeSourceURL = new URL('../src/pages/HomePage.vue', import.meta.url)
const homeDescriptor = parse(await readFile(homeSourceURL, 'utf8')).descriptor
const homeCompiled = compileScript(homeDescriptor, {
  id: 'home-page-session-view-test',
  inlineTemplate: true,
})
const vueModuleURL = import.meta.resolve('vue')
const homeCode = ts
  .transpileModule(homeCompiled.content, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.ESNext,
    },
  })
  .outputText.replace(/from ['"]([^'"]+)['"]/g, (_match, specifier) => {
    let url
    if (specifier === 'vue-router')
      url = moduleURL(
        `export function useRouter() {
          return {
            async replace(path) {
              globalThis.__routerReplaceHook?.(path)
              globalThis.__routerReplaces?.push(path)
            },
          }
        }`,
      )
    else if (specifier.endsWith('TerminalPane.vue'))
      url = moduleURL(`
        import { defineComponent, h, onBeforeUnmount, onMounted } from ${JSON.stringify(vueModuleURL)}
        export default defineComponent({
          name: 'TerminalPaneStub',
          props: ['active', 'session', 'startMode', 'closeHandle'],
          emits: ['ready', 'failed', 'closed', 'changed'],
          setup(props, { emit }) {
            const behavior = globalThis.__homePaneBehavior
            const handle = {
              sessionId: props.session.id,
              stopCalls: 0,
              closeCalls: 0,
              stop() {
                handle.stopCalls++
                return globalThis.__homePaneStop?.(handle.sessionId) ?? Promise.resolve()
              },
              close() {
                handle.closeCalls++
                return globalThis.__homePaneClose?.(handle.sessionId) ?? Promise.resolve()
              },
            }
            const instance = {
              sessionId: props.session.id,
              startMode: props.startMode,
              closeHandle: props.closeHandle,
              handle,
              mounted: false,
              get active() { return props.active },
              emitReady() { emit('ready', handle) },
              emitFailed() { emit('failed', handle) },
              emitClosed() { emit('closed') },
              emitChanged() { emit('changed') },
            }
            globalThis.__homePaneInstances.push(instance)
            let unregisterStop = () => {}
            onMounted(() => {
              instance.mounted = true
              globalThis.__homePaneMounts++
              // 与真实 Pane 一致：挂载时注册本地 stop，卸载时注销。
              unregisterStop = props.closeHandle.registerLocalStop(() =>
                handle.stop(),
              )
              if (behavior?.auto === 'failed') instance.emitFailed()
              else if (behavior?.auto !== 'none') instance.emitReady()
            })
            onBeforeUnmount(() => {
              unregisterStop()
              instance.mounted = false
              globalThis.__homePaneUnmounts++
            })
            return () => {
              globalThis.__homePaneActive = props.active
              return h('div', { class: 'terminal-pane-stub' }, String(props.active))
            }
          },
        })
      `)
    else if (specifier.endsWith('SessionManager.vue'))
      url = moduleURL(`
        import { defineComponent, getCurrentInstance, h, onBeforeUnmount, onMounted } from ${JSON.stringify(vueModuleURL)}
        export default defineComponent({
          name: 'SessionManagerStub',
          props: ['servers', 'canRefresh', 'authToken', 'endedIds'],
          emits: ['open', 'close'],
          setup(props, { emit, expose }) {
            const instance = getCurrentInstance()
            const refresh = () => { globalThis.__managerRefreshes++ }
            const invalidate = () => {
              globalThis.__managerInvalidates++
              globalThis.__managerEvents?.push('invalidate')
            }
            globalThis.__managerOpen = (item) => {
              const handler = instance?.vnode.props?.onOpen
              if (typeof handler === 'function') handler(item)
              else emit('open', item)
            }
            globalThis.__managerClose = (item) => {
              const handler = instance?.vnode.props?.onClose
              if (typeof handler === 'function') handler(item)
              else emit('close', item)
            }
            expose({ refresh, invalidate })
            onMounted(() => {
              globalThis.__managerMounts++
              // 真实组件挂载时会执行一次初始 GET。
              globalThis.__managerInitialLoads++
            })
            onBeforeUnmount(() => { globalThis.__managerUnmounts++ })
            return () => {
              globalThis.__managerEndedIds = props.endedIds ?? []
              return h('div', { class: 'manager-stub' }, '活动 Session')
            }
          },
        })
      `)
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
      url = moduleURL(
        'export default { render() { return null } }',
      )
    else if (specifier.startsWith('.'))
      url = new URL(
        specifier.endsWith('.ts') ? specifier : `${specifier}.ts`,
        homeSourceURL,
      ).href
    else url = import.meta.resolve(specifier)
    return `from ${JSON.stringify(url)}`
  })
const { default: HomePage } = await import(moduleURL(homeCode))

const flush = async () => {
  for (let i = 0; i < 40; i++) await Promise.resolve()
  await nextTick()
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((ok, fail) => {
    resolve = ok
    reject = fail
  })
  return { promise, resolve, reject }
}

const sessionID = (value) => value.repeat(32)
const serverID = (value) => value.repeat(32)

function view(
  id,
  status = 'CONNECTED',
  overrides = {},
) {
  const disconnected = status === 'DISCONNECTED'
  return {
    id: sessionID(id),
    server_id: serverID('b'),
    name: `session-${id}`,
    host: 'example.test',
    port: 22,
    username: 'operator',
    status,
    binding_generation: 3,
    current_cols: 120,
    current_rows: 40,
    created_at: '2026-11-17T01:02:03Z',
    disconnected_at: disconnected ? '2026-11-17T02:03:04Z' : null,
    last_attached_at: '2026-11-17T01:05:06Z',
    offline_expires_at: disconnected ? '2026-11-17T02:08:04Z' : null,
    remaining_seconds: disconnected ? 300 : null,
    ...overrides,
  }
}

const server = {
  id: serverID('b'),
  name: '生产服务器',
  host: 'example.test',
  port: 22,
  username: 'operator',
  group_name: '测试',
  remark: '',
  host_key: null,
  created_at: '2026-11-17T00:00:00Z',
  updated_at: '2026-11-17T00:00:00Z',
}

function success(items) {
  return Response.json({ code: 0, message: 'ok', data: { items } })
}

function failure(code, message) {
  return Response.json(
    { code, message, data: {} },
    { status: code === 'AUTH_REQUIRED' ? 401 : 503 },
  )
}

test('列表 DTO 只复制冻结字段，并拒绝非法数组与重复 ID', async (t) => {
  const original = globalThis.fetch
  t.after(() => {
    globalThis.fetch = original
  })
  let captured
  globalThis.fetch = async (path, options) => {
    captured = { path, options }
    return success([
      {
        ...view('a', 'DISCONNECTED'),
        password: 'must-not-escape',
        device_id: 'must-not-escape',
      },
    ])
  }
  const signal = new AbortController().signal
  const items = await listSessions(serverID('b'), signal)
  assert.equal(captured.path, `/api/sessions?server_id=${serverID('b')}`)
  assert.equal(captured.options.method, 'GET')
  assert.equal(captured.options.credentials, 'same-origin')
  assert.equal(captured.options.cache, 'no-store')
  assert.equal(captured.options.signal, signal)
  assert.deepEqual(items, [view('a', 'DISCONNECTED')])
  assert.equal('password' in items[0], false)
  assert.equal('device_id' in items[0], false)

  assert.deepEqual(sessionList({ items: [] }), [])
  assert.throws(() => sessionList({ items: null }), {
    code: 'INVALID_RESPONSE',
  })
  assert.throws(
    () => sessionList({ items: [view('a'), view('a')] }),
    { code: 'INVALID_RESPONSE' },
  )
  assert.throws(
    () => sessionList({ items: [{ ...view('a'), id: 'invalid' }] }),
    { code: 'INVALID_RESPONSE' },
  )
})

test('控制器：新请求胜出，取消后迟到响应和旧 finally 均被隔离', async () => {
  const calls = []
  const identity = {}
  const controller = new SessionListController({
    load: (_serverID, signal) => {
      const pending = deferred()
      calls.push({ signal, ...pending })
      return pending.promise
    },
    isAllowed: () => true,
    currentIdentity: () => identity,
  })

  const first = controller.refresh('')
  const second = controller.refresh(serverID('b'))
  assert.equal(calls.length, 2)
  assert.equal(calls[0].signal.aborted, true)
  assert.equal(controller.loading.value, true)

  calls[1].resolve([view('b')])
  await second
  assert.deepEqual(controller.items.value, [view('b')])
  assert.equal(controller.loading.value, false)
  assert.equal(controller.stale.value, false)

  calls[0].resolve([view('a')])
  await first
  assert.deepEqual(controller.items.value, [view('b')])
  assert.equal(controller.loading.value, false)
  assert.equal(controller.stale.value, false)
})

test('控制器：保留旧快照、标记非最新，失败不清空；dispose 后晚响应无效', async () => {
  const calls = []
  const identity = {}
  let disposed = false
  const controller = new SessionListController({
    load: (_serverID, signal) => {
      const pending = deferred()
      calls.push({ signal, ...pending })
      return pending.promise
    },
    isAllowed: () => true,
    currentIdentity: () => identity,
  })

  const initial = controller.refresh('')
  calls[0].resolve([view('a')])
  await initial
  const loadedAt = controller.lastLoadedAt.value
  assert.ok(loadedAt)

  const refresh = controller.refresh('')
  assert.equal(controller.loading.value, true)
  assert.equal(controller.stale.value, true)
  assert.deepEqual(controller.items.value, [view('a')])
  calls[1].reject(new ApiError('SERVER_UNAVAILABLE', '暂时不可用'))
  await refresh
  assert.deepEqual(controller.items.value, [view('a')])
  assert.equal(controller.stale.value, true)
  assert.equal(controller.error.value, '暂时不可用')
  assert.equal(controller.lastLoadedAt.value, loadedAt)

  const pending = controller.refresh('')
  controller.dispose()
  disposed = true
  assert.equal(calls[2].signal.aborted, true)
  calls[2].resolve([view('b')])
  await pending
  assert.equal(disposed, true)
  assert.deepEqual(controller.items.value, [view('a')])
  assert.equal(controller.loading.value, false)
})

test('控制器：身份变化、登出态和首次失败都不会发布旧结果', async () => {
  const calls = []
  let allowed = true
  let identity = { version: 1 }
  const controller = new SessionListController({
    load: (_serverID, signal) => {
      const pending = deferred()
      calls.push({ signal, ...pending })
      return pending.promise
    },
    isAllowed: () => allowed,
    currentIdentity: () => identity,
  })

  const pending = controller.refresh('')
  identity = { version: 2 }
  calls[0].resolve([view('a')])
  await pending
  assert.deepEqual(controller.items.value, [])
  assert.equal(controller.lastLoadedAt.value, null)

  allowed = false
  await controller.refresh('')
  assert.equal(calls.length, 1)

  allowed = true
  const failed = controller.refresh('')
  calls[1].reject(new ApiError('SERVER_UNAVAILABLE', '首次失败'))
  await failed
  assert.deepEqual(controller.items.value, [])
  assert.equal(controller.error.value, '首次失败')
  assert.equal(controller.lastLoadedAt.value, null)
})

const node = (type, text = '') => ({
  type,
  text,
  props: {},
  children: [],
  parent: null,
})
const renderer = createRenderer({
  createElement: (type) => {
    const el = node(type)
    el.style = {}
    el.addEventListener = () => {}
    el.removeEventListener = () => {}
    if (type === 'select') {
      el.options = []
      el.selectedIndex = -1
    }
    return el
  },
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
    if (parent.type === 'select' && el.type === 'option') parent.options.push(el)
  },
  remove(el) {
    if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1)
    if (el.parent?.type === 'select' && el.type === 'option') {
      const index = el.parent.options.indexOf(el)
      if (index >= 0) el.parent.options.splice(index, 1)
    }
    el.parent = null
  },
  parentNode: (el) => el.parent,
  nextSibling: (el) =>
    el.parent?.children[el.parent.children.indexOf(el) + 1] ?? null,
})

function textContent(el) {
  return (
    String(el.text ?? '') + el.children.map((child) => textContent(child)).join('')
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
function byClass(root, className) {
  return findAll(
    root,
    (el) => String(el.props.class ?? '').split(/\s+/).includes(className),
  )
}
function texts(root, className) {
  return byClass(root, className).map(textContent)
}
function byType(root, type) {
  return findAll(root, (el) => el.type === type)
}
function button(root, label) {
  return byType(root, 'button').find((el) => textContent(el) === label)
}
function click(el) {
  assert.ok(el)
  el.props.onClick?.({ type: 'click' })
}
function change(el, value) {
  assert.ok(el)
  el.props.onChange?.({ target: { value } })
}

async function mountManager(t, options = {}) {
  const originals = ['fetch', 'document'].map((key) => [
    key,
    Object.getOwnPropertyDescriptor(globalThis, key),
  ])
  const listeners = new Map()
  const document = {
    visibilityState: 'visible',
    addEventListener(type, callback) {
      listeners.set(type, callback)
    },
    removeEventListener(type) {
      listeners.delete(type)
    },
  }
  globalThis.document = document
  const requests = []
  globalThis.fetch = (path, options) => {
    const pending = deferred()
    requests.push({ path, options, ...pending })
    return pending.promise
  }
  const owner = {}
  const props = reactive({
    servers: options.servers ?? [server],
    canRefresh: options.canRefresh ?? true,
    authToken: owner,
    endedIds: options.endedIds ?? [],
  })
  const opens = []
  const closes = []
  const managerRef = ref(null)
  const Root = defineComponent({
    setup() {
      return () =>
        h(SessionManager, {
          ref: managerRef,
          ...props,
          onOpen: (item) => opens.push(item),
          onClose: (item) => closes.push(item),
        })
    },
  })
  const root = node('root')
  const app = renderer.createApp(Root)
  const component = app.mount(root)
  let mounted = true
  const unmount = () => {
    if (!mounted) return
    mounted = false
    app.unmount()
  }
  t.after(() => {
    unmount()
    for (const [key, descriptor] of originals) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else delete globalThis[key]
    }
  })
  await flush()
  return {
    component,
    document,
    listeners,
    closes,
    opens,
    props,
    requests,
    root,
    unmount,
    invalidate() {
      managerRef.value?.invalidate()
    },
    refresh() {
      managerRef.value?.refresh()
    },
    setAuthToken(value) {
      props.authToken = value
    },
    setCanRefresh(value) {
      props.canRefresh = value
    },
    setEndedIds(value) {
      props.endedIds = value
    },
    async respond(index, response) {
      requests[index].resolve(response)
      await flush()
    },
    async reject(index, error) {
      requests[index].reject(error)
      await flush()
    },
    async visibility(value) {
      document.visibilityState = value
      listeners.get('visibilitychange')?.()
      await flush()
    },
  }
}

test('实际 SessionManager：首次查询、空态、多状态展示和本地状态筛选', async (t) => {
  const h = await mountManager(t)
  assert.equal(h.requests.length, 1)
  assert.equal(h.requests[0].options.method, 'GET')
  assert.match(textContent(h.root), /正在查询活动 Session/)

  await h.respond(
    0,
    success([
      view('1', 'CONNECTING'),
      view('2', 'CONNECTED'),
      view('3', 'DISCONNECTED'),
      view('4', 'CLOSING'),
    ]),
  )
  assert.match(textContent(h.root), /生产服务器 · operator@example\.test:22/)
  for (const label of ['正在连接', '已连接', '已断开', '正在关闭'])
    assert.match(textContent(h.root), new RegExp(label))
  assert.match(textContent(h.root), /120 × 40/)
  assert.match(textContent(h.root), /300 秒/)
  const bodyRows = () =>
    findAll(byType(h.root, 'tbody')[0], (el) => el.type === 'tr')
  assert.equal(bodyRows().length, 4)

  const selects = byType(h.root, 'select')
  change(selects[1], 'CONNECTED')
  await flush()
  assert.equal(h.requests.length, 1)
  assert.equal(bodyRows().length, 1)
  assert.match(textContent(bodyRows()[0]), /session-2/)

  change(selects[0], serverID('b'))
  await flush()
  assert.equal(h.requests.length, 2)
  assert.equal(
    h.requests[1].path,
    `/api/sessions?server_id=${serverID('b')}`,
  )
  await h.respond(1, success([]))
  assert.match(textContent(h.root), /当前没有活动 Session/)

  const settledCount = h.requests.length
  for (let i = 0; i < 20; i++) await flush()
  assert.equal(h.requests.length, settledCount)
})

test('实际 SessionManager：首次失败与重试入口，刷新失败保留旧快照', async (t) => {
  const h = await mountManager(t)
  await h.respond(0, failure('SERVER_UNAVAILABLE', '服务暂时不可用'))
  assert.match(textContent(h.root), /查询失败：服务暂时不可用/)
  click(button(h.root, '重试'))
  await flush()
  assert.equal(h.requests.length, 2)

  await h.respond(1, success([view('a')]))
  click(button(h.root, '刷新'))
  await flush()
  assert.equal(h.requests.length, 3)
  assert.match(textContent(h.root), /session-a/)
  assert.match(textContent(h.root), /当前仍显示上次成功查询的快照/)

  await h.respond(2, failure('SERVER_UNAVAILABLE', '刷新失败'))
  assert.match(textContent(h.root), /session-a/)
  assert.match(textContent(h.root), /旧快照/)
  assert.match(textContent(h.root), /数据可能不是最新的/)
  assert.match(textContent(h.root), /刷新失败：刷新失败/)

  click(button(h.root, '刷新'))
  await flush()
  await h.respond(3, success([view('b')]))
  assert.match(textContent(h.root), /session-b/)
  assert.doesNotMatch(textContent(h.root), /session-a/)
  assert.match(textContent(h.root), /最新查询/)
})

test('实际 SessionManager：隐藏期间旧请求晚到，不能覆盖重新可见后的新结果', async (t) => {
  const h = await mountManager(t)
  assert.equal(h.requests.length, 1)
  await h.visibility('hidden')
  await h.visibility('visible')
  assert.equal(h.requests.length, 2)
  assert.equal(h.requests[0].options.signal.aborted, true)

  await h.respond(1, success([view('b')]))
  await h.respond(0, success([view('a')]))
  assert.match(textContent(h.root), /session-b/)
  assert.doesNotMatch(textContent(h.root), /session-a/)
  assert.match(textContent(h.root), /最新查询/)
})

test('实际 SessionManager：hidden 不查询，重新可见刷新一次，卸载取消并清理监听', async (t) => {
  const h = await mountManager(t)
  await h.respond(0, success([view('a')]))
  await h.visibility('hidden')
  assert.equal(h.requests.length, 1)
  for (let i = 0; i < 20; i++) await flush()
  assert.equal(h.requests.length, 1)

  await h.visibility('visible')
  assert.equal(h.requests.length, 2)
  assert.equal(h.listeners.has('visibilitychange'), true)
  assert.equal(h.requests[1].options.signal.aborted, false)

  h.unmount()
  assert.equal(h.requests[1].options.signal.aborted, true)
  assert.equal(h.listeners.has('visibilitychange'), false)
  h.requests[1].resolve(success([view('b')]))
  await flush()
})

test('S6-T36 SessionManager 操作列：打开 emit 当前行，!canRefresh 时禁用', async (t) => {
  const h = await mountManager(t)
  await h.respond(0, success([view('a'), view('b')]))
  const openButtons = () =>
    byType(h.root, 'button').filter((el) => textContent(el) === '打开')
  assert.equal(openButtons().length, 2)

  click(openButtons()[0])
  assert.equal(h.opens.length, 1)
  assert.equal(h.opens[0].id, view('a').id)

  h.setCanRefresh(false)
  await flush()
  assert.equal(openButtons().length, 2)
  assert.equal(
    openButtons().every((el) => el.props.disabled === true),
    true,
  )

  h.setCanRefresh(true)
  await flush()
  click(openButtons()[1])
  assert.equal(h.opens.length, 2)
  assert.equal(h.opens[1].id, view('b').id)
})

test('S7-2 SessionManager：行级关闭 emit 完整 item，CONNECTING/CLOSING 允许重复意图', async (t) => {
  const h = await mountManager(t)
  await h.respond(
    0,
    success([
      view('a', 'CONNECTING'),
      view('b', 'CLOSING'),
      view('c', 'CONNECTED'),
    ]),
  )
  const closeButtons = () =>
    byType(h.root, 'button').filter((el) => textContent(el) === '关闭')
  assert.equal(closeButtons().length, 3)

  click(closeButtons()[0])
  assert.deepEqual(h.closes[0], view('a', 'CONNECTING'))
  click(closeButtons()[1])
  assert.deepEqual(h.closes[1], view('b', 'CLOSING'))
  // 幂等去重属于协调器；列表只重复发出关闭意图。
  click(closeButtons()[1])
  assert.equal(h.closes.length, 3)
  click(closeButtons()[2])
  assert.deepEqual(h.closes[3], view('c', 'CONNECTED'))

  // 组件只查询列表，不直接调用任何 DELETE API。
  assert.equal(h.requests.length, 1)
  assert.equal(h.requests[0].options.method, 'GET')
  const source = await readFile(sourceURL, 'utf8')
  assert.doesNotMatch(source, /closeSession/)
  assert.doesNotMatch(source, /window\.confirm|<Modal/)
})

test('S7-2/S7-3 SessionManager：ended 行禁用打开与关闭，!canRefresh 全部禁用', async (t) => {
  const h = await mountManager(t, { endedIds: [view('a').id] })
  await h.respond(0, success([view('a'), view('b')]))
  const closeButtons = () =>
    byType(h.root, 'button').filter((el) => textContent(el) === '关闭')
  const openButtons = () =>
    byType(h.root, 'button').filter((el) => textContent(el) === '打开')

  assert.equal(closeButtons()[0].props.disabled, true)
  assert.equal(closeButtons()[1].props.disabled, false)
  click(closeButtons()[0])
  assert.equal(h.closes.length, 0)
  click(closeButtons()[1])
  assert.deepEqual(h.closes[0], view('b'))
  assert.equal(textContent(h.root).includes('确认关闭'), false)

  // ended tombstone 行不得重新打开，普通行行为不变。
  assert.equal(openButtons()[0].props.disabled, true)
  assert.equal(openButtons()[1].props.disabled, false)
  click(openButtons()[0])
  assert.equal(h.opens.length, 0)
  click(openButtons()[1])
  assert.deepEqual(h.opens[0], view('b'))

  h.setCanRefresh(false)
  await flush()
  assert.equal(
    closeButtons().every((el) => el.props.disabled === true),
    true,
  )
  click(closeButtons()[1])
  assert.equal(h.closes.length, 1)
})

test('S7-2 SessionManager：endedIds 变化后关闭按钮无需刷新即时禁用', async (t) => {
  const h = await mountManager(t)
  await h.respond(0, success([view('a'), view('b')]))
  const closeButtons = () =>
    byType(h.root, 'button').filter((el) => textContent(el) === '关闭')
  assert.equal(closeButtons()[0].props.disabled, false)

  h.setEndedIds([view('a').id])
  await flush()
  assert.equal(closeButtons()[0].props.disabled, true)
  assert.equal(closeButtons()[1].props.disabled, false)
  click(closeButtons()[0])
  assert.equal(h.closes.length, 0)
  click(closeButtons()[1])
  assert.deepEqual(h.closes[0], view('b'))
})


async function mountHomePage(t, options = {}) {
  const originals = ['fetch', 'matchMedia', 'WebSocket'].map((key) => [
    key,
    Object.getOwnPropertyDescriptor(globalThis, key),
  ])
  globalThis.__homePaneMounts = 0
  globalThis.__homePaneUnmounts = 0
  globalThis.__managerMounts = 0
  globalThis.__managerUnmounts = 0
  globalThis.__homePaneActive = null
  globalThis.__homePaneInstances = []
  globalThis.__managerRefreshes = 0
  globalThis.__managerInvalidates = 0
  globalThis.__managerInitialLoads = 0
  globalThis.__managerEvents = []
  globalThis.__webSocketCreates = 0
  globalThis.__routerReplaces = []
  globalThis.__homePaneBehavior = options.paneBehavior ?? { auto: 'ready' }
  globalThis.__homePaneStop = null
  globalThis.__homePaneClose = null
  globalThis.__routerReplaceHook = null
  authNotice.value = ''
  logoutPending.value = false
  globalThis.matchMedia = () => ({ matches: false })
  globalThis.WebSocket = class {
    constructor() {
      globalThis.__webSocketCreates++
    }
  }
  acceptIdentity({
    expires_at: '2099-01-01T00:00:00Z',
    client_settings: { max_cols: 80, max_rows: 24 },
  })
  const requests = []
  globalThis.fetch = async (path, init = {}) => {
    const record = {
      path,
      options: init,
      method: init.method ?? 'GET',
      body: init.body === undefined ? undefined : JSON.parse(init.body),
    }
    requests.push(record)
    if (options.fetch) {
      const custom = await options.fetch(record)
      if (custom !== undefined) return custom
    }
    if (path.startsWith('/api/servers?'))
      return Response.json({
        code: 0,
        message: 'ok',
        data: { items: [server] },
      })
    if (path === `/api/servers/${server.id}`)
      return Response.json({ code: 0, message: 'ok', data: server })
    if (
      path === `/api/servers/${server.id}/sessions` &&
      record.method === 'POST'
    )
      return Response.json({
        code: 0,
        message: 'ok',
        data: options.session ?? view('a'),
      })
    if (path === '/api/auth/logout' && record.method === 'POST') {
      globalThis.__managerEvents.push('logout')
      return Response.json({ code: 0, message: 'ok', data: {} })
    }
    if (record.method === 'DELETE' && path.startsWith('/api/sessions/'))
      return Response.json({ code: 0, message: 'ok', data: {} })
    if (record.method === 'GET' && path.startsWith('/api/sessions/'))
      return Response.json({
        code: 0,
        message: 'ok',
        data: options.sessionDetail ?? options.session ?? view('a'),
      })
    throw new Error(`unexpected request: ${path}`)
  }
  const root = node('root')
  const app = renderer.createApp(HomePage)
  app.mount(root)
  let mounted = true
  const unmount = () => {
    if (!mounted) return
    mounted = false
    app.unmount()
  }
  t.after(() => {
    unmount()
    clearIdentity()
    authNotice.value = ''
    logoutPending.value = false
    for (const [key, descriptor] of originals) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else delete globalThis[key]
    }
    delete globalThis.__homePaneMounts
    delete globalThis.__homePaneUnmounts
    delete globalThis.__managerMounts
    delete globalThis.__managerUnmounts
    delete globalThis.__homePaneActive
    delete globalThis.__homePaneInstances
    delete globalThis.__managerRefreshes
    delete globalThis.__managerInvalidates
    delete globalThis.__managerInitialLoads
    delete globalThis.__managerEvents
    delete globalThis.__webSocketCreates
    delete globalThis.__routerReplaces
    delete globalThis.__homePaneBehavior
    delete globalThis.__homePaneStop
    delete globalThis.__homePaneClose
    delete globalThis.__routerReplaceHook
    delete globalThis.__managerOpen
    delete globalThis.__managerClose
    delete globalThis.__managerEndedIds
  })
  await flush()
  return {
    requests,
    root,
    instances: globalThis.__homePaneInstances,
    routerReplaces: globalThis.__routerReplaces,
    unmount,
  }
}

test('实际 HomePage：会话管理视图切换重新挂载列表，但终端 Pane 保持同一实例', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '☰'))
  await flush()
  const serverRow = byType(h.root, 'button').find((item) =>
    textContent(item).includes('生产服务器'),
  )
  assert.ok(serverRow, textContent(h.root))
  click(serverRow)
  await flush()
  click(button(h.root, '连接终端'))
  await flush()
  assert.equal(globalThis.__homePaneMounts, 1)
  assert.equal(globalThis.__homePaneUnmounts, 0)
  assert.equal(globalThis.__homePaneActive, true)

  click(button(h.root, '会话管理'))
  await flush()
  assert.equal(globalThis.__managerMounts, 1)
  assert.equal(globalThis.__homePaneMounts, 1)
  assert.equal(globalThis.__homePaneUnmounts, 0)
  assert.equal(globalThis.__homePaneActive, false)

  click(button(h.root, '返回服务器'))
  await flush()
  assert.equal(globalThis.__managerUnmounts, 1)
  assert.equal(globalThis.__homePaneMounts, 1)
  assert.equal(globalThis.__homePaneUnmounts, 0)
  assert.equal(globalThis.__homePaneActive, true)

  click(button(h.root, '会话管理'))
  await flush()
  assert.equal(globalThis.__managerMounts, 2)
  assert.equal(globalThis.__homePaneMounts, 1)
})

test('S6-T27 同步双击打开只创建一条记录和一个 Pane', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  const item = view('a')
  globalThis.__managerOpen(item)
  globalThis.__managerOpen(item)
  await flush()
  assert.equal(h.instances.length, 1)
  assert.equal(globalThis.__homePaneMounts, 1)
  assert.equal(h.instances[0].startMode, 'candidate')
  assert.equal(byClass(h.root, 'terminal-pane-stub').length, 1)
})

test('S6-T28 已有记录再次打开只定位，不新建 Pane', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  const item = view('a')
  globalThis.__managerOpen(item)
  await flush()
  const first = h.instances[0]

  globalThis.__managerOpen(item)
  await flush()
  assert.equal(h.instances.length, 1)
  assert.equal(globalThis.__homePaneMounts, 1)
  assert.equal(first.active, true)
  assert.equal(byClass(h.root, 'terminal-pane-stub').length, 1)
})

test('S6-T29 无记录且为 CONNECTING/CLOSING 时提示且不建 Pane', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()

  globalThis.__managerOpen(view('c', 'CONNECTING'))
  await flush()
  assert.equal(h.instances.length, 0)
  assert.match(texts(h.root, 'notice').join(), /正在建立/)

  globalThis.__managerOpen(view('d', 'CLOSING'))
  await flush()
  assert.equal(h.instances.length, 0)
  assert.match(texts(h.root, 'notice').join(), /正在关闭/)
})

test('S6-T30 closed 后重新打开使用新 key，旧 key 迟到事件被忽略', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  const item = view('a')
  globalThis.__managerOpen(item)
  await flush()
  const first = h.instances[0]

  first.emitClosed()
  globalThis.__managerOpen(item)
  first.emitReady()
  await flush()
  assert.equal(h.instances.length, 2)
  const second = h.instances[1]
  assert.notEqual(second, first)
  assert.equal(byClass(h.root, 'terminal-pane-stub').length, 1)

  first.emitClosed()
  await flush()
  assert.equal(byClass(h.root, 'terminal-pane-stub').length, 1)

  click(button(h.root, '退出登录'))
  await flush()
  assert.equal(first.handle.stopCalls, 0)
  assert.equal(second.handle.stopCalls, 1)
})

test('S6-T31 failed 保留记录和已登记句柄', async (t) => {
  const h = await mountHomePage(t, {
    paneBehavior: { auto: 'failed' },
  })
  click(button(h.root, '会话管理'))
  await flush()
  const item = view('a')
  globalThis.__managerOpen(item)
  await flush()
  assert.equal(h.instances.length, 1)
  const failed = h.instances[0]
  assert.equal(byClass(h.root, 'terminal-pane-stub').length, 1)

  globalThis.__managerOpen(item)
  await flush()
  assert.equal(h.instances.length, 1)

  click(button(h.root, '退出登录'))
  await flush()
  assert.equal(failed.handle.stopCalls, 1)
  assert.equal(failed.handle.closeCalls, 1)
})

test('S6-T32 Pane 关闭失败不发 closed，记录保留且再次打开只定位', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  const item = view('a')
  globalThis.__managerOpen(item)
  await flush()
  const pane = h.instances[0]

  globalThis.__homePaneClose = () =>
    Promise.reject(new Error('close failed'))
  await assert.rejects(pane.handle.close(), /close failed/)
  assert.equal(pane.handle.closeCalls, 1)
  assert.equal(byClass(h.root, 'terminal-pane-stub').length, 1)

  globalThis.__managerOpen(item)
  await flush()
  assert.equal(h.instances.length, 1)
  assert.equal(globalThis.__homePaneMounts, 1)
})

test('S6-T33 接管中切换或卸载管理视图不影响 Pane 实例', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]
  assert.equal(pane.startMode, 'candidate')

  click(button(h.root, '会话管理'))
  await flush()
  assert.equal(globalThis.__managerMounts, 2)
  assert.equal(globalThis.__homePaneMounts, 1)
  assert.equal(globalThis.__homePaneUnmounts, 0)
  assert.equal(pane.active, false)

  click(button(h.root, '返回服务器'))
  await flush()
  assert.equal(h.instances[0], pane)
  assert.equal(pane.active, true)
  assert.equal(globalThis.__homePaneMounts, 1)
})

test('S6-T34 changed 只在管理视图刷新列表', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]

  pane.emitChanged()
  await flush()
  assert.equal(globalThis.__managerRefreshes, 0)

  click(button(h.root, '会话管理'))
  await flush()
  assert.equal(globalThis.__managerMounts, 2)
  pane.emitChanged()
  await flush()
  assert.equal(globalThis.__managerRefreshes, 1)
})

test('S6-T35 signOut 正常路径：先 stop，logout 与 stop 完成后再 close', async (t) => {
  const stop = deferred()
  const close = deferred()
  const logout = deferred()
  const h = await mountHomePage(t, {
    fetch: (record) =>
      record.path === '/api/auth/logout' ? logout.promise : undefined,
  })
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]
  globalThis.__homePaneStop = () => stop.promise
  globalThis.__homePaneClose = () => close.promise

  click(button(h.root, '退出登录'))
  assert.equal(pane.handle.stopCalls, 1)
  assert.equal(
    h.requests.some((item) => item.path === '/api/auth/logout'),
    true,
  )
  assert.equal(pane.handle.closeCalls, 0)

  logout.resolve(Response.json({ code: 0, message: 'ok', data: {} }))
  await flush()
  assert.equal(pane.handle.closeCalls, 0)

  stop.resolve()
  await flush()
  assert.equal(pane.handle.closeCalls, 1)

  close.resolve()
  await flush()
  assert.deepEqual(h.routerReplaces, ['/login'])
  assert.equal(authNotice.value, '')
})

test('S6-T37 connectServer 仍以 attach 创建并同 id 去重', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '☰'))
  await flush()
  const serverRow = byType(h.root, 'button').find((item) =>
    textContent(item).includes('生产服务器'),
  )
  assert.ok(serverRow, textContent(h.root))
  click(serverRow)
  await flush()
  const posts = () =>
    h.requests.filter(
      (item) =>
        item.path === `/api/servers/${server.id}/sessions` &&
        item.method === 'POST',
    )

  click(button(h.root, '连接终端'))
  await flush()
  assert.equal(h.instances.length, 1)
  assert.equal(h.instances[0].startMode, 'attach')
  assert.equal(posts().length, 1)

  click(button(h.root, '服务器'))
  await flush()
  click(button(h.root, '连接终端'))
  await flush()
  assert.equal(h.instances.length, 1)
  assert.equal(posts().length, 2)
})

test('S6-T39 HomePage 各阶段卸载：无新请求、无迟到记录变更', async (t) => {
  for (const stage of ['opening', 'ready', 'failed']) {
    await t.test(stage, async (t) => {
      const h = await mountHomePage(t, {
        paneBehavior: { auto: stage === 'opening' ? 'none' : stage },
      })
      click(button(h.root, '会话管理'))
      await flush()
      globalThis.__managerOpen(view('a'))
      await flush()
      const pane = h.instances[0]
      assert.ok(pane)
      const before = h.requests.length
      h.unmount()
      pane.emitReady()
      pane.emitFailed()
      pane.emitClosed()
      pane.emitChanged()
      await flush()
      assert.equal(h.requests.length, before)
      assert.equal(globalThis.__managerRefreshes, 0)
      assert.equal(globalThis.__managerUnmounts, 1)
      assert.equal(globalThis.__homePaneUnmounts, 1)
    })
  }

  await t.test('列表查询中卸载会 abort 且迟到结果不触发新请求', async (t) => {
    const pending = deferred()
    const h = await mountHomePage(t, {
      fetch: (record) =>
        record.path.startsWith('/api/servers?') ? pending.promise : undefined,
    })
    assert.equal(h.requests.length, 1)
    h.unmount()
    assert.equal(h.requests[0].options.signal.aborted, true)
    pending.resolve(
      Response.json({
        code: 0,
        message: 'ok',
        data: { items: [server] },
      }),
    )
    await flush()
    assert.equal(h.requests.length, 1)
    assert.equal(globalThis.__managerRefreshes, 0)
  })
})

test('S6-T40 多个 Pane 并发：记录、句柄和事件互不影响', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  globalThis.__managerOpen(view('b'))
  await flush()
  assert.equal(h.instances.length, 2)
  const [a, b] = h.instances
  assert.notEqual(a.sessionId, b.sessionId)
  assert.notEqual(a.handle, b.handle)

  globalThis.__homePaneStop = (id) =>
    id === a.sessionId
      ? Promise.resolve()
      : Promise.reject(new Error('other pane must not stop'))
  await a.handle.stop()
  assert.equal(a.handle.stopCalls, 1)
  assert.equal(b.handle.stopCalls, 0)

  click(button(h.root, '会话管理'))
  await flush()
  a.emitChanged()
  await flush()
  assert.equal(globalThis.__managerRefreshes, 1)
  assert.equal(b.handle.stopCalls, 0)

  a.emitClosed()
  await flush()
  assert.equal(h.instances.length, 2)
  assert.equal(byClass(h.root, 'terminal-pane-stub').length, 1)
  click(button(h.root, '返回服务器'))
  await flush()
  const secondTab = byType(h.root, 'button').find((item) =>
    textContent(item).includes('session-b'),
  )
  assert.ok(secondTab)
  click(secondTab)
  await flush()
  assert.equal(b.active, true)

  globalThis.__homePaneClose = (id) =>
    id === a.sessionId
      ? Promise.resolve()
      : Promise.reject(new Error('other pane must not close'))
  await a.handle.close()
  assert.equal(a.handle.closeCalls, 1)
  assert.equal(b.handle.closeCalls, 0)
})

test('S6-T50 logout 成功时分别统计 stop/close 失败并在跳转后反馈', async (t) => {
  const logged = []
  t.mock.method(console, 'error', (...args) => logged.push(args))
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  const a = view('a')
  const b = view('b')
  globalThis.__managerOpen(a)
  await flush()
  globalThis.__managerOpen(b)
  await flush()
  assert.equal(h.instances.length, 2)
  const panes = new Map(h.instances.map((item) => [item.sessionId, item]))

  globalThis.__homePaneStop = (id) =>
    id === a.id
      ? Promise.reject(new Error('stop failed'))
      : Promise.resolve()
  globalThis.__homePaneClose = (id) =>
    id === b.id
      ? Promise.reject(new Error('close failed'))
      : Promise.resolve()
  globalThis.__routerReplaceHook = () => {
    authNotice.value = '登录已过期，请重新登录'
  }

  click(button(h.root, '退出登录'))
  await flush()
  assert.deepEqual(h.routerReplaces, ['/login'])
  assert.equal(
    authNotice.value,
    '已退出登录；1 个终端未确认停止，1 个终端本地资源未释放，建议刷新页面',
  )
  assert.equal(panes.get(a.id).handle.closeCalls, 1)
  assert.equal(panes.get(b.id).handle.closeCalls, 1)
  assert.ok(
    logged.some(
      (args) => String(args[0]).includes('停止') && args.includes(a.id),
    ),
  )
  assert.ok(
    logged.some(
      (args) =>
        String(args[0]).includes('本地资源释放') &&
        args.includes(b.id),
    ),
  )
})

test('S6-T50 logout 失败时停留在本页、不 close 并反馈 stop 失败数', async (t) => {
  const logged = []
  t.mock.method(console, 'error', (...args) => logged.push(args))
  const h = await mountHomePage(t, {
    fetch: (record) =>
      record.path === '/api/auth/logout'
        ? failure('SERVER_UNAVAILABLE', '登出失败')
        : undefined,
  })
  click(button(h.root, '会话管理'))
  await flush()
  const item = view('a')
  globalThis.__managerOpen(item)
  await flush()
  const pane = h.instances[0]
  globalThis.__homePaneStop = () => Promise.reject(new Error('stop failed'))

  click(button(h.root, '退出登录'))
  await flush()
  assert.equal(pane.handle.stopCalls, 1)
  assert.equal(pane.handle.closeCalls, 0)
  assert.deepEqual(h.routerReplaces, [])
  assert.match(
    texts(h.root, 'notice').join(),
    /退出未完成，请重试。登出失败；1 个终端未确认停止/,
  )
  assert.ok(
    logged.some(
      (args) => String(args[0]).includes('停止') && args.includes(item.id),
    ),
  )
})

test('S7-C03/C22 非 idle 重开创建只读 Pane，ended 旧行不能重开', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  assert.equal(h.instances.length, 1)
  const first = h.instances[0]
  await first.closeHandle.requestClose()
  assert.equal(first.closeHandle.snapshot().state, 'requested')
  first.emitClosed()
  await flush()
  assert.equal(first.mounted, false)

  globalThis.__managerOpen(view('a'))
  await flush()
  assert.equal(h.instances.length, 2)
  assert.equal(
    h.instances[1].closeHandle.snapshot().state,
    'requested',
  )

  h.instances[1].closeHandle.markEnded()
  h.instances[1].emitClosed()
  await flush()
  const before = h.instances.length
  globalThis.__managerOpen(view('a'))
  await flush()
  assert.equal(h.instances.length, before)
  assert.match(textContent(h.root), /已结束或正在完成关闭/)
})

test('S7-C15 HomePage 卸载 dispose 协调器，迟到 DELETE 不发布', async (t) => {
  const pending = deferred()
  const h = await mountHomePage(t, {
    fetch: (record) =>
      record.method === 'DELETE' ? pending.promise : undefined,
  })
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const handle = h.instances[0].closeHandle
  const closing = handle.requestClose()
  assert.equal(handle.snapshot().state, 'pending')

  h.unmount()
  assert.equal(handle.snapshot().disposed, true)
  pending.resolve(Response.json({ code: 0, message: 'ok', data: {} }))
  await closing
  assert.equal(handle.snapshot().state, 'pending')
  assert.equal(handle.snapshot().disposed, true)
})

const deleteRequests = (h) =>
  h.requests.filter(
    (item) => item.method === 'DELETE' && item.path.startsWith('/api/sessions/'),
  )
const credentialOrTakeoverRequests = (h) =>
  h.requests.filter(
    (item) =>
      item.path.includes('credentials') || item.path.includes('takeover'),
  )

test('S7-2 HomePage：行级关闭先确认，取消时零 DELETE', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()

  assert.match(texts(h.root, 'modal-title').join(), /关闭远端 SSH Session/)
  assert.match(
    textContent(h.root),
    /不是关闭列表、本地终端或浏览器标签/,
  )
  assert.equal(deleteRequests(h).length, 0)

  click(button(h.root, '取消'))
  await flush()
  assert.equal(texts(h.root, 'modal-title').length, 0)
  assert.equal(deleteRequests(h).length, 0)
})

test('S7-2/C09 HomePage：无 Pane 确认后只发一次 DELETE，零凭证/takeover/WS', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()

  const deletes = deleteRequests(h)
  assert.equal(deletes.length, 1)
  assert.equal(deletes[0].path, `/api/sessions/${view('a').id}`)
  assert.equal(h.instances.length, 0)
  assert.equal(credentialOrTakeoverRequests(h).length, 0)
  assert.equal(
    h.requests.filter((item) => item.method === 'POST').length,
    0,
  )
  assert.equal(globalThis.__webSocketCreates, 0)
  // 关闭结果落地后沿用现有列表刷新机制。
  assert.equal(globalThis.__managerRefreshes, 1)
})

test('S7-2/D5 HomePage：无 Pane 关闭被接受时提示已提交，不宣称完全关闭', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()

  const notice = texts(h.root, 'notice').join()
  assert.match(notice, /关闭请求已提交/)
  assert.match(notice, /尚未完全关闭/)
  assert.doesNotMatch(notice, /关闭成功|已完全关闭/)
  assert.equal(deleteRequests(h).length, 1)
})

test('S7-2/D5 HomePage：无 Pane 关闭结果未知时明确提示且不自动重试', async (t) => {
  const h = await mountHomePage(t, {
    fetch: (record) =>
      record.method === 'DELETE'
        ? Response.json(
            { code: 'SERVER_UNAVAILABLE', message: '网关错误', data: {} },
            { status: 503 },
          )
        : undefined,
  })
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()

  assert.equal(deleteRequests(h).length, 1)
  assert.match(texts(h.root, 'notice').join(), /关闭请求结果未知/)
  for (let i = 0; i < 10; i++) await flush()
  assert.equal(deleteRequests(h).length, 1)

  // 再次点击只返回既有 unknown 快照，不重复 DELETE，也不改变反馈语义。
  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()
  assert.equal(deleteRequests(h).length, 1)
  assert.match(texts(h.root, 'notice').join(), /关闭请求结果未知/)
})

test('S7-2/D5 HomePage：有 Pane 时由 Pane 展示关闭状态，不额外提示', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]

  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()

  assert.equal(pane.closeHandle.snapshot().state, 'requested')
  assert.equal(texts(h.root, 'notice').length, 0)
  assert.equal(deleteRequests(h).length, 1)
})

test('S7-2/C08 HomePage：有 Pane 确认后经协调器调用本地 stop，只发一次 DELETE', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]

  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()

  assert.equal(pane.handle.stopCalls, 1)
  assert.equal(pane.handle.closeCalls, 0)
  assert.equal(deleteRequests(h).length, 1)
  assert.equal(deleteRequests(h)[0].path, `/api/sessions/${view('a').id}`)
  assert.equal(pane.closeHandle.snapshot().state, 'requested')
  assert.equal(credentialOrTakeoverRequests(h).length, 0)
})

test('S7-2/C08 HomePage：同一 Session 重复行级关闭只产生一个 DELETE', async (t) => {
  const pending = deferred()
  const h = await mountHomePage(t, {
    fetch: (record) => (record.method === 'DELETE' ? pending.promise : undefined),
  })
  click(button(h.root, '会话管理'))
  await flush()

  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()
  assert.equal(deleteRequests(h).length, 1)

  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()
  assert.equal(deleteRequests(h).length, 1)

  pending.resolve(Response.json({ code: 0, message: 'ok', data: {} }))
  await flush()
})

test('S7-2/C10 HomePage：不同 Session 分别关闭，状态互不影响', async (t) => {
  const pending = new Map()
  const h = await mountHomePage(t, {
    fetch: (record) => {
      if (record.method !== 'DELETE') return undefined
      const item = deferred()
      pending.set(record.path, item)
      return item.promise
    },
  })
  click(button(h.root, '会话管理'))
  await flush()

  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()
  globalThis.__managerClose(view('b'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()

  const deletes = deleteRequests(h)
  assert.equal(deletes.length, 2)
  assert.deepEqual(
    deletes.map((item) => item.path).sort(),
    [`/api/sessions/${view('a').id}`, `/api/sessions/${view('b').id}`].sort(),
  )
  pending.get(`/api/sessions/${view('a').id}`).resolve(
    Response.json({ code: 0, message: 'ok', data: {} }),
  )
  await flush()
  assert.equal(
    pending.has(`/api/sessions/${view('b').id}`),
    true,
  )
  pending.get(`/api/sessions/${view('b').id}`).resolve(
    Response.json({ code: 0, message: 'ok', data: {} }),
  )
  await flush()
})

test('S7-2 HomePage：CONNECTING 与 CLOSING 行都能走关闭意图', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()

  globalThis.__managerClose(view('c', 'CONNECTING'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()
  globalThis.__managerClose(view('d', 'CLOSING'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()

  assert.deepEqual(
    deleteRequests(h).map((item) => item.path),
    [
      `/api/sessions/${view('c', 'CONNECTING').id}`,
      `/api/sessions/${view('d', 'CLOSING').id}`,
    ],
  )
  assert.equal(h.instances.length, 0)
})

test('S7-2 HomePage：ended 行不打开确认框，也不发 DELETE', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  h.instances[0].closeHandle.markEnded()

  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()

  assert.equal(texts(h.root, 'modal-title').length, 0)
  assert.match(texts(h.root, 'notice').join(), /已结束或正在完成关闭/)
  assert.equal(deleteRequests(h).length, 0)
})

test('S7-2/C08 HomePage：行级关闭与 Pane 内关闭并发只有一个任务一个 DELETE', async (t) => {
  const pending = deferred()
  const h = await mountHomePage(t, {
    fetch: (record) => (record.method === 'DELETE' ? pending.promise : undefined),
  })
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]

  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()

  const paneClose = pane.closeHandle.requestClose()
  assert.equal(deleteRequests(h).length, 1)
  assert.equal(pane.handle.stopCalls, 1)
  assert.equal(pane.closeHandle.snapshot().state, 'pending')

  pending.resolve(Response.json({ code: 0, message: 'ok', data: {} }))
  await paneClose
  await flush()
  assert.equal(deleteRequests(h).length, 1)
  assert.equal(pane.closeHandle.snapshot().state, 'requested')
})

test('S7-2/C09 HomePage：无 Pane 关闭后打开同一 Session，新 Pane 非 idle 且零资源', async (t) => {
  const pending = deferred()
  const h = await mountHomePage(t, {
    fetch: (record) => (record.method === 'DELETE' ? pending.promise : undefined),
  })
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()
  click(button(h.root, '确认关闭'))
  await flush()
  assert.equal(deleteRequests(h).length, 1)

  globalThis.__managerOpen(view('a'))
  await flush()
  assert.equal(h.instances.length, 1)
  assert.equal(h.instances[0].startMode, 'candidate')
  assert.equal(h.instances[0].closeHandle.snapshot().state, 'pending')
  assert.equal(
    h.requests.filter((item) => item.method === 'POST').length,
    0,
  )
  assert.equal(credentialOrTakeoverRequests(h).length, 0)
  assert.equal(globalThis.__webSocketCreates, 0)
  assert.equal(deleteRequests(h).length, 1)

  pending.resolve(Response.json({ code: 0, message: 'ok', data: {} }))
  await flush()
})

test('S7-2 HomePage：确认框打开期间登出，不在登出期间发 DELETE', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerClose(view('a'))
  await flush()
  assert.equal(texts(h.root, 'modal-title').length, 1)

  click(button(h.root, '退出登录'))
  await flush()

  assert.equal(texts(h.root, 'modal-title').length, 0)
  assert.equal(deleteRequests(h).length, 0)
  assert.deepEqual(h.routerReplaces, ['/login'])
})

test('S7-2/D1 HomePage：协调器 ended 后列表立即收到 endedIds，权威拦截保留', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]

  click(button(h.root, '会话管理'))
  await flush()
  assert.deepEqual(globalThis.__managerEndedIds, [])
  const refreshes = globalThis.__managerRefreshes

  pane.closeHandle.markEnded()
  await flush()
  assert.deepEqual(globalThis.__managerEndedIds, [view('a').id])
  // ended 状态变化按统一合并入口刷新一次，不依赖重新进入页面。
  assert.equal(globalThis.__managerRefreshes, refreshes + 1)

  // 按钮禁用只是 UI 层；HomePage 入口校验仍然拒绝 ended 行。
  globalThis.__managerClose(view('a'))
  await flush()
  assert.equal(texts(h.root, 'modal-title').length, 0)
  assert.match(texts(h.root, 'notice').join(), /已结束或正在完成关闭/)
  assert.equal(deleteRequests(h).length, 0)
})

test('S7-2/D1 HomePage：卸载后 ended 订阅失效，不产生无效更新', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]
  h.unmount()

  assert.equal(pane.closeHandle.snapshot().disposed, true)
  const endedBefore = [...globalThis.__managerEndedIds]
  pane.closeHandle.markEnded()
  await flush()
  assert.equal(pane.closeHandle.snapshot().state, 'idle')
  assert.deepEqual(globalThis.__managerEndedIds, endedBefore)
})

test('S7-2/D1 Pane 关闭后迟到的 SESSION_NOT_FOUND 仍更新 ended 按钮', async (t) => {
  const detail = deferred()
  const h = await mountHomePage(t, {
    fetch: (record) => {
      if (record.method === 'DELETE')
        return Response.json({ code: 0, message: 'ok', data: {} })
      if (
        record.method === 'GET' &&
        record.path === `/api/sessions/${view('a').id}`
      )
        return detail.promise
      return undefined
    },
  })
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]
  await pane.closeHandle.requestClose()
  assert.equal(pane.closeHandle.snapshot().state, 'requested')

  const refreshing = pane.closeHandle.refreshClose()
  assert.equal(
    h.requests.filter(
      (item) =>
        item.method === 'GET' &&
        item.path === `/api/sessions/${view('a').id}`,
    ).length,
    1,
  )

  // 详情 GET 仍在途时关闭 Pane，record 被释放。
  pane.emitClosed()
  await flush()
  assert.equal(pane.mounted, false)
  assert.equal(byClass(h.root, 'terminal-pane-stub').length, 0)

  click(button(h.root, '会话管理'))
  await flush()
  assert.deepEqual(globalThis.__managerEndedIds, [])
  const refreshes = globalThis.__managerRefreshes

  // 迟到的详情 GET 走真实协调器订阅路径，而不是直接 markEnded。
  detail.resolve(
    Response.json(
      { code: 'SESSION_NOT_FOUND', message: '会话不存在', data: {} },
      { status: 404 },
    ),
  )
  await refreshing
  await flush()

  assert.equal(pane.closeHandle.snapshot().state, 'ended')
  assert.deepEqual(globalThis.__managerEndedIds, [view('a').id])
  // ended 状态变化触发一次合并刷新，且刷新不会清除 tombstone。
  assert.equal(globalThis.__managerRefreshes, refreshes + 1)
  assert.deepEqual(globalThis.__managerEndedIds, [view('a').id])
  assert.equal(deleteRequests(h).length, 1)
})

test('S7-2/D1 同一 Session 只建立一个 ended 订阅，Pane 重开不重复订阅', async (t) => {
  const subscribe = t.mock.method(
    CloseCoordinator.prototype,
    'subscribeClose',
  )
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  assert.equal(subscribe.mock.callCount(), 1)
  const pane = h.instances[0]

  // 已有记录再次打开只定位，不新建订阅。
  globalThis.__managerOpen(view('a'))
  await flush()
  assert.equal(h.instances.length, 1)
  assert.equal(subscribe.mock.callCount(), 1)

  // Pane 关闭后重开同一 Session，仍复用页面级唯一订阅。
  pane.emitClosed()
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  assert.equal(h.instances.length, 2)
  assert.equal(subscribe.mock.callCount(), 1)

  click(button(h.root, '会话管理'))
  await flush()
  h.instances[1].closeHandle.markEnded()
  await flush()
  assert.deepEqual(globalThis.__managerEndedIds, [view('a').id])
})

test('S7-2/D1 HomePage 销毁时注销全部 ended 订阅，销毁后不再更新', async (t) => {
  const original = CloseCoordinator.prototype.subscribeClose
  const unsubscribed = []
  const subscribe = t.mock.method(
    CloseCoordinator.prototype,
    'subscribeClose',
    function (sessionId, listener) {
      const unsubscribe = original.call(this, sessionId, listener)
      return () => {
        unsubscribed.push(sessionId)
        unsubscribe()
      }
    },
  )
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  globalThis.__managerOpen(view('b'))
  await flush()
  assert.equal(subscribe.mock.callCount(), 2)
  const [a, b] = h.instances

  h.unmount()
  assert.deepEqual(
    unsubscribed.sort(),
    [view('a').id, view('b').id].sort(),
  )

  const endedBefore = [...globalThis.__managerEndedIds]
  a.closeHandle.markEnded()
  b.closeHandle.markEnded()
  await flush()
  assert.deepEqual(globalThis.__managerEndedIds, endedBefore)
})

test('S7-3/A logoutPending 同步阻止新 GET，不依赖 props 更新', async (t) => {
  const h = await mountManager(t)
  await h.respond(0, success([view('a')]))
  const before = h.requests.length
  logoutPending.value = true
  t.after(() => {
    logoutPending.value = false
  })

  // 同步边界：不等待 nextTick，props.canRefresh 仍为 true。
  h.refresh()
  click(button(h.root, '刷新'))
  assert.equal(h.requests.length, before)

  // 身份缺失（props 已传播后）同样被拒绝。
  logoutPending.value = false
  h.setAuthToken(null)
  await flush()
  const afterIdentity = h.requests.length
  h.refresh()
  assert.equal(h.requests.length, afterIdentity)
})

test('S7-3/B invalidate 后旧请求不发布，旧 finally 不清新 loading', async (t) => {
  const h = await mountManager(t)
  await h.respond(0, success([view('a')]))

  h.refresh()
  assert.equal(h.requests.length, 2)
  h.invalidate()
  assert.equal(h.requests[1].options.signal.aborted, true)
  h.refresh()
  assert.equal(h.requests.length, 3)
  await flush()
  assert.match(textContent(h.root), /正在刷新/)

  // 旧请求迟到：不能发布，也不能关闭新请求的 loading。
  h.requests[1].resolve(success([view('b')]))
  await flush()
  assert.match(textContent(h.root), /session-a/)
  assert.doesNotMatch(textContent(h.root), /session-b/)
  assert.match(textContent(h.root), /正在刷新/)

  await h.respond(2, success([view('c')]))
  assert.match(textContent(h.root), /session-c/)
})

test('S7-3/B signOut 在 logout 请求前同步 invalidate', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()

  click(button(h.root, '退出登录'))
  // signOut 同步段：invalidate 必须先于 logout 请求。
  assert.deepEqual(globalThis.__managerEvents, ['invalidate', 'logout'])
  await flush()
  assert.deepEqual(h.routerReplaces, ['/login'])
})

test('S7-3/C paneChanged 在登出、身份失效与销毁后零刷新', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]
  click(button(h.root, '会话管理'))
  await flush()
  const before = globalThis.__managerRefreshes

  logoutPending.value = true
  pane.emitChanged()
  await flush()
  assert.equal(globalThis.__managerRefreshes, before)
  logoutPending.value = false

  clearIdentity()
  pane.emitChanged()
  await flush()
  assert.equal(globalThis.__managerRefreshes, before)

  acceptIdentity({
    expires_at: '2099-01-01T00:00:00Z',
    client_settings: { max_cols: 80, max_rows: 24 },
  })
  await flush()
  pane.emitChanged()
  await flush()
  assert.equal(globalThis.__managerRefreshes, before + 1)

  h.unmount()
  pane.emitChanged()
  await flush()
  assert.equal(globalThis.__managerRefreshes, before + 1)
})

test('S7-3/D 同批 changed 只刷新一次，下一批次允许新的刷新', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  globalThis.__managerOpen(view('b'))
  await flush()
  const [a, b] = h.instances
  click(button(h.root, '会话管理'))
  await flush()
  const before = globalThis.__managerRefreshes

  a.emitChanged()
  b.emitChanged()
  await flush()
  assert.equal(globalThis.__managerRefreshes, before + 1)

  a.emitChanged()
  await flush()
  assert.equal(globalThis.__managerRefreshes, before + 2)
})

test('S7-3/E 微任务执行前的视图、登出或身份变化会阻止刷新', async (t) => {
  for (const mode of ['view', 'logout', 'identity']) {
    await t.test(mode, async (t) => {
      const h = await mountHomePage(t)
      click(button(h.root, '会话管理'))
      await flush()
      globalThis.__managerOpen(view('a'))
      await flush()
      const pane = h.instances[0]
      click(button(h.root, '会话管理'))
      await flush()
      const before = globalThis.__managerRefreshes

      pane.emitChanged() // 已排入 microtask
      if (mode === 'view') click(button(h.root, '返回服务器'))
      if (mode === 'logout') logoutPending.value = true
      if (mode === 'identity') clearIdentity()
      await flush()
      assert.equal(globalThis.__managerRefreshes, before)
    })
  }
})

test('S7-3/F 隐藏期间的变更只标记，进入列表只执行一次初始加载', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]
  const refreshes = globalThis.__managerRefreshes
  const loads = globalThis.__managerInitialLoads

  // 打开后已定位到终端视图，changed 与协调器状态变化都发生在列表隐藏期间。
  pane.emitChanged()
  pane.closeHandle.markEnded()
  await flush()
  assert.equal(globalThis.__managerRefreshes, refreshes)
  assert.equal(globalThis.__managerInitialLoads, loads)

  click(button(h.root, '会话管理'))
  await flush()
  assert.equal(globalThis.__managerInitialLoads, loads + 1)
  assert.equal(globalThis.__managerRefreshes, refreshes)
})

test('S7-3/G changed 与协调器状态变化合并为一次刷新，idle 不刷新', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()
  const pane = h.instances[0]
  click(button(h.root, '会话管理'))
  await flush()

  // 订阅建立时的 idle 快照不触发刷新。
  assert.equal(globalThis.__managerRefreshes, 0)
  const before = globalThis.__managerRefreshes

  pane.emitChanged()
  pane.closeHandle.markEnded()
  await flush()
  assert.equal(globalThis.__managerRefreshes, before + 1)
})

test('S7-3/H 非 idle 打开只创建只读候选 Pane，不 prepare/凭证/WS/takeover', async (t) => {
  for (const state of ['pending', 'requested', 'unknown']) {
    await t.test(state, async (t) => {
      const pendingDelete = deferred()
      const h = await mountHomePage(t, {
        fetch: (record) => {
          if (record.method !== 'DELETE') return undefined
          if (state === 'pending') return pendingDelete.promise
          if (state === 'requested')
            return Response.json({ code: 0, message: 'ok', data: {} })
          return Response.json(
            { code: 'SERVER_UNAVAILABLE', message: '网关错误', data: {} },
            { status: 503 },
          )
        },
      })
      click(button(h.root, '会话管理'))
      await flush()
      globalThis.__managerClose(view('a'))
      await flush()
      click(button(h.root, '确认关闭'))
      await flush()
      assert.equal(deleteRequests(h).length, 1)

      globalThis.__managerOpen(view('a'))
      await flush()
      assert.equal(h.instances.length, 1)
      assert.equal(h.instances[0].startMode, 'candidate')
      assert.equal(h.instances[0].closeHandle.snapshot().state, state)
      assert.equal(
        h.requests.filter((item) => item.method === 'POST').length,
        0,
      )
      assert.equal(credentialOrTakeoverRequests(h).length, 0)
      assert.equal(globalThis.__webSocketCreates, 0)

      if (state === 'pending') {
        pendingDelete.resolve(
          Response.json({ code: 0, message: 'ok', data: {} }),
        )
        await flush()
      }
    })
  }

  await t.test('ended', async (t) => {
    const h = await mountHomePage(t)
    click(button(h.root, '会话管理'))
    await flush()
    globalThis.__managerOpen(view('a'))
    await flush()
    const pane = h.instances[0]
    pane.closeHandle.markEnded()
    pane.emitClosed()
    await flush()
    assert.equal(h.instances.length, 1)

    globalThis.__managerOpen(view('a'))
    await flush()
    assert.equal(h.instances.length, 1)
    assert.match(texts(h.root, 'notice').join(), /已结束或正在完成关闭/)
    assert.equal(deleteRequests(h).length, 0)
    assert.equal(credentialOrTakeoverRequests(h).length, 0)
    assert.equal(globalThis.__webSocketCreates, 0)
  })
})

test('S7-3/I ended tombstone 行禁用打开并提示，普通刷新不清除', async (t) => {
  const h = await mountManager(t, { endedIds: [view('a').id] })
  await h.respond(0, success([view('a'), view('b')]))
  const rows = () =>
    findAll(byType(h.root, 'tbody')[0], (el) => el.type === 'tr')
  const openButtons = () =>
    byType(h.root, 'button').filter((el) => textContent(el) === '打开')

  assert.match(textContent(rows()[0]), /已结束（不可接管）/)
  assert.doesNotMatch(textContent(rows()[1]), /已结束/)
  assert.equal(openButtons()[0].props.disabled, true)
  assert.equal(openButtons()[1].props.disabled, false)
  click(openButtons()[0])
  assert.equal(h.opens.length, 0)
  click(openButtons()[1])
  assert.deepEqual(h.opens[0], view('b'))

  // 服务端刷新仍返回旧行，tombstone 不清除，也不会新建连接请求。
  click(button(h.root, '刷新'))
  await h.respond(1, success([view('a'), view('b')]))
  assert.equal(openButtons()[0].props.disabled, true)
  assert.match(textContent(rows()[0]), /已结束（不可接管）/)
  assert.equal(
    h.requests.every((item) => (item.options.method ?? 'GET') === 'GET'),
    true,
  )
})

test('S7-3/J idle 打开保持 S6 候选流程，确认前零凭证/WS', async (t) => {
  const h = await mountHomePage(t)
  click(button(h.root, '会话管理'))
  await flush()
  globalThis.__managerOpen(view('a'))
  await flush()

  assert.equal(h.instances.length, 1)
  assert.equal(h.instances[0].startMode, 'candidate')
  assert.equal(h.instances[0].closeHandle.snapshot().state, 'idle')
  assert.equal(
    h.requests.filter((item) => item.method === 'POST').length,
    0,
  )
  assert.equal(credentialOrTakeoverRequests(h).length, 0)
  assert.equal(globalThis.__webSocketCreates, 0)
})
