<template>
  <!-- 截图专用渲染页:纯终端,无 TabBar/清单/轮询。加载蒙层放容器外,
       保证截图目标(.terminal)纯净。 -->
  <div class="capture-page">
    <div v-if="!ready && !error" class="capture-loading">rendering…</div>
    <div v-if="error" class="capture-error">{{ error }}</div>
    <Terminal ref="terminalRef" :dom-renderer="true" class="capture-terminal" @title="(t) => (title = t)" />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import Terminal from './Terminal.vue'
import { openTerminalWS, type TermHandle } from '../utils/ws'
import { getSession } from '../utils/api'
import { logger } from '../utils/logger'

// 无头浏览器(capture --engine browser)轮询的全局握手信号
declare global {
    interface Window {
        __gottyCaptureReady?: boolean
        __gottyCaptureError?: string
        __gottyLastActivity?: number
        __gottyTextTail?: string
    }
}

const route = useRoute()
const terminalRef = ref<InstanceType<typeof Terminal> | null>(null)
const ready = ref(false)
const error = ref('')
const title = ref('')

// 截图页固定深色(见 docs/design/capture-design.md §7.2):与 .capture-page 的
// 终端底色一致(两者都取 --bg-terminal),也不受"跟随系统"影响
// (headless Chrome 的系统色是浅色)。
// 直接写 data-theme 而不走 applyTheme —— 截图的浏览器 profile 不写用户偏好。
// 时机安全:父组件 setup 先于子组件 Terminal 的挂载,后者从 CSS 变量取终端配色。
document.documentElement.dataset.theme = 'dark'

// 文本尾窗(去二进制解码的有界窗口),供 --marker 判定
const TAIL_LIMIT = 4096
const decoder = new TextDecoder()

function fail(msg: string) {
    logger.warn('capture', 'error: %s', msg)
    error.value = msg
    window.__gottyCaptureError = msg
}

onMounted(async () => {
    const sid = String(route.params.sid || '')
    if (!sid) {
        fail('missing session id in /capture/:sid')
        return
    }

    // 会话须存活;不存在则置错误信号,chromedp 可区分就绪/失败
    const s = await getSession(sid)
    if (!s) {
        fail('session not found')
        return
    }

    const cols = Number(route.query.cols) || 120
    const rows = Number(route.query.rows) || 30
    window.__gottyLastActivity = Date.now()
    window.__gottyTextTail = ''

    const handle: TermHandle = {
        info: () => ({ columns: cols, rows }),
        write: (data) => {
            terminalRef.value?.write(data)
            window.__gottyLastActivity = Date.now()
            const tail = (window.__gottyTextTail || '') + decoder.decode(data)
            window.__gottyTextTail = tail.slice(-TAIL_LIMIT)
        },
        setWindowTitle: (_t) => {},
        reset: () => terminalRef.value?.reset(),
        deactivate: () => {},
        onInput: () => {},
        onResize: () => {},
        onWriteParsed: (cb) => terminalRef.value?.onWriteParsed(cb),
    }

    const ws = openTerminalWS(handle, sid, {
        // 收到服务端握手标记即"渲染就绪":chromedp 轮询此标志后开始判定
        onReady: () => {
            window.__gottyCaptureReady = true
            ready.value = true
        },
        onDisconnect: (msg) => {
            // 正常断开(进程退出)不算错误;握手前断开才是
            if (!window.__gottyCaptureReady) fail(msg)
        },
    })
    wsRef.value = ws

    // 标题:程序 OSC 0/2 写入 document.title(无头页面调试用)
    window.document.title = title.value || `capture ${sid}`
})

const wsRef = ref<ReturnType<typeof openTerminalWS> | null>(null)

onBeforeUnmount(() => {
    wsRef.value?.close()
})
</script>

<style scoped>
.capture-page {
    position: fixed;
    inset: 0;
    background: var(--bg-terminal); /* 与终端面同色,避免题图边缘出现黑边 */
    overflow: hidden;
}

.capture-terminal {
    position: absolute;
    inset: 0;
}

.capture-loading {
    position: absolute;
    top: 8px;
    left: 8px;
    z-index: 10;
    color: var(--fg-3);
    font: var(--font-size-sm) / 1.4 var(--font-mono);
}

.capture-error {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--error); /* errorForeground */
    font: var(--font-size-md) / 1.6 var(--font-mono);
}
</style>