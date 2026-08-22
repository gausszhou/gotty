// 终端复制/粘贴能力(issue xtermjs/xterm.js#2478):
// 网页终端里 Ctrl+C/Ctrl+V 是 SIGINT/引号插入,不符合浏览器用户习惯。
// 这里统一接管:
//   - Ctrl+Shift+C / Cmd+C  复制(有选区时)
//   - Ctrl+C                 有选区 → 复制;无选区 → 照常 SIGINT
//   - Ctrl+Shift+V / Ctrl+V / Cmd+V  粘贴
//   - 终端区域内右键         粘贴(替代浏览器上下文菜单)
//   - OSC 52                 终端内程序(vim/tmux/ssh)读写系统剪贴板
//
// 注意:xterm 的 attachCustomKeyEventHandler 是**单槽位**,重复 attach
// 会互相覆盖,所以所有按键判断必须合并进同一个 handler。
import { Terminal, type ITerminalAddon } from '@xterm/xterm'
import { ClipboardAddon, type IClipboardProvider } from '@xterm/addon-clipboard'
import { logger } from './logger'

// copyText 复制到剪贴板:优先 Clipboard API(要求安全上下文/权限),
// 失败回退 execCommand(非安全上下文如局域网 http 也可用)。
async function copyText(text: string): Promise<void> {
    try {
        if (navigator.clipboard?.writeText) {
            await navigator.clipboard.writeText(text)
            return
        }
    } catch (err) {
        logger.warn('clipboard', 'clipboard API copy failed, fallback: %s', err)
    }
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
}

// readClipboard 读取剪贴板文本;不可用(非安全上下文/权限被拒)返回空串。
async function readClipboard(): Promise<string> {
    try {
        if (navigator.clipboard?.readText) {
            return await navigator.clipboard.readText()
        }
    } catch (err) {
        logger.warn('clipboard', 'clipboard API read failed: %s', err)
    }
    return ''
}

// osc52ClipboardProvider 供 @xterm/addon-clipboard 使用:让终端内的程序
// (vim 的 +clipboard / tmux set-clipboard on / ssh 会话)通过 OSC 52 读写
// 浏览器系统剪贴板。复用上面带回退的读写实现。
// 选区参数用字面量比较:ClipboardSelectionType.SYSTEM = 'c',PRIMARY = 'p'
// (addon 的类型是 ambient const enum,tsconfig 开了 isolatedModules 无法引用)。
export function osc52ClipboardProvider(): IClipboardProvider {
    return {
        readText: (sel) => (sel === 'c' ? readClipboard() : Promise.resolve('')),
        writeText: (sel, text) => (sel === 'c' ? copyText(text) : Promise.resolve()),
    }
}

// loadClipboardAddon 装载 OSC 52 剪贴板 addon。
// 构造签名是 (base64?, provider?):必须留空 base64 槽位,
// 否则 provider 会被当成编码器,OSC 52 读写直接抛错。
export function loadClipboardAddon(term: Terminal): ITerminalAddon {
    const addon = new ClipboardAddon(undefined, osc52ClipboardProvider())
    term.loadAddon(addon)
    return addon
}

// pasteFromClipboard 把剪贴板内容送进终端:
// 优先 term.paste(直接走终端输入通道,与键入等价);
// Clipboard API 不可用时聚焦 xterm 辅助 textarea,由浏览器原生粘贴流程接管。
export async function pasteFromClipboard(term: Terminal): Promise<void> {
    const text = await readClipboard()
    if (text) {
        term.paste(text)
        return
    }
    const textarea = term.element?.querySelector('textarea')
    textarea?.focus()
    document.execCommand('paste')
}

// useXTermClipboard 装载复制/粘贴快捷键。返回 false 表示本处理器已接管,
// xterm 不再继续处理该键(不会把 Ctrl+C 当 SIGINT、不会把 Ctrl+V 当输入)。
export function useXTermClipboard(term: Terminal): void {
    term.attachCustomKeyEventHandler((ev) => {
        if (ev.type !== 'keydown') return true

        const isCopyKey = ev.code === 'KeyC' && (ev.ctrlKey || ev.metaKey)
        const isPasteKey = ev.code === 'KeyV' && (ev.ctrlKey || ev.metaKey)
        if (!isCopyKey && !isPasteKey) return true

        if (isCopyKey) {
            const selection = term.getSelection()
            if (selection) {
                ev.preventDefault()
                void copyText(selection)
                return false // 有选区:Ctrl/Cmd+C = 复制,不发 SIGINT
            }
            // 无选区:Ctrl+Shift+C / Cmd+C 无事可做,吞掉;
            // 纯 Ctrl+C 放行给终端(SIGINT)
            return !(ev.shiftKey || ev.metaKey)
        }

        // 粘贴键:统一走剪贴板 → term.paste
        ev.preventDefault()
        void pasteFromClipboard(term)
        return false
    })
}