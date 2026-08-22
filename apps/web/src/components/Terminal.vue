<template>
  <div ref="terminalEl" class="terminal-container"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { Terminal as XTerminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'

const terminalEl = ref<HTMLElement>()
let term: XTerminal
let fitAddon: FitAddon
let resizeHandler: () => void
let messageEl: HTMLElement
let messageTimer: ReturnType<typeof setTimeout>

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

  try {
    const webglAddon = new WebglAddon()
    webglAddon.onContextLoss(() => webglAddon.dispose())
    term.loadAddon(webglAddon)
  } catch {
    console.warn('WebGL renderer not available, falling back to canvas')
  }

  messageEl = document.createElement('div')
  messageEl.className = 'xterm-overlay'

  term.open(terminalEl.value!)

  resizeHandler = () => {
    fitAddon.fit()
    showMessage(String(term.cols) + 'x' + String(term.rows), 2000)
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

function info() {
  return { columns: term.cols, rows: term.rows }
}

function write(data: Uint8Array) {
  term?.write(data)
}

function showMessage(message: string, timeout: number) {
  if (!terminalEl.value || !messageEl) return
  messageEl.textContent = message
  terminalEl.value.appendChild(messageEl)
  if (messageTimer) clearTimeout(messageTimer)
  if (timeout > 0) {
    messageTimer = setTimeout(() => {
      if (messageEl.parentNode === terminalEl.value) {
        terminalEl.value.removeChild(messageEl)
      }
    }, timeout)
  }
}

function removeMessage() {
  if (messageEl?.parentNode === terminalEl.value) {
    terminalEl.value!.removeChild(messageEl)
  }
}

function setWindowTitle(title: string) {
  document.title = title
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
  removeMessage()
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
  showMessage,
  removeMessage,
  setWindowTitle,
  setPreferences,
  onInput,
  onResize,
  reset,
  deactivate,
  close,
})
</script>

<style scoped>
.terminal-container {
  width: 100%;
  height: 100vh;
  background: black;
}
</style>
