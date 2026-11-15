import test from 'node:test'
import assert from 'node:assert/strict'
import { TerminalOutputTracker, parseControl } from '../src/terminal/output.ts'
import { TerminalClient } from '../src/terminal/client.ts'
const settings = {
  max_control_message_size: 16384,
  max_input_message_size: 64,
  output_chunk_size: 64,
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
test('实际手动连接逻辑：只发 attach、输入分块、断线不自动请求、不刷新代次', async (t) => {
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
  await client.connect()
  assert.deepEqual(requests[1], { mode: 'attach', expected_generation: 1 })
  await client.dispose()
  assert.equal(sockets[1].readyState, 3)
})
