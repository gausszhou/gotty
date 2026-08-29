# docs 文档索引

本目录按**文档性质**分类组织,每个类别一个子目录;文档自身的状态
(已实现 / 待实施 / 搁置)标注在各文档头部状态行,不另建按状态的目录。

## 类别定义

| 目录 | 类别 | 内容 | 命名 |
|---|---|---|---|
| [`adr/`](adr/) | 决策记录 | 已确定的技术决策与理由(含被否决的备选) | `NNNN-主题.md`,编号递增 |
| [`design/`](design/) | 设计文档 | 架构 / 功能 / 协议的完整设计方案(已落地或部分落地) | 按主题命名 |
| [`feat/`](feat/) | 优化提案 | 待实施的优化改动(含对标调研结论) | `NNNN-主题.md` + `index.md` 索引 |
| [`fix/`](fix/) | 修复记录 | 已修复问题的现象 / 根因 / 修复 / 提交 | `index.md` 汇总 |
| [`guide/`](guide/) | 操作指南 | 流程性说明(测试、部署、协作) | 按主题命名 |

## 全部文档

### 决策记录 — `docs/adr/`

| 文档 | 内容 |
|---|---|
| [`0001-client-generated-session-ids.md`](adr/0001-client-generated-session-ids.md) | 会话 id 由客户端生成,设备本地清单成为列表的唯一来源 |

### 设计文档 — `docs/design/`

| 文档 | 内容 | 状态 |
|---|---|---|
| [`feat-architecture.md`](design/feat-architecture.md) | 现代化重构设计(多会话 / REST / 二进制 WS / 前端现代化) | 已落地 |
| [`capture-design.md`](design/capture-design.md) | capture 命令设计方案(M1–M4 里程碑) | M1–M3 已实现,M4 待实施 |
| [`ws-multiplex.md`](design/ws-multiplex.md) | WebSocket 连接复用与二进制路由协议 | 搁置 |

### 优化提案 — `docs/feat/`

| 文档 | 内容 | 状态 |
|---|---|---|
| [`index.md`](feat/index.md) | 优化改动索引(含实施顺序建议) | — |
| [`0001-agent-driving-api.md`](feat/0001-agent-driving-api.md) | Agent 可驱动 API:读屏 / 等待 / 输入注入 | 待实施 |
| [`0002-native-emulator-completeness.md`](feat/0002-native-emulator-completeness.md) | native 仿真器完整度:查询应答与缺失 CSI 对齐 | 待实施 |
| [`0003-distribution-self-update.md`](feat/0003-distribution-self-update.md) | 分发与自更新:install.sh + gotty self update | 待实施 |

### 修复记录 — `docs/fix/`

| 文档 | 内容 |
|---|---|
| [`index.md`](fix/index.md) | 已修复问题汇总(现象 / 根因 / 修复 / 提交) |

### 操作指南 — `docs/guide/`

| 文档 | 内容 |
|---|---|
| [`e2e-testing.md`](guide/e2e-testing.md) | 端到端测试指南(Go 集成测试 + 浏览器级 e2e 两层) |
