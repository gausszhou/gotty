import { logger } from './logger'

// 极简 WebSocket 收发层:一个连接、一套帧类型,直接桥接 xterm。
// 帧格式:[type byte][payload];类型同服务端 internal/terminal/protocol.go。

export const WS_PROTOCOLS = ['webtty']

// 客户端(→服务端):输入 / 心跳 / 终端尺寸
const MSG_INPUT = 0x31 // '1'
const MSG_PING = 0x32 // '2'
const MSG_RESIZE = 0x33 // '3'

// 服务端(→客户端):输出 / 心跳回应 / 窗口标题 / 偏好 / 重连秒数
const MSG_OUTPUT = 0x31
const MSG_PONG = 0x32
const MSG_WINDOW_TITLE = 0x33
const MSG_PREFERENCES = 0x34
const MSG_RECONNECT = 0x35

const encoder = new TextEncoder()
const decoder = new TextDecoder()

// TermHandle:xterm 组件只需暴露这几个能力,其余由本模块直接处理。
export interface TermHandle {
    info(): { columns: number, rows: number }
    write(data: Uint8Array): void
    setWindowTitle(title: string): void
    reset(): void
    deactivate(): void
    onInput(callback: (input: string) => void): void
    onResize(callback: (columns: number, rows: number) => void): void
}

export interface WSHooks {
    onConnect?: () => void
    onDisconnect?: (message: string) => void
    onGone?: () => void
    onLatency?: (ms: number) => void
    // 自动重连前确认会话仍存活;返回 null 则停止重连。
    resolveSession?: () => Promise<string | null>
}

function encode(type: number, payload?: string): Uint8Array {
    if (payload === undefined) return new Uint8Array([type])
    const body = encoder.encode(payload)
    const msg = new Uint8Array(1 + body.length)
    msg[0] = type
    msg.set(body, 1)
    return msg
}

function disconnectMessage(code: number, text: string): string {
    if (text) return text
    switch (code) {
        case 1006: return 'Network connection lost'
        case 1011: return 'Server error'
        case 1013: return 'Session is already attached by another client'
        default: return 'Connection closed'
    }
}

export interface WSWrapper {
    close(): void
    reconnect(): void
}

// openTerminalWS 建立一条会话 WebSocket 并完成收发桥接。
// 返回 { close, reconnect } 供组件在卸载/断开弹窗时调用。
export function openTerminalWS(term: TermHandle, sessionId: string, hooks: WSHooks = {}): WSWrapper {
    let closed = false
    let ws: WebSocket | null = null
    let reconnectSeconds = 0
    let pingTimer: ReturnType<typeof setInterval> | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let pendingPingAt: number | null = null
    // xterm 的 onData/onResize 是累加事件:每次 connect 若重新注册,
    // 重连后一次按键会发送多次输入 → 输入输出重复。只注册一次。
    let inputBound = false

    const clearTimers = () => {
        if (pingTimer) { clearInterval(pingTimer); pingTimer = null }
        if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
    }

    const connect = (sid: string) => {
        // 单连接语义:新连接抢占旧连接 —— 先关闭旧连接并摘除其回调,
        // 避免旧连接的 onclose 再触发断开/重连逻辑,也避免计时器叠加。
        clearTimers()
        if (ws && ws.readyState !== WebSocket.CLOSED) {
            const old = ws
            old.onopen = null
            old.onmessage = null
            old.onclose = null
            try { old.close() } catch { /* 已关闭的忽略 */ }
        }

        const scheme = window.location.protocol === 'https:' ? 'wss://' : 'ws://'
        const url = `${scheme}${window.location.host}/ws?session_id=${encodeURIComponent(sid)}`
        ws = new WebSocket(url, WS_PROTOCOLS)
        // 关键:二进制消息必须以 ArrayBuffer 到达,默认 Blob 会被 else 丢弃。
        ws.binaryType = 'arraybuffer'

        ws.onopen = () => {
            logger.info('ws', 'connected session=%s', sid)
            hooks.onConnect?.()
            // onopen 时连接必然已就绪:取局部常量以便 TS 类型收窄(闭包内无法收窄捕获的可变 ws)
            const sock = ws!

            // 回调只绑定一次;闭包引用的是最新 ws 变量,始终发往当前连接
            if (!inputBound) {
                inputBound = true
                term.onInput((input) => {
                    if (ws && ws.readyState === WebSocket.OPEN) ws.send(encode(MSG_INPUT, input))
                })
                term.onResize((columns, rows) => {
                    if (ws && ws.readyState === WebSocket.OPEN) {
                        ws.send(encode(MSG_RESIZE, JSON.stringify({ columns, rows })))
                    }
                })
            }
            const { columns, rows } = term.info()
            sock.send(encode(MSG_RESIZE, JSON.stringify({ columns, rows })))

            // 立即测一次延迟(不等第一个周期),之后每 2s 心跳保活 +
            // 刷新延迟/抖动指标(标题栏右侧展示)
            pendingPingAt = performance.now()
            sock.send(encode(MSG_PING))
            pingTimer = setInterval(() => {
                pendingPingAt = performance.now()
                if (ws && ws.readyState === WebSocket.OPEN) ws.send(encode(MSG_PING))
            }, 2000)
        }

        ws.onmessage = (ev) => {
            if (!(ev.data instanceof ArrayBuffer)) {
                logger.warn('ws', 'skip non-binary message (%s)', typeof ev.data)
                return
            }
            const data = new Uint8Array(ev.data)
            const type = data[0]
            const payload = data.subarray(1)
            logger.debug('ws', '<<< frame 0x%s len=%d (session=%s)', type.toString(16), payload.length, sid)
            switch (type) {
                case MSG_OUTPUT:
                    term.write(payload)
                    break
                case MSG_PONG:
                    if (pendingPingAt !== null) {
                        const rtt = Math.round(performance.now() - pendingPingAt)
                        pendingPingAt = null
                        hooks.onLatency?.(rtt)
                    }
                    break
                case MSG_WINDOW_TITLE:
                    term.setWindowTitle(decoder.decode(payload))
                    break
                case MSG_PREFERENCES:
                    break // xterm 构造参数已配置,无需动态应用
                case MSG_RECONNECT:
                    reconnectSeconds = Number(decoder.decode(payload))
                    break
            }
        }

        ws.onclose = (ev) => {
            clearTimers()
            pendingPingAt = null
            term.deactivate()
            const message = disconnectMessage(ev.code, ev.reason)
            logger.info('ws', 'closed session=%s code=%d msg=%s', sid, ev.code, message)
            hooks.onDisconnect?.(message)

            if (reconnectSeconds > 0 && !closed) {
                reconnectTimer = setTimeout(async () => {
                    const id = await hooks.resolveSession?.()
                    if (id === null || id === undefined) {
                        logger.warn('ws', 'session gone, stop reconnect (session=%s)', sid)
                        hooks.onGone?.()
                        return
                    }
                    term.reset()
                    connect(id)
                }, reconnectSeconds * 1000)
            }
        }
    }

    connect(sessionId)

    return {
        close() {
            closed = true
            clearTimers()
            if (ws) {
                ws.onopen = null
                ws.onmessage = null
                ws.onclose = null
            }
            ws?.close()
        },
        reconnect() {
            clearTimers()
            void (async () => {
                const id = await hooks.resolveSession?.()
                if (id === null || id === undefined) {
                    hooks.onGone?.()
                    return
                }
                term.reset()
                connect(id)
            })()
        },
    }
}