# GoTTY - Web 终端,端到端驱动

[English](README.md) | **简体中文**

GoTTY 是一个命令行工具,把你的 CLI 工具跑在**浏览器托管的终端**里:会话
通过 REST API 创建、经 WebSocket 附着,在页面里以页签管理;`gotty capture`
则把同一条 PTY 管线变成**端到端测试命令**——无头执行任意命令,取走渲染
结果(纯文本、带样式的 JSON 单元格或 HTML),Playwright 风格。

![Screenshot](screenshot.gif)

# 特性

- **多会话** — 通过 REST API 创建/附着/分离/销毁终端会话。
- **终端捕获** — `gotty capture` 执行任意命令并返回渲染结果(text / styled JSON cells / HTML);无需浏览器、无需运行中的服务。
- **断线重连** — 客户端断开后进程继续运行;刷新页面(同 id)即恢复同一会话。
- **现代化前端** — Vite + Vue 3 + xterm.js WebGL 渲染,界面语言跟随浏览器(中文/English)并可手动切换。

# 安装

从 [Releases](https://github.com/gausszhou/gotty/releases) 页面下载最新二进制,
或从源码构建(需要 Go 1.26+、Node.js 18+ 与 pnpm):

```sh
make install   # 前端依赖
make build     # 前端 + 内嵌静态资源 + ./build/gotty
```

# 使用

## `gotty serve` — Web 终端

```sh
gotty serve --port 8080 top
```

打开 `http://localhost:8080`,点击「创建终端会话」卡片(或页签栏 **＋**
按钮)即可创建运行该命令的会话并附着。不带命令启动时,默认会话命令为
登录 shell(`$SHELL`)。

会话 id 由**客户端生成**(16 位 base36)并保存在设备本地的清单
(`localStorage`)里;服务端只按 id 保存记录,**不提供全局会话列表**。
刷新页面**不会自动创建会话**——只重开最近的存活会话,否则停在创建
卡片。用已知 id 创建是幂等的,有记录的 id 会**复活**(记录的命令,
`run_count+1`);同 id 重新附着会抢占旧客户端(WS 1013)。

## `gotty capture` — 端到端终端测试

```sh
gotty capture --format text -- ls -la
gotty capture --format json -- chafa --format symbols logo.png
gotty capture --format html --out screen.html -- 'printf "\033[31mRED\033[0m"'
```

在固定尺寸的 PTY(`--cols/--rows`,默认 120×30)中执行命令,并在以下时机
快照屏幕:进程退出、输出静默超过 `--wait-ms`(默认 500ms)、流中出现
`--marker`、或 `--timeout`(默认 30s;超时返回当前画面并标
`timed_out`)。文本由内置 VT 仿真器渲染:SGR 颜色(16/256/24-bit)、
光标/滚动/擦除、备用屏、CJK 宽字符。命令前加 `--`,shell 语法用
`sh -c "..."`。图形协议图片(kitty/sixel/iTerm2)与像素级浏览器渲染
计划在后续里程碑实现——见 [docs/capture-design.md](docs/capture-design.md)。

# 选项

```
-a, --address string        IP address to listen (default: "0.0.0.0") [$GOTTY_ADDRESS]
-p, --port string           Port number to listen (default: "8080") [$GOTTY_PORT]
-w, --permit-write          Permit clients to write to the TTY (default: true — BE CAREFUL) [$GOTTY_PERMIT_WRITE]
    --title-format string   Title format of browser window (default: "GoTTY - {{ .command }}@{{ .hostname }}") [$GOTTY_TITLE_FORMAT]
    --reconnect             Enable reconnection [$GOTTY_RECONNECT]
    --reconnect-time int    Time to reconnect (default: 10) [$GOTTY_RECONNECT_TIME]
    --max-session int       Maximum number of concurrent sessions (default: 0 = unlimited) [$GOTTY_MAX_SESSION]
    --timeout int           Idle timeout seconds for destroying unattached sessions (default: 900, 0 = disabled) [$GOTTY_TIMEOUT]
    --session-file string   File path to persist session records (default: "~/.gotty/sessions.json", empty = disabled) [$GOTTY_SESSION_FILE]
    --width int             Static width of the screen, 0(default) means dynamically resize [$GOTTY_WIDTH]
    --height int            Static height of the screen, 0(default) means dynamically resize [$GOTTY_HEIGHT]
    --ws-origin string      A regular expression that matches origin URLs to be accepted by WebSocket [$GOTTY_WS_ORIGIN]
    --term string           TERM value used inside session PTYs (default: "xterm-256color") [$GOTTY_TERM]
-t, --tls                   Enable TLS/SSL [$GOTTY_TLS]
    --tls-crt string        TLS/SSL certificate file path (default: "~/.gotty.crt") [$GOTTY_TLS_CRT]
    --tls-key string        TLS/SSL key file path (default: "~/.gotty.key") [$GOTTY_TLS_KEY]
    --log-file string       Server log file path (default: "~/.gotty/logs/gotty.log", empty = console only) [$GOTTY_LOG_FILE]
    --close-signal int      Signal sent to the command process when the session is closed (default: 1 = SIGHUP) [$GOTTY_CLOSE_SIGNAL]
    --close-timeout int     Time in seconds to force kill process after the session is closed (default: 3, -1 = wait forever) [$GOTTY_CLOSE_TIMEOUT]
    --config string         Config file path (default: "~/.gotty/config.json") [$GOTTY_CONFIG]
-v, --version               print the version
```

# 配置与安全

默认读取 `~/.gotty/config.json`(存在时;用 `--config` 覆盖;优先级:
命令行 > 配置文件 > `GOTTY_*` 环境变量)。未知键将被忽略。

> `--permit-write` 默认为 **true**:默认任何能打开页面的人都可以向你的
> 会话键入内容。只读部署请 `--permit-write=false`,并用 TLS(`-t`)和/或
> 反向代理保护端口。未启用 TLS 时流量不加密。

# 后台运行

```sh
nohup ./build/gotty serve --log-file ~/.gotty/logs/gotty.log >/dev/null 2>&1 &
```

崩溃自愈与开机自启请安装 systemd(用户级)服务;完整 unit 与说明见
[指南](apps/docs/guide/usage.md)。

# 多客户端共用一个终端

一个会话 = 一个附着客户端;同 **id** 再附着会抢占旧连接,不同 id 互不
抢占。想多人**同时**观看同一终端,用终端复用器:

```sh
gotty tmux new -A -s gotty top     # 观看者看到同一画面(默认可键入)
```

# REST API

```
POST   /api/sessions               create a session (empty command uses the default command)
POST   /api/sessions/status        query liveness of client manifest ids {"ids": [...]}
GET    /api/sessions/:id           session detail
PUT    /api/sessions/:id/title     rename a session (persisted in the record)
DELETE /api/sessions/:id           destroy a session
POST   /api/sessions/:id/resize    resize the terminal {width, height}
POST   /api/sessions/:id/signal    send a signal {signal: "SIGINT" | "SIGHUP" | "SIGTERM" | "SIGKILL" | "SIGQUIT"}
```

`POST /api/sessions` 接受可选的客户端 id(16 位 base36):存活的 id 返回
现有会话(`200` 幂等),有记录的 id **复活**(记录的 command/args,
`run_count+1`),未知/新 id(或不带)则新建会话。没有会话列表端点——
清单在客户端。

# 开发

```sh
make install   # pnpm install for the pnpm workspace
make build     # frontend (Vite) -> static (go:embed) -> ./build/gotty
make all       # frontend + static + cross-platform release (linux/amd64 + arm64)
make docs      # VitePress documentation site (apps/docs)
make test      # go vet + gofmt + go test
```

代码分层:`internal/api`(HTTP/WebSocket)→ `internal/session`(生命周期)
→ `internal/terminal`(PTY + 二进制协议);capture 引擎在
`internal/capture`。详见 [docs/feat-architecture.md](docs/feat-architecture.md)
与[中文使用指南](apps/docs/guide/usage.md)。

# License

The MIT License (MIT)