// 轻量国际化:偏好为 zh / en / system(默认,跟随系统/浏览器语言),
// 持久化在 localStorage("gotty.lang"),设置弹窗内可手动切换。
// 不引入 vue-i18n 依赖:一个 reactive lang ref + 字典即可满足界面文案。
//
// 与 utils/theme.ts 是同一套结构(pref → resolve → 写 DOM → 广播 → 跟随系统变化),
// 两者的差别只在信号源:语言是 navigator.language / languagechange,
// 主题是 matchMedia('(prefers-color-scheme)')。改一边时另一边通常也要改。
import { ref } from 'vue'
import { logger } from './logger'

// Lang:实际生效的语言,只有两种(供 t() 与 <html lang> 使用)。
export type Lang = 'zh' | 'en'
// LangPref:用户在设置里的选择,比 Lang 多一个"跟随系统"。
export type LangPref = Lang | 'system'

const LANG_KEY = 'gotty.lang'
const LANG_EVENT = 'gotty:lang'
// 默认跟随系统:没存过偏好的用户(首次访问/清空 localStorage)按系统语言走。
const DEFAULT_PREF: LangPref = 'system'
const LANG_PREFS: readonly string[] = ['zh', 'en', 'system']

const messages: Record<Lang, Record<string, string>> = {
    zh: {
        'tab.new': '新建会话',
        'tab.destroy': '销毁会话',
        'tab.dragHint': '拖拽调整顺序',
        'tab.latency': '往返延迟(RTT),每 2 秒刷新',
        'settings.open': '打开设置',
        'settings.close': '关闭',
        'settings.title': '设置',
        'settings.theme': '主题',
        'settings.system': '跟随系统',
        'settings.dark': '深色',
        'settings.light': '浅色',
        'settings.language': '语言',
        'settings.pageTitle': '页面标题',
        'settings.pageTitlePlaceholder': '浏览器标签页标题,留空恢复默认',
        'settings.save': '保存',
        'settings.saved': '已保存',
        'settings.saveFailed': '保存失败',
        'empty.title': '创建终端会话',
        'empty.hint': '点击新建一个终端',
        'empty.loading': '正在连接…',
        'dialog.gone': '会话已销毁',
        'dialog.lost': '连接已断开',
        'dialog.goneMsg': '该会话已被销毁或不存在',
        'dialog.reconnect': '重新连接',
        'dialog.close': '关闭',
    },
    en: {
        'tab.new': 'New session',
        'tab.destroy': 'Destroy session',
        'tab.dragHint': 'Drag to reorder',
        'tab.latency': 'Round-trip latency (RTT), refreshed every 2s',
        'settings.open': 'Open settings',
        'settings.close': 'Close',
        'settings.title': 'Settings',
        'settings.theme': 'Theme',
        'settings.system': 'System',
        'settings.dark': 'Dark',
        'settings.light': 'Light',
        'settings.language': 'Language',
        'settings.pageTitle': 'Page title',
        'settings.pageTitlePlaceholder': 'Browser tab title; empty restores default',
        'settings.save': 'Save',
        'settings.saved': 'Saved',
        'settings.saveFailed': 'Failed to save',
        'empty.title': 'Create terminal session',
        'empty.hint': 'Click to open a new terminal',
        'empty.loading': 'Connecting…',
        'dialog.gone': 'Session closed',
        'dialog.lost': 'Connection lost',
        'dialog.goneMsg': 'This session has been destroyed or does not exist',
        'dialog.reconnect': 'Reconnect',
        'dialog.close': 'Close',
    },
}

// systemLang 读系统/浏览器语言。
//
// 网页**无法**直接读操作系统 locale,`navigator.language` 是唯一可用的信号:
// 它默认等于操作系统语言,但用户可以在浏览器里单独改(那种情况下跟随的是浏览器设置,
// 不是 OS —— 这是 Web 的固有限制,不是本实现的取舍)。
// 取 languages[0](首选语言)而不是 language,语义相同但对多语言偏好的浏览器更明确。
//
// 字典只有 zh / en 两套,所以这里做的是"是不是中文":`zh*`(zh-CN/zh-TW/zh-Hans…)
// 一律归 zh,其余(ja/de/fr…)一律归 en。加第三种语言时这里要跟着改。
export function systemLang(): Lang {
    try {
        const preferred = navigator.languages?.[0] || navigator.language || ''
        return preferred.toLowerCase().startsWith('zh') ? 'zh' : 'en'
    } catch {
        // 极老环境没有 navigator.languages:与旧版 detectLang 一致,兜底 en
        return 'en'
    }
}

// resolveLang 把用户偏好解析成实际生效的语言。
export function resolveLang(pref: LangPref): Lang {
    return pref === 'system' ? systemLang() : pref
}

// currentLangPref 读取持久化偏好。
// 兼容旧版本:那时存的是**已解析**的 'zh'/'en',现在按"显式偏好"读 ——
// 用户当时手点过切换,继续固定在他选的语言才是他想要的,所以不需要迁移。
export function currentLangPref(): LangPref {
    try {
        const v = localStorage.getItem(LANG_KEY)
        if (v !== null && LANG_PREFS.includes(v)) return v as LangPref
    } catch {
        // localStorage 不可用时静默降级
    }
    return DEFAULT_PREF
}

// lang 为全局响应式状态:切换后所有使用 t() 的模板自动重渲染。
// 注意它存的是**解析后**的语言,不是偏好 —— "跟随系统"的偏好存在 localStorage 里。
export const lang = ref<Lang>(resolveLang(currentLangPref()))

// 初始化同步 <html lang>(供字体选择、拼写检查、无障碍朗读使用)。
document.documentElement.lang = lang.value

// t 返回当前语言文案;未知 key 原样返回(便于发现遗漏)。
export function t(key: string): string {
    return messages[lang.value][key] ?? key
}

// applyLang 应用偏好:解析成 zh/en 写 lang ref 与 <html lang>,再持久化**偏好本身**。
// 持久化偏好而不是解析结果,是"跟随系统"能跨刷新存活的关键 —— 存结果的话,
// 刷新后读到的就是 'zh',再也回不到跟随状态了。
// 顺序与 applyTheme 一致:先写状态,订阅者读到的才是新值。
export function applyLang(pref: LangPref = currentLangPref()): Lang {
    const resolved = resolveLang(pref)
    lang.value = resolved
    document.documentElement.lang = resolved
    try {
        localStorage.setItem(LANG_KEY, pref)
    } catch {
        // localStorage 不可用时静默降级
    }
    logger.info('i18n', 'applied pref=%s lang=%s', pref, resolved)
    return resolved
}

// notifyLangChange 广播语言变化(与 notifyThemeChange 同构,便于调用方统一处理)。
export function notifyLangChange(resolved: Lang) {
    window.dispatchEvent(new CustomEvent<Lang>(LANG_EVENT, { detail: resolved }))
}

// onLangChange 订阅语言变化,返回退订函数。
export function onLangChange(cb: (lang: Lang) => void): () => void {
    const handler = (e: Event) => cb((e as CustomEvent<Lang>).detail)
    window.addEventListener(LANG_EVENT, handler)
    return () => window.removeEventListener(LANG_EVENT, handler)
}

// watchSystemLang 订阅系统/浏览器语言变化:仅当偏好仍是 system 时重新解析并广播。
// 事件名是 HTML 规范的 window 'languagechange',Chrome/Edge/Firefox/Safari 都支持。
// 它在实际使用中很少触发(用户去改浏览器语言),但补上它,"跟随系统"才是完整的。
export function watchSystemLang() {
    window.addEventListener('languagechange', () => {
        if (currentLangPref() !== 'system') return
        logger.info('i18n', 'system language changed → %s', systemLang())
        notifyLangChange(applyLang('system'))
    })
}
