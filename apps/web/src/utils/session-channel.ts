import { logger } from './logger'
import {
    MSG_INPUT,
    MSG_ATTACH,
    MSG_RESIZE,
    MSG_OUTPUT,
    MSG_PONG,
    MSG_WINDOW_TITLE,
    MSG_PREFERENCES,
    MSG_RECONNECT,
    MSG_REPLAY_DONE,
    MSG_ATTACH_OK,
    MSG_ATTACH_ERR,
    MSG_EVENT,
    EVENT_PREEMPTED,
    EVENT_DESTROYED,
    saneSize,
} from './ws'
import type { TermHandle, WSHooks, WSWrapper } from './ws'
import type { Multiplexer } from './multiplexer'

const decoder = new TextDecoder()

// 输入上行开关的兜底封顶:attach 握手完成后、重放解析完成前,最长等待这么久
// 就强制开启输入(避免极端情况下永久无法输入)。
const REPLAY_GATE_MAX_MS = 2000
// 收到 MSG_REPLAY_DONE 后,xterm 对重放字节流的解析是异步的;解析完成
// (onWriteParsed)才开启上行,此 600ms 为二次兜底。
const REPLAY_PARSE_MAX_MS = 600

// SessionChannel 是一条逻辑会话通道的浏览器侧桥接:它绑定一个 TermHandle
// (xterm 能力),把服务端帧翻译成终端写/标题/握手事件,把终端输入/尺寸翻译成
// 路由帧经 Multiplexer 写回单条 WS。复用自移除前的 openTerminalWS 的桥接逻辑。
class SessionChannel {
    readonly sid: string
    wrapper!: WSWrapper

    private term: TermHandle
    private hooks: WSHooks
    private mux: Multiplexer

    // 输入上行开关:attach 握手 + 重放解析完成前关闭。xterm 会对重放流中
    // 的终端查询(DSR/DECRQM/OSC)自动生成应答并经 onInput 上行;握手完成前
    // 写回 PTY 等于向并不等待的程序注入陈旧应答,前台 shell 会把转义载荷
    // 显示成乱码。
    private inputEnabled = false
    private closed = false
    private reconnectSeconds = 0

    private gateTimer: ReturnType<typeof setTimeout> | null = null
    private reconnectTimer: ReturnType<typeof setTimeout> | null = null
    // onWriteParsed 回调的退订;重放解析完成后清理,防跨连接残留。
    private parsedUnsub: (() => void) | null = null

    constructor(sid: string, term: TermHandle, hooks: WSHooks, mux: Multiplexer) {
        this.sid = sid
        this.term = term
        this.hooks = hooks
        this.mux = mux
        // 终端回调只绑定一次(通道生命周期内复用同一 term),避免重连后
        // 一次按键被多次上行(旧代码 inputBound 的同源问题)。
        this.bindTerminal()
    }

    // bindTerminal:把 xterm 的输入/尺寸事件桥接到本通道的路由发送。
    private bindTerminal(): void {
        this.term.onInput((input) => {
            if (this.inputEnabled && !this.closed) {
                this.mux.send(this.sid, MSG_INPUT, input)
            }
        })
        this.term.onResize((columns, rows) => {
            if (saneSize(columns, rows) && !this.closed) {
                this.mux.send(this.sid, MSG_RESIZE, JSON.stringify({ columns, rows }))
            }
        })
    }

    // startGate:重置输入门控(每次新附着/重附着都调用)。
    startGate(): void {
        this.inputEnabled = false
        if (this.gateTimer) {
            clearTimeout(this.gateTimer)
            this.gateTimer = null
        }
        this.gateTimer = setTimeout(() => {
            this.inputEnabled = true
        }, REPLAY_GATE_MAX_MS)
    }

    // reattach:(重)附着本通道:重置门控并发送 Attach 帧。
    reattach(): void {
        this.startGate()
        this.mux.send(this.sid, MSG_ATTACH)
    }

    // reportLatency:连接级 RTT 测量的回调入口(Multiplexer 广播给所有通道)。
    reportLatency(ms: number | null): void {
        this.hooks.onLatency?.(ms)
    }

    // onAttached:服务端确认 Attach(AttachOK)。触发组件 onConnect,并补发
    // 一次初始尺寸(PTY 据此从服务端默认尺寸切换到客户端真实尺寸)。
    onAttached(): void {
        this.hooks.onConnect?.()
        const { columns, rows } = this.term.info()
        if (saneSize(columns, rows)) {
            this.mux.send(this.sid, MSG_RESIZE, JSON.stringify({ columns, rows }))
        }
    }

    // handleFrame:处理一条会话级帧(路由头已解开,传入内层的 type/payload)。
    handleFrame(type: number, payload: Uint8Array): void {
        switch (type) {
            case MSG_OUTPUT:
                // 复制一份:payload 是路由帧的子视图,避免底层缓冲被复用导致串数据。
                this.term.write(payload.slice())
                break
            case MSG_PONG:
                // 连接级 Pong 由 Multiplexer 处理,会话级不会到达;忽略。
                break
            case MSG_WINDOW_TITLE:
                this.term.setWindowTitle(decoder.decode(payload))
                break
            case MSG_PREFERENCES:
                // xterm 构造参数已配置,无需动态应用。
                break
            case MSG_RECONNECT:
                this.reconnectSeconds = Number(decoder.decode(payload)) || 0
                break
            case MSG_REPLAY_DONE:
                this.onReplayDone()
                break
            case MSG_ATTACH_OK:
                this.onAttached()
                break
            case MSG_ATTACH_ERR:
                this.onAttachErr(payload)
                break
            case MSG_EVENT:
                this.onEvent(payload)
                break
            default:
                logger.debug('mux', 'unhandled session frame 0x%s (sid=%s)', type.toString(16), this.sid)
        }
    }

    // onReplayDone:重放字节已全部交给 xterm,但解析是异步的;等 onWriteParsed
    // (重放解析完成)再开启上行,600ms 兜底防卡。同时触发 onReady(CaptureView 截图驱动)。
    private onReplayDone(): void {
        this.hooks.onReady?.()
        if (this.gateTimer) {
            clearTimeout(this.gateTimer)
            this.gateTimer = null
        }
        this.parsedUnsub?.()
        let opened = false
        const open = () => {
            if (opened) return
            opened = true
            this.inputEnabled = true
            this.parsedUnsub?.()
            this.parsedUnsub = null
        }
        this.parsedUnsub = this.term.onWriteParsed(open) ?? null
        this.gateTimer = setTimeout(() => {
            if (!opened) {
                this.inputEnabled = true
                opened = true
                this.parsedUnsub?.()
                this.parsedUnsub = null
            }
        }, REPLAY_PARSE_MAX_MS)
    }

    private onAttachErr(payload: Uint8Array): void {
        const msg = decoder.decode(payload)
        logger.warn('mux', 'attach rejected (sid=%s): %s', this.sid, msg)
        this.unregister()
        this.hooks.onGone?.()
    }

    private onEvent(payload: Uint8Array): void {
        const status = payload.length > 0 ? payload[0] : 0
        if (status === EVENT_DESTROYED) {
            this.hooks.onGone?.()
        } else {
            // 被抢占(0x01)或未知事件:显示断开弹窗。刻意不自动重连,
            // 避免两个客户端来回抢占死循环(设计稿 §3.2)。
            const msg =
                status === EVENT_PREEMPTED
                    ? 'Session is already attached by another client'
                    : 'Session ended'
            this.hooks.onDisconnect?.(msg)
        }
    }

    // onConnClose:底层 WS 整体断开(网络/服务重启)。通知组件、安排重连。
    onConnClose(message: string): void {
        this.clearTimers()
        this.inputEnabled = false
        this.term.deactivate()
        this.hooks.onDisconnect?.(message)
        if (this.reconnectSeconds > 0 && !this.closed) {
            this.reconnectTimer = setTimeout(() => {
                void this.reconnect()
            }, this.reconnectSeconds * 1000)
        }
    }

    // reconnect:手动重连(弹窗"重连"按钮)。先确认会话仍存活(resolveSession
    // 可在消失时直接重建同 id),再经 Multiplexer 重附着本通道。
    async reconnect(): Promise<void> {
        this.clearTimers()
        const id = await this.hooks.resolveSession?.()
        if (id === null || id === undefined) {
            this.hooks.onGone?.()
            return
        }
        this.term.reset()
        this.mux.reconnectNow(this.sid)
    }

    // unregister:通道级释放(Detach/AttachErr)。摘除终端回调、标记关闭。
    // 注意:从路由表删除由调用方(Multiplexer.detach / dispatch)负责。
    unregister(): void {
        this.closed = true
        this.clearTimers()
        this.parsedUnsub?.()
        this.parsedUnsub = null
        this.term.deactivate()
    }

    private clearTimers(): void {
        if (this.gateTimer) {
            clearTimeout(this.gateTimer)
            this.gateTimer = null
        }
        if (this.reconnectTimer) {
            clearTimeout(this.reconnectTimer)
            this.reconnectTimer = null
        }
    }
}

export { SessionChannel }
