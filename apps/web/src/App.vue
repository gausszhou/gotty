<template>
  <div class="app">
    <!-- Activity Bar(仿 VSCode) -->
    <div class="activity-bar">
      <button class="icon-btn" title="新建会话" @click="createNew">＋</button>
      <button class="icon-btn" title="左右拆分聚焦终端" :disabled="!focusId" @click="splitFocused('row')">⬌</button>
      <button class="icon-btn" title="上下拆分聚焦终端" :disabled="!focusId" @click="splitFocused('column')">⬍</button>
      <div class="spacer"></div>
      <button class="icon-btn" title="关闭聚焦终端(会话保留)" :disabled="!focusId" @click="closeFocused">✕</button>
    </div>

    <!-- 左侧:会话管理列表 -->
    <div class="sidebar-wrap">
      <SessionSidebar :active-session-id="activeSessionId" @open="openPane" @destroy="destroyFromSidebar" />
    </div>

    <!-- 右侧:分屏工作区 -->
    <div class="workspace">
      <SplitView
        v-if="tree"
        :node="tree"
        :focused-id="focusId"
        :session-of-pane="sessionOfPane"
        @focus="focusId = $event"
        @close="closePane"
        @destroy="destroyPane"
      />
      <div v-else class="workspace-empty">
        <div class="empty-title">没有打开的终端</div>
        <div class="empty-hint">点击左侧 ＋ 新建会话,或从会话列表选择一个会话打开</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import SessionSidebar from './components/SessionSidebar.vue'
import SplitView from './components/SplitView.vue'
import { leaf, splitLeaf, removeLeaf, firstLeaf, type LayoutNode, type SplitDir } from './utils/split'
import { createSession, getSession, destroySession, type SessionInfo } from './utils/api'

// ── 布局树:叶子 = 终端 pane,内部节点 = 二分 split ──
const tree = ref<LayoutNode | null>(null)
const focusId = ref<string | null>(null)

// paneId -> sessionId 与反向索引
const sessionOfPane = reactive<Record<string, string>>({})
const paneOfSession = reactive<Record<string, string>>({})
const paneSeq = ref(0)

const activeSessionId = computed(() =>
    focusId.value ? sessionOfPane[focusId.value] : undefined,
)

// 打开一个会话:已打开则聚焦;否则新建 pane 附着。
// target 指定拆分方向与锚点(默认:左右拆分当前聚焦 pane)。
function openPane(s: SessionInfo, target?: { dir: SplitDir; paneId: string }) {
    const existing = paneOfSession[s.id]
    if (existing) {
        focusId.value = existing
        return
    }
    if (s.state === 'destroyed' || s.exited) return

    const paneId = `p${paneSeq.value++}`
    sessionOfPane[paneId] = s.id
    paneOfSession[s.id] = paneId

    if (!tree.value) {
        tree.value = leaf(paneId)
    } else {
        const dir = target?.dir ?? 'row'
        const anchor = target?.paneId ?? focusId.value ?? firstLeaf(tree.value).id
        tree.value = splitLeaf(tree.value, anchor, dir, paneId)
    }
    focusId.value = paneId
}

// 新建会话并打开
async function createNew() {
    const s = await createSession('')
    openPane(s)
}

// 拆分聚焦 pane:两个 pane 各附着一个会话,新 pane 用新会话
async function splitFocused(dir: SplitDir) {
    if (!focusId.value) return
    const s = await createSession('')
    openPane(s, { dir, paneId: focusId.value })
}

// 关闭 pane:仅分离(WS 断开),会话在服务端保留
function closePane(paneId: string) {
    const sid = sessionOfPane[paneId]
    if (sid) delete paneOfSession[sid]
    delete sessionOfPane[paneId]

    if (tree.value) {
        tree.value = removeLeaf(tree.value, paneId)
    }
    if (focusId.value === paneId) {
        focusId.value = tree.value ? firstLeaf(tree.value).id : null
    }
}

// 销毁会话并关闭其 pane
async function destroyPane(paneId: string) {
    const sid = sessionOfPane[paneId]
    if (sid) await destroySession(sid)
    closePane(paneId)
}

function closeFocused() {
    if (focusId.value) closePane(focusId.value)
}

// 侧边栏销毁:关掉绑定该会话的 pane(会话已在侧边栏内销毁)
function destroyFromSidebar(s: SessionInfo) {
    const paneId = paneOfSession[s.id]
    if (paneId) closePane(paneId)
}

// 启动:URL ?id= 兼容 —— 存活则打开,否则等待用户操作
onMounted(async () => {
    const urlId = new URLSearchParams(window.location.search).get('id')
    if (!urlId) return
    const s = await getSession(urlId)
    if (s && s.state !== 'destroyed' && !s.exited) {
        openPane(s)
    } else {
        // 清理失效的 ?id=
        const url = new URL(window.location.href)
        url.searchParams.delete('id')
        window.history.replaceState(null, '', url)
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
    height: 100vh;
    width: 100vw;
    background: #1e1e1e;
    overflow: hidden;
}

.activity-bar {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    width: 48px;
    padding: 10px 0;
    flex: 0 0 auto;
    background: #333333; /* VSCode activity bar */
    border-right: 1px solid #1e1e1e;
}

.icon-btn {
    background: none;
    border: none;
    color: #cccccc;
    font-size: 18px;
    width: 40px;
    height: 40px;
    cursor: pointer;
    border-radius: 4px;
    line-height: 1;
}

.icon-btn:hover:not(:disabled) {
    background: #3a3d41;
    color: #ffffff;
}

.icon-btn:disabled {
    opacity: 0.35;
    cursor: default;
}

.spacer {
    flex: 1 1 auto;
}

.sidebar-wrap {
    width: 240px;
    flex: 0 0 auto;
    border-right: 1px solid #1e1e1e;
    background: #252526;
}

.workspace {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    background: #1e1e1e;
    padding: 0;
}

.workspace-empty {
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
</style>