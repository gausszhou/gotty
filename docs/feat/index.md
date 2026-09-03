# 优化改动索引(docs/feat)

针对 [flipbit03/terminal-use](https://github.com/flipbit03/terminal-use) 调研
(2026-08-29)确定的三项优化改动,按实施依赖排序:

| # | 文档 | 内容 | 依赖 / 状态 |
|---|---|---|---|
| 0001 | [Agent 可驱动 API —— 读屏 / 等待 / 输入注入](0001-agent-driving-api.md) | `GET /screen`、`POST /wait`、`POST /keys`,让 agent 像 `tu` 一样驱动运行中的会话 | 依赖 0002 的查询应答与 `Resize`;**已实施** |
| 0002 | [native 仿真器完整度 —— 选用现成仿真器或补齐手写引擎](0002-native-emulator-completeness.md) | 仿真层替换为 x/vt(查询应答、Resize、vttest 一致性)+ 图形提取保留 + 缺口补偿 | 无(0001 的前置);**已实施(路线 A)** |
| 0003 | [分发与自更新 —— install.sh + gotty self update](0003-distribution-self-update.md) | 一键安装、版本单一来源、构建矩阵 + 校验和、发布工作流 | 无(独立);**已实施** |

> 注:0001 实施时已同步落地 0002 的最小查询应答与两侧应答回写;0002 本体
> (x/vt 仿真层替换 + 缺口补偿 + raw PTY + 回归)于 2026-08-29 完成——见文档
> 顶部状态块。0003 独立,随时可并行。
