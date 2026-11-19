// S7-6: real compiled Vue/router/xterm in Chromium; HTTP and WS server responses are fixtures.
// No backend, SSH, deployment, auth/router production changes, or project dependency changes.
import test, { before, after } from 'node:test'
import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
const { chromium } = await import(pathToFileURL(process.env.S7_PLAYWRIGHT_MODULE || '/tmp/codex/web-ssh-s7-6-tools/node_modules/playwright/index.mjs'))
const root = fileURLToPath(new URL('../../..', import.meta.url))
const out = fileURLToPath(new URL('../../../doc/测试报告/stage-5-s7-6-logs/', import.meta.url))
const settings = { max_control_message_size:16384,max_input_message_size:4096,output_chunk_size:4096,credential_timeout:3,bind_timeout:3,operation_wait_timeout:1,write_timeout:1,max_cols:500,max_rows:300,write_queue_bytes:8192,write_drain_timeout:1,reconnect_max_attempts:2,reconnect_delay:1,reconnect_stable_time:30 }
const A = 'a'.repeat(32), B = 'c'.repeat(32), SERVER = 'b'.repeat(32)
const stamp = '2026-11-19T00:00:00Z'
const view = (id=A,status='CONNECTED') => ({ id,server_id:SERVER,name:id===A?'S7 session A':'S7 session B',host:'fixture.example',port:22,username:'operator',status,binding_generation:5,current_cols:80,current_rows:24,created_at:stamp,disconnected_at:status==='DISCONNECTED'?stamp:null,last_attached_at:null,offline_expires_at:status==='DISCONNECTED'?'2099-01-01T00:00:00Z':null,remaining_seconds:status==='DISCONNECTED'?300:null })
const ok = data => ({ status:200, body:{code:0,message:'ok',data} })
const err = (code,status=503) => ({ status, body:{code,message:code,data:{}} })
function gate() { let resolve; const promise = new Promise(r=>resolve=r); return {promise,resolve} }
let vite, browser, base
const evidence = { environment:'Real Chromium + Vite + actual Vue SFC/router/xterm/client/coordinator; fixture HTTP and WS, no real backend/SSH', results:[] }
before(async()=>{
  await mkdir(out,{recursive:true})
  vite = await createServer({root:root+'/frontend',configFile:false,plugins:[vue()],server:{port:0,strictPort:false,host:'127.0.0.1'},logLevel:'warn'})
  await vite.listen()
  base = 'http://127.0.0.1:'+vite.httpServer.address().port
  browser = await chromium.launch({executablePath:process.env.S7_CHROMIUM || '/home/x/.cache/ms-playwright/chromium-1255/chrome-linux64/chrome',headless:true,env:{...process.env,FONTCONFIG_FILE:process.env.S7_FONTCONFIG || '/tmp/codex/web-ssh-s7-6-tools/fonts.conf',LD_LIBRARY_PATH:process.env.S7_BROWSER_LIBS || '/tmp/codex/web-ssh-s7-6-tools/libs/usr/lib/x86_64-linux-gnu'},args:['--no-sandbox']})
  evidence.browser = browser.version()
  evidence.node = process.version
})
after(async()=>{
  await browser?.close()
  await vite?.close()
  await writeFile(out+'browser-results.json',JSON.stringify(evidence,null,2)+'\n')
})
async function fixture(t,items=[view()]) {
  const context = await browser.newContext({viewport:{width:1440,height:1000}})
  const page = await context.newPage()
  page.setDefaultTimeout(10000)
  const requests=[], queues=new Map(), pending=[], wsPeers=[]
  let loggedIn=true
  const errors=[], warnings=[], resourceErrors=[]
  page.on('pageerror',e=>errors.push(e.message))
  page.on('console',m=>{if(m.type()==='warning' && m.text().includes('[Vue warn]')) warnings.push(m.text()); if(m.type()==='error') resourceErrors.push({text:m.text(),url:m.location().url})})
  const enqueue=(method,path,reply)=>{
    const key=method+' '+path
    if(!queues.has(key)) queues.set(key,[])
    queues.get(key).push(reply)
  }
  const installSocketCounter=()=>{
    if(window.WebSocket.__s7wrapped)return
    window.__s7ws=[]
    const Native=window.WebSocket
    window.WebSocket=class extends Native {
      constructor(...args){super(...args);if(!String(args[0]).includes('/ws/sessions/'))return;const record={url:String(args[0]),closeCalls:0,closed:0,messages:[]};window.__s7ws.push(record);this.addEventListener('close',()=>record.closed++);const close=this.close.bind(this);this.close=(...a)=>{record.closeCalls++;return close(...a)};this.addEventListener('message',e=>{if(typeof e.data==='string')record.messages.push(e.data)})}
    }
    window.WebSocket.__s7wrapped=true
  }
  await context.addInitScript(installSocketCounter)
  await context.route(url=>url.pathname.startsWith('/api/'),async route=>{
    const req=route.request(), path=new URL(req.url()).pathname, method=req.method()
    const record={method,path,body:req.postDataJSON(),index:requests.length}
    requests.push(record)
    let reply=queues.get(method+' '+path)?.shift()
    if(typeof reply==='function') reply=reply(record)
    if(reply?.then) { const marker=reply; pending.push(marker);reply=await marker }
    if(!reply){
      if(path==='/api/auth/me')reply=loggedIn?ok({expires_at:'2099-01-01T00:00:00Z',client_settings:settings}):err('AUTH_REQUIRED',401)
      else if(path==='/api/auth/logout')reply=ok({})
      else if(path==='/api/servers')reply=ok({items:[]})
      else if(path==='/api/sessions')reply=ok({items})
      else if(method==='DELETE')reply=ok({})
      else if(path.endsWith('/takeover'))reply=ok({credential:'a'.repeat(43),expires_at:'2099-01-01T00:00:00Z'})
      else reply=ok(items.find(i=>path.endsWith(i.id)) || view())
    }
    if(path==='/api/auth/logout' && reply.status===200 && reply.body?.code===0)loggedIn=false
    try { if(reply.abort)await route.abort('failed');else await route.fulfill({status:reply.status,contentType:reply.raw?'text/html':'application/json',body:reply.raw || JSON.stringify(reply.body)}) } catch(e) { if(!page.isClosed() && !/closed|handled|Invalid Interception|cancel/i.test(e.message))throw e }
  })
  await context.routeWebSocket('**/ws/sessions/**',ws=>{
    wsPeers.push(ws)
    ws.onMessage(data=>{
      const msg=typeof data==='string'?JSON.parse(data):null
      if(msg?.type==='bind') ws.send(JSON.stringify({type:'bind_success',binding_generation:6,replay_start_offset:0,replay_end_offset:0,current_cols:80,current_rows:24}))
    })
  })
  const count=(method,path)=>requests.filter(r=>r.method===method && (!path || r.path===path)).length
  const snap=async()=>page.evaluate(()=>{
    const panes=[...document.querySelectorAll('.terminal-pane')].map(el=>{const s=el.__vueParentComponent.setupState;return {id:s.session?.id || el.__vueParentComponent.props.session.id,life:s.life,phase:s.phase,takeover:s.takeoverView?.phase,close:s.closeSnapshot?.state,ended:s.ended,active:el.__vueParentComponent.props.active}})
    return {panes,ws:window.__s7ws,xterms:document.querySelectorAll('.xterm').length,path:location.pathname}
  })
  const ready=async()=>{await page.waitForFunction(()=>document.querySelector('.session-manager tbody tr') || document.body.innerText.includes('没有活动 Session'))}
  const manager=async()=>{await page.evaluate(installSocketCounter);await page.getByRole('button',{name:'会话管理',exact:true}).click();await ready()}
  const row=id=>page.locator('tbody tr').filter({hasText:id})
  const open=async(id=A)=>{await row(id).getByRole('button',{name:'打开',exact:true}).click();await page.locator('.terminal-pane').last().waitFor()}
  const shot=async name=>{await page.screenshot({path:out+name+'.png',fullPage:true})}
  t.after(async()=>{
    const final=page.isClosed()?null:await snap()
    evidence.results.push({name:t.name,requests,final,errors,warnings,resourceErrors})
    if(t.passed === false) await page.screenshot({path:out+t.name.slice(0,6)+'-failure.png',fullPage:true})
    await context.close()
    assert.deepEqual(errors,[],'no runtime error / unhandled Promise')
    assert.deepEqual(warnings,[],'no Vue warnings')
    const unexpected=resourceErrors.filter(s=>!s.text.startsWith('Failed to load resource:') || !new URL(s.url,base).pathname.startsWith('/api/'))
    assert.deepEqual(unexpected,[],'no unexpected console errors')
  })
  await page.goto(base)
  await page.getByRole('button',{name:'会话管理',exact:true}).waitFor()
  await manager()
  return {page,context,requests,enqueue,count,snap,manager,row,open,shot,wsPeers,ready}
}
const detailPath=id=>'/api/sessions/'+id
const closeRow=async(f,id=A)=>{await f.row(id).getByRole('button',{name:'关闭',exact:true}).click();await f.page.getByRole('button',{name:'确认关闭',exact:true}).click()}
const waitState=async(f,id,state)=>f.page.waitForFunction(({id,state})=>[...document.querySelectorAll('.terminal-pane')].some(e=>e.__vueParentComponent.props.session.id===id && e.__vueParentComponent.setupState.closeSnapshot.state===state),{id,state})

test('S7-B01 real component row-close -> pending -> Pane reopen -> cancel -> proxy unknown -> detail -> ended',async t=>{
  const f=await fixture(t), g=gate()
  f.enqueue('DELETE',detailPath(A),g.promise)
  await closeRow(f)
  assert.equal(f.count('DELETE'),1)
  await f.open()
  await waitState(f,A,'pending')
  assert.equal(f.count('GET',detailPath(A)),0)
  assert.equal(f.count('POST'),0)
  assert.equal((await f.snap()).ws.length,0)
  await f.page.getByRole('button',{name:'取消等待',exact:true}).click()
  await waitState(f,A,'unknown')
  g.resolve(ok({}))
  await f.page.getByRole('button',{name:'关闭标签',exact:true}).click()
  await f.page.locator('.terminal-pane').waitFor({state:'detached'})
  await f.manager();await f.open();await waitState(f,A,'unknown')
  f.enqueue('DELETE',detailPath(A),err('INVALID_REQUEST',400))
  await f.page.getByRole('button',{name:'重试关闭',exact:true}).click()
  await waitState(f,A,'unknown')
  assert.equal(f.count('DELETE'),2)
  f.enqueue('GET',detailPath(A),ok(view(A,'CLOSING')))
  await f.page.getByRole('button',{name:'刷新状态',exact:true}).click()
  await waitState(f,A,'requested')
  await f.shot('01-requested-after-proxy-unknown')
  f.enqueue('GET',detailPath(A),err('SESSION_NOT_FOUND',404))
  await f.page.getByRole('button',{name:'刷新状态',exact:true}).click()
  await waitState(f,A,'ended')
  assert.equal(f.count('POST'),0)
  assert.equal((await f.snap()).ws.length,0)
  await f.page.getByRole('button',{name:'关闭标签',exact:true}).click()
  await f.manager()
  assert.equal(await f.row(A).getByRole('button',{name:'打开',exact:true}).isDisabled(),true)
  assert.equal(await f.row(A).getByRole('button',{name:'关闭',exact:true}).isDisabled(),true)
  await f.shot('02-ended-tombstone-stale-row')
})

test('S7-B02 candidate belongs to Pane after leaving list; confirm once, close stops fixture WS; two Sessions isolated',async t=>{
  const f=await fixture(t,[view(),view(B)])
  await f.open()
  await f.page.getByRole('button',{name:'确认接管',exact:true}).waitFor()
  assert.equal(f.count('GET',detailPath(A)),1)
  assert.equal(f.count('POST'),0)
  // openSession already unmounted the manager while the real candidate was preparing.
  assert.equal(await f.page.locator('.session-manager').count(),0)
  assert.equal((await f.snap()).panes[0].takeover,'confirming')
  await f.page.getByRole('button',{name:'确认接管',exact:true}).click()
  await f.page.locator('.connection-state').filter({hasText:'已连接'}).waitFor()
  assert.equal(f.count('POST',detailPath(A)+'/takeover'),1)
  assert.deepEqual(f.requests.find(r=>r.path.endsWith('/takeover')).body,{mode:'takeover',expected_generation:5})
  assert.equal((await f.snap()).xterms,1)
  await f.manager();await f.open(B)
  await f.page.getByRole('button',{name:'确认接管',exact:true}).waitFor()
  await f.page.getByRole('button',{name:'取消',exact:true}).click()
  await f.manager()
  await closeRow(f,A)
  await waitState(f,A,'requested')
  let s=await f.snap()
  assert.equal(s.ws.length,1);assert.equal(s.ws[0].closeCalls,1)
  assert.equal(s.panes.find(p=>p.id===B).close,'idle')
  assert.equal(s.panes.find(p=>p.id===B).takeover,'idle')
  await f.manager();await closeRow(f,B);await waitState(f,B,'requested')
  assert.equal(f.count('DELETE',detailPath(A)),1);assert.equal(f.count('DELETE',detailPath(B)),1)
  assert.equal(f.count('POST'),1)
  await f.shot('03-multiple-panes-isolated')
})

test('S7-B03 real router logout failure with pending DELETE, late AUTH_REQUIRED, retry success',async t=>{
  const f=await fixture(t), g=gate()
  await f.open();await f.page.getByRole('button',{name:'确认接管',exact:true}).waitFor()
  await f.page.getByRole('button',{name:'确认接管',exact:true}).click()
  await f.page.locator('.connection-state').filter({hasText:'已连接'}).waitFor()
  await f.manager();f.enqueue('DELETE',detailPath(A),g.promise)
  await closeRow(f);await waitState(f,A,'pending')
  await f.manager()
  const before=f.requests.length
  f.enqueue('POST','/api/auth/logout',err('STORAGE_ERROR',500))
  await f.page.getByRole('button',{name:'退出登录',exact:true}).click()
  await f.page.getByText(/退出未完成，请重试/).waitFor()
  assert.equal(new URL(f.page.url()).pathname,'/')
  assert.equal((await f.snap()).panes.length,1)
  await f.shot('04-logout-failure-retry')
  // Existing request's AUTH_REQUIRED side effect must not turn failed logout into success.
  g.resolve(err('AUTH_REQUIRED',401))
  await f.page.evaluate(async()=>{const m=await import('/src/api/management.ts');m.notifyAuthExpired()})
  assert.equal(new URL(f.page.url()).pathname,'/')
  await f.page.getByRole('button',{name:'重试退出登录',exact:true}).click()
  await f.page.waitForURL('**/login')
  assert.equal(f.count('DELETE'),1)
  assert.deepEqual(f.requests.slice(before).map(r=>r.method+' '+r.path),['POST /api/auth/logout','POST /api/auth/logout','GET /api/auth/me'])
  assert.equal((await f.snap()).panes.length,0)
  assert.equal((await f.snap()).ws.length,1)
  assert.equal((await f.snap()).ws[0].closeCalls,1)
  await f.shot('05-logout-success-real-router')
})

test('S7-B04 rapid page reentry with idle+CLOSING / network error never confirms or prepares',async t=>{
  const f=await fixture(t)
  for(const [name,reply] of [['CLOSING',ok(view(A,'CLOSING'))],['network-error',{abort:true}]]) {
    await f.page.reload();await f.manager()
    f.enqueue('GET',detailPath(A),reply)
    await f.open()
    await f.page.getByText(name==='CLOSING'?'Session 当前状态不能接管':'无法连接服务，请重试',{exact:false}).waitFor()
    const s=await f.snap()
    assert.equal(s.panes.length,1);assert.equal(s.panes[0].life,'ready');assert.equal(s.panes[0].close,'idle')
    assert.equal(await f.page.getByRole('button',{name:'确认接管',exact:true}).count(),0)
    assert.equal(s.ws.length,0);assert.equal(f.count('POST'),0)
    await f.shot('06-reentry-'+name)
  }
})

test('S7-B05 pending candidate detail survives list exit; whole page exit discards old query, new Pane separate',async t=>{
  const f=await fixture(t), g=gate()
  f.enqueue('GET',detailPath(A),g.promise)
  await f.open()
  await f.page.getByText('正在查询 Session 状态',{exact:true}).waitFor()
  await f.manager()
  assert.equal((await f.snap()).panes[0].takeover,'loading')
  await f.page.getByRole('button',{name:'返回服务器',exact:true}).click()
  g.resolve(ok(view()))
  await f.page.getByRole('button',{name:'确认接管',exact:true}).waitFor()
  assert.equal(f.count('GET',detailPath(A)),1)
  assert.equal(f.count('POST'),0)
  await f.page.reload();await f.manager()
  const old=gate();f.enqueue('GET',detailPath(A),old.promise)
  await f.open();await f.page.getByText('正在查询 Session 状态',{exact:true}).waitFor()
  await f.page.reload();await f.manager();await f.open()
  await f.page.getByRole('button',{name:'确认接管',exact:true}).waitFor()
  old.resolve(err('SESSION_NOT_FOUND',404))
  const s=await f.snap()
  assert.equal(s.panes.length,1);assert.equal(s.panes[0].takeover,'confirming');assert.equal(s.panes[0].close,'idle')
  assert.equal(f.count('GET',detailPath(A)),3);assert.equal(f.count('POST'),0);assert.equal(s.ws.length,0)
  await f.shot('07-new-page-candidate')
})


test('S7-B06 failed takeover retries attach with same generation; real xterm output/resize retained',async t=>{
  const f=await fixture(t)
  await f.open()
  await f.page.getByRole('button',{name:'确认接管',exact:true}).waitFor()
  f.enqueue('POST',detailPath(A)+'/takeover',err('BIND_BUSY',409))
  await f.page.getByRole('button',{name:'确认接管',exact:true}).click()
  await f.page.locator('.connection-state').filter({hasText:'已连接'}).waitFor()
  const credentials=f.requests.filter(r=>r.path.endsWith('/takeover'))
  assert.deepEqual(credentials.map(r=>r.body),[
    {mode:'takeover',expected_generation:5},{mode:'attach',expected_generation:5},
  ])
  assert.equal(f.count('GET',detailPath(A)),1)
  assert.equal((await f.snap()).ws.length,1)
  f.wsPeers[0].send(Buffer.from('S7 integration 中文\r\n\x1b[32mANSI OK\x1b[0m\r\n'))
  await f.page.waitForFunction(()=>{
    const term=document.querySelector('.terminal-pane').__vueParentComponent.setupState.terminal
    return Array.from({length:term.rows},(_,i)=>term.buffer.active.getLine(i)?.translateToString()).join('\n').includes('ANSI OK')
  })
  const before=await f.page.evaluate(()=>document.querySelector('.terminal-pane').__vueParentComponent.setupState.terminal.cols)
  await f.page.setViewportSize({width:1050,height:850})
  await f.page.waitForFunction(before=>document.querySelector('.terminal-pane').__vueParentComponent.setupState.terminal.cols!==before,before)
  assert.equal((await f.snap()).xterms,1)
  await f.shot('08-retry-attach-xterm-output')
  await f.page.getByRole('button',{name:'关闭标签',exact:true}).click()
  await f.page.locator('.terminal-pane').waitFor({state:'detached'})
  assert.equal(f.count('DELETE'),0)
})

test('S7-B07 old DELETE across page reload cannot restore local intent or automatically takeover',async t=>{
  const f=await fixture(t), old=gate()
  f.enqueue('DELETE',detailPath(A),old.promise)
  await closeRow(f)
  assert.equal(f.count('DELETE'),1)
  await f.page.reload();await f.manager()
  f.enqueue('GET',detailPath(A),ok(view(A,'CLOSING')))
  await f.open()
  await f.page.getByText('Session 当前状态不能接管',{exact:true}).waitFor()
  old.resolve(ok({}))
  const s=await f.snap()
  assert.equal(s.panes[0].close,'idle');assert.equal(s.panes[0].takeover,'idle')
  assert.equal(f.count('POST'),0);assert.equal(s.ws.length,0);assert.equal(f.count('DELETE'),1)
  await f.shot('09-reload-old-delete-closing')
})
