<template>
  <div class="tab-bar">
    <!-- 新建会话(固定在左侧) -->
    <div class="tab-actions tab-actions-left">
      <button class="icon-btn" :title="t('tab.new')" @click="create">＋</button>
    </div>
    <div
      v-for="item in displayList"
      :key="item.session.id"
      class="tab"
      :class="{ active: item.session.id === activeSessionId }"
      @click="open(item)"
    >
      <span class="state-dot" :class="stateClass(item.session)"></span>
      <span class="tab-title">{{ item.title }}</span>
      <button class="tab-close" :title="t('tab.destroy')" @click.stop="destroy(item.session)">✕</button>
    </div>

    <!-- 右侧:网络状态 + 语言/主题切换 -->
    <div class="tab-actions">
      <div
        v-if="latency != null"
        class="net-status"
        :class="netClass"
        :title="t('tab.latency')"
      >
        {{ latency }}ms
      </div>
      <button
        class="icon-btn"
        :title="t('lang.toggle')"
        @click="toggleLang"
      >中/EN</button>
      <button
        class="icon-btn"
        :title="theme === 'light' ? t('theme.toLight') : t('theme.toDark')"
        @click="toggle"
      >{{ theme === 'light' ? '☾' : '☀' }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { destroySession, type SessionInfo } from '../utils/api'
import { removeFromManifest, type ManifestEntry } from '../utils/manifest'
import { toggleLang, t } from '../utils/i18n'
import { logger } from '../utils/logger'

const props = defineProps<{
    // 设备会话清单(localStorage)
    entries: ManifestEntry[]
    // 清单中服务端存活的会话(status 轮询结果)
    alive: SessionInfo[]
    // 本设备各打开视图的 WS 连接状态(id → 已附着),圆点即时变绿
    connected?: Record<string, boolean>
    // 当前主题(驱动 ☾/☀ 图标)
    theme?: string
    activeSessionId?: string
    // 当前激活会话的实测 RTT(毫秒),展示在标题栏(顶部栏)右侧,颜色按延迟分级。
    latency?: number | null
}>()

// 延迟颜色分级:绿(<30ms) / 黄(30~100ms) / 红(≥100ms)
const netClass = computed(() => {
    const l = props.latency
    if (l == null) return ''
    if (l < 30) return 'net-good'
    if (l < 100) return 'net-fair'
    return 'net-bad'
})

const emit = defineEmits<{
    (e: 'open', detail: { session: SessionInfo; title: string }): void
    (e: 'destroy', session: SessionInfo): void
    // 请求新建会话(创建逻辑在上层 App 统一,顶部 ＋ 与空态卡片共用)
    (e: 'create'): void
    // 请求切换亮/暗主题(applyTheme + 广播在上层 App)
    (e: 'theme'): void
    // 清单/存活状态已改变,请求上层重新拉取
    (e: 'changed'): void
}>()

// 主题切换按钮 → App 处理(toggleTheme + notify)
function toggle() {
    emit('theme')
}

const entryById = computed(() => new Map(props.entries.map((e) => [e.id, e])))

// commandBasename 取命令名作为默认标题(如 /bin/bash → "bash")。
function commandBasename(command: string): string {
    const base = command.split('/').pop() || ''
    return base || command
}

// 页签标题 = 程序最近设置的标题(OSC 0/2,Gnome-Shell 风格自动命名);
// 无程序标题时回退命令名。旧版遗留的 "会话N" 编号视为无标题。
function displayTitle(entry: ManifestEntry | undefined, command: string): string {
    const t = entry?.title
    if (t && !/^会话\d+$/.test(t)) return t
    return commandBasename(command)
}

// 页签 = 清单中存活的会话,按服务端 created_at 升序
const displayList = computed(() => {
    const alive = [...props.alive].sort((a, b) => (a.created_at > b.created_at ? 1 : -1))
    return alive.map((session) => {
        const entry = entryById.value.get(session.id)
        return { session, title: displayTitle(entry, session.command) }
    })
})

function open(item: { session: SessionInfo; title: string }) {
    if (item.session.state === 'destroyed' || item.session.exited) return
    emit('open', { session: item.session, title: item.title })
}

// 新建:交由上层 App(生成 id + 创建 + 写清单 + 打开)
function create() {
    logger.info('tab', 'create request → app')
    emit('create')
}

// 销毁:服务端销毁(记录保留,可凭 id 复活)+ 本设备清单移除
async function destroy(s: SessionInfo) {
    logger.info('tab', 'destroy session=%s (record kept, manifest entry removed)', s.id)
    await destroySession(s.id)
    removeFromManifest(s.id)
    emit('destroy', s)
    emit('changed')
}

function stateClass(s: SessionInfo): string {
    // 本设备已成功附着 → 即时绿(不等 status 轮询)
    if (props.connected?.[s.id]) return 'dot-running'
    if (s.exited || s.state === 'destroyed') return 'dot-dead'
    if (s.state === 'running') return 'dot-running'
    return 'dot-idle'
}
</script>

<style scoped>
.tab-bar {
    display: flex;
    align-items: stretch;
    height: 30px;
    flex: 0 0 auto;
    background: var(--bg-bar);
    border-bottom: 1px solid var(--bg-bar-border);
    overflow-x: auto;
    overflow-y: hidden;
    position: relative;
    user-select: none;
}

.tab {
    display: flex;
    align-items: center;
    gap: 6px;
    max-width: 200px;
    min-width: 100px;
    padding: 0 8px;
    border-right: 1px solid var(--bg-bar-border);
    color: var(--fg-dim);
    font-size: 13px;
    cursor: pointer;
    white-space: nowrap;
    flex: 0 0 auto;
    background: var(--bg-tab);
    border-top: 1px solid var(--border-tab);
}

.tab.active {
    background: var(--bg-tab-active);
    color: var(--fg-bright);
    border-top: 1px solid var(--accent); /* VSCode 活动页签顶条 */
}

.tab-title {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
}

/* ── 网络状态(延迟 + 抖动,颜色分级) ── */
.net-status {
    display: flex;
    align-items: center;
    gap: 2px;
    font-family: 'SF Mono', Consolas, monospace;
    font-size: 12px;
    padding: 2px 8px;
    border-radius: 3px;
    white-space: nowrap;
    flex: 0 0 auto;
}

.net-good {
    color: var(--net-good); /* 绿:<30ms */
}

.net-fair {
    color: var(--net-fair); /* 黄:30~100ms */
}

.net-bad {
    color: var(--net-bad); /* 红:≥100ms */
}

.state-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex: 0 0 auto;
}

.dot-idle {
    background: var(--dot-idle);
}

.dot-running {
    background: var(--dot-running);
}

.dot-dead {
    background: var(--dot-dead);
}

.tab-close {
    background: none;
    border: none;
    color: var(--fg-dim);
    font-size: 11px;
    line-height: 1;
    padding: 2px 4px;
    border-radius: 3px;
    cursor: pointer;
    flex: 0 0 auto;
    /* 常驻显示:不依赖 hover(原先是 visibility: hidden + .tab:hover 才可见) */
}

.tab-close:hover {
    background: var(--bg-tab-hover);
    color: var(--fg-bright);
}

.tab-actions {
    display: flex;
    align-items: center;
    gap: 2px;
    margin-left: auto;
    padding: 0 6px;
    flex: 0 0 auto;
    position: sticky;
    right: 0;
    background: var(--bg-bar);
}

/* 新建会话固定在左侧,滚动时保持可见 */
.tab-actions-left {
    margin-left: 0;
    position: sticky;
    left: 0;
    z-index: 2;
}

.icon-btn {
    background: none;
    border: none;
    color: var(--fg);
    font-size: 14px;
    cursor: pointer;
    padding: 2px 8px;
    line-height: 1;
    border-radius: 3px;
}

.icon-btn:hover {
    background: var(--bg-tab-hover);
    color: var(--fg-bright);
}
</style>