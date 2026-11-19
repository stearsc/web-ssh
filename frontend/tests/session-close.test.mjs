import test from 'node:test'
import assert from 'node:assert/strict'
import { CloseCoordinator } from '../src/session/close.ts'
import { onAuthExpired, request } from '../src/api/management.ts'

const ID = 'a'.repeat(32)
const OTHER = 'b'.repeat(32)

const flush = async () => {
  for (let i = 0; i < 20; i++) await Promise.resolve()
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

function envelope(code, message = 'ok', data = {}, status = 200) {
  return Response.json({ code, message, data }, { status })
}

function detail(status = 'CONNECTED') {
  const disconnected = status === 'DISCONNECTED'
  return Response.json({
    code: 0,
    message: 'ok',
    data: {
      id: ID,
      server_id: OTHER,
      name: 'session',
      host: 'example.test',
      port: 22,
      username: 'operator',
      status,
      binding_generation: 1,
      current_cols: 80,
      current_rows: 24,
      created_at: '2026-11-19T00:00:00Z',
      disconnected_at: disconnected ? '2026-11-19T00:01:00Z' : null,
      last_attached_at: null,
      offline_expires_at: disconnected ? '2026-11-19T00:06:00Z' : null,
      remaining_seconds: disconnected ? 300 : null,
    },
  })
}

function installFetch(t, handler) {
  const original = globalThis.fetch
  globalThis.fetch = handler
  t.after(() => {
    globalThis.fetch = original
  })
}

test('S7-C01/C06 退订不取消 DELETE，迟到响应仍更新协调器但不通知旧订阅', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  const second = coordinator.handle(ID)
  const pending = deferred()
  let deletes = 0
  installFetch(t, async (_path, init = {}) => {
    if (init.method === 'DELETE') {
      deletes++
      return pending.promise
    }
    throw new Error('unexpected request')
  })

  let calls = 0
  const unsubscribe = handle.subscribe(() => {
    calls++
  })
  const first = handle.requestClose()
  const same = second.requestClose()
  assert.equal(first, same)
  assert.equal(handle.snapshot().state, 'pending')
  unsubscribe()
  const afterUnsubscribe = calls

  pending.resolve(envelope(0))
  await first
  assert.equal(deletes, 1)
  assert.equal(handle.snapshot().state, 'requested')
  assert.equal(calls, afterUnsubscribe)
})

test('S7-C02 pending 取消等待进入 unknown，Pane 退订后状态保留', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  const pending = deferred()
  installFetch(t, async (_path, init = {}) => {
    if (init.method === 'DELETE') return pending.promise
    throw new Error('unexpected request')
  })
  const unsubscribe = handle.subscribe(() => {})
  const closing = handle.requestClose()
  handle.cancelWaiting()
  assert.equal(handle.snapshot().state, 'unknown')
  assert.match(handle.snapshot().message, /已停止等待/)
  unsubscribe()

  // 服务端实际可能已接受；迟到 200 不得把 unknown 改成 requested。
  pending.resolve(envelope(0))
  await closing
  assert.equal(handle.snapshot().state, 'unknown')
})

test('S7-C05 unknown 重试幂等：只替换唯一 DELETE，200 后 requested', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  let deletes = 0
  let succeed = false
  installFetch(t, async (_path, init = {}) => {
    if (init.method !== 'DELETE') throw new Error('unexpected request')
    deletes++
    if (!succeed) throw new TypeError('network down')
    return envelope(0)
  })

  await handle.requestClose()
  assert.equal(handle.snapshot().state, 'unknown')
  succeed = true
  await handle.retryClose()
  assert.equal(deletes, 2)
  assert.equal(handle.snapshot().state, 'requested')
  // 非 idle 状态下重复调用只返回当前快照，不会产生第二个请求。
  const pending = handle.requestClose()
  const again = handle.requestClose()
  assert.equal((await pending).state, 'requested')
  assert.equal((await again).state, 'requested')
  assert.equal(deletes, 2)
})

test('S7-C07 旧 DELETE 迟到被 epoch 丢弃，新任务状态不被覆盖', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  const first = deferred()
  const second = deferred()
  let deletes = 0
  installFetch(t, async (_path, init = {}) => {
    if (init.method !== 'DELETE') throw new Error('unexpected request')
    deletes++
    return deletes === 1 ? first.promise : second.promise
  })

  const firstClosing = handle.requestClose()
  handle.cancelWaiting()
  const secondClosing = handle.retryClose()
  assert.equal(handle.snapshot().state, 'pending')

  first.resolve(envelope(0))
  await flush()
  assert.equal(handle.snapshot().state, 'pending')

  second.resolve(envelope(0))
  await Promise.all([firstClosing, secondClosing])
  assert.equal(handle.snapshot().state, 'requested')
})

test('S7-C10 不同 Session 使用独立 CloseTask 与 DELETE', async (t) => {
  const coordinator = new CloseCoordinator()
  const a = coordinator.handle(ID)
  const b = coordinator.handle(OTHER)
  const pendingA = deferred()
  const pendingB = deferred()
  installFetch(t, async (path, init = {}) => {
    if (init.method !== 'DELETE') throw new Error('unexpected request')
    return path.endsWith(ID) ? pendingA.promise : pendingB.promise
  })

  const closingA = a.requestClose()
  const closingB = b.requestClose()
  assert.equal(a.snapshot().state, 'pending')
  assert.equal(b.snapshot().state, 'pending')
  pendingA.resolve(envelope(0))
  await closingA
  assert.equal(a.snapshot().state, 'requested')
  assert.equal(b.snapshot().state, 'pending')
  pendingB.resolve(envelope(0))
  await closingB
  assert.equal(b.snapshot().state, 'requested')
})

test('S7-C15 dispose 幂等：abort 本地等待、阻止迟到发布、退订后不再回调', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  const pending = deferred()
  let signal
  installFetch(t, async (_path, init = {}) => {
    if (init.method !== 'DELETE') throw new Error('unexpected request')
    signal = init.signal
    return pending.promise
  })
  const closing = handle.requestClose()
  coordinator.dispose()
  coordinator.dispose()
  assert.equal(handle.snapshot().disposed, true)
  assert.equal(signal.aborted, true)

  let calls = 0
  handle.subscribe(() => {
    calls++
  })
  assert.equal(calls, 0)

  pending.resolve(envelope(0))
  await closing
  assert.equal(handle.snapshot().state, 'pending')
  assert.equal(handle.snapshot().disposed, true)
})

test('S7-C16 requested 下详情 CONNECTED/DISCONNECTED 保持 requested', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  let status = 'CONNECTED'
  installFetch(t, async (_path, init = {}) => {
    if (init.method === 'DELETE') return envelope(0)
    if (init.method === 'GET') return detail(status)
    throw new Error('unexpected request')
  })
  await handle.requestClose()
  assert.equal(handle.snapshot().state, 'requested')
  await handle.refreshClose()
  assert.equal(handle.snapshot().state, 'requested')
  assert.equal(handle.snapshot().serverStatus, 'CONNECTED')
  status = 'DISCONNECTED'
  await handle.refreshClose()
  assert.equal(handle.snapshot().state, 'requested')
  assert.equal(handle.snapshot().serverStatus, 'DISCONNECTED')
})

test('S7-C17 unknown + CLOSING 升格 requested，仍禁止接管', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  let deleteFails = true
  installFetch(t, async (_path, init = {}) => {
    if (init.method === 'DELETE') {
      if (deleteFails) throw new TypeError('network down')
      return envelope(0)
    }
    if (init.method === 'GET') return detail('CLOSING')
    throw new Error('unexpected request')
  })
  await handle.requestClose()
  assert.equal(handle.snapshot().state, 'unknown')
  await handle.refreshClose()
  assert.equal(handle.snapshot().state, 'requested')
  assert.notEqual(handle.snapshot().state, 'idle')
})

test('S7-C18 详情 GET 在途时重试：GET 被 abort/丢弃，DELETE 唯一', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  let deletes = 0
  let getSignal
  const pendingGet = deferred()
  installFetch(t, async (_path, init = {}) => {
    if (init.method === 'DELETE') {
      deletes++
      if (deletes === 1) throw new TypeError('network down')
      return envelope(0)
    }
    if (init.method === 'GET') {
      getSignal = init.signal
      return pendingGet.promise
    }
    throw new Error('unexpected request')
  })
  await handle.requestClose()
  assert.equal(handle.snapshot().state, 'unknown')

  const refreshing = handle.refreshClose()
  assert.equal(getSignal.aborted, false)
  const retrying = handle.retryClose()
  assert.equal(getSignal.aborted, true)
  assert.equal(deletes, 2)
  await retrying
  assert.equal(handle.snapshot().state, 'requested')

  pendingGet.resolve(detail('CLOSING'))
  await refreshing
  assert.equal(handle.snapshot().state, 'requested')
  assert.equal(handle.snapshot().serverStatus, null)
})

test('S7-C22 并发 refresh 只有最后一次发布，旧响应不改 message/updatedAt', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  const firstGet = deferred()
  const secondGet = deferred()
  let gets = 0
  installFetch(t, async (_path, init = {}) => {
    if (init.method === 'DELETE') return envelope(0)
    if (init.method === 'GET') {
      gets++
      return gets === 1 ? firstGet.promise : secondGet.promise
    }
    throw new Error('unexpected request')
  })
  await handle.requestClose()
  assert.equal(handle.snapshot().state, 'requested')

  let notifications = 0
  handle.subscribe(() => {
    notifications++
  })
  const baseline = notifications
  const refreshingFirst = handle.refreshClose()
  const refreshingSecond = handle.refreshClose()
  const before = handle.snapshot()

  firstGet.resolve(detail('CLOSING'))
  await refreshingFirst
  // 旧响应被 queryEpoch 丢弃：不写 message、不更新 updatedAt、不通知订阅者。
  assert.equal(notifications, baseline)
  assert.equal(handle.snapshot().message, before.message)
  assert.equal(handle.snapshot().updatedAt, before.updatedAt)

  secondGet.resolve(detail('CONNECTED'))
  await refreshingSecond
  assert.equal(notifications, baseline + 1)
  assert.equal(handle.snapshot().state, 'requested')
  assert.equal(handle.snapshot().serverStatus, 'CONNECTED')
})

test('S7-C20/C24 本地 stop 同步抛错仍发 DELETE，失败独立记录', async (t) => {
  t.mock.method(console, 'error', () => {})
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  let deletes = 0
  installFetch(t, async (_path, init = {}) => {
    if (init.method !== 'DELETE') throw new Error('unexpected request')
    deletes++
    return envelope(0)
  })
  const unregister = handle.registerLocalStop(() => {
    throw new Error('local stop failed')
  })
  await handle.requestClose()
  assert.equal(deletes, 1)
  assert.equal(handle.snapshot().localStopFailed, true)
  assert.notEqual(handle.snapshot().state, 'idle')
  unregister()
})

test('S7-C25 本地 stop Promise reject 不改变 DELETE 结果', async (t) => {
  t.mock.method(console, 'error', () => {})
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  let rejectStop
  installFetch(t, async (_path, init = {}) => {
    if (init.method !== 'DELETE') throw new Error('unexpected request')
    return envelope(0)
  })
  handle.registerLocalStop(
    () =>
      new Promise((_resolve, reject) => {
        rejectStop = reject
      }),
  )
  const closing = handle.requestClose()
  assert.equal(handle.snapshot().state, 'pending')
  rejectStop(new Error('local stop failed'))
  await flush()
  assert.equal(handle.snapshot().localStopFailed, true)
  await closing
  assert.equal(handle.snapshot().state, 'requested')
})

test('S7-C26 旧注销不影响新注册；S7-C27 重复注销与 dispose 后注册幂等', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  let oldCalls = 0
  let newCalls = 0
  installFetch(t, async (_path, init = {}) => {
    if (init.method !== 'DELETE') throw new Error('unexpected request')
    return envelope(0)
  })
  const unregisterOld = handle.registerLocalStop(async () => {
    oldCalls++
  })
  handle.registerLocalStop(async () => {
    newCalls++
  })
  unregisterOld()
  unregisterOld()
  await handle.requestClose()
  assert.equal(oldCalls, 0)
  assert.equal(newCalls, 1)

  coordinator.dispose()
  let disposedCalls = 0
  const unregisterAfterDispose = handle.registerLocalStop(async () => {
    disposedCalls++
  })
  unregisterAfterDispose()
  assert.equal(disposedCalls, 0)
  let disposedPublications = 0
  const unsubscribeAfterDispose = handle.subscribe(() => disposedPublications++)
  await handle.requestClose()
  await handle.retryClose()
  await handle.refreshClose()
  unsubscribeAfterDispose()
  assert.equal(disposedCalls, 0)
  assert.equal(disposedPublications, 0)
  assert.equal(handle.snapshot().disposed, true)
})

test('S7-C28 ended 只由服务端证据产生：DELETE 200 不置 ended', async (t) => {
  const coordinator = new CloseCoordinator()
  const handle = coordinator.handle(ID)
  installFetch(t, async (_path, init = {}) => {
    if (init.method === 'DELETE') return envelope(0)
    if (init.method === 'GET') return detail('CONNECTED')
    throw new Error('unexpected request')
  })
  await handle.requestClose()
  assert.equal(handle.snapshot().state, 'requested')
  await handle.refreshClose()
  assert.notEqual(handle.snapshot().state, 'ended')

  handle.markEnded()
  assert.equal(handle.snapshot().state, 'ended')
  let requests = 0
  globalThis.fetch = async () => {
    requests++
    return detail('CONNECTED')
  }
  await handle.refreshClose()
  assert.equal(requests, 0)
  assert.equal(handle.snapshot().state, 'ended')
})

test('S7-C29 双重准入矩阵：详情不解除关闭意图，NOT_FOUND 进入 ended', async (t) => {
  const scenarios = [
    ['requested', 'CONNECTED', 'requested'],
    ['requested', 'DISCONNECTED', 'requested'],
    ['requested', 'CLOSING', 'requested'],
    ['unknown', 'CONNECTED', 'unknown'],
    ['unknown', 'DISCONNECTED', 'unknown'],
    ['unknown', 'CLOSING', 'requested'],
    ['requested', 'NOT_FOUND', 'ended'],
    ['unknown', 'NOT_FOUND', 'ended'],
  ]
  for (const [initial, status, expected] of scenarios) {
    await t.test(`${initial} + ${status} -> ${expected}`, async (t) => {
      const coordinator = new CloseCoordinator()
      const handle = coordinator.handle(ID)
      let deleteFails = initial === 'unknown'
      installFetch(t, async (_path, init = {}) => {
        if (init.method === 'DELETE') {
          if (deleteFails) throw new TypeError('network down')
          return envelope(0)
        }
        if (init.method === 'GET') {
          if (status === 'NOT_FOUND')
            return envelope('SESSION_NOT_FOUND', '不存在', {}, 404)
          return detail(status)
        }
        throw new Error('unexpected request')
      })
      await handle.requestClose()
      assert.equal(handle.snapshot().state, initial)
      await handle.refreshClose()
      assert.equal(handle.snapshot().state, expected)
      if (expected === 'ended')
        assert.match(handle.snapshot().message, /已不可接管/)
    })
  }

  // idle 不发起关闭详情查询；S6 Pane 详情由 Pane 自己负责。
  const coordinator = new CloseCoordinator()
  let requests = 0
  installFetch(t, async () => {
    requests++
    return detail('CLOSING')
  })
  const handle = coordinator.handle(ID)
  await handle.refreshClose()
  assert.equal(requests, 0)
  assert.equal(handle.snapshot().state, 'idle')
})

test('S7-C30 DELETE 分类：除合法 200 {code:0} 外全部 unknown，不产生 failed', async (t) => {
  const cases = [
    ['合法 200', () => envelope(0), 'requested'],
    ['HTTP 201 + code 0', () => envelope(0, 'ok', {}, 201), 'unknown'],
    ['HTTP 202 + code 0', () => envelope(0, 'ok', {}, 202), 'unknown'],
    ['HTTP 203 + code 0', () => envelope(0, 'ok', {}, 203), 'unknown'],
    ['HTTP 204 无响应体', () => new Response(null, { status: 204 }), 'unknown'],
    [
      'HTTP 200 非法响应结构',
      () => Response.json({ code: 0 }, { status: 200 }),
      'unknown',
    ],
    [
      '代理同形 AUTH_REQUIRED',
      () => envelope('AUTH_REQUIRED', '需要登录', {}, 401),
      'unknown',
    ],
    [
      '代理同形 INVALID_REQUEST',
      () => envelope('INVALID_REQUEST', '参数错误', {}, 400),
      'unknown',
    ],
    [
      '代理同形 MESSAGE_TOO_LARGE',
      () => envelope('MESSAGE_TOO_LARGE', '过大', {}, 413),
      'unknown',
    ],
    [
      '代理同形 PERMISSION_DENIED',
      () => envelope('PERMISSION_DENIED', '拒绝', {}, 403),
      'unknown',
    ],
    [
      '代理同形 SERVER_SHUTTING_DOWN',
      () => envelope('SERVER_SHUTTING_DOWN', '关闭中', {}, 503),
      'unknown',
    ],
    [
      '未知业务码',
      () => envelope('SOMETHING_NEW', '未知', {}, 200),
      'unknown',
    ],
    [
      '5xx',
      () => envelope('INTERNAL_ERROR', '内部错误', {}, 500),
      'unknown',
    ],
    ['非 JSON 网关错误', () => new Response('bad gateway', { status: 502 }), 'unknown'],
    [
      '网络错误',
      () => {
        throw new TypeError('network down')
      },
      'unknown',
    ],
    [
      '超时/abort',
      () => {
        throw new DOMException('aborted', 'AbortError')
      },
      'unknown',
    ],
  ]
  let expired = 0
  onAuthExpired(() => {
    expired++
  })
  t.after(() => onAuthExpired(() => {}))

  for (const [name, response, expected] of cases) {
    await t.test(name, async (t) => {
      const coordinator = new CloseCoordinator()
      const handle = coordinator.handle(ID)
      installFetch(t, async (_path, init = {}) => {
        if (init.method !== 'DELETE') throw new Error('unexpected request')
        return response()
      })
      await handle.requestClose()
      assert.equal(handle.snapshot().state, expected)
      assert.notEqual(handle.snapshot().state, 'failed')
      assert.notEqual(handle.snapshot().state, 'idle')
    })
  }
  // AUTH_REQUIRED 保留既有全局副作用，但不参与 failed 分类。
  assert.equal(expired, 1)
})

test('通用 request 保持 response.ok 语义：其他 2xx 仍按原行为成功', async (t) => {
  installFetch(t, async () =>
    Response.json({ code: 0, message: 'ok', data: { value: 1 } }, { status: 201 }),
  )
  assert.deepEqual(await request('/api/anything', 'POST', {}), { value: 1 })
})


test('S7-6/C29 requested 与 unknown 的 CONNECTING、非法详情和网络失败保留关闭意图', async (t) => {
  for (const initial of ['requested', 'unknown']) {
    for (const result of ['CONNECTING', 'invalid', 'network']) {
      await t.test(`${initial} + ${result}`, async (t) => {
        const coordinator = new CloseCoordinator()
        t.after(() => coordinator.dispose())
        const handle = coordinator.handle(ID)
        const requests = []
        installFetch(t, async (path, init) => {
          requests.push({ path, method: init.method, body: init.body })
          if (init.method === 'DELETE') {
            if (initial === 'unknown') throw new TypeError('response lost')
            return envelope(0)
          }
          if (result === 'network') throw new TypeError('detail unavailable')
          if (result === 'invalid') return envelope(0, 'ok', { status: 'SURPRISE' })
          return detail('CONNECTING')
        })
        await handle.requestClose()
        await handle.refreshClose()
        assert.equal(handle.snapshot().state, initial)
        assert.equal(handle.snapshot().serverStatus, result === 'CONNECTING' ? 'CONNECTING' : null)
        assert.deepEqual(requests.map(r => [r.method, r.path]), [
          ['DELETE', `/api/sessions/${ID}`], ['GET', `/api/sessions/${ID}`],
        ])
        assert.equal(requests[0].body, '{}')
        assert.notEqual(handle.snapshot().state, 'idle')
        assert.notEqual(handle.snapshot().state, 'failed')
      })
    }
  }
})

test('S7-6/C07/C18/C22 ended 抢占在途重试，旧 DELETE 和详情均零迟到发布', async (t) => {
  const coordinator = new CloseCoordinator()
  t.after(() => coordinator.dispose())
  const handle = coordinator.handle(ID)
  const query = deferred(), retry = deferred()
  const requests = []
  installFetch(t, async (path, init) => {
    requests.push({ path, ...init })
    if (init.method === 'GET') return query.promise
    return requests.length === 1 ? envelope(0) : retry.promise
  })
  const published = []
  handle.subscribe(snapshot => published.push(snapshot))
  await handle.requestClose()
  const refreshing = handle.refreshClose()
  const retrying = handle.retryClose()
  assert.equal(requests[1].signal.aborted, true)
  assert.equal(handle.snapshot().state, 'pending')
  handle.markEnded()
  handle.markEnded()
  const ended = handle.snapshot(), publications = published.length
  assert.equal(requests[2].signal.aborted, true)
  query.reject(new TypeError('late query rejection'))
  retry.resolve(envelope(0))
  await Promise.all([refreshing, retrying])
  await flush()
  assert.deepEqual(handle.snapshot(), ended)
  assert.equal(published.length, publications)
  assert.equal(published.filter(s => s.state === 'ended').length, 1)
  assert.deepEqual(requests.map(r => r.method), ['DELETE', 'GET', 'DELETE'])
  await handle.requestClose()
  await handle.retryClose()
  await handle.refreshClose()
  assert.equal(requests.length, 3)
})
