// 亮/暗主题:偏好为 dark / light / system(默认,跟随系统 prefers-color-scheme),
// 持久化在 localStorage("gotty.theme")。实际生效的"解析后主题"写到
// <html data-theme="dark|light"> 驱动 CSS 变量,并广播事件给 xterm(动态主题)。
import { logger } from './logger'

// Theme:实际生效的配色,只有两种(供 xterm 等消费方使用)。
export type Theme = 'dark' | 'light'
// ThemePref:用户在设置里的选择,比 Theme 多一个"跟随系统"。
export type ThemePref = Theme | 'system'

const THEME_KEY = 'gotty.theme'
const THEME_EVENT = 'gotty:theme'
const LIGHT_QUERY = '(prefers-color-scheme: light)'
// 默认跟随系统:没有存过偏好的用户(首次访问/清空 localStorage)跟随系统亮暗。
const DEFAULT_PREF: ThemePref = 'system'
const THEME_PREFS: readonly string[] = ['dark', 'light', 'system']

// systemTheme 读取系统当前亮/暗;无 matchMedia(极老环境)时兜底 dark,
// 与 index.css 的 :root 默认值一致。
export function systemTheme(): Theme {
    try {
        return window.matchMedia(LIGHT_QUERY).matches ? 'light' : 'dark'
    } catch {
        return 'dark'
    }
}

// resolveTheme 把用户偏好解析成实际生效的主题。
export function resolveTheme(pref: ThemePref): Theme {
    return pref === 'system' ? systemTheme() : pref
}

// currentThemePref 读取持久化偏好;未设置或值非法(旧版本/被手改)→ 默认跟随系统。
export function currentThemePref(): ThemePref {
    try {
        const v = localStorage.getItem(THEME_KEY)
        if (v !== null && THEME_PREFS.includes(v)) return v as ThemePref
    } catch {
        // localStorage 不可用时静默降级
    }
    return DEFAULT_PREF
}

// applyTheme 应用偏好:解析成 dark/light 写 html data-theme(驱动 CSS 变量),
// 再持久化偏好本身;返回实际生效的主题。
// 顺序有意义:必须先写 data-theme,订阅者(onThemeChange)读 CSS 变量才拿到新值。
export function applyTheme(pref: ThemePref = currentThemePref()): Theme {
    const theme = resolveTheme(pref)
    document.documentElement.dataset.theme = theme
    try {
        localStorage.setItem(THEME_KEY, pref)
    } catch {
        // localStorage 不可用时静默降级
    }
    logger.info('theme', 'applied pref=%s theme=%s', pref, theme)
    return theme
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

// watchSystemTheme 订阅系统亮/暗变化:仅当偏好仍是 system 时重新解析并广播
// (显式选了 dark/light 的用户不受系统切换影响)。在 mount 前调用一次。
export function watchSystemTheme() {
    let mq: MediaQueryList
    try {
        mq = window.matchMedia(LIGHT_QUERY)
    } catch {
        return // 环境不支持:偏好仍按调用时的系统状态解析,只是不再跟随变化
    }
    mq.addEventListener('change', () => {
        if (currentThemePref() !== 'system') return
        notifyThemeChange(applyTheme('system'))
    })
}
