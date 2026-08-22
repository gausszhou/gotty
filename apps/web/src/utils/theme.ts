// 亮/暗主题:通过 <html data-theme="dark|light"> 切换 CSS 变量,
// 持久化在 localStorage("gotty.theme"),并广播事件给 xterm(动态主题)。
import { logger } from './logger'

export type Theme = 'dark' | 'light'

const THEME_KEY = 'gotty.theme'
const THEME_EVENT = 'gotty:theme'

export function currentTheme(): Theme {
    try {
        const v = localStorage.getItem(THEME_KEY)
        return v === 'light' ? 'light' : 'dark'
    } catch {
        return 'dark'
    }
}

// applyTheme 设置 html data-theme(驱动 CSS 变量)并持久化。
export function applyTheme(theme: Theme) {
    document.documentElement.dataset.theme = theme
    try {
        localStorage.setItem(THEME_KEY, theme)
    } catch {
        // localStorage 不可用时静默降级
    }
    logger.info('theme', 'applied theme=%s', theme)
}

// notifyThemeChange 广播主题变化(xterm 终端组件订阅以动态更新配色)。
export function notifyThemeChange(theme: Theme) {
    window.dispatchEvent(new CustomEvent<Theme>(THEME_EVENT, { detail: theme }))
}

// onThemeChange 订阅主题变化,返回退订函数。
export function onThemeChange(cb: (theme: Theme) => void): () => void {
    const handler = (e: Event) => cb((e as CustomEvent<Theme>).detail)
    window.addEventListener(THEME_EVENT, handler)
    return () => window.removeEventListener(THEME_EVENT, handler)
}

// toggleTheme 返回切换后的主题。
export function toggleTheme(): Theme {
    const next: Theme = currentTheme() === 'dark' ? 'light' : 'dark'
    applyTheme(next)
    notifyThemeChange(next)
    return next
}