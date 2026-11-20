import test from 'node:test'
import assert from 'node:assert/strict'
import {
  FONT_STORAGE_KEY,
  FONT_FAMILIES,
  readTerminalFont,
  setTerminalFont,
  terminalFont,
  terminalFontFamily,
} from '../src/terminal/font.ts'

test('字体设置严格校验、存储拒绝降级、持久化与字体白名单', (t) => {
  const original = Object.getOwnPropertyDescriptor(
    globalThis,
    'localStorage',
  )
  let raw = null
  let denied = false
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem(key) {
        assert.equal(key, FONT_STORAGE_KEY)
        if (denied) throw new Error('denied')
        return raw
      },
      setItem(key, value) {
        assert.equal(key, FONT_STORAGE_KEY)
        if (denied) throw new Error('denied')
        raw = value
      },
    },
  })
  t.after(() => {
    setTerminalFont(14, 'system')
    if (original)
      Object.defineProperty(globalThis, 'localStorage', original)
    else delete globalThis.localStorage
  })
  for (const value of [
    null,
    'bad',
    '[]',
    'null',
    '{}',
    '{"size":9,"family":"menlo"}',
    '{"size":25,"family":"menlo"}',
    '{"size":14.5,"family":"menlo"}',
    '{"size":"14","family":"menlo"}',
    '{"size":14,"family":"untrusted"}',
  ]) {
    raw = value
    assert.deepEqual(readTerminalFont(), { size: 14, family: 'system' })
  }
  for (const font of FONT_FAMILIES) {
    setTerminalFont(24, font.id)
    assert.equal(terminalFontFamily(), font.css)
    assert.ok(font.css.endsWith('monospace'))
    assert.deepEqual(readTerminalFont(), { size: 24, family: font.id })
  }
  setTerminalFont(10, 'menlo')
  assert.deepEqual(readTerminalFont(), { size: 10, family: 'menlo' })
  setTerminalFont(9, 'monaco')
  setTerminalFont(25, 'monaco')
  setTerminalFont(14, 'unknown')
  assert.equal(terminalFont.size, 10)
  assert.equal(terminalFont.family, 'menlo')
  denied = true
  assert.deepEqual(readTerminalFont(), { size: 14, family: 'system' })
  setTerminalFont(18, 'consolas')
  assert.equal(terminalFont.size, 18)
  assert.equal(terminalFontFamily(), 'Consolas, monospace')
})
