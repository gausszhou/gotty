export const protocols = ["webtty"];

// Message type bytes (binary protocol)
export const msgInput           = 0x31; // '1'
export const msgPing            = 0x32; // '2'
export const msgResizeTerminal  = 0x33; // '3'

export const msgOutput          = 0x31; // '1'
export const msgPong            = 0x32; // '2'
export const msgSetWindowTitle  = 0x33; // '3'
export const msgSetPreferences  = 0x34; // '4'
export const msgSetReconnect    = 0x35; // '5'

const encoder = new TextEncoder();
const decoder = new TextDecoder();

// encodeMsg builds a binary frame: [type byte, ...payload bytes]
function encodeMsg(type: number, payload?: string): Uint8Array {
    if (payload !== undefined) {
        const p = encoder.encode(payload);
        const msg = new Uint8Array(1 + p.length);
        msg[0] = type;
        msg.set(p, 1);
        return msg;
    }
    return new Uint8Array([type]);
}

export interface Terminal {
    info(): { columns: number, rows: number };
    output(data: Uint8Array): void;
    showMessage(message: string, timeout: number): void;
    removeMessage(): void;
    setWindowTitle(title: string): void;
    setPreferences(value: object): void;
    onInput(callback: (input: string) => void): void;
    onResize(callback: (columns: number, rows: number) => void): void;
    reset(): void;
    deactivate(): void;
    close(): void;
}

export interface Connection {
    open(): void;
    close(): void;
    send(data: string | Uint8Array): void;
    isOpen(): boolean;
    onOpen(callback: () => void): void;
    onReceive(callback: (data: Uint8Array) => void): void;
    onClose(callback: () => void): void;
}

export interface ConnectionFactory {
    // create builds a connection to the given session.
    create(sessionId: string): Connection;
}

// SessionResolver returns the id of a living session, creating a new one
// when the previously used session is gone (e.g. destroyed by idle timeout).
export type SessionResolver = () => Promise<string>;

export class WebTTY {
    term: Terminal;
    connectionFactory: ConnectionFactory;
    args: string;
    authToken: string;
    reconnect: number;
    initialSessionId: string;
    resolveSession: SessionResolver;

    constructor(
        term: Terminal,
        connectionFactory: ConnectionFactory,
        args: string,
        authToken: string,
        initialSessionId: string,
        resolveSession: SessionResolver,
    ) {
        this.term = term;
        this.connectionFactory = connectionFactory;
        this.args = args;
        this.authToken = authToken;
        this.reconnect = -1;
        this.initialSessionId = initialSessionId;
        this.resolveSession = resolveSession;
    };

    open() {
        let pingTimer: ReturnType<typeof setInterval>;
        let reconnectTimeout: ReturnType<typeof setTimeout>;
        let connection: Connection;

        const connect = (sessionId: string) => {
            connection = this.connectionFactory.create(sessionId);

            const setup = () => {
                connection.onOpen(() => {
                    const termInfo = this.term.info();

                    // Send init message (JSON as binary)
                    connection.send(encoder.encode(JSON.stringify({
                        Arguments: this.args,
                        AuthToken: this.authToken,
                    })));

                    const resizeHandler = (columns: number, rows: number) => {
                        connection.send(encodeMsg(msgResizeTerminal, JSON.stringify({
                            columns: columns,
                            rows: rows,
                        })));
                    };

                    this.term.onResize(resizeHandler);
                    resizeHandler(termInfo.columns, termInfo.rows);

                    this.term.onInput(
                        (input: string) => {
                            connection.send(encodeMsg(msgInput, input));
                        }
                    );

                    pingTimer = setInterval(() => {
                        connection.send(encodeMsg(msgPing));
                    }, 30 * 1000);

                });

                connection.onReceive((data) => {
                    const type = data[0];
                    const payload = data.slice(1);
                    switch (type) {
                        case msgOutput:
                            this.term.output(payload);
                            break;
                        case msgPong:
                            break;
                        case msgSetWindowTitle:
                            this.term.setWindowTitle(decoder.decode(payload));
                            break;
                        case msgSetPreferences:
                            const preferences = JSON.parse(decoder.decode(payload));
                            this.term.setPreferences(preferences);
                            break;
                        case msgSetReconnect:
                            const autoReconnect = JSON.parse(decoder.decode(payload));
                            console.log("Enabling reconnect: " + autoReconnect + " seconds")
                            this.reconnect = autoReconnect;
                            break;
                    }
                });

                connection.onClose(() => {
                    clearInterval(pingTimer);
                    this.term.deactivate();
                    this.term.showMessage("Connection Closed", 0);
                    if (this.reconnect > 0) {
                        this.term.showMessage(
                            "Reconnecting in " + this.reconnect + " seconds...", 0);
                        reconnectTimeout = setTimeout(async () => {
                            try {
                                const id = await this.resolveSession();
                                this.term.reset();
                                connect(id);
                            } catch (err) {
                                console.error("Failed to resolve a living session:", err);
                            }
                        }, this.reconnect * 1000);
                    }
                });

                connection.open();
            }

            setup();
        };

        connect(this.initialSessionId);

        return () => {
            clearTimeout(reconnectTimeout);
            connection.close();
        }
    };
};