# 优化 1:Agent 可驱动 API —— 读屏 / 等待 / 输入注入

> 状态:**待实施**(对标 [flipbit03/terminal-use](https://github.com/flipbit03/terminal-use) 调研结论)
>
> 背景:调研 `tu`("tmux for your coding agent")后确认,它面向 AI agent 的核心
> 能力是**输入侧**——`type/press/mouse/wait` + 读屏截图。GoTTY 目前只覆盖了
> "输出侧"(`gotty capture` 跑完拿快照),缺"看屏、等文字、发按键"这三个
> agent 驱动原语。本设计补齐它们,让 GoTTY 从"人的远程终端"升级为
> "人的远程终端 + agent 可驱动",且保留我们独有的远程访问与图形协议能力。

## 1. 目标

让 AI agent(或其他程序化客户端)通过 REST API 驱动一个**正在运行**的 GoTTY
会话,与 `tu` 对齐:

| `tu` 原语 | GoTTY 对应(本设计新增) |
|---|---|
| `tu screenshot`(文本/PNG) | `GET /api/sessions/{id}/screen?format=text\|json\|png` |
| `tu wait --text <regex> --timeout` | `POST /api/sessions/{id}/wait`(正则匹配或屏幕静止) |
| `tu type` / `tu press` | `POST /api/sessions/{id}/keys`(字节注入) |
| `tu run`(新会话) | 已有 `POST /api/sessions`(幂等/复活) |
| `tu list` / `status` | 已有 `POST /api/sessions/status` |

关键洞察(沿用 capture-design.md 的结论):服务端本就是 PTY 字节流的中枢,
"渲染 = 字节流 → 文本网格 + 图片对象"不需要浏览器即可完成。`internal/capture`
里已有一个完整可用的 VT 仿真器(`internal/capture/emulator.go`,支持 16/256/24-bit
颜色、备用屏、滚动区、kitty/sixel/iTerm2 图片提取),**serve 模式只需把 PTY 输出
tee 一份进这个仿真器,读屏能力就齐了**——不需要为 agent 重写渲染栈。

## 2. 设计

### 2.1 会话镜像仿真器(mirror emulator)

- 在 `internal/session` 的输出泵(`session.go` 的 outputPump,现把 PTY 输出写入
  conn 和 ring)中增加一个**可选的镜像写入端**:`capture.Emulator` 实例,按会话
  初始尺寸创建,PTY 每读出一块字节就 `emu.Write(buf)` 一份。
- 内存有界:网格固定 cols×rows;OSC/DCS/APC 缓冲已有 `gfxBufLimit`(16MB)上限;
  图片资产 `images[]` 数量上限由仿真器统一控制。镜像默认开启(成本≈一个固定
  网格),由 `serve` 选项 `--mirror=false` 可关。
- 尺寸同步:resize 时镜像必须跟着变。当前 `Emulator` 没有 Resize 方法
  (capture 固定尺寸),**前置依赖 0002 的 `Emulator.Resize(cols, rows)`**。
- 并发:仿真器不是线程安全的,镜像读写统一在 outputPump 的临界区内完成,
  快照端点通过 `Session.Screen()` 在锁内取网格副本(深拷贝 Grid,毫秒级)。

### 2.2 读屏:`GET /api/sessions/{id}/screen`

- 查询参数 `format`:`text`(去 ANSI 的纯文本,默认)、`json`(复用
  `internal/capture/render.go` 的 RenderDocument 形状:version/engine/
  cols/rows/cursor/palette/cells/images[])、`png`(复用 PNG 光栅化;
  注意与 capture 一致:CJK/emoji 为占位框,像素级请走浏览器引擎)。
- 响应统一包一层快照元数据:`{"taken_at": ..., "mirror": true, ...}`。
- 复用点:`internal/capture/render.go` 的文本/JSON 渲染器直接调用,不新增
  渲染逻辑。

### 2.3 等待:`POST /api/sessions/{id}/wait`

- 请求体:
  ```json
  {"regex": "Complete|error", "timeout_ms": 10000, "quiet_ms": 500}
  ```
  语义与 `tu wait` 一致,任一满足即返回(长轮询,最长 `timeout_ms`):
  1. 镜像屏幕文本命中 `regex`;
  2. 输出静默 `quiet_ms`(判定"画面稳定",对齐 capture 的 quiet stop);
  3. 超时(`timed_out: true`)。
- 返回:命中时的完整快照(同 2.2 的响应形状)+ `{"matched": true/false,
  "quiet": true/false, "timed_out": true/false}`。
- 实现:在 outputPump 里维护一个"已通知版本号"(每次镜像写入后 bump),
  wait 请求订阅版本号变化并做正则匹配;长轮询用 `http.Request.Context()`
  取消,会话销毁时立即返回 404。
- 复用点:`internal/capture/driver.go` 已有 marker/quiet/timeout 三种停止
  判定逻辑,抽成可复用的"匹配器"供 serve 与 capture 共用。

### 2.4 按键注入:`POST /api/sessions/{id}/keys`

- 请求体:`{"input": "ls -la\r", "encoding": "text|base64"}`(默认 text,
  按 UTF-8 编码后原样写入 PTY;base64 用于注入任意字节)。
- 实现:输入目前只能经 attach 连接(`Attach` 的 conn 读端)进入 PTY。新增
  `Session.Input(p []byte) error`——不走 attach、直接写 PTY master
  (与 attach 输入共用同一把写锁,保证不交错)。`permit-write=false` 时该
  端点返回 403(与现有写权限语义一致)。
- 命名键(`Enter`/`F5`/`Ctrl+C`)不做服务端翻译——agent 直接发字节序列最
  通用(`\r`、`\x1b[15~`);README 给出对照表即可,不引入 tu 的键名 DSL。

### 2.5 无客户端附着时的查询应答(前置依赖 0002)

全屏程序(vim/htop/ncurses)在**没有浏览器客户端附着**时,其 DA/DSR/DECRQM
终端查询无人应答会挂起启动(浏览器端 xterm 附着时由 xterm 应答)。agent 驱动
场景恰好是"无浏览器"的,所以 0002 实现的**查询应答器**要接入 serve 的输出
循环:检测到程序查询时,把应答字节写回 PTY。这是本优化能驱动 vim 级程序的
硬前置,文档见 `docs/feat/0002-native-emulator-completeness.md`。

## 3. 涉及文件

| 文件 | 改动 |
|---|---|
| `internal/session/session.go` | outputPump 增加镜像写入;新增 `Screen()` 快照(锁内深拷贝)、`Input(p)` 直写;wait 订阅器 |
| `internal/session/manager.go` | 按 id 暴露 Screen/Input/Wait 能力(经 Session) |
| `internal/api/session_handler.go` | 新增 screen / wait / keys 三个 handler |
| `internal/api/server.go` | 注册三条路由 |
| `internal/capture/emulator.go` | `Resize(cols, rows)`(0002 一并做) |
| `internal/capture/driver.go` | 抽出可复用的 marker/quiet 匹配器 |
| `apps/web/src/utils/api.ts` | 前端预留(暂不接 UI,仅 API 层) |
| `docs/` | README REST API 一节补三条端点 |

## 4. 测试与验收

- 单测:mirror 快照 = 浏览器视角(用已知输出流断言 Grid 内容);wait 的
  regex/quiet/timeout 三分支;`Input` 与 attach 输入并发不交错。
- e2e(无浏览器):`curl` 全流程——create → keys 注入 `htop` → wait 屏幕稳定
  → screen 读到 htop 标题行 → signal SIGTERM。
- e2e(有浏览器):agent 驱动与真人浏览器**同时**操作同一会话,互不破坏
  (真人看到 agent 的输入如同自己输入)。
- 验收标准:
  1. 无浏览器客户端时 `vim` 能通过 `POST /keys` 驱动且不卡启动(依赖 0002);
  2. `--mirror=false` 时内存零增长且读屏返回 404/503;
  3. `permit-write=false` 时 keys 返回 403。

## 5. 不做的事(范围外)

- 不做 `tu` 的鼠标点击定位(`--on-text`)——GoTTY 无屏幕坐标→按钮映射的
  需求场景;如需可后续基于 JSON cells 的 `bbox` 扩展。
- 不做监控流(`tu monitor` 的 30fps 差分)——浏览器页面已是监控器;如要
  SSH 低带宽场景可另立优化。
- 不做键名 DSL——agent 直接发字节。
