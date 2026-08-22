// 统一前端日志:所有日志带 [gotty][tag] 前缀,便于过滤排查。
//
// 分级策略:
//   - info/warn/error:连接生命周期、会话操作等关键事件,默认打印;
//   - debug:协议帧级等高频日志,默认关闭 —— 浏览器控制台执行
//     localStorage.setItem('gotty.debug', '1') 后刷新开启。
type Level = 'debug' | 'info' | 'warn' | 'error'

const PREFIX = '[gotty]'

function isDebugEnabled(): boolean {
    try {
        return localStorage.getItem('gotty.debug') === '1'
    } catch {
        return false
    }
}

function log(level: Level, tag: string, message: string, ...args: unknown[]) {
    const line = `${PREFIX}[${tag}] ${message}`
    switch (level) {
        case 'debug':
            if (isDebugEnabled()) console.debug(line, ...args)
            break
        case 'info':
            console.info(line, ...args)
            break
        case 'warn':
            console.warn(line, ...args)
            break
        case 'error':
            console.error(line, ...args)
            break
    }
}

export const logger = {
    debug: (tag: string, message: string, ...args: unknown[]) =>
        log('debug', tag, message, ...args),
    info: (tag: string, message: string, ...args: unknown[]) =>
        log('info', tag, message, ...args),
    warn: (tag: string, message: string, ...args: unknown[]) =>
        log('warn', tag, message, ...args),
    error: (tag: string, message: string, ...args: unknown[]) =>
        log('error', tag, message, ...args),
}