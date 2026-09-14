import { logger } from './logger'

// 多路复用收发层:一条 WebSocket 连接承载 N 个会话通道,按 session id 路由。
// 帧格式(与服务端 internal/terminal/protocol.go 完全一致):
//
//	[ sid 16B ][ type 1B ][ len 2B BE ][ payload ]
//
// sid 全 0 表示连接级消息(心跳 / RTT 测量);其余按 sid 投递到对应通道。

export const WS_PROTOCOLS = ['webtty']

// 客户端(→服务端):输入 / 心跳 / 终端尺寸 / 附着 / 分离
const MSG_INPUT = 0x31 // '1'
const MSG_PING = 0x32 // '2'
const MSG_RESIZE = 0x33 // '3'
const MSG_ATTACH = 0x41 // 'A'
const MSG_DETACH = 0x44 // 'D'

// 服务端(→客户端):输出 / 心跳回应 / 窗口标题 / 偏好 / 重连秒数 / 握手完成 /
// 附着OK / 附着失败 / 事件
const MSG_OUTPUT = 0x31 // '1'
const MSG_PONG = 0x32 // '2'
const MSG_WINDOW_TITLE = 0x33 // '3'
const MSG_PREFERENCES = 0x34 // '4'
const MSG_RECONNECT = 0x35 // '5'
const MSG_REPLAY_DONE = 0x36 // 历史重放已移除;该帧仍是"输入可上行"握手标记
const MSG_ATTACH_OK = 0x61 // 'a'
const MSG_ATTACH_ERR = 0x62 // 'b'
const MSG_EVENT = 0x45 // 'E'

// 事件状态码('E' 帧 payload)
const EVENT_PREEMPTED = 0x01 // 被其他客户端抢占
const EVENT_DESTROYED = 0x02 // 会话已销毁

const ROUTE_SESSION_ID_LEN = 16
const ROUTE_HEADER_LEN = ROUTE_SESSION_ID_LEN + 1 + 2

const encoder = new TextEncoder()
const decoder = new TextDecoder()

// TermHandle:xterm 组件只需暴露这几个能力,其余由本模块直接处理。
export interface TermHandle {
    info(): { columns: number; rows: number }
    write(data: Uint8Array): void
    setWindowTitle(title: string): void
    reset(): void
    deactivate(): void
    onInput(callback: (input: string) => void): void
    onResize(callback: (columns: number, rows: number) => void): void
    // 解析完成事件(返回退订函数):输入上行等待重放解析完再开启。
    onWriteParsed(callback: () => void): (() => void) | undefined
}

export interface WSHooks {
    onConnect?: () => void
    onDisconnect?: (message: string) => void
    onGone?: () => void
    onLatency?: (ms: number | null) => void
    // 渲染就绪:收到服务端握手标记(MSG_REPLAY_DONE)。CaptureView 据此
    // 置 window.__gottyCaptureReady,供无头浏览器(capture browser 引擎)
    // 轮询后截图。
    onReady?: () => void
    // 自动重连前确认会话仍存活;返回 null 则停止重连。
    resolveSession?: () => Promise<string | null>
}

export interface WSWrapper {
    close(): void
    reconnect(): void
}

// encode 编码一条[会话级]帧(不含路由头):[type][payload]。
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

// columns/rows 太小即视为"容器尚未就绪"的探测值(FitAddon 在隐藏
// 容器上会把 rows 钳到 1):发出去会把 PTY 缩成 1 行,画面只剩一行、
// 无法向下。过滤,等真实尺寸(激活后的 fit)再发。
function saneSize(columns: number, rows: number): boolean {
    return columns >= 2 && rows >= 2
}

// encodeRoute 包裹路由头:[sid 16B][type 1B][len 2B BE][payload]。
// sid 为 16 个 ASCII 字符(服务端生成的 base36 id);payload 可为字符串
// (UTF-8)或原始字节(输入);连接级 sid 传 16 个 '\u0000'。
function encodeRoute(sid: string, type: number, payload?: string | Uint8Array): Uint8Array {
    const sidBuf = new Uint8Array(ROUTE_SESSION_ID_LEN)
    const sidBytes = encoder.encode(sid)
    sidBuf.set(sidBytes.subarray(0, ROUTE_SESSION_ID_LEN))

    let body: Uint8Array
    if (payload == null) {
        body = new Uint8Array(0)
    } else if (typeof payload === 'string') {
        body = encoder.encode(payload)
    } else {
        body = payload
    }

    const frame = new Uint8Array(ROUTE_HEADER_LEN + body.length)
    frame.set(sidBuf, 0)
    frame[ROUTE_SESSION_ID_LEN] = type
    frame[ROUTE_SESSION_ID_LEN + 1] = (body.length >> 8) & 0xff
    frame[ROUTE_SESSION_ID_LEN + 2] = body.length & 0xff
    frame.set(body, ROUTE_HEADER_LEN)
    return frame
}

interface RoutedFrame {
    sid: string
    type: number
    payload: Uint8Array
}

// decodeRoute 解析一条路由帧;返回 sid(16 字符)、类型与 payload。
function decodeRoute(data: Uint8Array): RoutedFrame {
    const sid = decoder.decode(data.subarray(0, ROUTE_SESSION_ID_LEN))
    const type = data[ROUTE_SESSION_ID_LEN]
    const len = (data[ROUTE_SESSION_ID_LEN + 1] << 8) | data[ROUTE_SESSION_ID_LEN + 2]
    const payload = data.subarray(ROUTE_HEADER_LEN, ROUTE_HEADER_LEN + len)
    return { sid, type, payload }
}

// isConnLevel 判断 sid 是否为全 0 的连接级标记。
function isConnLevel(sid: string): boolean {
    for (let i = 0; i < sid.length; i++) {
        if (sid.charCodeAt(i) !== 0) return false
    }
    return true
}

export {
    MSG_INPUT,
    MSG_PING,
    MSG_RESIZE,
    MSG_ATTACH,
    MSG_DETACH,
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
    ROUTE_SESSION_ID_LEN,
    ROUTE_HEADER_LEN,
    encode,
    encodeRoute,
    decodeRoute,
    isConnLevel,
    disconnectMessage,
    saneSize,
}
