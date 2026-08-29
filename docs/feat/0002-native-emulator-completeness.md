# 优化 2:native 仿真器完整度 —— 查询应答与缺失 CSI 对齐

> 状态:**待实施**(对标 `tu` 的 alacritty_terminal 方案调研结论)
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
> 结论:继续手写(Go 生态没有 alacritty_terminal 等价物,移植 C 系仿真器超纲),
> 但补齐"查询应答 + 缺失 CSI + 尺寸变化",并用一致性测试锁住行为。

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

## 2. 设计

### 2.1 查询应答器(answerer)

- 新增应答通道:`Emulator` 增加只读"应答队列"(如 `func (e *Emulator)
  Answers() []byte`,消费后清空),仿真器解析到查询序列时把应答字节压入队列;
  由调用方(outputPump / capture driver)把应答写回 PTY。
- 应答内容(对齐 xterm 默认行为):
  - DA1(`CSI c` 无参数)→ `CSI ? 6 c`(VT102 with advanced video,最通用);
  - DA2(`CSI > c`)→ `CSI > 0;0;0c`(xterm 兼容格式);
  - DSR 5 → `CSI 0 n`;DSR 6 → `CSI {row+1};{col+1}R`(1-based);
  - DECRQM(`CSI ? Ps $ p`)→ `CSI ? Ps;0 $ y`(0 = 未识别,或按实际状态
    应答 ?25/?1049 等已跟踪的模式);
  - OSC 10/11/12 → `OSC 10;rgb:rrrr/gggg/bbbb BEL`(用当前调色板
    `palette` 应答;无调色板配置时可不答或答默认色)。
- 接入点:
  - **capture driver**(`internal/capture/driver.go`):读循环里每次 `emu.Write`
    后取 `Answers()` 写回 PTY——native 引擎立即获得"vim 能启动"的能力;
  - **serve outputPump**(`internal/session/session.go`):仅在**无浏览器客户端
    附着**时应答(有人附着时 xterm 会答,双应答会污染);0001 的 agent 驱动
    场景即属此类。可选开关 `--answer-queries=false` 关闭。
- 应答字节与"程序实际预期"的一致性:DA 应答只需让程序认为终端存在,
    不必逐版本精确;按 xterm 默认应答即可(与浏览器端 xterm.js 的应答同为
    兼容路线)。

### 2.2 缺失 CSI 补齐

- `@` ICH / `P` DCH / `X` ECH:与现有 `L`/`M`(insert/delete lines)同构,
  在行内做 copy 移位 + 空白填充,遵守滚动区与行尾边界。
- `b` REP:重复前一字符 n 次(等价于把前一 putRune 再执行 n 次;需记录
  `lastRune` 与当时 style,注意宽字符语义)。
- `h`/`l` 非私有模式:IRM(4)——插入模式后续字符前移,与 `@` 共用移位
  逻辑;DECAWM(7)——关闭时 `putRune` 到行尾不再换行,而是原地覆盖
  (`pendingWrap` 不置位);LNM(20)——LF 后额外 CR(可答状态但不启用,
  现代程序不用)。
- `g` TBC:维护 256 列 tabstop 位图(默认 8 列一个),`CSI 0 g` 清当前列、
  `CSI 3 g` 全清;`TAB` 前进逻辑改查位图。
- `Emulator.Resize(cols, rows)`:重建两个 Grid 为新尺寸,内容按左上角
  截断/补齐,滚动区与光标位置夹紧,清空应答与图形暂存(尺寸突变时
  kitty 放置坐标会失效,直接清 images 最稳)。

### 2.3 一致性测试(锁行为)

- 每个新增 CSI 的单元测试(参数边界、滚动区交互、宽字符)。
- **vttest 子集**:用 vttest 的 Screen 测试(光标移动、擦除、滚动区、DECAWM、
  tabstop)跑一遍,把通过的用例固化为 fixtures(输出字节流 → 期望 Grid)。
- **真实程序 golden**:`vim -u NONE -c 'q'`、`htop -d 1`、`w3m`、`less`
  的 PTY 输出流固化为 fixture,断言启动后 1s 内快照含预期内容(防回归挂起)。
- 现状先补两个回归用例:① DA/DSR 查询后 vim 能在 1s 内退出;② 无应答时
  capture 超时兜底路径仍可用。

## 3. 涉及文件

| 文件 | 改动 |
|---|---|
| `internal/capture/emulator.go` | 应答队列 + DA/DSR/DECRQM/OSC10-12 应答;ICH/DCH/ECH/REP/IRM/DECAWM/TBC;tabstop 位图;`Resize` |
| `internal/capture/driver.go` | 读循环回写 `Answers()` |
| `internal/session/session.go` | outputPump 无客户端时回写 `Answers()`(经 `--answer-queries` 开关) |
| `internal/capture/emulator_test.go` | 新 CSI 单测 + vttest/golden fixtures |
| `cmd/serve.go` / `internal/config` | `--answer-queries` 选项 |
| `docs/design/capture-design.md` | native 引擎能力说明更新 |

## 4. 验收标准

1. `gotty capture -- vim -u NONE -c 'q'` 在 2s 内完成且无超时告警;
2. `gotty capture -- htop -d 1` 快照含 htop 界面(非空屏);
3. 0001 的 `POST /keys` 驱动 vim 时无需浏览器客户端,DA/DSR 不挂起;
4. `Resize` 后镜像快照与浏览器视角一致(0001 联调);
5. `go test ./internal/capture/` 全绿,新增 vttest 子集 fixtures 通过。

## 5. 不做的事(范围外)

- 不移植/引入 C 系或 Rust 系完整仿真器(超纲;手写 + 一致性测试足够覆盖
  capture 与 agent 驱动场景)。
- 不做滚动回看(scrollback)——快照只需当前屏,保持网格固定尺寸。
- 不做字符集/双宽历史遗留(ESC ( 系列已消费忽略,现代 UTF-8 世界足够)。
- 不做超链接提取(OSC 8)——如需要可在 JSON cells 里加 `link` 字段,单列
  优化,不阻塞本项。
