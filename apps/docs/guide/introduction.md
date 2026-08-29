# 介绍

GoTTY 是一个把终端会话共享到浏览器的工具：它把一个命令行程序（如 `bash`）的输出与输入，
通过 WebSocket 转发到浏览器中的终端模拟器，让你在任何设备上通过网页操作终端。它可以运行
任意命令行程序（`htop`、`vim`、`top`…），默认是登录 shell（`$SHELL`），并支持多会话并行管理。

仓库地址：<https://github.com/gausszhou/gotty>

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