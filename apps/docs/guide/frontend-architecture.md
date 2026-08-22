# 前端架构

GoTTY 的前端应用位于 `apps/web`，采用 Vite + Vue 3 + TypeScript 构建，
终端模拟能力来自 [xterm.js](https://xtermjs.org/) v6 及其官方插件。

## 技术栈

- **构建工具**：Vite 6，产物为单文件 `gotty-bundle.js`。
- **框架**：Vue 3（Composition API）。
- **终端**：`@xterm/xterm` v6，配合以下插件：
  - `@xterm/addon-fit`：自适应容器尺寸；
  - `@xterm/addon-webgl`：启用 WebGL 渲染加速；
  - `@xterm/addon-web-links`：终端内链接可点击。
- **样式注入**：`vite-plugin-css-injected-by-js`，把 CSS 内联进 JS bundle，
  便于 Go 侧通过 `go:embed` 单文件分发。

## 目录结构

```text
apps/web
├── index.html
├── vite.config.ts
├── tsconfig.json
├── static            # 交由 Go 直接 embed 的静态资源
│   ├── index.html
│   ├── favicon.png
│   └── css/
└── src
    ├── main.ts       # 入口
    ├── App.vue       # 会话驱动流程：创建/重连会话 + WebTTY 连接
    ├── components
    │   └── Terminal.vue   # xterm.js 终端组件
    └── utils
        ├── webtty.ts      # WebTTY 协议编解码 + 断线重连
        └── websocket.ts   # WebSocket 连接管理
```

## 会话驱动流程

打开页面时 `App.vue` 按以下流程建立连接：

1. 读取 URL 中的 `?id=xxx`；若该会话仍存活（`GET /api/sessions/:id`），
   直接复用；
2. 否则通过 `POST /api/sessions` 创建一个新会话（`command` 传空串，
   服务端使用 CLI 启动时给定的默认命令），并把会话 ID 写回 URL；
3. 以 `WS /ws?session_id=<id>`（子协议 `webtty`）附着到会话；
4. 连接断开且服务端允许重连时，先通过 `resolveSession()` 确认会话是否
   仍然存活，若已销毁（如空闲超时），自动创建新会话再重连。

## WebTTY 协议

浏览器与 Go 服务端通过 WebSocket 通信，消息格式为二进制帧：

```text
[消息类型(1 byte)][负载(可选)]
```

- 客户端 → 服务端：`0x31` 输入、`0x32` Ping、`0x33` 尺寸调整（JSON）；
- 服务端 → 客户端：`0x31` 输出（原始字节）、`0x32` Pong、`0x33` 标题、
  `0x34` 偏好（JSON）、`0x35` 重连参数（JSON）。

连接建立后的第一帧必须是二进制 JSON 握手消息
`{"Arguments":"","AuthToken":""}`，用于认证。

## 构建流程

```bash
# 在仓库根目录执行（pnpm workspace）
pnpm --filter gotty-frontend build
```

产物输出到 `apps/web/dist/gotty-bundle.js`。随后 `make static` 会把
`apps/web/static` 与构建产物拷贝到 `internal/api/static`，由 Go 的
`go:embed` 在编译期打包进二进制。