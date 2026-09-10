# 优化 7:主题与语言偏好 —— 都支持「跟随系统」且默认跟随

> 状态:**已实施**(2026-09-11)
>
> 范围:设置弹窗里的两项偏好(主题、界面语言)的**取值模型、默认值、持久化与跟随行为**。
> 外观取值本身(对 VSCode 调色板)在 [0006](0006-ui-token-and-component-spec.md);
> 本文只讲"偏好怎么解析成生效值"。

## 1. 一句话

两项偏好都是 **`值 | 值 | system`** 三选一,**默认 `system`**,并且 `system` 会**实时跟随**。

## 2. 结构(两项刻意做成同构)

`apps/web/src/utils/theme.ts` 与 `apps/web/src/utils/i18n.ts` 是同一套骨架 ——
改一边时另一边通常也要改,所以这里并排列出:

| 环节 | 主题(`utils/theme.ts`) | 语言(`utils/i18n.ts`) |
|---|---|---|
| 偏好类型 | `ThemePref = 'dark' \| 'light' \| 'system'` | `LangPref = 'zh' \| 'en' \| 'system'` |
| 生效类型 | `Theme = 'dark' \| 'light'` | `Lang = 'zh' \| 'en'` |
| 默认偏好 | `DEFAULT_PREF = 'system'` | `DEFAULT_PREF = 'system'` |
| 系统信号 | `matchMedia('(prefers-color-scheme: light)')` | `navigator.languages[0] \|\| navigator.language` |
| 写入 DOM | `<html data-theme="dark\|light">`(驱动 CSS 变量) | `<html lang="zh\|en">` |
| 持久化 key | `localStorage['gotty.theme']` | `localStorage['gotty.lang']` |
| 广播事件 | `gotty:theme`(`notifyThemeChange`/`onThemeChange`) | `gotty:lang`(`notifyLangChange`/`onLangChange`) |
| 跟随系统变化 | `matchMedia` 的 `change` 事件 | `window` 的 `languagechange` 事件 |
| 启动时机 | `main.ts` mount **前** `applyTheme` + `watchSystemTheme` | 同上 `applyLang` + `watchSystemLang` |
| 弹窗高亮 | `App.vue` 的 `themePref` 经 `:theme` 传入 | `App.vue` 的 `langPref` 经 `:lang` 传入 |

**偏好由 `App.vue` 持有、组件只上报** —— 因为高亮"跟随系统"需要的是**偏好**,
而不是解析后的值(解析后只有 dark/light、zh/en,看不出用户选的是不是 system)。

## 3. 两个容易做错的点

### 3.1 持久化的是**偏好**,不是解析结果

`applyTheme` / `applyLang` 写进 localStorage 的是 `'system'`,不是解析出的 `'dark'`/`'zh'`。
**存结果的话,"跟随系统"活不过一次刷新** —— 刷新后读到 `'zh'` 就变成显式偏好,再也回不到跟随态。
这一条对语言尤其重要:旧版本 `i18n.ts` 存的正是解析结果(`loadLang()` 返回 `Lang`),
所以旧版**根本没有真正的"跟随系统"**:首次访问跟着系统,一旦手点过就永久钉死。

### 3.2 向后兼容:旧值按"显式偏好"读,不做迁移

旧版本可能已经在 localStorage 里留下了 `'zh'` / `'en'`。新实现把它们当**显式偏好**读 ——
也就是"用户当时点过切换,继续固定在他选的语言",这正是那次点击的含义。
不需要迁移代码,也不会出现"升级后语言突然变了"。

## 4. `system` 的解析规则与固有限制

**语言**:字典只有中文/英文两套,所以 `systemLang()` 做的是"是不是中文":

- `navigator.language` 以 `zh` 开头(`zh`,`zh-CN`,`zh-TW`,`zh-Hans`…)→ `zh`;
- 其余一切(`en-US`,`ja-JP`,`de-DE`…)→ `en`。**加第三种语言时这里必须一起改。**

两个必须说清的限制:

1. **网页读不到操作系统 locale**。`navigator.language` 默认等于系统语言,但用户可以
   在浏览器里单独把界面语言设成别的 —— 那种情况下"跟随系统"跟随的是**浏览器设置**。
   这是 Web 的固有限制,不是本实现的取舍(没有别的可用信号)。
2. 因此 `docs` 里描述这个功能时不要说成"跟随操作系统语言",准确说法是
   "跟随系统/浏览器语言(浏览器上报的 `navigator.language`)"。

**主题**没有这类歧义:`prefers-color-scheme` 就是操作系统/浏览器主题。

## 5. 实时跟随

偏好为 `system` 时,系统变化**不需要刷新**:

- 主题:`matchMedia(...).addEventListener('change', …)`;
- 语言:`window.addEventListener('languagechange', …)`。

两者都先检查 `currentThemePref()` / `currentLangPref() === 'system'`,不在跟随态就直接 return ——
**显式选择优先于系统**(用户选了深色,系统切成浅色也不该动他)。

## 6. 用户可见行为

| 场景 | 结果 |
|---|---|
| 首次访问(清空 localStorage) | 主题与语言都跟随系统 |
| 手选"深色"/"中文" | 立即生效并持久化;此后**不再**跟随系统 |
| 选回"跟随系统" | 立即按当前系统重新解析;随后系统变化继续跟随 |
| 改系统主题/浏览器语言 | 偏好为 `system` 时实时跟着变;否则不受影响 |

## 7. 验证(真实渲染)

`.tmp/verify-lang-system.mjs`(Node + 裸 CDP,与仓库 e2e 同款连法):用 CDP 伪造
`navigator.language` 跑 A→E 五段,断言 `navigator.language`、`localStorage['gotty.lang']`、
`<html lang>`、实际按钮文案四项,**全部 PASS**:

- **A** 系统 `en-US` + 全新状态 → 默认 `system`,界面英文(`New session`),`<html lang>=en`,
  语言分组三项且"跟随系统/System"高亮;
- **B** 点"中文" → 持久化为 **`'zh'`(偏好本身)**,界面立刻中文;刷新后**仍是中文**(系统仍是 en),
  且高亮的变成"中文"而不是"跟随系统";
- **C** 点回"跟随系统" → 持久化为 `'system'`,立刻解析回 `en`;刷新后**仍然跟随**(证明存的是偏好);
- **D** 系统换成 `zh-CN` → 跟随生效,界面中文;
- **E** 运行中把 `navigator.language` 从 `zh-CN` 改成 `en-US` 并触发 `languagechange` →
  **不刷新**就切回英文(事件是脚本合成的,因为浏览器只在用户改语言时才真发这个事件;
  这里验的是处理逻辑)。

> ⚠️ **CDP 有两个"语言"开关,别用错**:`Emulation.setLocaleOverride` 只改 `Intl.*` 用的
> locale,**不动 `navigator.language`**;要伪造 `navigator.language` / `navigator.languages`
> 必须走 `acceptLanguage`(`Emulation.setUserAgentOverride` 或 `Network.setUserAgentOverride`)。
> 实测踩过:用 `setLocaleOverride` 时 `navigator.language` 始终是真实系统语言,整轮断言都跑偏。

## 8. 顺带修掉的一个 e2e 隐患

语言默认跟随系统后,**界面上就没有"固定语言"了** —— 而
`scripts/e2e/resilience.mjs` 原先按中文文案找重连按钮:

```js
[...document.querySelectorAll('.pane-overlay button')].find(b => b.textContent.includes('重新连接'))
```

在英文机器上文案是 `Reconnect`,`find` 返回 `undefined`、`?.click()` 静默变成空操作,
随后 `waitFor('overlay gone')` 超时 —— **脚本会失败,而且失败点离真正原因很远**。
改为按类名定位:

```js
document.querySelector('.pane-overlay .btn-primary')?.click()
```

`.pane-overlay .btn-primary` 就是重连按钮(`v-if="connState === 'disconnected'"`),
关闭按钮是 `.btn-secondary`,与文案无关。

**验证**:headless Chrome 以 `--lang=en-US --accept-lang=en-US` 启动(页面实测
`navigator.language = en-US`、`html lang = en`、按钮 title = `New session`),
`scripts/e2e/manifest-flow.mjs` 与 `scripts/e2e/resilience.mjs` **均通过**;
后者的 `DOT green after rebuild ✓` 证明重连点击确实生效(否则 overlay 不会消失)。
