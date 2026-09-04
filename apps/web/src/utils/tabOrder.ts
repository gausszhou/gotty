// 页签顺序(tab order):本设备上用户拖拽调整后的页签显示顺序。
// 与清单(manifest)一样是"这台设备"的本地偏好,localStorage 持久化;
// 服务端不保存顺序,仅按会话 id 保留记录。
import { logger } from './logger'

export const TAB_ORDER_KEY = 'gotty.tabOrder'

// loadTabOrder 读取持久化的页签顺序(会话 id 数组)。
// 顺序中可能残留已销毁/已移除的 id:渲染时按现存会话过滤,无害。
export function loadTabOrder(): string[] {
    try {
        const raw = localStorage.getItem(TAB_ORDER_KEY)
        if (!raw) return []
        const parsed = JSON.parse(raw)
        if (!Array.isArray(parsed)) return []
        return parsed.filter((id): id is string => typeof id === 'string')
    } catch (err) {
        logger.warn('tabOrder', 'failed to load tab order: %s', err)
        return []
    }
}

// saveTabOrder 持久化当前页签显示顺序(ids 为会话 id 数组)。
export function saveTabOrder(ids: string[]) {
    try {
        localStorage.setItem(TAB_ORDER_KEY, JSON.stringify(ids))
    } catch (err) {
        logger.warn('tabOrder', 'failed to save tab order: %s', err)
    }
}