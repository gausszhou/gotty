# GoTTY

GoTTY 是一个多会话终端共享服务:浏览器通过 REST 创建会话、通过 WebSocket 附着到会话的 PTY 上,并用顶部页签管理多个并行终端。本上下文覆盖"会话身份、清单、记录与存活"这一组概念——它们决定了会话如何被命名、被哪个设备记住、以及销毁后能否复活。

## 语言

**会话（Session）**:
服务端注册表中一个正在运行的 PTY 进程及其状态(StateIdle/StateRunning/StateDestroyed)。由会话 id 唯一标识。
_避免使用_: 终端、进程、连接

**会话 id（Session ID）**:
16 位 base36(`[0-9a-z]`) 字符串,唯一标识一个会话。**由客户端生成**(`crypto.getRandomValues`);旧客户端不传 id 时由服务端生成。同 id = 同一会话身份,不同设备各用各的 id 互不抢占。
_避免使用_: 会话号、标识符、URL 参数

**会话记录（Session Record）**:
服务端 FileStore 按 id 持久化的元数据(command/args/title/run_count)。服务端**不保存会话列表**,只按 id 保留记录,用于会话复活。
_避免使用_: 会话历史、历史记录(旧语义:服务端全量列表)

**会话清单（Session Manifest）**:
单个设备 localStorage(`gotty.sessions`)中保存的本地会话列表,是"这台设备上有哪些会话"的唯一事实来源。清单条目含 id/title/command/args/createdAt/lastSeen。服务端不感知清单。**清单只保留当前存活的会话**:服务端无存活记录的条目在 status 轮询时自动移出(前端已无"历史"入口)。
_避免使用_: 会话列表、服务端列表

**存活（Alive）**:
会话当前在 Manager 注册表中(PTY 在运行、可附着)。清单中存活者显示为页签;仅存活的条目会保留在清单中。

**复活（Resurrect）**:
凭会话记录用记录的 command/args 重建同 id 会话,`run_count+1`。这是"同 id 再创建/刷新恢复"的服务端机制(API 级别;前端当前没有显式重跑入口,销毁即从清单移除)。

**幂等创建（Idempotent create）**:
`POST /api/sessions` 携带已存活 id 时,返回现有会话而不新建;携带有记录的 id 时触发复活;无 id 时服务端生成(兼容旧客户端)。

**抢占（Preemption）**:
同 id 的新客户端附着立刻踢掉旧客户端(旧连接收 1013)。不同 id 的附着互不影响——这是"多设备各用各 id"的基础。

**空闲淘汰（Idle expiry）**:
无客户端附着超过 `--timeout`(默认 900s)即销毁 PTY;会话记录保留,可凭原 id 复活。
_避免使用_: 超时关闭、服务关停