// 轻量国际化:跟随浏览器语言(zh/en),右上角可手动切换并持久化。
// 不引入 vue-i18n 依赖:一个 reactive lang ref + 字典即可满足界面文案。
import { ref } from 'vue'

export type Lang = 'zh' | 'en'

const LANG_KEY = 'gotty.lang'

const messages: Record<Lang, Record<string, string>> = {
    zh: {
        'tab.new': '新建会话',
        'tab.destroy': '销毁会话',
        'theme.toLight': '切换到亮色主题',
        'theme.toDark': '切换到暗色主题',
        'tab.latency': '往返延迟(RTT),每 2 秒刷新',
        'lang.toggle': '切换界面语言',
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
        'theme.toLight': 'Switch to light theme',
        'theme.toDark': 'Switch to dark theme',
        'tab.latency': 'Round-trip latency (RTT), refreshed every 2s',
        'lang.toggle': 'Switch UI language',
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

function detectLang(): Lang {
    return (navigator.language || '').toLowerCase().startsWith('zh') ? 'zh' : 'en'
}

function loadLang(): Lang {
    try {
        const v = localStorage.getItem(LANG_KEY)
        if (v === 'zh' || v === 'en') return v
    } catch {
        // localStorage 不可用时静默降级
    }
    return detectLang()
}

// lang 为全局响应式状态:切换后所有使用 t() 的模板自动重渲染。
export const lang = ref<Lang>(loadLang())

// 初始化同步 <html lang>。
document.documentElement.lang = lang.value

// t 返回当前语言文案;未知 key 原样返回(便于发现遗漏)。
export function t(key: string): string {
    return messages[lang.value][key] ?? key
}

// setLang 切换语言并持久化。
export function setLang(l: Lang) {
    lang.value = l
    document.documentElement.lang = l
    try {
        localStorage.setItem(LANG_KEY, l)
    } catch {
        // 忽略持久化失败
    }
}

// toggleLang 中/英互切,返回切换后的语言。
export function toggleLang(): Lang {
    setLang(lang.value === 'zh' ? 'en' : 'zh')
    return lang.value
}