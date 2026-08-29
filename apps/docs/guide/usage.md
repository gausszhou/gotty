# 安装与使用

## 获取二进制

下载适用于你平台的预编译二进制并赋予执行权限即可，无需安装任何运行时：

```bash
# Linux (x86_64) 示例
wget https://github.com/gausszhou/gotty/releases/download/v2.0.0/gotty_linux_amd64.tar.gz
tar -xzf gotty_linux_amd64.tar.gz
chmod +x gotty
```

或者从源码构建（需要 Go 1.26+、Node.js 18+ 与 pnpm）：

```bash
make install   # 安装前端依赖
make all       # 构建前端 + 拷贝静态资源 + 编译 gotty 二进制
```

## 启动终端

最简单的方式是直接运行一个 shell：

```bash
gotty serve /bin/bash
```

然后在浏览器中打开 `http://localhost:8080` 即可看到终端。

运行其他命令同样简单，例如 `htop`、`top`、`vim` 等：

```bash
gotty serve --port 8080 htop
```

不跟命令启动则回退到登录 shell（`$SHELL`，未设置时用 `/bin/sh`）
作为默认命令，页面打开即可用：

```bash
gotty serve --port 8080            # 默认会话命令 = $SHELL
curl -X POST localhost:8080/api/sessions -d '{"command": "top"}'   # 显式命令优先
```

页面打开后点击「创建终端会话」卡片(或页签栏 ＋ 按钮)即创建一个
运行该命令的会话并附着到它。**刷新页面不会自动创建会话**:只重连
清单中最新的存活会话;若没有存活会话则停在空态卡片,由你手动创建。

## 常用选项

| 参数 | 说明 |
| --- | --- |
| `-p, --port` | 监听端口（默认 `8080`） |
| `-a, --address` | 监听地址（默认 `0.0.0.0`） |
| `-w, --permit-write` | 允许浏览器向终端写输入（**默认 true**，谨慎） |
| `--reconnect` | 启用客户端断线重连（`--reconnect-time` 控制间隔, 默认 10） |
| `--max-session` | 最大并发会话数（默认 0 = 不限） |
| `--timeout` | 空闲会话销毁超时秒数（0 = 禁用） |
| `--session-file` | 会话记录持久化文件（默认 `~/.gotty/sessions.json`，空 = 关闭） |
| `--width, --height` | 固定终端尺寸（0 = 跟随浏览器窗口） |
| `--ws-origin` | WebSocket 来源校验正则 |
| `--title-format` | 页面标题格式，支持模板变量（见下） |
| `--term` | 会话 PTY 的 TERM 值（默认 `xterm-256color`） |
| `--close-signal` | 关闭会话时发送给进程的信号（默认 SIGHUP） |
| `--log-file` | 服务端日志落盘路径（默认 `~/.gotty/logs/gotty.log`，追加；空值仅控制台） |
| `--close-timeout` | 发信号后强制 kill 的等待秒数（-1 = 禁用） |

默认即可写（`--permit-write` 默认为 true）；只想让浏览器只看不写，
显式关闭：

```bash
gotty serve --permit-write=false /bin/bash
```

标题格式支持命令名与主机名模板变量，默认如下：

```text
GoTTY - {{ .command }}@{{ .hostname }}
```

## 配置文件

默认读取 `~/.gotty/config.json`，可用 `--config` 指定（也支持 `gotty --config <path> serve`
这种根命令写法，或 `GOTTY_CONFIG` 环境变量）。JSON 格式，等价于命令行
参数；命令行参数优先于配置文件，配置文件优先于 `GOTTY_*` 环境变量。
未知的键会被忽略，旧版本写的配置文件可以继续使用：

```json
{
    "port": "8080",
    "permit_write": false,
    "timeout": 0,
    "max_session": 0
}
```

## REST API 与 WebSocket

多会话能力通过 REST API 暴露：

```text
POST   /api/sessions               创建会话（可携带客户端 id → 幂等/复活）
POST   /api/sessions/status        批量查询清单 id 的存活状态 {"ids":[...]}
GET    /api/sessions/:id           查看会话详情
PUT    /api/sessions/:id/title     设置显示名（持久化到记录）
DELETE /api/sessions/:id           销毁会话
POST   /api/sessions/:id/resize    调整终端尺寸
POST   /api/sessions/:id/signal    发送信号（SIGINT/SIGTERM/SIGKILL/SIGHUP/SIGQUIT）
```

服务端**不提供会话列表**端点：清单在客户端 localStorage，服务端只按
id 记录（存活幂等、有记录则复活——用记录的 command/args 重建,`run_count+1`）。

```bash
curl -X POST localhost:8080/api/sessions -d '{"command": "top", "width": 120, "height": 40}'
```

浏览器通过 `WS /ws?session_id=xxx`（子协议 `webtty`）附着到会话，二进制
帧首字节为消息类型：`0x31` 输入/输出、`0x32` Ping/Pong、`0x33` 调整尺寸/
设置标题、`0x34` 偏好、`0x35` 重连参数。

## 开发模式

前端为 Vite + Vue 3，文档站为 VitePress，二者由根目录的 pnpm workspace 统一管理：

```bash
pnpm install          # 安装全部 workspace 依赖
pnpm dev:web          # 启动前端开发服务器（带 HMR）
pnpm dev:docs         # 启动文档站开发服务器（带 HMR）
pnpm build            # 构建前端 + 文档站
```

## Gotty capture — 端到端终端测试

> 状态：M1 已实现（native 文本）；完整方案见 `docs/capture-design.md`。

`gotty capture` 像 Playwright 驱动浏览器一样驱动一个终端：在固定尺寸的
PTY 里执行指定命令，等渲染稳定后取走结果——纯文本、带样式的 JSON
单元格或 HTML：

```bash
gotty capture --format text -- ls -la
gotty capture --format json -- chafa --format symbols logo.png   # cells[] 带样式
gotty capture --format html --out screen.html -- sh -c 'printf "\033[31mRED\033[0m"'
```

停止条件（先到先得）：

| 条件 | 触发 |
| --- | --- |
| 进程退出 | 命令结束后立即快照（`exit_code`） |
| `--wait-ms`（默认 500） | 输出静默超过该时长（适合 htop 等不退出程序） |
| `--marker` | 输出流出现指定字符串 |
| `--timeout`（默认 30s） | 兜底，返回当前画面并标 `timed_out: true` |

- 尺寸：`--cols/--rows`，默认 120×30；命令前加 `--`，shell 语法用 `sh -c "..."`；
- 渲染引擎为内置 VT 仿真器：SGR（16/256/24-bit）、光标/滚动/擦除、
  备用屏、CJK 宽字符；
- 图形协议图片（kitty/sixel/iTerm2）与像素级浏览器渲染在后续里程碑。

## 复制与粘贴

- 选择文本：鼠标双击选词、三击选行；
- 复制：`Ctrl+Shift+C`（Linux/Windows）或 `Cmd+C`（macOS）；有选区时
  纯 `Ctrl+C` 也是复制，无选区则照常发送 SIGINT；
- 粘贴：`Ctrl+Shift+V` / `Ctrl+V` / `Cmd+V` / 终端内右键（键盘粘贴走
  浏览器原生 paste 事件，局域网 http 同样可用）；
- OSC 52：终端内程序（vim `+clipboard`、tmux `set-clipboard on`、SSH
  会话）经 OSC 52 读写浏览器系统剪贴板；
- 复制走 Clipboard API（HTTPS/localhost），不可用时回退 `execCommand`。

## 主题与终端颜色

亮/暗主题纯前端切换（CSS 变量 + xterm 配色，选择会被记住）。会话注入
`COLORTERM=truecolor`（neovim/lazygit/fzf 等开启 24-bit 真彩），vim 等
启动时的 `OSC 10/11 ; ?` 颜色查询由 xterm.js 按当前主题应答。主题不会
在运行时推送给已启动的进程。

## 安全

- `--permit-write` **默认 true**：能打开页面的客户端即可向会话键入；
  只读部署请 `--permit-write=false`，并用 TLS/反向代理保护端口。
- 默认流量不加密，机密环境务必 `-t` 开启 TLS（证书 `~/.gotty.crt` /
  `~/.gotty.key`，可用 `--tls-crt`/`--tls-key` 覆盖）。

## 后台运行

仅想跨终端关闭存活：

```bash
nohup ./build/gotty serve --log-file ~/.gotty/logs/gotty.log >/dev/null 2>&1 &
disown
```

崩溃自愈 + 开机自启用 systemd（用户级，免 root）。放入
`~/.config/systemd/user/gotty.service`：

```ini
[Unit]
Description=GoTTY web terminal server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/path/to/build/gotty serve
WorkingDirectory=/path/to
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now gotty.service   # 开机自启并立即启动
loginctl enable-linger $USER                  # 注销后继续运行
```

> 服务重启后内存中的会话丢失（状态在进程内）；页面在下次状态轮询时把
> 失效会话移出清单并停在创建卡片，已知 id 仍可通过 `POST /api/sessions`
> 复活（记录命令，`run_count+1`）。

## 多客户端共用一个终端

一个会话同一时刻只允许一个客户端附着：同 id 再附着会抢占旧连接
（WS 1013），不同 id 互不抢占——多设备各用各的 id 即互不干扰；刷新
页面（同 id）即恢复同一会话。想要多人**同时**观看，用终端复用器：

```bash
gotty tmux new -A -s gotty top
```

所有观看者看到同一画面；默认 `--permit-write` 下任何人都可以键入，
如果只希望观看，服务端加 `--permit-write=false`，操作改在本地终端
进行：`tmux new -A -s gotty`。