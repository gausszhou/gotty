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

页面会创建一个运行该命令的会话并附着到它。会话 ID 会写入地址栏
（`?id=xxx`），刷新页面即可重连到同一个仍在运行中的会话；若会话已被
销毁（例如空闲超时），页面会自动创建一个新会话。

## 常用选项

| 参数 | 说明 |
| --- | --- |
| `-p, --port` | 监听端口（默认 `8080`） |
| `-a, --address` | 监听地址（默认 `0.0.0.0`） |
| `-w, --permit-write` | 允许浏览器向终端写输入（默认只读） |
| `--reconnect` | 启用客户端断线重连（`--reconnect-time` 控制间隔） |
| `--max-session` | 最大并发会话数（默认 0 = 不限） |
| `--timeout` | 空闲会话销毁超时秒数（0 = 禁用） |
| `--width, --height` | 固定终端尺寸（0 = 跟随浏览器窗口） |
| `--ws-origin` | WebSocket 来源校验正则 |
| `--title-format` | 页面标题格式，支持模板变量（见下） |
| `--term` | 终端类型（默认 `xterm`） |
| `--close-signal` | 关闭会话时发送给进程的信号（默认 SIGHUP） |
| `--log-file` | 服务端日志落盘路径（默认 `~/.gotty/logs/gotty.log`，追加；空值仅控制台） |
| `--close-timeout` | 发信号后强制 kill 的等待秒数（-1 = 禁用） |

例如，启用可写会话：

```bash
gotty -w /bin/bash
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
POST   /api/sessions               创建会话（command 为空时使用 CLI 命令）
GET    /api/sessions               列出所有会话
GET    /api/sessions/:id           查看会话详情
DELETE /api/sessions/:id           销毁会话
POST   /api/sessions/:id/resize    调整终端尺寸
POST   /api/sessions/:id/signal    发送信号（SIGINT/SIGTERM/SIGKILL/SIGHUP/SIGQUIT）
```

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