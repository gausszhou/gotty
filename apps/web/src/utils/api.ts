// 与 GoTTY REST API 通信的薄封装。
import { logger } from './logger'

export interface SessionInfo {
    id: string;
    state: string; // idle | running | destroyed
    command: string;
    args: string[];
    pid: number;
    exited: boolean;
    title?: string; // 显示名(空 = 自动编号)
    created_at: string;
}

// 服务端持久化的历史会话记录(id/command/args/title/state/created_at)
export interface HistoryInfo {
    id: string;
    command: string;
    args: string[];
    title?: string;
    state: string;
    created_at: number; // unix 秒
}

export interface SessionList {
    sessions: SessionInfo[];
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

export async function listSessions(): Promise<SessionInfo[]> {
    const data = await fetchJSON<SessionList>('/api/sessions');
    return data.sessions ?? [];
}

// listHistory 返回服务端持久化的会话历史(重启后仍可恢复)
export async function listHistory(): Promise<HistoryInfo[]> {
    const data = await fetchJSON<SessionList>('/api/sessions/history');
    return (data.sessions as unknown as HistoryInfo[]) ?? [];
}

// createSession 新建会话;command 为空时服务端回退到默认命令($SHELL)
export async function createSession(command = '', args: string[] = []): Promise<SessionInfo> {
    return fetchJSON<SessionInfo>('/api/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ command, args }),
    });
}

// renameSession 持久化会话显示名(活会话与历史均支持,存服务端)
export async function renameSession(id: string, title: string): Promise<void> {
    await fetch(`/api/sessions/${id}/title`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title }),
    });
}

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