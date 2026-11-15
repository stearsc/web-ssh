import test from 'node:test'
import assert from 'node:assert/strict'
import {
  identity,
  logoutPending,
  acceptIdentity,
  clearIdentity,
  logout,
  checkIdentity,
  expireIdentity,
} from '../src/stores/auth.ts'
const view = { expires_at: '2099-01-01T00:00:00.000Z', client_settings: {} }
test('登出串行、失败保留退出意图，AUTH_REQUIRED 和晚到 me 不覆盖；成功才清身份', async (t) => {
  const original = globalThis.fetch
  t.after(() => {
    globalThis.fetch = original
    clearIdentity()
    logoutPending.value = false
  })
  for (const code of ['STORAGE_ERROR', 'SERVER_UNAVAILABLE', 'network']) {
    logoutPending.value = false
    acceptIdentity(view)
    let resolve,
      reject,
      calls = 0
    globalThis.fetch = () => {
      calls++
      return new Promise((a, b) => {
        resolve = a
        reject = b
      })
    }
    const first = logout(),
      second = logout()
    assert.equal(first, second)
    assert.equal(calls, 1)
    assert.equal(logoutPending.value, true)
    assert.equal(expireIdentity(), false)
    assert.notEqual(identity.value, null)
    if (code === 'network') reject(new Error('network'))
    else
      resolve({
        ok: false,
        json: async () => ({ code, message: code, data: {} }),
      })
    await assert.rejects(first)
    assert.equal(logoutPending.value, true)
    assert.notEqual(identity.value, null)
    globalThis.fetch = async () => ({
      ok: true,
      json: async () => ({ code: 0, message: 'ok', data: {} }),
    })
    await logout()
    assert.equal(identity.value, null)
    assert.equal(logoutPending.value, false)
  }
  let late
  globalThis.fetch = () =>
    new Promise((resolve) => {
      late = resolve
    })
  const checking = checkIdentity()
  globalThis.fetch = async () => ({
    ok: true,
    json: async () => ({ code: 0, message: 'ok', data: {} }),
  })
  await logout()
  late({
    ok: true,
    json: async () => ({ code: 0, message: 'ok', data: view }),
  })
  await checking
  assert.equal(identity.value, null)
})
