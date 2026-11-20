// 使用真实 Vue、xterm 和浏览器；HTTP/WS 仅提供测试响应，不访问真实 SSH。
import test, { before, after } from 'node:test'
import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
const { chromium } = await import(
  pathToFileURL(
    process.env.UI_PLAYWRIGHT_MODULE ||
      '/tmp/codex/terminal-ui-optimization/tools/node_modules/playwright/index.mjs',
  )
)
const root = fileURLToPath(new URL('../../..', import.meta.url))
const out =
  process.env.UI_EVIDENCE_DIR ||
  '/tmp/codex/terminal-ui-optimization/browser/'
const settings = {
  max_control_message_size: 16384,
  max_input_message_size: 4096,
  output_chunk_size: 4096,
  credential_timeout: 3,
  bind_timeout: 3,
  operation_wait_timeout: 1,
  write_timeout: 1,
  max_cols: 500,
  max_rows: 300,
  write_queue_bytes: 8192,
  write_drain_timeout: 1,
  reconnect_max_attempts: 2,
  reconnect_delay: 1,
  reconnect_stable_time: 30,
}
const A = 'a'.repeat(32),
  B = 'c'.repeat(32),
  SERVER = 'b'.repeat(32)
const stamp = '2026-11-19T00:00:00Z'
const view = (id = A, status = 'CONNECTED') => ({
  id,
  server_id: SERVER,
  name: '终端 ' + id.slice(0, 1).toUpperCase(),
  host: 'fixture.example',
  port: 22,
  username: 'operator',
  status,
  binding_generation: 5,
  current_cols: 80,
  current_rows: 24,
  created_at: stamp,
  disconnected_at: status === 'DISCONNECTED' ? stamp : null,
  last_attached_at: null,
  offline_expires_at:
    status === 'DISCONNECTED' ? '2099-01-01T00:00:00Z' : null,
  remaining_seconds: status === 'DISCONNECTED' ? 300 : null,
})
const ok = (data) => ({
  status: 200,
  body: { code: 0, message: 'ok', data },
})
const err = (code, status = 503) => ({
  status,
  body: { code, message: code, data: {} },
})
function gate() {
  let resolve
  const promise = new Promise((r) => (resolve = r))
  return { promise, resolve }
}
let vite, browser, base
const evidence = {
  environment:
    'Real Chromium + Vite + actual Vue SFC/router/xterm/client/coordinator; fixture HTTP and WS, no real backend/SSH',
  results: [],
}
before(async () => {
  await mkdir(out, { recursive: true })
  vite = await createServer({
    root: root + '/frontend',
    configFile: false,
    plugins: [vue()],
    server: { port: 0, strictPort: false, host: '127.0.0.1' },
    logLevel: 'warn',
  })
  await vite.listen()
  base = 'http://127.0.0.1:' + vite.httpServer.address().port
  browser = await chromium.launch({
    executablePath:
      process.env.UI_CHROMIUM ||
      '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    headless: true,
  })
  evidence.browser = browser.version()
  evidence.node = process.version
  evidence.platform = process.platform
  evidence.unverifiedPlatforms = ['Windows', 'Linux']
})
after(async () => {
  await browser?.close()
  await vite?.close()
  await writeFile(
    out + 'browser-results.json',
    JSON.stringify(evidence, null, 2) + '\n',
  )
})
async function fixture(t, items = [view()], expectedConsoleErrors = []) {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    permissions: ['clipboard-read', 'clipboard-write'],
  })
  const page = await context.newPage()
  page.setDefaultTimeout(10000)
  const requests = [],
    queues = new Map(),
    pending = [],
    wsPeers = [],
    inputs = []
  let loggedIn = true
  const errors = [],
    warnings = [],
    resourceErrors = []
  page.on('pageerror', (e) => errors.push(e.message))
  page.on('console', (m) => {
    if (m.type() === 'warning' && m.text().includes('[Vue warn]'))
      warnings.push(m.text())
    if (m.type() === 'error')
      resourceErrors.push({ text: m.text(), url: m.location().url })
  })
  const enqueue = (method, path, reply) => {
    const key = method + ' ' + path
    if (!queues.has(key)) queues.set(key, [])
    queues.get(key).push(reply)
  }
  const installSocketCounter = () => {
    if (window.WebSocket.__uiWrapped) return
    window.__uiWS = []
    const Native = window.WebSocket
    window.WebSocket = class extends Native {
      constructor(...args) {
        super(...args)
        if (!String(args[0]).includes('/ws/sessions/')) return
        const record = {
          url: String(args[0]),
          closeCalls: 0,
          closed: 0,
          messages: [],
        }
        window.__uiWS.push(record)
        this.addEventListener('close', () => record.closed++)
        const close = this.close.bind(this)
        this.close = (...a) => {
          record.closeCalls++
          return close(...a)
        }
        this.addEventListener('message', (e) => {
          if (typeof e.data === 'string') record.messages.push(e.data)
        })
      }
    }
    window.WebSocket.__uiWrapped = true
  }
  await context.addInitScript(installSocketCounter)
  await context.addInitScript(() => {
    window.__copies = []
    const write = navigator.clipboard.writeText.bind(navigator.clipboard)
    navigator.clipboard.writeText = (text) => {
      window.__copies.push(text)
      return write(text)
    }
  })
  await context.route(
    (url) => url.pathname.startsWith('/api/'),
    async (route) => {
      const req = route.request(),
        path = new URL(req.url()).pathname,
        method = req.method()
      const record = {
        method,
        path,
        body: req.postDataJSON(),
        index: requests.length,
      }
      requests.push(record)
      let reply = queues.get(method + ' ' + path)?.shift()
      if (typeof reply === 'function') reply = reply(record)
      if (reply?.then) {
        const marker = reply
        pending.push(marker)
        reply = await marker
      }
      if (!reply) {
        if (path === '/api/auth/me')
          reply = loggedIn
            ? ok({
                expires_at: '2099-01-01T00:00:00Z',
                client_settings: settings,
              })
            : err('AUTH_REQUIRED', 401)
        else if (path === '/api/auth/logout') reply = ok({})
        else if (path === '/api/servers') reply = ok({ items: [] })
        else if (path === '/api/sessions') reply = ok({ items })
        else if (method === 'DELETE') reply = ok({})
        else if (path.endsWith('/takeover'))
          reply = ok({
            credential: 'a'.repeat(43),
            expires_at: '2099-01-01T00:00:00Z',
          })
        else reply = ok(items.find((i) => path.endsWith(i.id)) || view())
      }
      if (
        path === '/api/auth/logout' &&
        reply.status === 200 &&
        reply.body?.code === 0
      )
        loggedIn = false
      try {
        if (reply.abort) await route.abort('failed')
        else
          await route.fulfill({
            status: reply.status,
            contentType: reply.raw ? 'text/html' : 'application/json',
            body: reply.raw || JSON.stringify(reply.body),
          })
      } catch (e) {
        if (
          !page.isClosed() &&
          !/closed|handled|Invalid Interception|cancel/i.test(e.message)
        )
          throw e
      }
    },
  )
  await context.routeWebSocket('**/ws/sessions/**', (ws) => {
    wsPeers.push(ws)
    ws.onMessage((data) => {
      if (typeof data !== 'string') inputs.push([...data])
      const msg = typeof data === 'string' ? JSON.parse(data) : null
      if (msg?.type === 'bind')
        ws.send(
          JSON.stringify({
            type: 'bind_success',
            binding_generation: 6,
            replay_start_offset: 0,
            replay_end_offset: 0,
            current_cols: 80,
            current_rows: 24,
          }),
        )
    })
  })
  const count = (method, path) =>
    requests.filter(
      (r) => r.method === method && (!path || r.path === path),
    ).length
  const snap = async () =>
    page.evaluate(() => {
      const panes = [...document.querySelectorAll('.terminal-pane')].map(
        (el) => {
          const s = el.__vueParentComponent.setupState
          return {
            id: s.session?.id || el.__vueParentComponent.props.session.id,
            life: s.life,
            phase: s.phase,
            takeover: s.takeoverView?.phase,
            close: s.closeSnapshot?.state,
            ended: s.ended,
            active: el.__vueParentComponent.props.active,
          }
        },
      )
      return {
        panes,
        ws: window.__uiWS,
        xterms: document.querySelectorAll('.xterm').length,
        path: location.pathname,
      }
    })
  const ready = async () => {
    await page.waitForFunction(
      () =>
        document.querySelector('.session-manager tbody tr') ||
        document.body.innerText.includes('没有活动 Session'),
    )
  }
  const manager = async () => {
    await page.evaluate(installSocketCounter)
    await page
      .getByRole('button', { name: '会话管理', exact: true })
      .click()
    await ready()
  }
  const row = (id) => page.locator('tbody tr').filter({ hasText: id })
  const open = async (id = A) => {
    await row(id)
      .getByRole('button', { name: '打开', exact: true })
      .click()
    await page.locator('.terminal-pane').last().waitFor()
  }
  const shot = async (name) => {
    await page.screenshot({ path: out + name + '.png', fullPage: true })
  }
  t.after(async () => {
    const final = page.isClosed() ? null : await snap()
    evidence.results.push({
      name: t.name,
      requests,
      final,
      errors,
      warnings,
      resourceErrors,
    })
    await page.screenshot({
      path: out + t.name.slice(0, 6) + '-final.png',
      fullPage: true,
    })
    await context.close()
    assert.deepEqual(errors, [], 'no runtime error / unhandled Promise')
    assert.deepEqual(warnings, [], 'no Vue warnings')
    for (const expected of expectedConsoleErrors) {
      assert.equal(
        resourceErrors.filter((item) => item.text.includes(expected))
          .length,
        1,
        '预期故障日志恰好一次',
      )
    }
    const unexpected = resourceErrors.filter(
      (s) =>
        !expectedConsoleErrors.some((expected) =>
          s.text.includes(expected),
        ) &&
        (!s.text.startsWith('Failed to load resource:') ||
          !new URL(s.url, base).pathname.startsWith('/api/')),
    )
    assert.deepEqual(unexpected, [], 'no unexpected console errors')
  })
  await page.goto(base)
  await page
    .getByRole('button', { name: '会话管理', exact: true })
    .waitFor()
  await manager()
  return {
    page,
    context,
    requests,
    enqueue,
    count,
    snap,
    manager,
    row,
    open,
    shot,
    wsPeers,
    ready,
    inputs,
  }
}
const C = 'd'.repeat(32)
const menu = async (f) => {
  await f.page
    .getByRole('button', { name: '当前终端操作', exact: true })
    .click()
  await f.page.locator('.terminal-menu:popover-open').waitFor()
}
async function connect(f, id = A) {
  await f.open(id)
  await f.page
    .getByRole('button', { name: '确认接管', exact: true })
    .click()
  await f.page.waitForFunction(() =>
    document
      .querySelector('.connection-state')
      ?.textContent.includes('已连接'),
  )
}
const selected = (f) =>
  f.page.evaluate(() =>
    [...document.querySelectorAll('.terminal-pane')]
      .find((e) => e.__vueParentComponent.props.active)
      .__vueParentComponent.setupState.terminal.getSelection(),
  )
async function drag(f, { reverse = false, multiline = false } = {}) {
  const box = await f.page
    .locator('.terminal-pane:visible .xterm-screen')
    .boundingBox()
  const size = await f.page.evaluate(() => {
    const s = [...document.querySelectorAll('.terminal-pane')].find(
      (e) => e.__vueParentComponent.props.active,
    ).__vueParentComponent.setupState
    return { cols: s.terminal.cols, rows: s.terminal.rows }
  })
  const x = box.x + 1,
    y = box.y + box.height / size.rows / 2,
    cw = box.width / size.cols,
    rh = box.height / size.rows
  const start = { x: x + 0.1 * cw, y },
    end = { x: x + 10 * cw, y: y + (multiline ? 3 * rh : 0) }
  const [a, b] = reverse ? [end, start] : [start, end]
  await f.page.mouse.move(a.x, a.y)
  await f.page.mouse.down()
  await f.page.mouse.move(b.x, b.y, { steps: 20 })
  await f.page.mouse.up()
  assert.ok((await selected(f)).length > 0, '真实鼠标拖动产生选区')
}
async function waitFor(check) {
  const end = Date.now() + 2000
  while (!check() && Date.now() < end)
    await new Promise((resolve) => setTimeout(resolve, 10))
  assert.ok(check())
}

test('UI-B01 真实鼠标、浅深主题、多行与 macOS 原生复制及中断', async (t) => {
  const f = await fixture(t)
  await connect(f)
  f.wsPeers[0].send(
    Buffer.from(
      'English 中文 output\r\n\r\n\x1b[32mANSI green\x1b[0m\r\n' +
        'long wrapped line '.repeat(18) +
        '\r\n',
    ),
  )
  await f.page.waitForFunction(() =>
    document
      .querySelector('.terminal-pane')
      .__vueParentComponent.setupState.terminal.buffer.active.getLine(0)
      .translateToString()
      .includes('English'),
  )
  for (const theme of ['light', 'dark']) {
    await f.page.evaluate((theme) => {
      document.documentElement.dataset.theme = theme
    }, theme)
    await drag(f, { reverse: theme === 'dark', multiline: true })
    const text = await selected(f)
    assert.match(text, /English 中文 output/)
    assert.match(text, /ANSI green/)
    assert.ok(!text.includes('\x1b'))
    const colors = await f.page.evaluate(
      () =>
        document.querySelector('.terminal-pane').__vueParentComponent
          .setupState.terminal.options.theme,
    )
    assert.ok(colors.selectionBackground)
    assert.ok(colors.selectionInactiveBackground)
    await menu(f)
    await f.page
      .getByRole('button', { name: '复制选中文本', exact: true })
      .click()
    assert.equal(
      await f.page.evaluate(() => navigator.clipboard.readText()),
      text,
    )
    assert.equal(await selected(f), text, '失焦菜单保留选区')
    await f.shot('selection-' + theme)
  }
  if (process.platform === 'darwin') {
    await f.page.evaluate(() =>
      navigator.clipboard.writeText('native-copy-sentinel'),
    )
    const beforeCopies = await f.page.evaluate(
      () => window.__copies.length,
    )
    await f.page
      .locator('.terminal-pane:visible .xterm-helper-textarea')
      .focus()
    await f.page.keyboard.press('Meta+c')
    assert.equal(
      await f.page.evaluate(() => navigator.clipboard.readText()),
      await selected(f),
    )
    assert.equal(
      await f.page.evaluate(() => window.__copies.length),
      beforeCopies,
      'Cmd+C 只走原生路径',
    )
  }
  await f.page.evaluate(() =>
    document
      .querySelector('.terminal-pane')
      .__vueParentComponent.setupState.terminal.clearSelection(),
  )
  await f.page.keyboard.press('Control+c')
  await waitFor(() => f.inputs.some((value) => value.includes(3)))
  await f.page.keyboard.insertText('粘贴命令\n')
  await waitFor(() =>
    Buffer.from(f.inputs.flat()).toString().includes('粘贴命令'),
  )
  await f.page.locator('.xterm').click({ button: 'right' })
  assert.equal(
    await f.page.locator('.terminal-menu').count(),
    0,
    '右键未增加应用菜单',
  )
})

test('UI-B02 A/B/C 后台关闭、菜单身份、稳定 host、窄屏与重开 key', async (t) => {
  const f = await fixture(t, [view(), view(B), view(C)])
  await connect(f, A)
  await f.manager()
  await connect(f, B)
  await f.manager()
  await connect(f, C)
  await f.page.evaluate(() => {
    window.__paneRefs = [
      ...document.querySelectorAll('.terminal-pane'),
    ].map((el) => ({
      el,
      host: el.querySelector('.terminal-host'),
      term: el.__vueParentComponent.setupState.terminal,
      client: el.__vueParentComponent.setupState.client,
    }))
  })
  await menu(f)
  assert.equal(
    await f.page
      .locator('.terminal-menu-group[aria-label="当前任务"]')
      .count(),
    0,
  )
  assert.equal(
    await f.page
      .locator('.terminal-menu-group[aria-label="关闭处理中"]')
      .count(),
    0,
  )
  assert.equal(
    await f.page.locator('.terminal-menu-details[open]').count(),
    0,
  )
  await f.page.keyboard.press('Escape')
  await f.page.locator('.terminal-menu').waitFor({ state: 'detached' })
  assert.equal(
    await f.page
      .getByRole('button', { name: '当前终端操作', exact: true })
      .evaluate((e) => e === document.activeElement),
    true,
  )
  await menu(f)
  await f.page.mouse.click(40, 120)
  await f.page.locator('.terminal-menu').waitFor({ state: 'detached' })
  await menu(f)
  const stale = await f.page.evaluate(() => {
    const old = [...document.querySelectorAll('.terminal-pane')].find(
      (el) => el.__vueParentComponent.props.active,
    ).__vueParentComponent
    const action = [
      ...document.querySelectorAll('.terminal-menu button'),
    ].find((el) => el.textContent.includes('关闭 SSH 会话'))
    document.querySelector('.terminal-tab-title').click()
    const allowed = old.props.menuAllowed()
    action.click()
    return {
      allowed,
      closing: old.setupState.closing,
      oldDOMStillPresent: !!document.querySelector('.terminal-menu'),
    }
  })
  assert.deepEqual(stale, {
    allowed: false,
    closing: false,
    oldDOMStillPresent: true,
  })
  await f.page.locator('.terminal-menu').waitFor({ state: 'detached' })
  assert.equal(await f.page.locator('.terminal-menu-trigger').count(), 1)
  assert.equal(
    await f.page.evaluate(() =>
      window.__paneRefs.every(
        (r) =>
          r.el.isConnected &&
          r.host === r.el.querySelector('.terminal-host') &&
          r.term === r.el.__vueParentComponent.setupState.terminal &&
          r.client === r.el.__vueParentComponent.setupState.client,
      ),
    ),
    true,
  )
  await menu(f)
  await f.page
    .getByRole('button', { name: '会话管理', exact: true })
    .click()
  await f.page
    .locator('.terminal-menu-trigger')
    .waitFor({ state: 'detached' })
  await f.page
    .getByRole('button', { name: '返回服务器', exact: true })
    .click()
  assert.equal(await f.page.locator('.terminal-menu').count(), 0)
  await f.page.setViewportSize({ width: 390, height: 740 })
  await menu(f)
  const bounds = await f.page.locator('.terminal-menu').boundingBox()
  assert.ok(
    bounds.x >= 0 &&
      bounds.x + bounds.width <= 390 &&
      bounds.height <= 640,
  )
  await f.shot('narrow-menu')
  await f.page.keyboard.press('Escape')
  await f.page
    .getByRole('button', { name: '关闭标签：终端 D', exact: true })
    .click()
  await f.page
    .getByRole('button', { name: '关闭标签：终端 D', exact: true })
    .waitFor({ state: 'detached' })
  assert.match(
    await f.page
      .locator('.terminal-tab.selected .terminal-tab-title')
      .textContent(),
    /终端 A/,
  )
  await f.page
    .getByRole('button', { name: '关闭标签：终端 A', exact: true })
    .click()
  await f.page
    .getByRole('button', { name: '关闭标签：终端 A', exact: true })
    .waitFor({ state: 'detached' })
  assert.match(
    await f.page
      .locator('.terminal-tab.selected .terminal-tab-title')
      .textContent(),
    /终端 C/,
  )
  const oldKey = await f.page
    .locator('.terminal-tab-close-slot')
    .getAttribute('id')
  await f.page
    .getByRole('button', { name: '关闭标签：终端 C', exact: true })
    .click()
  await f.page.locator('.terminal-pane').waitFor({ state: 'detached' })
  assert.equal(await f.page.locator('#terminal-tools button').count(), 0)
  assert.equal(f.count('DELETE'), 0)
  await f.manager()
  await f.open(B)
  assert.notEqual(
    await f.page.locator('.terminal-tab-close-slot').getAttribute('id'),
    oldKey,
  )
  await f.page.getByRole('button', { name: '取消', exact: true }).click()
  await f.page
    .getByRole('button', { name: '关闭标签：终端 C', exact: true })
    .click()
  await f.page.locator('.terminal-pane').waitFor({ state: 'detached' })
  assert.equal(f.count('DELETE'), 0)
})

test('UI-B03 实际 macOS 浏览器中非 Mac 分支诊断（不作为 Windows/Linux 平台验收）', async (t) => {
  const f = await fixture(t)
  await connect(f)
  f.wsPeers[0].send(Buffer.from('shortcut text\r\n'))
  await f.page.waitForFunction(() =>
    document
      .querySelector('.terminal-pane')
      .__vueParentComponent.setupState.terminal.buffer.active.getLine(0)
      .translateToString()
      .includes('shortcut'),
  )
  // 显式安装被测分支，不更改 UA、navigator.platform 或宣称跨平台验收。
  await f.page.evaluate(async () => {
    const { clipboardKeyHandler } =
      await import('/src/terminal/clipboard.ts')
    const terminal =
      document.querySelector('.terminal-pane').__vueParentComponent
        .setupState.terminal
    window.__keyEvidence = []
    const handler = clipboardKeyHandler(false, () => {
      void document.querySelector('.terminal-pane').__vueParentComponent.setupState.copySelection(terminal)
    })
    terminal.attachCustomKeyEventHandler((event) => {
      let prevented = 0,
        stopped = 0
      const prevent = event.preventDefault.bind(event),
        stop = event.stopPropagation.bind(event)
      event.preventDefault = () => {
        prevented++
        prevent()
      }
      event.stopPropagation = () => {
        stopped++
        stop()
      }
      const result = handler(event)
      window.__keyEvidence.push({
        type: event.type,
        repeat: event.repeat,
        prevented,
        stopped,
        result,
        defaultPrevented: event.defaultPrevented,
      })
      return result
    })
  })
  for (const selection of [true, false]) {
    if (selection) await drag(f)
    else
      await f.page.evaluate(() =>
        document
          .querySelector('.terminal-pane')
          .__vueParentComponent.setupState.terminal.clearSelection(),
      )
    const copies = await f.page.evaluate(() => window.__copies.length)
    const inputs = f.inputs.length
    await f.page.locator('.xterm-helper-textarea').focus()
    await f.page.keyboard.down('Control')
    await f.page.keyboard.down('Shift')
    await f.page.keyboard.down('c')
    await f.page.keyboard.down('c') // 自动重复
    await f.page.keyboard.up('c')
    await f.page.keyboard.up('Shift')
    await f.page.keyboard.up('Control')
    const entries = await f.page.evaluate(() =>
      window.__keyEvidence.filter((e) => e.prevented),
    )
    assert.ok(entries.some((e) => e.type === 'keydown'))
    assert.ok(entries.some((e) => e.repeat))
    assert.ok(
      entries.every(
        (e) =>
          e.prevented === 1 &&
          e.stopped === 1 &&
          e.result === false &&
          e.defaultPrevented,
      ),
    )
    assert.equal(
      await f.page.evaluate(() => window.__copies.length),
      copies + (selection ? 1 : 0),
    )
    assert.equal(f.inputs.length, inputs)
    evidence.results.push({
      name: '非 Mac 分支诊断',
      actualPlatform: process.platform,
      selection,
      entries,
      clipboardWrites: selection ? 1 : 0,
      terminalInputs: 0,
      browser:
        '页面收到事件，正确取消；未测试 Windows/Linux 浏览器保留快捷键',
    })
  }
})

test('UI-B04 真实终端软换行、历史滚动、粘贴、IME 事件与清理失败保护', async (t) => {
  const f = await fixture(
    t,
    [view()],
    [
      '退出登录时终端本地资源释放失败 ' +
        A +
        ' Error: fixture dispose failure',
    ],
  )
  await connect(f)
  const line = 'soft-wrap-中文 '.repeat(30)
  f.wsPeers[0].send(
    Buffer.from(
      line +
        '\r\n' +
        Array.from({ length: 80 }, (_, i) => 'history ' + i + '\r\n').join(
          '',
        ),
    ),
  )
  await f.page.waitForFunction(
    () =>
      document.querySelector('.terminal-pane').__vueParentComponent
        .setupState.terminal.buffer.active.baseY > 20,
  )
  const text = await f.page.evaluate(() => {
    const terminal =
      document.querySelector('.terminal-pane').__vueParentComponent
        .setupState.terminal
    terminal.selectAll()
    return terminal.getSelection()
  })
  assert.ok(text.includes(line), '软换行不引入额外换行')
  assert.match(text, /history 0/)
  assert.match(text, /history 79/)
  await menu(f)
  await f.page
    .getByRole('button', { name: '复制选中文本', exact: true })
    .click()
  assert.equal(
    await f.page.evaluate(() => navigator.clipboard.readText()),
    text,
  )
  const y = await f.page.evaluate(
    () =>
      document.querySelector('.terminal-pane').__vueParentComponent
        .setupState.terminal.buffer.active.viewportY,
  )
  await f.page.locator('.xterm-screen').hover()
  await f.page.mouse.wheel(0, -400)
  await f.page.waitForFunction(
    (y) =>
      document.querySelector('.terminal-pane').__vueParentComponent
        .setupState.terminal.buffer.active.viewportY < y,
    y,
  )
  // 合成 composition 事件只验证输入未受新增复制处理器拦截，不替代真实输入法候选窗口验收。
  await f.page.locator('.xterm-helper-textarea').focus()
  await f.page
    .locator('.xterm-helper-textarea')
    .dispatchEvent('compositionstart', { data: '' })
  await f.page
    .locator('.xterm-helper-textarea')
    .dispatchEvent('compositionupdate', { data: '中文' })
  await f.page.locator('.xterm-helper-textarea').evaluate((el) => {
    el.value = '中文'
    el.dispatchEvent(
      new CompositionEvent('compositionend', {
        data: '中文',
        bubbles: true,
      }),
    )
  })
  await waitFor(() =>
    Buffer.from(f.inputs.flat()).toString().includes('中文'),
  )
  await f.page.evaluate(() => {
    document.querySelector(
      '.terminal-pane',
    ).__vueParentComponent.setupState.terminal.dispose = () => {
      throw new Error('fixture dispose failure')
    }
  })
  await menu(f)
  await f.page
    .getByRole('button', { name: '关闭标签：终端 A', exact: true })
    .click()
  await f.page.waitForFunction(
    () =>
      document.querySelector('.terminal-pane').__vueParentComponent
        .setupState.life === 'dispose_failed',
  )
  assert.equal(await f.page.locator('.terminal-pane').count(), 1)
  assert.equal(
    await f.page
      .getByRole('button', { name: '关闭标签：终端 A', exact: true })
      .isDisabled(),
    true,
  )
  assert.equal(await f.page.locator('.terminal-menu').count(), 0)
  assert.equal(f.count('DELETE'), 0)
  await f.page
    .getByRole('button', { name: '退出登录', exact: true })
    .click()
  await f.page.waitForURL('**/login')
  assert.equal(await f.page.locator('#terminal-tools').count(), 0)
  assert.equal(await f.page.locator('.terminal-tab-close').count(), 0)
})
