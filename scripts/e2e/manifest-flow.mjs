// GoTTY 浏览器级 e2e —— 主流程:boot → 新建 → 销毁 → 清单清理 → 刷新恢复
// 前置:服务运行在 8080;headless Chrome CDP 9222(--user-data-dir=/tmp/chrome-gotty)
// 运行:node scripts/e2e/manifest-flow.mjs
// CDP 连接模板见 docs/e2e-testing.md

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
// Page.reload 必须带 ignoreCache:location.reload() 会命中 HTTP 缓存导致旧 bundle
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
// 条件轮询断言;不要用固定 sleep 猜测时序
const waitFor = async (desc, cond, timeoutMs = 12000) => {
    const end = Date.now() + timeoutMs
    while (Date.now() < end) {
        const v = await evalJS(cond)
        if (v) return v
        await sleep(300)
    }
    throw new Error('timeout waiting: ' + desc)
}
const MANIFEST_LEN = `JSON.parse(localStorage.getItem('gotty.sessions')).length`

// ⚠️ e2e 前清理 localStorage:避免与用户浏览器同 id 抢占(1013)
await evalJS(`localStorage.clear(); sessionStorage.clear(); true`)
await hardReload()

// boot:空清单 → 自动创建默认会话
await waitFor('boot manifest', `localStorage.getItem('gotty.sessions')`)
console.log('STEP boot: entries =', await evalJS(MANIFEST_LEN), '| tabs =', await evalJS(`document.querySelectorAll('.tab').length`))

// ＋ 新建第二个会话
await evalJS(`document.querySelector('.tab-actions-left .icon-btn').click(); true`)
await waitFor('second entry', `${MANIFEST_LEN} === 2`)
console.log('STEP create: entries = 2 | tabs =', await evalJS(`document.querySelectorAll('.tab').length`))

// 销毁第一个页签:服务端销毁 + 清单条目被移除(无历史残留)
const idsBefore = await evalJS(`JSON.parse(localStorage.getItem('gotty.sessions')).map(e => e.id)`)
await evalJS(`document.querySelector('.tab .tab-close').click(); true`)
await waitFor('manifest shrinks', `${MANIFEST_LEN} === 1`)
const sameIds = JSON.stringify(idsBefore) ===
    JSON.stringify(await evalJS(`JSON.parse(localStorage.getItem('gotty.sessions')).map(e => e.id)`))
console.log('STEP destroy: tabs =', await evalJS(`document.querySelectorAll('.tab').length`), '| manifest kept =', sameIds)

// 一轮轮询后无僵尸条目、无历史 UI
await sleep(2500)
console.log('STEP poll: tabs =', await evalJS(`document.querySelectorAll('.tab').length`), '| entries =', await evalJS(MANIFEST_LEN))

// 刷新恢复:最近存活会话重新打开
await hardReload()
await sleep(1200)
console.log('STEP reload: tabs =', await evalJS(`document.querySelectorAll('.tab').length`), '| entries =', await evalJS(MANIFEST_LEN))

ws.close()
console.log('E2E DONE')