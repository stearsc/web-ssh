import test from 'node:test'
import assert from 'node:assert/strict'
import {
  TerminalOutputTracker,
  parseControl,
} from '../src/terminal/output.ts'
import { SessionRecovery } from '../src/terminal/recovery.ts'
import { sessionView } from '../src/api/sessions.ts'
import {
  TerminalClient,
  TerminalClientTaskConflictError,
  clientSettings,
} from '../src/terminal/client.ts'
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
const tick = () => new Promise((resolve) => queueMicrotask(resolve))
function fixture(overrides = {}) {
  const terminals = [],
    notices = []
  const create = () => {
    const term = {
      writes: [],
      disposed: false,
      write(data, callback) {
        this.writes.push({ data, callback })
      },
      dispose() {
        this.disposed = true
      },
    }
    terminals.push(term)
    return term
  }
  const output = new TerminalOutputTracker(
    create,
    { ...settings, ...overrides },
    (v) => notices.push(v),
    () => notices.push('failure'),
  )
  return { output, terminals, notices, create }
}
const bound = (start, end, generation = 1) => ({
  type: 'bind_success',
  binding_generation: generation,
  replay_start_offset: start,
  replay_end_offset: end,
  current_cols: 80,
  current_rows: 24,
})
test('实际 tracker：回调偏移、唯一在途 write、断线丢弃未提交队列', async () => {
  const { output, terminals } = fixture()
  output.begin(0)
  output.bind(bound(0, 4))
  output.binary(Uint8Array.of(1, 2))
  output.binary(Uint8Array.of(3, 4))
  await tick()
  assert.equal(terminals[0].writes.length, 1)
  assert.equal(output.inputEnabled, false)
  assert.throws(() => output.requestedStartOffset())
  const drain = output.settle(false)
  terminals[0].writes[0].callback()
  await drain
  assert.equal(output.requestedStartOffset(), 2)
  assert.equal(terminals[0].writes.length, 1)
  output.begin(2)
  output.bind(bound(2, 4, 2))
  output.binary(Uint8Array.of(3, 4))
  await tick()
  terminals[0].writes[1].callback()
  await tick()
  assert.equal(output.inputEnabled, true)
  assert.equal(output.requestedStartOffset(), 4)
  await output.dispose()
})
test('实际 tracker：稳定字节、截断重建和旧回调隔离', async () => {
  const { output, terminals, notices } = fixture()
  output.begin(0)
  output.bind(bound(8, 10))
  assert.throws(() => output.binary(Uint8Array.of(1)))
  output.truncated({
    type: 'history_truncated',
    requested_start_offset: 0,
    replay_start_offset: 8,
    replay_end_offset: 10,
    lost_bytes: 8,
  })
  assert.equal(terminals[0].disposed, true)
  assert.equal(terminals.length, 2)
  assert.match(notices[0], /截断/)
  output.binary(Uint8Array.of(0x1b, 0xe4))
  await tick()
  assert.deepEqual([...terminals[1].writes[0].data], [0x1b, 0xe4])
  terminals[1].writes[0].callback()
  await tick()
  assert.equal(output.requestedStartOffset(), 10)
  await output.dispose()
  assert.equal(terminals.length, 2)
})
test('实际 tracker：队列上限、跨历史边界、排空超时不推进新实例', async () => {
  const { output, terminals } = fixture({
    write_queue_bytes: 2,
    write_drain_timeout: 0.01,
  })
  output.begin(0)
  output.bind(bound(0, 2))
  assert.throws(() => output.binary(Uint8Array.of(1, 2, 3)))
  output.binary(Uint8Array.of(1, 2))
  await tick()
  assert.throws(() => output.binary(Uint8Array.of(3)))
  const callback = terminals[0].writes[0].callback
  await output.settle(false)
  assert.equal(terminals[0].disposed, true)
  assert.equal(output.requestedStartOffset(), 0)
  callback()
  assert.equal(output.requestedStartOffset(), 0)
  await output.dispose()
})
test('实际 tracker：销毁期间超时不创建替代实例，重复销毁幂等', async () => {
  const { output, terminals } = fixture({ write_drain_timeout: 0.01 })
  output.begin(0)
  output.bind(bound(0, 2))
  output.binary(Uint8Array.of(1, 2))
  await tick()
  const a = output.dispose(),
    b = output.dispose()
  assert.equal(a, b)
  await a
  assert.equal(terminals.length, 1)
  assert.equal(terminals[0].disposed, true)
  terminals[0].writes[0].callback()
  assert.throws(() => output.begin(0))
})
test('控制消息：字段、代次、尺寸及错误码严格校验', () => {
  assert.deepEqual(
    parseControl(JSON.stringify(bound(0, 0)), settings),
    bound(0, 0),
  )
  for (const message of [
    { ...bound(0, 0), extra: 1 },
    { ...bound(0, 0), binding_generation: 0 },
    { ...bound(0, 0), current_cols: 501 },
    { type: 'error', code: 'made-up', message: 'x' },
  ])
    assert.throws(() => parseControl(JSON.stringify(message), settings))
})
test('实际连接逻辑：默认只发 attach、输入分块、停止后按原代次开启新任务', async (t) => {
  const originals = {
    fetch: globalThis.fetch,
    WebSocket: globalThis.WebSocket,
    location: globalThis.location,
  }
  t.after(() => Object.assign(globalThis, originals))
  const sockets = [],
    requests = [],
    writes = []
  class Socket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = 0
    bufferedAmount = 0
    constructor() {
      sockets.push(this)
      queueMicrotask(() => {
        this.readyState = 1
        this.onopen?.()
      })
    }
    send(value) {
      writes.push(value)
      if (typeof value === 'string' && JSON.parse(value).type === 'bind')
        queueMicrotask(() =>
          this.onmessage?.({
            data: JSON.stringify(bound(0, 0, requests.length)),
          }),
        )
    }
    close() {
      this.readyState = 3
    }
  }
  globalThis.WebSocket = Socket
  globalThis.location = { href: 'http://localhost/', protocol: 'http:' }
  globalThis.fetch = async (path, options) => {
    requests.push(JSON.parse(options.body))
    return Response.json({
      code: 0,
      message: 'success',
      data: {
        credential: 'a'.repeat(43),
        expires_at: new Date(Date.now() + 30000).toISOString(),
      },
    })
  }
  const notices = []
  const client = new TerminalClient(
    'a'.repeat(32),
    0,
    settings,
    () => ({
      write(_d, cb) {
        cb()
      },
      dispose() {},
    }),
    (s) => notices.push(s),
    () => {},
  )
  await client.connect()
  assert.deepEqual(requests, [{ mode: 'attach', expected_generation: 0 }])
  await client.send(new Uint8Array(150))
  assert.deepEqual(
    writes.filter((v) => v instanceof Uint8Array).map((v) => v.length),
    [64, 64, 22],
  )
  sockets[0].onclose({ code: 1006 })
  await tick()
  assert.equal(requests.length, 1)
  await client.stop()
  await client.connect()
  assert.deepEqual(requests[1], { mode: 'attach', expected_generation: 1 })
  await client.dispose()
  assert.equal(sockets[1].readyState, 3)
})

test('客户端配置：独立字段、缺项和计时器范围不得回退', () => {
  assert.equal(
    clientSettings({ ...settings, credential_timeout: 2, bind_timeout: 7 })
      .credential_timeout,
    2,
  )
  for (const value of [
    undefined,
    0,
    -1,
    1.5,
    2147484,
    Number.MAX_SAFE_INTEGER + 1,
  ])
    assert.throws(() =>
      clientSettings({ ...settings, credential_timeout: value }),
    )
})
test('凭证 POST 仅由 credential_timeout 取消，不创建 WS', async (t) => {
  const original = globalThis.fetch
  t.after(() => {
    globalThis.fetch = original
  })
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let signal
  globalThis.fetch = (_path, options) =>
    new Promise((_resolve, reject) => {
      signal = options.signal
      signal.addEventListener(
        'abort',
        () => reject(new DOMException('aborted', 'AbortError')),
        { once: true },
      )
    })
  const client = new TerminalClient(
    'a'.repeat(32),
    0,
    { ...settings, credential_timeout: 2, bind_timeout: 1 },
    () => ({
      write(_d, cb) {
        cb()
      },
      dispose() {},
    }),
    () => {},
    () => {},
  )
  const started = client.connect().catch(() => {})
  for (let i = 0; i < 6; i++) await tick()
  t.mock.timers.tick(1000)
  assert.equal(signal.aborted, false)
  t.mock.timers.tick(1000)
  assert.equal(signal.aborted, true)
  await started
  await client.dispose()
})

const flush = async () => {
  for (let i = 0; i < 30; i++) await tick()
}
function recoveryHarness(t, overrides = {}) {
  const originals = {
    fetch: globalThis.fetch,
    WebSocket: globalThis.WebSocket,
    location: globalThis.location,
  }
  t.after(() => Object.assign(globalThis, originals))
  const sockets = [],
    requests = [],
    states = [],
    terminals = []
  let response = () => ({
    ok: true,
    json: async () => ({
      code: 0,
      message: 'ok',
      data: {
        credential: 'a'.repeat(43),
        expires_at: new Date(Date.now() + 30000).toISOString(),
      },
    }),
  })
  let allowed = true
  class Socket {
    static OPEN = 1
    static CLOSING = 2
    readyState = 0
    bufferedAmount = 0
    sent = []
    constructor() {
      sockets.push(this)
    }
    open() {
      this.readyState = 1
      this.onopen?.()
    }
    message(value) {
      this.onmessage?.({
        data: value instanceof ArrayBuffer ? value : JSON.stringify(value),
      })
    }
    send(value) {
      this.sent.push(value)
    }
    close(code) {
      this.closeCode = code
      this.readyState = 3
    }
  }
  globalThis.WebSocket = Socket
  globalThis.location = { href: 'http://localhost/', protocol: 'http:' }
  globalThis.fetch = (path, options) => {
    requests.push({
      path,
      ...JSON.parse(options.body),
      signal: options.signal,
    })
    return response(path, options)
  }
  const client = new TerminalClient(
    'a'.repeat(32),
    0,
    { ...settings, ...overrides },
    () => {
      const term = {
        writes: [],
        disposed: false,
        write(data, cb) {
          this.writes.push({ data, cb })
        },
        dispose() {
          this.disposed = true
        },
      }
      terminals.push(term)
      return term
    },
    () => {},
    () => {},
    (s) => states.push(s),
    () => allowed,
  )
  t.after(() => client.dispose())
  return {
    client,
    sockets,
    requests,
    states,
    terminals,
    setResponse: (fn) => {
      response = fn
    },
    deny: () => {
      allowed = false
    },
  }
}
test('stop 同步封闭 controller/finish 尚为空的窗口，重复停止与 dispose 幂等', async (t) => {
  const h = recoveryHarness(t)
  const connecting = h.client.connect()
  const done = h.client.completion
  const a = h.client.stop(),
    b = h.client.stop()
  assert.equal(a, b)
  assert.equal((await connecting).connected, false)
  await done
  await a
  assert.equal(h.requests.length, 0)
  assert.equal(h.sockets.length, 0)
  const disposing = h.client.dispose()
  assert.equal(disposing, h.client.dispose())
  await disposing
  assert.throws(() => h.client.connect())
  assert.equal(h.terminals.length, 1)
})
test('stop 后晚到凭证与旧 onopen 不建立或发送新绑定', async (t) => {
  const h = recoveryHarness(t)
  let deliver
  h.setResponse(
    () =>
      new Promise((resolve) => {
        deliver = resolve
      }),
  )
  const connection = h.client.connect()
  await flush()
  const stopping = h.client.stop()
  assert.equal(h.requests[0].signal.aborted, true)
  deliver({
    ok: true,
    json: async () => ({
      code: 0,
      message: 'ok',
      data: {
        credential: 'a'.repeat(43),
        expires_at: new Date().toISOString(),
      },
    }),
  })
  await connection
  await stopping
  assert.equal(h.sockets.length, 0)
  h.setResponse(() => ({
    ok: true,
    json: async () => ({
      code: 0,
      message: 'ok',
      data: {
        credential: 'a'.repeat(43),
        expires_at: new Date().toISOString(),
      },
    }),
  }))
  const next = h.client.connect()
  await flush()
  const socket = h.sockets[0],
    oldOpen = socket.onopen,
    oldMessage = socket.onmessage
  await h.client.stop()
  await next
  oldOpen()
  oldMessage({ data: JSON.stringify(bound(0, 0)) })
  assert.equal(socket.sent.length, 0)
  assert.equal(h.client.currentGeneration, 0)
})
test('首次连接 Promise 在前缀完成结束，长期任务持续；输入等回调，resize 可先发', async (t) => {
  const h = recoveryHarness(t, { max_input_message_size: 128 })
  let done = false
  const connection = h.client.connect()
  h.client.completion.then(() => {
    done = true
  })
  await flush()
  const socket = h.sockets[0]
  socket.open()
  socket.message(bound(0, 2))
  assert.equal((await connection).connected, true)
  assert.equal(done, false)
  assert.equal(h.client.canInput, false)
  await h.client.send(Uint8Array.of(9))
  h.client.resize(100, 30)
  await flush()
  assert.equal(JSON.parse(socket.sent.at(-1)).type, 'resize')
  assert.equal(socket.sent.filter((x) => x instanceof Uint8Array).length, 0)
  socket.message(Uint8Array.of(1, 2).buffer)
  await flush()
  assert.equal(h.client.canInput, false)
  h.terminals[0].writes[0].cb()
  await flush()
  assert.equal(h.client.canInput, true)
  assert.equal(h.states.at(-1).phase, 'online')
  await h.client.stop()
  assert.equal(done, true)
})
test('网络和 HTTP 失败共享有限预算，忙不创建 WS、不换代、不复用凭证', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t, { reconnect_max_attempts: 2 })
  const connection = h.client.connect(),
    done = h.client.completion
  await flush()
  h.sockets[0].open()
  h.sockets[0].message(bound(0, 0))
  await connection
  const oldClose = h.sockets[0].onclose
  h.sockets[0].onerror()
  oldClose({ code: 1006 })
  await flush()
  h.setResponse(() => ({
    ok: false,
    json: async () => ({ code: 'BIND_BUSY', message: 'busy', data: {} }),
  }))
  for (let i = 0; i < 2; i++) {
    t.mock.timers.tick(1000)
    await flush()
  }
  const result = await done
  assert.equal(result.exhausted, true)
  assert.equal(h.requests.length, 3)
  assert.equal(h.sockets.length, 1)
  assert.deepEqual(
    h.requests.map((r) => r.expected_generation),
    [0, 1, 1],
  )
  assert.ok(h.requests.every((r) => r.mode === 'attach'))
  t.mock.timers.tick(100000)
  await flush()
  assert.equal(h.requests.length, 3)
})
test('一次稳定仅重置一次，随后 HTTP 失败不能连续重置；回调前不建立稳定资格', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t, {
    reconnect_max_attempts: 1,
    reconnect_stable_time: 2,
  })
  h.client.connect()
  const done = h.client.completion
  await flush()
  h.sockets[0].open()
  h.sockets[0].onerror()
  await flush()
  t.mock.timers.tick(1000)
  await flush()
  h.sockets[1].open()
  h.sockets[1].message(bound(0, 2))
  h.sockets[1].message(Uint8Array.of(1, 2).buffer)
  await flush()
  t.mock.timers.tick(5000)
  assert.equal(h.client.output.consumeStability(Date.now()), false)
  h.terminals[0].writes[0].cb()
  await flush()
  t.mock.timers.tick(2000)
  h.sockets[1].onerror()
  await flush()
  h.setResponse(() => ({
    ok: false,
    json: async () => ({ code: 'BIND_BUSY', message: 'busy', data: {} }),
  }))
  t.mock.timers.tick(1000)
  await flush()
  assert.equal((await done).exhausted, true)
  assert.equal(h.requests.length, 3)
})
test('排空完成后才开始重试间隔，旧 write 回调决定恢复偏移', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t, { write_drain_timeout: 20 })
  h.client.connect()
  await flush()
  const s = h.sockets[0]
  s.open()
  s.message(bound(0, 4))
  s.message(Uint8Array.of(1, 2).buffer)
  s.message(Uint8Array.of(3, 4).buffer)
  await flush()
  s.onerror()
  t.mock.timers.tick(5000)
  await flush()
  assert.equal(h.requests.length, 1)
  h.terminals[0].writes[0].cb()
  await flush()
  t.mock.timers.tick(999)
  await flush()
  assert.equal(h.requests.length, 1)
  t.mock.timers.tick(1)
  await flush()
  h.sockets[1].open()
  assert.equal(JSON.parse(h.sockets[1].sent[0]).requested_start_offset, 2)
  assert.equal(h.terminals[0].writes.length, 1)
})
test('所有停止业务码优先于后续 Close，冲突不自动查询或换代', async (t) => {
  const h = recoveryHarness(t)
  for (const code of [
    'BIND_CONFLICT',
    'AUTH_REQUIRED',
    'PERMISSION_DENIED',
    'INPUT_BACKPRESSURE',
    'INVALID_MESSAGE',
    'INVALID_OFFSET',
    'INVALID_SIZE',
    'ALREADY_BOUND',
    'MESSAGE_TOO_LARGE',
    'SESSION_NOT_FOUND',
    'SESSION_NOT_READY',
  ]) {
    const connection = h.client.connect(),
      done = h.client.completion
    await flush()
    const s = h.sockets.at(-1),
      close = s.onclose
    s.open()
    s.message({ type: 'error', code, message: code })
    close({ code: 1006 })
    assert.equal((await connection).connected, false)
    const result = await done
    assert.equal(result.code, code)
    assert.equal(result.exhausted, false)
    assert.equal(
      result.diagnostic,
      ['SESSION_NOT_FOUND', 'SESSION_NOT_READY'].includes(code),
    )
  }
  assert.equal(h.requests.length, 11)
  assert.ok(
    h.requests.every(
      (r) => r.expected_generation === 0 && r.path.endsWith('/takeover'),
    ),
  )
})
test('裸关闭码遵循冻结分类；4001/1000停止，4002及网络关闭有限恢复', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t)
  for (const code of [
    4001, 1000, 1008, 1009, 4003, 1006, 1011, 1012, 1013, 4002,
  ]) {
    h.client.connect()
    const done = h.client.completion
    await flush()
    const s = h.sockets.at(-1)
    s.onclose({ code })
    await flush()
    if ([1006, 1011, 1012, 1013, 4002].includes(code)) {
      assert.equal(h.states.at(-1).phase, 'retry_wait')
      await h.client.stop()
    } else assert.equal((await done).code, `WS_${code}`)
  }
})
test('持续 OUTPUT_GAP 和本地终端重建不能无限恢复，旧 epoch 回调无效', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t, { reconnect_max_attempts: 2 })
  h.client.connect()
  const done = h.client.completion
  for (let i = 0; i < 3; i++) {
    await flush()
    const s = h.sockets[i]
    s.open()
    s.message(bound(0, 0, i + 1))
    s.message({ type: 'error', code: 'OUTPUT_GAP', message: 'gap' })
    await flush()
    if (i < 2) t.mock.timers.tick(1000)
  }
  assert.equal((await done).exhausted, true)
  assert.equal(h.requests.length, 3)
})

const sessionSnapshot = (generation = 7, status = 'DISCONNECTED') => ({
  id: 'a'.repeat(32),
  server_id: 'b'.repeat(32),
  name: 'test',
  host: 'localhost',
  port: 22,
  username: 'tester',
  status,
  binding_generation: generation,
  current_cols: 80,
  current_rows: 24,
  created_at: '2026-11-15T00:00:00.000Z',
  disconnected_at: null,
  last_attached_at: null,
  offline_expires_at:
    status === 'DISCONNECTED' ? '2026-11-15T01:00:00.000Z' : null,
  remaining_seconds: status === 'DISCONNECTED' ? 600 : null,
})
const jsonResponse = (data, code = 0) => ({
  ok: code === 0,
  json: async () => ({ code, message: String(code), data }),
})
const credentialResponse = () =>
  jsonResponse({
    credential: 'a'.repeat(43),
    expires_at: new Date(Date.now() + 30000).toISOString(),
  })
test('takeover mode 只消费一次：首次失败后 attach 使用初始代次且首次 Promise 不二次结束', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t, { reconnect_max_attempts: 3 })
  let prepares = 0
  h.setResponse(() => {
    prepares++
    return prepares < 3 ? jsonResponse({}, 'BIND_BUSY') : credentialResponse()
  })
  let settlements = 0
  const connection = h.client.connectTakeover(7).then((value) => {
    settlements++
    return value
  })
  const done = h.client.runTakeover(7)
  assert.equal(done, h.client.completion)
  await flush()
  assert.deepEqual(
    h.requests.map(({ mode, expected_generation }) => ({
      mode,
      expected_generation,
    })),
    [{ mode: 'takeover', expected_generation: 7 }],
  )
  assert.equal((await connection).connected, false)
  assert.equal(settlements, 1)
  assert.equal(h.client.running, true)
  t.mock.timers.tick(1000)
  await flush()
  assert.deepEqual(
    h.requests.map(({ mode, expected_generation }) => ({
      mode,
      expected_generation,
    })),
    [
      { mode: 'takeover', expected_generation: 7 },
      { mode: 'attach', expected_generation: 7 },
    ],
  )
  assert.equal(h.sockets.length, 0)
  t.mock.timers.tick(1000)
  await flush()
  assert.deepEqual(
    h.requests.map(({ mode, expected_generation }) => ({
      mode,
      expected_generation,
    })),
    [
      { mode: 'takeover', expected_generation: 7 },
      { mode: 'attach', expected_generation: 7 },
      { mode: 'attach', expected_generation: 7 },
    ],
  )
  const socket = h.sockets[0]
  socket.open()
  socket.message(bound(0, 0, 8))
  await flush()
  assert.equal(settlements, 1)
  assert.equal(h.client.currentGeneration, 8)
  assert.equal(h.states.at(-1).phase, 'online')
  assert.equal(h.requests.length, 3)
  await h.client.stop()
  await done
})
test('takeover bind_success 丢失不推算 G+1；后续合法确认代次驱动 attach', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t, { reconnect_max_attempts: 3 })
  const connection = h.client.connectTakeover(7)
  const done = h.client.completion
  await flush()
  const lost = h.sockets[0]
  lost.open()
  lost.onerror()
  assert.equal((await connection).connected, false)
  await flush()
  t.mock.timers.tick(1000)
  await flush()
  assert.deepEqual(
    h.requests.map(({ mode, expected_generation }) => ({
      mode,
      expected_generation,
    })),
    [
      { mode: 'takeover', expected_generation: 7 },
      { mode: 'attach', expected_generation: 7 },
    ],
  )
  const recovered = h.sockets[1]
  recovered.open()
  recovered.message(bound(0, 0, 8))
  await flush()
  assert.equal(h.client.currentGeneration, 8)
  recovered.onerror()
  await flush()
  t.mock.timers.tick(1000)
  await flush()
  assert.deepEqual(
    h.requests.map(({ mode, expected_generation }) => ({
      mode,
      expected_generation,
    })),
    [
      { mode: 'takeover', expected_generation: 7 },
      { mode: 'attach', expected_generation: 7 },
      { mode: 'attach', expected_generation: 8 },
    ],
  )
  await h.client.stop()
  await done
})
test('活动任务参数先校验再复用，不同 mode/generation 和 stopping 明确拒绝', async (t) => {
  const h = recoveryHarness(t)
  const conflict = (reason) => (error) =>
    error instanceof TerminalClientTaskConflictError && error.reason === reason
  const first = h.client.connect(0)
  assert.equal(h.client.run(0), h.client.completion)
  assert.equal(h.client.connect(0), first)
  await flush()
  const socket = h.sockets[0]
  socket.open()
  socket.message(bound(0, 0, 1))
  await first
  assert.equal(h.client.connect(), first)
  assert.throws(() => h.client.connectTakeover(0), conflict('mode_mismatch'))
  assert.throws(() => h.client.connect(1), conflict('generation_mismatch'))
  assert.throws(() => h.client.connect(-1), /非法预期绑定代次/)
  assert.equal(h.requests.length, 1)
  const stopping = h.client.stop()
  assert.throws(() => h.client.connectTakeover(1), conflict('task_stopping'))
  await stopping
  const takeover = h.client.connectTakeover(1)
  await flush()
  assert.deepEqual(
    h.requests.map(({ mode, expected_generation }) => ({
      mode,
      expected_generation,
    })),
    [
      { mode: 'attach', expected_generation: 0 },
      { mode: 'takeover', expected_generation: 1 },
    ],
  )
  assert.throws(() => h.client.connect(1), conflict('mode_mismatch'))
  await h.client.stop()
  assert.equal((await takeover).connected, false)
  await h.client.dispose()
})
test('takeover BIND_CONFLICT 立即停止，不自动查询或再发 attach', async (t) => {
  const h = recoveryHarness(t)
  h.setResponse(() => jsonResponse({}, 'BIND_CONFLICT'))
  const connection = h.client.connectTakeover(3)
  const done = h.client.completion
  await flush()
  assert.equal((await connection).connected, false)
  const result = await done
  assert.equal(result.code, 'BIND_CONFLICT')
  assert.equal(result.exhausted, false)
  assert.equal(h.requests.length, 1)
  assert.equal(h.requests[0].mode, 'takeover')
  assert.equal(h.requests[0].expected_generation, 3)
  assert.equal(h.sockets.length, 0)
})
test('takeover 提交结果不确定后的 attach 冲突停止，不查询新代次', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t)
  const connection = h.client.connectTakeover(3)
  const done = h.client.completion
  await flush()
  h.sockets[0].open()
  h.sockets[0].onerror()
  assert.equal((await connection).connected, false)
  await flush()
  h.setResponse(() => jsonResponse({}, 'BIND_CONFLICT'))
  t.mock.timers.tick(1000)
  await flush()
  const result = await done
  assert.equal(result.code, 'BIND_CONFLICT')
  assert.deepEqual(
    h.requests.map(({ mode, expected_generation }) => ({
      mode,
      expected_generation,
    })),
    [
      { mode: 'takeover', expected_generation: 3 },
      { mode: 'attach', expected_generation: 3 },
    ],
  )
  t.mock.timers.tick(10000)
  await flush()
  assert.equal(h.requests.length, 2)
})
test('人工恢复必须刷新再点击：查询不改代次，候选只消费一次且只发 attach', async (t) => {
  const h = recoveryHarness(t)
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push({
      path,
      method: options.method,
      body: options.body && JSON.parse(options.body),
    })
    return options.method === 'GET'
      ? jsonResponse(sessionSnapshot())
      : jsonResponse({
          credential: 'a'.repeat(43),
          expires_at: new Date().toISOString(),
        })
  }
  const recovery = new SessionRecovery(
    'a'.repeat(32),
    h.client,
    () => true,
    () => {},
  )
  assert.equal(await recovery.recover(), null)
  await recovery.refresh()
  assert.equal(h.client.currentGeneration, 0)
  assert.equal(requests.length, 1)
  assert.equal(h.sockets.length, 0)
  const action = recovery.recover(),
    duplicate = recovery.recover()
  assert.equal(await duplicate, null)
  await flush()
  assert.deepEqual(requests[1].body, {
    mode: 'attach',
    expected_generation: 7,
  })
  const socket = h.sockets[0]
  socket.open()
  socket.message(bound(0, 0, 8))
  assert.equal((await action).connected, true)
  await recovery.stop()
  assert.equal(await recovery.recover(), null)
  assert.equal(requests.length, 2)
})
test('详情查询乱序、主动取消、失败、诊断和迟到结果均不能留下可用旧候选', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t)
  let allowed = true
  const pending = []
  globalThis.fetch = (_path, options) =>
    new Promise((resolve) =>
      pending.push({ resolve, signal: options.signal }),
    )
  const recovery = new SessionRecovery(
    'a'.repeat(32),
    h.client,
    () => allowed,
    () => {},
  )
  const first = recovery.refresh(),
    second = recovery.refresh()
  assert.equal(pending[0].signal.aborted, true)
  t.mock.timers.tick(1000000)
  assert.equal(pending[1].signal.aborted, false) // GET 没有借用任何本地自动期限。
  pending[1].resolve(jsonResponse(sessionSnapshot(9)))
  await second
  pending[0].resolve(jsonResponse(sessionSnapshot(2)))
  await first
  assert.equal(recovery.candidate.binding_generation, 9)
  const diagnostic = recovery.refresh(false)
  assert.equal(recovery.candidate, null)
  pending[2].resolve(jsonResponse(sessionSnapshot(10)))
  await diagnostic
  assert.equal(recovery.snapshot.binding_generation, 10)
  assert.equal(recovery.candidate, null)
  const late = recovery.refresh()
  allowed = false
  recovery.invalidate()
  pending[3].resolve(jsonResponse(sessionSnapshot(11)))
  await late
  assert.equal(recovery.candidate, null)
  assert.equal(recovery.loading, false)
  allowed = true
  const failure = recovery.refresh()
  pending[4].resolve(jsonResponse({}, 'SESSION_NOT_FOUND'))
  await failure
  assert.equal(recovery.ended, true)
  assert.equal(recovery.canRecover, false)
  assert.equal(h.sockets.length, 0)
})
test('SessionView 所用字段缺失或非法不能回退旧快照', () => {
  assert.equal(
    sessionView(sessionSnapshot(), 'a'.repeat(32)).binding_generation,
    7,
  )
  for (const patch of [
    { id: 'other' },
    { status: 'CLOSED' },
    { binding_generation: -1 },
    { binding_generation: Number.MAX_SAFE_INTEGER + 1 },
    { remaining_seconds: undefined },
    { offline_expires_at: null },
    { current_cols: 0 },
    { created_at: 'bad' },
  ])
    assert.throws(() =>
      sessionView({ ...sessionSnapshot(), ...patch }, 'a'.repeat(32)),
    )
})
test('人工动作 await 停止窗口内失效，不得启动连接或重用候选', async (t) => {
  const h = recoveryHarness(t)
  globalThis.fetch = async () => jsonResponse(sessionSnapshot())
  const recovery = new SessionRecovery(
    'a'.repeat(32),
    h.client,
    () => true,
    () => {},
  )
  await recovery.refresh()
  const result = recovery.recover()
  recovery.invalidate()
  assert.equal(await result, null)
  assert.equal(recovery.candidate, null)
  assert.equal(h.sockets.length, 0)
})

test('各明确可重试错误共享同一有限恢复入口', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t)
  for (const code of [
    'BIND_BUSY',
    'INVALID_CREDENTIAL',
    'BIND_TIMEOUT',
    'OUTPUT_GAP',
    'SLOW_CLIENT',
    'SERVER_SHUTTING_DOWN',
    'INTERNAL_ERROR',
  ]) {
    h.client.connect()
    await flush()
    h.sockets.at(-1).message({ type: 'error', code, message: code })
    await flush()
    assert.equal(h.states.at(-1).phase, 'retry_wait')
    assert.equal(h.states.at(-1).code, code)
    await h.client.stop()
  }
  for (const code of ['CREDENTIAL_LIMIT', 'CONNECTION_LIMIT_REACHED']) {
    h.setResponse(() => jsonResponse({}, code))
    h.client.connect()
    await flush()
    assert.equal(h.states.at(-1).phase, 'retry_wait')
    assert.equal(h.states.at(-1).code, code)
    await h.client.stop()
  }
})
test('本地过载关闭4003，终端 write 失败重建；销毁时异常不重建', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t, { write_queue_bytes: 2 })
  h.client.connect()
  await flush()
  const s = h.sockets[0]
  s.open()
  s.message(bound(0, 0))
  s.message(Uint8Array.of(1, 2).buffer)
  await flush()
  s.message(Uint8Array.of(3).buffer)
  assert.equal(s.closeCode, 4003)
  await flush()
  const oldCallback = h.terminals[0].writes[0].cb
  t.mock.timers.tick(1000)
  await flush()
  assert.equal(h.terminals.length, 2)
  oldCallback()
  assert.equal(h.client.output.requestedStartOffset(), 0)
  t.mock.timers.tick(1000)
  await flush()
  const next = h.sockets[1]
  next.open()
  next.message(bound(0, 0, 2))
  h.terminals[1].write = () => {
    throw new Error('write failed')
  }
  next.message(Uint8Array.of(4).buffer)
  await flush()
  assert.equal(h.states.at(-1).code, 'CLIENT_TERMINAL_ERROR')
  assert.equal(h.terminals.length, 3)
  await h.client.dispose()
  assert.equal(h.terminals.length, 3)
})
test('stop 在旧 write 排空窗口及退出意图下禁止凭证、WS和输入', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = recoveryHarness(t)
  h.client.connect()
  await flush()
  const s = h.sockets[0]
  s.open()
  s.message(bound(0, 2))
  s.message(Uint8Array.of(1, 2).buffer)
  await flush()
  s.onerror()
  const stopping = h.client.stop()
  h.deny()
  h.terminals[0].writes[0].cb()
  await stopping
  t.mock.timers.tick(5000)
  await flush()
  assert.equal(h.requests.length, 1)
  assert.equal(h.sockets.length, 1)
  assert.equal(h.client.canInput, false)
  assert.throws(() => h.client.connect())
})
