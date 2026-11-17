import test from 'node:test'
import assert from 'node:assert/strict'
import {
  TerminalClient,
  TerminalClientTaskConflictError,
} from '../src/terminal/client.ts'
import { SessionRecovery } from '../src/terminal/recovery.ts'

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
const sessionId = 'a'.repeat(32)
const bound = (start, end, generation) => ({
  type: 'bind_success',
  binding_generation: generation,
  replay_start_offset: start,
  replay_end_offset: end,
  current_cols: 80,
  current_rows: 24,
})
const credentialResponse = () =>
  Response.json({
    code: 0,
    message: 'ok',
    data: {
      credential: 'a'.repeat(43),
      expires_at: new Date(Date.now() + 30000).toISOString(),
    },
  })
const jsonResponse = (data, code = 0) =>
  Response.json({ code, message: String(code), data })
const flush = async () => {
  for (let i = 0; i < 50; i++)
    await new Promise((resolve) => queueMicrotask(resolve))
}
const deferred = () => {
  let resolve
  const promise = new Promise((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const conflict = (reason) => (error) =>
  error instanceof TerminalClientTaskConflictError && error.reason === reason

function harness(t, { generation = 0, overrides = {} } = {}) {
  const originals = ['fetch', 'WebSocket', 'location'].map((key) => [
    key,
    Object.getOwnPropertyDescriptor(globalThis, key),
  ])
  const sockets = []
  const requests = []
  const states = []
  const terminals = []
  let allowed = true
  let responder = () => credentialResponse()
  class Socket {
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = 0
    bufferedAmount = 0
    sent = []
    constructor() {
      sockets.push(this)
    }
    open() {
      this.readyState = Socket.OPEN
      this.onopen?.()
    }
    message(value) {
      this.onmessage?.({
        data: value instanceof ArrayBuffer ? value : JSON.stringify(value),
      })
    }
    error() {
      this.onerror?.()
    }
    send(value) {
      this.sent.push(value)
    }
    close(code) {
      this.closeCode = code
      this.readyState = Socket.CLOSED
    }
  }
  globalThis.WebSocket = Socket
  globalThis.location = { href: 'http://localhost/', protocol: 'http:' }
  globalThis.fetch = (path, options) => {
    const body =
      typeof options?.body === 'string' ? JSON.parse(options.body) : undefined
    const request = {
      path,
      method: options?.method ?? 'GET',
      body,
      signal: options?.signal,
    }
    requests.push(request)
    return responder(request, options)
  }
  const client = new TerminalClient(
    sessionId,
    generation,
    { ...settings, ...overrides },
    () => {
      const terminal = {
        writes: [],
        disposed: false,
        write(data, callback) {
          this.writes.push({ data, callback })
        },
        dispose() {
          this.disposed = true
        },
      }
      terminals.push(terminal)
      return terminal
    },
    () => {},
    () => {},
    (state) => states.push(state),
    () => allowed,
  )
  t.after(async () => {
    await client.dispose()
    for (const [key, descriptor] of originals) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else delete globalThis[key]
    }
  })
  return {
    client,
    sockets,
    requests,
    states,
    terminals,
    setResponse(fn) {
      responder = fn
    },
    deny() {
      allowed = false
    },
  }
}

test('S5-C2 M01-M05：任务身份复用、mode/generation 冲突与参数校验优先', async (t) => {
  const h = harness(t, { generation: 5 })
  const first = h.client.connect(5)
  const completion = h.client.completion
  const taskId = h.client.taskId
  assert.equal(h.client.run(5), completion)
  assert.equal(h.client.connect(5), first)
  assert.equal(h.client.taskId, taskId)
  assert.throws(() => h.client.connectTakeover(5), conflict('mode_mismatch'))
  assert.throws(() => h.client.connect(6), conflict('generation_mismatch'))
  assert.throws(() => h.client.connect(-1), /非法预期绑定代次/)
  assert.throws(() => h.client.start(5, 'invalid'), /非法连接模式/)
  await flush()
  assert.equal(h.requests.length, 1)
  assert.deepEqual(h.requests[0].body, {
    mode: 'attach',
    expected_generation: 5,
  })
  await h.client.stop()
  assert.equal((await first).connected, false)

  const takeover = h.client.connectTakeover(6)
  await flush()
  assert.equal(h.requests.length, 2)
  assert.deepEqual(h.requests[1].body, {
    mode: 'takeover',
    expected_generation: 6,
  })
  assert.throws(() => h.client.connect(6), conflict('mode_mismatch'))
  await h.client.stop()
  assert.equal((await takeover).connected, false)
})

test('S5-C2 M06-M07/P07：stop 等待输出排空，期间拒绝新任务，完成后允许独立新任务', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = harness(t, { overrides: { write_drain_timeout: 5 } })
  const oldConnection = h.client.connect()
  const oldTaskId = h.client.taskId
  await flush()
  const socket = h.sockets[0]
  socket.open()
  socket.message(bound(0, 2, 1))
  socket.message(Uint8Array.of(1, 2).buffer)
  await flush()
  assert.equal(h.terminals[0].writes.length, 1)
  socket.error()
  await flush()

  let doneSettled = false
  let stopSettled = false
  h.client.completion.then(() => {
    doneSettled = true
  })
  const stopping = h.client.stop().then(() => {
    stopSettled = true
  })
  assert.equal(h.client.running, true)
  assert.equal(doneSettled, false)
  assert.equal(stopSettled, false)
  assert.throws(() => h.client.connectTakeover(2), conflict('task_stopping'))
  await flush()
  assert.equal(doneSettled, false)
  assert.equal(stopSettled, false)
  assert.equal(h.requests.length, 1)

  t.mock.timers.tick(5000)
  await stopping
  assert.equal((await oldConnection).connected, true)
  assert.equal(doneSettled, true)
  assert.equal(stopSettled, true)
  assert.equal(h.client.running, false)

  const next = h.client.connectTakeover(3)
  assert.ok(h.client.taskId > oldTaskId)
  await flush()
  assert.deepEqual(h.requests[1].body, {
    mode: 'takeover',
    expected_generation: 3,
  })
  assert.equal(h.sockets.length, 2)
  await h.client.stop()
  assert.equal((await next).connected, false)
})

test('S5-C2 M08：旧 socket 与旧 write 回调在新任务后保持隔离', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = harness(t, { overrides: { write_drain_timeout: 5 } })
  const oldConnection = h.client.connect()
  await flush()
  const oldSocket = h.sockets[0]
  oldSocket.open()
  oldSocket.message(bound(0, 2, 1))
  oldSocket.message(Uint8Array.of(1, 2).buffer)
  await flush()
  const oldWrite = h.terminals[0].writes[0].callback
  const oldCallbacks = {
    open: oldSocket.onopen,
    message: oldSocket.onmessage,
    error: oldSocket.onerror,
    close: oldSocket.onclose,
  }
  oldSocket.error()
  const stopping = h.client.stop()
  await flush()
  t.mock.timers.tick(5000)
  await stopping
  assert.equal((await oldConnection).connected, true)

  const next = h.client.connectTakeover(1)
  await flush()
  const nextSocket = h.sockets[1]
  nextSocket.open()
  nextSocket.message(bound(0, 0, 2))
  await flush()
  const sentBefore = nextSocket.sent.length
  const statesBefore = h.states.length
  oldCallbacks.open()
  oldCallbacks.message({
    data: JSON.stringify({ type: 'status', status: 'CONNECTED' }),
  })
  oldCallbacks.error()
  oldCallbacks.close({ code: 4001 })
  oldWrite()
  await flush()
  assert.equal(h.client.currentGeneration, 2)
  assert.equal(nextSocket.readyState, 1)
  assert.equal(nextSocket.sent.length, sentBefore)
  assert.equal(h.states.length, statesBefore)
  assert.equal(h.client.output.requestedStartOffset(), 0)
  await h.client.stop()
  assert.equal((await next).connected, true)
})

test('S5-C2 M08/P06：旧 Prepare 在 stop 后迟到，不二次 settle 或污染新任务', async (t) => {
  const h = harness(t)
  const pending = deferred()
  h.setResponse(() => pending.promise)
  let settlements = 0
  const oldConnection = h.client.connectTakeover(4).then((value) => {
    settlements++
    return value
  })
  await flush()
  assert.equal(h.requests.length, 1)
  const stopping = h.client.stop()
  pending.resolve(credentialResponse())
  assert.equal((await oldConnection).connected, false)
  assert.equal(settlements, 1)
  await stopping
  assert.equal(h.sockets.length, 0)

  h.setResponse(() => credentialResponse())
  const next = h.client.connectTakeover(4)
  await flush()
  assert.equal(settlements, 1)
  assert.equal(h.requests.length, 2)
  assert.equal(h.sockets.length, 1)
  assert.deepEqual(h.requests[1].body, {
    mode: 'takeover',
    expected_generation: 4,
  })
  await h.client.stop()
  assert.equal((await next).connected, false)
  assert.equal(settlements, 1)
})

test('S5-C2 P01-P02：首次 attach 失败返回 false，长期任务继续并只 settle 一次', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = harness(t, { overrides: { reconnect_max_attempts: 3 } })
  let prepares = 0
  h.setResponse(() => {
    prepares++
    return prepares === 1
      ? jsonResponse({}, 'BIND_BUSY')
      : credentialResponse()
  })
  let settlements = 0
  const connection = h.client.connect(7).then((value) => {
    settlements++
    return value
  })
  let completionSettled = false
  h.client.completion.then(() => {
    completionSettled = true
  })
  await flush()
  assert.equal((await connection).connected, false)
  assert.equal(settlements, 1)
  assert.equal(h.client.running, true)
  assert.equal(completionSettled, false)
  assert.equal(h.states.at(-1).phase, 'retry_wait')

  t.mock.timers.tick(1000)
  await flush()
  const socket = h.sockets[0]
  socket.open()
  socket.message(bound(0, 0, 8))
  await flush()
  assert.equal(settlements, 1)
  assert.equal(completionSettled, false)
  assert.equal(h.client.running, true)
  assert.equal(h.states.at(-1).phase, 'online')
  assert.deepEqual(
    h.requests.map(({ body }) => body),
    [
      { mode: 'attach', expected_generation: 7 },
      { mode: 'attach', expected_generation: 7 },
    ],
  )
  await h.client.stop()
})

test('S5-C2 P03-P04：attach/takeover 首次成功只结束 connection，长期 completion 保持 pending', async (t) => {
  const h = harness(t, { generation: 2 })
  let completionSettled = false
  const attach = h.client.connect(2)
  h.client.completion.then(() => {
    completionSettled = true
  })
  await flush()
  h.sockets[0].open()
  h.sockets[0].message(bound(0, 0, 3))
  assert.equal((await attach).connected, true)
  assert.equal(completionSettled, false)
  assert.equal(h.client.running, true)
  await h.client.stop()
  assert.equal(completionSettled, true)

  completionSettled = false
  const takeover = h.client.connectTakeover(3)
  h.client.completion.then(() => {
    completionSettled = true
  })
  await flush()
  h.sockets[1].open()
  h.sockets[1].message(bound(0, 0, 4))
  assert.equal((await takeover).connected, true)
  assert.equal(completionSettled, false)
  assert.equal(h.client.running, true)
  await h.client.stop()
  assert.equal(completionSettled, true)
})

test('S5-C2 P05-A：首次 takeover 失败后固定 attach，使用 initial generation 并最终 online', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = harness(t, { overrides: { reconnect_max_attempts: 3 } })
  let prepares = 0
  h.setResponse(() => {
    prepares++
    return prepares === 1
      ? jsonResponse({}, 'BIND_BUSY')
      : credentialResponse()
  })
  let settlements = 0
  const connection = h.client.connectTakeover(7).then((value) => {
    settlements++
    return value
  })
  await flush()
  assert.equal((await connection).connected, false)
  assert.equal(settlements, 1)
  assert.equal(h.client.running, true)
  t.mock.timers.tick(1000)
  await flush()
  const socket = h.sockets[0]
  socket.open()
  socket.message(bound(0, 0, 8))
  await flush()
  assert.equal(settlements, 1)
  assert.equal(h.client.currentGeneration, 8)
  assert.equal(h.states.at(-1).phase, 'online')
  assert.deepEqual(
    h.requests.map(({ body }) => body),
    [
      { mode: 'takeover', expected_generation: 7 },
      { mode: 'attach', expected_generation: 7 },
    ],
  )
  assert.ok(h.requests.every(({ path }) => path.endsWith('/takeover')))
  await h.client.stop()
})

test('S5-C2 P05-B：bind_success 丢失不推算 G+1，后续 attach 冲突即停止', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = harness(t, { overrides: { reconnect_max_attempts: 3 } })
  const connection = h.client.connectTakeover(3)
  const completion = h.client.completion
  await flush()
  h.sockets[0].open()
  h.sockets[0].error()
  assert.equal((await connection).connected, false)
  await flush()
  h.setResponse(() => jsonResponse({}, 'BIND_CONFLICT'))
  t.mock.timers.tick(1000)
  await flush()
  const result = await completion
  assert.equal(result.code, 'BIND_CONFLICT')
  assert.deepEqual(
    h.requests.map(({ body }) => body),
    [
      { mode: 'takeover', expected_generation: 3 },
      { mode: 'attach', expected_generation: 3 },
    ],
  )
  t.mock.timers.tick(10000)
  await flush()
  assert.equal(h.requests.length, 2)
})

test('S5-C2 P05-C：合法 bind_success 后断线，后续 attach 使用 confirmed generation', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = harness(t, { overrides: { reconnect_max_attempts: 3 } })
  h.client.connectTakeover(7)
  await flush()
  const socket = h.sockets[0]
  socket.open()
  socket.message(bound(0, 0, 8))
  await flush()
  assert.equal(h.client.currentGeneration, 8)
  socket.error()
  await flush()
  t.mock.timers.tick(1000)
  await flush()
  assert.deepEqual(
    h.requests.map(({ body }) => body),
    [
      { mode: 'takeover', expected_generation: 7 },
      { mode: 'attach', expected_generation: 8 },
    ],
  )
  await h.client.stop()
})

test('S5-C2 P05-D：首次 takeover 后可重试失败仍只 attach，预算耗尽即停止', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = harness(t, { overrides: { reconnect_max_attempts: 1 } })
  h.setResponse(() => jsonResponse({}, 'BIND_BUSY'))
  const connection = h.client.connectTakeover(5)
  const completion = h.client.completion
  await flush()
  assert.equal((await connection).connected, false)
  t.mock.timers.tick(1000)
  await flush()
  const result = await completion
  assert.equal(result.code, 'BIND_BUSY')
  assert.equal(result.exhausted, true)
  assert.deepEqual(
    h.requests.map(({ body }) => body),
    [
      { mode: 'takeover', expected_generation: 5 },
      { mode: 'attach', expected_generation: 5 },
    ],
  )
})

test('S5-C2 P05-E：不可恢复首败停止，缺失合法 generation 时不发请求', async (t) => {
  const stopped = harness(t)
  stopped.setResponse(() => jsonResponse({}, 'BIND_CONFLICT'))
  const connection = stopped.client.connectTakeover(6)
  const completion = stopped.client.completion
  await flush()
  assert.equal((await connection).connected, false)
  assert.equal((await completion).code, 'BIND_CONFLICT')
  assert.equal(stopped.requests.length, 1)

  const missing = harness(t, { generation: Number.NaN })
  assert.throws(() => missing.client.connect(), /非法预期绑定代次/)
  await flush()
  assert.equal(missing.requests.length, 0)
  assert.equal(missing.sockets.length, 0)
})

test('S5-C2 mode latch：稳定资格重置 retries 后仍不会重新 takeover', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = harness(t, {
    overrides: {
      reconnect_max_attempts: 3,
      reconnect_stable_time: 2,
    },
  })
  let prepares = 0
  h.setResponse(() => {
    prepares++
    return prepares === 1
      ? jsonResponse({}, 'BIND_BUSY')
      : credentialResponse()
  })
  const connection = h.client.connectTakeover(7)
  await flush()
  assert.equal((await connection).connected, false)
  t.mock.timers.tick(1000)
  await flush()
  const socket = h.sockets[0]
  socket.open()
  socket.message(bound(0, 0, 8))
  await flush()
  t.mock.timers.tick(2000)
  socket.error()
  await flush()
  t.mock.timers.tick(1000)
  await flush()
  assert.deepEqual(
    h.requests.map(({ body }) => body),
    [
      { mode: 'takeover', expected_generation: 7 },
      { mode: 'attach', expected_generation: 7 },
      { mode: 'attach', expected_generation: 8 },
    ],
  )
  await h.client.stop()
})

test('S5-C2 S6-PRE01-03：默认恢复只 attach，首次 false 不等于任务结束，stop 后才允许显式 takeover', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 10000 })
  const h = harness(t)
  h.setResponse(({ path, method, body }) => {
    if (method === 'GET' && path === `/api/sessions/${sessionId}`)
      return jsonResponse({
        id: sessionId,
        server_id: 'b'.repeat(32),
        name: 'test',
        host: 'localhost',
        port: 22,
        username: 'tester',
        status: 'DISCONNECTED',
        binding_generation: 7,
        current_cols: 80,
        current_rows: 24,
        created_at: '2026-11-15T00:00:00.000Z',
        disconnected_at: null,
        last_attached_at: null,
        offline_expires_at: '2026-11-15T01:00:00.000Z',
        remaining_seconds: 600,
      })
    if (body?.mode === 'attach') return jsonResponse({}, 'BIND_BUSY')
    return credentialResponse()
  })
  const recovery = new SessionRecovery(
    sessionId,
    h.client,
    () => true,
    () => {},
  )
  await recovery.refresh()
  assert.equal(recovery.candidate.binding_generation, 7)
  const action = recovery.recover()
  await flush()
  assert.deepEqual(h.requests[1].body, {
    mode: 'attach',
    expected_generation: 7,
  })
  assert.equal((await action).connected, false)
  assert.equal(h.client.running, true)
  assert.equal(recovery.canRecover, false)
  assert.equal(await recovery.recover(), null)
  assert.equal(h.requests.length, 2)

  await h.client.stop()
  const takeover = h.client.connectTakeover(8)
  await flush()
  assert.deepEqual(h.requests[2].body, {
    mode: 'takeover',
    expected_generation: 8,
  })
  assert.equal(h.requests.some(({ method }) => method === 'GET'), true)
  assert.equal(
    h.requests.filter(({ body }) => body?.mode === 'takeover').length,
    1,
  )
  await h.client.stop()
  assert.equal((await takeover).connected, false)
})
