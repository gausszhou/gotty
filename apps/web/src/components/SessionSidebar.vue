<template>
  <div class="sidebar">
    <div class="sidebar-header">
      <span class="sidebar-title">SESSIONS</span>
      <span class="sidebar-actions">
        <button class="icon-btn" title="刷新列表" @click="refresh">⟳</button>
        <button class="icon-btn" title="新建会话" @click="toggleCreate">＋</button>
      </span>
    </div>
    <div v-if="creating" class="create-row">
      <input
        ref="commandInputRef"
        v-model="commandInput"
        class="create-input"
        placeholder="命令，留空使用服务端默认"
        spellcheck="false"
        @keyup.enter="create"
        @keyup.esc="cancelCreate"
      />
      <button class="create-btn" title="创建并打开" @click="create">创建</button>
      <div v-if="errorMessage" class="create-error">{{ errorMessage }}</div>
    </div>
    <div class="sidebar-body">
      <div
        v-for="s in sessions"
        :key="s.id"
        class="session-item"
        :class="{ active: s.id === activeSessionId }"
        :title="s.id"
        @click="open(s)"
      >
        <span class="state-dot" :class="stateClass(s)"></span>
        <span class="session-command">{{ s.command }}</span>
        <span class="session-id">{{ s.id.slice(0, 6) }}</span>
        <span class="item-actions">
          <button
            class="icon-btn small"
            title="销毁会话"
            @click.stop="destroy(s)"
          >🗑</button>
        </span>
      </div>
      <div v-if="!sessions.length" class="sidebar-empty">
        暂无会话
        <br />点击上方 ＋ 新建
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { listSessions, createSession, destroySession, type SessionInfo } from '../utils/api'

const props = defineProps<{
    activeSessionId?: string
}>()

const emit = defineEmits<{
    (e: 'open', session: SessionInfo): void
    (e: 'destroy', session: SessionInfo): void
}>()

const sessions = ref<SessionInfo[]>([])
const creating = ref(false)
const commandInput = ref('')
const errorMessage = ref('')
const commandInputRef = ref<HTMLInputElement>()
let timer: ReturnType<typeof setInterval>

async function refresh() {
    try {
        const list = await listSessions()
        // 新会话在前
        list.sort((a, b) => (a.created_at < b.created_at ? 1 : -1))
        sessions.value = list
    } catch {
        // 服务端不可用,保留旧列表
    }
}

function open(s: SessionInfo) {
    if (s.state === 'destroyed' || s.exited) return
    emit('open', s)
}

function toggleCreate() {
    creating.value = !creating.value
    errorMessage.value = ''
    if (creating.value) {
        commandInput.value = ''
        requestAnimationFrame(() => commandInputRef.value?.focus())
    }
}

function cancelCreate() {
    creating.value = false
    errorMessage.value = ''
}

// 创建会话:输入为空时使用服务端默认命令;
// 服务端无默认命令且未输入 → 400,错误直接显示在输入行下方。
async function create() {
    errorMessage.value = ''
    const command = commandInput.value.trim()
    try {
        const s = await createSession(command)
        sessions.value = [s, ...sessions.value]
        creating.value = false
        commandInput.value = ''
        emit('open', s)
    } catch (err) {
        errorMessage.value = err instanceof Error ? err.message : String(err)
    }
}

async function destroy(s: SessionInfo) {
    await destroySession(s.id)
    sessions.value = sessions.value.filter((x) => x.id !== s.id)
    emit('destroy', s)
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
.sidebar {
    display: flex;
    flex-direction: column;
    width: 100%;
    height: 100%;
    background: #252526;
    color: #cccccc;
    font-size: 13px;
    user-select: none;
}

.sidebar-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 35px;
    padding: 0 10px;
    flex: 0 0 auto;
    border-bottom: 1px solid #1e1e1e;
    box-sizing: border-box;
}

.create-row {
    padding: 6px 10px;
    flex: 0 0 auto;
    border-bottom: 1px solid #1e1e1e;
    display: flex;
    flex-direction: column;
    gap: 4px;
}

.create-input {
    width: 100%;
    height: 24px;
    padding: 0 8px;
    background: #3c3c3c;
    border: 1px solid #007fd4; /* VSCode 焦点输入框 */
    color: #cccccc;
    font-size: 12px;
    font-family: 'SF Mono', Consolas, monospace;
    outline: none;
    box-sizing: border-box;
}

.create-input::placeholder {
    color: #858585;
}

.create-btn {
    align-self: flex-end;
    height: 22px;
    padding: 0 12px;
    background: #0e639c; /* VSCode 主按钮 */
    border: none;
    border-radius: 3px;
    color: #ffffff;
    font-size: 12px;
    cursor: pointer;
}

.create-btn:hover {
    background: #1177bb;
}

.create-error {
    color: #f48771; /* VSCode 错误红 */
    font-size: 11px;
    line-height: 1.4;
}

.sidebar-title {
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    color: #bbbbbb; /* VSCode 侧边栏标题 */
}

.sidebar-actions {
    display: flex;
    gap: 2px;
}

.icon-btn {
    background: none;
    border: none;
    color: #cccccc;
    font-size: 14px;
    cursor: pointer;
    padding: 2px 6px;
    line-height: 1;
    border-radius: 3px;
}

.icon-btn:hover {
    background: #3a3d41;
}

.icon-btn.small {
    font-size: 12px;
    padding: 0 3px;
    visibility: hidden;
}

.sidebar-body {
    flex: 1 1 auto;
    overflow-y: auto;
    padding: 4px 0;
}

.session-item {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 24px;
    padding: 0 10px;
    cursor: pointer;
    border-left: 2px solid transparent;
    box-sizing: border-box;
}

.session-item:hover {
    background: #2a2d2e;
}

.session-item:hover .icon-btn.small {
    visibility: visible;
}

.session-item.active {
    background: #37373d;
    border-left-color: #007fd4; /* VSCode 活动项高亮 */
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

.session-command {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.session-id {
    color: #6e7681;
    font-family: 'SF Mono', Consolas, monospace;
    font-size: 11px;
    flex: 0 0 auto;
}

.item-actions {
    flex: 0 0 auto;
}

.sidebar-empty {
    padding: 16px 10px;
    color: #6e7681;
    font-size: 12px;
    text-align: center;
    line-height: 1.8;
}
</style>