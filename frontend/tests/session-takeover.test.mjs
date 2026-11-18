import test from 'node:test'
import assert from 'node:assert/strict'
import { ApiError } from '../src/api/client.ts'
import { SessionTakeover } from '../src/terminal/takeover.ts'

const id = 'a'.repeat(32)
const flush = async () => {
  for (let i = 0; i < 40; i++) await Promise.resolve()
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
function view(overrides = {}) {
  return {
    id,
    server_id: 'b'.repeat(32),
    name: 'session-a',
    host: 'example.test',
    port: 22,
    username: 'operator',
    status: 'CONNECTED',
    binding_generation: 5,
    current_cols: 120,
    current_rows: 40,
    created_at: '2026-11-17T01:02:03Z',
    disconnected_at: null,
    last_attached_at: '2026-11-17T01:05:06Z',
    offline_expires_at: null,
    remaining_seconds: null,
    ...overrides,
  }
}
function success(data) {
  return Response.json({ code: 0, message: 'ok', data })
}
function failure(code, message) {
  return Response.json({ code, message, data: {} }, { status: 503 })
}

function setup(t, options = {}) {
  const original = globalThis.fetch
  const state = {
    allowed: true,
    canBegin: true,
    ended: false,
    running: false,
    ...options.state,
  }
  const requests = []
  const connectCalls = []
  const finished = []
  const counts = { invalidateRecovery: 0, changed: 0, markEnded: 0 }
  const client = {
    get running() {
      return state.running
    },
    stop: options.stop ?? (async () => {}),
    connectTakeover: async (generation) => {
      connectCalls.push(generation)
      if (options.connectTakeover) return options.connectTakeover(generation)
      return { connected: true, code: '', message: '' }
    },
  }
  globalThis.fetch = async (path, init = {}) => {
    const record = {
      path,
      method: init.method ?? 'GET',
      body: init.body === undefined ? undefined : JSON.parse(init.body),
      signal: init.signal,
    }
    requests.push(record)
    if (options.fetch) return options.fetch(record)
    return success(view(options.detail))
  }
  const takeover = new SessionTakeover({
    id,
    client,
    invalidateRecovery: () => {
      counts.invalidateRecovery++
    },
    allowed: () => state.allowed,
    canBegin: () => state.canBegin,
    isEnded: () => state.ended,
    markEnded: () => {
      counts.markEnded++
      state.ended = true
    },
    changed: () => {
      counts.changed++
    },
    finished: (result) => finished.push(result),
  })
  t.after(() => {
    globalThis.fetch = original
  })
  return {
    takeover,
    state,
    requests,
    connectCalls,
    finished,
    counts,
  }
}

test('S6-T01 prepare 先完整 stop，stop 完成前不发 GET', async (t) => {
  const stop = deferred()
  const h = setup(t, {
    stop: async () => {
      await stop.promise
      h.state.running = false
    },
    state: { running: true },
  })
  const task = h.takeover.prepare()
  await flush()
  assert.equal(h.counts.invalidateRecovery, 1)
  assert.equal(h.takeover.phase, 'stopping')
  assert.equal(h.requests.length, 0)
  stop.resolve()
  await task
  assert.equal(h.requests.length, 1)
  assert.equal(h.requests[0].method, 'GET')
  assert.equal(h.takeover.phase, 'confirming')
  assert.equal(h.takeover.candidate?.binding_generation, 5)
})

test('S6-T02 stopping 阶段取消后，stop 完成也不发 GET、不启动连接', async (t) => {
  const stop = deferred()
  const h = setup(t, {
    stop: async () => {
      await stop.promise
    },
    state: { running: true },
  })
  const task = h.takeover.prepare()
  await flush()
  h.takeover.cancel()
  assert.equal(h.takeover.phase, 'idle')
  stop.resolve()
  await task
  assert.equal(h.requests.length, 0)
  assert.equal(h.connectCalls.length, 0)
  assert.equal(h.takeover.candidate, null)
})

test('S6-T03 loading 阶段取消会 abort GET，迟到响应不写候选', async (t) => {
  const response = deferred()
  const h = setup(t, {
    fetch: () => response.promise,
  })
  const task = h.takeover.prepare()
  await flush()
  assert.equal(h.takeover.phase, 'loading')
  assert.equal(h.requests.length, 1)
  h.takeover.cancel()
  assert.equal(h.requests[0].signal.aborted, true)
  assert.equal(h.takeover.phase, 'idle')
  response.resolve(success(view()))
  await task
  assert.equal(h.takeover.candidate, null)
  assert.equal(h.takeover.phase, 'idle')
  assert.equal(h.connectCalls.length, 0)
})

test('S6-T04 详情状态为 CONNECTING/CLOSING/非法时不进入确认', async (t) => {
  for (const detail of [
    view({ status: 'CONNECTING' }),
    view({ status: 'CLOSING' }),
    { ...view(), binding_generation: -1 },
  ]) {
    const h = setup(t, { detail })
    await h.takeover.prepare()
    assert.equal(h.takeover.phase, 'idle')
    assert.equal(h.takeover.candidate, null)
    assert.ok(h.takeover.error)
  }
})

test('S6-T05 SESSION_NOT_FOUND 标记结束，之后不能准备', async (t) => {
  const h = setup(t, {
    fetch: () => failure('SESSION_NOT_FOUND', 'Session 已结束'),
  })
  await h.takeover.prepare()
  assert.equal(h.counts.markEnded, 1)
  assert.equal(h.state.ended, true)
  assert.equal(h.takeover.phase, 'idle')
  assert.equal(h.takeover.canPrepare, false)
  await h.takeover.prepare()
  assert.equal(h.requests.length, 1)
})

test('S6-T06 确认只调用一次 connectTakeover，使用详情代次，双击无效', async (t) => {
  const h = setup(t)
  await h.takeover.prepare()
  assert.equal(h.takeover.phase, 'confirming')
  const first = h.takeover.confirm()
  const second = h.takeover.confirm()
  await Promise.all([first, second])
  assert.deepEqual(h.connectCalls, [5])
  assert.equal(h.finished.length, 1)
  assert.equal(h.takeover.phase, 'idle')
  assert.equal(h.takeover.message, '已接管该 Session')
})

test('S6-T07 确认时 start 同步抛错，回到 idle 并提示', async (t) => {
  const h = setup(t, {
    connectTakeover: () => {
      throw new ApiError('task_stopping', '请等待旧任务停止')
    },
  })
  await h.takeover.prepare()
  await h.takeover.confirm()
  assert.equal(h.connectCalls.length, 1)
  assert.equal(h.takeover.phase, 'idle')
  assert.match(h.takeover.error, /请等待旧任务停止/)
  assert.equal(h.finished.length, 0)
})

test('S6-T08 连接结果失败时保留原因，不再次 takeover', async (t) => {
  const h = setup(t, {
    connectTakeover: () => ({
      connected: false,
      code: 'BIND_CONFLICT',
      message: '绑定代次冲突',
    }),
  })
  await h.takeover.prepare()
  await h.takeover.confirm()
  assert.equal(h.connectCalls.length, 1)
  assert.equal(h.finished.length, 1)
  assert.equal(h.takeover.phase, 'idle')
  assert.match(h.takeover.message, /绑定代次冲突/)
  await flush()
  assert.equal(h.connectCalls.length, 1)
})

test('S6-T09 connecting 阶段 invalidate 丢弃迟到结果', async (t) => {
  const result = deferred()
  const h = setup(t, {
    connectTakeover: () => result.promise,
  })
  await h.takeover.prepare()
  const task = h.takeover.confirm()
  await flush()
  assert.equal(h.takeover.phase, 'connecting')
  h.takeover.invalidate()
  assert.equal(h.takeover.phase, 'idle')
  result.resolve({ connected: true, code: '', message: '' })
  await task
  assert.equal(h.finished.length, 0)
  assert.equal(h.takeover.phase, 'idle')
})

test('S6-T10 确认时 allowed 为假或 running 为真都不启动', async (t) => {
  for (const patch of [
    { allowed: false },
    { running: true },
  ]) {
    const h = setup(t)
    await h.takeover.prepare()
    Object.assign(h.state, patch)
    assert.equal(h.takeover.canConfirm, false)
    await h.takeover.confirm()
    assert.equal(h.connectCalls.length, 0)
    assert.equal(h.takeover.phase, 'idle')
    assert.ok(h.takeover.error)
  }
})

test('S6-T11 stop 后 running 仍为真时失败，不发 GET', async (t) => {
  const h = setup(t, {
    stop: async () => {},
    state: { running: true },
  })
  await h.takeover.prepare()
  assert.equal(h.requests.length, 0)
  assert.equal(h.takeover.phase, 'idle')
  assert.match(h.takeover.error, /尚未停止/)
})

test('S6-T12 loading 中准入消失但未显式失效时就地复位', async (t) => {
  const response = deferred()
  const h = setup(t, { fetch: () => response.promise })
  const task = h.takeover.prepare()
  await flush()
  assert.equal(h.takeover.phase, 'loading')
  h.state.allowed = false
  response.resolve(success(view()))
  await task
  assert.equal(h.takeover.phase, 'idle')
  assert.equal(h.takeover.candidate, null)
  assert.equal(h.connectCalls.length, 0)
})

test('S6-T48 confirming 时 canBegin 变假，confirm 不调用 connectTakeover', async (t) => {
  const h = setup(t)
  await h.takeover.prepare()
  assert.equal(h.takeover.phase, 'confirming')
  h.state.canBegin = false
  assert.equal(h.takeover.canConfirm, false)
  await h.takeover.confirm()
  assert.equal(h.connectCalls.length, 0)
  assert.equal(h.takeover.phase, 'idle')
  assert.match(h.takeover.error, /请重新准备接管/)
})

test('S6-T49 GET 返回前结束或准入失效，不进入 confirming', async (t) => {
  for (const patch of [
    { ended: true },
    { canBegin: false },
    { allowed: false },
  ]) {
    const response = deferred()
    const h = setup(t, { fetch: () => response.promise })
    const task = h.takeover.prepare()
    await flush()
    assert.equal(h.takeover.phase, 'loading')
    Object.assign(h.state, patch)
    response.resolve(success(view()))
    await task
    assert.equal(h.takeover.phase, 'idle')
    assert.equal(h.takeover.candidate, null)
    assert.equal(h.takeover.canConfirm, false)
    await h.takeover.confirm()
    assert.equal(h.connectCalls.length, 0)
  }
})
