import { createApp } from 'vue'
import App from './App.vue'
// 唯一全局样式入口(html/body 锁滚动、xterm 字体),构建时内联进 main.js
import './style/index.css'

createApp(App).mount('#app')