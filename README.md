# GoTTY - Share your terminal as a web application

GoTTY is a simple command line tool that turns your CLI tools into web
applications. It runs a command in a PTY and serves it to browsers over
WebSocket, with REST API based session management.

![Screenshot](screenshot.gif)

# Features

- **Multiple sessions** — create, attach, detach and destroy terminal sessions via a REST API.
- **Reconnection** — a session's process keeps running while its client is disconnected; the client can rejoin the same session.
- **Binary WebSocket protocol** — terminal streams are transferred as raw bytes (no base64 overhead).
- **Modern frontend** — Vite + Vue 3 + xterm.js with WebGL rendering.

# Installation

Download the latest stable binary file from the [Releases](https://github.com/gausszhou/gotty/releases) page.
(File names containing `darwin_amd64`/`darwin_arm64` are for macOS users.)

Or build from source:

```sh
# requires Go 1.26+, Node.js 18+ and pnpm
make all
```

# Usage

```
Usage: gotty serve [flags] [command [<arguments...>]]
```

Run the server with your preferred command as its arguments
(e.g. `gotty serve --port 8080 top`), then open `http://localhost:8080` in
your browser: the page creates a session running the command and attaches
to it.

The session id is kept in the URL (`?id=xxx`), so reloading the page
rejoins the same running session; if the session is gone, a new one is
created.

Starting without a command (`gotty serve`) falls back to the login shell
(`$SHELL`, or `/bin/sh` when unset) as the default session command, so the
page always opens with a usable terminal. An explicit command in
`POST /api/sessions` always takes precedence.

## Options

```
-a, --address string        IP address to listen (default: "0.0.0.0") [$GOTTY_ADDRESS]
-p, --port string           Port number to listen (default: "8080") [$GOTTY_PORT]
-w, --permit-write          Permit clients to write to the TTY (BE CAREFUL) [$GOTTY_PERMIT_WRITE]
    --title-format string   Title format of browser window (default: "GoTTY - {{ .command }}@{{ .hostname }}") [$GOTTY_TITLE_FORMAT]
    --reconnect             Enable reconnection [$GOTTY_RECONNECT]
    --reconnect-time int    Time to reconnect (default: 10) [$GOTTY_RECONNECT_TIME]
    --max-session int       Maximum number of concurrent sessions (default: 0 = unlimited) [$GOTTY_MAX_SESSION]
    --timeout int           Idle timeout seconds for destroying unattached sessions (default: 0 = disabled) [$GOTTY_TIMEOUT]
    --width int             Static width of the screen, 0(default) means dynamically resize [$GOTTY_WIDTH]
    --height int            Static height of the screen, 0(default) means dynamically resize [$GOTTY_HEIGHT]
    --ws-origin string      A regular expression that matches origin URLs to be accepted by WebSocket [$GOTTY_WS_ORIGIN]
    --term string           Terminal name to use on the browser (default: "xterm") [$GOTTY_TERM]
-t, --tls                   Enable TLS/SSL [$GOTTY_TLS]
    --tls-crt string        TLS/SSL certificate file path (default: "~/.gotty.crt") [$GOTTY_TLS_CRT]
    --tls-key string        TLS/SSL key file path (default: "~/.gotty.key") [$GOTTY_TLS_KEY]
    --log-file string       Server log file path (default: "~/.gotty/logs/gotty.log", empty = console only) [$GOTTY_LOG_FILE]
    --close-signal int      Signal sent to the command process when the session is closed (default: 1 = SIGHUP) [$GOTTY_CLOSE_SIGNAL]
    --close-timeout int     Time in seconds to force kill process after the session is closed (default: -1 = disabled) [$GOTTY_CLOSE_TIMEOUT]
    --config string         Config file path (default: "~/.gotty/config.json") [$GOTTY_CONFIG]
-v, --version               print the version
```

### Config File

GoTTY loads a JSON profile file by default from `~/.gotty/config.json` when it exists.
The path can be changed with `--config` (also honored on the root command,
e.g. `gotty --config ./gotty.json serve`) or the `GOTTY_CONFIG` environment
variable. Command line flags take precedence over config file values, which
take precedence over `GOTTY_*` environment variables.

```json
{
  "port": "9000",
  "enable_tls": true,
  "permit_write": false
}
```

Config files are read from `~/.gotty/config.json` by default (override
with `--config FILE`). Unknown keys are ignored so that config files
written for older GoTTY versions keep working.

### Server Log

The server log is written to both the console and
`~/.gotty/logs/gotty.log` (append mode), so connection issues can be
checked later. Use `--log-file <path>` to change the path, or an empty
value to keep the console-only behavior.

### Security Options

By default, GoTTY doesn't allow clients to send any keystrokes or commands
except terminal window resizing. When you want to permit clients to write
input to the TTY, add the `-w` option. However, accepting input from remote
clients is dangerous for most commands.

All traffic between the server and clients is NOT encrypted by default.
When you send secret information through GoTTY, we strongly recommend you
use the `-t` option which enables TLS/SSL. By default, GoTTY loads the crt
and key files placed at `~/.gotty.crt` and `~/.gotty.key`. You can overwrite
these file paths with the `--tls-crt` and `--tls-key` options. To generate a
self-signed certificate:

```sh
openssl req -x509 -nodes -days 9999 -newkey rsa:2048 -keyout ~/.gotty.key -out ~/.gotty.crt
```

## Sharing with Multiple Clients

Each session runs one process, shared across page reloads: while one client
is attached, a second attach to the same session is rejected, but when the
attached client disconnects the process keeps running and any client with
the session URL can rejoin. For multiple *simultaneous* viewers, use a
terminal multiplexer:

```sh
$ gotty tmux new -A -s gotty top
```

This command doesn't allow clients to send keystrokes, however, you can
attach the session from your local terminal and run operations like
switching the mode of the `top` command. To connect to the tmux session
from your terminal, use the following command.

```sh
$ tmux new -A -s gotty
```

## REST API

### Sessions

```
POST   /api/sessions               create a session (empty command uses the default command)
GET    /api/sessions               list all sessions
GET    /api/sessions/history       list persisted session history
GET    /api/sessions/:id           session detail
PUT    /api/sessions/:id/title     rename a session (persisted, alive or historical)
DELETE /api/sessions/:id           destroy a session
POST   /api/sessions/:id/resize    resize the terminal {width, height}
POST   /api/sessions/:id/signal    send a signal {signal: "SIGINT" | "SIGHUP" | "SIGTERM" | "SIGKILL" | "SIGQUIT"}
```

Example:

```sh
$ curl -X POST localhost:8080/api/sessions \
    -d '{"command": "top", "width": 120, "height": 40}'
{"id":"a1b2c3d4","state":"idle","command":"top","args":[],"pid":1234,"exited":false,"created_at":"..."}

$ curl localhost:8080/api/sessions
{"sessions":[{"id":"a1b2c3d4","state":"running","command":"top", ...}]}
```

### WebSocket

```
WS /ws?session_id=xxx   attach to an existing session
```

The connection is established with subprotocol `webtty` (no handshake
frame required). Messages are binary frames of the form
`[type byte][payload]`:

| type | client → server | server → client |
|---|---|---|
| `0x31` ('1') | Input (raw bytes) | Output (raw bytes) |
| `0x32` ('2') | Ping | Pong |
| `0x33` ('3') | ResizeTerminal (JSON) | SetWindowTitle (string) |
| `0x34` ('4') | — | SetPreferences (JSON) |
| `0x35` ('5') | — | SetReconnect (JSON) |

## Development

Build the frontend and the binary:

```sh
make install   # pnpm install for the pnpm workspace
make all       # frontend (Vite) -> static (go:embed) -> release binary
make test      # go vet + gofmt + go test
```

The Go sources are layered as `internal/api` (HTTP/WebSocket) →
`internal/session` (session lifecycle) → `internal/terminal` (PTY +
binary protocol). See [docs/feat-architecture.md](docs/feat-architecture.md) for the
full design.

# License

The MIT License (MIT)