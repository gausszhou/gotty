import { createApp, h } from 'vue'
import { createRouter, createWebHashHistory, RouterView } from 'vue-router'
import App from './App.vue'
import CaptureView from './components/CaptureView.vue'
// 唯一全局样式入口(html/body 锁滚动、xterm 字体),构建时内联进 main.js
import './style/index.css'
import { applyTheme, currentTheme } from './utils/theme'

// 捕获/截图专用渲染页与用户日常多会话管理页分离:
//   /              日常页(TabBar + 清单 + 轮询)
//   #/capture/:sid 纯渲染页(无 UI 杂项,供 capture --engine browser 截图)
const router = createRouter({
    history: createWebHashHistory(),
    routes: [
        { path: '/', component: App },
        { path: '/capture/:sid', component: CaptureView },
        { path: '/:pathMatch(.*)*', redirect: '/' },
    ],
})

// mount 前应用持久化主题:<html data-theme=...> 驱动 CSS 变量,避免首帧闪烁
applyTheme(currentTheme())

createApp({ render: () => h(RouterView) }).use(router).mount('#app')