# 优化改动索引(docs/feat)

针对 [flipbit03/terminal-use](https://github.com/flipbit03/terminal-use) 调研
(2026-08-29)确定的三项优化改动,按实施依赖排序:

| # | 文档 | 内容 | 依赖 / 状态 |
|---|---|---|---|
| 0001 | [Agent 可驱动 API —— 读屏 / 等待 / 输入注入](0001-agent-driving-api.md) | `GET /screen`、`POST /wait`、`POST /keys`,让 agent 像 `tu` 一样驱动运行中的会话 | 依赖 0002 的查询应答与 `Resize`;**已实施** |
| 0002 | [native 仿真器完整度 —— 查询应答与缺失 CSI 对齐](0002-native-emulator-completeness.md) | DA/DSR/DECRQM 应答、ICH/DCH/ECH/REP/IRM/DECAWM/TBC、`Emulator.Resize` | 无(0001 的前置);待实施 |
| 0003 | [分发与自更新 —— install.sh + gotty self update](0003-distribution-self-update.md) | 一键安装、版本单一来源、构建矩阵 + 校验和、发布工作流 | 无(独立);待实施 |

> 注:0001 实施时已同步落地 0002 中它依赖的部分——`Emulator.Resize`、最小查询
> 应答(DA1/DA2、DSR 5/6、DECRQM)及 serve/capture 两侧的应答回写;0002 其余
> 内容(ICH/DCH/ECH/REP/IRM/DECAWM/TBC、OSC 颜色查询、vttest fixtures)仍待实施。

实施顺序建议:先 0002(独立、收益立竿见影——capture 立刻能跑 vim),再 0001
(站在 0002 的应答与 Resize 上),0003 随时可并行。
