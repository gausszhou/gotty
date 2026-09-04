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

一键安装(Linux/macOS,amd64/arm64,不需要 sudo,默认装到 `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/gausszhou/gotty/master/scripts/install.sh | sh
# 可选参数:sh install.sh --version v0.0.2 --prefix ~/.local --repo owner/gotty
```

升级到最新版(下载 → 校验 `sha256sums.txt` → 原子替换当前二进制;替换后
需重启服务生效):

```sh
gotty self update          # 或 --yes / --dry-run / --check
```

需要自定义构建或跑未发布代码时再从源码构建(需要 Go 1.26+、Node.js 18+
与 pnpm):

```sh
make install   # 前端依赖
make build     # 前端 + 内嵌静态资源 + ./build/gotty
make release   # 5 平台矩阵(原始二进制 + tar.gz/zip 压缩包)+ sha256sums.txt 到 ./build/
```

发布资产命名为 `gotty-{os}-{arch}[.exe]`,覆盖
`linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64`;每个二进制
额外附一个压缩包(`gotty-{os}-{arch}.tar.gz`,Windows 为 `.zip`),针对
GitHub 慢网络缩小下载体积——`install.sh` 下载压缩包并在本地解压,老版本
发布没有压缩包时自动回退下载原始二进制。全部资产配套 `sha256sums.txt`;
`gotty version --json` 输出
name/version/commit/go_version/os/arch 便于排查。版本号由 git tag 经
`git describe --tags` 派生(单一来源)。

# 使用

## `gotty serve` — Web 终端

```sh
gotty serve top
```

打开 `http://localhost:9049`,点击「创建终端会话」卡片(或页签栏 **＋**
按钮)即可创建运行该命令的会话并附着。不带命令启动时,默认会话命令为
登录 shell(`$SHELL`)。

会话 id 由**客户端生成**(16 位 base36)并保存在设备本地的清单
(`localStorage`)里;服务端只按 id 保存记录,**不提供全局会话列表**。
刷新页面**不会自动创建会话**——只重开最近的存活会话,否则停在创建
卡片。用已知 id 创建是幂等的,有记录的 id 会**复活**(记录的命令,
`run_count+1`);同 id 重新附着会抢占旧客户端(WS 1013)。
顶部页签栏支持**拖拽排序**(顺序按设备持久化在 `localStorage`),
新会话自动追加在末尾。

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
计划在后续里程碑实现——见 [docs/design/capture-design.md](docs/design/capture-design.md)。

# 选项

```
-a, --address string        IP address to listen (default: "0.0.0.0") [$GOTTY_ADDRESS]
-p, --port string           Port number to listen (default: "9049") [$GOTTY_PORT]
-w, --permit-write          Permit clients to write to the TTY (default: true — BE CAREFUL) [$GOTTY_PERMIT_WRITE]
    --title-format string   Title format of browser window (default: "GoTTY - {{ .command }}@{{ .hostname }}") [$GOTTY_TITLE_FORMAT]
    --reconnect             Enable reconnection [$GOTTY_RECONNECT]
    --reconnect-time int    Time to reconnect (default: 10) [$GOTTY_RECONNECT_TIME]
    --max-session int       Maximum number of concurrent sessions (default: 0 = unlimited) [$GOTTY_MAX_SESSION]
    --mirror                Keep a screen mirror per session for the agent API (screen/wait; default: true) [$GOTTY_MIRROR]
    --timeout int           Idle timeout seconds for destroying unattached sessions (default: 900, 0 = disabled) [$GOTTY_TIMEOUT]
    --session-file string   File path to persist session records (default: "~/.gotty/sessions.json", empty = disabled) [$GOTTY_SESSION_FILE]
    --title-file string     File path to persist the page title (default: "~/.gotty/title.json", empty = memory only) [$GOTTY_TITLE_FILE]
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

# 部署

`build/gotty` 是自包含的单二进制:前端通过 `go:embed` 内嵌,交付物只有
可执行文件本身(不需要 Node、不需要静态资源目录)。

## 用 systemd(用户级)服务运行

崩溃自愈 + 开机自启,免 root。把下面这个 unit 存到
`~/.config/systemd/user/gotty.service`:

```ini
[Unit]
Description=GoTTY web terminal server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/path/to/build/gotty serve --address 127.0.0.1 --port 9049 --log-file ~/.gotty/logs/gotty.log --session-file ~/.gotty/sessions.json
WorkingDirectory=/path/to
Restart=on-failure
RestartSec=5
Environment=HOME=/home/you   # 上面路径里的 ~ 由 GoTTY 自己展开

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now gotty.service   # 立即启动 + 开机自启
loginctl enable-linger $USER                  # 注销后继续运行
```

日常管理:`systemctl --user start gotty` 启动、`systemctl --user status
gotty` 查看状态、`journalctl --user -u gotty.service -f` 跟踪日志、
升级二进制后 `systemctl --user restart gotty`。

> 会话状态在进程内存里。服务重启后页面会把失效清单条目移出并停在创建
> 卡片;有记录的会话(id、命令)仍可通过再次创建同 id 复活
> (`run_count+1`)。

把 GoTTY 绑到 `127.0.0.1` 意味着只有本机进程能访问——对外访问请在前面
加反向代理。不想要守护的临时做法是
`nohup ./build/gotty serve ... >/dev/null 2>&1 &`。

## 反向代理 + TLS(nginx)

GoTTY 会升级为 WebSocket(`WS /ws?session_id=...`),代理必须转发
`Upgrade`/`Connection` 头。站点配置示例:

```nginx
server {
    listen 443 ssl;
    server_name tty.example.com;

    ssl_certificate     /etc/letsencrypt/live/tty.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/tty.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:9049;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 86400;            # 长连接 WebSocket
    }
}
```

```sh
sudo certbot --nginx -d tty.example.com     # 签发 + 自动续期 TLS
```

不想用代理也可以让 GoTTY 自己终结 TLS:`gotty serve -t`(默认证书
`~/.gotty.crt` / `~/.gotty.key`,可用 `--tls-crt` / `--tls-key` 覆盖)。

## 加固清单

- 只读暴露用 `--permit-write=false`(默认是 **true** —— 能打开页面的人
  就能往你的会话里键入)。
- 除非 TLS 由 GoTTY 自己终结,否则绑 `127.0.0.1`;绝不把未加密且可写的
  实例暴露在 `0.0.0.0` 上。
- `--ws-origin '^https://tty\.example\.com$'` 拒绝其他来源的跨站
  WebSocket 连接。
- 给 `serve` 一个固定命令来限制会话可运行的命令(如
  `gotty serve tmux new -A -s gotty`),见[多客户端共用一个终端](#多客户端共用一个终端)。
- 所有部署都上 TLS:走代理(上文)或 `-t`。完整选项参考见
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
GET    /api/sessions/:id/screen    read the rendered screen: ?format=text (default) | json | png
POST   /api/sessions/:id/wait      block until the screen matches: {"regex": "...", "timeout_ms": 30000, "quiet_ms": 0}
POST   /api/sessions/:id/keys      inject input bytes into the PTY: {"input": "ls -la\r", "encoding": "text" | "base64"}
GET    /api/title                  deployment page title (browser tab; "" = unset)
PUT    /api/title                  set the page title {"title": "..."}
```

`POST /api/sessions` 接受可选的客户端 id(16 位 base36):存活的 id 返回
现有会话(`200` 幂等),有记录的 id **复活**(记录的 command/args,
`run_count+1`),未知/新 id(或不带)则新建会话。没有会话列表端点——
清单在客户端。

### Agent 驱动

`screen` / `wait` / `keys` 让 AI agent(或任意脚本)无头驱动运行中的会话,
对标 `tu`:读屏、等待正则/输出静默、注入输入,全程不需要浏览器。它们由
**会话屏幕镜像**支撑——PTY 输出 tee 进一个 VT 仿真器(默认开启;`--mirror=false`
关闭后 `screen`/`wait` 返回 `503`)。无浏览器客户端附着时镜像还会应答终端
查询(DA/DSR/DECRQM),因此 vim 等全屏程序无头启动不会挂起:

```sh
curl -X POST localhost:9049/api/sessions -d '{"command": "vim", "args": ["-u", "NONE"]}'
curl -X POST localhost:9049/api/sessions/<id>/keys -d '{"input": ":q!\r"}'
curl -X POST localhost:9049/api/sessions/<id>/wait -d '{"regex": "VIM", "timeout_ms": 5000}'
curl "localhost:9049/api/sessions/<id>/screen?format=text"
```

`keys` 遵循 `--permit-write`:只读部署下注入输入返回 `403`。

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
`internal/capture`。详见 [docs/design/feat-architecture.md](docs/design/feat-architecture.md)
与[中文使用指南](apps/docs/guide/usage.md)。

# License

The MIT License (MIT)