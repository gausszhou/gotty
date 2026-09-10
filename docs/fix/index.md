# 修复记录（2026-08-23）

本次会话围绕"刷新 / 重连 / 长输出 / TUI 退出"等场景的终端渲染问题做了一轮
排查与修复，均已在对应提交中固化。按问题组织如下。

---

## 1. 主题切换只改渲染层，运行中程序不同步 → 移除服务端主题同步

- **现象**：页面亮/暗主题只更新 CSS 变量与 xterm 配色，终端内运行的程序
  （vim/nvim 等）不感知；且整套服务端主题链路（COLORFGBG 注入、`theme` 字段、
  NotifyTheme/OSC 10/11/12 推送）被认为过度设计。
- **根因**：程序感知主题的机制（环境变量、OSC 查询应答）只在启动/查询时生效，
  运行时切换无法可靠传播；盲注 OSC 到 PTY 还会污染 shell 命令行。
- **修复**：主题切换收敛为纯前端行为（CSS 变量 + xterm `options.theme`）；
  服务端不再接收/透传 theme、不再注入 `COLORFGBG`、删除 OSC 推送协议与测试。
- **提交**：`28781a4`（terminal）、`26798dc`（api）、`8300f17`（文档同步）。

---

## 2. 提示符复位序列中的 ?1049l 导致"提示符与历史输出重叠"＋"光标始终在第一行"

- **现象**：`ls -al` 等长输出后，标题行被拼进文件列表（`total 260e Desktop  …`）、
  新提示符与 `.Xauthority` 等行叠在一起（`$ s gauss 102 … .Xauthority`），
  光标被固定在第一行。
- **根因**：`PROMPT_COMMAND` 每次提示符前注入 `ESC[?1049l`（离开备用屏）。
  bash 从不进入备用屏，而 xterm.js 把 `?1049l` 当作"切回主缓冲 + 恢复已保存
  光标位置"，反复执行把光标拽到陈旧位置，新提示符便画在历史输出行上。
- **修复**：`PROMPT_COMMAND` 复位序列移除 `?1049l`（保留鼠标/光标/括号粘贴
  复位）；补充负向断言测试。备用屏的进入/退出由 TUI 程序自己负责。
- **提交**：`28781a4`（含 `reset_prompt_test.go` 断言更新）。

---

## 3. 刷新后终端空白（shell 场景）

- **现象**：移除全量重放后，attach 不再送任何历史输出；bash 不会因 SIGWINCH
  重绘历史文本，屏幕只剩空白。
- **根因**：全量重放被移除后没有替代的画面恢复机制；resize/SIGWINCH 只能让
  前台程序重绘当前帧，对 shell 无效。
- **修复**：恢复"输出环缓冲"，attach 时重放最近 **256KB** 尾部
  （`attachReplayTailBytes`）——足以重建刷新前的画面，又不会像 MB 级全量
  重放那样疯狂滚动；保留 `SetReplayDone` 作为输入上行握手标记。
- **提交**：`c1fd31c`（session）。

---

## 4. 全量历史重放导致"疯狂刷屏"＋画面错乱

- **现象**：刷新页面时终端以最快速度重演全部历史（最多 4MB），且长输出会话
  中画面出现内容拼贴错乱（`total 256e`、权限串与提示符混叠）。
- **根因**：ring 为环形缓冲，回绕后从任意字节位置切开；重放起点既非转义
  边界，又无法重建终端状态（备用屏/光标/模式），字节流重放即"错位重演"。
- **修复**：重放从"全量"收敛为"尾部 256KB"；恢复会话重放后再补一次
  `r-1→r` 尺寸抖动（真实 TIOCSWINSZ 发 SIGWINCH），让前台程序整帧重绘、
  画面与真实 PTY 状态收敛；新会话不抖动。
- **提交**：`c1fd31c`。

---

## 5. 新建 / 切换会话后光标不自动聚焦

- **现象**：点「＋」新建会话或切换页签后，需要先点击终端才能输入。
- **修复**：`Terminal` 暴露 `focus()`；`TerminalPane` 在"挂载即激活"
  （新建会话，watcher 不触发初始值）与"激活变化"（切换页签）两个时机
  `fit + focus`。
- **提交**：`a79d44e`。

---

## 6. 终端被 fit 缩成 1 行（"无法向下"、行重叠）

- **现象**：某会话画面只剩一行：多行输出相互覆盖（`total 260e Desktop…`）、
  光标永远在第一行、无法向下滚动/查看。
- **根因**：隐藏页签（v-show `display:none`）在窗口 resize 时也执行 `fit()`，
  容器高度为 0 被 FitAddon 钳成 `rows=1` 并 resize 出 1 行终端，onResize 又把
  PTY 同步缩小，会话被永久破坏。
- **修复**（三层防御）：
  1. `Terminal.fit()` 增加可见性守卫：容器宽/高为 0 时跳过；
  2. `ws.ts` 发送 resize 前用 `saneSize()` 过滤 `cols/rows < 2` 的探测值；
  3. 服务端忽略 `rows<2` 的 resize（任何客户端来源兜底）。
- **提交**：`dfc3e12`。

---

## 7. 终端右键被劫持为粘贴，无法使用浏览器原生菜单

- **现象**：终端区域内右键不弹系统菜单，直接粘贴；恢复原生菜单的需求。
- **修复**：移除容器上的 `@contextmenu.prevent` 覆盖与 `onContextMenu`，
  右键恢复浏览器默认行为；粘贴能力保留在 `Ctrl+Shift+V` / `Ctrl+V` 快捷键。
- **提交**：`f12a993`。

---

## 8. 退出 opencode 等 TUI 后刷新出现乱码（`10;rgb:…`、`$y…`、`1R…`）

分两个独立缺陷，分两次修复：

### 8.1 重放尾部起点切在半截转义序列中间

- **表现**：刷新后屏上出现无 `^[` 前缀的载荷文本（`10;rgb:cccc/cccc/cccc`、
  `1R1016;2$y` 等）。
- **根因**：尾部重放从任意字节切开，若起点落在 OSC/CSI/DCS 载荷中间，
  缺失 `ESC[`/`ESC]` 前缀的部分被 xterm 当普通文本显示。
- **修复**：`alignTailStart` —— 跳过被切开的 UTF-8 续字节（丢弃半个字），
  并把起点回溯到最近的 `ESC`（1KB 窗口），让 xterm 从头解析该序列、载荷被
  静默消费而非显示；结尾处未终结的序列停留在收集态、不渲染。
- **提交**：`a249b57`。

### 8.2 重放唤醒终端查询，xterm 自动应答被写回 PTY

- **表现**：已修 8.1 后乱码仍现；内容与 8.1 相同且无 `^[`。
- **根因**（CDP 抓帧实锤）：opencode 启动时的查询（DECRQM/DSR/OSC）记录在
  输出环中；刷新重放时 xterm 重新解析并自动应答；旧逻辑在握手标记
  （`MSG_REPLAY_DONE`）到达时就放开输入上行，陈旧应答被当用户输入写回
  PTY，前台 shell 把载荷显示成乱码。
- **修复**：输入上行不再随握手标记立即开启，而是等 xterm 把重放全部解析完
  （`onWriteParsed`，`Terminal` 新暴露）再开启，600ms 兜底防卡；
  `clearTimers` 同步清理解析回调订阅。CDP 实测刷新后上行仅 resize/ping、
  无任何应答帧。
- **提交**：`f1a7328`。

---

## 验证方式

- Go：`go test ./...`、`go vet`、`gofmt`（session/terminal/api 均含针对性单测：
  尾部重放有界、恢复抖动、忽略 1 行 resize、转义边界对齐、`?1049l` 负向断言）。
- 前端：`vue-tsc --noEmit` + `vite build`。
- 浏览器实测：headless Chrome + CDP 自动化（新建会话 → 连续 `ls` → 逐行读
  屏幕文本 / WebSocket 帧级断言 / 像素行结构分析）复现与验证以上各场景。

---

# 修复记录（2026-09-11）

本轮修 Windows 上"页面能打开、任何会话都建不起来"的可用性故障：根因是
`creack/pty` 根本没有 Windows 后端，而代码里假设它有。

## 9. Windows：无法创建任何会话（PTY 不可用）

- **现象**：Windows 上 `gotty serve` 正常启动、`GET /` 正常返回页面，但
  `POST /api/sessions` 一律 500 `{"error":"failed to create session"}`（页面上
  点空态卡片/＋没有任何反应），服务端日志：
  `Failed to create session: failed to create terminal: failed to start command
  `cmd.exe`: unsupported`。即**服务端功能在 Windows 上整体不可用**。
- **根因**：`internal/terminal` 用 `github.com/creack/pty` 起 PTY，而该库**没有
  Windows 实现**：`start_windows.go` 的 `StartWithSize` 直接返回 `ErrUnsupported`，
  `pty_unsupported.go` 的 `open()` 同样如此（v1.1.24）；`pty.Setsize` 也是
  `winsize_unsupported.go`。`raw_other.go` 里"creack/pty 的 Windows 后端
  (conpty) 自己管理控制台"是**错误假设**，于是 Windows 既没有 PTY 也没有
  resize，`pty.Start*` 必然失败。
- **修复**：把 PTY 抽象为 `ptyProcess` 接口（`process.go`），平台各自实现：
  - `process_unix.go`：`creack/pty` + `exec.Cmd`，调用序列与改动前逐字一致
    （含 `startRawPTY` 原始模式与进程组信号）；
  - `process_windows.go`：直接基于 ConPTY API —— `CreatePseudoConsole`（成对
    管道）→ `PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE` 进程属性 → `CreateProcess`；
    `ResizePseudoConsole` 做 resize；`os.Process` 等退出并取退出码，非零包成
    `*exec.ExitError`（`capture/driver.go` 用 `errors.As` 读码）。
  两个只有实测才会发现的细节：① `CreateProcess` 之后必须释放交给 pseudoconsole
  的管道句柄副本，否则子进程退出时读端看不到 broken pipe、session 的输出 pump
  永久阻塞；② 必须置 `STARTF_USESTDHANDLES` 且不给句柄，否则 `CreateProcess`
  会把 gotty 自己的 std 句柄复制给子进程，cmd.exe 的 banner/提示符写进 gotty
  的控制台，浏览器里只剩一串控制序列。
- **涉及文件**：`internal/terminal/process.go`、`process_unix.go`、
  `process_windows.go`、`process_windows_test.go`、`terminal.go`、
  `raw_other.go`、`terminal_test.go`、`terminal_unix_test.go`。

## 10. Windows：默认命令回退到不存在的 /bin/sh

- **现象**：不带命令启动 `gotty serve` 时，Windows 上默认会话命令被解析成
  `/bin/sh`（`$SHELL` 未设置时），该路径在 Windows 上不存在。
- **根因**：`cmd/serve.go` 的回退链是 `args[0]` → `$SHELL` → `/bin/sh`，是 unix
  假设；Windows 没有登录 shell 约定，等价物是 `COMSPEC`（cmd.exe）。
- **修复**：抽出 `fallbackShell()`（`cmd/shell_windows.go` / `cmd/shell_other.go`）：
  Windows 走 `$SHELL`（尊重 Git Bash / MSYS）→ `COMSPEC` →
  `C:\Windows\System32\cmd.exe`；其他平台保持 `$SHELL` → `/bin/sh` 不变。

## 11. 跨平台单测的 Windows 缺口（部分已补）

- **现象/根因**：CI 只有 `ubuntu-latest`（`.github/workflows/ci.yml`），**没有
  Windows 作业**，所以上面的整体性断裂长期无人发现；同时现有单测大量硬编码
  `/bin/sh` 与 POSIX 权限语义，在 Windows 上本就失败，无法充当 Windows 回归。
- **本轮已补**：`internal/terminal/process_windows_test.go` —— 三个真机用例：
  交互式 cmd.exe 的收发/resize/退出与通道断开、非零退出码的 `*exec.ExitError`
  契约、`Close` 终止进程不留孤儿；另把 bash `PROMPT_COMMAND` 复位断言移到
  `terminal_unix_test.go`（`//go:build unix`，它验的是 termios 语义）。
- **同轮未完、随后已收尾**：`internal/capture`（11 例，硬编码 `/bin/sh` 与 POSIX
  脚本）、`internal/update`（2 例，chmod 0755 与"只读目录必须失败"是 POSIX 语义）
  当时在 Windows 上仍红 —— 已在 §14 按平台镜像分文件处理完，这两个包在 Windows
  上转绿。
- **仍未做（建议）**：CI 仍只有 `ubuntu-latest`，**没有 Windows 作业**，这正是
  Windows 断裂长期无人发现的原因。§14 已完成其前置（测试可移植性），但现在加作业
  还要先解决 Makefile 是 POSIX 的、`go:embed all:static` 要求编译期存在前端产物
  这两点。

## 12. scripts/*.ps1 的中文在 Windows PowerShell 5.1 下乱码（且可致整脚本解析失败）

- **现象**：在只装了 Windows PowerShell 5.1 的机器上运行 `scripts/install.ps1`，
  脚本自己打印的中文全是乱码（`一键安装脚本` → `涓€閿畨瑁呰剼鏈?`）；本轮新增的
  构建脚本更严重——**直接语法报错** `Unexpected token '}' in expression or
  statement.`，根本无法执行。
- **根因**：两个 `.ps1` 都是**无 BOM 的 UTF-8**。PowerShell 5.1 对无 BOM 的
  `.ps1` 按系统 ANSI 代码页解码（本机为 GBK），UTF-8 中文被拆成错误字节序列：
  轻则满屏乱码，重则某些字节对被解成引号/括号等分隔符，让整个脚本无法解析。
  PowerShell 7 默认按 UTF-8 读取，因此该问题只在 5.1 上暴露。
- **修复**：两个 `.ps1` 均改为 **UTF-8 with BOM + CRLF**（带 BOM 时 5.1 即按
  UTF-8 解码）；新增的 `scripts/build-install.ps1` 直接以此编码落盘。
  `scripts/install.sh` **不能**加 BOM（会破坏 `#!/bin/sh` shebang），它不受影响。
- **涉及文件**：`scripts/install.ps1`（仅编码）、`scripts/build-install.ps1`。

## 13. `capture --marker` 漏检：滑动窗口在查找之前截断（平台无关的真实缺陷）

- **现象**：输出里明明出现了 marker，`gotty capture --marker` 却不停，一路等到
  进程自己退出（Windows + `cmd.exe` 上必现）。
- **根因**：`internal/capture/driver.go` 的 marker 查找把顺序写反了 —— 先把累积
  缓冲截到 `len(marker)+31` 的滑动窗口，**再**在其中查找：

  ```go
  tail = append(tail, buf[:n]...)           // 本块里含 marker
  if len(tail) > len(marker)+markerSlack {  // ← 先截断
      tail = tail[len(tail)-len(marker)-markerSlack:]
  }
  if bytes.Index(tail, marker) >= 0 { … }   // ← 只剩尾部 33 字节
  ```

  于是一次读里 marker 之后只要跟了超过 `len(marker)+31` 字节，marker 就被截掉。
  ConPTY 会在文本后追加 `ESC]0;<title>BEL` 与光标序列（约 40+ 字节），正好触发：
  实测 `echo abc` 配 marker `ab`，同一读块里 `abc` 就在其中，却被丢弃。
  unix 上文本后面跟的字节少，所以从未暴露 —— **这是平台无关的缺陷**，只是 Windows
  把它踩响了。
- **修复**：改成"先查后截"。本块整体（拼上一次读的末尾重叠区）先参与查找，查找
  之后再只保留 `len(marker)-1` 字节作为下一次的重叠区（跨块 marker 至多跨
  `len(marker)-1` 字节，这个长度是充分必要的），并删掉已无意义的 `markerSlack`。
- **验证**：`internal/capture` 的 `TestRunMarker` / `TestRunMarkerAcrossChunks`
  在 Windows 上由红转绿；产线级 A/B 见"验证方式"。

## 14. Windows 测试可移植性收尾，以及随之暴露的两处 Windows 可用性缺陷

§11 里"仍红"的两个包按**平台镜像分文件**处理（沿用本批已确立的
`process_windows_test.go` / `terminal_unix_test.go` 惯例）：

- **`internal/capture`**（原 11 例红）
  - `driver_unix_test.go`（`//go:build unix`）：全部 `sh -c` 用例原样保留；
  - `driver_windows_test.go`（`//go:build windows`）：用 `cmd /c` 复刻同一批契约
    （退出文本、退出码、尺寸透传、超时、静默、marker、宽输出折行）；
  - `driver_test.go`：只留平台中立用例（命令不存在、vim/htop，后两者装了就跳过）。
  - 两处**无法镜像**、已在用例里写明理由：① cmd 内置命令没有亚秒等待（`timeout`
    依赖控制台、`ping -n` 只能整秒），静默与跨块 marker 改用秒级间隔并相应放大
    `WaitMs` —— 移植的是契约，不是删用例；② 查询应答见 §14.3。
- **`internal/update`**：可移植断言（内容被替换、无临时残留、目录不存在的失败路径）
  留在 `update_test.go`；POSIX 权限语义（0755 保留、"只读目录必须失败"）移入
  `update_unix_test.go`（`//go:build unix`）。**故意不写 Windows 等价断言**：Windows
  上 `os.Stat` 报 0666、`os.Chmod` 对目录是 no-op（造不出只读目录），且 `os.Rename`
  覆盖只读文件本身就可能 ACCESS_DENIED —— 那种断言会因无关原因失败，反而给出假绿。

### 14.1 Windows 上"命令不存在"的友好提示失效（已修）

- **现象**：`gotty capture -- definitely-not-a-command` 在 Windows 上只回
  `…The system cannot find the file specified.`，没有"用 shell 包一层"的提示。
- **根因**：`capture/driver.go` 的提示分支判 `errors.Is(err, exec.ErrNotFound)`。
  unix 侧由 `exec.LookPath` 提供该错误；Windows 侧 ConPTY 的 `CreateProcess` 直接
  返回 Win32 errno，匹配不上，分支**永不触发**（提示文案本身也硬编码了 `sh -c`）。
- **修复**：`process_windows.go` 把 `ERROR_FILE_NOT_FOUND` /
  `ERROR_PATH_NOT_FOUND` 用 `fmt.Errorf("…%w (%w)", exec.ErrNotFound, err)` 归类
  （多重 `%w` 保留原始 errno 细节）；提示片段抽成
  `capture/shell_hint_{unix,windows}.go`，Windows 上为 `cmd /c "..."`。

### 14.2 ConPTY 缺失时 panic 而非报错（已修）

- **根因**：x/sys 的过程解析是惰性的，`LazyProc.Addr()` 找不到过程时**直接
  panic**（`dll_windows.go` 的 `mustFind`）。所以在没有 ConPTY 导出的 Windows
  （10 1809 之前）上，`windows.CreatePseudoConsole` 会让进程**崩溃**，原来那条
  `errors.Is(err, windows.ERROR_PROC_NOT_FOUND)` 分支其实是**死代码**。
- **修复**：`process_windows.go` 显式 `Find()` 三个 ConPTY 入口
  （`Create`/`Resize`/`ClosePseudoConsole`），缺失时返回清晰的
  "requires Windows 10 1809 or newer" 错误，而不是 panic。

### 14.3 Windows 上查询应答由 conhost 负责（实测结论，不是覆盖缺口）

交接文档一度把 `TestRunAnswersQueriesBack` 当作 Windows 覆盖缺口；实测（临时探针，
用完即删）表明**它不是缺口**：

- 子进程（测试二进制 re-exec + `terminal.WithEnv`）发出 `ESC[6n` 后，该查询
  **从未到达本进程** —— 读到的原始字节里不含 `ESC[6n`；
- 但 conhost 把它**自己应答**了：原始字节里出现 `^[[1;1R`（应答被回显，所以以
  caret 转义形式可见），而本进程的仿真器**没有**产生任何应答；
- 输出流没有卡住（`BEFORE` / 空行 / `AFTER` 都正常渲染），所以查询型 TUI 不会因为
  "没人应答"而挂起。

结论：Windows 上控制台查询由 conhost 消费并应答，gotty 的应答链路对 DSR 不可达、
也无需可达。给它写 Windows 版等价用例只会测到 **conhost** 的行为。因此
`TestRunAnswersQueriesBack` 保留 unix-only，并新增
`TestConPTYConsoleHostAnswersQueriesItself`（`//go:build windows`）钉住这条**平台
契约**：子进程确实拿到格式正确的 `ESC[…R`，且查询从未到达本进程 —— 将来 Windows
若改为转发查询，该用例会红，提醒必须给应答链路补 Windows 用例。

## 15. Windows 上 `gotty self update` 必然失败（已修）

- **现象**：Windows 上 `gotty self update` 永远装不上，报
  `the update was verified but could not be installed … Access is denied
  (the old binary was left intact)`；旧二进制还在，功能等于不可用。
- **根因**：Windows 会映射正在运行进程的镜像，该文件在进程存活期间**既不能被覆盖也
  不能被删除**：`os.Rename(tmp, 运行中的 exe)` 返回 `ERROR_ACCESS_DENIED`。而
  `self update` 替换的恰恰是**它自己正在运行的那个二进制**
  （`update.go` → `AtomicReplace(exe, bin)`）。实测四步（临时探针，用完即删）：

  | 操作 | 结果 |
  |---|---|
  | `rename(tmp → 运行中的 exe)` | **Access is denied**（= 修复前的现状） |
  | `rename(运行中的 exe → 让位名)` | ✅ 成功 |
  | `rename(tmp → 腾出的名字)` | ✅ 成功（新二进制就位，旧进程照常运行） |
  | `remove(让位文件)`（进程仍在跑） | Access is denied → 只能留给下次回收 |

  unix 不受影响：POSIX rename 可以覆盖运行中的可执行文件，旧 inode 由运行中的进程
  继续持有 —— 所以这个缺陷只在 Windows 上暴露。
- **修复**：把"落地"这一步按平台拆开（`replaceFile`）：
  - `replace_unix.go`：`os.Rename` 即可，unix 侧无需特殊处理；
  - `replace_windows.go`：先试直接替换（目标没在运行时与 unix 一致）；失败则
    **先把运行中的旧 exe 改名为 `*.gotty-old-<pid>` 让位**，再装上新的；第二步失败
    会回滚，旧二进制绝不会以让位名失踪。让位文件此刻删不掉是正常的，由下一次运行
    `reapStaleBinaries` 回收。
  - 与本仓 `scripts/build-install.ps1` 已验证过的"旧二进制改名让位"同一套做法。
- **顺带修**：`self update` 的提示文案原本硬编码 `install.sh | sh` 与
  `systemctl --user restart gotty`，这两样在 Windows 上都不存在；抽成
  `platform_hint_{unix,windows}.go`，Windows 上给 `install.ps1` 与
  "stop and start gotty again"。
- **回归测试**：`replace_windows_test.go` 的 `TestAtomicReplaceRunningExecutable` 与
  `selfupdate_windows_test.go` 的 `TestSelfUpdateReplacesRunningExecutable`（后者跑完整
  `Run` 路径：资产选择 + 下载 + 校验 + 替换，用本地假 release，不需要联网）。
  **实测：临时去掉让位回退后这两条都会红**，报的正是线上那条 `Access is denied`
  —— 它们确实钉住了这个缺陷，不是摆设。

## 16. Windows 上 `build\gotty` 与 `build\gotty.exe` 并存 → 启动到过期二进制（已修）

- **现象**：用户报"还是没法创建终端"，服务端日志：
  `Failed to create session: failed to create terminal: failed to start command
  \`D:\Program Files (x86)\Git\usr\bin\bash.exe\`: unsupported`。
- **根因**：**不是代码缺陷，而是启动到了修复之前的旧二进制**。两条判据：
  1. `unsupported` 是 `creack/pty` 在 Windows 上的桩错误（就是 §9 的根因本身），
     带 ConPTY 实现的新代码不可能再吐出这个字符串；
  2. 日志里是 **"using the login shell"**，而 §10 的修复已把文案改成
     "using the fallback shell" —— 说明该进程的代码早于 `cmd/serve.go` 的改动。
  运行中的进程指向 `build\gotty`（**无扩展名**，mtime 00:01:57），比整批修复
  （00:09 之后才写的文件）还早。
- **为什么会有两个名字**：`Makefile` 的 `build` 目标输出 `$(OUTPUT_DIR)/gotty`（无扩展名），
  而 `scripts/build-install.ps1` 输出 `build\gotty.exe`、发布资产是
  `gotty-windows-amd64.exe`。Windows 上两者只差三个字符，**极易启动到过期的那个**。
- **修复**：`Makefile` 在 Windows 上给本地构建产物补 `.exe`
  （`ifeq ($(OS),Windows_NT)` → `EXT := .exe`），与 install 脚本及发布资产命名统一。
  ubuntu 上 `OS != Windows_NT`，`EXT` 为空，行为完全不变 —— 已用 `make -n build` 在两种
  环境下分别验证：Windows 输出 `-o ./build/gotty.exe`，未设 `OS` 时输出 `-o ./build/gotty`。
- **残留提醒**：事故当时正在运行的那个 `build\gotty` 是**新构建**（01:31:57，已含修复），
  功能正常；但它是旧命名下的产物，**停掉服务后应手动删除**，否则下次 `make build` 产出
  `build\gotty.exe` 之后，又会回到"一新一旧两个名字"的局面。

## 验证方式（本轮）

- 真机（Windows 11 · go1.26.6）：`go test ./internal/terminal/ -v` 全绿
  （含 3 个 ConPTY 真机用例）；`go test ./...` 中 `internal/api`、`internal/session`、
  `cmd` 已由红转绿（改前 `internal/api` 因建不起会话而失败）。
- 交叉编译：`GOOS=linux|darwin|freebsd` 的 `go build ./...` 与 `go vet ./...`
  全部通过（unix 侧文件与 `terminal_unix_test.go` 参与类型检查）；Windows 侧
  `go build`/`go vet` 通过，新增文件 `gofmt` 无差异。
- 端到端（真实服务器 + 真实 PTY，非 mock）：`gotty serve` →
  `POST /api/sessions`（不带 command，回退到 `$SHELL` = Git Bash）→ **201**；
  `POST /api/sessions/{id}/keys {"input":"echo ...\r"}` → `GET /screen` 回读到
  命令行回显与命令输出；`POST /resize {"width":100,"height":30}` → 200；
  `DELETE /api/sessions/{id}` → 204 且会话进程 pid 确实消失（无孤儿）。
- `gotty capture --format json -- cmd.exe /c "exit 7"` 输出 `"exit_code": 7`，
  证明 `*exec.ExitError` 链路端到端正确。
- 改前基线（同一台机器）：`POST /api/sessions` 一律 500 `unsupported`；
  `internal/capture` 12 例、`internal/api` 1 例、`internal/terminal` 1 例失败。
- 本地构建安装脚本（`scripts/build-install.ps1`，Windows PowerShell 5.1 真机）：
  连续执行 4 次验证四条路径——① 常规安装（自动找到未在 PATH 中的 Go、复用内嵌
  前端产物不联网、`git describe` 派生版本、装到 `%USERPROFILE%\.local\bin`、
  自检 `version --json`）；② **服务正在运行时重装**（旧二进制被改名让位，
  运行中的服务不受影响）；③ `-Serve -Port <占用端口>` 正确提示端口占用且
  不误开浏览器；④ 服务停止后再跑一次，让位文件被自动回收。
  安装结果实测：`gotty` 在新终端可直接解析，`gotty serve` 返回 200、
  `/main.js` 581KB 正常、会话创建 201、输入/读屏往返正常。

### 追加验证（§13 / §14；同一台 Windows 11 · go1.26.6 真机）

- `go test ./internal/capture/ ./internal/update/ -count=1` —— 由红转绿
  （改前 capture 11 例红、update 2 例红）。
- `go test ./... -count=1`：**6 个包全绿**（`cmd`、`internal/api`、`internal/capture`、
  `internal/session`、`internal/terminal`、`internal/update`）。
  期间曾在受限的文件沙箱下看到 `internal/api` 6 例红，**已查明那不是产品缺陷**：该沙箱
  禁止 MSYS2 建立共享内存映射，Git Bash 的 `sh.exe`/`cat.exe` 一启动就
  `*** fatal error - CreateFileMapping S-1-5-21-…, Win32 error 5. Terminating.`
  （屏幕上那串 `gdi32full.dll` / `ucrtbase.dll` 正是它 fatal error dump 的
  `Loaded modules:`），而这些用例都跑 `sh -c` / `cat`，于是必然失败；放开限制后**同一条
  命令立刻全绿**，诊断得到证实。注意 `internal/api` 的用例依赖 Git Bash（GitHub 的
  windows runner 自带；没有该工具链的 Windows 机器上会红），与 §14 同源，补 Windows CI
  时可一并处理。
- `internal/update` 的 Windows 专项：`go test ./internal/update/ -run RunningExecutable -v`
  两条均绿；**临时禁用让位回退后两条均红**（报 `Access is denied`），证明回归测试有效。
- 交叉类型检查：`GOOS=linux|darwin|freebsd go vet ./...` 全部通过。
- **unix 侧测试也已在 Linux 上真实跑通**（此前几轮只做到类型检查）：在 Windows 上交叉编译出
  linux/amd64 测试二进制，再在 WSL2（Ubuntu）里执行。6 个包 **全部 exit=0、零失败**
  （capture 100 / terminal 11 / update 13 / session 35 / cmd 2 / api 22 个用例通过）。
  `internal/update` 必须以**非 root 用户**运行 —— `TestAtomicReplaceFailsOnReadOnlyDir`
  依赖权限位，root 会忽略它，以 root 跑就是假绿。点名复跑确认下列 unix-only 用例全部 PASS：
  `TestAtomicReplacePreservesMode`、`TestAtomicReplaceFailsOnReadOnlyDir`（§14 移出）、
  `TestRunExitText` / `TestRunQuiet` / `TestRunWideOutput` / `TestRunMarkerAcrossChunks`
  （§14 移入 `driver_unix_test.go`）、`TestRunAnswersQueriesBack`（§14.3 的 unix-only 用例）、
  `TestTerminal_ResetSequenceOnPrompt`。
  **这同时证明 §13 对共享 `driver.go` 的改动没有破坏 unix 语义**，也表明 ubuntu CI 作业
  应当是绿的（同一平台、同一批用例；差异仅在 CI 用 `make test`，即 vet+fmt+go test）。
  复现方式：Windows 侧 `go test -c` 交叉编译出 linux/amd64 测试二进制，再用
  `.tmp/wsl-run-unix-tests.sh` 在 WSL 里执行（ASCII-only，WSL 内**不需要装 Go**）。
  环境注记：本机 `proxy.golang.org` 不可达、`go.dev` 的 tarball 下载也会被 TLS 拦，
  所以"在 WSL 里装一套 Go"这条路走不通，交叉编译才是可行解。
- 格式：`gofmt` 对本轮改动的 10 个文件按 LF 规范化后**无差异**。（工作树是 CRLF，
  直接 `gofmt -l .` 会列出几乎所有文件，那是环境假象不是格式问题。）
- 端到端（真实服务器 + 真实 ConPTY，非 mock；清掉 `SHELL` 以模拟普通 Windows 环境，
  默认命令回退到 `COMSPEC`）：**15/15 通过** —— `GET /` 200、`/main.js` 正常、
  `POST /api/sessions` 201 且 `command` 为 `C:\Windows\system32\cmd.exe`、
  `keys` 输入回显进 `/screen` 且命令输出独占一行、`resize` 到 100x30 后在
  `?format=json` 的 `cols/rows` 上确实生效、`DELETE` 204 且会话进程确实消失（无孤儿）。
- marker 缺陷的产线级 A/B（`--wait-ms 0` 排除静默停止干扰，命令为
  `echo a-MARKER-b & ping -n 8 …`）：marker 命中 → 30ms 停止且进程仍在运行（即被
  主动中止）；marker 不存在 → 跑满 7116ms 直到进程退出。
- 提示修复：`gotty capture -- definitely-not-a-command-xyz` 现在输出
  `command "…" not found: use \`gotty capture -- cmd /c "..."\` for shell syntax (…: executable file not found in %PATH% (The system cannot find the file specified.))`。
