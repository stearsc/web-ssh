import test from 'node:test'
import assert from 'node:assert/strict'
import { UploadQueue } from '../src/files/upload.ts'
import { ApiError } from '../src/api/client.ts'
import {
  MAX_UPLOAD_SIZE,
  SFTP_CANCEL_MESSAGE,
  uploadFile,
} from '../src/api/files.ts'
const deferred = () => {
  let resolve, reject
  const promise = new Promise((a, b) => {
    resolve = a
    reject = b
  })
  return { promise, resolve, reject }
}
const file = (name) => new File(['content'], name)
const flush = async () => {
  for (let i = 0; i < 30; i++) await Promise.resolve()
}
const id = 'a'.repeat(32)
test('顺序、普通失败继续、逐文件覆盖及跳过、100% 不提前完成', async () => {
  const calls = [],
    gate = deferred()
  const queue = new UploadQueue(id, () => {}, {
    check: async (_id, dir, name) => {
      calls.push(['check', _id, dir, name])
      return {
        exists: ['skip', 'replace'].includes(name),
        path: `${dir}/${name}`,
      }
    },
    upload: async (_id, dir, f, overwrite, signal, progress) => {
      calls.push(['upload', _id, dir, f.name, overwrite])
      progress(100)
      if (f.name === 'bad') throw new ApiError('SFTP_FAILED', 'write failed')
      if (f.name === 'last') await gate.promise
      return { path: `${dir}/${f.name}`, size: f.size }
    },
  })
  queue.setDirectory('/remote')
  queue.add(['bad', 'skip', 'replace', 'last'].map(file))
  const task = queue.start()
  await flush()
  assert.equal(queue.confirming.file.name, 'skip')
  queue.confirm(false)
  await flush()
  assert.equal(queue.confirming.file.name, 'replace')
  queue.confirm(true)
  await flush()
  assert.equal(queue.items.at(-1).progress, 100)
  assert.equal(queue.items.at(-1).state, 'uploading')
  gate.resolve()
  await task
  assert.deepEqual(
    queue.items.map((i) => i.state),
    ['failed', 'skipped', 'done', 'done'],
  )
  assert.deepEqual(
    calls.filter((c) => c[0] === 'upload').map((c) => [c[3], c[4]]),
    [
      ['bad', false],
      ['replace', true],
      ['last', false],
    ],
  )
  assert.ok(calls.every((c) => c[1] === id && c[2] === '/remote'))
})
test('取消当前等待完成屏障再处理下一项', async () => {
  const barrier = deferred(),
    entered = deferred(),
    order = []
  let aborted = false
  const queue = new UploadQueue(id, () => {}, {
    check: async (_id, dir, name) => {
      order.push(`check:${name}`)
      if (aborted && name === 'first') await barrier.promise
      return { exists: false, path: `${dir}/${name}` }
    },
    upload: async (_id, dir, f, _overwrite, signal) => {
      order.push(`upload:${f.name}`)
      if (f.name === 'first') {
        entered.resolve()
        await new Promise((_resolve, reject) =>
          signal.addEventListener('abort', () => {
            aborted = true
            reject(new DOMException('cancel', 'AbortError'))
          }),
        )
      }
      return { path: `${dir}/${f.name}`, size: f.size }
    },
  })
  queue.setDirectory('/remote')
  queue.add(['first', 'second'].map(file))
  const task = queue.start()
  await entered.promise
  queue.cancelCurrent()
  await flush()
  assert.equal(order.includes('check:second'), false)
  barrier.resolve()
  await task
  assert.deepEqual(
    queue.items.map((i) => i.state),
    ['canceled', 'done'],
  )
})
test('取消队列解除覆盖确认、不发送上传', async () => {
  const queue = new UploadQueue(id, () => {}, {
    check: async () => ({ exists: true, path: '/remote/x' }),
    upload: async () => {
      assert.fail('不应上传')
    },
  })
  queue.setDirectory('/remote')
  queue.add(['x', 'y'].map(file))
  const task = queue.start()
  await flush()
  assert.ok(queue.confirming)
  await queue.cancelAll()
  await task
  assert.deepEqual(
    queue.items.map((i) => i.state),
    ['canceled', 'canceled'],
  )
  assert.equal(queue.pending, false)
})
test('兜底当前失败并取消余项、其他 Session 不受影响', async () => {
  const queue = new UploadQueue(id, () => {}, {
    check: async () => ({ exists: false, path: '/remote/x' }),
    upload: async () => {
      throw new ApiError('SFTP_CANCEL_FAILED', SFTP_CANCEL_MESSAGE)
    },
  })
  const other = new UploadQueue('b'.repeat(32), () => {})
  other.add([file('other')])
  queue.setDirectory('/remote')
  queue.add(['x', 'y'].map(file))
  await queue.start()
  assert.deepEqual(
    queue.items.map((i) => i.state),
    ['failed', 'canceled'],
  )
  assert.equal(queue.notice, SFTP_CANCEL_MESSAGE)
  assert.equal(other.items[0].state, 'waiting')
})
test('abort 后从完成屏障读取异常回执', async () => {
  const entered = deferred()
  let canceled = false
  const queue = new UploadQueue(id, () => {}, {
    check: async () => {
      if (canceled)
        throw new ApiError('SFTP_CANCEL_FAILED', SFTP_CANCEL_MESSAGE)
      return { exists: false, path: '/remote/x' }
    },
    upload: async (_id, _dir, _f, _overwrite, signal) => {
      entered.resolve()
      return new Promise((_resolve, reject) =>
        signal.addEventListener('abort', () => {
          canceled = true
          reject(new DOMException('cancel', 'AbortError'))
        }),
      )
    },
  })
  queue.setDirectory('/remote')
  queue.add(['x', 'y'].map(file))
  const task = queue.start()
  await entered.promise
  queue.cancelCurrent()
  await task
  assert.deepEqual(
    queue.items.map((i) => i.state),
    ['failed', 'canceled'],
  )
  assert.equal(queue.notice, SFTP_CANCEL_MESSAGE)
})
test('Session 关闭终止队列；累计大小不限、单文件独立检查', async () => {
  const queue = new UploadQueue(id, () => {})
  queue.add([
    { name: 'a', size: MAX_UPLOAD_SIZE },
    { name: 'b', size: MAX_UPLOAD_SIZE },
    { name: 'too-big', size: MAX_UPLOAD_SIZE + 1 },
  ])
  assert.deepEqual(
    queue.items.map((i) => i.state),
    ['waiting', 'waiting', 'failed'],
  )
  await queue.stopSession()
  assert.deepEqual(
    queue.items.map((i) => i.state),
    ['canceled', 'canceled', 'failed'],
  )
  await queue.start()
  assert.equal(queue.running, false)
})
test('XHR 字段顺序、HTTP 100% 等待成功、abort 注销监听', async () => {
  const original = globalThis.XMLHttpRequest,
    instances = []
  class XHR {
    upload = {}
    status = 200
    responseText = ''
    withCredentials = false
    constructor() {
      instances.push(this)
    }
    open(method, url) {
      this.method = method
      this.url = url
    }
    send(form) {
      this.form = form
    }
    abort() {
      this.onabort?.()
    }
  }
  globalThis.XMLHttpRequest = XHR
  try {
    const controller = new AbortController(),
      local = file('a'),
      progress = []
    let completed = false
    const task = uploadFile(
      id,
      '/remote',
      local,
      true,
      controller.signal,
      (p) => progress.push(p),
    ).then((v) => {
      completed = true
      return v
    })
    const xhr = instances[0]
    assert.equal(xhr.withCredentials, true)
    assert.deepEqual([...xhr.form.keys()], ['directory', 'overwrite', 'file'])
    xhr.upload.onprogress({ lengthComputable: true, loaded: 100, total: 100 })
    await flush()
    assert.equal(completed, false)
    assert.deepEqual(progress, [100])
    xhr.responseText = JSON.stringify({
      code: 0,
      message: 'success',
      data: { path: '/remote/a', size: local.size },
    })
    xhr.onload()
    await task
    assert.equal(xhr.onload, null)
    assert.equal(xhr.upload.onprogress, null)
    const canceled = new AbortController(),
      next = uploadFile(
        id,
        '/remote',
        local,
        false,
        canceled.signal,
        () => {},
      )
    canceled.abort()
    await assert.rejects(next, { name: 'AbortError' })
  } finally {
    globalThis.XMLHttpRequest = original
  }
})
