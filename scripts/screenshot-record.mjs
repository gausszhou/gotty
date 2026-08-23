// GoTTY README 截图录制器 —— 浏览器级交互流程 GIF 生成(零第三方依赖,Node 18+)
//
// 沉淀为可复用脚本:录制"基本功能交互流程"(空态 → 创建 → 键入 → 多页签 → 切换
// → 销毁 → 刷新恢复),产出连续 PNG 帧 + 关键态快照 + 可选 GIF(screenshot.gif)。
//
// 前置条件:
//   1) 构建并启动最新服务(端口与 APP 常量一致,默认 8081):
//        make build
//        ./build/gotty serve --address 127.0.0.1 --port 8081 --session-file .tmp/shot/sessions.json
//   2) headless Chrome 调试端口 9223(独立用户目录)。必须 --disable-webgl:
//      无头环境第二个 WebGL 画布不绘制(双 WebGL 上下文问题),禁用后 xterm 回退
//      DOM 渲染器,两个终端都能渲染、且内容可经 .xterm-rows 读取用于断言。
//        google-chrome --headless=new --no-sandbox --disable-dev-shm-usage \
//          --disable-webgl --disable-background-networking \
//          --remote-debugging-port=9223 --user-data-dir=.tmp/shot/chrome-profile \
//          --window-size=1280,720 --force-device-scale-factor=1 --hide-scrollbars about:blank
//
// 用法:
//   node scripts/screenshot-record.mjs          # 只录制帧(默认)
//   node scripts/screenshot-record.mjs --gif    # 录制后调用 ImageMagick 合成 GIF
//
// 产物:.tmp/shot/frames/{frame-*,snap-*}.png;--gif 时额外写出 screenshot.gif。
// 断言:每个关键态做 DOM 内容断言(读取可见 pane 的 .xterm-rows 文本),任一失败
//       抛错退出非 0,便于在 CI 或本地快速回归。

import { writeFileSync, mkdirSync, rmSync } from 'node:fs'
import { execSync } from 'node:child_process'

const APP = 'http://127.0.0.1:8081/'
const CDP = 'http://127.0.0.1:9223'
const FRAME_MS = 125 // 捕获间隔 ≈ 8fps
const OUT = '.tmp/shot/frames'
const GIF_OUT = 'screenshot.gif'
const DO_GIF = process.argv.includes('--gif')

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function findPage() {
    for (let i = 0; i < 160; i++) {
        try {
            const list = await fetch(`${CDP}/json`).then((r) => r.json())
            const page = list.find((t) => t.type === 'page')
            if (page) return page
        } catch { /* chrome 未就绪 */ }
        await sleep(250)
    }
    throw new Error(`no chrome page target on ${CDP}`)
}

const page = await findPage()
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
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej })

const send = (method, params = {}) =>
    new Promise((res, rej) => {
        const i = ++seq
        const guard = setTimeout(() => {
            pending.delete(i)
            rej(new Error(`no response for ${method} within 10s`))
        }, 10000)
        pending.set(i, (m) => {
            clearTimeout(guard)
            res(m)
        })
        ws.send(JSON.stringify({ id: i, method, params }))
    })
const evalJS = async (expression) => {
    const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true })
    if (r.result?.exceptionDetails) {
        throw new Error('eval failed: ' + JSON.stringify(r.result.exceptionDetails).slice(0, 300))
    }
    return r.result?.result?.value
}
const waitFor = async (desc, cond, timeoutMs = 15000) => {
    const end = Date.now() + timeoutMs
    while (Date.now() < end) {
        const v = await evalJS(cond)
        if (v) return v
        await sleep(250)
    }
    throw new Error('timeout waiting: ' + desc)
}
const hardReload = async () => {
    await send('Network.enable')
    await send('Network.setCacheDisabled', { cacheDisabled: true })
    await send('Page.enable')
    await send('Page.reload', { ignoreCache: true })
}

// ── 键鼠输入 ──
// 关键经验:无头 Chrome 下逐字符 Input.dispatchKeyEvent / 合成 keydown 会被 xterm
// 误判(小写 p-z 变 '~'、功能键序列),唯有 Input.insertText 上行字节干净
// (已用 WS 探针验证:'1','p')。因此可打印字符一律 insertText,Enter 用
// dispatchKeyEvent(验证干净)。
async function typeText(text, msPerChar = 80) {
    // 只聚焦当前可见 pane 的 textarea:v-show 隐藏的 pane 仍在 DOM 中,
    // querySelector 会错选第一个隐藏实例,输入会进错终端。
    const ok = await evalJS(`(() => {
        const p = [...document.querySelectorAll('.terminal-pane')].find(el => el.style.display !== 'none');
        const ta = p ? p.querySelector('.xterm-helper-textarea') : null;
        if (ta) ta.focus();
        return !!ta;
    })()`)
    if (!ok) throw new Error('no visible xterm textarea to focus')
    for (const ch of text) {
        await send('Input.insertText', { text: ch })
        await sleep(msPerChar)
    }
}
async function pressEnter() {
    const common = { key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 }
    await send('Input.dispatchKeyEvent', { type: 'keyDown', ...common })
    await send('Input.dispatchKeyEvent', { type: 'keyUp', ...common })
}

// ── 帧捕获 + 关键态快照 + DOM 断言 ──
rmSync(OUT, { recursive: true, force: true })
mkdirSync(OUT, { recursive: true })
const t0 = Date.now()
const frames = []
let capturing = true
async function captureLoop() {
    while (capturing) {
        try {
            const r = await send('Page.captureScreenshot', { format: 'png', fromSurface: true })
            frames.push({ t: Date.now() - t0, data: r.result.data })
        } catch (e) { console.error('CAPERR', e?.message ?? e) }
        await sleep(FRAME_MS)
    }
}
const snap = async (name) => {
    const r = await send('Page.captureScreenshot', { format: 'png', fromSurface: true })
    writeFileSync(`${OUT}/snap-${name}.png`, Buffer.from(r.result.data, 'base64'))
    console.log(`SNAP ${name} @ ${Date.now() - t0}ms`)
}
const step = (name) => console.log(`STEP ${name} @ ${Date.now() - t0}ms (frames=${frames.length})`)

// 可见 pane 的终端行文本(DOM 渲染器)。断言基准:任何页面断言都用 DOM,不做截图目检。
const activeRows = async () =>
    await evalJS(`(() => {
        const p = [...document.querySelectorAll('.terminal-pane')].find(el => el.style.display !== 'none');
        return p ? [...p.querySelectorAll('.xterm-rows > div')].map((r) => r.textContent) : [];
    })()`)
async function assertRows(desc, mustContain = []) {
    const rows = await activeRows()
    const text = rows.join('\n')
    const missing = mustContain.filter((s) => !text.includes(s))
    console.log(`ASSERT ${desc}: rows=${rows.length}` +
        (rows.length ? ` last:\n${rows.slice(-8).map((r) => '      | ' + r).join('\n')}` : ' (empty)'))
    if (missing.length) throw new Error(`assert failed [${desc}]: missing ${JSON.stringify(missing)}`)
}

// ── 主流程:空态 → 创建 → 键入 → 多页签 → 切换 → 销毁 → 刷新恢复 ──
await send('Network.enable')
await send('Network.setCacheDisabled', { cacheDisabled: true })
await send('Page.enable')
// 强制视口尺寸(--window-size 在 headless 中不可靠)
await send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 720, deviceScaleFactor: 1, mobile: false })
await send('Page.navigate', { url: APP })
await waitFor('app loaded', `document.readyState === 'complete' && !!document.querySelector('#app')`)
// 清空本地状态保证空态开场(about:blank 下 localStorage 不可读,必须先导航)
await evalJS(`localStorage.clear(); sessionStorage.clear(); true`)
await hardReload()

// 1. boot:空清单停在空态卡片
await waitFor('empty card', `!!document.querySelector('.empty-card')`)
void captureLoop() // 页面稳定后再开捕获:首个截图撞上导航会挂死(见 send 超时兜底)
step('boot:empty-card')
await sleep(1500)
await snap('boot-empty-card')

// 2. 点击卡片创建第一个会话(默认命令 $SHELL)
await evalJS(`document.querySelector('.empty-card').click(); true`)
await waitFor('tab1', `document.querySelectorAll('.tab').length === 1`)
await waitFor('pane1 rows', `(() => {
    const p = [...document.querySelectorAll('.terminal-pane')].find(el => el.style.display !== 'none');
    return !!p && p.querySelectorAll('.xterm-rows > div').length > 0;
})()`)
step('session1-attached')
await sleep(1800)
await snap('session1-attached')
await assertRows('tab1 prompt', ['$'])

// 3. 键入命令:登录 shell 起始于 $HOME,先 cd 进本仓库再列出,输出稳定且不滚动出屏
await typeText('cd Code/gausszhou/gotty', 70)
await sleep(200)
await pressEnter()
await sleep(1000)
await assertRows('prompt shows repo cwd', ['gotty'])
await typeText('ls -la | head -18', 70)
await sleep(300)
await pressEnter()
await sleep(1700)
await snap('tab1-ls-output')
await assertRows('tab1 ls output', ['ls -la', 'total', 'README.md'])
step('ls-output')

// 4. 顶部 ＋ 新建第二个会话,并执行 echo
await evalJS(`document.querySelector('.tab-actions-left .icon-btn').click(); true`)
await waitFor('tab2', `document.querySelectorAll('.tab').length === 2`)
await waitFor('pane2 prompt', `(() => {
    const p = [...document.querySelectorAll('.terminal-pane')].find(el => el.style.display !== 'none');
    return !!p && p.querySelectorAll('.xterm-rows > div').length > 0;
})()`)
step('tab2-attached')
await sleep(1500)
await snap('tab2-attached')
await assertRows('tab2 fresh prompt (no ls output)', ['$'])
await typeText('echo "Hello GoTTY"')
await sleep(300)
await pressEnter()
await sleep(1500)
await snap('tab2-output')
const tab2Rows = await activeRows()
if (!tab2Rows.join('\n').includes('Hello GoTTY')) {
    throw new Error('assert failed: echo output missing on tab2: ' + JSON.stringify(tab2Rows))
}
console.log('ASSERT tab2 echo output: PASS (Hello GoTTY on tab2)')
step('tab2-output')

// 5. 切回第一个页签(多会话切换,屏幕内容保留)
await evalJS(`document.querySelectorAll('.tab')[0].click(); true`)
await sleep(1300)
await snap('switch-back-tab1')
await assertRows('tab1 still shows ls output after switching', ['ls -la', 'total'])
step('switch-tab1')

// 6. 销毁第二个页签(活动会话保持 tab1)
await evalJS(`document.querySelectorAll('.tab')[1].querySelector('.tab-close').click(); true`)
await waitFor('tab1only', `document.querySelectorAll('.tab').length === 1`)
await sleep(1400)
await snap('tab2-destroyed')
await assertRows('tab1 intact after destroy', ['ls -la', 'total'])
step('tab2-destroyed')

// 7. 刷新 → 恢复最近存活会话(同 id 重附着,输出重放回同屏)
await hardReload()
await waitFor('resume tab', `document.querySelectorAll('.tab').length === 1`)
await waitFor('resume rows', `(() => {
    const p = document.querySelector('.terminal-pane');
    return !!p && p.querySelectorAll('.xterm-rows > div').length > 0;
})()`, 8000)
await sleep(2000)
await snap('resumed-after-reload')
await assertRows('resume replays same screen', ['ls -la', 'total'])
step('resumed-after-reload')

capturing = false
await sleep(300)
ws.close()

let i = 0
for (const f of frames) {
    writeFileSync(`${OUT}/frame-${String(i++).padStart(4, '0')}.png`, Buffer.from(f.data, 'base64'))
}
console.log(`DONE frames=${frames.length} -> ${OUT}/`)

if (DO_GIF) {
    console.log('assembling GIF via ImageMagick...')
    // 注意:IM7 的 -layers Optimize 会把帧延迟清零;-delay 必须放在输入文件之前
    execSync(`magick -delay 12 ${OUT}/frame-*.png -colors 256 -layers Optimize ${GIF_OUT}`, {
        shell: '/bin/bash',
        stdio: 'inherit',
    })
    console.log(`GIF written: ${GIF_OUT}`)
}