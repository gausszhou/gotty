<template>
  <div class="app">
    <!-- 顶部:会话页签栏 -->
    <TabBar
      :entries="entries"
      :alive="aliveSessions"
      :connected="connected"
      :theme="theme"
      :active-session-id="activeSession?.id"
      :latency="activeSession ? latency : null"
      @open="openSession"
      @destroy="destroyFromTab"
      @create="createNewSession"
      @changed="refreshStatus"
      @theme="onToggleTheme"
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
        @conn="onConn(v.id, $event)"
        @tab-title="(t) => onTabTitle(v.id, t)"
      />
      <div v-if="!openedViews.length" class="content-empty">
        <div v-if="bootError" class="empty-error">{{ bootError }}</div>
        <!-- boot 进行中:显示连接占位,避免"创建终端会话"卡片一闪而过 -->
        <div v-else-if="booting" class="empty-loading">
          <span class="spinner" aria-hidden="true"></span>
          <span class="empty-loading-text">正在连接…</span>
        </div>
        <!-- 空态:居中小卡片(仅当确认没有可打开的会话时),点击直接创建终端会话 -->
        <button v-else class="empty-card" @click="createNewSession">
          <span class="empty-card-icon">＋</span>
          <span class="empty-card-title">创建终端会话</span>
          <span class="empty-card-hint">点击新建一个终端</span>
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import TabBar from './components/TabBar.vue'
import TerminalPane from './components/TerminalPane.vue'
import { createSession, checkSessions, type SessionInfo } from './utils/api'
import { currentTheme, toggleTheme, type Theme } from './utils/theme'
import {
    loadManifest, upsertManifest, touchManifest, removeFromManifest, generateSessionID, findManifestEntry,
    type ManifestEntry,
} from './utils/manifest'
import { logger } from './utils/logger'

const activeSession = ref<SessionInfo | null>(null)
// 当前会话的实测 RTT(毫秒),展示在标题栏右侧(颜色分级)
const latency = ref<number | null>(null)
const bootError = ref('')
// boot 进行中:内容区显示连接占位,避免空态卡片在恢复会话前一闪而过
const booting = ref(true)
// 当前主题(dark/light);main.ts 已在 mount 前应用持久化主题,
// 这里用 currentTheme() 同步初值,保证图标与实际主题一致。
const theme = ref<Theme>(currentTheme())

// 主题切换:TabBar 触发,切换 CSS 变量并广播给 xterm
function onToggleTheme() {
    theme.value = toggleTheme()
}

function resetLatency() {
    latency.value = null
}

// ── 会话清单(设备本地事实来源)+ 存活轮询 ──
// entries:localStorage 清单;aliveSessions:服务端存活的清单 id。
const entries = ref<ManifestEntry[]>([])
const aliveSessions = ref<SessionInfo[]>([])
// connected:各打开视图的 WS 连接状态(id → 是否已成功附着)。
// 页签圆点据此即时变绿,不等 status 轮询。
const connected = ref<Record<string, boolean>>({})

// onConn 记录/清除某视图的连接状态。
function onConn(viewId: string, isConnected: boolean) {
    connected.value = { ...connected.value, [viewId]: isConnected }
}

// onTabTitle:程序设置的标题(OSC 0/2)写入清单,页签随 entries 响应式更新;
// 标题归属"自动命名/程序更新"(GNOME-Shell 风格),不再支持手动重命名。
function onTabTitle(sessionId: string, title: string) {
    if (!title) return
    const entry = findManifestEntry(sessionId)
    if (entry) {
        upsertManifest({ ...entry, title })
        entries.value = loadManifest()
    }
}

// createNewSession:生成客户端 id → 创建(幂等/复活语义)→ 写清单 → 打开。
// 顶部 ＋ 按钮与空态卡片共用此入口(统一创建逻辑)。
async function createNewSession() {
    logger.info('app', 'create new session (default command)')
    const id = generateSessionID()
    const s = await createSession('', [], id, theme.value)
    upsertManifest({
        id: s.id,
        command: s.command,
        args: s.args,
        createdAt: Date.now(),
        lastSeen: Date.now(),
    })
    openSession({ session: s, title: '' })
    await refreshStatus()
}

const POLL_PERIOD_MS = 2000
let pollTimer: ReturnType<typeof setInterval> | null = null

// refreshStatus 从清单 + 服务端 status 拉取最新状态(TabBar 变更后/轮询)。
// 已无历史入口:服务端不再存活的清单条目(销毁/空闲淘汰/进程退出)
// 自动从本设备清单移除,清单始终 = "这台设备当前存活的会话"。
async function refreshStatus() {
    entries.value = loadManifest()
    const ids = entries.value.map((e) => e.id)
    try {
        const alive = await checkSessions(ids)
        aliveSessions.value = alive
        const aliveIds = new Set(alive.map((s) => s.id))
        const stale = entries.value.filter((e) => !aliveIds.has(e.id))
        if (stale.length > 0) {
            for (const e of stale) removeFromManifest(e.id)
            entries.value = loadManifest()
        }
    } catch {
        // 服务端不可用:保留旧存活列表,不清理
    }
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

// 打开一个会话:懒创建常驻视图并激活(已打开的只切换)
function openSession(detail: { session: SessionInfo; title: string }) {
    const s = detail.session
    if (s.state === 'destroyed' || s.exited) return
    logger.info('app', 'open session=%s title=%s', s.id, detail.title)
    openView(s)
    activeSession.value = s
    resetLatency()
    touchManifest(s.id) // 更新 lastSeen:boot 按最近打开恢复
}

// 延迟只展示当前激活会话的实测值,其他视图的测量忽略
function onLatency(viewId: string, ms: number | null) {
    if (activeSession.value?.id !== viewId) return
    if (ms === null) {
        resetLatency()
        return
    }
    latency.value = ms
}

// 关闭某视图(仅解除视图,会话在服务端与清单中保留)
function closeView(id: string) {
    logger.info('app', 'close view session=%s (session stays server-side)', id)
    removeView(id)
    delete connected.value[id]
    connected.value = { ...connected.value }
    if (activeSession.value?.id === id) {
        activeSession.value = null
        resetLatency()
    }
}

// 页签销毁(服务端已删,记录保留,清单保留 → 落入历史可重跑)
function destroyFromTab(s: SessionInfo) {
    closeView(s.id)
}

// 启动:读清单 → status 批量查 → 打开最近存活(lastSeen 最大);
// 清单为空(新设备)则自动创建默认会话。
onMounted(async () => {
    try {
        entries.value = loadManifest()

        if (entries.value.length === 0) {
            logger.info('app', 'boot: empty manifest, creating default session')
            const s = await createSession('')
            upsertManifest({
                id: s.id,
                command: s.command,
                args: s.args,
                createdAt: Date.now(),
                lastSeen: Date.now(),
            })
            await refreshStatus()
            openSession({ session: s, title: '会话1' })
            startPolling()
            return
        }

        logger.info('app', 'boot: manifest entries=%d', entries.value.length)
        await refreshStatus()

        // 最近存活的清单条目(按 lastSeen 降序)
        const aliveById = new Map(aliveSessions.value.map((s) => [s.id, s]))
        const recent = [...entries.value]
            .sort((a, b) => b.lastSeen - a.lastSeen)
            .find((e) => aliveById.has(e.id))
        if (recent) {
            openSession({ session: aliveById.get(recent.id)!, title: recent.title || '' })
        }
        // 无存活会话:留在空态,由用户新建(卡片在 booting=false 后显示)

        startPolling()
    } catch (err) {
        bootError.value = err instanceof Error ? err.message : String(err)
    } finally {
        booting.value = false
    }
})

function startPolling() {
    if (pollTimer) return
    pollTimer = setInterval(() => {
        void refreshStatus()
    }, POLL_PERIOD_MS)
}

onBeforeUnmount(() => {
    if (pollTimer) clearInterval(pollTimer)
})
</script>

<style>
html, body, #app {
    margin: 0;
    padding: 0;
    height: 100%;
    width: 100%;
    background: var(--bg-app);
}

/* VSCode 风格字体栈 */
body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Helvetica Neue',
        sans-serif;
    color: var(--fg);
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
    background: var(--bg-app);
    overflow: hidden;
}

.content {
    flex: 1 1 auto;
    min-height: 0;
    display: flex;
    background: var(--bg-app);
    padding: 0; /* 内容区无修饰,纯 xterm */
}

.content-empty {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    color: var(--fg-muted);
}

/* boot 连接占位:避免空态卡片一闪而过 */
.empty-loading {
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--fg-muted);
    font-size: 13px;
}

.empty-loading-text {
    color: var(--fg-hint);
}

/* 简易旋转指示器 */
.spinner {
    width: 14px;
    height: 14px;
    border: 2px solid var(--border-tab);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
    flex: 0 0 auto;
}

@keyframes spin {
    to {
        transform: rotate(360deg);
    }
}

/* 空态居中小卡片:点击创建终端会话 */
.empty-card {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 28px 44px;
    background: var(--bg-dialog);
    border: 1px dashed var(--border-tab);
    border-radius: 8px;
    color: var(--fg);
    cursor: pointer;
    font-family: inherit;
    transition: border-color 0.15s, background 0.15s;
}

.empty-card:hover {
    border-color: var(--accent);
    background: var(--bg-tab-hover);
}

.empty-card-icon {
    font-size: 26px;
    line-height: 1;
    color: var(--fg-hint);
}

.empty-card:hover .empty-card-icon {
    color: var(--accent);
}

.empty-card-title {
    font-size: 15px;
    color: var(--fg-bright);
}

.empty-card-hint {
    font-size: 12px;
    color: var(--fg-muted);
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