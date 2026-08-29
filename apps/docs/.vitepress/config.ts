import { defineConfig } from 'vitepress'

export default defineConfig({
    lang: 'zh-CN',
    title: 'GoTTY',
    titleTemplate: ':title · GoTTY',
    description: '基于 Vue 3 + xterm.js + Go 的 Web 终端：随时随地通过浏览器访问你的终端',

    lastUpdated: true,

    themeConfig: {
        logo: '/favicon.png',
        nav: [
            { text: '首页', link: '/' },
            { text: '指南', link: '/guide/introduction' },
        ],
        sidebar: {
            '/guide/': [
                {
                    text: '指南',
                    items: [
                        { text: '介绍', link: '/guide/introduction' },
                        { text: '安装与使用', link: '/guide/usage' },
                    ],
                },
            ],
        },
        socialLinks: [{ icon: 'github', link: 'https://github.com/gausszhou/gotty' }],
        footer: {
            message: '基于 MIT 许可发布',
            copyright: 'Copyright © 2025 gausszhou',
        },
        docFooter: {
            prev: '上一页',
            next: '下一页',
        },
        outline: {
            label: '本页目录',
        },
        search: {
            provider: 'local',
        },
    },
})