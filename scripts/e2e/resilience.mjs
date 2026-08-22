// GoTTY 浏览器级 e2e —— 韧性:空态卡片创建 / 会话已销毁自动重建
// 前置:服务 8080;headless Chrome CDP 9222。运行:node scripts/e2e/resilience.mjs

const list = await fetch('http://127.0.0.1:9222/json').then((r) => r.json())
const page = list.find((t) => t.type === 'page' && t.url.startsWith('http://127.0.0.1:8080'))
if (!page) {
    console.error('no page target at 127.0.0.1:8080')
    process.exit(1)
}

const ws = new WebSocket(page.webSocketDebuggerUrl)
let seq = 0
const pending = new Map()
ws.onmessage = (e) => {
    const m = JSON.parse(e.data)
    if (m.id && pending.has(m.id)) {
        pending.get(m.id)(m)
        pending.delete(m.id)
    }
}
await new Promise((res, rej) => {
    ws.onopen = res
    ws.onerror = rej
})
const send = (method, params = {}) =>
    new Promise((res) => {
        const i = ++seq
        pending.set(i, res)
        ws.send(JSON.stringify({ id: i, method, params }))
    })
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const hardReload = async (waitMs = 1500) => {
    await send('Network.enable')
    await send('Network.setCacheDisabled', { cacheDisabled: true })
    await send('Page.enable')
    await send('Page.reload', { ignoreCache: true })
    await sleep(waitMs)
}
const evalJS = async (expression) => {
    const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true })
    if (r.result?.exceptionDetails) {
        throw new Error('eval failed: ' + JSON.stringify(r.result.exceptionDetails).slice(0, 200))
    }
    return r.result?.result?.value
}
const waitFor = async (desc, cond, timeoutMs = 12000) => {
    const end = Date.now() + timeoutMs
    while (Date.now() < end) {
        const v = await evalJS(cond)
        if (v) return v
        await sleep(300)
    }
    throw new Error('timeout waiting: ' + desc)
}
// 服务端 status 批量查询(与前端 checkSessions 同语义)
const aliveCount = `(async () => {
  const ids = JSON.parse(localStorage.getItem('gotty.sessions')).map(e => e.id)
  const r = await fetch('/api/sessions/status',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({ids})})
  const d = await r.json()
  return Object.keys(d.sessions ?? {}).length
})()`

// ── 1. 空态卡片:清单有 id 但服务端无存活 → 空态 → 点击创建 ──
await evalJS(`localStorage.clear(); sessionStorage.clear(); true`)
// 写入一个服务端不存在的伪条目,让 boot 落空(空清单会走自动创建,测不到空态)
await evalJS(`localStorage.setItem('gotty.sessions', JSON.stringify([{id:'zz99999999999999',command:'/bin/bash',args:[],createdAt:Date.now(),lastSeen:Date.now()}])); true`)
await hardReload()
const card = await waitFor('empty card', `!!document.querySelector('.empty-card')`)
console.log('EMPTY CARD visible:', card)
await evalJS(`document.querySelector('.empty-card').click(); true`)
await waitFor('tab created by card', `document.querySelectorAll('.tab').length >= 1`, 15000)
console.log('CARD CLICK: tabs =', await evalJS(`document.querySelectorAll('.tab').length`), '| entries =', await evalJS(`JSON.parse(localStorage.getItem('gotty.sessions')).length`))

// ── 2. 会话已销毁 → 已打开视图重连时自动重建(服务端复活),不再报"已销毁" ──
const sid = await evalJS(`JSON.parse(localStorage.getItem('gotty.sessions'))[0].id`)
// 服务端销毁该会话:pane 的 WS 会被关闭,显示"连接已断开"弹窗
await evalJS(`(async () => { await fetch('/api/sessions/' + '${sid}', { method: 'DELETE' }); return true })()`)
const goneMsg = await waitFor('disconnect overlay', `!!document.querySelector('.pane-overlay')`, 8000)
console.log('GONE overlay shown:', goneMsg)
// 点"重新连接":resolveSession 发现已销毁 → rebuild(服务端复活)
await evalJS(`[...document.querySelectorAll('.pane-overlay button')].find(b => b.textContent.includes('重新连接'))?.click(); true`)
// 断言用页面态(重建与清单清理有竞态,清单内 status 断言不可靠):
// overlay 消失 = 重新连接成功;pane 重建后 WS 连接,圆点即时变绿
await waitFor('overlay gone', `!document.querySelector('.pane-overlay')`, 10000)
console.log('REBUILD: overlay dismissed, tabs =', await evalJS(`document.querySelectorAll('.tab').length`))
await waitFor('dot green', `document.querySelector('.tab .state-dot')?.classList.contains('dot-running')`, 8000)
console.log('DOT green after rebuild ✓')

ws.close()
console.log('E2E DONE')