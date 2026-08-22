// 会话清单(manifest):设备本地的会话列表,localStorage 持久化。
// 服务端不保存列表,只按会话 id 保留记录;本模块是"这台设备上有
// 哪些会话"的唯一事实来源。
import { logger } from './logger'

export const MANIFEST_KEY = 'gotty.sessions'

export interface ManifestEntry {
    id: string
    title?: string // 显示名(可空,回退自动编号)
    command: string
    args: string[]
    createdAt: number // unix 毫秒
    lastSeen: number // unix 毫秒,最近一次在本设备打开
}

// 与服务端 utils.RandomString(16) 一致:16 位 base36([0-9a-z])。
const BASE36 = '0123456789abcdefghijklmnopqrstuvwxyz'
const ID_LENGTH = 16

// generateSessionID 用 crypto.getRandomValues 生成 16 位 base36 会话 id,
// 与服务端 id 格式校验(utils.IsValidSessionID)保持一致。
export function generateSessionID(): string {
    const bytes = new Uint8Array(ID_LENGTH)
    crypto.getRandomValues(bytes)
    let id = ''
    for (let i = 0; i < ID_LENGTH; i++) {
        id += BASE36[bytes[i] % BASE36.length]
    }
    return id
}

export function loadManifest(): ManifestEntry[] {
    try {
        const raw = localStorage.getItem(MANIFEST_KEY)
        if (!raw) return []
        const parsed = JSON.parse(raw)
        if (!Array.isArray(parsed)) return []
        return parsed.filter(
            (e): e is ManifestEntry =>
                e && typeof e.id === 'string' && e.id.length === ID_LENGTH,
        )
    } catch (err) {
        logger.warn('manifest', 'failed to load manifest: %s', err)
        return []
    }
}

function saveManifest(entries: ManifestEntry[]) {
    try {
        localStorage.setItem(MANIFEST_KEY, JSON.stringify(entries))
    } catch (err) {
        logger.warn('manifest', 'failed to save manifest: %s', err)
    }
}

// upsertManifest 新增或更新一个清单条目(按 id 去重,保持创建顺序)。
export function upsertManifest(entry: ManifestEntry): ManifestEntry[] {
    const entries = loadManifest()
    const idx = entries.findIndex((e) => e.id === entry.id)
    if (idx >= 0) {
        entries[idx] = entry
    } else {
        entries.push(entry)
    }
    saveManifest(entries)
    return entries
}

// touchManifest 更新最后打开时间;返回更新后的清单。
export function touchManifest(id: string, now = Date.now()): ManifestEntry[] {
    const entries = loadManifest()
    const idx = entries.findIndex((e) => e.id === id)
    if (idx < 0) return entries
    entries[idx] = { ...entries[idx], lastSeen: now }
    saveManifest(entries)
    return entries
}

// removeFromManifest 从清单移除一个会话(忘记);服务端记录保留,
// 但本设备不再展示它。
export function removeFromManifest(id: string): ManifestEntry[] {
    const entries = loadManifest().filter((e) => e.id !== id)
    saveManifest(entries)
    return entries
}

// findManifestEntry 按 id 查找清单条目。
export function findManifestEntry(id: string): ManifestEntry | undefined {
    return loadManifest().find((e) => e.id === id)
}