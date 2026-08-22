export class ConnectionFactory {
    baseUrl: string;
    protocols: string[];

    constructor(baseUrl: string, protocols: string[]) {
        this.baseUrl = baseUrl;
        this.protocols = protocols;
    };

    create(sessionId: string): Connection {
        return new Connection(
            this.baseUrl + '?session_id=' + encodeURIComponent(sessionId),
            this.protocols,
        );
    };
}

export class Connection {
    bare: WebSocket;

    constructor(url: string, protocols: string[]) {
        this.bare = new WebSocket(url, protocols);
    }

    open() {
        // nothing todo for websocket
    };

    close() {
        this.bare.close();
    };

    send(data: string | Uint8Array) {
        if (typeof data === 'string') {
            this.bare.send(new TextEncoder().encode(data));
        } else {
            this.bare.send(data);
        }
    };

    isOpen(): boolean {
        if (this.bare.readyState == WebSocket.CONNECTING ||
            this.bare.readyState == WebSocket.OPEN) {
            return true
        }
        return false
    }

    onOpen(callback: () => void) {
        this.bare.onopen = (event) => {
            callback();
        }
    };

    onReceive(callback: (data: Uint8Array) => void) {
        this.bare.onmessage = (event) => {
            if (event.data instanceof ArrayBuffer) {
                callback(new Uint8Array(event.data));
            }
        }
    };

    onClose(callback: () => void) {
        this.bare.onclose = (event) => {
            callback();
        };
    };
}