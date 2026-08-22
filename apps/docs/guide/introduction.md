# 介绍

GoTTY 是一个把终端会话共享到浏览器的工具：它把一个命令行程序（如 `bash`）的输出与输入，
通过 WebSocket 转发到浏览器中的终端模拟器，让你在任何设备上通过网页操作终端。

仓库地址：<https://github.com/gausszhou/gotty>

## 特性

- **跨平台**：Go 编译为单个静态二进制，前端资源通过 `go:embed` 内置，部署无需外部资源。
- **现代前端**：Vite + Vue 3 + xterm.js v5，支持 WebGL 渲染、拖放复制粘贴等交互。
- **多种命令**：可以运行任意命令行程序，默认是 `$SHELL`，支持 `/bin/sh`、`/bin/bash` 等。
- **部署友好**：单二进制分发，支持 TLS；访问控制可交由反向代理。

## 目录结构

本项目使用 pnpm workspace 管理前端工程：

```text
.
├── apps
│   ├── web        # 实际前端应用（Vite + Vue 3 + xterm.js）
│   └── docs       # 文档工程（VitePress），即本站
├── backend        # Go 后端：命令执行与回话管理
├── server         # Go 服务端：HTTP / WebSocket / 静态资源
├── webtty         # WebTTY 协议：主从会话与消息编解码
└── internal       # 内部工具包
```

## 工作原理

```
浏览器中的 xterm.js  ──WebSocket──▶  Go 服务端
                                        │
                                        ▼
                                  本地被执行的命令（bash / zsh / 任意程序）
```

终端输出经过 WebTTY 协议编码后推送回浏览器，输入按键同样经由 WebSocket 回传，
从而形成一条双向的「伪终端」管道。

## 许可

MIT License。