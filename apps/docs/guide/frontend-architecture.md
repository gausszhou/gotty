# 前端架构

GoTTY 的前端应用位于 `apps/web`，采用 Vite + Vue 3 + TypeScript 构建，
终端模拟能力来自 [xterm.js](https://xtermjs.org/) v5（`@xterm/xterm` 5.5.x）及其官方插件。

## 技术栈

- **构建工具**：Vite 6，产物为单文件 `main.js`。
- **框架**：Vue 3（Composition API）。
- **终端**：`@xterm/xterm` v5（当前最新稳定版 5.5），配合以下插件：
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
├── public              # Vite 原样复制到构建产物(dist/)
│   └── favicon.png
└── src
    ├── main.ts       # 入口(引入唯一全局样式 src/style/index.css)
    ├── style
    │   └── index.css # 唯一全局 CSS(html/body 锁滚动、xterm 字体)
    ├── App.vue       # VSCode 式壳:顶部页签栏 + 会话内容区
    ├── components
    │   ├── TabBar.vue         # 顶部页签(活会话 + 新建 + 历史下拉 + 延迟)
    │   ├── TerminalPane.vue   # 会话查看器(附着/断开弹窗/重连)
    │   └── Terminal.vue       # xterm.js 终端组件
    └── utils
        ├── logger.ts     # 统一日志([gotty][tag],debug 需 gotty.debug=1)
        ├── webtty.ts      # WebTTY 协议编解码 + 断线重连
        ├── websocket.ts   # WebSocket 连接管理
        └── api.ts         # REST API 封装(会话 CRUD)
```

> `internal/api/static` 的内容(页面/bundle/favicon)全部由 `make static`
> 从 `dist/` 生成并被 `go:embed` 打包,仓库内不维护构建产物。

## 多会话管理

页面采用 VSCode 式布局：**顶部会话页签 + 下方单会话内容区**。

```text
┌─ ● bash ─┬─ ○ top ──┬───────┬─────┐
│          │          │ ＋ ▾ │     │   ← 顶部页签栏
├─────────────────────────────┬─────┤
│           内容区（当前会话终端）     │
└──────────────────────────────┴─────┘
```

- **会话即页签**：页签栏每 2 秒轮询 `GET /api/sessions`，每个活会话
  一个页签（状态点：绿=running、灰=idle），点击切换；页签 ✕ = 销毁
  会话（历史保留，可从 ▾ 恢复）。
- **页签标题 = 程序标题**：终端程序通过 OSC 0/2 设置标题（如 vim 的
  `vim - file`）时，xterm 解析后驱动页签标题（GNOME-Shell 风格自动
  命名）；未设置时回退到命令名（`/bin/bash` → `bash`）。标题只记忆在
  设备本地清单中，刷新后保留；**不提供手动重命名**。
- **＋ 新建**：直接创建默认会话（空 command 用服务端默认命令，
  如 `$SHELL`），新页签自动激活。
- **▾ 历史**：下拉展示服务端持久化的会话历史（`GET
  /api/sessions/history`，`--session-file` 持久化，跨重启保留），点击
  历史项弹出确认框后**重新运行**原命令。
- **会话 id 保存在 sessionStorage**（`gotty.sessionId`，不进入 URL、
  不在界面展示）：刷新页面恢复当前会话；关闭浏览器后重开则回退到
  "最近的活会话 / 空态卡片"（**不再自动创建**，空清单由用户点击创建）。
- **内容区是查看器**：`TerminalPane` 附着到当前页签对应会话
  （`WS /ws?session_id=`）。内容区**无任何修饰**——纯 xterm canvas，
  标题/操作全部在顶部页签；仅异常态（断开/会话销毁）弹出提示框。
- **语言切换**：界面文案跟随浏览器语言（中文/English），页签栏右侧按钮可手动切换并持久化（`gotty.lang`）。
- **延迟显示**：前端每 5 秒发送 Ping（`0x32`），收到 Pong（`0x32`）后
  按 `performance.now()` 差值实测 RTT，展示在**激活页签标题左侧**
  （如 `12ms ● bash`），断开时清零。

## 连接状态与断开弹窗

`TerminalPane` 内部是显式状态机：`connecting → connected`，断开后进入
`disconnected` 或 `gone`，并弹出 VSCode 风格模态框：

| 状态 | 弹窗 | 操作 |
|---|---|---|
| `connecting` | 头部显示 connecting… | — |
| `connected` | 无 | — |
| `disconnected` | 「连接已断开」+ 原因（网络异常/服务器关闭/1013 会话忙等） | 重新连接 / 关闭 |
| `gone` | 「会话已销毁」 | 关闭 |

- 断线原因由关闭码翻译：`1006` 网络异常、`1011` 服务器错误、
  `1013` 会话已被其他客户端附着等。
- 「重新连接」立即重连（取消自动重连定时器）；服务端启用
  `--reconnect` 时（SetReconnect 帧）断线后会自动重连，重连成功弹窗
  自动消失；会话确认销毁则停留在 `gone` 弹窗。

## 会话驱动流程

`TerminalPane` 挂载后按以下流程建立连接：

1. 通过 `GET /api/sessions/:id` 确认绑定会话仍存活；
2. 以 `WS /ws?session_id=<id>`（子协议 `webtty`）附着；
3. 连接断开且服务端允许重连时，`resolveSession()` 再次确认会话存活；
   返回 `null`（会话已销毁）则停止重连并进入 `gone` 弹窗 —— 新建会话
   只由左侧边栏发起，保证会话生命周期单一归属。

## WebTTY 协议

浏览器与 Go 服务端通过 WebSocket 通信，消息格式为二进制帧：

```text
[消息类型(1 byte)][负载(可选)]
```

- 客户端 → 服务端：`0x31` 输入、`0x32` Ping、`0x33` 尺寸调整（JSON）；
- 服务端 → 客户端：`0x31` 输出（原始字节）、`0x32` Pong、`0x33` 标题、
  `0x34` 偏好（JSON）、`0x35` 重连参数（JSON）、`0x36` 握手完成（空帧）。

握手完成帧 `0x36` 在附着时的初始化帧（标题/偏好/重连）之后发送：客户端
在此之前丢弃上行输入，避免 xterm 为流中的终端查询（DSR/DECRQM/OSC）
自动生成的应答被写回 PTY、注入到并不等待这些应答的程序里。
（历史输出重放已移除：服务端不再把缓冲的历史字节流回放给新附着者——
环形切口的字节流无法重建终端状态，长输出会话下会刷屏且拼错画面；画面
由客户端 attach 时发送的首个尺寸调整帧触发 SIGWINCH，让前台程序整帧重绘。）

连接建立后直接收发帧（无握手/认证帧，访问控制交由部署层）。

## 构建流程

```bash
# 在仓库根目录执行（pnpm workspace）
pnpm --filter gotty-frontend build
```

产物输出到 `apps/web/dist/main.js`。随后 `make static` 会把
`apps/web/dist` 产物拷贝到 `internal/api/static`，由 Go 的
`go:embed` 在编译期打包进二进制。