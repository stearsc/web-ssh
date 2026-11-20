import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { createRenderer, nextTick, reactive } from 'vue'
import { onAuthExpired, request } from '../src/api/management.ts'

const sourceURL = new URL('../src/components/server/ServerForm.vue', import.meta.url)
const { descriptor } = parse(await readFile(sourceURL, 'utf8'))
const compiled = compileScript(descriptor, { id: 'private-key-form-test' })
const moduleURL = source => `data:text/javascript,${encodeURIComponent(source)}`
const code = ts.transpileModule(compiled.content, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText.replace(/from ['"]([^'"]+)['"]/g, (_match, specifier) => {
  let url
  if (specifier.endsWith('.vue')) url = moduleURL('export default { render() { return null } }')
  else if (specifier.startsWith('.')) url = new URL(specifier + (specifier.endsWith('.ts') ? '' : '.ts'), sourceURL).href
  else url = import.meta.resolve(specifier)
  return `from ${JSON.stringify(url)}`
})
const component = (await import(moduleURL(code))).default
component.render = () => null
const renderer = createRenderer({
  createElement: tag => ({ tag, children: [] }), createText: text => ({ text }), createComment: text => ({ text }),
  setText(node, text) { node.text = text }, setElementText(node, text) { node.text = text },
  parentNode: node => node.parent, nextSibling: () => null, patchProp() {},
  insert(node, parent) { node.parent = parent; parent.children.push(node) },
  remove(node) { const parent = node.parent; if (parent) parent.children = parent.children.filter(child => child !== node) },
})
const server = { id: 'a'.repeat(32), name: 'saved', host: 'localhost', port: 22, username: 'tester', auth_type: 'private_key', credential_configured: true, group_name: '', remark: '', host_key: null }
function mount(t, value = null) {
  const emitted = []
  const props = reactive({ server: value, disabled: false, onSaved: value => emitted.push(value) })
  const app = renderer.createApp(component, props)
  app.mount({ children: [] })
  t.after(() => app.unmount())
  return { state: app._instance.setupState, props, emitted, unmount: () => app.unmount() }
}
function mockFetch(t, handler) {
  const original = globalThis.fetch
  globalThis.fetch = handler
  t.after(() => { globalThis.fetch = original })
}
const success = () => Response.json({ code: 0, message: 'success', data: server })

test('私钥上传保留 Cookie/no-store/signal，浏览器生成 boundary，AUTH_REQUIRED 行为不变', async t => {
  const form = new FormData()
  form.append('metadata', new Blob(['{}'], { type: 'application/json' }), '')
  form.append('private_key', new File(['key'], 'key.pem'))
  const signal = new AbortController().signal
  let expired = 0
  onAuthExpired(() => expired++)
  mockFetch(t, async (_path, options) => {
    assert.equal(options.body, form)
    assert.equal(options.headers, undefined)
    assert.equal(options.credentials, 'same-origin')
    assert.equal(options.cache, 'no-store')
    assert.equal(options.signal, signal)
    return Response.json({ code: 'AUTH_REQUIRED', message: 'expired', data: {} }, { status: 401 })
  })
  await assert.rejects(request('/api/servers', 'POST', form, signal), { code: 'AUTH_REQUIRED' })
  assert.equal(expired, 1)
})

test('私钥 metadata 与密码互斥，文件原样，替换文件重新输入口令', async t => {
  const h = mount(t)
  h.state.form.auth_type = 'private_key'
  h.state.changeAuthentication()
  h.state.passphrase = 'old-input'
  const file = new File(['original\r\n'], 'id_ed25519')
  h.state.changeFile({ target: { files: [file], value: 'selected' } })
  assert.equal(h.state.passphrase, '')
  h.state.passphrase = 'new-pass'
  mockFetch(t, async (_path, options) => {
    assert.ok(options.body instanceof FormData)
    const metadataPart = options.body.get('metadata')
    assert.equal(metadataPart.type, 'application/json')
    assert.equal(metadataPart.name, '')
    const metadata = JSON.parse(await metadataPart.text())
    assert.equal(metadata.auth_type, 'private_key')
    assert.equal(metadata.passphrase, 'new-pass')
    assert.equal('password' in metadata, false)
    assert.equal(await options.body.get('private_key').text(), 'original\r\n')
    return success()
  })
  await h.state.save()
  assert.equal(h.emitted.length, 1)
  assert.equal(h.state.passphrase, '')
  assert.equal(h.state.selectedFile, null)
})

test('普通资料编辑发 JSON 并完全省略私钥秘密，dirty 和处理函数拒绝测试草稿', async t => {
  const h = mount(t, server)
  let calls = 0
  h.state.form.name = 'renamed'
  await nextTick()
  mockFetch(t, async (_path, options) => {
    calls++
    const body = JSON.parse(options.body)
    assert.equal(body.auth_type, 'private_key')
    assert.equal('password' in body, false)
    assert.equal('passphrase' in body, false)
    assert.equal('private_key' in body, false)
    return success()
  })
  await h.state.testConnection()
  assert.equal(calls, 0)
  await h.state.save()
  assert.equal(calls, 1)
})

test('测试只发送保存 ID 与空对象，资料变化取消后迟到结果不发布', async t => {
  const h = mount(t, server)
  let finish
  mockFetch(t, async (path, options) => {
    assert.equal(path, `/api/servers/${server.id}/test`)
    assert.deepEqual(JSON.parse(options.body), {})
    return new Promise(resolve => { finish = resolve })
  })
  const pending = h.state.testConnection()
  h.state.form.remark = 'changed'
  await nextTick()
  finish(success())
  await pending
  assert.equal(h.state.testNotice, '')
  assert.equal(h.state.probe, null)
})

test('取消和卸载清理本地文件与口令，超限在上传前拒绝', async t => {
  const h = mount(t, server)
  h.state.changeFile({ target: { files: [new File(['x'.repeat(65537)], 'oversize')], value: '' } })
  h.state.passphrase = 'local secret'
  mockFetch(t, () => { throw new Error('不应发出超限上传') })
  await h.state.save()
  assert.match(h.state.error, /65536/)
  h.unmount()
  assert.equal(h.state.selectedFile, null)
  assert.equal(h.state.passphrase, '')
})
