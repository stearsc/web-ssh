import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { createRenderer, nextTick } from 'vue'
import { acceptIdentity, clearIdentity } from '../src/stores/auth.ts'

// 挂载实际 SFC 和 TerminalClient，仅替换浏览器与 xterm 外部接口。
const sourceURL = new URL(
  '../src/components/terminal/TerminalPane.vue',
  import.meta.url,
)
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
      url = moduleURL('export const Terminal = globalThis.__paneTerminal')
    else if (specifier === '@xterm/addon-fit')
      url = moduleURL(
        'export class FitAddon { proposeDimensions() { return { cols: 80, rows: 24 } } }',
      )
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
const renderer = createRenderer({
  createElement: (type) => Object.assign(node(type), { clientWidth: 800 }),
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
function texts(root, className) {
  const text = (el) => el.text + el.children.map(text).join('')
  const found = []
  const visit = (el) => {
    if (String(el.props.class).split(' ').includes(className))
      found.push(text(el).trim())
    el.children.forEach(visit)
  }
  visit(root)
  return found
}
async function mountPane(t, online = true) {
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
  ]
  const originals = keys.map((key) => [
    key,
    Object.getOwnPropertyDescriptor(globalThis, key),
  ])
  const sockets = [],
    requests = []
  globalThis.window = new EventTarget()
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: { onLine: online },
  })
  class Observer {
    observe() {}
    disconnect() {}
  }
  globalThis.ResizeObserver = globalThis.MutationObserver = Observer
  globalThis.document = { visibilityState: 'visible', documentElement: {} }
  globalThis.getComputedStyle = () => ({ getPropertyValue: () => '' })
  globalThis.__paneTerminal = class {
    cols = 80
    rows = 24
    options = {}
    loadAddon() {}
    open() {}
    focus() {}
    dispose() {}
    resize(cols, rows) {
      this.cols = cols
      this.rows = rows
    }
    onData() {
      return { dispose() {} }
    }
    onBinary() {
      return { dispose() {} }
    }
    onResize() {
      return { dispose() {} }
    }
    write(_bytes, callback) {
      callback()
    }
  }
  class Socket {
    static OPEN = 1
    static CLOSING = 2
    readyState = 0
    bufferedAmount = 0
    sent = []
    constructor() {
      sockets.push(this)
    }
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
  globalThis.WebSocket = Socket
  globalThis.location = { href: 'http://localhost/', protocol: 'http:' }
  globalThis.fetch = async (path, options) => {
    requests.push({ path, body: JSON.parse(options.body) })
    return Response.json({
      code: 0,
      message: 'ok',
      data: {
        credential: 'a'.repeat(43),
        expires_at: '2099-01-01T00:00:00Z',
      },
    })
  }
  acceptIdentity({
    expires_at: '2099-01-01T00:00:00Z',
    client_settings: settings,
  })
  const { default: Pane } = await import(moduleURL(code))
  const root = node('root')
  const app = renderer.createApp(Pane, {
    active: true,
    session: {
      id: 'a'.repeat(32),
      name: 'test',
      host: 'localhost',
      port: 22,
      username: 'test',
      binding_generation: 0,
      current_cols: 80,
      current_rows: 24,
    },
  })
  const pane = app.mount(root)
  t.after(async () => {
    await pane.dispose()
    app.unmount()
    clearIdentity()
    for (const [key, descriptor] of originals) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else delete globalThis[key]
    }
  })
  await flush()
  return { sockets, requests, root, pane }
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
  await h.pane.dispose()
  const before = texts(h.root, 'connection-state')
  window.dispatchEvent(new Event('offline'))
  await flush()
  assert.deepEqual(texts(h.root, 'connection-state'), before)
})

test('挂载时已经离线，联网事件本身不恢复已连接显示', async (t) => {
  const h = await mountPane(t, false)
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
