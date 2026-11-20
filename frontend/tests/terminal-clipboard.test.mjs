import test from 'node:test'
import assert from 'node:assert/strict'
import {
  clipboardKeyHandler,
  copyText,
} from '../src/terminal/clipboard.ts'

function key(overrides = {}) {
  return {
    type: 'keydown',
    key: 'C',
    ctrlKey: true,
    shiftKey: true,
    altKey: false,
    metaKey: false,
    repeat: false,
    prevented: 0,
    stopped: 0,
    preventDefault() {
      this.prevented++
    },
    stopPropagation() {
      this.stopped++
    },
    ...overrides,
  }
}
test('Ctrl+Shift+C 有无选区都取消页面事件，仅首次 keydown 复制；Ctrl+C 和 macOS 不拦截', () => {
  let copies = 0
  let selection = '中文\noutput'
  const handler = clipboardKeyHandler(false, () => {
    if (selection) copies++
  })
  for (const hasSelection of [true, false]) {
    selection = hasSelection ? 'text' : ''
    const event = key()
    const before = copies
    assert.equal(handler(event), false)
    assert.equal(event.prevented, 1)
    assert.equal(event.stopped, 1)
    assert.equal(copies - before, hasSelection ? 1 : 0)
    for (const overrides of [
      { repeat: true },
      { type: 'keypress' },
      { type: 'keyup' },
    ]) {
      const ignored = key(overrides)
      assert.equal(handler(ignored), false)
      assert.equal(ignored.prevented, 1)
    }
    assert.equal(copies - before, hasSelection ? 1 : 0)
  }
  for (const overrides of [
    { shiftKey: false },
    { altKey: true },
    { metaKey: true },
    { key: 'v' },
  ]) {
    const event = key(overrides)
    assert.equal(handler(event), true)
    assert.equal(event.prevented, 0)
  }
  const mac = key({ ctrlKey: false, shiftKey: false, metaKey: true })
  assert.equal(
    clipboardKeyHandler(true, () => {
      throw new Error('macOS 不应自定义复制')
    })(mac),
    true,
  )
  assert.equal(mac.prevented, 0)
})

function replaceGlobal(t, name, value) {
  const original = Object.getOwnPropertyDescriptor(globalThis, name)
  Object.defineProperty(globalThis, name, { configurable: true, value })
  t.after(() => {
    if (original) Object.defineProperty(globalThis, name, original)
    else delete globalThis[name]
  })
}
test('Clipboard API 成功只写一次，异步拒绝不降级', async (t) => {
  const values = []
  replaceGlobal(t, 'navigator', {
    clipboard: {
      writeText: async (text) => {
        values.push(text)
      },
    },
  })
  await copyText('中文\n\noutput')
  assert.deepEqual(values, ['中文\n\noutput'])
  let fallback = 0
  replaceGlobal(t, 'document', {
    createElement() {
      fallback++
      throw new Error('不应降级')
    },
  })
  navigator.clipboard.writeText = async () => {
    throw new Error('denied')
  }
  await assert.rejects(copyText('text'), /denied/)
  assert.equal(fallback, 0)
})

test('API 不可用时独立降级，成功或失败均移除临时节点并恢复焦点', async (t) => {
  replaceGlobal(t, 'navigator', {})
  class Element {
    isConnected = true
    focus() {
      this.focused = true
    }
  }
  replaceGlobal(t, 'HTMLElement', Element)
  const previous = new Element()
  let success = true
  const fields = []
  replaceGlobal(t, 'document', {
    activeElement: previous,
    body: {
      appendChild(field) {
        field.appended = true
      },
    },
    createElement(name) {
      assert.equal(name, 'textarea')
      const field = {
        style: {},
        setAttribute() {},
        select() {
          this.selected = true
        },
        remove() {
          this.removed = true
        },
      }
      fields.push(field)
      return field
    },
    execCommand(command) {
      assert.equal(command, 'copy')
      return success
    },
  })
  await copyText('original text')
  assert.equal(fields[0].value, 'original text')
  assert.equal(fields[0].selected, true)
  assert.equal(fields[0].removed, true)
  assert.equal(previous.focused, true)
  success = false
  await assert.rejects(copyText('text'), /浏览器未允许复制/)
  assert.equal(fields[1].removed, true)
})
