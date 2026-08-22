<template>
  <div class="app">
    <!-- 顶部:会话页签栏 -->
    <TabBar
      :active-session-id="activeSession?.id"
      :latency="activeSession ? latency : null"
      :jitter="activeSession ? jitter : null"
      @open="openSession"
      @destroy="destroyFromTab"
    />

    <!-- 内容区:常驻视图(每会话一个,懒创建,v-show 显隐,连接保持) -->
    <div class="content">
      <TerminalPane
        v-for="v in openedViews"
        v-show="activeSession?.id === v.id"
        :key="v.id"
        :session-id="v.id"
        :active="activeSession?.id === v.id"
        @close="closeView(v.id)"
        @latency="onLatency(v.id, $event)"
      />
      <div v-if="!openedViews.length" class="content-empty">
        <div v-if="bootError" class="empty-error">{{ bootError }}</div>
        <template v-else>
          <div class="empty-title">没有打开的终端</div>
          <div class="empty-hint">点击顶部 ＋ 新建会话,或从 ▾ 历史重新运行</div>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import TabBar from './components/TabBar.vue'
import TerminalPane from './components/TerminalPane.vue'
import { createSession, destroySession, listSessions, type SessionInfo } from './utils/api'
import { logger } from './utils/logger'

// 会话 id 保存在 sessionStorage:刷新页面恢复同一会话;
// 不进入 URL,不在界面展示。
const STORAGE_KEY = 'gotty.sessionId'

const activeSession = ref<SessionInfo | null>(null)
// 当前会话的实测 RTT(毫秒)与抖动,展示在标题栏右侧(颜色分级)
const latency = ref<number | null>(null)
const jitter = ref<number | null>(null)
const bootError = ref('')

// RTT 采样历史(抖动 = 相邻采样差均值的绝对值)
const rttSamples = ref<number[]>([])
const JITTER_WINDOW = 8

function pushSample(ms: number) {
    rttSamples.value.push(ms)
    if (rttSamples.value.length > JITTER_WINDOW) {
        rttSamples.value.shift()
    }
    const samples = rttSamples.value
    if (samples.length < 2) {
        jitter.value = null
        return
    }
    let sum = 0
    for (let i = 1; i < samples.length; i++) {
        sum += Math.abs(samples[i] - samples[i - 1])
    }
    jitter.value = Math.round(sum / (samples.length - 1))
}

function resetLatency() {
    latency.value = null
    jitter.value = null
    rttSamples.value = []
}

// 常驻视图:打开过的会话各保留一个 TerminalPane(v-show 显隐,连接不断)
const openedViews = ref<SessionInfo[]>([])
const openedIds = new Set<string>()

function openView(s: SessionInfo) {
    if (openedIds.has(s.id)) return
    openedIds.add(s.id)
    openedViews.value.push(s)
}

function removeView(id: string) {
    openedIds.delete(id)
    openedViews.value = openedViews.value.filter((v) => v.id !== id)
}

function loadStoredId(): string | null {
    try {
        return sessionStorage.getItem(STORAGE_KEY)
    } catch {
        return null
    }
}

function saveStoredId(id: string) {
    try {
        sessionStorage.setItem(STORAGE_KEY, id)
    } catch {
        // sessionStorage 不可用时静默降级
    }
}

function clearStoredId() {
    try {
        sessionStorage.removeItem(STORAGE_KEY)
    } catch {
        // ignore
    }
}

// 打开一个会话:懒创建常驻视图并激活(已打开的只切换)
function openSession(detail: { session: SessionInfo; title: string }) {
    const s = detail.session
    if (s.state === 'destroyed' || s.exited) return
    logger.info('app', 'open session=%s title=%s', s.id, detail.title)
    openView(s)
    activeSession.value = s
    resetLatency()
    saveStoredId(s.id)
}

// 延迟/抖动只展示当前激活会话的实测值,其他视图的测量忽略
function onLatency(viewId: string, ms: number | null) {
    if (activeSession.value?.id !== viewId) return
    if (ms === null) {
        resetLatency()
        return
    }
    latency.value = ms
    pushSample(ms)
}

// 关闭某视图(仅解除视图,会话在服务端保留)
function closeView(id: string) {
    logger.info('app', 'close view session=%s (session stays server-side)', id)
    removeView(id)
    if (activeSession.value?.id === id) {
        activeSession.value = null
        resetLatency()
        clearStoredId()
    }
}

// 页签销毁(服务端已删,历史保留):同步移除常驻视图
function destroyFromTab(s: SessionInfo) {
    closeView(s.id)
}

// 会话N 编号(按 created_at 升序),重命名/服务端标题优先
function autoTitle(s: SessionInfo, list: SessionInfo[]): string {
    if (s.title) return s.title
    const asc = [...list].sort((a, b) => (a.created_at > b.created_at ? 1 : -1))
    const idx = asc.findIndex((x) => x.id === s.id)
    return idx >= 0 ? `会话${idx + 1}` : s.command
}

// 启动:优先恢复 sessionStorage 中的会话;否则打开最近的活会话;
// 都没有则自动创建默认会话。
onMounted(async () => {
    try {
        let list: SessionInfo[] = []
        try {
            list = await listSessions()
        } catch {
            // 列表不可用时继续尝试创建
        }
        logger.info('app', 'boot: sessions=%d stored=%s', list.length, loadStoredId() ?? 'none')

        const stored = loadStoredId()
        let s: SessionInfo | null =
            (stored && list.find((x) => x.id === stored)) || null
        if (!s && list.length > 0) {
            s = [...list].sort((a, b) => (a.created_at < b.created_at ? 1 : -1))[0]
        }
        if (!s) {
            s = await createSession('')
            openSession({ session: s, title: `会话${list.length + 1}` })
            return
        }
        openSession({ session: s, title: autoTitle(s, list) })
    } catch (err) {
        bootError.value = err instanceof Error ? err.message : String(err)
    }
})
</script>

<style>
html, body, #app {
    margin: 0;
    padding: 0;
    height: 100%;
    width: 100%;
    background: #1e1e1e;
}

/* VSCode 风格字体栈 */
body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Helvetica Neue',
        sans-serif;
    color: #cccccc;
}

* {
    box-sizing: border-box;
}
</style>

<style scoped>
.app {
    display: flex;
    flex-direction: column;
    height: 100vh;
    width: 100vw;
    background: #1e1e1e;
    overflow: hidden;
}

.content {
    flex: 1 1 auto;
    min-height: 0;
    display: flex;
    background: #1e1e1e;
    padding: 0; /* 内容区无修饰,纯 xterm */
}

.content-empty {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    color: #6e7681;
}

.empty-title {
    font-size: 16px;
    color: #8b949e;
}

.empty-hint {
    font-size: 13px;
}

.empty-error {
    max-width: 320px;
    padding: 10px 16px;
    color: #f48771;
    font-size: 13px;
    text-align: center;
    line-height: 1.6;
}
</style>