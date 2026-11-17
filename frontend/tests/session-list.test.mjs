import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { createRenderer, nextTick } from 'vue'
import { ApiError } from '../src/api/client.ts'
import { listSessions, sessionList } from '../src/api/sessions.ts'
import { SessionListController } from '../src/session/list.ts'
import { acceptIdentity, clearIdentity } from '../src/stores/auth.ts'

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
        'export function useRouter() { return { replace: async () => {} } }',
      )
    else if (specifier.endsWith('TerminalPane.vue'))
      url = moduleURL(`
        import { defineComponent, h, onBeforeUnmount, onMounted } from ${JSON.stringify(vueModuleURL)}
        export default defineComponent({
          props: ['active', 'session'],
          setup(props) {
            onMounted(() => { globalThis.__homePaneMounts++ })
            onBeforeUnmount(() => { globalThis.__homePaneUnmounts++ })
            return () => {
              globalThis.__homePaneActive = props.active
              return h('div', { class: 'terminal-pane-stub' }, String(props.active))
            }
          },
        })
      `)
    else if (specifier.endsWith('SessionManager.vue'))
      url = moduleURL(`
        import { defineComponent, h, onBeforeUnmount, onMounted } from ${JSON.stringify(vueModuleURL)}
        export default defineComponent({
          props: ['servers', 'canRefresh', 'authToken'],
          setup() {
            onMounted(() => { globalThis.__managerMounts++ })
            onBeforeUnmount(() => { globalThis.__managerUnmounts++ })
            return () => h('div', { class: 'manager-stub' }, '活动 Session')
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

async function mountManager(t) {
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
  const root = node('root')
  const app = renderer.createApp(SessionManager, {
    servers: [server],
    canRefresh: true,
    authToken: owner,
  })
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
    requests,
    root,
    unmount,
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


async function mountHomePage(t) {
  const originals = ['fetch', 'matchMedia'].map((key) => [
    key,
    Object.getOwnPropertyDescriptor(globalThis, key),
  ])
  globalThis.__homePaneMounts = 0
  globalThis.__homePaneUnmounts = 0
  globalThis.__managerMounts = 0
  globalThis.__managerUnmounts = 0
  globalThis.__homePaneActive = null
  globalThis.matchMedia = () => ({ matches: false })
  acceptIdentity({
    expires_at: '2099-01-01T00:00:00Z',
    client_settings: { max_cols: 80, max_rows: 24 },
  })
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push({ path, options })
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
      options.method === 'POST'
    )
      return Response.json({
        code: 0,
        message: 'ok',
        data: view('a'),
      })
    throw new Error(`unexpected request: ${path}`)
  }
  const root = node('root')
  const app = renderer.createApp(HomePage)
  app.mount(root)
  t.after(() => {
    app.unmount()
    clearIdentity()
    for (const [key, descriptor] of originals) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else delete globalThis[key]
    }
    delete globalThis.__homePaneMounts
    delete globalThis.__homePaneUnmounts
    delete globalThis.__managerMounts
    delete globalThis.__managerUnmounts
    delete globalThis.__homePaneActive
  })
  await flush()
  return { requests, root }
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
