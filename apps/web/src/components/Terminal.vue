<template>
  <!-- 不拦截右键:保留浏览器原生上下文菜单(复制/检查元素等) -->
  <div ref="terminalEl" class="terminal-container"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { Terminal as XTerminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { WebglAddon } from '@xterm/addon-webgl'
import { ImageAddon } from '@xterm/addon-image'
import '@xterm/xterm/css/xterm.css'
import { onThemeChange } from '../utils/theme'
import { useXTermClipboard, loadClipboardAddon } from '../utils/clipboard'

const props = defineProps<{
    // 使用 DOM 渲染器而非 WebGL:图形协议图片(image addon)在 DOM
    // 渲染器下以 img 元素渲染,截图/合成最稳(capture 渲染页用)。
    domRenderer?: boolean
}>()

const emit = defineEmits<{
    // 服务端 SetWindowTitle 帧;不再直接写 document.title,
    // 由上层(pane 头部)决定如何展示。
    (e: 'title', title: string): void
}>()

// 终端字体(等宽栈)与字号的单一真源是 index.css 的 --font-mono / --term-font-size:
// 终端面的"半字符内边距"取 --term-cell-w(xterm 的真实格子宽)的一半,而格子宽
// 由字体字号决定,只有这里与那边一致,留白才正好是半个字符。下面的常量仅是
// 变量缺失时的兜底。
const FONT_FAMILY_FALLBACK =
    'Menlo, Monaco, Consolas, "DejaVu Sans Mono", "Courier New", monospace'
const FONT_SIZE_FALLBACK = 14

const terminalEl = ref<HTMLElement>()
let term: XTerminal
let fitAddon: FitAddon
let resizeHandler: () => void
let unsubscribeTheme: (() => void) | null = null

// 终端内部配色来自 CSS 变量的单一真源(index.css 的 --bg-terminal/--term-*)。
// 之前这里另写一套 hex,与 CSS 各存一份,改动只落一边就会出现
// "CSS 改了、终端没改"(见 docs/feat/0006 §3.6)。
//
// 读取时机是安全的:applyTheme 先写 <html data-theme> 再广播主题变化
// (utils/theme.ts 的 applyTheme),且 main.ts 在 mount 之前就应用了主题,
// 所以这里读到的 computed 值一定是当前主题的。
function cssVar(name: string, fallback: string): string {
    const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
    return v || fallback
}

function terminalTheme(): Record<string, string> {
    return {
        // 兜底值与 index.css 的暗色值一致:变量缺失时至少不是非法颜色
        background: cssVar('--bg-terminal', '#1f1f1f'),
        foreground: cssVar('--term-fg', '#cccccc'),
        cursor: cssVar('--term-cursor', '#cccccc'),
        cursorAccent: cssVar('--term-cursor-accent', '#1f1f1f'),
        selectionBackground: cssVar('--term-selection-bg', '#264f78'),
        // 故意不设 selectionForeground:VSCode 的 terminal.selectionForeground 默认是
        // null(含义为"保留原字形颜色"),xterm 缺省时同样是保留 → 语义一致。
        //
        // ANSI 16 色:不设的话 xterm 用自己的内置调色板,ls --color / git status / vim
        // 的配色会和 VSCode 集成终端明显不同。取值来自 VSCode 的
        // terminalColorRegistry.ts(见 index.css 的 --term-ansi-*)。
        black: cssVar('--term-ansi-black', '#000000'),
        red: cssVar('--term-ansi-red', '#cd3131'),
        green: cssVar('--term-ansi-green', '#0dbc79'),
        yellow: cssVar('--term-ansi-yellow', '#e5e510'),
        blue: cssVar('--term-ansi-blue', '#2472c8'),
        magenta: cssVar('--term-ansi-magenta', '#bc3fbc'),
        cyan: cssVar('--term-ansi-cyan', '#11a8cd'),
        white: cssVar('--term-ansi-white', '#e5e5e5'),
        brightBlack: cssVar('--term-ansi-bright-black', '#666666'),
        brightRed: cssVar('--term-ansi-bright-red', '#f14c4c'),
        brightGreen: cssVar('--term-ansi-bright-green', '#23d18b'),
        brightYellow: cssVar('--term-ansi-bright-yellow', '#f5f543'),
        brightBlue: cssVar('--term-ansi-bright-blue', '#3b8eea'),
        brightMagenta: cssVar('--term-ansi-bright-magenta', '#d670d6'),
        brightCyan: cssVar('--term-ansi-bright-cyan', '#29b8db'),
        brightWhite: cssVar('--term-ansi-bright-white', '#e5e5e5'),
    }
}

onMounted(() => {
  // 字体/字号在挂载时从 CSS 变量读取(此时样式已应用,index.css 由 main.ts 导入)。
  const fontFamily = cssVar('--font-mono', FONT_FAMILY_FALLBACK)
  const fontFamilyParsed = Number.parseFloat(cssVar('--term-font-size', String(FONT_SIZE_FALLBACK)))
  const fontSize = Number.isFinite(fontFamilyParsed) ? fontFamilyParsed : FONT_SIZE_FALLBACK

  term = new XTerminal({
    cursorBlink: true,
    fontSize,
    fontFamily,
    theme: terminalTheme(),
    // xterm 的"右侧滚动条槽"宽度(默认 14px)是网格右侧空白的来源,
    // 收窄它可消除全屏 TUI(opencode/btop 自绘底色)尾部那 14px 槽。
    // 5.5 的选项名是 overviewRulerWidth(数值);6.0 改成了
    // overviewRuler: { width },此写法为 5.5 版(见 543d4cf 的 6.0 适配)。
    // 注意不能写 0:xterm 里是 `|| 14`,0 是假值会退回 14。
    overviewRulerWidth: 1,
  })

  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.loadAddon(new WebLinksAddon())
  // OSC 52:终端内程序(vim/tmux/ssh)读写浏览器系统剪贴板
  loadClipboardAddon(term)

  // 图形协议图片(kitty / sixel / iTerm2 inline):chafa/img2sixel 等
  // 输出在终端里显示为真实图片(WebGL 渲染器下以 overlay 层覆盖)。
  try {
    term.loadAddon(new ImageAddon())
    document.body.dataset.imageAddon = '1'
  } catch (e) {
    // 环境不支持时静默回退(图片退化为占位文本)
    document.body.dataset.imageAddon = '0'
    console.error('image addon failed to load', e)
  }

  // WebGL 渲染器:GPU 不可用(无显卡/远程桌面/部分 headless)时抛错,
  // 自动回退到 xterm 内置的 DOM 渲染器。
  if (!props.domRenderer) {
    try {
      term.loadAddon(new WebglAddon())
    } catch {
      // 回退 DOM 渲染器即可,无需处理
    }
  }

  term.open(terminalEl.value!)

  // 复制/粘贴快捷键(Ctrl+Shift+C/V、Ctrl+C 选区复制、Ctrl+V 粘贴)
  useXTermClipboard(term)

  // 程序设置的终端标题(OSC 0/2,如 vim 的 "vim - file"):
  // xterm 解析后经 onTitleChange 上报,上层据此更新页签标题
  // (GNOME-Shell 风格:标题由程序自动命名/更新)。
  term.onTitleChange((title) => emit('title', title))

  // xterm.css 的 .terminal 规则自带默认等宽字体;显式覆盖到元素上,
  // 保证 WebGL 与 DOM 两种渲染路径都使用配置的字体栈。
  ;(term.element as HTMLElement).style.fontFamily = fontFamily

  resizeHandler = () => {
    fit()
  }

  requestAnimationFrame(() => {
    resizeHandler()
    window.addEventListener('resize', resizeHandler)
  })

  // 跟随亮/暗主题,动态切换 xterm 内部的配色(纯渲染层;不向 PTY 同步)
  unsubscribeTheme = onThemeChange(() => {
    term.options.theme = terminalTheme()
  })
})

onBeforeUnmount(() => {
  if (resizeHandler) window.removeEventListener('resize', resizeHandler)
  unsubscribeTheme?.()
  term?.dispose()
})

// fit 重新适配容器尺寸;v-show 隐藏后重新显示时必须调用(激活 watcher)。
// 隐藏(v-show display:none)或未布局的容器高度为 0:FitAddon 会把 rows
// 钳到 1 并 resize 出"1 行终端",该会话从此只剩一行、光标永远在第一行、
// 无法向下 —— 因此零尺寸时跳过,等可见后再由上层 fit。
function fit() {
  const el = terminalEl.value
  if (!el || el.clientWidth === 0 || el.clientHeight === 0) return
  fitAddon?.fit()
  publishCellWidth()
}

// publishCellWidth 把 xterm 实际使用的字符格宽写进 --term-cell-w,供终端面的
// "半字符内边距"使用(见 TerminalPane.vue)。
//
// 为什么不能直接用 CSS 的 0.5ch:ch 是字体 "0" 的步进宽度,而 xterm 内部把格子
// 宽度取整了 —— 实测 1ch = 7.70px 而真实格子 = 7.00px,差约 10%,用 0.5ch 会得到
// 0.55 个字符格而不是半个。所以这里直接取 xterm 的真实几何:
// 格子宽 = .xterm-screen 的宽度 / 列数(两者都是公开 DOM/API)。
// 格子宽度只由字体与字号决定,与容器尺寸无关,所以每次 fit 后重算是幂等的。
function publishCellWidth() {
  const screenEl = terminalEl.value?.querySelector('.xterm-screen') as HTMLElement | null
  if (!screenEl || !term || term.cols <= 0) return
  const width = screenEl.getBoundingClientRect().width / term.cols
  if (width > 0) {
    document.documentElement.style.setProperty('--term-cell-w', width + 'px')
  }
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

// onWriteParsed 在 xterm 解析完一批写入后触发;返回退订函数。
// ws.ts 用它把"输入上行"的开启推迟到重放字节全部解析完成之后:
// 重放里的终端查询会触发 xterm 自动应答,若在解析完成前就放开上行,
// 这些陈旧应答被写回 PTY,前台 shell 会把转义载荷显示成乱码。
function onWriteParsed(callback: () => void): (() => void) | undefined {
  if (!term) return undefined
  const d = term.onWriteParsed(() => callback())
  return () => d.dispose()
}

function reset() {
  term?.clear()
}

// focus 把键盘焦点交给 xterm 的输入区;激活/创建会话后由上层调用,
// 让用户无需点击终端即可直接输入。
function focus() {
  term?.focus()
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
  onWriteParsed,
  reset,
  focus,
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
