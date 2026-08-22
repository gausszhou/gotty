<template>
  <div class="tab-bar">
    <!-- 新建会话(固定在左侧) -->
    <div class="tab-actions tab-actions-left">
      <button class="icon-btn" title="新建会话" @click="create">＋</button>
    </div>
    <div
      v-for="item in displayList"
      :key="item.session.id"
      class="tab"
      :class="{ active: item.session.id === activeSessionId }"
      :title="'双击重命名'"
      @click="open(item)"
      @dblclick="startRename(item)"
    >
      <span class="state-dot" :class="stateClass(item.session)"></span>
      <input
        v-if="renamingId === item.session.id"
        :ref="setRenameInputRef"
        v-model="renameDraft"
        class="rename-input"
        spellcheck="false"
        @click.stop
        @keyup.enter="commitRename"
        @keyup.esc="cancelRename"
        @blur="commitRename"
      />
      <span v-else class="tab-title">{{ item.title }}</span>
      <button class="tab-close" title="销毁会话" @click.stop="destroy(item.session)">✕</button>
    </div>

    <!-- 右侧:网络状态 + 主题切换 -->
    <div class="tab-actions">
      <div
        v-if="latency != null"
        class="net-status"
        :class="netClass"
        :title="'往返延迟(RTT),每 ' + PING_PERIOD_S + ' 秒刷新'"
      >
        {{ latency }}ms
      </div>
      <button
        class="icon-btn"
        :title="theme === 'light' ? '切换到暗色主题' : '切换到亮色主题'"
        @click="toggle"
      >{{ theme === 'light' ? '☾' : '☀' }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, nextTick } from 'vue'
import { destroySession, renameSession, type SessionInfo } from '../utils/api'
import {
    upsertManifest, removeFromManifest,
    type ManifestEntry,
} from '../utils/manifest'
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

// 与 ws.ts 的心跳周期保持一致,用于提示"延迟刷新率"
const PING_PERIOD_S = 2

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

const renamingId = ref<string | null>(null)
const renameDraft = ref('')
// 函数 ref:v-for 内的字符串 ref 会被收集成数组导致 .focus() 失效,
// 函数 ref 每次只收到单个元素(卸载时为 null)。
const renameInputRef = ref<HTMLInputElement | null>(null)
function setRenameInputRef(el: unknown) {
    renameInputRef.value = el as HTMLInputElement | null
}

const entryById = computed(() => new Map(props.entries.map((e) => [e.id, e])))

// 页签 = 清单中存活的会话,按服务端 created_at 升序编号(会话N)
const displayList = computed(() => {
    const alive = [...props.alive].sort((a, b) => (a.created_at > b.created_at ? 1 : -1))
    return alive.map((session) => {
        const entry = entryById.value.get(session.id)
        const title = entry?.title || session.title || `会话${alive.indexOf(session) + 1}`
        return { session, title }
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

// ── 双击重命名(清单 + 服务端记录双写) ──
function startRename(item: { session: SessionInfo; title: string }) {
    renamingId.value = item.session.id
    renameDraft.value = item.title
    // 聚焦并全选(类似 VSCode 重命名):直接输入即覆盖原标题
    nextTick(() => {
        renameInputRef.value?.focus()
        renameInputRef.value?.select()
    })
}

async function commitRename() {
    if (renamingId.value === null) return
    const id = renamingId.value
    const title = renameDraft.value.trim()
    renamingId.value = null

    const entry = entryById.value.get(id)
    if (entry) {
        upsertManifest({ ...entry, title: title || undefined })
    }
    try {
        await renameSession(id, title)
    } catch {
        // 服务端保存失败时清单仍保留本次重命名
    }
}

function cancelRename() {
    renamingId.value = null
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
    font-size: 11px;
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
}

.tab:hover .tab-close {
    visibility: visible;
}

.tab-close {
    visibility: hidden;
}

.tab-close:hover {
    background: var(--bg-tab-hover);
    color: var(--fg-bright);
}

.rename-input {
    flex: 1 1 auto;
    height: 20px;
    padding: 0 4px;
    background: var(--bg-input);
    border: 1px solid var(--accent);
    color: var(--fg);
    font-size: 13px;
    outline: none;
    min-width: 0;
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