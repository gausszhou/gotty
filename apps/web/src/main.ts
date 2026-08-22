import { createApp } from 'vue'
import App from './App.vue'
// 唯一全局样式入口(html/body 锁滚动、xterm 字体),构建时内联进 main.js
import './style/index.css'
import { applyTheme, currentTheme } from './utils/theme'

// mount 前应用持久化主题:<html data-theme=...> 驱动 CSS 变量,避免首帧闪烁
applyTheme(currentTheme())

createApp(App).mount('#app')