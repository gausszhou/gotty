# AGENTS.md

Guidance for AI agents working in this repository (and for agents driving a
running GoTTY server through the CLI).

## What this repo is

GoTTY shares a terminal over HTTP/WebSocket. A `gotty serve` process owns
sessions; each session is a PTY plus an in-process VT emulator ("screen
mirror") that renders the screen without a browser. The mirror is what makes
the agent API possible: read the screen, wait for it to change, write input.

## Driving a running server

```
gotty serve --permit-write          # start it (default :9049)
gotty usage                         # <1000-token cheat sheet — read this first
```

Quick loop:

```sh
ID=<session id>
gotty screen --session "$ID"                    # read
gotty press Escape : w q Enter --session "$ID"  # named keys
gotty wait --regex 'Total: [0-9]+' --session "$ID"
gotty mouse click --on-text OK --session "$ID"  # click by label, not coords
gotty paste "$(cat patch.diff)" --session "$ID" # ONE paste
gotty monitor --fps 10 --session "$ID"          # read-only live diff stream
```

Rules of thumb:

- **Multi-line text: `paste`, never `type`.** A newline in typed text makes a
  shell execute each line.
- **Target the mouse by label** (`--on-text` / `--on-regex`) instead of
  coordinates — it survives layout changes.
- **`--session` is required.** The server keeps no session list (the client's
  localStorage manifest is the source of truth), so there is no way to
  discover or default to a session.
- **Machine-readable output**: stdout that is not a TTY (or `--json`) emits
  one line of JSON per result. Pipe into `jq`.
- `wait` times out (5s by default) rather than hanging; check `timed_out`.

## Working on the code

```sh
go build ./...        # must pass
go test ./...         # must pass
gofmt -l -w internal cmd
make                  # build the frontend + the binary
```

Layout:

| Path | Role |
|---|---|
| `cmd/` | CLI (`serve`, `capture`, agent subcommands, `usage`) |
| `internal/terminal/` | PTY process + wire protocol frames |
| `internal/session/` | sessions, attach/preempt, ring replay, mirror plumbing |
| `internal/capture/` | VT emulator wrapper, rendering (text/HTML/PNG), mouse, locate |
| `internal/keys/` | key name → bytes |
| `internal/api/` | HTTP + WebSocket server, REST handlers |
| `docs/feat/` | feature specs (0001–0007) with implementation status |
| `docs/design/` | design notes |

Conventions worth knowing:

- **`internal/session` must not import `internal/capture`.** The capture
  browser engine depends on session, so a session → capture edge would be an
  import cycle. Mirror capabilities are declared as optional interfaces in
  `internal/session/agent.go` and adapted in `internal/api/mirror.go`.
- Extra mirror capabilities (mouse, scrollback) are discovered by **interface
  probing**, not by concrete types — a mirror without them simply answers 503.
- Feature work lands with its spec: update the status block in the
  `docs/feat/*.md` file you are implementing.
- Comments explain *why*; they are written in English, and Chinese comments
  exist in older files — match the surrounding file.
