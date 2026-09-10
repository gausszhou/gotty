# 优化 4:输入侧对齐 —— 鼠标驱动 / 键名层 / 粘贴 + agent CLI 面

> 状态:**待实施**(对标 [flipbit03/terminal-use](https://github.com/flipbit03/terminal-use) 二次调研,2026-08-30)
>
> 背景:0001 落地了"看屏 / 等文字 / 发字节"三个 agent 驱动原语,但 `tu` 的
> **输入侧**还剩三块没对齐,且当时被显式划出范围(0001 §5):鼠标
> (`mouse click/drag/scroll` + `--on-text` 语义定位)、键名 DSL(`press
> Escape : w q Enter`)、括号粘贴(`paste`)。此外 agent 只能靠 `curl` 拼 REST,
> 没有 `tu` 那种"stateless CLI + 自动 daemon"的顺手程度。
>
> 结论:本优化把**输入侧**补齐(鼠标事件构造 + 跟踪模式状态机 + `--on-text`
> 语义定位、键名→字节表、条件括号粘贴),并新增一层 CLI 薄封装
> (`gotty mouse/press/paste/screen/…`),让 GoTTY 在"agent 能驱动"这一维度与
> `tu` 等价;`tu monitor`、字体栅格化 PNG、scrollback、per-session env、
> 服务端 list 等仍留在范围外(§5,已另立 0005)。
>
> 范围外判断依据:`tu` 的 PNG 渲染器(`src/render/image.rs`)只画单元格背景 +
> 字形,**不含 sixel/kitty/iTerm2 图形**;我们的 `internal/capture/graphics.go`
> 已经是净优势,本优化不动渲染栈,只补输入侧与 CLI 形态。
>
> 实施前已核对 `x/vt` 源码:鼠标 X10/SGR 两路编码与模式跟踪**上游已有**
> (`vt/mouse.go` 的 `SendMouse`、`vt/csi_mode.go` 的 `isModeSet`),本优化因此
> 从"自写状态机 + 三协议编码器"收缩为"事件构造 + 未导出的模式查询补位"。

## 1. 现状盘点

### 1.1 `tu` 全量能力 → GoTTY 映射

| `tu` 命令 | GoTTY 现状 | 归属 |
|---|---|---|
| `run <cmd> --size CxR` | `POST /api/sessions`(`width`/`height`) | ✅ 已有 |
| `run --env/--cwd/--term/--shell/--scrollback` | 无(`terminal.Options.Env` 无 flag,仅全局) | 0005 §2.4 |
| `kill` | `DELETE /api/sessions/{id}` | ✅ 已有 |
| `list` | 无(设计上服务端不存清单,见 `CONTEXT.md`) | 需 ADR(不在 0005) |
| `status`(pid/alive/exit code/size) | `GET /api/sessions/{id}`(缺 `exit_code`/`cols`/`rows`) | §2.8 |
| `screenshot`(文本) | `GET /screen?format=text` | ✅ 已有 |
| `screenshot --png` | `GET /screen?format=png`;像素级走 `capture --engine browser` | ✅ 已有 |
| `screenshot --font/--font-size/--no-cursor` | 无(`--font`/`--font-size` 参数 + 光标叠印;栅格化本身已有) | 0005 §2.2 |
| `cursor`(row,col) | `GET /screen?format=json` 的 `cursor` 字段 | ✅ 已有 |
| `scrollback --lines N` | 无(x/vt 已默认保留 10000 行,未暴露/未可配) | 0005 §2.3 |
| `type <text>` | `POST /keys {"input"}` | ✅ 已有 |
| `press <key>...`(含 `Ctrl+C`/`F5`/多键序列) | 无(agent 须自备 `\x1b[15~` 之类的字节) | **§2.4** |
| `paste <text>`(括号粘贴) | 无 | **§2.5** |
| `mouse click/down/up/move/drag/scroll/state`(含 `--button/--mods/--clicks/--on-text/--on-regex/--match-index/--force/--amount`) | 无(`grep mouse` 只命中一句重置 `?1000l/?1002l/?1003l/?1006l` 的注释) | **§2.1–2.3** |
| `resize <CxR>` | `POST /api/sessions/{id}/resize` | ✅ 已有 |
| `wait --text/--stable/--timeout` | `POST /wait`(`regex`/`quiet_ms`/`timeout_ms`) | ✅ 已有(CLI 默认值对齐 §2.6) |
| `monitor`(30fps 差分 TUI) | 浏览器页(非终端内) | 0005 §2.1 |
| `daemon start｜stop｜status` | `gotty serve`(手动常驻) | 搁置(0005 §5) |
| `self update` | `gotty self update` | ✅ 已有 |
| `usage`(agent 精简手册) | 无(`--help` 是给人看的全量输出) | 0005 §2.5 |

### 1.2 缺口归因

| 缺口 | 为什么现在会痛 |
|---|---|
| 鼠标 | agent 驱动 GUI 式 TUI(`mc`、dialog 安装器、`htop` 的 F 键条、ncurses 按钮)时完全没有输入手段;`--on-text` 的"按标签点,而不是猜坐标"正是为 LLM 省 token 设计的 |
| 键名层 | agent 必须自己背 terminfo 字节;错误率与 token 成本都高,且 `Alt+X`/`Ctrl+方向` 这类组合极易写错 |
| 括号粘贴 | 往启用 `?2004` 的程序里粘多行会被当逐行回车,shell 会直接执行每一行——不安全 |
| CLI 面 | 每步都要拼 `curl -X POST … -d '{...}'`;没有 `--json`/TTY 自动判定的输出约定 |

## 2. 设计

### 2.1 镜像侧鼠标跟踪模式状态机

- 在会话镜像(0001 §2.1 的 `capture.Emulator` tee)上增加 `MouseTracker`,
  跟踪应用开启的鼠标模式:`?9`(X10)、`?1000`(普通)、`?1002`(按键拖动)、
  `?1003`(任意移动)、`?1005`(UTF-8 坐标)、`?1006`(SGR 坐标)、`?1015`(urxvt),
  以及 `?2004`(括号粘贴,§2.5 用)。
- **复用优先(已核对上游源码)**:x/vt 的 `Emulator` 已有 `SendMouse(m Mouse)`
  (`vt/mouse.go`)与模式跟踪 `isModeSet`(`vt/csi_mode.go`),内部覆盖
  `?9/?1000/?1001/?1002/?1003` 与 `?1006`。所以本优化**不自己写状态机与编码器**,
  而是:把鼠标事件交给 `SendMouse`,写入自家的 `io.Writer` 缓冲,再转发进 PTY。
- 两个必须自补的点:(1)`isModeSet` **未导出**,`mouse state` 与"未启用鼠标模式
  时 409"的判断需要在自家 tee 里跟踪 `CSI ? Pn h/l`(复用 `graphics.go` 的
  "跨块收集 + 尾缓冲"),或向上游提一个 `Mode(m) ModeSetting` 导出;
  (2)`SendMouse` 在**无任何鼠标模式开启时静默不写字节**,可据此判定 disabled。
- 合成状态(对齐 `tu` 的 per-session `MouseTracker`):虚拟光标 `(col,row)`、
  按住中的按钮集合、最后一次事件;供 `mouse state` 与 §2.7 叠印使用。
- 并发:与镜像写入同一临界区(`outputPump` 内),快照按 `Screen()` 的深拷贝约定返回。

### 2.2 线格式编码(复用上游,不自写)

| 协议 | 触发 | 字节形态 | 来源 |
|---|---|---|---|
| SGR | `?1006h` | `CSI < b;x;y M`(按下)/ `m`(抬起);`b` = 按钮位 + 修饰位(Shift 4 / Alt 8 / Ctrl 16) | x/vt `ansi.MouseSgr`(复用) |
| Default(X10) | 1000/1002/1003 且未开 1006 | `CSI M Cb Cx Cy`,坐标 **+32**;无按键移动用 `3+32` | x/vt `ansi.MouseX10`(复用) |
| UTF-8(1005)/ urxvt(1015)/ SGR-pixel | — | — | 上游明确 TODO,**本轮不做**(§5.8) |

- 按钮/修饰位由 `ansi.EncodeMouseButton(button, isMotion, shift, alt, ctrl)` 生成,
  移动(`MouseMotion`)与抬起(`MouseRelease`)由事件类型区分——都不必自己拼位域。
- 坐标 **0-based**、以当前尺寸为界;越界 `400`,并回报当前 `cols`/`rows`。
- `--clicks N`:同一坐标连续 N 次 down/up(`2` = 双击)。
- `drag`:起点/终点之间**插值**中间移动事件(对齐 `tu` 的 `handle_mouse_glided`,
  否则只落两个端点,拖动型控件不认)。
- 应用**未启用**鼠标模式:默认 `409`(附 `mouse state` 提示);`"force": true`
  时按 SGR 发字节(对齐 `tu --force`)。

### 2.3 语义定位:`--on-text` / `--on-regex`

- 请求携带 `target: {"on_text": "OK"}` 或 `{"on_regex": "..."}`,可加 `match_index`(默认 0)。
- 在可见屏**网格**上按"从左到右、从上到下"扫描(直接在 cells 上做,不用去空格的
  文本行,避免列错位),取匹配的**中心格**;匹配跨宽字符时取其左格。
- 回应带 `resolved_from` 与最终 `col`/`row`,便于 agent 复核;未命中 `404`。
- 复用点:`internal/capture/render.go` 的网格/文本渲染已能取全屏文本与样式。

### 2.4 键名层(`internal/keys`)

- 新增 `internal/keys`:`键名 → 字节`,覆盖
  - 字母/数字/符号:原样(`:`, `/`, `!`, `@` …);
  - 编辑/导航:`Enter \r`、`Tab \t`、`Escape \x1b`、`Backspace \x7f`、
    `Space`, `Delete CSI 3~`、`Insert CSI 2~`、`Home CSI H`、`End CSI F`、
    `PgUp CSI 5~`、`PgDn CSI 6~`、方向 `CSI A/B/C/D`;
  - 功能键:`F1–F4` = `SS3 P/Q/R/S`,`F5–F12` = `CSI 15~ / 17~ / 18~ / 19~ / 20~ / 21~ / 23~ / 24~`;
  - 修饰:`Ctrl+<字母>` = `<字母> & 0x1f`,`Ctrl+@ [ \ ] ^ _` = `0x00 / 0x1b / 0x1c / 0x1d / 0x1e / 0x1f`,
    `Alt+X` = `ESC` + `X`,`Shift+Tab` = `CSI Z`,`Ctrl+方向` = `CSI 1;5A..`。
- `POST /keys` 增加 `{"keys": ["Escape",":","w","q","Enter"]}`,与 `input` 互斥;
  `input`(+`encoding: text|base64`)语义**完全不变**,老 agent 继续发裸字节。
- 未知键名:`400` 并回报候选键名(不静默吞字节)。

### 2.5 条件括号粘贴

- `POST /keys` 增加 `{"paste": "<多行文本>"}`。
- **条件包装**:镜像跟踪到 `?2004h` 才包 `ESC[200~ … ESC[201~`;未启用时原样写入
  (`tu` 无条件包装的做法会把 ESC 序列打给不认识的程序,变成屏幕垃圾);
  `"force_paste": true` 强制包装。
- 语义:整段文本作为**一次**粘贴,不触发 shell 逐行执行,也不触发 vim 自动缩进。

### 2.6 agent CLI 面(`cmd/agent.go`)

- 新增子命令,全部是对现有 REST 的薄封装:
  `screen`、`wait`、`type`、`press`、`paste`、`mouse`、`cursor`、`resize`、`signal`、`status`。
- 服务端定位优先序:`--server` > `$GOTTY_SERVER` >
  `$GOTTY_ADDRESS`/`$GOTTY_PORT`(`0.0.0.0` 归一为 `127.0.0.1`)>
  `http://127.0.0.1:9049`。会话定位:`--session <id>`(必填——服务端没有全量
  清单,不做隐式"默认会话",见 §5.5)。
- 输出约定(对齐 `tu`):stdout 是 TTY → 人类可读;**非 TTY 或 `--json` → 单行 JSON**;
  错误统一 `{"error": "..."}` + 非零退出码。`capture` 的 `--format` 语义不动。
- `wait` 的 **CLI 默认 `--timeout-ms 5000`**(对齐 `tu`),服务端默认仍是 30s、上限 5min。
- 示例:

  ```sh
  gotty mouse click --on-text "OK" --session "$ID"     # 按标签点,不猜坐标
  gotty mouse click --on-regex 'Buy.*' --clicks 2 --session "$ID"
  gotty mouse scroll down --amount 5 --session "$ID"
  gotty press Escape : w q Enter --session "$ID"       # 多键序列
  gotty paste "$(cat patch.diff)" --session "$ID"      # 括号粘贴
  gotty screen --format text --session "$ID"
  ```

### 2.7 可选:PNG 叠印虚拟鼠标光标

- 镜像有合成光标时,`GET /screen?format=png&mouse_cursor=1` 在对应格叠印品红标记
  (对齐 `tu`:空闲为品红字形,按住为填充格),让"agent 点了哪里"一眼可见。
- 浏览器引擎截图(`capture --engine browser`)不做叠印——真人视角不需要。

### 2.8 `status` 元数据补齐

- `StateDescription` 增加 `exit_code`、`cols`、`rows`(对齐 `tu status`)。
- 依赖:`internal/terminal` 的退出码目前只用了 `exited` bool,需要把
  `proc.Wait()` 的结果回传给 `Session`(`capture` 的 `Result.ExitCode` 是先例);
  当前尺寸从镜像/terminal 现取。

## 3. 涉及文件

| 文件 | 改动 |
|---|---|
| `internal/capture/mouse.go`(新) | 鼠标事件构造(`vt.MouseClick/Motion/Release/Wheel`)、模式跟踪补位(判 disabled/409)、拖拽插值;写入走 `SendMouse` → 缓冲 → PTY |
| `internal/capture/locate.go`(新) | 网格上按文本/正则定位中心格(含 `match_index`、宽字符、越界) |
| `internal/capture/emulator.go` | 鼠标模式/`?2004` 状态提取(或接入 x/vt 已跟踪的私有模式) |
| `internal/keys/keys.go`(新) | 键名 → 字节表 + 键序列解析 + 未知键名错误 |
| `internal/session/session.go` | 镜像持有 `MouseTracker`;新增 `Mouse(ev)` / `MouseState()`;退出码与当前尺寸入状态 |
| `internal/session/manager.go` | 按 id 透传 Mouse/MouseState |
| `internal/api/agent_handler.go` | 新增 `POST/GET /api/sessions/{id}/mouse`;`keys` 支持 `keys[]` / `paste` / `force_paste` |
| `internal/api/server.go` | 注册两条鼠标路由 |
| `cmd/agent.go`(新) | `screen/wait/type/press/paste/mouse/cursor/resize/signal/status` 薄封装 + `--json`/TTY 输出约定 |
| `cmd/root.go` | 注册 agent 子命令组 |
| `apps/web/src/utils/api.ts` | 前端预留(暂不接 UI) |
| `README.md`、`apps/docs/guide/usage.md` | 端点与 CLI 文档、键名对照表 |

## 4. 测试与验收

- 单测
  - 鼠标:X10 / SGR 两路的**逐字节断言**(含修饰位、多击、按住移动;对拍
    `ansi.MouseX10` / `ansi.MouseSgr`);自家模式跟踪与 `SendMouse` 的一致性
    (`?1000h` 下 X10、`?1006h` 下 SGR、全关时不写字节);拖拽插值点集;
    `--on-text`/`--on-regex` 定位(重复匹配 + `match_index`、宽字符、未命中、越界)。
  - 键名:全表对照 + `Ctrl/Alt/Shift` 组合 + 多键序列 + 未知键名报错。
  - 粘贴:`?2004` 开 / 关 / `force_paste` 三分支的包装行为。
  - `state`:空会话 `{"enabled": false}`,开启后 mode/held/光标正确。
- e2e(无浏览器,`curl` 全流程)
  1. 起 `mc` 或自写 curses 程序 → `mouse click --on-text` 命中标签 → `screen` 断言画面变化;
  2. `vim -u NONE` + `keys: ["Escape",":","w","q","Enter"]` 能退出(键名层闭环);
  3. 往启用 `?2004` 的程序粘贴多行 → 不逐行执行(单次粘贴语义);
  4. 应用未启用鼠标模式 → 点击 `409`,`--force` 可发字节。
- e2e(有浏览器):真人用鼠标与 agent 鼠标点击作用于**同一会话**互不破坏,
  镜像模式状态与 xterm.js 侧一致。
- 验收标准
  1. `tu` 的 `mouse`/`press`/`paste` 三条能力有 GoTTY 等价入口,且 CLI 非 TTY 输出可被 `jq` 解析;
  2. `--permit-write=false` 时 `keys`/`mouse`/`paste` 全部 `403`;
  3. `--mirror=false` 时 `mouse state`/`screen` 返回 `503`(与 0001 一致);
  4. `input`(裸字节)老路径行为零变化——回归测试必须覆盖。

## 5. 不做的事(范围外)

§5.1–5.7 已另立 **[0005(观察侧与配置面补齐)](0005-observation-and-config.md)**,本节只留索引与不作为理由。

1. **`tu monitor`(终端内 30fps 差分观察器)** —— 浏览器页已是监控器(且带多标签、
   拖拽排序、i18n);仅 SSH/无浏览器/低带宽场景才有独立价值 → 0005 §2.1。
2. **无 Chrome 的 CJK 字体栅格化 PNG** —— native PNG 已用内嵌 Go Mono 栅格化
   (`internal/capture/render.go`),缺的是 CJK 字体与 `--font/--font-size`;
   像素级仍走浏览器引擎 → 0005 §2.2。
3. **scrollback 读取** —— x/vt 的 Screen **默认已分配 10000 行滚动缓冲**(只是我们
   没暴露,也没做成可配) → 0005 §2.3。
4. **per-session `env`/`cwd`/`term`/`scrollback`** —— `createSessionRequest` 目前只有
   `id/command/args/width/height`;`terminal.Options.Env` 还没有 flag 入口 → 0005 §2.4。
5. **服务端全量 `list` 端点** —— 与 `CONTEXT.md` 的"服务端不保存清单、清单是设备侧
   唯一事实来源"直接冲突,**不在 0005**,需先写 ADR。
6. **daemon 自动拉起/自动退出** —— `tu` 的零配置来自"CLI 自动 spawn + Unix socket +
   8h 空闲退出";我们是常驻 HTTP 服务,换来的是**可远程、可多人共享**,不建议对齐
   → 0005 §5 记为搁置。
7. **`gotty usage` / 仓库内 agent 指令片段** —— `tu usage` 是给 LLM 的 <1000 token
   速查表(与 `--help` 分工明确),我们仓库也没有 `AGENTS.md`/`CLAUDE.md` → 0005 §2.5。
8. **UTF-8(`?1005`)/ urxvt(`?1015`)/ SGR-pixel 鼠标协议** —— 上游 `SendMouse` 明确
   标为 TODO,且这三者现实中极少有 TUI 使用;`--force` 场景统一按 SGR 发字节即可,
   真需要时再补自家编码。
