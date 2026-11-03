# 优化 8:TUI 验收测试 —— `gotty run` + `gotty-tui-test` skill

> 状态:**已实施(2026-11-01;`gotty kill` 于 2026-11-03 补齐)**
>
> 背景:GoTTY 已经能"驱动一个正在运行的会话"(0001/0004/0005),但**给任意 TUI 写
> 验收测试**这条路径还断在第一公里:
>
> 1. agent CLI 没有创建会话的命令——每个后续命令都需要 `--session`,而会话 id
>    只能靠 `curl -X POST /api/sessions` 拼 JSON 得到;
> 2. 没有一份"怎么把读屏/等待/按键组织成可提交测试"的说明。`gotty usage` 是命令
>    速查表,`AGENTS.md` 是仓库契约,都不是测试方法论;
> 3. 因此 agent 写出来的"验收"往往是:裸 `sleep`、恒真断言、`pkill gotty` 收尾。
>
> 本优化补两件事:**`gotty run`**(建会话并打印 id)与 **`gotty-tui-test` skill**
> (可安装给 agent 的测试流程 + 可执行模板)。

## 1. 现状盘点

| 项 | 现状 | 缺口 |
|---|---|---|
| 建会话 | `POST /api/sessions` 支持 `id/command/args/width/height/env/cwd/term/scrollback` | CLI 无对应命令;agent 只能手拼 `curl -d '{…}'` |
| 销毁会话 | `DELETE /api/sessions/{id}` | CLI 无对应命令;测试收尾只能 `signal` 或 `pkill` |
| agent 自描述 | `gotty usage`(命令速查)、根 `AGENTS.md`(契约) | 没有"测试怎么写"的流程知识;没有可按需加载的 skill |
| 会话清单 | 服务端不保存(`CONTEXT.md`) | 测试必须自己记住 `gotty run` 的输出 |
| 测试隔离 | 仓库 e2e 是浏览器侧 Node 脚本 | 没有 shell 侧的 TUI 验收范式;`~/.gotty` 与端口冲突没有统一处理 |

## 2. 设计

### 2.1 `gotty run` / `gotty kill`

- 形态:`gotty run [flags] -- <command> [args...]`,薄封装 `POST /api/sessions`。
- flags:`--id`、`--width`/`--height`(必须成对)、`--env K=V`(可重复)、`--cwd`、
  `--term`、`--scrollback`、`--server`、`--timeout`、`--json`。
- 命令之后的一切都属于命令:`SetInterspersed(false)` + 文档要求 `--`,避免
  `gotty run ./tui --help` 被 gotty 自己吃掉。
- 输出契约:**stdout 只有会话 id**(`ID=$(gotty run …)` 直接可用);`--json` 改为
  一条完整的会话记录 JSON。这是对"TTY 文本 / 管道 JSON"总约定的刻意例外,
  理由与 `screen --format text` 相同:捕获 id 才是主用法。
- 不传 command 时沿用服务端默认命令(`gotty serve` 的 shell),与 API 一致。
- 会话退出后约 1s 被清扫(`DestroyExpired`),所以 skill 明确:**退出码要在退出前
  取**,`q` 之后不要再轮询 `status`。
- 配套 **`gotty kill`**(`DELETE /api/sessions/{id}`,对齐 `tu kill`):stop 进程 +
  从注册表移除,一步收尾;晚到的 kill 得到 404 视为"已清理"。`run` + `kill` 把
  一个测试用例夹在中间。

### 2.2 `gotty skill`

- skill 源文件在仓库 `skills/gotty-tui-test/SKILL.md`,用 `go:embed` 进二进制,
  `gotty skill` 逐字节打印——与 `gotty usage` 同一套"自描述、不漂移"思路。
- `gotty skill --install DIR`:写 `DIR/SKILL.md`(目录不存在则创建;已存在需
  `--force`);**不猜** `~/.claude` / `~/.agents`,文档给两条常用路径。
- 不改 `install.sh`:安装二进制时偷偷往 agent 目录写文件是意外副作用。
- 升级漂移:不做 `self update` 自动刷新;skill 内钉住 `v0.0.3` 的 release 下载
  链接作为版本标记,发布时随 tag 更新。

### 2.3 skill 内容

- **触发边界窄**:任务明确是"给 TUI 写自动化验收/回归"时加载;一次性看屏、调试
  不加载(那属于 `gotty usage`)。
- **环境契约**:POSIX `bash` + `jq` + `gotty`;缺 gotty 时给钉版本下载链接;
  Windows 走 Git Bash/WSL,不做原生 PowerShell 模板。
- **内容 = 流程 + 模板 + 坑**:
  - 何时用 / 何时不用(非交互 CLI 用子进程;一次性抓屏用 `gotty capture`);
  - 完整 harness 模板(隔离服务、建会话、等首帧、驱动、断言、失败取证、清理);
  - 真实坑:`wait` 超时退出 0、备用屏无 scrollback、`--` 语义、`paste` vs `type`、
    `--permit-write`/`--mirror` 的 403/503、退出后 1s 清扫、固定尺寸。
- **不复制命令全集**:只写"先跑 `gotty usage`",避免第三份会腐烂的副本。
- **完成标准**:必须实际跑绿,并把某个断言故意改错确认会红——skill 写死
  "不要交付没跑过的测试"。

### 2.4 harness 契约(写进模板)

- 每个测试文件一个隔离服务器:`--address 127.0.0.1 --port 0`,从日志
  `HTTP server is listening at:` 解析真实端口;`--session-file`/`--title-file`/
  `--log-file` 全在 `mktemp -d`,绝不碰 `~/.gotty`。
- 每个用例一个会话;`trap` 清理顺序:先 `gotty kill` 销毁会话(失败再
  `signal SIGKILL` 兜底)→ 服务端 `TERM` → `KILL` → 删临时目录;只杀自己启动的
  PID,不 `pkill gotty`。
- 同步一律 `gotty wait --format json | jq -e '.matched'`(超时不是错误);
  断言以文本/正则为主,JSON 只做结构化断言,PNG 只作证据。
- 失败时 `dump()`:screen 文本、最近 200 行 scrollback(备用屏注明)、服务端日志
  尾 50 行、会话状态。

### 2.5 防腐验证

- `scripts/e2e/skill-smoke.sh`:
  1. `gotty skill` 输出与仓库文件逐字节一致;`--install` 写入同样内容;
  2. 用 `<!-- harness:start -->…<!-- harness:end -->` 标记从 SKILL.md 抽出模板,
     套 `scripts/e2e/fixture-tui`(打印 Ready → Enter 后 Done → `q` 退出)跑通;
  3. 用错误的 `READY_REGEX` 再跑一次,要求**非零退出**且打印 screen / 服务端日志。
- CI(ubuntu)在 `make build` 之后跑这个脚本;`jq` 由 runner 自带。

## 3. 涉及文件

| 文件 | 改动 |
|---|---|
| `cmd/run.go`(新) | `gotty run`:建会话、打印 id、`--json`、参数校验 |
| `cmd/run_test.go`(新) | 请求体、省略默认值、JSON 输出、`--` 透传、错误、注册位置 |
| `cmd/kill.go`(新) | `gotty kill`:DELETE 会话,`run` 的对称收尾 |
| `cmd/kill_test.go`(新) | DELETE 方法与路径、404 错误、缺 session、注册位置 |
| `cmd/skill.go`(新) | `gotty skill` / `--install` / `--force` |
| `cmd/skill_test.go`(新) | 打印 = 内嵌内容、安装、拒绝覆盖、强制覆盖、注册 |
| `cmd/agent.go`、`cmd/root.go` | 注册 `run`/`kill`(顶层 + `agent` 组)与 `skill` |
| `cmd/usage.go` | SETUP 增加 `ID=$(gotty run …)`,WRITE 增加 `gotty kill` |
| `skills/gotty-tui-test/SKILL.md`(新) | 中文 skill 正文 + harness 模板 + 坑 |
| `skills/gotty-tui-test/embed.go`(新) | `//go:embed SKILL.md` |
| `scripts/e2e/fixture-tui/main.go`(新) | 微型 TUI,供 smoke 用 |
| `scripts/e2e/skill-smoke.sh`(新) | 模板抽取 + 成功/失败两条路径 |
| `.github/workflows/ci.yml` | 增加 skill smoke 步骤 |
| `README.md`、`README.zh-CN.md`、`AGENTS.md` | `gotty run` / `gotty skill` / skill 安装 |
| `apps/docs/guide/usage.md` | 新命令、新选项与 skill 说明 |
| `docs/feat/0005-…` §6.3 | 勾掉"可选 skill"待办并指向本文 |

## 4. 测试与验收

- `go test ./cmd/`:run、kill 与 skill 的全部用例。
- `bash scripts/e2e/skill-smoke.sh ./build/gotty`:成功路径 PASS、失败路径非零且
  有 screen/日志证据。
- 手工:`gotty serve --port 0` → `ID=$(gotty run --width 80 --height 24 -- cmd …)`
  → `gotty screen --session "$ID"` → `gotty kill --session "$ID"`。
- 验收标准:
  1. `gotty run` 的 stdout 只有 id(`jq`/命令替换都成立);
  2. 模板在 Linux/macOS 与 Windows Git Bash 下都能跑通(CI 覆盖 Linux);
  3. skill 模板若断言错误必须红,不允许"永远绿";
  4. `gotty skill` 的输出与 `skills/gotty-tui-test/SKILL.md` 逐字节一致;
  5. `gotty kill` 之后会话不可再查询(404),`run` + `kill` 可反复执行。

## 5. 不做的事(范围外)

1. **声明式场景格式 / `gotty test` 运行器** —— 需要断言语法、重试、fixture、报告,
   是独立 feature;skill 只产出 shell 测试。
2. **PNG 像素断言** —— 字体/平台差异导致系统性 flaky;PNG 只作证据。
3. **原生 Windows PowerShell 模板** —— Git Bash/WSL 已够用,模板翻倍不划算。
4. **安装二进制时自动装 skill** —— 不猜 agent 目录;`install.sh` 不动。
5. **skill 版本自动刷新** —— 升级后重跑 `gotty skill --install` 即可。

## 6. 实施记录(2026-11-01,`gotty kill` 于 2026-11-03 补齐)

- `gotty run` 落地并注册到顶层与 `gotty agent run`;手工端到端验证:
  `gotty run` 返回 id、`screen` 读到首帧、`kill` 收尾。
- `gotty kill` 落地(DELETE 封装,对齐 `tu kill`),skill 的 `trap` 改为先 kill、
  失败再 `signal SIGKILL` 兜底。
- `gotty skill` 落地;skill 正文为中文,含钉 `v0.0.3` 的下载链接与可执行模板。
- `scripts/e2e/skill-smoke.sh` 在本机(Git Bash + jq 1.7.1)与 CI 步骤同时可用:
  成功路径 PASS,错误 `READY_REGEX` 路径按预期失败并打印证据。
- README / AGENTS / guide 的 `run`/`kill`/`skill`/新选项说明已同步。
