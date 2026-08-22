<template>
  <div class="tab-bar" @click="closeAllPops">
    <!-- 新建会话(固定在左侧) -->
    <div class="tab-actions tab-actions-left">
      <button class="icon-btn" title="新建会话" @click="create">＋</button>
    </div>

    <!-- 会话页签(活会话) -->
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
        ref="renameInputRef"
        v-model="renameDraft"
        class="rename-input"
        spellcheck="false"
        @click.stop
        @keyup.enter="commitRename"
        @keyup.esc="cancelRename"
        @blur="commitRename"
      />
      <span v-else class="tab-title">{{ item.title }}</span>
      <button class="tab-close" title="销毁会话（历史保留，可重新运行）" @click.stop="destroy(item.session)">✕</button>
    </div>

    <!-- 会话历史(固定在右侧) -->
    <div class="tab-actions">
      <div
        v-if="latency != null"
        class="net-status"
        :class="netClass"
        :title="'往返延迟(RTT),每 ' + PING_PERIOD_S + ' 秒刷新'"
      >
        {{ latency }}ms<template v-if="jitter != null"> · 抖动 {{ jitter }}ms</template>
      </div>
      <button class="icon-btn" title="会话历史" @click.stop="toggleHistory">▾</button>
    </div>

    <!-- 历史下拉（服务端持久化） -->
    <div v-if="historyOpen" class="history-pop" @click.stop>
      <div class="history-head">HISTORY</div>
      <div
        v-for="item in historyList"
        :key="'h' + item.history.id"
        class="history-item"
        :title="'重新运行: ' + item.history.command"
        @click="rerun(item)"
      >
        <span class="history-title">{{ item.title }}</span>
        <span class="history-cmd">{{ item.history.command }}</span>
      </div>
      <div class="history-item history-empty">暂无历史</div>
    </div>

    <!-- 历史重跑确认弹窗 -->
    <div v-if="confirmItem" class="tab-overlay" @mousedown.stop>
      <div class="vsc-dialog">
        <div class="dialog-title">重新运行</div>
        <div class="dialog-message">
          <span class="mono">{{ confirmItem.history.command }}</span>
          <span v-if="confirmItem.history.args.length" class="mono dim">
            &nbsp;{{ confirmItem.history.args.join(' ') }}
          </span>
        </div>
        <div class="dialog-actions">
          <button class="btn-primary" @click="doRerun">运行</button>
          <button class="btn-secondary" @click="confirmItem = null">取消</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, nextTick } from 'vue'
import {
    listSessions, listHistory, createSession, destroySession, renameSession,
    type SessionInfo, type HistoryInfo,
} from '../utils/api'
import { logger } from '../utils/logger'

const props = defineProps<{
    activeSessionId?: string
    // 当前激活会话的实测 RTT(毫秒)与网络抖动(相邻采样差均值),
    // 展示在标题栏(顶部栏)右侧,颜色按延迟分级。
    latency?: number | null
    jitter?: number | null
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
    (e: 'rename', detail: { id: string; title: string }): void
    (e: 'destroy', session: SessionInfo): void
}>()

const sessions = ref<SessionInfo[]>([])
const history = ref<HistoryInfo[]>([])

// 本次会话内的重命名覆盖(持久化由服务端承担,PUT title)
const renamed = ref<Record<string, string>>({})

const renamingId = ref<string | null>(null)
const renameDraft = ref('')
const renameInputRef = ref<HTMLInputElement>()

const historyOpen = ref(false)
const confirmItem = ref<{ history: HistoryInfo } | null>(null)

let timer: ReturnType<typeof setInterval>

// 页签按创建时间(created_at 升序)编号:会话N
const displayList = computed(() => {
    const asc = [...sessions.value].sort((a, b) => (a.created_at > b.created_at ? 1 : -1))
    return asc.map((session) => ({
        session,
        title: renamed.value[session.id] || session.title || `会话${asc.indexOf(session) + 1}`,
    }))
})

const historyList = computed(() => {
    const asc = [...history.value].sort((a, b) => (a.created_at > b.created_at ? 1 : -1))
    return asc.map((h) => ({
        history: h,
        title: renamed.value[h.id] || h.title || `会话${sessions.value.length + asc.indexOf(h) + 1}`,
    }))
})

async function refresh() {
    try {
        sessions.value = await listSessions()
    } catch {
        // 服务端不可用,保留旧列表
    }
    try {
        history.value = await listHistory()
    } catch {
        // 忽略
    }
}

function open(item: { session: SessionInfo; title: string }) {
    if (item.session.state === 'destroyed' || item.session.exited) return
    emit('open', { session: item.session, title: item.title })
}

async function create() {
    logger.info('tab', 'create new session (default command)')
    const s = await createSession('')
    await refresh()
    const item = displayList.value.find((x) => x.session.id === s.id)
    emit('open', { session: s, title: item?.title || s.title || s.command })
}

async function destroy(s: SessionInfo) {
    logger.info('tab', 'destroy session=%s (history kept)', s.id)
    await destroySession(s.id)
    sessions.value = sessions.value.filter((x) => x.id !== s.id)
    emit('destroy', s)
}

// ── 历史:下拉 + 重跑确认 ──
function toggleHistory() {
    historyOpen.value = !historyOpen.value
    refresh()
}

function closeAllPops() {
    historyOpen.value = false
}

function rerun(item: { history: HistoryInfo }) {
    historyOpen.value = false
    confirmItem.value = item
}

async function doRerun() {
    const h = confirmItem.value?.history
    confirmItem.value = null
    if (!h) return
    const s = await createSession(h.command, h.args)
    await refresh()
    emit('open', { session: s, title: renamed.value[s.id] || h.title || `会话${sessions.value.length + history.value.length}` })
}

// ── 双击重命名(持久化到服务端) ──
function startRename(item: { session: SessionInfo; title: string }) {
    renamingId.value = item.session.id
    renameDraft.value = renamed.value[item.session.id] || item.title
    nextTick(() => renameInputRef.value?.focus())
}

async function commitRename() {
    if (renamingId.value === null) return
    const id = renamingId.value
    const title = renameDraft.value.trim()
    renamingId.value = null

    if (title === '') {
        delete renamed.value[id]
    } else {
        renamed.value[id] = title
    }
    try {
        await renameSession(id, title)
    } catch {
        // 服务端保存失败时本次会话内仍生效
    }
    emit('rename', { id, title })
}

function cancelRename() {
    renamingId.value = null
}

function stateClass(s: SessionInfo): string {
    if (s.exited || s.state === 'destroyed') return 'dot-dead'
    if (s.state === 'running') return 'dot-running'
    return 'dot-idle'
}

onMounted(() => {
    refresh()
    timer = setInterval(refresh, 2000)
})

onBeforeUnmount(() => {
    clearInterval(timer)
})
</script>

<style scoped>
.tab-bar {
    display: flex;
    align-items: stretch;
    height: 35px;
    flex: 0 0 auto;
    background: #252526;
    border-bottom: 1px solid #1e1e1e;
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
    border-right: 1px solid #1e1e1e;
    color: #969696;
    font-size: 13px;
    cursor: pointer;
    white-space: nowrap;
    flex: 0 0 auto;
    background: #2d2d2d;
    border-top: 1px solid #333;
}

.tab.active {
    background: #1e1e1e;
    color: #ffffff;
    border-top: 1px solid #007fd4; /* VSCode 活动页签顶条 */
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
    color: #3fb950; /* 绿:<30ms */
}

.net-fair {
    color: #d29922; /* 黄:30~100ms */
}

.net-bad {
    color: #f85149; /* 红:≥100ms */
}

.state-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex: 0 0 auto;
}

.dot-idle {
    background: #6e7681;
}

.dot-running {
    background: #3fb950;
}

.dot-dead {
    background: #f85149;
}

.tab-close {
    background: none;
    border: none;
    color: #969696;
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
    background: #3a3d41;
    color: #ffffff;
}

.rename-input {
    flex: 1 1 auto;
    height: 20px;
    padding: 0 4px;
    background: #3c3c3c;
    border: 1px solid #007fd4;
    color: #cccccc;
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
    background: #252526;
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
    color: #cccccc;
    font-size: 14px;
    cursor: pointer;
    padding: 2px 8px;
    line-height: 1;
    border-radius: 3px;
}

.icon-btn:hover {
    background: #3a3d41;
    color: #ffffff;
}

/* ── 历史下拉 ── */
.history-pop {
    position: absolute;
    top: 35px;
    right: 6px;
    min-width: 240px;
    max-width: 320px;
    max-height: 60vh;
    overflow-y: auto;
    background: #252526;
    border: 1px solid #454545;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.5);
    border-radius: 4px;
    z-index: 30;
    padding: 4px 0;
}

.history-head {
    padding: 6px 12px 2px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    color: #6e7681;
}

.history-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 12px;
    cursor: pointer;
    font-size: 13px;
    color: #cccccc;
}

.history-item:hover {
    background: #094771;
    color: #ffffff;
}

.history-title {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.history-cmd {
    flex: 0 1 auto;
    max-width: 50%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: #8b949e;
    font-family: 'SF Mono', Consolas, monospace;
    font-size: 11px;
}

.history-empty {
    color: #6e7681;
    cursor: default;
}

/* ── 重跑确认弹窗 ── */
.tab-overlay {
    position: fixed;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(0, 0, 0, 0.55);
    z-index: 40;
}

.vsc-dialog {
    min-width: 280px;
    max-width: 360px;
    padding: 16px;
    background: #252526;
    border: 1px solid #454545;
    border-radius: 6px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.5);
    display: flex;
    flex-direction: column;
    gap: 10px;
}

.dialog-title {
    font-size: 15px;
    font-weight: 600;
    color: #ffffff;
}

.dialog-message {
    font-size: 13px;
    color: #cccccc;
    line-height: 1.5;
    word-break: break-word;
}

.mono {
    font-family: 'SF Mono', Consolas, monospace;
}

.mono.dim {
    color: #8b949e;
}

.dialog-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
}

.btn-primary {
    height: 26px;
    padding: 0 14px;
    background: #0e639c;
    border: none;
    border-radius: 3px;
    color: #ffffff;
    font-size: 12px;
    cursor: pointer;
}

.btn-primary:hover {
    background: #1177bb;
}

.btn-secondary {
    height: 26px;
    padding: 0 14px;
    background: #3a3d41;
    border: none;
    border-radius: 3px;
    color: #cccccc;
    font-size: 12px;
    cursor: pointer;
}

.btn-secondary:hover {
    background: #45494e;
}
</style>