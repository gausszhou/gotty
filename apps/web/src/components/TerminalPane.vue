<template>
  <div class="terminal-pane" :class="{ focused }" @mousedown="focus">
    <div class="pane-header">
      <span class="pane-title" :title="title">{{ title }}</span>
      <span class="pane-session">{{ shortSessionId }}</span>
      <span v-if="connecting" class="pane-status">connecting…</span>
      <button class="pane-btn" title="关闭此终端(会话保留)" @mousedown.stop @click.stop="close">✕</button>
      <button class="pane-btn" title="销毁会话并关闭" @mousedown.stop @click.stop="destroy">🗑</button>
    </div>
    <Terminal ref="terminalRef" class="pane-terminal" @title="onServerTitle" />
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import Terminal from './Terminal.vue'
import { WebTTY, protocols, type Terminal as ITerminal } from '../utils/webtty'
import { ConnectionFactory } from '../utils/websocket'
import { getSession } from '../utils/api'

const props = defineProps<{
    paneId: string
    sessionId: string
    command?: string
    focused?: boolean
}>()

const emit = defineEmits<{
    (e: 'focus', paneId: string): void
    (e: 'close', paneId: string): void
    (e: 'destroy', paneId: string): void
}>()

const terminalRef = ref<InstanceType<typeof Terminal>>()
const title = ref(props.command || props.sessionId)
const connecting = ref(true)

let closer: (() => void) | null = null

const shortSessionId = () => props.sessionId.slice(0, 8)

function onServerTitle(t: string) {
    if (t) title.value = t
}

function focus() {
    emit('focus', props.paneId)
}

function close() {
    closer?.()
    emit('close', props.paneId)
}

async function destroy() {
    emit('destroy', props.paneId)
}

// resolveSession 只负责确认绑定会话仍然存活;会话销毁后返回 null,
// WebTTY 会停止重连(会话生命周期由左侧列表管理,不在 pane 内自建)。
const resolveSession = async (): Promise<string | null> => {
    const session = await getSession(props.sessionId)
    if (session && session.state !== 'destroyed' && !session.exited) {
        return session.id
    }
    return null
}

onMounted(async () => {
    const el = terminalRef.value!
    if (!el) return

    const termAdapter: ITerminal = {
        info: () => el.info(),
        output: (data: Uint8Array) => el.write(data),
        showMessage: (msg: string, timeout: number) => el.showMessage(msg, timeout),
        removeMessage: () => el.removeMessage(),
        setWindowTitle: (t: string) => el.setWindowTitle(t),
        setPreferences: () => {},
        onInput: (cb) => el.onInput(cb),
        onResize: (cb) => el.onResize(cb),
        reset: () => el.reset(),
        deactivate: () => el.deactivate(),
        close: () => el.close(),
    }

    const sid = await resolveSession()
    if (sid === null) {
        connecting.value = false
        el.showMessage('Session is gone', 0)
        return
    }

    const httpsEnabled = window.location.protocol === 'https:'
    const wsBase =
        (httpsEnabled ? 'wss://' : 'ws://') +
        window.location.host +
        '/ws'
    const token = (window as any).gotty_auth_token || ''
    const wt = new WebTTY(
        termAdapter,
        new ConnectionFactory(wsBase, protocols),
        '', // Arguments (unused by the new session-based API)
        token,
        sid,
        resolveSession,
    )
    closer = wt.open()
    connecting.value = false
})

onBeforeUnmount(() => {
    closer?.()
})
</script>

<style scoped>
.terminal-pane {
    display: flex;
    flex-direction: column;
    width: 100%;
    height: 100%;
    min-width: 0;
    min-height: 0;
    background: #0d1117;
    border: 1px solid #30363d;
    box-sizing: border-box;
}

.terminal-pane.focused {
    border-color: #2f81f7;
}

.pane-header {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 26px;
    padding: 0 6px;
    flex: 0 0 auto;
    background: #161b22;
    border-bottom: 1px solid #30363d;
    color: #c9d1d9;
    font-size: 12px;
    user-select: none;
}

.pane-title {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.pane-session {
    color: #8b949e;
    font-family: monospace;
    flex: 0 0 auto;
}

.pane-status {
    color: #d29922;
    flex: 0 0 auto;
}

.pane-btn {
    background: none;
    border: none;
    color: #8b949e;
    cursor: pointer;
    font-size: 12px;
    padding: 0 4px;
    line-height: 18px;
}

.pane-btn:hover {
    color: #f0f6fc;
    background: #30363d;
    border-radius: 3px;
}

.pane-terminal {
    flex: 1 1 auto;
    min-height: 0;
}
</style>