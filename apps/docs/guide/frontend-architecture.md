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
    ├── App.vue       # VSCode 式壳：Activity Bar + 会话边栏 + 分屏工作区
    ├── components
    │   ├── SessionSidebar.vue  # 左侧会话列表（新建/销毁/状态轮询）
    │   ├── SplitView.vue       # 递归分屏树（左右/上下二分 + 拖分隔条）
    │   ├── TerminalPane.vue    # 单个会话查看器（附着/重连/关闭）
    │   └── Terminal.vue        # xterm.js 终端组件
    └── utils
        ├── webtty.ts      # WebTTY 协议编解码 + 断线重连
        ├── websocket.ts   # WebSocket 连接管理
        ├── api.ts         # REST API 封装（会话 CRUD）
        └── split.ts       # 分屏树纯函数（拆分/删除/遍历）
```

## 多终端会话

页面采用 VSCode 式三段布局，支持**单页面多终端会话**：

```text
┌─ Activity Bar ─┬─ 会话列表 ─┬─ 分屏工作区 ─┐
│ ＋ 新建        │ ● bash     │ ┌────────┬─┐ │
│ ⬌ 左右拆分    │ ○ top      │ │ pane A │ │ │
│ ⬍ 上下拆分    │ ● vim      │ ├────────┤ │ │
│ ✕ 关闭        │            │ │ pane B │ │ │
└───────────────┴────────────┴─────────┴─┴─┘
```

- **会话生命周期归左侧列表管理**：列表每 2 秒轮询 `GET /api/sessions`，
  显示状态徽标（灰=idle、绿=running、红=exited）；「＋」新建会话（空
  command 使用服务端默认命令）；hover 项上的 🗑 销毁会话。
- **分屏工作区是查看器**：`SplitView` 是递归二分树（`utils/split.ts`），
  叶子 `TerminalPane` 附着到绑定会话（`WS /ws?session_id=`）。点击列表
  中的会话：已打开则聚焦对应 pane，否则新建 pane 附着。
- **关闭 pane 只分离、不杀进程**（PTY 在服务端继续运行）；点 Activity
  Bar 的 ⬌/⬍ 拆分当前聚焦 pane（新 pane 附着新会话），分隔条可拖动。
- 打开页面时若 URL 带 `?id=xxx` 且会话仍存活，自动打开该会话，兼容
  旧版单会话分享链接。

## 会话驱动流程

`TerminalPane` 挂载后按以下流程建立连接：

1. 通过 `GET /api/sessions/:id` 确认绑定会话仍存活；
2. 以 `WS /ws?session_id=<id>`（子协议 `webtty`）附着；
3. 连接断开且服务端允许重连时，`resolveSession()` 再次确认会话存活；
   返回 `null`（会话已销毁）则停止重连并提示，不再自动创建新会话 ——
   新建会话只由左侧边栏发起，保证会话生命周期单一归属。

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