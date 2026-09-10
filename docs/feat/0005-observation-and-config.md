# 优化 5:观察侧与配置面补齐 —— scrollback / monitor / CJK 栅格化 / per-session 配置 / agent 自描述

> 状态:**待实施**(2026-08-30;承接 [0004](0004-agent-input-parity.md) §5 的"范围外"清单)
>
> 背景:0004 把 `tu` 的**输入侧**(鼠标 / 键名 / 粘贴)补齐后,`tu` 剩下的能力
> 缺口全部落在**观察侧与配置面**:`monitor`(终端内 30fps 观察器)、`scrollback`
> (读历史)、`screenshot --font`(CJK 栅格化)、`run --env/--cwd/--term`(会话级
> 启动参数)、`usage`(给 LLM 的速查表)。统一记为 0005;`list` 端点与 daemon
> 自动拉起**不在本优化**(前者需 ADR,后者搁置,见 §5)。
>
> 结论:本优化四项按依赖排序实施——
> ① **scrollback 暴露与容量治理**(最便宜:x/vt 的 Screen **默认就已分配 10000 行
> 滚动缓冲**,我们只是没暴露、也没做成可配,而且正在为它付内存);
> ② **per-session 配置**(`env`/`cwd`/`term`/`scrollback`,复用 ① 的容量开关);
> ③ **`gotty monitor`**(需要新增**只读、不抢占**的订阅通道,是四项里唯一有架构
> 取舍的);
> ④ **CJK 字体栅格化**与 ⑤ **agent 自描述**(独立,可并行)。
>
> 实施前已核对上游源码(`charmbracelet/x/vt`、`charmbracelet/ultraviolet`),下面
> 每条设计都标注了可复用的现成 API 与需要自补的部分。

## 1. 现状盘点

| 项 | 现状 | 缺口 |
|---|---|---|
| scrollback | x/vt 的 `Screen` **默认** `NewScrollback(DefaultScrollbackSize=10000)`(`vt/screen.go:26`);`Emulator.Scrollback()/ScrollbackLen()/ScrollbackCellAt(x,y)`、`Scrollback.Line(i)/Lines()/Len()/MaxLines()/SetMaxLines()`、`Screen.SetScrollback/SetScrollbackSize` 全都可用 | 我们的镜像包装层注释写着 "no scrollback exposure"(`internal/capture/emulator.go:9`),既没有读取端点,也没有容量开关 |
| 备用屏 | `Emulator.Scrollback()` 在备用屏(全屏程序)下返回 **nil** | 端点需要优雅处理(空结果 + `alternate_screen: true`),不能报 500 |
| monitor | 浏览器页(xterm.js + WebGL)是唯一的实时观察器;`GET /ws?session_id=` 走 `Session.Attach`,**读写且同 id 抢占**(旧客户端收 1013,`internal/api/ws_handler.go:176`);镜像是现成的屏数据源 | 没有"只读、不抢占"的订阅通道;终端内观察器(SSH / 无浏览器)完全缺失 |
| 差分数据 | x/vt 有 damage **类型**(`vt/damage.go`:`Damage`/`CellDamage`/`RectDamage`/`ScreenDamage`),但**没有"取出并清空 damage 列表"的 API**,它是给写入侧渲染用的 | 差分要我们自己维护(在镜像写入临界区按行/单元格标脏),这也是最省事可控的做法 |
| PNG 文本 | `internal/capture/render.go` **已经在做字体栅格化**:内嵌 Go Mono(`gofont/gomono`)+ `opentype` + `monoFace(cellH)` + 缺字走 `drawTofu`(空心框) | 字体固定、只有 Latin 覆盖;CJK/emoji 全是豆腐块;没有 `--font`/`--font-size` 选项 |
| per-session 配置 | `createSessionRequest` 只有 `id/command/args/width/height`;`terminal.Options.Env` 存在但**没有 `flagName`**(只能从配置文件全局注入);工作目录固定 `childWorkDir()`;`WithTerm` 已有 | 无法按会话指定 `env`/`cwd`/`term`/`scrollback`;`tu run` 四个参数全有 |
| agent 自描述 | 无 `gotty usage`,仓库内无 `AGENTS.md`/`CLAUDE.md`(glob 无结果);`--help` 是 cobra 全量输出 | agent 没有"读一次就知道怎么用"的入口;`tu` 的惯例是 `usage` 给 agent、`--help` 给人 |
| 会话发现 | `POST /api/sessions/status` 需传 `ids`;服务端不存清单(`CONTEXT.md`) | 不在 0005:与领域约定冲突,**需先写 ADR** |

### 1.1 scrollback 的内存量级(必读)

`uv.Cell` 的字段是 `Content string`(16B)+ `Style`(3 个 `color.Color` 接口 48B +
`Underline`/`Attrs` 约 8B)+ `Link`(两个 string,32B)+ `Width int`(8B)
≈ **112B/格**(64 位),而一行是 `[]uv.Cell`:

| 场景 | 估算 |
|---|---|
| 120 列 × 10000 行(x/vt 当前默认) | ≈ **130MB/会话** |
| 120 列 × 1000 行(与 `tu --scrollback` 默认一致) | ≈ **13MB/会话** |
| `--mirror=false` | 0(镜像不存在) |

`Scrollback.Push` 会裁掉行尾空单元格(`vt/scrollback.go:31`),所以实际低于估算;
但这说明**默认 10000 行是一个需要被治理的隐藏成本**,不是"顺手就能开"的功能。
结论:暴露 scrollback 时必须同时把默认容量降到 1000 并提供 `--scrollback=0`,
最终数值以 pprof/`-benchmem` 实测为准。

## 2. 设计

### 2.1 scrollback 的读取与容量治理(先做,最便宜)

- **读取端点**:`GET /api/sessions/{id}/screen?part=scrollback&lines=N`
  - `lines` 省略 = 全部;`N>0` = **尾部优先**(最近 N 行,agent 通常只要最近上下文)。
  - 格式复用现有 `format=text|json`:json 走 `capture.CellsJSON`,text 走 `Text()`;
    实现方式是把 `uv.Line` 转成我们现有的 `Grid` 行,复用 `render.go` 的渲染器,
    不新增一套输出格式。
  - 备用屏:返回 `{"alternate_screen": true, "lines": []}`,**不是错误**。
  - `--mirror=false` 时与其他 agent 端点一致返回 `503`。
- **容量开关**:`serve --scrollback int`(默认 **1000**,`0` = 关闭)
  - 关闭时对镜像调 `Screen().SetScrollback(nil)`,`Emulator.Scrollback()` 返回 nil;
    端点返回 `503` 或空(建议 503 + 原因,与 mirror 关闭同构)。
  - 该值随会话创建确定,供 §2.4 的会话级 `scrollback` 覆盖。
- **CLI**:`gotty scrollback --lines N --session <id>`(归属 0004 §2.6 的 CLI 组)。

### 2.2 `gotty monitor` —— 终端内实时观察器

- **关键约束(必须绕开)**:现有 `GET /ws` 是**读写且抢占**的——monitor 若走 attach,
  会把真人从浏览器里踢出去(1013)。所以 monitor **不占用 attach 槽位**,只订阅镜像。
- 方案选型:
  | 方案 | 做法 | 结论 |
  |---|---|---|
  | A 轮询 screen | `GET /screen?format=json` × 30fps | ✗ 每帧全量 cells(120×40≈4800 格)数百 KB,带宽与 CPU 都不划算 |
  | B 只读镜像订阅 | 新端点 `GET /ws?session_id=X&mode=mirror`:服务端在镜像更新时推送**脏行差分** | ✔ 推荐:不影响 attach 抢占语义、不写 PTY、带宽与变化量成正比 |
  | C attach 只读变体 | `AttachOptions{ReadOnly, NoPreempt}` | △ 改动看似最小,但把"每会话单客户端"的线协议复杂化(reconnect/preempt 语义要分叉) |
- **设计(方案 B)**
  - 服务端:镜像写入的临界区里,按行标记脏(`dirtyRows map[int]struct{}` 或位图),
    以固定节拍(≤30fps)把发生变化的行为一个二进制帧推给订阅者;订阅者 0 时零成本。
  - 协议:沿用现有二进制帧风格(见 `internal/terminal/protocol.go`)新增一个
    `msgMirror` 类型:帧 = 会话尺寸 + 脏行号 + 行 cells(紧凑编码,复用 JSON 亦可先跑通)。
  - 客户端:`charmbracelet/ultraviolet`(已在 `go.mod`)的 cellbuf 渲染到 TTY;
    **无变化不重绘**,`SIGWINCH` 只重排本地画面。
  - **不改变远端尺寸**:观察者 resize 不应写 PTY(否则会动到被观察终端);要改尺寸
    得显式 `gotty resize`。这与 `tu monitor`"处理终端 resize"的直觉相反,但更安全。
  - 交互:方向键在**命令行传入的会话列表**内切换(`--session a,b,c`;服务端无清单,
    不做隐式枚举——见 §5.2)、`Ctrl+C` detach、虚拟鼠标光标(0004 §2.1 的合成状态)
    以品红标记显示,与 PNG 叠印(0004 §2.7)一致。

### 2.3 CJK / emoji 字体栅格化

- 现状:`render.go` 已有完整栅格化链路(内嵌字体 + `opentype` + tofu 兜底),缺的是
  **字形覆盖**与**可替换字体**。
- 两步走:
  1. **`--font <path.ttf> --font-size <px>`**(零体积成本):用户自备字体(Noto Sans
     Mono CJK / Sarasa / 系统字体),走现有 `opentype.Parse` 链路;`monoFace` 从
     "固定 cellH 推导"改为"接受尺寸参数"。这直接对位 `tu screenshot --font/--font-size`。
  2. **内嵌子集**(可选):若要开箱即用,需内嵌一份 CJK 子集(全量 Noto CJK ≈ 10MB+,
     子集化后 1–3MB),体积与二进制"自包含"的承诺冲突 → 需实测后再定,
     建议先不内嵌。
- **emoji 不做**:彩色字形需要 CBDT/COLR 位图字体渲染,`golang.org/x/image/font`
  不支持彩色字形,成本远超收益(像素级仍可走浏览器引擎)。
- 叠印(0004 §2.7 的品红鼠标标记)与本节共用同一渲染入口,建议一起实现。

### 2.4 per-session 启动参数

- `createSessionRequest` 增加:`env`(`["K=V", …]`)、`cwd`、`term`、`scrollback`。
- `internal/terminal`:
  - `Options.Env` 补 `flagName`(全局默认,`--env K=V` 可重复)+ 会话级追加覆盖;
  - 工作目录:把 `childWorkDir()` 从"固定进程 cwd"改为"会话 `cwd` 优先,回退进程 cwd";
    Windows 侧同步(`process_windows.go` 已有 `newEnvBlock` 先例,环境块与 cwd 都要走同一入口);
  - `TERM` 用已有 `WithTerm`;`scrollback` 透给 §2.1 的容量开关。
- 校验:`cwd` 必须存在且是目录(否则 `400`);`env` 只接受 `K=V`(拒绝裸 `K` 与非法 `K`);
  单值长度与总量设上限,避免成为内存放大入口。
- **安全语义(写进 README 的安全一节)**:能创建会话 = 能起进程,因此 `env`/`cwd`
  **不新增攻击面**;真正的边界仍是 `--permit-write`、监听地址、TLS 与反向代理。

### 2.5 agent 自描述

- **`gotty usage`**:<1000 token 的速查表(对齐 `tu` 惯例:`usage` 给 agent、`--help`
  给人),内容 = 最短调用路径 + 键名表(0004 §2.4)+ 鼠标动作表(0004 §2.2)+
  常见坑(必须 `--`、shell 语法要 `sh -c`、`--permit-write=false` 会让输入 403、
  `--mirror=false` 会让读屏 503、`wait` 默认 30s)。
- **仓库内 `AGENTS.md`**:说明"什么时候该用 GoTTY"(需要真实终端渲染的程序:
  vim/htop/mc/dialog 安装器)、最短示例、指向 `gotty usage`;
  可选再加 `CLAUDE.md` 同内容。
- 可选交付形态:做成可安装的 skill(`gotty-drive`),把 `usage` 文本作为 skill 正文。

## 3. 涉及文件

| 文件 | 改动 |
|---|---|
| `internal/capture/emulator.go` | 暴露 scrollback 包装(`ScrollbackLines(limit)`、`ScrollbackLen()`、容量设置),更新 "no scrollback exposure" 注释 |
| `internal/session/session.go` | `Scrollback()` 快照(锁内按行拷贝,与 `Screen()` 同构);脏行标记(§2.2);会话级 `env/cwd/term/scrollback` 直达 `terminal` |
| `internal/session/manager.go` | 透传 Scrollback 与创建参数 |
| `internal/api/agent_handler.go` | `GET /screen` 增加 `part=scrollback&lines=N` |
| `internal/api/options.go` | 新增 `--scrollback`(默认 1000) |
| `internal/api/ws_handler.go` | 新增 `mode=mirror` 只读订阅分支(不触碰 attach 抢占路径) |
| `internal/api/mirror.go` | 订阅者注册 + 差分节拍推送 |
| `internal/terminal/options.go`、`process*.go` | `Env` 加 flag;`childWorkDir` 支持会话级 cwd;Windows 同步 |
| `internal/capture/render.go` | `--font`/`--font-size` 参数化 `monoFace`;光标叠印入口(与 0004 §2.7 共用) |
| `cmd/agent.go` | `scrollback`、`monitor` 两个子命令(+ 0004 那一组) |
| `cmd/usage.go`(新)、`AGENTS.md`(新) | §2.5 的速查表与 agent 指令 |
| `README.md`、`apps/docs/guide/usage.md` | 新端点/新选项/安全说明/内存量级 |

## 4. 测试与验收

- 单测
  - scrollback:行转 `Grid` 的文本/JSON 断言(含宽字符、颜色、超长行);`lines=N` 尾部优先;
    备用屏返回 `alternate_screen: true`;`--scrollback=0` 时 `SetScrollback(nil)` 生效;
    容量上限生效(超出后最旧行被丢弃)。
  - monitor:脏行标记的正确性(相邻两次写入只推变化的行)、订阅者 0 时零分配、
    订阅者断开后不泄漏 goroutine(与现有 `activeConns`/`wsWG` 同款校验)、
    `mode=mirror` **不影响** 正常 attach 的抢占(回归:真人浏览器 + monitor 同时在线不互相踢)。
  - 配置:`env`/`cwd`/`term`/`scrollback` 生效(在会话里 `echo $FOO`、`pwd`、`echo $TERM`);
    `cwd` 不存在 → 400;非法 `env` → 400;Windows 环境块与 cwd 行为一致。
  - 字体:`--font` 指向的 TTF 能渲染出非 tofu 字形(可用一个含 CJK 的测试字体在 CI 跳过);
    `--font` 不存在 → 明确报错,不静默回退。
- e2e
  1. `sh -c 'seq 1 500'` 后 `gotty scrollback --lines 20`:能读到 500…481,而 `screen` 只有可见屏;
  2. 同一会话:浏览器保持连接 + `gotty monitor` 同时观察,浏览器**不被踢**、monitor 画面随输入更新;
  3. `vim`(备用屏)下 `scrollback` 返回空且 `alternate_screen: true`;
  4. `gotty usage | wc -c` 在 1000 token 量级;`AGENTS.md` 里的示例可原样复制执行成功。
- 验收标准
  1. 默认配置下 120×40 会话的镜像内存增量在 §1.1 估算的 1000 行量级(附实测数字);
  2. monitor 与浏览器共存零抢占(用 1013 关闭帧断言"未被抢占");
  3. scrollback/monitor 在 `--mirror=false` 下统一 `503`,不 panic;
  4. `usage` 与 `AGENTS.md` 的每条示例都在 CI 的 e2e 里跑通(文档不能腐烂)。

## 5. 不做的事(搁置)

1. **服务端全量 `list` 端点** —— 与 `CONTEXT.md`"服务端不保存清单、清单是设备侧
   唯一事实来源"直接冲突;要做必须先写 ADR(可见性、鉴权、与客户端清单的合并规则),
   本优化只依赖显式 `--session` 传参绕开它。
2. **daemon 自动拉起 / 自动退出** —— `tu` 的零配置来自"CLI 自动 spawn + Unix socket +
   8h 空闲退出";我们是常驻 HTTP 服务,换来的是**可远程、可多人共享**,不建议对齐。
3. **emoji 彩色字形** —— `golang.org/x/image/font` 不支持彩色字形,像素级需求走浏览器引擎。
4. **内嵌 CJK 字体(若不采用子集化)** —— 与"单二进制自包含"的体积承诺冲突,先只做 `--font`。
5. **monitor 的服务端渲染/视频流** —— 差分脏行已经够用,服务端不做逐帧图像编码。
6. **UTF-8(`?1005`)/ urxvt(`?1015`)鼠标协议** —— 见 0004 §5.8,归 0004 的范围外。
