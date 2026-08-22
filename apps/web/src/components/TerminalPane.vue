<template>
  <div class="terminal-pane">
    <!-- 纯净的 xterm:无头部、无边框、无任何修饰 -->
    <Terminal ref="terminalRef" class="pane-terminal" />

    <!-- 断开 / 会话消失:VSCode 风格弹窗(异常态提示,正常态不出现) -->
    <div
      v-if="connState === 'disconnected' || connState === 'gone'"
      class="pane-overlay"
      @mousedown.stop
      @contextmenu.prevent.stop
    >
      <div class="vsc-dialog">
        <div class="dialog-title">{{ connState === 'gone' ? '会话已销毁' : '连接已断开' }}</div>
        <div class="dialog-message">{{ overlayMessage }}</div>
        <div class="dialog-actions">
          <button
            v-if="connState === 'disconnected'"
            class="btn-primary"
            @click="reconnect"
          >重新连接</button>
          <button class="btn-secondary" @click="close">关闭</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount } from 'vue'
import Terminal from './Terminal.vue'
import { openTerminalWS, type TermHandle, type WSWrapper } from '../utils/ws'
import { getSession } from '../utils/api'
import { logger } from '../utils/logger'

const props = defineProps<{
    sessionId: string
    // v-show 常驻视图:是否当前可见(可见时重新 fit 终端)
    active?: boolean
}>()

const emit = defineEmits<{
    (e: 'close'): void
    // 实测 RTT(毫秒),由上层展示在页签标题旁
    (e: 'latency', ms: number | null): void
}>()

type ConnState = 'connecting' | 'connected' | 'disconnected' | 'gone'

const terminalRef = ref<InstanceType<typeof Terminal>>()
const connState = ref<ConnState>('connecting')
const overlayMessage = ref('')

let wsWrapper: WSWrapper | null = null

// resolveSession 确认绑定会话仍然存活;销毁后返回 null。
const resolveSession = async (): Promise<string | null> => {
    const session = await getSession(props.sessionId)
    if (session && session.state !== 'destroyed' && !session.exited) {
        return session.id
    }
    return null
}

// xterm 组件暴露的能力,直接映射给收发层
const termHandle: TermHandle = {
    info: () => terminalRef.value!.info(),
    write: (data) => terminalRef.value!.write(data),
    setWindowTitle: () => {},
    reset: () => terminalRef.value!.reset(),
    deactivate: () => terminalRef.value!.deactivate(),
    onInput: (cb) => terminalRef.value!.onInput(cb),
    onResize: (cb) => terminalRef.value!.onResize(cb),
}

function attach() {
    connState.value = 'connecting'
    overlayMessage.value = ''
    logger.info('attach', 'attach session=%s', props.sessionId)

    void (async () => {
        const sid = await resolveSession()
        if (sid === null) {
            logger.warn('attach', 'session gone (session=%s)', props.sessionId)
            connState.value = 'gone'
            overlayMessage.value = '该会话已被销毁或不存在'
            emit('latency', null)
            return
        }
        logger.info('attach', 'session resolved ok (session=%s)', sid)

        wsWrapper = openTerminalWS(termHandle, sid, {
            onConnect: () => {
                logger.info('attach', 'connected (session=%s)', props.sessionId)
                connState.value = 'connected'
            },
            onDisconnect: (message) => {
                logger.warn('attach', 'disconnected (session=%s): %s', props.sessionId, message)
                connState.value = 'disconnected'
                overlayMessage.value = message
                emit('latency', null)
            },
            onGone: () => {
                logger.warn('attach', 'gone (session=%s)', props.sessionId)
                connState.value = 'gone'
                overlayMessage.value = '该会话已被销毁或不存在'
                emit('latency', null)
            },
            onLatency: (ms) => emit('latency', ms),
            resolveSession,
        })
    })()
}

function reconnect() {
    connState.value = 'connecting'
    overlayMessage.value = ''
    if (wsWrapper) {
        wsWrapper.reconnect()
    } else {
        attach()
    }
}

function close() {
    wsWrapper?.close()
    emit('close')
}

onMounted(attach)

// v-show 从隐藏切回可见时,容器尺寸恢复,重新 fit 终端
watch(
    () => props.active,
    (visible) => {
        if (visible) {
            requestAnimationFrame(() => terminalRef.value?.fit())
        }
    },
)

onBeforeUnmount(() => {
    logger.info('attach', 'detach/unmount (session=%s)', props.sessionId)
    wsWrapper?.close()
    emit('latency', null)
})
</script>

<style scoped>
.terminal-pane {
    position: relative;
    flex: 1 1 0%;
    width: 100%;
    height: 100%;
    min-width: 0;
    min-height: 0;
    background: #000000;
    overflow: hidden;
}

.pane-terminal {
    width: 100%;
    height: 100%;
}

/* ── 断开弹窗(仿 VSCode 模态框,仅异常态) ── */
.pane-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(0, 0, 0, 0.55);
    z-index: 10;
}

.vsc-dialog {
    min-width: 320px;
    max-width: 420px;
    padding: 16px;
    background: #252526; /* VSCode 对话框背景 */
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

.dialog-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
}

.btn-primary {
    height: 26px;
    padding: 0 14px;
    background: #0e639c; /* VSCode 主按钮 */
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
    background: #3a3d41; /* VSCode 次按钮 */
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