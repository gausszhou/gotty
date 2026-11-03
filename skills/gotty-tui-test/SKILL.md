---
name: gotty-tui-test
description: 为交互式终端程序(TUI)编写自动化验收测试时使用:启动隔离的 GoTTY 服务、用 gotty run 创建会话、驱动按键/鼠标、用 wait + screen 断言,失败时留现场并清理。
---

# GoTTY TUI 验收测试

目标:给一个交互式终端程序(TUI)写**可提交、可在 CI 重复运行**的 bash 验收脚本。
GoTTY 把程序跑在 PTY 里,`gotty` CLI 负责读屏、等文字、发按键/鼠标。

## 何时用 / 何时不用

**用**:
- 被测对象是 TUI:自研 bubbletea/ratatui 界面、vim/htop/mc 类程序、安装器对话框;
- 验收要看**屏幕内容**、按键响应、鼠标点击、备用屏切换。

**不用**:
- 非交互 CLI —— 直接用普通子进程断言 stdout / 退出码;
- 只需一次性抓输出 —— `gotty capture` 更合适;
- 纯 HTTP / 库逻辑 —— 不需要终端。

## 前置

- `bash`、`jq`、`gotty` 在 PATH;Linux/macOS 直接跑,Windows 用 Git Bash 或 WSL。
- 没装 gotty:下载与本 skill 同版本的 `v0.0.3` 原始二进制(换文件名即换平台):

  ```text
  https://github.com/gausszhou/gotty/releases/download/v0.0.3/gotty-linux-amd64
  https://github.com/gausszhou/gotty/releases/download/v0.0.3/gotty-linux-arm64
  https://github.com/gausszhou/gotty/releases/download/v0.0.3/gotty-darwin-amd64
  https://github.com/gausszhou/gotty/releases/download/v0.0.3/gotty-darwin-arm64
  ```

  同一 release 里有 `sha256sums.txt` 可校验;`chmod +x` 后放进 PATH。
- 命令全集不在本文:**先跑 `gotty usage`**;单个命令看 `gotty <cmd> --help`。

## 核心模型(30 秒)

- `gotty serve` 为每个会话维护:一个 PTY + 一块 VT 屏幕镜像(agent 读的就是它)。
- 服务端**不保存会话清单**:会话 id 由调用方记住。`gotty run` 建会话并打印 id,
  `gotty kill` 销毁它。
- 所有命令都是薄封装:`--server` 选服务、`--session` 选会话;也认 `$GOTTY_SERVER` / `$GOTTY_SESSION`。
- 输出:TTY 上是人读文本,管道里是单行 JSON;`screen --format text` 例外(永远纯文本)。

## 标准骨架

复制下面整段,改顶部三个变量即可。它包含:隔离服务、建会话、等首帧、驱动、断言、失败取证、清理。

```bash
# <!-- harness:start -->
#!/usr/bin/env bash
set -euo pipefail

# ===== 被测程序:改这里 =====
# TUI_CMD 按空格分词;路径含空格时写一个包装脚本
read -r -a TUI_CMD <<< "${TUI_CMD:-./your-tui}"
READY_REGEX="${READY_REGEX:-Ready}"   # 启动完成的标志
DONE_REGEX="${DONE_REGEX:-Done}"      # 一次操作后的标志

GOTTY="${GOTTY:-gotty}"
COLS="${COLS:-120}"
ROWS="${ROWS:-40}"

WORK="$(mktemp -d)"
SERVER_PID=""
SESSION=""

dump() {
  echo "---- screen ----" >&2
  "$GOTTY" screen --format text >&2 || true
  echo "---- scrollback (last 200) ----" >&2
  "$GOTTY" scrollback --lines 200 --format json 2>/dev/null \
    | jq -r 'if .alternate_screen then "(alternate screen: no history)" else .text end' >&2 || true
  echo "---- server log (tail 50) ----" >&2
  tail -n 50 "$WORK/server.err" >&2 || true
  echo "---- session ----" >&2
  "$GOTTY" status --format json >&2 || true
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "$SESSION" ]]; then
    "$GOTTY" kill >/dev/null 2>&1 \
      || "$GOTTY" signal SIGKILL >/dev/null 2>&1 || true
  fi
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    for _ in $(seq 1 50); do
      kill -0 "$SERVER_PID" 2>/dev/null || break
      sleep 0.1
    done
    kill -9 "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
  exit "$status"
}
trap cleanup EXIT INT TERM

fail() { echo "FAIL: $*" >&2; dump; exit 1; }

# 1. 隔离服务:只绑本机、随机端口、状态全在临时目录
"$GOTTY" serve --address 127.0.0.1 --port 0 \
  --session-file "$WORK/sessions.json" \
  --title-file "$WORK/title.json" \
  --log-file "$WORK/server.log" \
  >"$WORK/server.out" 2>"$WORK/server.err" &
SERVER_PID=$!

URL=""
for _ in $(seq 1 100); do
  URL="$(sed -n 's/.*listening at: \(http[^ ]*\).*/\1/p' "$WORK/server.err" | head -n 1)"
  [[ -n "$URL" ]] && break
  sleep 0.05
done
[[ -n "$URL" ]] || fail "server did not report a listening URL"
export GOTTY_SERVER="$URL"

# 2. 建会话;run 只打印 id(重试覆盖 listen 与 serve 之间的微窗口)
SESSION=""
for _ in $(seq 1 100); do
  if SESSION="$("$GOTTY" run --width "$COLS" --height "$ROWS" -- "${TUI_CMD[@]}" 2>"$WORK/run.err")"; then
    [[ -n "$SESSION" ]] && break
  fi
  sleep 0.05
done
[[ -n "$SESSION" ]] || { cat "$WORK/run.err" >&2 2>/dev/null || true; fail "gotty run printed no session id"; }
export GOTTY_SESSION="$SESSION"

# 3. 等首帧:用程序自己的启动标志,不要 sleep
"$GOTTY" wait --regex "$READY_REGEX" --timeout-ms 10000 --format json \
  | jq -e '.matched' >/dev/null || fail "TUI did not reach /$READY_REGEX/"

# 4. 驱动 + 断言
"$GOTTY" press Enter >/dev/null
"$GOTTY" wait --regex "$DONE_REGEX" --timeout-ms 5000 --format json \
  | jq -e '.matched' >/dev/null || fail "TUI did not show /$DONE_REGEX/ after Enter"
"$GOTTY" screen --format text | grep -Fq "$DONE_REGEX" \
  || fail "screen does not contain '$DONE_REGEX'"

# 5. 收尾:先让程序自己退出;残留进程由 cleanup 的 kill/SIGKILL 兜底
"$GOTTY" press q >/dev/null

echo "PASS"
# <!-- harness:end -->
```

模板要点:

- `--port 0`:让内核选端口,从日志解析真实地址,避免端口冲突与竞态。
- `--session-file` / `--title-file` / `--log-file` 全在 `mktemp -d`,**绝不碰 `~/.gotty`**。
- 只绑 `127.0.0.1`;测试需要可写,所以保持 `--permit-write` 默认 true。
- `trap` 只杀自己启动的 PID;先 `gotty kill` 销毁被测会话(失败再 `signal SIGKILL`
  兜底),避免孤儿。
- 所有等待都有超时;每个失败都走 `fail` → `dump`。

## 断言策略

- **首选文本 + 正则**:`gotty wait --regex`,再用 `gotty screen --format text | grep -F`。
- 只有文本表达不了的结构才用 `--format json` + `jq`,例如光标位置:

  ```bash
  gotty screen --format json | jq -e '.cursor.row == 5'
  ```

- **PNG 只作失败证据**,不要拿像素做断言(字体/平台差异会让它系统性 flaky)。

## 同步规则(最容易做错的地方)

- **禁止裸 `sleep` 等界面**;启动竞态用 50ms 级短轮询。
- `gotty wait` **超时不是错误**:退出码仍是 0。必须把结果转成失败:

  ```bash
  gotty wait --regex 'Ready' --timeout-ms 10000 --format json | jq -e '.matched'
  ```

- “画面静止”用 `--quiet-ms`,同样用 `jq -e '.quiet'` 判断。
- 等待之后再读屏做最终断言,不要用一次读屏碰运气。

## 真实坑(都踩过)

1. **备用屏没有历史**:vim/htop 下 `scrollback` 返回 `alternate_screen: true` 且无行;只能断言当前屏。
2. **没有会话清单**:别猜 id;`gotty run` 的输出就是 id。
3. **`--` 不是可选礼貌**:`gotty run ./tui --help` 会把 `--help` 吞成 gotty 的;写 `gotty run -- ./tui --help`。
4. **多行文本必须用 `paste`**:`type 'a\nb'` 会让 shell 逐行执行;`paste` 在程序开启 `?2004` 时自动包括号粘贴。
5. **`--permit-write=false`**:`type/press/paste/mouse` 全部 403;测试服务器别关它。
6. **`--mirror=false`**:`screen/wait/mouse/cursor/scrollback` 全部 503;别关镜像。
7. **鼠标要程序先开启**:应用没开鼠标模式时点击返回 409;只有 `--force` 才强发。
8. **会话会因空闲被回收**:默认 900s 没有客户端附着即销毁;长测试要么保持节奏,要么 serve 时调大 `--timeout`。
9. **固定尺寸**:不传 `--width/--height` 时尺寸随客户端变化,断言列号/换行会飘;测试里显式传。
10. **退出后会话很快消失**:程序退出后约 1s 内会话被清扫,之后的 `status`/`screen` 会 404;退出码要在退出前取,或由被测程序自己写到文件里。

## 失败取证

模板的 `dump()` 在失败时打印:

- 最后一次 `screen --format text`;
- 最近 200 行 scrollback(备用屏则注明);
- 服务端日志尾 50 行;
- 会话状态 JSON。

需要图片时用 `gotty screen --format png --out evidence.png` 另存,**不要**把它当断言。

## 完成标准(必须做到)

1. 测试**实际跑绿**——不要交付没运行过的脚本。
2. 把某个断言故意改错,确认测试**会红**,且失败现场真的打印出来。
3. 测试能在干净 shell 里独立运行(不依赖你交互式起的服务),退出后无残留 gotty / 被测进程。

## 更多

- 命令与键名速查:`gotty usage`
- 按标签点击:`gotty mouse click --on-text OK`
- 只读观察(不抢占真人会话):`gotty monitor`
- 一次性抓屏:`gotty capture`
