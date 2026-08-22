# GoTTY 现代化重构设计文档

## 1. 目标

将 gotty 从单连接终端分享工具，重构为支持多终端会话管理的服务。

### 1.1 后端现代化

- Go 1.26（go.mod 替代 Godeps/vendor）
- `//go:embed` 替代 go-bindata
- 移除过时依赖，引入现代替代

### 1.2 前端现代化

- Vite + Vue 3 + TypeScript
- xterm.js v6 + WebGL 渲染 + FitAddon
- 二进制 WebSocket 协议（无 base64）

### 1.3 架构重构

- 支持多终端会话
- 会话生命周期管理（创建/附着/分离/销毁）
- 客户端断线后 PTY 存活，支持重连
- REST API 管理会话

---

## 2. 目录结构

```
gotty/
├── cmd/                         # CLI 子命令
│   ├── root.go                  #   根命令（--version、--config 持久 flag）
│   └── serve.go                 #   serve 子命令：启动服务
│
├── apps/
│   └── web/                     # 前端 (Vite + Vue 3 + xterm.js v6)
│       ├── index.html           # Vite dev 入口
│       ├── vite.config.ts
│       ├── package.json
│       ├── src/
│       │   ├── main.ts          # createApp → mount #app
│       │   ├── App.vue          # 根组件，组装 WebTTY 连接
│       │   ├── components/
│       │   │   └── Terminal.vue # xterm.js + WebGL + FitAddon
│       │   └── utils/
│       │       ├── webtty.ts    # 二进制协议
│       │       └── websocket.ts # WS BinaryMessage 收发
│       └── static/              # Go 模板 + CSS + 图标
│           ├── index.html       # Go template ({{ .title }})
│           ├── favicon.png
│           └── css/
│               ├── index.css
│               └── xterm_customize.css
│
├── internal/
│   ├── api/                     # 接入层
│   │   ├── router.go            #   路由定义
│   │   ├── session_handler.go   #   REST: 会话 CRUD
│   │   ├── ws_handler.go        #   WebSocket: 附着到会话
│   │   ├── middleware.go        #   Auth / CORS / 日志
│   │   ├── embed.go             #   //go:embed all:static
│   │   └── static/              #   前端构建产物
│   │
│   ├── session/                 # 业务层
│   │   ├── session.go           #   Session 实体
│   │   ├── manager.go           #   注册表 + 生命周期
│   │   └── store.go             #   持久化接口
│   │
│   ├── terminal/                # 能力层（webtty + backend 合并）
│   │   ├── terminal.go          #   Terminal: PTY + 协议桥接
│   │   ├── protocol.go          #   二进制协议编解码
│   │   ├── options.go           #   配置选项
│   │   └── errors.go            #   错误定义
│   │
│   ├── config/                  # 配置解析
│   │   ├── flags.go             #   CLI 标志 (cobra)
│   │   └── default.go           #   默认值
│   │
│   └── utils/                   # 工具函数
│       ├── path.go              #   Expand("~/")
│       └── rand.go              #   RandomString()
│
├── main.go                      # 入口（仅调用 cmd.Execute）
├── go.mod
├── Makefile
└── docs/
    └── architecture.md          # 本文档
```

> 实施注记：`gotty serve [flags] [command [args...]]` 启动服务；不带命令则
> 进入网关模式，会话命令由 REST API 显式指定。`--config` 为根命令持久
> flag，`gotty --config x serve` 与 `gotty serve --config x` 均可。

---

## 3. 分层架构

### 3.1 层次关系

```
┌─────────────────────────────────────────────────────┐
│                    HTTP / WebSocket                   │
└──────────────────────┬──────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────┐
│  api/                    接入层                      │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────┐  │
│  │ REST Router │  │ WS Handler   │  │ Middleware  │  │
│  └──────┬──────┘  └──────┬───────┘  └────────────┘  │
│         │                │                            │
└─────────┼────────────────┼───────────────────────────┘
          │                │
┌─────────▼────────────────▼───────────────────────────┐
│  session/                业务层                      │
│  ┌──────────────────────────────────────────────┐    │
│  │ Manager                                      │    │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐     │    │
│  │  │Session#1 │ │Session#2 │ │Session#3 │ ... │    │
│  │  └────┬─────┘ └────┬─────┘ └────┬─────┘     │    │
│  │       │ owns        │ owns        │ owns      │    │
│  └───────┼─────────────┼─────────────┼───────────┘    │
└──────────┼─────────────┼─────────────┼────────────────┘
           │             │             │
┌──────────▼─────────────▼─────────────▼────────────────┐
│  terminal/              能力层                        │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐            │
│  │Terminal#1│  │Terminal#2│  │Terminal#3│  ...       │
│  │ ┌──────┐ │  │ ┌──────┐ │  │ ┌──────┐ │           │
│  │ │PTY   │ │  │ │PTY   │ │  │ │PTY   │ │           │
│  │ │Process│ │  │ │Process│ │  │ │Process│ │           │
│  │ └──────┘ │  │ └──────┘ │  │ └──────┘ │           │
│  └──────────┘  └──────────┘  └──────────┘            │
└──────────────────────────────────────────────────────┘
```

### 3.2 职责划分

| 层 | 包 | 职责 | 对外接口 |
|---|---|---|---|
| 接入层 | `api/` | HTTP 路由、协议转换、中间件 | REST API + WebSocket |
| 业务层 | `session/` | 会话注册表、生命周期管理 | Create/Get/List/Attach/Detach/Destroy |
| 能力层 | `terminal/` | PTY 进程管理、二进制协议桥接 | New/Read/Write/Resize/Close |
| 基础层 | `config/` | 配置解析、CLI 标志 | GenerateFlags/ApplyConfigFile |
| 工具层 | `utils/` | 通用小函数 | Expand/RandomString |

### 3.3 设计原则

- **api/ 不碰 PTY**：只做 HTTP/WS 协议转换
- **session/ 不碰 HTTP**：纯业务逻辑
- **terminal/ 不管生命周期**：只提供 PTY 能力
- **每层单向依赖**：api → session → terminal，不反向

---

## 4. Session 生命周期

```
                    ┌─────────┐
    POST /sessions  │  创建   │  Terminal.New(command)
   ───────────────→ │  IDLE   │  注册到 Manager
                    └────┬────┘
                         │
        WS /ws?sid=xxx   │  Attach(conn)
   ─────────────────────→│  创建协议桥接
                    ┌────▼────┐
                    │ RUNNING │  客户端在线
                    └────┬────┘
                         │
        WS 断开          │  Detach()
   ─────────────────────→│  销毁桥接，PTY 存活
                    ┌────▼────┐
                    │  IDLE   │  PTY 继续运行
                    └────┬────┘
           ┌─────────────┤
           │             │
    WS 重连│    DELETE    │  Destroy()
   ────────│  /sessions/x │  杀进程，移除注册表
           │        ┌────▼────┐
           │        │ 销毁    │
           │        └─────────┘
     回到 RUNNING
```

### 4.1 状态定义

```go
type State int

const (
    StateIdle     State = iota  // PTY 运行中，无客户端连接
    StateRunning                // PTY 运行中，有客户端连接
    StateDestroyed              // 已销毁
)
```

### 4.2 关键行为

- **Attach()**：阻塞，直到客户端断开。断开后 PTY 存活。
- **Detach()**：释放协议桥接，PTY 不受影响。
- **Destroy()**：杀进程，释放资源，从 Manager 移除。

---

## 5. API 设计

### 5.1 REST API

```
会话管理
  POST   /api/sessions              创建会话
  GET    /api/sessions              列出所有会话
  GET    /api/sessions/:id          查看会话详情
  DELETE /api/sessions/:id          销毁会话

会话控制
  POST   /api/sessions/:id/resize   调整终端大小
  POST   /api/sessions/:id/signal   发送信号 (SIGINT, SIGKILL...)
```

### 5.2 WebSocket

```
  WS     /ws?session_id=xxx         附着到已有会话
```

### 5.3 请求/响应示例

```json
// POST /api/sessions
// Request:
{ "command": "bash", "args": [], "width": 80, "height": 24 }

// Response:
{
  "id": "a1b2c3d4",
  "state": "idle",
  "command": "bash",
  "created_at": "2026-01-01T00:00:00Z"
}

// GET /api/sessions
// Response:
{
  "sessions": [
    { "id": "a1b2c3d4", "state": "running", "command": "bash" },
    { "id": "e5f6g7h8", "state": "idle", "command": "top" }
  ]
}
```

---

## 6. 二进制协议

WebSocket 使用 BinaryMessage 帧，终端输出直接传输原始字节（无 base64）。

### 6.1 消息格式

```
[type byte] [payload bytes...]
```

### 6.2 消息类型

```
客户端 → 服务端：
  0x31 ('1') + raw bytes     Input（用户输入）
  0x32 ('2')                 Ping
  0x33 ('3') + JSON bytes    ResizeTerminal

服务端 → 客户端：
  0x31 ('1') + raw bytes     Output（终端输出，原始字节）
  0x32 ('2')                 Pong
  0x33 ('3') + string        SetWindowTitle
  0x34 ('4') + JSON bytes    SetPreferences
  0x35 ('5') + JSON bytes    SetReconnect
```

### 6.3 握手

```
客户端连接 WS（subprotocol: "webtty"）
→ 发送 init JSON: {"Arguments":"", "AuthToken":""}
→ 服务端校验 auth_token
→ 开始双向数据传输
```

---

## 7. 数据流

```
客户端浏览器                    gotty 服务端
    │                              │
    │── WS 连接 ──────────────────→│ api/ws_handler
    │                              │   ↓
    │                              │ session.Attach()
    │                              │   ↓
    │                              │ terminal.bridge(conn)
    │                              │   ↓
    │◄── [0x31] + raw output ─────│ PTY 输出 → 协议编码 → WS
    │── [0x31] + input ──────────→│ WS → 协议解码 → PTY 输入
    │── [0x33] + {cols,rows} ────→│ WS → PTY resize
    │                              │
    │    (客户端断开)              │
    │                              │ session.Detach()
    │                              │   PTY 仍在运行...
    │                              │
    │── WS 重连 ──────────────────→│ session.Attach()
    │                              │   新桥接，同一个 PTY
    │◄── 继续输出 ────────────────│
```

---

## 8. 依赖清单

### 8.1 直接依赖

| 包 | 版本 | 用途 |
|---|---|---|
| `github.com/spf13/cobra` | v1.8+ | CLI 框架（替代 urfave/cli） |
| `github.com/coder/websocket` | v1.8+ | WebSocket（替代 gorilla/websocket） |
| `github.com/NYTimes/gziphandler` | v1.1+ | HTTP gzip 中间件 |
| `github.com/creack/pty` | v1.1+ | PTY 创建（替代 kr/pty） |

### 8.2 前端依赖

| 包 | 版本 | 用途 |
|---|---|---|
| `vue` | ^3.5 | UI 框架 |
| `@xterm/xterm` | ^5.5 | 终端模拟器 |
| `@xterm/addon-fit` | ^0.10 | 终端自适应 |
| `@xterm/addon-webgl` | ^0.18 | WebGL 渲染 |
| `@xterm/addon-web-links` | ^0.11 | 链接识别 |
| `vite` | ^6.0 | 构建工具 |
| `@vitejs/plugin-vue` | ^5.2 | Vue SFC 支持 |
| `vite-plugin-css-injected-by-js` | ^3.5 | CSS 内联到 JS |

### 8.3 移除的依赖

| 包 | 替代 | 移除原因 |
|---|---|---|
| `urfave/cli/v2` | `spf13/cobra` | 子命令支持更好 |
| `gorilla/websocket` | `coder/websocket` | 活跃维护 |
| `kr/pty` | `creack/pty` | 活跃维护 |
| `pkg/errors` | stdlib `fmt.Errorf %w` | 已弃用 |
| `fatih/structs` | 标准库 `reflect` | 减少依赖 |
| `yudai/hcl` | `encoding/json` | 减少依赖 |
| `elazarl/go-bindata-assetfs` | `//go:embed` | Go 原生支持 |
| `go-bindata` | `//go:embed` | Go 原生支持 |
| `hashicorp/go-multierror` | 无需 | 未使用 |

---

## 9. 关键接口定义

```go
// terminal/ — 能力层
type Terminal struct {
    cmd    *exec.Cmd
    pty    *os.File
    opts   Options
}

func New(command string, args []string, opts ...Option) (*Terminal, error)
func (t *Terminal) Read(p []byte) (int, error)
func (t *Terminal) Write(p []byte) (int, error)
func (t *Terminal) Resize(cols, rows int) error
func (t *Terminal) Wait() error
func (t *Terminal) Close() error

// session/ — 业务层
type Manager struct { ... }

func NewManager() *Manager
func (m *Manager) Create(command string, args []string, opts ...terminal.Option) (*Session, error)
func (m *Manager) Get(id string) (*Session, error)
func (m *Manager) List() []*Session
func (m *Manager) Destroy(id string) error

type Session struct { ... }

func (s *Session) ID() string
func (s *Session) State() State
func (s *Session) Attach(conn io.ReadWriter) error   // 阻塞直到客户端断开
func (s *Session) Detach()                            // 释放桥接，PTY 存活
func (s *Session) Destroy() error                     // 杀进程

// api/ — 接入层（薄封装，无业务逻辑）
type Server struct {
    manager *session.Manager
}
```

---

## 10. 构建流程

```
make
  ├── frontend:  cd apps/web && npm ci && npx vite build
  ├── static:    复制资源 + bundle 到 internal/server/static/
  └── build:     go build -ldflags "..." -o gotty .
```

---

## 11. 验证清单

- [x] `go build ./...` 通过
- [x] `go vet ./...` 无警告
- [x] `go test ./...` 全通过
- [x] `npx vite build` 前端构建成功
- [x] `gotty --version` 输出版本号
- [x] HTTP 200 返回终端页面
- [x] WebSocket 二进制协议双向通信
- [x] 多会话：创建 → 附着 → 分离 → 重连 → 销毁
- [x] 客户端断线后 PTY 存活
- [x] REST API 会话列表正确

---

## 12. 实施说明（决策记录）

### 12.1 移除的旧选项

| 旧选项 | 原因 | 替代 |
|---|---|---|
| `--random-url` | 会话 ID 已承担"难猜 URL"职责（`?id=xxx`） | REST API + 会话 ID |
| `--once` | 与多会话服务模式冲突 | `--max-session 1` |
| `--permit-arguments` | 命令与参数改由 REST 创建时指定 | `POST /api/sessions` |
| `--tls-ca-crt` / TLS 客户端证书 | 减少配置面，Basic Auth 已覆盖鉴权场景 | `--credential` |
| `--index` | 自定义页面模板场景罕见 | 直接修改 `apps/web/static/index.html` |

旧配置文件中的这些键会被 `encoding/json` 静默忽略，无需迁移。

### 12.2 语义调整

- **`timeout`**：由"服务器空闲 N 秒后整体关停"改为"空闲会话（无客户端
  附着）超过 N 秒被销毁"，0 表示禁用。由 `session.Manager` 的清扫循环
  实现（`DestroyExpired`，每秒一轮）。
- **`max_connection` → `max-session`**：限制并发存活会话数，0 表示不限。
- **WS 认证**：沿用 webtty 握手协议 —— 首帧二进制 JSON
  `{"Arguments":"","AuthToken":""}`，`AuthToken` 与 `credential` 不一致
  时拒绝连接；HTTP 侧 Basic Auth 不包裹 `/ws`（浏览器 WebSocket 携带
  Authorization 头受限）。
- **单客户端附着**：一个会话同一时刻只允许一个客户端附着（第二个附着
  返回 WS 1013 *Try Again Later*），与状态机 `IDLE/RUNNING` 一致；
  断线后 PTY 存活，可用同一 `?id=` 重连。

### 12.3 测试

- `internal/terminal/protocol_test.go`：二进制协议编解码。
- `internal/session/manager_test.go`：生命周期、MaxSession、空闲超时、
  附着协议全流程（stub terminal 注入，确定性断言）。
- `internal/api/handlers_test.go`：httptest + 真实 PTY 的端到端：
  REST CRUD、WS 握手/认证/繁忙拒绝、`cat` 会话输入回显、断线重连、
  清扫移除退出会话。
