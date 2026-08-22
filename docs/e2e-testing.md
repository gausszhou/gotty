# GoTTY 端到端测试指南

本仓库的端到端验证分两层,互为补充:

| 层 | 工具 | 覆盖 | 入口 |
|---|---|---|---|
| Go 集成测试 | `httptest` + 真实 PTY | REST 语义、WS 协议/抢占、清扫 | `go test ./internal/api/` |
| 浏览器级 e2e | headless Chrome CDP + Node 原生 WebSocket | 前端页面交互、清单、连接、渲染 | `scripts/e2e/*.mjs` |

模型不支持读图 —— **所有浏览器断言都用 DOM / computed style / 网络响应**,不做截图目检。

---

## 1. 环境事实(本机)

- 服务:`./build/gotty serve --port 8080`;日志 `/tmp/gotty-serve.out`;
  真实 PID 用 `pgrep -f 'build/gotty serv[e]'`(**不要** `cat /tmp/gotty-serve.pid`,那里记录的是外层 shell)
- headless Chrome 调试端口 **9222**(`--user-data-dir=/tmp/chrome-gotty`),
  用 `curl http://127.0.0.1:9222/json` 列出 targets
- Node v24 自带 WebSocket 与 fetch,CDP 脚本零依赖
- 构建:前端 `cd apps/web && npm run build` → 拷贝 `dist/*` 到 `internal/api/static/` → `go build`。任何前端改动都要走到这一步,页面才会更新

## 2. Go 集成测试(协议/语义层)

`internal/api/handlers_test.go` 用 `httptest.NewServer(server.setupHandlers())` 起真服务,
`session.NewManager` + 真实 PTY(测试命令用 `sh -c "sleep …"` / `cat`)覆盖:

- REST 生命周期、客户端 id 幂等创建(201→200)、复活(记录命令 + run_count+1)、
  status 批量、id 格式校验 400
- WS attach → 输入回显 → 断线重连;同 id 抢占(1013);清扫移除退出会话
- `internal/session/manager_test.go` 用 stub terminal 做确定性单测
  (CreateWithID 幂等/复活/Status/FileStore 旧数组迁移)

运行:`GOCACHE=/tmp/gocache go test ./...`

## 3. 浏览器级 e2e(CDP)

### 3.1 连接模板

```js
// GET /json 找 8080 的 page target,TODO: 注意可能有多个 target(用户浏览器)
const page = list.find(t => t.type === 'page' && t.url.startsWith('http://127.0.0.1:8080'))
const ws = new WebSocket(page.webSocketDebuggerUrl)
// send(method, params) → Promise;evalJS(expr) → 结果;waitFor(desc, cond) 条件轮询
```

完整辅助函数见 `scripts/e2e/manifest-flow.mjs` 开头(可直接复制)。

### 3.2 关键步骤与铁律

1. **`Page.reload { ignoreCache: true }`**(配 `Network.setCacheDisabled`)——
   `location.reload()` 会命中 HTTP 缓存,测到旧 bundle 是经典假阴性。
2. **`localStorage.clear()`** —— e2e 前必做:测试浏览器与用户浏览器同 id 会互相抢占(1013);
   且清单持久在 localStorage,不清会污染场景。
3. **条件轮询 `waitFor`,不要固定 `sleep` 猜时序** —— 轮询周期 2s、WS 连接异步、
   页面首次加载网络往返,固定延时脆弱。
4. **断言用返回值**:DOM 计数 / className / `getComputedStyle` / localStorage 内容 /
   直接 `fetch('/api/sessions/status')` 与前端同语义校验服务端事实。

### 3.3 已沉淀的场景脚本

| 脚本 | 场景 |
|---|---|
| `scripts/e2e/manifest-flow.mjs` | boot 自动创建 → ＋ 新建 → 销毁(清单条目移除)→ 轮询无僵尸 → 刷新恢复 |
| `scripts/e2e/resilience.mjs` | 空态卡片点击创建;会话已销毁 → TerminalPane 自动重建(服务端复活) |

临时探索脚本(未沉淀的)可放 `/tmp/cdp-*.mjs`,但**结论场景请沉淀回 `scripts/e2e/`**。

## 4. 本仓库验证过的事实(可作断言基准)

- 顶部栏 30px(`.tab-bar`),页签内圆点/标题/关闭按钮垂直中心均为 15px(三者对齐)——
  `getBoundingClientRect` 实测
- 连接成功 → 页签圆点 **即时**变绿(WS `@conn` 事件,不等 2s 轮询)
- 终端字体:WebGL 下由 canvas 按 xterm `options.fontFamily` 渲染;`.xterm` 根的
  computed 字体来自 xterm.css `.terminal` 规则(易误判!)。验证字体要读
  `document.querySelector('.xterm').style.fontFamily`(open 后代码显式设置)
- WebGL 渲染器下 **不存在 `.xterm-rows`**(只有 canvas);查内容用 `.xterm-helpers`
- 服务端 DELETE 曾永久卡死:根因 SIGHUP 被 nohup 继承为 SIG_IGN;现 `--close-timeout`
  默认 3s + 进程组 SIGKILL 兜底(见 ADR/feat-architecture §12.2)

## 5. 常见坑速查

- `pkill -f 'gotty serve'` 会匹配到命令行含该字符串的 bash 自身 → 用 `[x]` 技巧
  (`'gotty serv[e]'`)或把 kill 与启动拆到两条命令
- CDP 选择器 `.tab-actions` 同时匹配 `.tab-actions-left`(＋ 按钮)→ 取右侧用
  `.tab-actions:not(.tab-actions-left)`
- `WebSocket.binaryType` 必须 `arraybuffer`,blob 会被误判为非二进制帧
- 断言服务端 bundle 与 dist 一致:`md5sum internal/api/static/main.js apps/web/dist/main.js`