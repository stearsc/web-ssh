// 使用真实 Go/SQLite/SSH/WS；Playwright 由执行环境提供，不加入产品依赖。
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises'
import { createServer as createNetServer } from 'node:net'
import { fileURLToPath, pathToFileURL } from 'node:url'
import path from 'node:path'
import { createServer } from 'vite'

const frontend = fileURLToPath(new URL('../..', import.meta.url))
const root = path.dirname(frontend)
const outputRoot = process.env.WEB_SSH_6K_BROWSER_OUTPUT ?? '/tmp/codex/web-ssh-6k/browser'
await mkdir(outputRoot, { recursive: true, mode: 0o700 })
const output = await mkdtemp(path.join(outputRoot, 'run-'))
process.env.TMPDIR = output
console.log(`浏览器证据目录：${output}`)
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : 'playwright')
const reservation = createNetServer()
await new Promise(resolve => reservation.listen(0, '127.0.0.1', resolve))
const port = reservation.address().port
await new Promise(resolve => reservation.close(resolve))
const origin = `http://127.0.0.1:${port}`
let fixtureLog = ''
const fixture = spawn('go', ['test', './internal/httpapi', '-run', '^TestStage6KBrowserFixture$', '-count=1', '-timeout=5m'], {
  cwd: path.join(root, 'backend'),
  env: { ...process.env, TMPDIR: output, WEB_SSH_6K_BROWSER_FIXTURE: '1', WEB_SSH_6K_FIXTURE_DIR: output, WEB_SSH_6K_ORIGIN: origin },
})
fixture.stdout.on('data', chunk => { fixtureLog += chunk })
fixture.stderr.on('data', chunk => { fixtureLog += chunk })
const fixtureExit = new Promise(resolve => fixture.on('exit', code => resolve(code)))
let vite, browser
const results = []
async function check(name, action) { await action(); results.push({ name, passed: true }) }
try {
  let data
  for (let i = 0; i < 600; i++) {
    if (fixture.exitCode !== null) throw new Error(`Go fixture failed: ${fixtureLog}`)
    try { data = JSON.parse(await readFile(path.join(output, 'fixture.json'), 'utf8')); break } catch {}
    await new Promise(resolve => setTimeout(resolve, 100))
  }
  assert.ok(data, 'Go fixture 启动超时')
  vite = await createServer({ root: frontend, configFile: path.join(frontend, 'vite.config.ts'), server: {
    port, strictPort: true, host: '127.0.0.1', proxy: { '/api': data.url, '/ws': { target: data.url, ws: true } },
  } })
  await vite.listen()
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true })
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  const savedForm = () => page.getByRole('dialog', { name: '编辑服务器', exact: true })
  const waitClosed = async () => { await page.getByRole('dialog').waitFor({ state: 'hidden' }) }
  async function edit() { await page.getByRole('button', { name: '编辑', exact: true }).click(); await savedForm().waitFor() }
  await check('Chrome 登录与原生 multipart 加密私钥创建', async () => {
    await page.goto(origin)
    await page.getByRole('textbox', { name: '管理密码' }).fill('browser-admin-password')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await page.getByRole('banner').getByRole('button', { name: '添加服务器', exact: true }).click()
    const form = page.getByRole('dialog', { name: '添加服务器', exact: true })
    await form.getByLabel('名称', { exact: true }).fill('6-K browser')
    await form.getByLabel('Host', { exact: true }).fill('127.0.0.1')
    await form.getByLabel('Port', { exact: true }).fill(String(data.port))
    await form.getByLabel('Username', { exact: true }).fill('tester')
    await form.getByLabel('认证方式').selectOption('private_key')
    await form.getByLabel('私钥文件', { exact: true }).setInputFiles(data.upload)
    await form.getByLabel('Passphrase（可选）').fill('browser-test-passphrase')
    await form.getByRole('button', { name: '保存服务器', exact: true }).click()
    await waitClosed()
    await page.getByRole('heading', { name: '6-K browser', exact: true }).waitFor()
  })
  await check('已保存凭据测试、主机信任确认、真实 SSH 公钥认证', async () => {
    await edit()
    await savedForm().getByRole('button', { name: '测试连接', exact: true }).click()
    const trust = page.getByRole('dialog', { name: '首次信任 SSH 主机', exact: true })
    await trust.getByRole('button', { name: '确认信任并测试', exact: true }).click()
    await savedForm().getByRole('status').filter({ hasText: 'SSH 连接测试成功' }).waitFor()
    assert.equal(await page.locator('dialog').evaluateAll(dialogs => new Set(dialogs.map(d => d.getAttribute('aria-labelledby'))).size), await page.locator('dialog').count())
    await page.screenshot({ path: path.join(output, 'desktop-key-test.png') })
    await savedForm().getByRole('button', { name: '取消', exact: true }).click()
  })
  await check('普通资料编辑保留凭据、错误替换保护旧配置', async () => {
    await edit()
    await savedForm().getByLabel('备注', { exact: true }).fill('保留原凭据')
    assert.equal(await savedForm().getByRole('button', { name: '测试连接', exact: true }).isDisabled(), true)
    await savedForm().getByRole('button', { name: '保存服务器', exact: true }).click()
    await waitClosed()
    await edit()
    await savedForm().getByLabel('私钥文件', { exact: true }).setInputFiles(data.upload)
    await savedForm().getByLabel('Passphrase（可选）').fill('wrong')
    await savedForm().getByLabel('私钥文件', { exact: true }).setInputFiles(data.upload)
    assert.equal(await savedForm().getByLabel('Passphrase（可选）').inputValue(), '')
    await savedForm().getByLabel('Passphrase（可选）').fill('wrong')
    await savedForm().getByRole('button', { name: '保存服务器', exact: true }).click()
    await savedForm().getByRole('alert').waitFor()
    assert.doesNotMatch(await savedForm().getByRole('alert').innerText(), /wrong|BEGIN|browser-test-passphrase/)
    await savedForm().getByRole('button', { name: '取消', exact: true }).click()
    await edit()
    await savedForm().getByRole('button', { name: '测试连接', exact: true }).click()
    await savedForm().getByRole('status').filter({ hasText: 'SSH 连接测试成功' }).waitFor()
    await savedForm().getByRole('button', { name: '取消', exact: true }).click()
  })
  await check('真实 Session/WS/SSH 终端连接', async () => {
    await page.getByRole('button', { name: '连接终端', exact: true }).click()
    await page.locator('.xterm-helper-textarea').waitFor({ state: 'attached' })
    await page.locator('.xterm-helper-textarea').focus()
    await page.keyboard.type('six-k-browser-echo')
    await page.keyboard.press('Enter')
    await page.getByText('已连接', { exact: true }).first().waitFor()
    await page.waitForFunction(() => [...document.querySelectorAll('.xterm-rows')].some(node => node.textContent.includes('six-k-browser-echo')))
    await page.screenshot({ path: path.join(output, 'desktop-terminal.png') })
  })
  await check('Chrome 窄屏认证表单与认证方式双向切换', async () => {
    // 关闭连接后返回服务器资料，防止表单测试改变活动终端。
    await page.reload()
    await page.getByRole('button', { name: '显示或隐藏服务器列表', exact: true }).click()
    await page.locator('.server-row').filter({ hasText: '6-K browser' }).click()
    await page.setViewportSize({ width: 390, height: 844 })
    await edit()
    await savedForm().getByLabel('认证方式').selectOption('password')
    await savedForm().getByLabel('Password', { exact: true }).fill('ssh-secret')
    await savedForm().getByRole('button', { name: '保存服务器', exact: true }).click()
    await waitClosed()
    await edit()
    assert.equal(await savedForm().getByLabel('认证方式').inputValue(), 'password')
    await savedForm().getByLabel('认证方式').selectOption('private_key')
    await savedForm().getByLabel('私钥文件', { exact: true }).setInputFiles(data.upload)
    await savedForm().getByLabel('Passphrase（可选）').fill('browser-test-passphrase')
    await page.screenshot({ path: path.join(output, 'narrow-key-form.png') })
    const bounds = await savedForm().boundingBox()
    assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= 391)
    await savedForm().getByRole('button', { name: '保存服务器', exact: true }).click()
    await waitClosed()
    await edit()
    await savedForm().getByRole('button', { name: '测试连接', exact: true }).click()
    await savedForm().getByRole('status').filter({ hasText: 'SSH 连接测试成功' }).waitFor()
  })
  assert.deepEqual(errors, [], '浏览器未捕获异常')
  console.log(JSON.stringify({ browser: await browser.version(), results }, null, 2))
} finally {
  await browser?.close()
  await vite?.close()
  await writeFile(path.join(output, 'stop'), '')
  const exit = await fixtureExit
  await writeFile(path.join(output, 'fixture.log'), fixtureLog)
  await writeFile(path.join(output, 'results.json'), JSON.stringify({ results, fixtureExit: exit }, null, 2))
  if (exit !== 0) throw new Error(`Go fixture exit ${exit}: ${fixtureLog}`)
}
