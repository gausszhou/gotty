# 优化改动索引(docs/feat)

针对 [flipbit03/terminal-use](https://github.com/flipbit03/terminal-use) 调研
(2026-08-29 首轮、2026-08-30 复盘的输入侧与观察侧对标)确定的五项优化改动(0001–0005),
按实施依赖排序。另有 0006 来自 UI 方法论大纲,与上述调研无关,独立可并行:

| # | 文档 | 内容 | 依赖 / 状态 |
|---|---|---|---|
| 0001 | [Agent 可驱动 API —— 读屏 / 等待 / 输入注入](0001-agent-driving-api.md) | `GET /screen`、`POST /wait`、`POST /keys`,让 agent 像 `tu` 一样驱动运行中的会话 | 依赖 0002 的查询应答与 `Resize`;**已实施** |
| 0002 | [native 仿真器完整度 —— 选用现成仿真器或补齐手写引擎](0002-native-emulator-completeness.md) | 仿真层替换为 x/vt(查询应答、Resize、vttest 一致性)+ 图形提取保留 + 缺口补偿 | 无(0001 的前置);**已实施(路线 A)** |
| 0003 | [分发与自更新 —— install.sh + gotty self update](0003-distribution-self-update.md) | 一键安装、版本单一来源、构建矩阵 + 校验和、发布工作流 | 无(独立);**已实施** |
| 0004 | [输入侧对齐 —— 鼠标驱动 / 键名层 / 粘贴 + agent CLI 面](0004-agent-input-parity.md) | 鼠标事件构造 + 模式跟踪补位(复用 x/vt 的 X10/SGR 编码)+ `--on-text` 语义定位、`keys[]` 键名表、条件括号粘贴、`gotty mouse/press/paste/screen/…` 薄封装 | 依赖 0001 的镜像与 `/keys`;**待实施** |
| 0005 | [观察侧与配置面补齐 —— scrollback / monitor / CJK 栅格化 / per-session 配置 / agent 自描述](0005-observation-and-config.md) | scrollback 读取 + 容量治理、只读镜像订阅的 `gotty monitor`、`--font`/`--font-size`、会话级 `env/cwd/term/scrollback`、`gotty usage` + `AGENTS.md` | 承接 0004 §5;`list` 端点与 daemon 自动拉起不做(需 ADR / 搁置);**待实施** |
| 0006 | [UI Token 与组件规格 —— 颜色 / 尺寸 / 控件收敛](0006-ui-token-and-component-spec.md) | token 层补齐尺寸/间距/圆角/字号 + 共享控件 + 焦点环,页签「同底色 + 顶部 1px 强调条」、终端面同色、空态与弹窗规格 | 无(独立);**已实施** |
| 0007 | [主题与语言偏好 —— 都支持「跟随系统」且默认跟随](0007-theme-and-language-preference.md) | 两项偏好同构:`值 \| 值 \| system` 三选一、默认 system、持久化**偏好本身**、系统变化实时跟随 | 无(独立);**已实施** |

> 注:0001 实施时已同步落地 0002 的最小查询应答与两侧应答回写;0002 本体
> (x/vt 仿真层替换 + 缺口补偿 + raw PTY + 回归)于 2026-08-29 完成——见文档
> 顶部状态块。0003 独立,随时可并行。0004 是复盘 `tu` 输入侧(`mouse`/`press`/
> `paste`)后新增的一项:0001 §5 曾把鼠标与键名 DSL 划为范围外,现按二次调研
> 结论收回——`tu` 的命令表里 `mouse` 是唯一我们完全没有对应物的能力。
> 0005 收拢 0004 §5 的观察侧与配置面缺口,其中 scrollback 最便宜(上游已有、
> 只是没暴露且默认 10000 行白白吃内存),monitor 是唯一有架构取舍的一项
> (需新增只读、不抢占的订阅通道)。
> 0006 与 0007 于 2026-09-11 落地:0006 是颜色/尺寸/控件全面对齐 VSCode
> Dark/Light Modern(含终端 ANSI 16 色与焦点环);0007 把主题与语言两项偏好
> 收敛成同一套「默认跟随系统」的模型 —— 原先两项都不完整(主题没有跟随项、
> 语言的"跟随"活不过一次手点),详见各文档的实施记录。
