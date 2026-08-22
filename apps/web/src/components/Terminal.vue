<template>
  <div ref="terminalEl" class="terminal-container"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { Terminal as XTerminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { WebglAddon } from '@xterm/addon-webgl'
import '@xterm/xterm/css/xterm.css'

const emit = defineEmits<{
    // 服务端 SetWindowTitle 帧;不再直接写 document.title,
    // 由上层(pane 头部)决定如何展示。
    (e: 'title', title: string): void
}>()

const terminalEl = ref<HTMLElement>()
let term: XTerminal
let fitAddon: FitAddon
let resizeHandler: () => void

onMounted(() => {
  term = new XTerminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily:
      '"DejaVu Sans Mono", "Everson Mono", FreeMono, Menlo, Terminal, monospace, "Apple Symbols"',
    theme: { background: '#000000' },
  })

  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.loadAddon(new WebLinksAddon())

  // WebGL 渲染器:GPU 不可用(无显卡/远程桌面/部分 headless)时
  // loadAddon 会抛错,自动回退到 xterm 内置的 DOM 渲染器。
  try {
    term.loadAddon(new WebglAddon())
  } catch {
    // 回退 DOM 渲染器即可,无需处理
  }

  term.open(terminalEl.value!)

  resizeHandler = () => {
    fitAddon.fit()
  }

  requestAnimationFrame(() => {
    resizeHandler()
    window.addEventListener('resize', resizeHandler)
  })
})

onBeforeUnmount(() => {
  if (resizeHandler) window.removeEventListener('resize', resizeHandler)
  term?.dispose()
})

// fit 重新适配容器尺寸;v-show 隐藏后重新显示时必须调用
// (隐藏时容器尺寸为 0,fit 结果无效)
function fit() {
  fitAddon?.fit()
}

function info() {
  return { columns: term.cols, rows: term.rows }
}

function write(data: Uint8Array) {
  term?.write(data)
}

function setWindowTitle(title: string) {
  emit('title', title)
}

function setPreferences(_value: object) {
  // no-op: xterm.js v5+ handles config via Terminal constructor options
}

function onInput(callback: (input: string) => void) {
  term?.onData((data) => callback(data))
}

function onResize(callback: (columns: number, rows: number) => void) {
  term?.onResize(({ cols, rows }) => callback(cols, rows))
}

function reset() {
  term?.clear()
}

function deactivate() {
  term?.blur()
}

function close() {
  term?.dispose()
}

defineExpose({
  info,
  write,
  setWindowTitle,
  setPreferences,
  onInput,
  onResize,
  reset,
  deactivate,
  fit,
  close,
})
</script>

<style scoped>
.terminal-container {
    width: 100%;
    height: 100%;
    background: black;
    padding: 0;
    margin: 0;
    overflow: hidden;
}
</style>
