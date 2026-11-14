import test from 'node:test'
import assert from 'node:assert/strict'
import {
  request,
  confirmation,
  ManagementError,
  onAuthExpired,
} from '../src/api/management.ts'

test('写请求同源 Cookie、结构化错误与登录失效', async (t) => {
  const original = globalThis.fetch
  t.after(() => {
    globalThis.fetch = original
  })
  let expired = 0
  onAuthExpired(() => expired++)
  globalThis.fetch = async (path, options) => {
    assert.equal(path, '/api/servers/id/host-key/confirm')
    assert.equal(options.credentials, 'same-origin')
    assert.deepEqual(JSON.parse(options.body), { probe_id: 'probe-only' })
    return Response.json({
      code: 0,
      message: 'success',
      data: { fingerprint: 'SHA256:test' },
    })
  }
  assert.deepEqual(
    await request('/api/servers/id/host-key/confirm', 'POST', {
      probe_id: 'probe-only',
    }),
    { fingerprint: 'SHA256:test' },
  )
  globalThis.fetch = async () =>
    Response.json(
      { code: 'AUTH_REQUIRED', message: 'expired', data: {} },
      { status: 401 },
    )
  await assert.rejects(request('/api/servers'), { code: 'AUTH_REQUIRED' })
  assert.equal(expired, 1)
  globalThis.fetch = async () =>
    Response.json(
      { code: 'STORAGE_ERROR', message: '退出未完成', data: {} },
      { status: 500 },
    )
  await assert.rejects(request('/api/auth/logout', 'POST', {}), {
    code: 'STORAGE_ERROR',
  })
  assert.equal(expired, 1)
})
test('Host Key 提示只接受完整探测响应，不自动保存信任', () => {
  const p = {
    probe_id: 'a'.repeat(43),
    host: 'localhost',
    port: 22,
    algorithm: 'ssh-ed25519',
    fingerprint: 'SHA256:test',
    expires_at: new Date().toISOString(),
    previous_fingerprint: null,
    reason: 'first_trust',
  }
  assert.deepEqual(
    confirmation(
      new ManagementError('HOST_KEY_CONFIRMATION_REQUIRED', 'confirm', p),
    ),
    p,
  )
  assert.equal(
    confirmation(
      new ManagementError('HOST_KEY_CONFIRMATION_REQUIRED', 'confirm', {
        ...p,
        probe_id: 'bad',
      }),
    ),
    null,
  )
  assert.equal(confirmation(new Error('network')), null)
})
