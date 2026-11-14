import test from 'node:test'
import assert from 'node:assert/strict'
import { ApiError, getHealth } from '../src/api/client.ts'

test('健康响应、错误码和取消信号', async (t) => {
  const original = globalThis.fetch
  t.after(() => { globalThis.fetch = original })
  const controller = new AbortController()
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/health')
    assert.equal(options.signal, controller.signal)
    assert.equal(options.credentials, 'same-origin')
    return Response.json({ code: 0, message: 'success', data: { status: 'ready' } })
  }
  assert.deepEqual(await getHealth(controller.signal), { status: 'ready' })
  globalThis.fetch = async () => Response.json({ code: 'SERVER_UNAVAILABLE', message: '服务尚未就绪', data: { status: 'not_ready' } }, { status: 503 })
  await assert.rejects(getHealth(controller.signal), (error) => error instanceof ApiError && error.code === 'SERVER_UNAVAILABLE' && error.message === '服务尚未就绪')
  globalThis.fetch = async () => Response.json({ code: 0, message: 'success', data: [] })
  await assert.rejects(getHealth(controller.signal), { code: 'INVALID_RESPONSE' })
})
