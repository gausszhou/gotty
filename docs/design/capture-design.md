# GoTTY Capture 命令设计方案

> 状态：**M1（native 文本）、M2（native 图片/PNG）、M3（browser 引擎 +
> `/capture/:sid` 渲染页 + 前端图片）、M4（attached 引擎 + 前端合成截屏 +
> `POST /api/sessions/:id/capture` + `--keys` 交互）、M5（browser 动画 GIF）
> 均已实现**（M4/M5 于 2026-11-04）。
>
> **使用推荐：M3/M5 优先、M2 兜底** —— 像素级渲染（真实字体/CJK/emoji、
> sixel/iTerm2 图片的正确形态）用 `gotty capture --engine browser --format png`;
> 动态渲染（htop 刷新、进度条、spinner、构建动画）用
> `gotty capture --engine browser --format gif`；无浏览器/无人值守/要文本或
> kitty 图片时用默认 native 引擎。
>
> 目标：提供 `gotty capture` 命令——像 Playwright 驱动浏览器一样，执行指定命令，
> 取回该命令在浏览器终端中的渲染结果（文字与图片均支持）。

## 1. 背景与目标

当前 gotty 只有 `gotty serve`：真人浏览器通过 REST 创建会话、WS 附着 PTY。缺一个
"自动化取渲染结果"的通道——跑指定命令、得到"浏览器终端里看到的样子"（文本+图片）。

Playwright 类比：

| Playwright | GoTTY capture |
|---|---|
| `page.goto(url)` | 在 PTY 中执行指定命令 |
| `waitForLoadState` / `waitForTimeout` | 退出 / 静默等待（渲染稳定判定） |
| `page.evaluate(serialize)` | `--format text/json`（文字结果） |
| `page.screenshot()` | `--format png`（像素结果） |
| 浏览器渲染 Inline Image | `images[]`（图片结果） |

**关键洞察**：gotty 服务端本就是 PTY 字节流的中枢（`internal/terminal.Terminal` 读取
全部输出），"渲染" = "字节流 → 文本网格 + 图片对象"，这一步不需要浏览器即可完成；
当需要像素级保真（字体/CJK/emoji/图形协议图片）时，再叠加真实浏览器渲染路径。

## 2. 渲染结果模型 RenderDocument

所有引擎产出的统一 JSON 文档（`--format json`）：

```json
{
  "version": 1,
  "engine": "native | browser",
  "command": "chafa", "args": ["--format", "kitty", "logo.png"],
  "cols": 120, "rows": 30,
  "exit_code": 0, "timed_out": false, "duration_ms": 1234,
  "palette": {"bg": "#000000", "fg": "#cccccc"},
  "cursor": {"row": 0, "col": 0, "visible": true},
  "text": "…快照时的屏幕文本（去 ANSI，图片处留 ⛶[imgN]）…",
  "cells": [{"r": 0, "c": 0, "ch": "R", "fg": "#ff0000", "bg": "#000000", "bold": true}],
  "images": [{
    "id": 0, "protocol": "kitty | sixel | iterm2",
    "row": 1, "col": 2, "cell_cols": 20, "cell_rows": 8,
    "width": 400, "height": 300, "format": "png",
    "data_uri": "data:image/png;base64,…"
  }],
  "captured_at": "2026-…"
}
```

- `text`：给人/脚本/LLM 的纯文本（图片处占位）；
- `images[]`：data URI 直出，可落盘、可喂视觉模型；
- `cells`：native 引擎的单元格级样式（SGR 派生），可选（体积大时用 `--no-cells`）。

## 3. 引擎矩阵

| 引擎 | 实现 | 保真 | 依赖 | 适用 |
|---|---|---|---|---|
| **browser**（推荐像素） | chromedp 驱动真实 `/#/capture/:sid` 页面截图（M3 已实现） | 100% 浏览器所见 | Chrome/Chromium（`--browser-path` 可复用系统 Chrome） | 像素级保真、CJK/emoji、截图存证 |
| **native**（兜底） | Go 进程内：PTY + VT 仿真 + 图形协议解码 + 光栅化（M1/M2 已实现） | 高（文本+图片协议） | 无浏览器、无 Node | 无人值守、CI、LLM 自动化、kitty 图片 |
| **attached**（M4，可选） | 在线浏览器 CaptureRequest/Result 协议快照 | 100% | 需客户端在线 + 前端合成截屏 | 巡检、远程查看当前屏幕 |

> **能力差异（重要）**：浏览器路径的图片渲染依赖 `@xterm/addon-image`，
> 支持 **sixel 与 iTerm2 inline**；**kitty 图形协议只有 native 引擎支持**。
> 像素级文字（真实字体/CJK/emoji）只有 browser 引擎提供；native 的 PNG
> 里 CJK/emoji 渲染为占位框。

## 4. CLI 设计

```
gotty capture [flags] [--] <command> [args...]
  --engine string       渲染引擎: native|browser|attached(默认 native)
  --format string       结果格式: text|json|html|png(默认 text;
                        browser/attached 未显式指定时默认 png);
                        browser 额外支持 gif(动画截图)
  --cols int            终端列数(默认 120)
  --rows int            终端行数(默认 30)
  --wait-ms int         输出静默 N ms 判定渲染稳定(默认 500)
  --timeout duration    总超时;超时仍输出当前画面,标 timed_out:true(默认 30s)
  --marker string       输出流中出现该字符串即判定渲染稳定
  --out string          输出目标: 路径 或 "-"(text/json/html 缺省→stdout;
                       png 缺省→ gotty-capture-<时间戳>.png,
                       gif 缺省→ gotty-capture-<时间戳>.gif)
  --browser-path string browser 引擎: 复用系统 Chrome/Chromium(未指定则自动查找)
  --port int            browser 引擎: 临时 server 端口,0=随机(默认 0)
  --session-id string   browser/attached: 附着已有会话(与 command 互斥)
  --server string       attached: GoTTY 服务地址(默认 http://127.0.0.1:9049)
  --keys strings        attached: 截图前先按下的键名(逗号分隔)
  --gif-fps int         browser gif: 目标帧率(默认 10;受截图速度限制,实际约 2–5)
  --gif-duration duration browser gif: 录制时长上限(默认 0 = 直到停止条件;
                       只作采样上限,不改变停止条件)
  --gif-max-frames int  browser gif: 帧数上限,防体积/内存爆炸(默认 200)
  --gif-optimize bool   browser gif: 丢弃与上一帧像素相同的帧,
                       其时间并入 delay(默认 true)
```

示例：

```sh
gotty capture --format text -- 'ls -la'
gotty capture --format json -- chafa --format kitty logo.png     # images[]
gotty capture --format png --out screen.png -- htop              # 全屏 TUI
gotty capture --cols 200 --rows 50 --marker 'Press q' -- htop
gotty capture --engine browser --format png -- chafa --format kitty logo.png
gotty capture --engine browser --session-id abc123abc123abca --format png -o live.png
gotty capture --engine attached --session-id abc123abc123abca -o live.png
gotty capture --engine browser --format gif --out progress.gif -- \
    sh -c 'for i in 1 2 3 4 5; do printf "\rprogress %d/5" $i; sleep 0.2; done'
gotty capture --engine browser --format gif --gif-duration 8s --out build.gif -- \
    sh -c 'make; make test'                            # 只录前 8 秒的构建动画
```

## 5. native 引擎（M1/M2）

### 5.1 模块划分（新增 `internal/capture/`）

```
internal/capture/
  emulator.go   # x/vt 外观：查询应答队列(DrainAnswers) + 图形帧扫描器 +
                # Grid 物化 + x/vt 缺口补偿(宽字符行尾换行、IRM、CSI s/u、REP)
  graphics.go   # kitty / iTerm2 inline / sixel 字节流解析 → image.Image + 摆放
  render.go     # 网格 → text / json / html / png（光栅化 + 图片合成）
  driver.go     # 执行 + 等待（exit / 静默 / marker / timeout）+ 快照
cmd/capture.go  # cobra 子命令，注册进 cmd/root.go 的 init
```

- **PTY**：复用 `terminal.New(command, args, terminal.WithInitialSize(w, h),
  terminal.WithRawMode())`（`internal/terminal/terminal.go`），读输出喂
  仿真器，`Wait()` 取退出码。raw 模式取消内核回显/规范缓冲：查询应答写回
  PTY 才能被程序读到(否则被 ECHOCTL 以 `^[` 回显成乱码、dd 等按字节读的
  程序在规范模式下还会等换行而挂起)。不经会话层/WS，纯本地执行。
- **文本仿真**：`charmbracelet/x/vt`(独立模块)——完整 VT 仿真器，内建
  DSR/DA/DECRQM/OSC 颜色查询应答(写内部 pw 管道,由本层泵入应答队列)、
  备用屏、Resize、按 vttest 一致性。本层做三件事:① 应答队列
  `DrainAnswers()`(读循环取应答写回 PTY);② 字节帧并行扫描器——在喂
  x/vt 的同时识别 OSC/DCS/APC 载荷交 graphics.go 提取图片,并跟踪
  `CSI ? 25 h/l` 光标可见性;③ 补偿 x/vt 缺口:行尾放不下的宽字符先换行
  (x/vt 会丢弃)、IRM 插入模式注入 ICH、ANSI `CSI s/u` 翻译为 DECSC/DECRC、
  宽字符 REP 翻译(避免 x/vt 只记录单宽字符)。
  宽度语义与浏览器端 xterm.js 对齐:ambiguous 按 1 格(CJK 全角仍 2 格),
  已由单测锁定。
- **图形协议解码**（服务端从原始字节流直接提取）：
  - kitty：`ESC_G …` 分片重组（`a=0/1/2` 续传、`t=d` base64、`f=100` PNG/JPEG），
    `p=row;col` 定位、`s/v` 缩放、`c=1` 占格；
  - iTerm2 inline：`OSC 1337;File=…;inline=1:<base64> ST`，单帧整体解码；
  - sixel：`ESC P q … ESC \`，用 `github.com/mattn/go-sixel` 解码；
  - 像素尺寸 ÷ 默认 cell 尺寸（可配 `--cell-w/--cell-h`）换算占用格数。
- **PNG 渲染**：Go `image` 标准库 —— 底色填充 → 逐格画字形（fg/bg/粗体/斜体/
  下划线/反转）→ 按位置合成解码图片。
  - 字体策略（首版）：go:embed 嵌入等宽字体（如 `x/image/font/gofont/gomono`，
    仅拉丁字母）+ runewidth 占宽；CJK/emoji 画 tofu 或留白，文档注明像素级
    保真请用 browser 引擎（M3）。
  - 可选增强：系统字体查找（fontconfig），二期评估。
- **HTML**：网格已带样式，逐格转 `<span style="color/background/…">`，零额外成本。

### 5.2 等待语义（渲染稳定判定）

1. **进程退出**：PTY 读端 EOF + `Wait()` 返回 → 最终画面（`exit_code`）；
2. **静默等待**（默认）：输出停顿 ≥ `--wait-ms` → 判定稳定（对位 networkidle）；
3. **哨兵**：`--marker` 出现（对位 waitForSelector）；
4. **超时兜底**：`--timeout` 到点返回当前画面并标 `timed_out: true`。

## 6. browser 引擎（M3）

### 6.1 原理

chromedp 驱动**真实 gotty 页面**对 `.terminal` 元素截图。浏览器/DevTools 层面截图
走合成器，**不受 WebGL `preserveDrawingBuffer` 限制**（那是 JS 侧 `toDataURL` 才受限），
所以无需对现有渲染器做任何修改即可截到 WebGL 内容。

### 6.2 管线

```
gotty capture --engine browser -- chafa --format kitty img.png
  ├─ 进程内起临时 gotty serve（net.Listen 随机端口，仅绑 127.0.0.1）
  ├─ REST POST /api/sessions {id:生成, command, args, width, height}   ← 固定尺寸
  ├─ chromedp 打开 http://127.0.0.1:PORT/#/capture/<sid>?cols=..&rows=..（hash 路由）
  ├─ 轮询 window.__gottyCaptureReady / __gottyCaptureError（带超时）
  ├─ 渲染稳定判定（双信号，职责分离）：
  │    浏览器只报"渲染就绪"；"业务稳定"由 chromedp 侧判定：
  │      - 退出：轮询 REST GET /api/sessions/:id → exited=true
  │      - 静默：轮询页面 window.__gottyLastActivity（CaptureView 每次 term.write 刷新）
  │      - 哨兵：轮询页面 window.__gottyTextTail（去 ANSI 尾窗）含 --marker
  │      - 超时：--timeout
  ├─ 稳定后对 .terminal 元素截图（取 bounding rect → Page.captureScreenshot clip）
  └─ 收尾：DELETE /api/sessions/<sid> + 关闭临时 server（进程组收尾）
```

> 说明：浏览器引擎不旁路读 PTY —— 会话的 outputPump 已是独占读者，旁路会丢字节。
> "业务稳定"信号全部来自页面全局标志 + REST 轮询，逻辑与 native driver 对应。

### 6.3 前端配合（也是独立产品增强）

1. **新增 `/capture/:sid` 渲染页**（与多会话管理页隔离，见 §7）；
2. `.terminal` 上叠 `@xterm/addon-image`（M3）：否则浏览器端不渲染图形协议图片，
   截图截不到图——日常页与 capture 页同步受益。

### 6.4 GIF 动画截图（M5，已实现 2026-11-04）

**结论先行：GIF 只在 browser 引擎实现，不做 native 光栅化采样**。原因：native 的
PNG 光栅化是"网格近似"（CJK/emoji 是占位框、字体非真实渲染），录成动画的保真
价值低于 png 静态快照；而 browser 引擎本来就要逐帧真实渲染（截图），GIF 只是把
"停止后截一帧"换成"判定循环里按帧率定时截图 + 停止后补一帧收尾"，实现成本低且
像素级保真。静态 png 与动画 gif 复用同一套停止语义（exit / 静默 / marker / timeout）。

```
gotty capture --engine browser --format gif --out anim.gif -- <command>
  ├─ 与 png 相同的启动流程:临时 server → 会话 → capture 页 → 渲染就绪
  ├─ EmulateViewport(cols*9, rows*18) 设一次(与 png 路径一致,视口定尺寸+全页截图)
  ├─ 判定循环(poll ticker 20ms)中:先做停止判定;未触发时按 --gif-fps 截图:
  │    now >= nextAt → CaptureScreenshot(单帧 10s 超时)→ 解码 → 量化 → 入帧缓冲
  │    (截图每张约 100–300ms,实际帧率约 2–5fps,--gif-fps 超出时自然降速)
  ├─ 停止触发 → sleep 400ms(留给异步图片渲染)→ 补一帧收尾(force,At=停止时刻)
  └─ EncodeGIF:delay 由相邻帧采样时刻差换算,末帧覆盖到停止时刻 → 落盘 .gif
```

**帧缓冲与去重**（`internal/capture/gif.go`）：

- 采样即量化：截图解码后立即 `PopularQuantize`（流行色直方图 → ≤256 色局部
  调色板，其余像素就近映射）成 `image.Paletted`（1 字节/像素）再入缓冲——
  200 帧 @120×30 只有 ~120MB，而不是 RGBA 的 ~1.9GB；
- **optimize 去重在量化前做**：同一帧保留原始 RGBA（`last`），新帧先与上一帧
  `bytes.Equal` 像素对比，相同则丢弃（时间自然并入上一帧 delay）。注意不能在
  量化后按"索引序列"比较——两帧调色板不同但索引可能相同（纯色换色画面）；
- `--gif-max-frames`（默认 200）与 `--gif-duration` 双上限；`--gif-duration`
  只是采样上限，不改变停止条件；
- EncodeGIF 的兜底合并（force 收尾帧可能与末帧相同）用调色板感知比较：
  逐像素比 `palette[pix]` 的实际颜色。

**时间轴**：第 i 帧 delay = 第 i+1 帧采样时刻 − 第 i 帧采样时刻（毫秒→百分之一
秒，四舍五入，最小 1），**末帧延迟到停止时刻**——播放总时长 = 停止时刻 − 首帧
时刻 = 录制时长；`--gif-optimize` 合并相同帧后总和不变。`LoopCount=0` 无限循环。

**边界**：

- 内容静止（只有 ≤1 个不同画面）：编码时返回
  `gif: content is static (single frame); use --format png for a still image`，
  提示用户改静态快照；
- 备用屏（htop/vim）：页面真实渲染，`?1049` 切换自然录进帧序列；
- 光标：`CaptureView` 已 `cursorBlink:false`，不会出现光标闪烁噪声；
- `--format gif` 传 native 引擎直接报错（提示用 browser 引擎）；gif 缺省输出
  文件名 `gotty-capture-<时间戳>.gif`。

**量化器选型**：终端画面颜色多为少量纯色（背景黑、前景灰、ANSI 色）+ 反锯齿
过渡，流行色量化（直方图取 Top-N，其余就近映射）能精确保留主要颜色，且每帧
局部调色板各不相同、由 GIF 编码器按需写局部色表，比固定 ANSI 256 色表保真更好
（反锯齿灰阶不会坍缩成 24 级灰度带）；`x/image/draw` 自 v0.45 起不再自带
量化器实现（仅保留 stdlib 接口别名），故自研 ~60 行，无新增依赖。

## 7. 前端路由与 CaptureView（M3）

### 7.1 路由设计

引入 vue-router（`apps/web/package.json`），两条路由：

| 路由 | 组件 | 用途 |
|---|---|---|
| `/` | 现有 `App.vue`（原样挂载） | 多会话管理页，行为零改动（TabBar/manifest/轮询/空态） |
| `/capture/:sid?cols=&rows=` | 新增 `CaptureView.vue` | 截图专用渲染页 |

**模式：`createWebHashHistory`（首版）**。原因：当前静态服务是精确路由
（`internal/api/server.go` 只挂 `GET /`、`GET /main.js`、`GET /favicon.png`，无 SPA
fallback）；hash 路由（`/#/capture/<sid>`）后端零改动。二期若要对外正式 URL
（`/capture/<sid>`），再给 server.go 加一处 fallback（非 `/api/*`、`/ws` 的未知 GET
回 index.html）。

### 7.2 CaptureView 设计要点

- **独立、零副作用**：不 import `manifest.ts`、不读写 localStorage、无 TabBar、
  无空态卡、无 2s 轮询——headless 全新 profile 无影响，M4 attached 模式在真人
  浏览器打开也不污染用户设备清单。
- **行为**：mount → `getSession(sid)` 确认存活 → 创建 xterm（固定 cols/rows 来自
  query；未传则 fit 后发 resize）→ 复用全局 `multiplexer.attach(sid)` 附着
  （单连接多路复用，见 ws-multiplex.md；`Terminal.vue` 的
  expose 即 `TermHandle`，组件不动）。
- **就绪握手**：`ws.ts` 的 `WSHooks` 加可选 `onReady`（收到 `MSG_REPLAY_DONE` 时
  回调，~3 行）；CaptureView 置 `window.__gottyCaptureReady = true`；失败路径置
  `window.__gottyCaptureError = <原因>`。
- **活动信号**：每次 `term.write` 刷新 `window.__gottyLastActivity`（browser 引擎
  静默判定用）；维护有界尾窗 `window.__gottyTextTail`（哨兵判定用）。
- **DOM 纯净**：loading 蒙层放 `.terminal` 容器外；截图目标 = `.terminal` 元素。
- **主题固定 dark**（当前终端默认），可选 `&theme=` query；加载 WebglAddon（现状
  不变），M3 起加载 image addon。
- **只读语义**：不发输入（仅 resize+ping）；临时 server 不带 `-w`。

## 8. attached 引擎（M4，已实现 2026-11-04）

复用**在线真人浏览器**做像素级快照（服务端发信号 → 客户端合成回传），零 Chromium。

- **协议**（`internal/terminal/protocol.go` 扩展帧表）：
  - S→C `CaptureRequest = 'C'`（payload 为 `{"include_selection":bool}`）；
  - C→S `CaptureResult = 'c'`（JSON，见下）。
  - 初稿写的 `0x37`/`0x38` 不成立：`0x37`('7') 已被 MirrorDiff 占用，实际取
    帧表里空着的 `'C'`/`'c'`。
- **分片**：路由帧的 payload 长度字段只有 2 字节（≤65535），而整屏 PNG 的 base64
  通常是几百 KiB。CaptureResult 因此按 24000 字符切片（`seq`/`total`/`data`），
  服务端按序拼接后再 base64 解码；`MaxCaptureChunks=1024` 兜住异常客户端。
- **会话层**：`Session.Capture(ctx, opts)` —— 取当前 `conn` 写 CaptureRequest 并
  注册 waiter，`masterToSlave` 收到 CaptureResult 时装配并 resolve；带超时
  （默认 10s）、无附着者 409、并发占用 429、客户端报错原样上抛。
- **REST**：`POST /api/sessions/:id/capture`（触发并阻塞返回结果，`?format=png`
  直接返回 PNG 字节，默认 JSON + base64）；`GET /api/sessions/:id/captures/latest`。
- **前端**：`WebglAddon(true)`（恢复 preserveDrawingBuffer，否则 drawImage 拿到
  空白缓冲）+ 把 `.terminal` 下所有 canvas 按 DOM 顺序合成到离屏画布，暴露
  `capture()`；`session-channel.ts` 处理 CaptureRequest 并按 24000 字符分片回传。
  **DOM 渲染器下 `capture()` 返回 null**（文字是 DOM 节点而非 canvas，合成出来
  只有背景色）——服务端回 502 并说明原因，而不是给一张空白图。截图页
  （`/capture/:sid`）默认用 DOM 渲染器，需要 attached 时加 `?webgl=1`。
- **尺寸**：attached 截的是"真人浏览器当前视口"，PNG 尺寸由页面决定（不是
  `cols×9 × rows×18`）；返回体里的 `cols/rows` 来自客户端回报的终端几何。
- **驱动**：`gotty capture --engine attached --session-id xxx [--wait-ms]
  [--keys Escape,Enter]`。`--keys` 在截图前经 `POST /keys` 发按键，让"点开菜单
  再截图"这类交互可复现。

> 冲突处理：快照由**服务端内建动作**发起（发当前附着者、存结果），CLI 只触发 +
> REST 取回，不抢附着、不动"单客户端 + 抢占"会话模型。

## 9. 安全考量

- native：本地执行，无网络面。
- browser：临时端口仅绑 127.0.0.1，用完即关；命令以当前用户身份执行（同 serve）。
- `/capture/:sid` 页面无鉴权——沿用"访问控制由部署层负责"策略；生产暴露需反向
  代理限制。
- attached（M4）：读取用户当前屏幕，权限面等同现有 WS 附着，同样由部署层控制。

## 10. 路线图

| 里程碑 | 内容 | 交付 | 状态 |
|---|---|---|---|
| **M1** | native 文本 | `--engine native --format text/json/html`；依赖 runewidth | ✅ 已实现 |
| **M2** | native 图片 | graphics.go（kitty → sixel → iterm2）+ `--format png` + `images[]`；依赖 go-sixel | ✅ 已实现 |
| **M3** | browser 引擎 + 路由 | vue-router、`/capture/:sid`、`ws.ts onReady`、临时 server 驱动、chromedp；前端 `@xterm/addon-image`（sixel/iTerm2） | ✅ 已实现 |
| **M4** | attached / API / 交互 | `CaptureRequest/CaptureResult`（分片）+ 前端 canvas 合成；`POST /api/sessions/:id/capture` + `captures/latest`；`--keys` 交互 | ✅ 已实现 |
| **M5** | browser 动画 GIF | `--format gif` + `--gif-fps/-duration/-max-frames/-optimize`；采样即量化、optimize 去重、delay 时间轴 | ✅ 已实现 |

## 11. 测试与验收

- **单元**：状态机 golden 测试（SGR/光标/滚动/备用屏/CJK 宽度）；图形解码器
  （kitty 分片重组 / sixel / iTerm2）；render 快照比对。
- **端到端**（真实命令喂 PTY）：
  - `gotty capture --format json -- tput cols` → 断言 `cols == 120`；
  - `gotty capture --format json -- printf '\e[31mRED\e[0m'` → 断言字体颜色；
  - `chafa --format kitty` → `images[]` 非空且 data URI 可解码；
  - `htop`/`vim` 全屏 TUI + `--marker`；CJK/emoji 输出；
  - `--wait-ms` / `--marker` / `--timeout` 三条等待路径（含 `timed_out: true`）；
  - 输出 png 用 read_image 人工核验。
- **路由回归**：`/` 行为不变（TabBar/manifest/轮询照旧）；`/#/capture/<sid>`
  无 TabBar、置就绪标志、不写 localStorage。
- **browser vs native 回归**：同一命令两张图 diff（M3）。
- **GIF（M5）**：`internal/capture/gif_test.go` 覆盖采样间隔/时长上限/帧数上限/
  optimize 去重/force 收尾、流行色量化的调色板与就近映射、EncodeGIF 的 delay
  时间轴与解码回放、静态画面与空帧错误；`internal/browser/browser_test.go` 的
  `TestBrowserEngineGIF`（`browser_e2e` 标签）跑真实 Chrome 录制并断言首末帧
  不同、帧为 ≤256 色 Paletted、尺寸与视口一致。

## 12. 改动清单汇总

| 文件 | 改动 |
|---|---|
| `cmd/root.go` | 注册 `capture` 子命令 |
| `internal/capture/*`（新） | emulator/graphics/render/driver；浏览器引擎在 `internal/browser`（M3） |
| `internal/terminal/protocol.go` | （M4）`CaptureRequest`/`CaptureResult` 帧 + 分片 |
| `internal/session/capture.go`（新） | （M4）Capture waiter + 分片装配 + latest |
| `internal/api/capture_handler.go`（新） | （M4）`POST /capture` 与 `GET /captures/latest` |
| `apps/web/package.json` | + vue-router（M3）；+ @xterm/addon-image（M3） |
| `apps/web/src/main.ts` | 注册 router：`/` → App，`/capture/:sid` → CaptureView |
| `apps/web/src/components/CaptureView.vue`（新） | 截图专用渲染页 |
| `apps/web/src/utils/ws.ts` / `session-channel.ts` | + `onReady` hook（M3）；CaptureRequest/Result 与分片（M4） |
| `apps/web/src/components/Terminal.vue` | （M3）+ image addon；（M4）+ `WebglAddon(true)` 与 `capture()` |
| `docs/design/ws-multiplex.md` / `README.md` | 帧表补 `CaptureRequest`/`CaptureResult`（M4） |

复用不动：`App.vue`、`TerminalPane.vue`、`utils/api.ts`（getSession/createSession）、
`utils/manifest.ts`（CaptureView 显式不依赖）。

## 13. 决策记录

1. **native 优先**：无人值守、无浏览器/Node 依赖，符合 gotty 单二进制哲学。
2. **browser = 真实页面元素截图**：零渲染器改造（合成器截图不受
   preserveDrawingBuffer 限制），保真 100%；attached 协议不作为首版（M4 才补）。
3. **hash 路由**：静态服务精确路由无 fallback → 零后端改动；history 模式二期。
4. **chromedp 而非 Playwright**：保持 Go 进程内的驱动；`--browser-path` 复用系统
   Chrome 可跳过下载。
5. **库依赖最小集**：runewidth（必须）、mattn/go-sixel（推荐，M2）、vt10x
   （可选替代自研状态机）、chromedp（仅 M3）。GIF 编码用标准库 `image/gif`，
   量化器自研，**无新增依赖**。
6. **GIF delay 语义偏离"固定帧间隔"**：按相邻帧的**实际采样时刻差**换算 delay，
   而不是用 `1/fps` 定长。理由：browser 截图耗时随画面复杂度波动（100–300ms），
   定长 delay 会让播放速度与实际录制速度脱节（画面越复杂播得越快）；用实测间隔
   则总时长与录制时长一致，`--gif-optimize` 合并帧后总和也不变。代价是帧率不均
   匀——对终端动画（进度条、htop 刷新）可接受。