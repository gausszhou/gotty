# 优化 2:native 仿真器完整度 —— 选用现成仿真器或补齐手写引擎

> 状态:**已实施(路线 A:x/vt 替换,2026-08-29)**
>
> 实施摘要:仿真层换成 `charmbracelet/x/vt` 外观(§3.1/§3.2);图形提取保留
> 自家 `graphics.go` 并行扫描(§2.5);补偿了 x/vt 的四处缺口——行尾放不下
> 的宽字符换行、IRM 插入模式、ANSI `CSI s/u`、宽字符 REP;capture PTY 改
> raw 模式避免了回显/规范缓冲吞掉应答;两组回归通过(vim 0.02s 退出、
> htop 界面完整)。x/vt 未实现且本层未补偿的:`CSI 1 J` 整行擦除(ED1 语义
> 以 x/vt 为准)、DECRQM 对 ANSI 模式 4 的应答为 0(仅应答已跟踪的私有
> 模式)。`—answer-queries` 开关已落地 serve。
>
> 背景:`tu` 直接复用 alacritty 的仿真器,能应答 Device Attributes/光标位置
> 等终端查询,所以 vim/less/mc 不会卡在启动。我们的 `internal/capture/
> emulator.go` 是手写简化仿真器(约 885 行),按文档注释"window-size queries
> are consumed and ignored"——**只消费、不应答**。这在"跑完拿快照"的 capture
> 场景够用,但有两个硬伤:
>
> 1. **全屏程序启动即挂**:vim/htop 启动时发 DA(`CSI c`)/DSR(`CSI n`)查询,
>    无客户端应答就一直等——capture native 引擎和 0001 的 agent 驱动场景都会
>    中招(capture 目前靠超时兜底,结果是空屏/半屏快照)。
> 2. **部分 CSI 缺失**:insert/delete char、erase char、repeat 等,少数 TUI
>    用它们画界面,缺失会导致画面错位。
>
> **结论(修正)**:2026-08 调研确认 Go 生态存在 alacritty_terminal 的等价物——
> [charmbracelet/x/vt](https://github.com/charmbracelet/x)(完整 VT 仿真 + DSR/CPR
> 内建应答 + vttest 一致性工作流,见 §2),"Go 生态没有等价物"不再成立。本优化
> 优先**替换**为现成仿真器(x/vt);若接入成本过高(依赖面、CJK 宽度语义差异),
> 回退**手写补齐**。两条路线共用 §3 的接入点设计,图形协议解码在任何路线下
> 都保留在自家 `graphics.go`。

## 1. 现状盘点(`internal/capture/emulator.go`)

已支持:UTF-8、光标移动/定位、擦除(J/K)、滚动区(DECSTBM)、insert/delete
lines(L/M)、scroll up/down(S/T)、SGR(16/256/24-bit 全量)、备用屏
(?1047/?1049,简化语义)、保存/恢复光标(7/8、s/u)、RIS、TAB、kitty/sixel/
iTerm2 图形提取(OSC/DCS/APC 三路,16MB 上限)、光标可见性(?25)。

缺失(本优化范围):

| 类别 | 序列 | 现状 | 影响 |
|---|---|---|---|
| **查询应答** | DSR `CSI 5 n` / `CSI 6 n`(光标位置报告)、DA1 `CSI c`、DA2 `CSI > c`、DECRQM `CSI ? Ps $ p` | 吞掉 | **vim/htop/mc 启动挂起**(最严重) |
| **查询应答** | OSC 10/11/12(前景/背景/光标色查询)、XTVERSION `CSI > 0 q` | 吞掉 | 程序取色失败(次要;仅真彩色程序) |
| 字符操作 | ICH `CSI @`(插入空字符)、DCH `CSI P`(删除字符)、ECH `CSI X`(擦除字符) | 缺失 | 少数 TUI 画面错位 |
| 重复 | REP `CSI b`(重复前一字符) | 缺失 | 进度条类输出退化 |
| 模式开关 | `CSI h/l` 非私有模式(4=IRM 插入模式、7=DECAWM 自动换行、20=LNM) | 缺失 | DECAWM 关闭时换行行为错误;IRM 影响插入类 TUI |
| 制表位 | TBC `CSI g`(清制表位) | 缺失 | 依赖自定义制表位的程序行为错误 |
| 光标样式 | DECSCUSR `CSI Ps SP q` | 缺失 | 仅外观,快照无感(可忽略) |
| 窗口操作 | `CSI Ps t` | 吞掉 | 正确(窗口操作对快照无意义) |
| 尺寸变化 | `Emulator.Resize(cols, rows)` | **没有此方法** | 0001 镜像随 PTY resize 失效 |

## 2. 现成仿真器选型(2026-08 调研)

### 2.1 候选盘点

| 库 | 定位 | 许可/活跃 | 查询应答 | Resize | 图形解码 |
|---|---|---|---|---|---|
| **[charmbracelet/x/vt](https://github.com/charmbracelet/x)(`x/vt` 独立模块)** | 完整 VT 仿真器(C SI/OSC/DCS/ESC、screen/buffer、scrollback、damage、mouse/focus、`SafeEmulator`) | MIT,极活跃(2026-08) | ✅ DSR/CPR **内建**(应答写内部 `pw` 管道) | ✅ | ❌ |
| **[charmbracelet/x/vttest](https://github.com/charmbracelet/x)(同仓库)** | 创建 PTY、随时抓取屏幕状态的虚拟终端(≈capture 场景) | MIT,同上 | (基于 x/vt) | ✅ | ❌ |
| **[charmbracelet/ultraviolet](https://github.com/charmbracelet/ultraviolet)** | 渲染层(Bubble Tea 兼容);fuzz 对照 **libghostty-vt** 真实仿真器 | MIT,376★,活跃 | — | — | ❌ |
| **[taigrr/bubbleterm](https://github.com/taigrr/bubbleterm)** | 无头嵌入式仿真器(基于 x/vt + ultraviolet) | 0BSD,活跃 | ✅ DA1 **自动应答并回写 PTY**(有防死锁测试) | ✅ | ❌ |
| **[rcarmo/go-te](https://github.com/rcarmo/go-te)** | pyte 忠实 Go 移植 + **ESCTest2 一致性套件** | MIT,新但活跃 | ✅ 回调式(`ReportDeviceAttributes/Status/Mode`) | ✅ | ❌ |
| [hinshun/vt10x](https://github.com/hinshun/vt10x) | 老牌 VT10x 后端 | MIT,2023 停更 | ❌ | — | ❌ |
| [buildkite/terminal](https://github.com/buildkite/terminal) | ANSI→HTML 日志渲染(非屏幕网格模型) | MIT,活跃 | — | — | — |
| [charmbracelet/x/ansi](https://github.com/charmbracelet/x) | parser 非完整仿真器(自研引擎时的解析层) | MIT,活跃 | — | — | ❌(只能**生成** iTerm2 序列) |

### 2.2 推荐路线:x/vt(含 vttest 生态)

选 x/vt 而非其他候选的理由:

- **查询应答开箱即用**:`handlers.go` 中 DSR 5/6、DECXCPR 已实现,应答写入
  仿真器内部 `pw` 管道——正是我们要的"应答字节通道",对应 §3.1 的接入点;
- **完整度对齐 tu 的 alacritty_terminal**:screen/scrollback/damage/cursor/
  mouse/focus/模式管理全有,含 `SafeEmulator` 线程安全封装(0001 镜像共享
  读屏需要);附 vttest 一致性工作流(`vt.yml`/`vttest.yml`),替代手写
  一致性测试;
- **生态可复用**:`x/vttest` 的"PTY + 随时抓屏"就是 capture 场景的样板;
  ultraviolet 用真实 ghostty vt 做 fuzz 对照,渲染语义可信;
- **许可友好**:MIT(与项目一致),独立 go.mod 模块,引入成本低。

### 2.3 备选:go-te

若 x/vt 的接入出现问题(CJK 宽度语义、依赖面),备选 go-te:ESCTest2 背书、
`ReportDeviceAttributes/ReportDeviceStatus/ReportMode` 回调式应答与 Diff/
History screen 都很贴合,`Resize` 具备。代价:较新(2026-02)、社区活跃度
不如 Charm、无渲染生态。

### 2.4 不推荐与排除

- vt10x:停更 3 年,VT102 能力面,无应答;
- buildkite/terminal:日志→HTML 定位,非屏幕模型;
- 自研 parser + 自写屏幕层(x/ansi):等于重复造轮子,仅当两条路线都失败
  时才考虑。

### 2.5 替换取舍:图形协议保留策略

现成仿真器均**不支持** kitty/sixel/iTerm2 图形解码(x/ansi 也只能"生成"
iTerm2 序列)。策略:仿真/读屏层交给 x/vt,**图片提取保留在自家
`internal/capture/graphics.go`**——在字节流喂给仿真器的同时并行解析
OSC/DCS/APC 提取 `ImageAsset`(与仿真器状态解耦,尺寸/坐标按放置序列
自行换算)。这与 0001"渲染栈复用"的分层思路一致:读屏归仿真器,图片归
提取器。

## 3. 设计

### 3.1 查询应答接入(两条路线共用)

- 应答通道:由"仿真器内建"(路线 A:x/vt `pw` 管道)或"手写队列"(路线 B:
  `Answers()` 只读队列)提供,调用方式统一为"读循环取应答 → 写回 PTY"。
- 接入点:
  - **capture driver**(`internal/capture/driver.go`):读循环里每次喂字节后
    取应答写回 PTY——native 引擎立即获得"vim 能启动"的能力;
  - **serve outputPump**(`internal/session/session.go`):仅在**无浏览器客户端
    附着**时应答(有人附着时 xterm 会答,双应答会污染);0001 的 agent 驱动
    场景即属此类。可选开关 `--answer-queries=false` 关闭。
- 应答内容对齐 xterm 默认(DA1 → `CSI ? 6 c`、DA2 → `CSI > 0;0;0c`、DSR 5
  → `CSI 0 n`、DSR 6 → `CSI {r+1};{c+1}R`、DECRQM → `CSI ? Ps;0 $ y`、
  OSC 10/11/12 → 用调色板应答);x/vt 未覆盖的项(DECRQM、OSC 色查询)在
  接入层补答。

### 3.2 路线 A:x/vt 替换(推荐)

1. **依赖**:`go.mod` 引入 `github.com/charmbracelet/x/vt`(独立模块)。
2. **仿真核心**:`internal/capture` 的仿真层从 `Emulator` 换成 x/vt
   (并发场景用 `SafeEmulator`);Cell 映射:x/vt 的 cell 字段 → 我们的
   `Color/Cell` 与 RenderDocument `cells[]`(字段名对照在实现时固化);
   text/json/html/png 渲染逻辑保留,只换输入仿真层。
3. **Resize / scrollback / damage**:随 x/vt 免费获得;damage 可留作将来
   差分监控(对标 tu monitor)的储备。
4. **宽度语义验证(关键风险)**:当前用 `runewidth{EastAsianWidth:false}`
   刻意对齐浏览器端 xterm.js;x/vt 走 uax29/displaywidth,需用 §3.4 的
   golden(vim/htop/w3m/less + CJK 用例)验证差异;若不可接受,回退路线 B
   或叠加宽度适配层。
5. **图片提取**:保留 `graphics.go`,与 x/vt 并行从字节流提取(§2.5)。

### 3.3 路线 B:手写补齐(兜底)

仅在路线 A 验证失败时采用:

- 应答队列与 DA/DSR/DECRQM/OSC10-12 应答(内容见 §3.1);
- 缺 CSI:`@` ICH / `P` DCH / `X` ECH(与 L/M 同构的行内移位)、`b` REP
  (记录 lastRune+style,注意宽字符)、`h`/`l` 非私有模式(IRM/DECAWM/LNM)、
  `g` TBC(tabstop 位图);
- `Emulator.Resize(cols, rows)`:重建 Grid 按左上角截断/补齐,滚动区与
  光标夹紧,清图形暂存。

### 3.4 一致性测试(锁行为)

- 每个新增/涉及序列的单元测试(参数边界、滚动区交互、宽字符)。
- **vttest/ESCTest2**:x/vt 自带 vttest 工作流;手写路线用 vttest 子集固化为
  fixtures(输出字节流 → 期望 Grid)。
- **真实程序 golden**:`vim -u NONE -c 'q'`、`htop -d 1`、`w3m`、`less`
  的 PTY 输出流固化为 fixture,断言启动后 1s 内快照含预期内容(防回归挂起);
  替换前后 golden 快照对比(路线 A 验收)。
- 回归用例:① DA/DSR 查询后 vim 能在 1s 内退出;② 无应答时 capture 超时
  兜底路径仍可用。

## 4. 涉及文件(按路线)

### 路线 A(x/vt 替换)

| 文件 | 改动 |
|---|---|
| `go.mod` | + `github.com/charmbracelet/x/vt` |
| `internal/capture/emulator.go` | 仿真核心替换为 x/vt(或薄封装);Cell 映射层;`Resize` 直接用 x/vt |
| `internal/capture/graphics.go` | 保留,改为独立于仿真器的图片提取 |
| `internal/capture/driver.go` | 应答回写改读 x/vt `pw` |
| `internal/session/session.go` | outputPump 无客户端时回写应答(经 `--answer-queries`) |
| `cmd/serve.go` / `internal/config` | `--answer-queries` 选项 |
| `internal/capture/emulator_test.go` | golden 对比测试(替换前后快照一致性) |

### 路线 B(手写补齐)

| 文件 | 改动 |
|---|---|
| `internal/capture/emulator.go` | 应答队列 + DA/DSR/DECRQM/OSC10-12 应答;ICH/DCH/ECH/REP/IRM/DECAWM/TBC;tabstop 位图;`Resize` |
| `internal/capture/driver.go` | 读循环回写 `Answers()` |
| `internal/session/session.go` | outputPump 无客户端时回写 `Answers()`(经 `--answer-queries` 开关) |
| `internal/capture/emulator_test.go` | 新 CSI 单测 + vttest/golden fixtures |
| `cmd/serve.go` / `internal/config` | `--answer-queries` 选项 |
| `docs/design/capture-design.md` | native 引擎能力说明更新 |

## 5. 验收标准

1. `gotty capture -- vim -u NONE -c 'q'` 在 2s 内完成且无超时告警;
2. `gotty capture -- htop -d 1` 快照含 htop 界面(非空屏);
3. 0001 的 `POST /keys` 驱动 vim 时无需浏览器客户端,DA/DSR 不挂起;
4. `Resize` 后镜像快照与浏览器视角一致(0001 联调);
5. [路线 A] x/vt 替换后 golden 快照与替换前一致(vim/htop/w3m/less)、
   CJK 宽度与浏览器端一致;
6. `go test ./internal/capture/` 全绿。

## 6. 不做的事(范围外)

- 不移植 C/Rust 系仿真器——已有 Go 等价物(x/vt),更无移植必要;
- 不把 scrollback 暴露进 capture 输出模型(屏幕模型保持"当前屏"),即使
  底层 x/vt 支持 scrollback;
- 不自研 parser + 屏幕层(x/ansi 路线)——仅当替换与手写都失败才考虑;
- 不做超链接提取(OSC 8)——如需要可在 JSON cells 里加 `link` 字段,单列
  优化,不阻塞本项。