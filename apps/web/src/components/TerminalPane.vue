<template>
  <div class="terminal-pane">
    <!-- 纯净的 xterm:无头部、无边框、无任何修饰 -->
    <Terminal ref="terminalRef" class="pane-terminal" @title="onProgramTitle" />

    <!-- 断开 / 会话消失:VSCode 风格弹窗(异常态提示,正常态不出现) -->
    <div
      v-if="connState === 'disconnected' || connState === 'gone'"
      class="pane-overlay"
      @mousedown.stop
      @contextmenu.prevent.stop
    >
      <div class="vsc-dialog">
        <div class="dialog-title">{{ connState === 'gone' ? t('dialog.gone') : t('dialog.lost') }}</div>
        <div class="dialog-message">{{ overlayMessage }}</div>
        <div class="dialog-actions">
          <button
            v-if="connState === 'disconnected'"
            class="btn-primary"
            @click="reconnect"
          > {{ t('dialog.reconnect') }}</button>
          <button class="btn-secondary" @click="close">{{ t('dialog.close') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount } from 'vue'
import Terminal from './Terminal.vue'
import { openTerminalWS, type TermHandle, type WSWrapper } from '../utils/ws'
import { getSession, createSession } from '../utils/api'
import { t } from '../utils/i18n'
import { findManifestEntry, upsertManifest } from '../utils/manifest'
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
    // WS 连接状态(connected = 本设备已成功附着),圆点即时变绿
    (e: 'conn', connected: boolean): void
    // 程序设置的终端标题(OSC 0/2),由上层更新页签标题
    (e: 'tab-title', title: string): void
}>()

// onProgramTitle:程序标题 → 上层(App 写入清单,页签随之更新)
function onProgramTitle(title: string) {
    emit('tab-title', title)
}

type ConnState = 'connecting' | 'connected' | 'disconnected' | 'gone'

const terminalRef = ref<InstanceType<typeof Terminal>>()
const connState = ref<ConnState>('connecting')
const overlayMessage = ref('')

let wsWrapper: WSWrapper | null = null

// resolveSession 确认绑定会话仍然存活。会话已销毁/消失时(空闲淘汰、
// 其他设备销毁、进程退出),用清单记录**直接重建**(服务端复活同 id),
// 而不是报"会话已销毁";重建也失败才返回 null。
const resolveSession = async (): Promise<string | null> => {
    const session = await getSession(props.sessionId)
    if (session && session.state !== 'destroyed' && !session.exited) {
        return session.id
    }
    return rebuildSession()
}

// rebuildSession:凭清单中的 command/args 重新创建同 id 会话。
// 服务端有记录则复活(记录命令,run_count+1);无清单条目时用默认命令。
// 重建成功后把会话加回本设备清单(可能已被轮询清理),保持清单一致。
async function rebuildSession(): Promise<string | null> {
    const entry = findManifestEntry(props.sessionId)
    logger.info('attach', 'session %s gone, rebuilding (command=%s)', props.sessionId, entry?.command ?? '(default)')
    try {
        const s = await createSession(entry?.command ?? '', entry?.args ?? [], props.sessionId)
        upsertManifest({
            id: s.id,
            command: s.command,
            args: s.args,
            createdAt: entry?.createdAt ?? Date.now(),
            lastSeen: Date.now(),
        })
        logger.info('attach', 'session rebuilt: %s', s.id)
        return s.id
    } catch (err) {
        logger.warn('attach', 'failed to rebuild session=%s: %s', props.sessionId, err)
        return null
    }
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
    onWriteParsed: (cb) => terminalRef.value!.onWriteParsed(cb),
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
            overlayMessage.value = t('dialog.goneMsg')
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
                overlayMessage.value = t('dialog.goneMsg')
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

onMounted(() => {
    attach()
    // 新建会话的 pane 挂载即激活:props.active 初始为 true,下方 watcher
    // 只在值变化时回调,不会触发;这里补一次 fit + 聚焦(子 Terminal
    // 已 open,光标直接落到新终端,无需点击)。
    if (props.active) {
        requestAnimationFrame(() => {
            terminalRef.value?.fit()
            terminalRef.value?.focus()
        })
    }
})

// 连接状态变化 → 上报上层(页签圆点即时变色)
watch(connState, (v) => emit('conn', v === 'connected'))

// v-show 从隐藏切回可见时,容器尺寸恢复,重新 fit 终端;
// 同时把键盘焦点交给 xterm —— 新建/切换会话后无需点击即可直接输入。
watch(
    () => props.active,
    (visible) => {
        if (visible) {
            requestAnimationFrame(() => {
                terminalRef.value?.fit()
                terminalRef.value?.focus()
            })
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
    background: var(--bg-terminal); /* 与内容区同色;此前硬编码 #000000 */
    overflow: hidden;
    /* 半个字符的内边距:字符网格不再贴着页签栏与窗口边缘。
       用的是 --term-cell-w —— Terminal.vue 用 xterm 的真实几何写回的格子宽,
       所以这里正好是半个字符格。**不要**写成 0.5ch:ch 量的是字体 "0" 的步进宽度,
       与 xterm 取整后的格子宽差约 10%(实测 7.70px vs 7.00px),会变成 0.55 个格子。
       下面那行 font-family/font-size 只是 --term-cell-w 还没写回时的首帧兜底。
       内边距区域由上面的 background 补齐,与终端面同色,所以看上去是"留白"而非"边框"。
       fit() 的安全性:FitAddon 量的是 .xterm 父元素(Terminal.vue 的
       .terminal-container)的 computed 尺寸,而百分比宽高按父元素 content box 解析、
       box-sizing 又是 border-box,因此这里的 padding 会被自动扣掉。 */
    font-family: var(--font-mono);
    font-size: var(--term-font-size);
    padding: calc(var(--term-cell-w) / 2);
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
    background: var(--overlay);
    z-index: 10;
    /* 上面的等宽字体只是为了让 0.5ch 量得准,不该传染给弹窗文字 */
    font-family: var(--font-ui);
}

.vsc-dialog {
    min-width: 320px;
    max-width: 420px;
    padding: 16px;
    background: var(--bg-dialog); /* VSCode 对话框背景 editorWidget.background */
    border: 1px solid var(--border-dialog); /* widget.border */
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-overlay); /* widget.shadow */
    display: flex;
    flex-direction: column;
    gap: var(--gap-lg);
}

.dialog-title {
    font-size: var(--font-size-lg);
    font-weight: 600;
    color: var(--fg-bright);
}

.dialog-message {
    font-size: var(--font-size-md);
    color: var(--fg);
    line-height: 1.5;
    word-break: break-word;
}

.dialog-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--gap-md);
    margin-top: 4px;
}

.btn-primary {
    height: 26px;
    padding: 0 14px;
    background: var(--accent); /* button.background */
    border: none;
    border-radius: var(--radius-sm);
    color: var(--fg-on-accent); /* button.foreground */
    font-size: var(--font-size-sm);
    cursor: pointer;
    transition: background-color var(--transition-fast) linear;
}

.btn-primary:hover {
    background: var(--accent-hover); /* button.hoverBackground */
}

.btn-secondary {
    height: 26px;
    padding: 0 14px;
    background: var(--bg-btn-secondary); /* button.secondaryBackground */
    border: 1px solid var(--border-btn); /* button.border */
    border-radius: var(--radius-sm);
    color: var(--fg); /* button.secondaryForeground */
    font-size: var(--font-size-sm);
    cursor: pointer;
    transition: background-color var(--transition-fast) linear;
}

.btn-secondary:hover {
    background: var(--bg-btn-secondary-hover); /* button.secondaryHoverBackground */
}
</style>