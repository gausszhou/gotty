// 与 GoTTY REST API 通信的薄封装。

export interface SessionInfo {
    id: string;
    state: string; // idle | running | destroyed
    command: string;
    args: string[];
    pid: number;
    exited: boolean;
    created_at: string;
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
        throw new APIError(res.status, message);
    }
    return res.json() as Promise<T>;
}

export async function listSessions(): Promise<SessionInfo[]> {
    const data = await fetchJSON<SessionList>('/api/sessions');
    return data.sessions ?? [];
}

// createSession 新建会话;command 为空时服务端回退到 CLI 默认命令。
export async function createSession(command = ''): Promise<SessionInfo> {
    return fetchJSON<SessionInfo>('/api/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ command, args: [] }),
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