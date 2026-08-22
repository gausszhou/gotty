// 与 GoTTY REST API 通信的薄封装。
// 会话列表由客户端 localStorage 清单(utils/manifest.ts)驱动,
// 服务端只提供:创建(幂等/复活)、详情、状态批量查询、销毁。
import { logger } from './logger'
import type { Theme } from './theme'

export interface SessionInfo {
    id: string;
    state: string; // idle | running | destroyed
    command: string;
    args: string[];
    pid: number;
    exited: boolean;
    title?: string; // 显示名(可空;页签标题由程序 OSC 0/2 自动命名)
    created_at: string;
}

export class APIError extends Error {
    status: number;

    constructor(status: number, message: string) {
        super(message);
        this.status = status;
    }
}

export async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
    const method = init?.method || 'GET';
    logger.debug('api', '%s %s', method, url);
    const res = await fetch(url, init);
    if (!res.ok) {
        // 优先透出服务端 JSON 错误体,如 {"error":"no command given"}
        let message = `request to ${url} failed with status ${res.status}`;
        try {
            const body = await res.json();
            if (body && typeof body.error === 'string') {
                message = body.error;
            }
        } catch {
            // 非 JSON 错误体,用默认消息
        }
        logger.warn('api', '%s %s -> %d: %s', method, url, res.status, message);
        throw new APIError(res.status, message);
    }
    return res.json() as Promise<T>;
}

// createSession 新建会话;command 为空时服务端回退到默认命令($SHELL)。
// id 为客户端生成的会话 id(16 位 base36):已存活 → 幂等返回现有会话;
// 服务端有记录 → 复活(记录命令重建,run_count+1);无 id → 服务端生成。
// theme 上报创建设备的页面主题(dark/light),服务端据此设置 PTY 的
// COLORFGBG,让会话内的程序按实际背景深浅着色。
export async function createSession(
    command = '',
    args: string[] = [],
    id?: string,
    theme?: Theme,
): Promise<SessionInfo> {
    const body: Record<string, unknown> = { command, args };
    if (id) body.id = id;
    if (theme) body.theme = theme;
    return fetchJSON<SessionInfo>('/api/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
}

// checkSessions 批量查询清单中 id 的存活状态(2s 轮询):
// 返回存活的会话,清单中未返回的 id 即服务端已无存活记录。
export async function checkSessions(ids: string[]): Promise<SessionInfo[]> {
    if (ids.length === 0) return [];
    const data = await fetchJSON<{ sessions: Record<string, SessionInfo> }>(
        '/api/sessions/status',
        {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ids }),
        },
    );
    return data.sessions ? Object.values(data.sessions) : [];
}

// getSession 获取单个会话详情。
export async function getSession(id: string): Promise<SessionInfo | null> {
    try {
        return await fetchJSON<SessionInfo>(`/api/sessions/${id}`);
    } catch {
        return null;
    }
}

export async function destroySession(id: string): Promise<void> {
    try {
        await fetch(`/api/sessions/${id}`, { method: 'DELETE' });
    } catch {
        // 会话可能已经不存在
    }
}