---
layout: home

hero:
  name: GoTTY
  text: 把终端搬进浏览器
  tagline: 基于 Vue 3 + xterm.js + Go 的 Web 终端，随时随地通过浏览器访问你的终端。
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/introduction
    - theme: alt
      text: 在 GitHub 查看
      link: https://github.com/gausszhou/gotty

features:
  - icon: 🖥️
    title: 无需安装客户端
    details: 只要浏览器支持 WebSocket，就能访问终端，Windows / macOS / Linux 通吃。
  - icon: ⚡
    title: 现代前端技术栈
    details: 前端使用 Vite + Vue 3 + xterm.js v5（含 WebGL 渲染与多行复制粘贴）。
  - icon: 🛡️
    title: 可选的访问控制
    details: 支持 TLS 加密与 WebSocket 来源校验（`--ws-origin`），也可交由反向代理加固。
  - icon: 📦
    title: 单文件分发
    details: Go 编译产出单二进制文件，前端资源通过 go:embed 内置，零外部依赖。
---