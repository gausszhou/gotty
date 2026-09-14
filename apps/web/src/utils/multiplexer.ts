import { logger } from './logger'
import {
    WS_PROTOCOLS,
    MSG_PING,
    MSG_PONG,
    MSG_ATTACH,
    MSG_DETACH,
    MSG_ATTACH_OK,
    MSG_ATTACH_ERR,
    encodeRoute,
    decodeRoute,
    isConnLevel,
    disconnectMessage,
} from './ws'
import type { TermHandle, WSHooks, WSWrapper } from './ws'
import { SessionChannel } from './session-channel'

// 多路复用收发层:整个页面只有"一条" WebSocket 连接,连接内按 session id
// 路由出 N 个逻辑会话通道(见 docs/design/ws-multiplex.md)。
//
// - 连接级消息(sid 全 0):Ping/Pong 保活 + RTT 测量,RTT 广播给所有通道。
// - 会话级消息:[sid][type][len][payload] 解码后投递到对应 SessionChannel。
//
// 多个 TerminalPane / CaptureView 共享同一个 multiplexer 单例;某条通道关闭
// (视图 ✕)仅发 Detach 释放该通道,不关闭底层连接;连接整体断开时所有通道
// 收 onDisconnect,各视图弹"连接已断开"。
//
// 连接级 sid:16 个 '\u0000'。encodeRoute 会把空 sid 编码成 16 字节零,
// decodeRoute 解码回全零,sid 经 isConnLevel 判定为连接级。
const CONN_LEVEL_SID = '\u0000'.repeat(16)

// Multiplexer 拥有单条 WebSocket,并维护 sid -> SessionChannel 的路由表。
class Multiplexer {
    private ws: WebSocket | null = null
    private connectPromise: Promise<void> | null = null

    private channels = new Map<string, SessionChannel>()
    private pending = new Map<string, (wrapper: WSWrapper) => void>()

    private pingTimer: ReturnType<typeof setInterval> | null = null
    private pendingPingAt: number | null = null

    // attach 绑定一条逻辑通道到会话 sid。确保底层 WS 已连接后注册通道并
    // 发送 Attach 帧;服务端回 AttachOK 时 resolve 出 WSWrapper(并触发
    // 组件的 onConnect),回 AttachErr 时触发 onGone(会话已销毁/不存在)。
    async attach(sid: string, term: TermHandle, hooks: WSHooks): Promise<WSWrapper> {
        const existing = this.channels.get(sid)
        if (existing) return existing.wrapper

        const channel = new SessionChannel(sid, term, hooks, this)
        this.channels.set(sid, channel)
        const wrapper = this.makeWrapper(channel)
        channel.wrapper = wrapper

        try {
            await this.ensureConnected()
        } catch (err) {
            // 传输层在握手前就失败:通道已注册,稍后用户点重连可重建连接。
            logger.warn('mux', 'connect failed (sid=%s): %s', sid, err)
            hooks.onDisconnect?.('Connection failed')
            return wrapper
        }

        channel.startGate()
        this.send(sid, MSG_ATTACH)
        return new Promise<WSWrapper>((resolve) => {
            this.pending.set(sid, resolve)
        })
    }

    // detach 显式释放一条逻辑通道(视图关闭):发送 Detach 帧,服务端保留会话,
    // 仅释放该通道;底层 WS 不动(其他通道仍在使用)。
    detach(sid: string): void {
        const ch = this.channels.get(sid)
        if (!ch) return
        this.send(sid, MSG_DETACH)
        this.channels.delete(sid)
        ch.unregister()
    }

    // reconnectNow 在 WS 已开时仅重附着该 sid;WS 未开则(重)开连接,
    // onopen 会把所有已注册通道一并重附着。
    reconnectNow(sid: string): void {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.channels.get(sid)?.reattach()
        } else {
            void this.ensureConnected()
        }
    }

    // send 包裹路由头写一帧到共享 WS。WS 未就绪时静默丢弃(重附着后会重发尺寸)。
    send(sid: string, type: number, payload?: string | Uint8Array): void {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
        try {
            this.ws.send(encodeRoute(sid, type, payload))
        } catch (err) {
            logger.warn('mux', 'send failed (sid=%s): %s', sid, err)
        }
    }

    // ── 内部:连接生命周期 ────────────────────────────────────────────────

    private ensureConnected(): Promise<void> {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) return Promise.resolve()
        if (this.connectPromise) return this.connectPromise
        this.connectPromise = this.open()
        return this.connectPromise
    }

    private open(): Promise<void> {
        return new Promise<void>((resolve, reject) => {
            const scheme = window.location.protocol === 'https:' ? 'wss://' : 'ws://'
            const url = `${scheme}${window.location.host}/ws`
            const ws = new WebSocket(url, WS_PROTOCOLS)
            // 关键:二进制消息必须以 ArrayBuffer 到达,默认 Blob 会被丢弃。
            ws.binaryType = 'arraybuffer'
            this.ws = ws

            ws.onopen = () => {
                this.connectPromise = null
                this.startPing()
                // 整条连接(重)开:重附着所有已注册通道(各自会重新握手 + 重新门控输入)。
                for (const ch of this.channels.values()) ch.reattach()
                resolve()
            }
            ws.onmessage = (ev) => this.onMessage(ev)
            ws.onclose = (ev) => this.onClose(ev)
            ws.onerror = () => {
                // 真实 code/reason 由紧随的 onclose 给出;此处仅终止首次连接 Promise。
                if (this.connectPromise) {
                    this.connectPromise = null
                    reject(new Error('websocket error'))
                }
            }
        })
    }

    private onMessage(ev: MessageEvent): void {
        const data = ev.data
        if (!(data instanceof ArrayBuffer)) {
            logger.warn('mux', 'skip non-binary message (%s)', typeof data)
            return
        }
        let decoded
        try {
            decoded = decodeRoute(new Uint8Array(data))
        } catch (err) {
            logger.warn('mux', 'bad route frame: %s', err)
            return
        }
        const { sid, type, payload } = decoded
        if (isConnLevel(sid)) {
            this.handleConnLevel(type)
            return
        }
        this.dispatch(sid, type, payload)
    }

    private handleConnLevel(type: number): void {
        if (type === MSG_PING) {
            // 回 Pong(连接级 sid 全 0),供服务端保活。
            this.send(CONN_LEVEL_SID, MSG_PONG)
        } else if (type === MSG_PONG) {
            if (this.pendingPingAt !== null) {
                const rtt = Math.round(performance.now() - this.pendingPingAt)
                this.pendingPingAt = null
                for (const ch of this.channels.values()) ch.reportLatency(rtt)
            }
        }
    }

    private dispatch(sid: string, type: number, payload: Uint8Array): void {
        const channel = this.channels.get(sid)
        if (!channel) {
            logger.debug('mux', 'frame for unknown sid %s (type 0x%s)', sid, type.toString(16))
            return
        }
        if (type === MSG_ATTACH_OK) {
            const resolve = this.pending.get(sid)
            this.pending.delete(sid)
            channel.onAttached()
            resolve?.(channel.wrapper)
            return
        }
        if (type === MSG_ATTACH_ERR) {
            const resolve = this.pending.get(sid)
            this.pending.delete(sid)
            this.channels.delete(sid) // 会话已销毁/不存在:摘除通道
            channel.handleFrame(type, payload) // 内部触发 onGone
            resolve?.(channel.wrapper)
            return
        }
        channel.handleFrame(type, payload)
    }

    private onClose(ev: CloseEvent): void {
        this.clearPing()
        this.connectPromise = null
        const message = disconnectMessage(ev.code, ev.reason)
        logger.info('mux', 'connection closed code=%d msg=%s', ev.code, message)
        // 连接整体断开:所有通道收 onDisconnect(各自按 reconnectSeconds 安排重连)。
        // 通道保持注册,以便重连后 onopen 重新附着它们。
        for (const ch of this.channels.values()) ch.onConnClose(message)
    }

    private startPing(): void {
        this.clearPing()
        // 立即测一次延迟(不等首个周期),之后每 2s 心跳保活 + 刷新 RTT。
        this.pendingPingAt = performance.now()
        this.send(CONN_LEVEL_SID, MSG_PING)
        this.pingTimer = setInterval(() => {
            this.pendingPingAt = performance.now()
            this.send(CONN_LEVEL_SID, MSG_PING)
        }, 2000)
    }

    private clearPing(): void {
        if (this.pingTimer) {
            clearInterval(this.pingTimer)
            this.pingTimer = null
        }
        this.pendingPingAt = null
    }

    private makeWrapper(channel: SessionChannel): WSWrapper {
        return {
            close: () => this.detach(channel.sid),
            reconnect: () => {
                void channel.reconnect()
            },
        }
    }
}

// 全局唯一 Multiplexer:每页一条 WS,多个常驻视图共享。
export const multiplexer = new Multiplexer()
export type { Multiplexer }
