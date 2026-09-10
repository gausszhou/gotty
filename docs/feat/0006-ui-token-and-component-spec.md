# 优化 6:UI Token 与组件规格 —— 颜色 / 尺寸 / 控件收敛

> 状态:**已实施**(2026-09-11;§10 终端底色、§11 空行/内边距、§13 全量对齐 VSCode 均已落地)
>
> 背景:前端目前的 token 层只有约 40 个**颜色**变量(`apps/web/src/style/index.css`),
> 尺寸、间距、圆角、字号、共享控件样式**全部没有 token**;同一个控件在每个组件里
> 被重复实现一遍;散落着 `#ccc`/`#000000`/`rgba(0,0,0,.5)` 这类字面量;
> 全仓没有任何 `:focus-visible`。本文件先把方法论与规格写清楚,**看过再改代码**。
>
> 本文件与 [flipbit03/terminal-use](https://github.com/flipbit03/terminal-use)
> 的那轮调研无关:0001–0005 是输入侧/观察侧对标,本项来自 UI 方法论大纲(见 §9)。

---

## 0. 一句话方法论

颜色 / 尺寸 / 控件全部收敛成 token,组件不再写字面量。

## 1. 范围(先读这段)

本仓库前端只有这五个界面单元,**规格只针对它们**:

| 单元 | 文件 |
|---|---|
| 顶部页签栏 | `apps/web/src/components/TabBar.vue` |
| 内容区 / 终端面 / 断开浮层 | `components/TerminalPane.vue`、`components/Terminal.vue` |
| 设置弹窗 | `components/SettingsModal.vue` |
| 空态卡片 | `App.vue`(`.empty-card`) |
| 截图专用页 | `components/CaptureView.vue` |

**明确不适用**(用户已澄清这些是"指错了项目 / 沿用别的设计"):左栏 / 侧栏、两行列表项、
侧栏拖拽调宽、折叠入口唯一性、右键状态菜单。**本仓库没有侧栏,不要写侧栏规格。**

### 1.1 硬约束:e2e 选择器契约

`scripts/e2e/*.mjs` 依赖这些类名。改造时**要么保留类名,要么同步改脚本**:

`.tab`、`.tab-close`、`.tab-actions-left .icon-btn`、`.empty-card`、`.pane-overlay`、
`.state-dot`、`.terminal-pane`

这是本次改造最容易踩的坑:**改类名会静默打断 e2e**,而 e2e 需要真实 Chrome,平时不会跑。

## 2. 改造顺序

**先 token 层 → 再页签栏与内容区/终端面 → 最后弹窗与空态。**

反序的代价是改两遍:组件里写死字面量之后,每加一个 token 都要回头重写一遍组件。
(原大纲第二步里的"左栏"在本仓库不存在,已删除。)

## 3. 现状盘点(逐文件核对,带行号)

### 3.1 token 层

`apps/web/src/style/index.css` 共 107 行、约 40 个变量,**全部是颜色**(外加滚动条三色)。
覆盖的语义:`--bg-app/--bg-bar/--bg-tab/--bg-tab-active/--bg-tab-hover/--bg-input/--bg-dialog`、
`--border-dialog/--border-tab/--bg-bar-border`、四级文本 `--fg/--fg-dim/--fg-muted/--fg-bright`(+`--fg-hint`)、
`--accent/--overlay`、状态点与网络分级、滚动条。

**缺失**:尺寸、间距、圆角、字号、层级(z-index)、过渡时长,以及**任何共享控件样式**。

### 3.2 散落的字面量

| 位置 | 字面量 | 问题 |
|---|---|---|
| `Terminal.vue:53` | `background: '#000000'`(xterm 暗色底) | 与 `--bg-app: #1e1e1e` 不等 → 见 §3.6 |
| `Terminal.vue:44` | `background: '#ffffff'`(xterm 亮色底) | 亮色下恰好等于 `--bg-app`,属巧合 |
| `Terminal.vue:45-49,54-57` | `#1a1a1a`/`#cfe3f7`/`#cccccc`/`#333333` | 选择色/光标色不在色板里 |
| `TerminalPane.vue:212` | `background: #000000` | 同上,第二处硬编码 |
| `TerminalPane.vue:239`、`SettingsModal.vue:177` | `box-shadow: 0 8px 24px rgba(0,0,0,.5)` | 同一浮层阴影写了两遍 |
| `TerminalPane.vue:278,294` | `filter: brightness(1.1)` / `brightness(1.15)` | 用滤镜伪造 hover 色,不跟随主题 |
| `App.vue:368,379` | `border-color: #ccc` / `color: #ccc` | 亮暗共用同一个值 |
| `App.vue:398`、`CaptureView.vue:132` | `color: #f48771` | 错误色写了两遍,且不在色板里 |
| `CaptureView.vue:108,122` | `background: #000`、`color: #666` | 截图页自成一派 |
| `TabBar.vue:306` | `font-family: 'SF Mono', Consolas, monospace` | 字体栈第三份,见 §3.4 |

### 3.3 同一个控件被重复实现

| 控件 | 位置 | 说明 |
|---|---|---|
| `.icon-btn` | `TabBar.vue:383` | 只有页签栏这一份,但它是"共享控件"的候选 |
| `.tab-close` | `TabBar.vue:345` | 与 `.icon-btn` 高度重叠的另一套写法 |
| `.option-btn` / `.option-btn-save` | `SettingsModal.vue:237,296` | 设置项按钮,自成一套 |
| `.btn-primary` / `.btn-secondary` | `TerminalPane.vue:265,281` | 浮层按钮,又一套 |

也就是说"按钮"这一个概念,在四个文件里有四套互不相干的实现。

### 3.4 字体栈有三份且互不相同

| 位置 | 值 |
|---|---|
| `App.vue:282` | `-apple-system, BlinkMacSystemFont, 'Segoe UI', 'Helvetica Neue', sans-serif` |
| `Terminal.vue:31` | `Menlo, Monaco, Consolas, "DejaVu Sans Mono", "Courier New", monospace`(TS 常量,喂给 xterm) |
| `TabBar.vue:306` | `'SF Mono', Consolas, monospace` |
| 另有 | `App.vue:363`、`SettingsModal.vue:245,283` 的 `font-family: inherit` |

### 3.5 焦点提示缺失(可访问性)

全仓**没有任何 `:focus-visible`**。唯一涉及焦点的写法是
`SettingsModal.vue:291` 的 `.title-input:focus { outline: none }` —— 把浏览器默认焦点环
抹掉且没有替换。键盘用户在所有按钮、页签关闭按钮上都看不到当前位置。

### 3.6 已核对出的三个具体不等价

**① 暗色下终端面与内容区不同色。**`Terminal.vue:53` 的 xterm 底是 `#000000`,
`TerminalPane.vue:212` 也是 `#000000`,而 `--bg-app` 是 `#1e1e1e` → 终端与其周边之间
有一道可见色差。亮色下两者都是 `#ffffff`,所以**这个缺陷只在暗色出现**(不要用亮色主题
验收它)。

**② 页签的"顶部 1px 强调条"其实已经实现了。**`TabBar.vue:288-292`:

```css
.tab.active {
    background: var(--bg-tab-active);   /* #1e1e1e */
    color: var(--fg-bright);
    border-top: 1px solid var(--accent); /* 已经是强调条 */
}
```

而且 `--bg-tab-active: #1e1e1e` 恰好等于 `--bg-app`,与 VSCode
`tab.activeBackground` 的口径一致(见 §4.1)。**所以这一项不是推翻重来**,真正不对的是
另外三个值:

- `--bg-tab: #2d2d2d`(非活动页签底色)既不是 chrome(`--bg-bar: #252526`)也不是内容区底色,
  是凭空多出来的第三个颜色;
- 非活动 `.tab` 带 `border-top: 1px solid var(--border-tab)`(`TabBar.vue:270`),
  顶部多一条线,把"强调条 = 活动页签"的语义稀释掉(VSCode 的非活动页签**没有**顶部边框);
- `--bg-tab-hover: #3a3d41` 不在色板里(VSCode 用 `tab.hoverBackground` = 内容区底色)。

**③ `#ccc` 在亮暗两套主题下共用。**`App.vue:368/379` 的 hover 边框与图标色写死 `#ccc`:
暗色下偏亮、亮色下偏灰,都不跟随主题。

## 4. Token 层规格

### 4.1 可直接抄的色板(VSCode Dark/Light Modern 实测值)

从 vscode 主仓库真文件取回并逐键核对过(来源见 §9),可直接作为色板基线:

| 语义 | key | Dark Modern | Light Modern |
|---|---|---|---|
| 编辑器/终端面 | `editor.background` | `#1F1F1F` | `#FFFFFF` |
| chrome(bar/tab strip/侧栏) | `editorGroupHeader.tabsBackground`、`sideBar.background`、`titleBar.activeBackground`、`panel.background` | `#181818` | `#F8F8F8` |
| 活动页签底色 | `tab.activeBackground` | `#1F1F1F`(= 编辑器底色) | `#FFFFFF`(= 编辑器底色) |
| 非活动页签底色 | `tab.inactiveBackground` | `#181818` | `#F8F8F8` |
| 悬停页签底色 | `tab.hoverBackground` | `#1F1F1F` | `#FFFFFF` |
| **顶部 1px 强调条** | `tab.activeBorderTop` | `#0078D4` | `#005FB8` |
| 页签分隔线 | `tab.border` | `#2B2B2B` | `#E5E5E5` |
| 正文 | `foreground` | `#CCCCCC` | `#3B3B3B` |
| 次要文本 | `descriptionForeground` | `#9D9D9D` | `#3B3B3B` |
| 最弱/行号 | `editorLineNumber.foreground` | `#6E7681` | `#6E7681` |
| 占位符 | `input.placeholderForeground` | `#989898` | `#767676` |
| 禁用 | `disabledForeground`(注册表默认) | `#CCCCCC80` | `#61616180` |
| 强调/焦点 | `focusBorder`、`button.background` | `#0078D4` | `#005FB8` |
| 主按钮悬停 | `button.hoverBackground` | `#026EC1` | `#0258A8` |
| 次按钮 | `button.secondaryBackground` / hover | `#00000000` / `#2B2B2B` | `#E5E5E5` / `#CCCCCC` |
| 输入框 | `input.background` / `input.border` | `#313131` / `#3C3C3C` | `#FFFFFF` / `#CECECE` |
| 浮层 | `editorWidget.background` / `widget.border` | `#202020` / `#313131` | `#F8F8F8` / `#E5E5E5` |
| 列表悬停/选中 | `list.hoverBackground` / `list.activeSelectionBackground` | `#2A2D2E` / `#04395E` | `#F2F2F2` / `#E8E8E8` |
| 错误/警告 | `errorForeground` / `editorWarning.foreground` | `#F85149` / `#CCA700` | `#F85149` / `#BF8803` |
| 浮层阴影 | `widget.shadow`(注册表派生) | `#0000005C` | `#00000029` |
| 滚动条滑块 | `scrollbarSlider.background` / hover | `#79797966` / `#646464B3` | `#64646466` / `#646464B3` |

**关键结论(必须在实施时想清楚,否则会理解反)**:`tab.activeBorderTop` 就是"顶部 1px 强调条";
`tab.activeBackground` 在两个主题里**都等于编辑器底色**(不是 chrome 底色)。
所以"**同底色**"的准确口径是:**活动页签与内容区(终端面)同色,与 chrome(strip)不同色**。
只写"同底色"三个字,实施时很可能被理解成"同 strip 底色"而做反。

`scrollbarSlider.*`、`list.hoverBackground`(dark)、`widget.shadow`、`disabledForeground`
**不在主题 JSON 里**,来自颜色注册表默认值;alpha 用 8 位 `#RRGGBBAA`(**alpha 在后**)。

### 4.2 文本色收敛到四级

现有 `--fg / --fg-dim / --fg-muted / --fg-bright` 再加一个 `--fg-hint` 共五个,语义有重叠
(`--fg-dim` #969696 与 `--fg-muted` #6e7681 与 `--fg-hint` #8b949e 三者都在当"次要文本"用)。
收敛为四级并明确各自用途:

| token | 用途 | Dark | Light |
|---|---|---|---|
| `--fg` | 正文 | `#CCCCCC` | `#3B3B3B` |
| `--fg-2` | 次要(描述、页签非活动) | `#9D9D9D` | `#3B3B3B` |
| `--fg-3` | 最弱(行号、空态提示) | `#6E7681` | `#6E7681` |
| `--fg-disabled` | 禁用 | `#CCCCCC80` | `#61616180` |

`--fg-bright` 保留(`#FFFFFF`/`#111111`,用于活动页签标题):它是"强调"而非"层级",
与上面四级不是一回事。迁移时**逐处判断**旧变量属于哪一级,不要机械替换。

### 4.3 状态色

现有 `--dot-idle/running/dead` 与 `--net-good/fair/bad` 语义清楚,保留;但把 `#f85149`
这类重复值改为引用同一个错误色 token(`--error`),避免"错误色在三个文件里各写一遍"
(见 §3.2),以及"状态点红"与"错误文本红"将来各自漂移。

### 4.4 尺寸 token(当前完全没有,本项新增)

**原则:数值取自现有实现,不发明新尺寸**(避免"改了 token 顺带改了视觉"):

| token | 值 | 取自 |
|---|---|---|
| `--bar-h` | `30px` | `.tab-bar { height: 30px }` |
| `--radius-sm` / `--radius-md` / `--radius-lg` | `3px` / `4px` / `8px` | `.icon-btn`、滚动条滑块、`.empty-card` |
| `--font-size-sm` / `--font-size-md` / `--font-size-lg` | `12px` / `13px` / `15px` | `.empty-card-hint`、`.tab`、`.empty-card-title` |
| `--font-size-icon` | `14px` | `.icon-btn` |
| `--gap-xs` / `--gap-sm` / `--gap-md` | `2px` / `6px` / `8px` | `.tab-actions`、`.tab`、`.tab-actions` padding |
| `--shadow-overlay` | `0 8px 24px rgba(0,0,0,.5)`(暗) / `rgba(0,0,0,.29)`(亮) | 两处重复的浮层阴影 |
| `--transition-fast` | `0.15s` | `.empty-card { transition: … 0.15s }` |
| `--z-sticky` / `--z-overlay` | `2` / `10` | `.tab-actions-left { z-index: 2 }`;浮层待定 |

### 4.5 共享控件样式 + **一个必须知道的优先级坑**

把按钮类提到全局(例如 `style/index.css` 的 `@layer` 或普通单类):
`.btn`、`.btn-primary`、`.btn-secondary`、`.icon-btn`、`.option-btn`、`.tab-close`。

> **坑:Vue `<style scoped>` 会给选择器加上 `[data-v-xxxxxxx]`,特异性高于全局单类选择器。**
> 所以组件里**残留的同名类必须删掉**,否则全局定义永远被盖住 —— 表现是"改了 token/全局样式
> 却毫无反应",很容易误判成缓存或构建问题。迁移时同步删除组件内的重复定义,
> 这也是 §3.3 那张表的实际用途。

### 4.6 细滚动条

现有细滚动条**只作用于 `.xterm-viewport`**(`index.css:74-107`)。页签栏
(`.tab-bar { overflow-x: auto }`)在多页签时会出现**默认样式**的粗滚动条,与终端区不一致。
把它抽成可复用的类/选择器组,同时覆盖 `.tab-bar`。

### 4.7 焦点环(新增)

定义 `--focus-ring: 1px solid var(--accent)`(可在亮色下用 `--accent` 的亮色值),
用 `:focus-visible` 应用到所有可聚焦元素;并且**必须同时删掉
`SettingsModal.vue:291` 的 `outline: none`**,或改为"仅 `:focus:not(:focus-visible)` 时去掉"。

### 4.8 字体栈(收敛)

- 界面:一套 sans(保留 `App.vue:282` 那套,它已是全站 `body` 值);
- 等宽:一套 mono,同时供 `Terminal.vue:31` 的 xterm `FONT_FAMILY` 与 `.net-status` 使用。
  两份必须**同源**(一份 CSS 变量 + 一份 TS 常量,值写在一处并加注释互相指向),
  否则改了其中一份就会不一致 —— 这正是 §3.4 的现状。

## 5. 组件规格

### 5.1 骨架尺寸表(取自现有值,改造后应完全一致)

| 单元 | 高度 | 字号 | 内边距 | 圆角 |
|---|---|---|---|---|
| 页签栏 `.tab-bar` | `--bar-h` 30px | — | — | — |
| 页签 `.tab` | 撑满 strip(`align-items: stretch`) | 13px | `0 8px` | 0 |
| 页签关闭 `.tab-close` | — | 11px | `2px 4px` | 3px |
| 图标按钮 `.icon-btn` | — | 14px | `2px 8px` | 3px |
| 网络状态 `.net-status` | — | 12px(mono) | `2px 8px` | 3px |
| 状态点 `.state-dot` | 8px × 8px,`border-radius: 50%` | — | — | — |
| 空态卡片 `.empty-card` | — | 标题 15px / 提示 12px | `28px 44px` | 8px |

`.empty-card` 有两个**别改掉**的细节(现有注释已说明原因):`min-width: 248px` 是按英文文案
实测定宽,用来避免切换语言时卡片跳动;`.empty-card-title { line-height: 1.4 }` 是固定行高,
避免中文字体默认行高更大导致两种语言卡片高度不同。

### 5.2 页签:同底色 + 顶部 1px 强调条(**附常见错误写法**)

**目标态**:

```css
.tab-bar { background: var(--chrome-bg); }        /* strip 用 chrome 底色 */
.tab     { background: var(--chrome-bg); border-top: 1px solid transparent; }
.tab:hover { background: var(--surface-bg); }      /* = 内容区底色 */
.tab.active { background: var(--surface-bg);       /* = 内容区底色(同底色) */
              border-top-color: var(--accent); }   /* 顶部 1px 强调条 */
```

**常见错误写法**(每条都说明为什么错):

| ❌ 写法 | 为什么错 |
|---|---|
| 活动页签用 chrome 底色(`--bg-bar`) | VSCode 的 `tab.activeBackground` 等于**内容区**底色。用 chrome 底色会让活动页签"浮起来",与下方终端面之间出现断层 —— 与"同底色"的意图正好相反 |
| 强调条用 `border-bottom` 或 `box-shadow: inset 0 -1px 0` | 强调条语义是"这个页签连着下面的内容区",必须在**顶部**;画在底部会与页签栏的 `border-bottom` 混在一起,看起来像分隔线 |
| 给非活动页签也留 `border-top: 1px solid var(--border-tab)` | 顶部就出现"两种线":非活动是分隔线、活动是强调条,视觉上无法一眼分辨哪个是强调条(现状 `TabBar.vue:270` 就是这个写法)。应为 `transparent`,只换颜色 |
| 用 `::after` + `position: absolute` 画强调条 | 页签栏 `overflow-x: auto` + 拖拽排序时绝对定位容易与滚动/插入指示(`.tab.drop-left` 的 `inset` 阴影)打架;`border-top` 在 `border-box` 下不参与布局,最稳 |
| 页签栏设高度但页签不设 `align-items: stretch` | 页签高度与 strip 不一致,强调条会短一截 |
| 在组件 `scoped` 里重定义 `.tab` | 见 §4.5 的特异性坑:全局 token 改了不生效 |

⚠️ 拖拽排序已有实现(`.tab.dragging` 半透明、`.tab.drop-left/.drop-right` 用
`box-shadow: inset ±2px 0 0 var(--accent)` 作插入指示),改用强调条时**注意两者都用 accent**,
不要让"插入指示"和"活动页签"混淆 —— 这是本仓库特有的约束。

### 5.3 终端面:同色 + 半字符内边距

- **同色**:xterm 的 `background` 必须等于内容区底色(`--bg-app` / `editor.background`),
  且 `TerminalPane.vue:212` 的硬编码 `#000000` 一并去掉。**当前暗色下 `#000000 ≠ #1e1e1e`**,
  这是 §3.6 ① 的缺陷。
- **半字符内边距**:终端是字符网格,任何内边距都应取**半个字符宽**
  (`padding: 0.5ch` 之类),否则文字网格与容器边缘不对齐、换行位置在小数像素上抖动。
  若不加内边距(现状),则必须保证 xterm 自己填满且底色一致,不能靠外层容器"补色"。
- xterm 配色目前硬编码在 `Terminal.vue:41-59` 的 TS 常量里(因为要同时喂给 xterm 和 CSS)。
  **值必须来自色板**:建议以 CSS 变量为唯一真源,在主题切换时读一次
  (`getComputedStyle`)喂给 xterm,避免"CSS 改了、终端没改"。

### 5.4 欢迎式空态

`.empty-card` 已经是"中心卡片 + 图标 + 标题 + 提示 + 点击创建"的结构,方向正确。
规格化要点:

- 虚线边框 + 悬停变实/变色,提示"可点击";但悬停色必须来自 token
  (现状 `#ccc` 见 §3.6 ③);
- 图标用纯 CSS 或 SVG,**不要用 emoji 字形当主图标**(跨平台字形差异大);现状是 `＋`
  (全角加号),在部分字体下会与标题基线不齐;
- 图标 + 标题 + 提示的 `gap`、字号、行高全部走 §5.1 的表;中英两套文案都要目视验收
  (卡片定宽就是为这个)。

## 6. 三个高价值交互(本仓库版)

原大纲的三个交互里,**侧栏拖拽调宽、折叠入口唯一性在本仓库不适用**(无侧栏)。
替换为下面三个真正有价值的:

1. **页签关闭按钮的键盘可达。**`.tab-close` 当前已从"悬停才显示"改为常驻显示
   (`TabBar.vue:355` 的注释记录了这个改动),方向是对的;剩下的缺口是它**没有焦点态**
   (§3.5)。补 `:focus-visible` 即可,不需要 hover 揭示。
   (原大纲"悬停揭示要配 `:focus-within` 保证键盘可达"的原则在这里体现为:
   既然选择了常驻显示,就只需补焦点环,更简单也更稳。)
2. **弹窗 Esc 与焦点管理,注意"下一帧挂 Esc"。**`SettingsModal.vue` 目前
   `outline: none` 且无 Esc 处理。加 Esc 关闭时**不要同步注册** ——
   否则"打开弹窗的那次按键"会在同一帧冒泡到新注册的处理器,把弹窗立刻关掉。
   正确做法是在**下一帧**(`requestAnimationFrame` / `nextTick`)再挂监听。
   同时要处理焦点陷阱(打开时聚焦首个可聚焦元素、关闭时归还给触发元素)。
3. **页签栏横向滚动的细滚动条。**见 §4.6。多页签时当前会出现默认粗滚动条,
   与终端区风格割裂。

## 7. 验收

- `pnpm --filter gotty-frontend build` 通过;`vue-tsc --noEmit` 无错。
- **e2e 选择器契约不变**(§1.1):`scripts/e2e/*.mjs` 无需修改即可通过
  (需真实 Chrome,本机跑 `make test-browser`)。
- 字面量收敛可机械校验:改造完成后,`#`/`rgba(` 开头的颜色值**只应出现在 token 文件**
  (例外:xterm 的 TS 常量,但它的值要从变量读取)。
- **两套主题都要目视验收**,尤其暗色 ——§3.6 的三个缺陷里有的是"只在暗色出现"的。
- 中英文两套文案都要看(空态卡片定宽、标题行高都是为它做的)。

## 8. 判断记录(不属于本文档范围)

- **侧栏 / 两行列表项 / 悬停揭示 / 右键菜单 / 拖拽调宽**:不适用本仓库(用户澄清"指错了项目")。
  仅迁移原则(如 `:focus-within` 的键盘可达)。
- **服务端主题同步**:2026-08-23 那批已移除(`docs/fix/index.md` §1),本次**不恢复** ——
  主题切换保持纯前端行为。
- **不做**:设计系统独立包、可视化主题编辑器、CSS 变量命名体系全面重命名
  (重命名的收益低于它带来的 diff 噪音与回归风险)。

## 9. 数据来源

- 色板与注册表默认值(需要重新核对时重取):
  - `https://raw.githubusercontent.com/microsoft/vscode/main/extensions/theme-defaults/themes/dark_modern.json`
  - `https://raw.githubusercontent.com/microsoft/vscode/main/extensions/theme-defaults/themes/light_modern.json`
  - 注册表默认值:`microsoft/vscode` 的
    `platform/theme/common/colors/{baseColors,editorColors,miscColors,listColors}.ts`
- 本文件的现状盘点(§3)是**逐文件核对当前工作树**得出的(行号对应 2026-09-11 的工作树),
  与早期交接文档中的描述有两处出入,已在 §3.6 更正:
  ① 页签的"顶部 1px 强调条"**已经实现**,真正错的是另外三个值(非活动底色、非活动顶部边框、hover 底色);
  ② 字体栈是**三份**且 `--fg-dim/--fg-muted/--fg-hint` 三个变量都在当次要文本用。

## 10. 已实施记录（2026-09-11，部分）

本轮只落地了**终端面底色**这一项,其余(尺寸 token、共享控件、焦点环、页签三值对齐)仍待实施。

- **决定**:终端内容区底色 = **`#0c0c0c`**(Windows Terminal 的默认底色),暗色主题;
  亮色主题仍为 `#ffffff`(**没有**跟着改成深色 —— 若要"终端永远深色",需要再确认,
  那会改变亮色主题下的整体观感)。
- **落地内容**:
  - `--bg-app: #0c0c0c`,新增 `--bg-terminal: var(--bg-app)`(终端面单独命名,便于将来独立调整);
    并把 `--bg-tab-active`、`--bg-bar-border`、`--scrollbar-track` 一并对齐到 `#0c0c0c`。
    原因:这四个值原先都等于 `#1e1e1e`(旧内容区底色),只改 `--bg-app` 会破坏
    "活动页签 / 页签栏底边 / 滚动条槽 = 内容区底色"这三处既有关系(暗色下会立刻看出来)。
  - `SettingsModal.vue` 的 `.option-btn.active` 原先**误用** `--bg-tab-active` 当选中项底色;
    新增 `--bg-selected` 解耦 —— 否则改页签颜色会连带改到设置弹窗。
  - `TerminalPane.vue` 去掉硬编码的 `#000000`,改用 `var(--bg-terminal)`。
  - **xterm 配色改为从 CSS 变量读取**(`--bg-terminal` / `--term-*`),不再在 `Terminal.vue`
    里另存一套 hex —— 这正是 §3.6 记录的"CSS 改了、终端没改"两份不一致的缺陷。
    读取时机是安全的:`applyTheme` 先写 `<html data-theme>` 再广播主题变化,且 `main.ts`
    在 mount 之前就应用了主题。
- **验证方式(可复用)**:
  用 CDP 驱动 headless Chrome 打开真实页面 → 点空态卡片建会话 → 读 computed style,
  并**截 1×1 区域回填到页面 canvas 取像素**。只读 computed style 证明不了 xterm 渲染器
  真的换了底色(canvas 的不透明清屏色会盖住 CSS 背景)。实测结果:
  `--bg-app`/`--bg-terminal`/`--scrollbar-track` 均为 `#0c0c0c`;
  `.terminal-pane`/`.xterm-viewport`/`.tab.active` 均为 `rgb(12, 12, 12)`;
  `.tab-bar` 仍为 `rgb(37, 37, 38)`(chrome 底色未动);**渲染像素 = `#0c0c0c`**。
- **顺带确认(不是缺陷)**:终端顶部那一行"空行"来自 **Git Bash 自己的 PS1** ——
  `etc/profile.d/git-prompt.sh` 里 `PS1="$PS1"'\n'` 以换行开头(提示符本身占两行)。
  **差分实测**(同一次会话建立流程,只看 row0):
  | 命令 | row0 |
  |---|---|
  | `cmd.exe /k echo ROW0-CMD` | `ROW0-CMD`(占位) |
  | `bash`(本机 PATH 解析到 `C:\Windows\System32\bash.exe`，即 **WSL** 的 bash) | WSL 代理告警文本(占位) |
  | Git Bash(`$SHELL` 绝对路径) | **`<EMPTY>`** |
  两个对照都占用了 row0，**只有 Git Bash 空着** → 排除"gotty/ConPTY 先输出一个换行"这一可能。
  Git Bash 会话实测为 row0 空、row1 `gauss@Mechrevo MINGW64 ~`、row2 `$ `、光标 row2col2,
  与截图一致；这正是 PS1 里那两个 `\n` 的结果。Windows Terminal 里同样如此,与 GoTTY 无关。
  若要去掉,应在用户级 `~/.bashrc` 里自定义 PS1(零产品改动),而不是改渲染层。
- **顺带发现(Windows 环境坑,非本仓库缺陷)**：在本机 `bash`(裸名)经 PATH 解析为
  `C:\Windows\System32\bash.exe`，也就是 **WSL 的 bash**(表现为 `root@Mechrevo:/mnt/c/...`,
  并打印 WSL 的 localhost 代理告警),不是 Git Bash。默认会话不受影响(它走 `$SHELL` 的绝对
  路径 = Git Bash)。但要注意 `internal/terminal` 的 `isBash()` 会按命令名给 `bash` 注入
  `PROMPT_COMMAND`，所以那条注入也会落到 WSL bash 上(无害,但意图本是 Git Bash)。

## 11. 已实施记录（续）：提示符空行 + 半字符内边距

用户看过 §10 后追加两项,均已实现并实测。

### 11.1 去掉提示符开头那个空行（顺带修掉一个 Windows 缺陷）

- **做法**：扩展 bash 会话注入的 `PROMPT_COMMAND`（`internal/terminal/terminal.go` 的
  `buildEnv`），在发送终端模式复位序列之前先去掉 Git Bash 提示符开头的换行转义。
  守卫条件写成"PS1 里仍存在**标题终止符 `\007\]` 紧跟换行**"，因此：
  - **幂等** —— 该序列被去掉后守卫不再成立，第二次提示符不会再吃掉 `$ ` 前面那个换行
    （实测连跑 3 次：`\n` 计数 2 → 1 → 1 → 1）；
  - **不误伤** —— 用户自定义提示符（不含该序列）原样不动（实测不变）。
  若用户自己设了 `PROMPT_COMMAND`，整段注入照旧不生效（以用户为准，原有行为）。
- **顺带修掉的 Windows 缺陷**：`isBash()` 原先只比较 `/bash`、`/bash.exe` **正斜杠**后缀，
  而 Windows 上默认命令是 `$SHELL` = `D:\...\Git\usr\bin\bash.exe`（反斜杠）→ 匹配失败
  → **整段 `PROMPT_COMMAND` 注入在 Windows 的默认 Git Bash 会话里一直被静默跳过**
  （也就是说 §10 之前那些"TUI 退出后鼠标乱字节/隐藏光标"的兜底在 Windows 上根本没生效）。
  改为按 `filepath.Base` 取基名比较，并接受 `bash.exe`。
- **实测（真实会话 + 屏幕 JSON）**：
  - 修复前：row0 `<EMPTY>`、row1 `gauss@Mechrevo MINGW64 ~`、row2 `$ `；
  - 修复后：row0 `gauss@Mechrevo MINGW64 ~`、row1 `$ `；
  - 输入 `echo SECOND-PROMPT` 后，第 0~光标行依次为
    `gauss@…` / `$ echo SECOND-PROMPT` / `SECOND-PROMPT` / `gauss@…` / `$ `，
    **光标行以上没有任何空行**（证明第二个提示符同样干净 = 幂等）。
- 回归测试：`internal/terminal/terminal_test.go` 新增 `TestIsBash`（含 Windows 反斜杠路径）
  与 `TestBuildEnvBashPromptCommand`（断言守卫/去换行/复位序列仍在/不含 `?1049l`/非 bash 不注入/
  用户设置时不覆盖）。

### 11.2 终端面半字符内边距

- **做法**：`.terminal-pane` 加 `padding: calc(var(--term-cell-w) / 2)`。
- **为什么不是 `0.5ch`**：`ch` 是字体 "0" 的步进宽度，而 xterm 内部把字符格宽度**取整**了。
  实测 `1ch = 7.70px` 而真实格子 = `7.00px`（差约 10%），用 `0.5ch` 会得到 0.55 个格子。
  因此改为由 `Terminal.vue` 在每次 `fit()` 之后用 **xterm 的真实几何**写回变量：
  `--term-cell-w = .xterm-screen 宽 / 列数`（两者都是公开 DOM/API）。格子宽只由字体与字号
  决定、与容器尺寸无关，所以每次 fit 重算是幂等的。`index.css` 里 `--term-cell-w: 1ch`
  只是首帧兜底。
- **fit() 安全性**：FitAddon 量的是 `.xterm` 父元素（`.terminal-container`）的 computed
  尺寸，百分比宽高按父元素 content box 解析、`box-sizing` 又是 `border-box`，所以这里的
  padding 会被自动扣掉，不会算出超宽的行列数。
- **同色前提**：内边距区域由 `.terminal-pane` 的 `background: var(--bg-terminal)` 补齐，
  与终端面同色，所以它看起来是"留白"而不是"边框"（对应 §5.3 的"同色 + 半字符内边距"）。
- **副作用处理**：为了让首帧兜底的 `ch` 量得准，`.terminal-pane` 上设了等宽字体与终端字号；
  这会传染给断开弹窗的文字，因此 `.pane-overlay` 显式恢复 `var(--font-ui)`。
  同时把字体栈收成单一真源（`--font-ui` / `--font-mono` / `--term-font-size`，
  `Terminal.vue` 用 `getComputedStyle` 读取，落实 §4.8）。
- **实测（真实渲染 + CDP 取像素/几何）**：`padding = 3.5px`，字符格 `= 7.00px`
  （`875px / 125 列`）→ **正好半个字符格**（naive `0.5ch` 会是 3.85px），左右对称；
  终端面渲染像素仍为 `#0c0c0c`。

### 11.3 复现方式

`.tmp/verify-ui2.mjs`（ASCII/Node，用仓库 e2e 同款裸 CDP）：
起服务（`--port 8080`）→ headless Chrome（`--remote-debugging-port=9222`）→
点空态卡片建会话 → 读屏幕 JSON 校验空行 → 用 `/keys` 打一条命令校验第二个提示符 →
读 computed padding 与 `.xterm-screen` 几何 → 截 1×1 取渲染像素。
本轮结果：`A1/A2/A3/A4/B1/B2/C1` 全部 PASS。

## 12. 与 VSCode 的差距清单（2026-09-11 重新取原文核对）

上一节之前的数据来自更早一轮的抓取。本轮**重新从 vscode 主仓库取原文**核对，并厘清了一个
此前含糊的点：**`dark_modern.json` / `light_modern.json` 是 `include` 继承的**
（`modern → plus → vs`），所以未覆盖的键必须沿链求值，不能只看 modern 文件；
另有若干键**根本不在主题文件里**，来自颜色注册表默认值——两者是不同的层，混用会得错值。

### 12.1 终端 ANSI 16 色（**当前完全没有设**，是观感差距最大的一项）

来源：`src/vs/workbench/contrib/terminal/common/terminalColorRegistry.ts` 的 `ansiColorMap`。
xterm.js 的 `theme` 支持这 16 项，可直接喂入；现在没有设，所以用的是 xterm.js 内置调色板，
`ls --color` / `git status` / vim / htop 的配色与 VSCode 集成终端**明显不同**。

| 索引 | key | Dark | Light |
|---|---|---|---|
| 0 | `ansiBlack` | `#000000` | `#000000` |
| 1 | `ansiRed` | `#cd3131` | `#cd3131` |
| 2 | `ansiGreen` | `#0DBC79` | `#107C10` |
| 3 | `ansiYellow` | `#e5e510` | `#949800` |
| 4 | `ansiBlue` | `#2472c8` | `#0451a5` |
| 5 | `ansiMagenta` | `#bc3fbc` | `#bc05bc` |
| 6 | `ansiCyan` | `#11a8cd` | `#0598bc` |
| 7 | `ansiWhite` | `#e5e5e5` | `#555555` |
| 8 | `ansiBrightBlack` | `#666666` | `#666666` |
| 9 | `ansiBrightRed` | `#f14c4c` | `#cd3131` |
| 10 | `ansiBrightGreen` | `#23d18b` | `#14CE14` |
| 11 | `ansiBrightYellow` | `#f5f543` | `#b5ba00` |
| 12 | `ansiBrightBlue` | `#3b8eea` | `#0451a5` |
| 13 | `ansiBrightMagenta` | `#d670d6` | `#bc05bc` |
| 14 | `ansiBrightCyan` | `#29b8db` | `#0598bc` |
| 15 | `ansiBrightWhite` | `#e5e5e5` | `#a5a5a5` |

同文件另给：`terminal.foreground` = **dark `#CCCCCC` / light `#333333`**
（我们亮色现在写的是 `#1a1a1a`，应对齐到 `#333333`）；
`terminal.selectionBackground` 继承 `editorSelectionBackground`（未取具体值）。

### 12.2 注册表默认值（**不在主题 JSON 里**，需另取）

来源：`src/vs/platform/theme/common/colors/{miscColors,baseColors,listColors}.ts`。

| key | Dark | Light |
|---|---|---|
| `scrollbarSlider.background` | `#79797966`（`#797979` @40%） | `#64646466`（`#646464` @40%） |
| `scrollbarSlider.hoverBackground` | `#646464B3`（@70%） | `#646464B3`（@70%） |
| `scrollbarSlider.activeBackground` | `#BFBFBF66`（@40%） | `#00000099`（@60%） |
| `disabledForeground` | `#CCCCCC80` | `#61616180` |
| `list.hoverBackground` | `#2A2D2E` | `#F0F0F0`（注：**light 主题**里是 `#F2F2F2`，主题优先） |
| `list.activeSelectionBackground` | `#04395E` | `#0060C0`（注：**light 主题**里是 `#E8E8E8`） |

`widget.shadow` **本轮未取到**（不在 miscColors.ts），暂不引用具体值。

### 12.3 已核对的 Dark/Light Modern 主键（沿继承链求值）

| key | Dark | Light |
|---|---|---|
| `editor.background` | `#1F1F1F` | `#FFFFFF` |
| `editorGroupHeader.tabsBackground` | `#181818` | `#F8F8F8` |
| `tab.activeBackground` | `#1F1F1F` | `#FFFFFF` |
| `tab.inactiveBackground` | `#181818` | `#F8F8F8` |
| `tab.hoverBackground` | `#1F1F1F` | `#FFFFFF` |
| `tab.activeBorderTop` | `#0078D4` | `#005FB8` |
| `tab.border` | `#2B2B2B` | `#E5E5E5` |
| `foreground` | `#CCCCCC` | `#3B3B3B` |
| `descriptionForeground` | `#9D9D9D` | `#3B3B3B` |
| `input.background` / `input.border` | `#313131` / `#3C3C3C` | `#FFFFFF` / `#CECECE` |
| `editorWidget.background` / `widget.border` | `#202020` / `#313131` | `#F8F8F8` / `#E5E5E5` |
| `errorForeground` | `#F85149` | `#F85149` |
| `focusBorder` / `button.background` | `#0078D4` | `#005FB8` |
| `button.hoverBackground` | `#026EC1` | `#0258A8` |

### 12.4 据此得出的待优化项（按"看得见的差距 / 成本"排序）

1. **终端 ANSI 16 色**（§12.1）：现在完全没设，一块就拉平终端观感。→ 新增 `--term-ansi-*` token。
2. **页签栏三值 + 非活动顶边**：`--bg-bar #252526 → #181818`、`--bg-tab #2d2d2d → #181818`(同 strip)、
   `--bg-tab-hover #3a3d41 → #1F1F1F`(同内容区)、`--border-tab #333 → #2B2B2B`，
   并去掉非活动 `.tab` 的 `border-top`（现在它稀释了"强调条=活动页签"的语义，见 §5.2）。
3. **焦点环**（§4.7）：全仓仍无 `:focus-visible`，且 `SettingsModal.vue:291` 是 `outline: none`。
4. **滚动条滑块半透明化**（§12.2）：我们是不透明 `#4d4d4d`/8px，VSCode 是带 alpha 的浮层；
   并把细滚动条从 `.xterm-viewport` 推广到页签栏横向滚动（§4.6）。
5. **输入框 / 弹窗 / 浮层阴影 → Dark Modern 值**：`--bg-input #3c3c3c → #313131`（现在把
   `input.border` 的色当成了底色）、`--bg-dialog #252526 → #202020`、
   `--border-dialog #454545 → #313131`；两处重复的 `0 8px 24px rgba(0,0,0,.5)` 收敛为一个 token。
6. **错误色 / 禁用色收敛**：`#f48771`（`App.vue:398`、`CaptureView.vue:132` 两处写死）→
   `errorForeground #F85149`；禁用态用 `disabledForeground` 而不是 `opacity: .6`。
7. **弹窗选中项**：`--bg-selected #1e1e1e → list.activeSelectionBackground`（dark `#04395E`）。
8. **页签底色过渡**：VSCode 页签有 `transition: background-color 100ms linear`，目前只有
   `.empty-card` 有 transition。
9. **剩余写死字面量**：`App.vue` 的 `#ccc`(×2)、`CaptureView.vue` 的 `#000`/`#666`、
   `TerminalPane.vue`/`SettingsModal.vue` 用 `filter: brightness()` 伪造 hover（§3.2）。

**刻意保留的偏离**：终端底色 `#0c0c0c`（用户指定，比 VSCode `editor.background #1F1F1F` 更暗）；
活动页签 = 内容区底色（已与 VSCode 一致）；`.empty-card` 的中英定宽与固定行高（为双语稳定，
VSCode 的 Welcome 页是另一套布局，不建议照抄）。

**取数方式(可复现)**：网页直连 `raw.githubusercontent.com` 在本机不通，改用 GitHub Contents API
（`Accept: application/vnd.github.raw`）。注意 PowerShell 5.1 下 `Invoke-WebRequest.Content`
可能返回 **byte[]**，需 `[Text.Encoding]::UTF8.GetString()` 解码后再 `ConvertFrom-Json`。

> ⚠️ 本节末尾那段「刻意保留的偏离」**已被用户取消**（原话：「刻意保留的偏离不要了」）。
> 终端底色随之一并改成 VSCode 的 `editor.background`，见 §13.1。
> 另：主题 JSON 是 **JSONC**（带 `//` 与 `/* */` 注释），`ConvertFrom-Json` / `JSON.parse`
> 会直接报错；本项目 `.tmp` 下的解码脚本做了「区分字符串内斜杠」的注释剥离，重取数据时要用它。

## 13. 已实施记录（2026-09-11）：全量对齐 VSCode

§12.4 那份清单**全部落地**，并在此过程中用原文重核出**两处自己写错的值**（§13.1）。
「刻意保留的偏离」按用户要求取消 —— 现在除下面 §13.5 明确列出的三项外，颜色与尺寸
都来自 VSCode Dark/Light Modern。

### 13.1 重新核对时发现的两处错值（此前记在 §12.3，是错的）

| 项 | §12.3 原先记的 | 原文实际值 | 说明 |
|---|---|---|---|
| 页签栏下边线 | *(未记录，实现时按"VSCode 无分隔线"处理成内容区色)* | `editorGroupHeader.tabsBorder` = dark `#2B2B2B` / light `#E5E5E5` | VSCode **有**这条线，取 `tab.border` 同值即可 |
| 亮色非活动页签文字 | *(未记录，实现时套用了 `descriptionForeground`)* | `tab.inactiveForeground` = dark `#9D9D9D` / **light `#868686`** | 亮色的 `descriptionForeground` 是 `#3B3B3B`，**与正文同色** → 套用它会让亮色下活动/非活动页签文字一样深，必须单列 token |
| 亮色强调文字 | `--fg-bright: #111111`（凭空值） | `tab.activeForeground` = light `#3B3B3B` | Light Modern 里没有"比正文更亮"的文字色，强调靠 `font-weight: 600`；`#111111` 是编的 |

顺带把 `button.border`（dark `#ffffff1a` / light `#0000001a`）与 `button.foreground`（两主题 `#FFFFFF`）
也取了回来 —— 前者是次按钮的边框，后者是主按钮文字色（亮色下**不能**用 `--fg-bright`，那是 `#3B3B3B`）。

### 13.2 token 层（`apps/web/src/style/index.css`）

- **取消的偏离**：`--bg-app: #0c0c0c`（§10 的一次性指定）→ `#1f1f1f`(`editor.background`)，
  `--bg-terminal` 跟随；`--bg-bar-border: #1f1f1f` → `#2b2b2b`。
- **文本层级从 5 个收敛到 3+3**：删掉 `--fg-dim`(§969696) / `--fg-hint`(#8b949e) / `--fg-muted`(#6e7681)，
  改为 `--fg`(foreground) / `--fg-2`(descriptionForeground) / `--fg-3`(editorLineNumber.foreground)
  + `--fg-placeholder` / `--fg-disabled` / `--fg-bright`。
  **映射**：`--fg-dim`→`--fg-2`、`--fg-hint`→`--fg-2`（两者都是"次要文本"，VSCode 只有一级）、
  `--fg-muted`→`--fg-3`。原先 3 个变量挤在同一层级本身就是偏离。
- **新增**：`--fg-tab`(tab.inactiveForeground)、`--border-btn`、`--fg-on-accent`、`--term-ansi-*`(16 个)、
  `--bg-btn-secondary(-hover)`、`--bg-list-hover`、`--hover-toolbar`、`--selection-bg`、
  `--accent-hover`、`--shadow-overlay`、`--transition-fast` 与尺寸/圆角/字号/间距 token。
- **删掉**未使用的 `--border-panel`、`--scrollbar-track`（VSCode 的轨道是透明的）。
- **全局新增**：`:focus-visible { outline: 1px solid var(--accent); outline-offset: -1px }`
  （输入框排除）、`::selection { background: var(--selection-bg) }`。
- **滚动条**：滑块改半透明浮层（`--scrollbar-thumb` 等），轨道透明，细滚动条从
  `.xterm-viewport` 推广到 `.tab-bar`（页签栏 6px，终端 10px）。

### 13.3 组件

| 文件 | 关键改动 |
|---|---|
| `TabBar.vue` | 非活动页签去掉 `border-top`（改 `transparent`，只换色不改高度）；活动页签 `tab.activeBorderTop` 强调条 + `inset 0 -1px 0 var(--bg-tab-active)` **盖掉页签栏下边线**（= `tab.activeBorder` 的语义，活动页签与内容区连成一体）；分隔线 `tab.border`；`transition: background-color var(--transition-fast) linear`；关闭按钮/图标按钮 hover 改用 `toolbar.hoverBackground` |
| `TerminalPane.vue` | 两个 `filter: brightness()` 伪造 hover → `button.hoverBackground` / `button.secondaryHoverBackground`；次按钮补 `button.border`；主按钮文字改 `--fg-on-accent`（亮色下不能用 `--fg-bright`）；两处重复的 `0 8px 24px rgba(0,0,0,.5)` → `--shadow-overlay`(`widget.shadow`) |
| `Terminal.vue` | `terminalTheme()` 补 **ANSI 16 色**（`--term-ansi-*`）；**移除** `selectionForeground`（VSCode 该项默认 `null` = 保留字形颜色，与 xterm 缺省一致） |
| `SettingsModal.vue` | 选项按钮 → `button.secondaryBackground` + `button.border`，选中项 → `list.activeSelectionBackground/Foreground`；输入框边框 `input.border`；`:disabled` 用 `disabledForeground` 而不是 `opacity: .6`；`outline: none` 旁补上焦点边框并写清"去掉外环后仍有可见焦点指示" |
| `App.vue` / `CaptureView.vue` | 空的 `#ccc`/`#f48771`/`#000`/`#666` 全部 token 化；字体栈重复定义 → `--font-ui` |

**页签强调条的一个实现坑（本仓库特有）**：`.tab.drop-left` / `.tab.drop-right` 也用 `box-shadow: inset`
画插入指示，而 `box-shadow` **不能叠加、只能整体覆盖** —— 两者写在同一条 `.tab` 上时，
`.tab.active` 会吃掉插入指示。解法是把"额外阴影"抽成 `--tab-extra-shadow` 变量，
两条规则都引用它，于是 `active + drop-left` 能同时成立。

### 13.4 验证（真实渲染，非读代码）

`.tmp/verify-vscode-align.mjs`（本轮新写，Node + 裸 CDP，与仓库 e2e 同款连法）：
强制 `prefers-color-scheme: dark` → 清 localStorage → 硬刷新 → 点空态卡片建会话 →
**计算样式逐条对 VSCode key** + **1×1 截图回填 canvas 取渲染像素** + **CDP 强制伪类验焦点环** +
**经设置弹窗真实切换主题**。共 **57 项断言，全部 PASS**。本轮结果（摘）：

- 暗色页签：strip `#181818` / 下边线 `#2b2b2b` / 非活动底 `#181818` 文字 `#9d9d9d` 顶边透明 /
  活动底 `#1f1f1f` 文字 `#ffffff` 顶条 `#0078d4` / 活动页签底部盖线 `rgb(31,31,31) 0px -1px 0px 0px inset` /
  hover 底 `#1f1f1f` / 关闭按钮 hover `rgba(90,93,94,.314)`。
- 亮色页签：strip `#f8f8f8` / 下边线 `#e5e5e5` / 非活动文字 **`#868686`** / 活动文字 `#3b3b3b` 顶条 `#005fb8`。
- 弹窗（暗/亮各一遍）：`#202020`/`#f8f8f8` 底、`#313131`/`#e5e5e5` 边、圆角 6px、
  阴影 `rgba(0,0,0,.36) 0px 2px 8px`（亮色 `.16`）；选项按钮 `#00000000` 底 + `#ffffff1a` 边，
  选中 `#04395e`/`#e8e8e8` + `#ffffff`/`#000000`；输入框 `#313131`/`#ffffff` 底、`#3c3c3c`/`#cecece` 边、
  占位 `#989898`；禁用态文字 `rgba(204,204,204,.5)` 且 `opacity: 1`。
- 焦点环：`.tab-close` / `.icon-btn` 强制 `:focus-visible` → `1px solid rgb(0,120,212)`、`offset -1px`。
- **ANSI 16 色（像素级）**：让 shell 打印 16 个 ANSI 背景色块，
  按网格几何取**色块里的空格格**（不是 `X` 格 —— X 格采到的是字形抗锯齿混色，实测 `#9fcccc` 之类）：
  暗色 16 值全部命中，亮色 `101` 命中 `#14ce14`（证明主题切换真的传到了 xterm）。
- 终端面渲染像素：暗 `#1f1f1f`、亮 `#ffffff`；`.terminal-pane` padding `3.5px` = 半个字符格（`7.00px`）；
  首行不留空（§11 的回归）；主题默认**跟随系统**（headless 的系统色是浅色 → 必须显式模拟深色偏好，
  否则整轮断言都会跑在亮色下）。

**回归**：`go test ./...` 6/6 包通过；`go vet ./...` 干净；`vue-tsc --noEmit` 通过；
仓库自带浏览器 e2e `scripts/e2e/manifest-flow.mjs` 与 `resilience.mjs` **不改一行**全部通过
（§1.1 的选择器契约 `.tab`/`.tab-close`/`.tab-actions-left .icon-btn`/`.empty-card`/`.pane-overlay`/
`.state-dot`/`.terminal-pane` 一个没动）；Windows 端到端 `e2e.ps1` 15/15。

### 13.5 仍然存在的偏离（只有三项，都有理由）

1. **`.empty-card` 的 `min-width: 248px` 与 `line-height: 1.4`** —— 中英文切换不跳动的防抖，
   不是外观选择（VSCode 的 Welcome 页是另一套布局，整块照抄会把空态做没）。
2. **`--overlay`（浮层遮罩）与状态点 / 网络分级色** —— VSCode 没有对应 key，属产品自有语义，
   颜色借用 `errorForeground` / `editorWarning.foreground` / `ansiGreen`。
3. **`box-shadow` 上的两条 1ch 级细节**：页签栏横向滚动条（VSCode 靠溢出箭头，我们没那套，只能给一条 6px 细的）。

### 13.6 本轮之外的收获：`internal/browser` 在 Windows 上一直静默 SKIP

`findChrome()` 的候选只有 POSIX 路径 → Windows 上恒返回 `""` → 四个浏览器引擎用例
**全部 SKIP**，`make test-browser` 显示"通过"但一次都没跑过。补上 Chrome/Edge 的
Windows 安装位置后暴露出第二层问题：用例硬编码 `Command: "/bin/sh"`，
于是从 SKIP 变成 **FAIL**。两层都修了：新增 `internal/browser/browser_shell_test.go`
把"原样写字节 + 等待"翻译成平台命令（unix 用 `sh -c "printf '%s' '...'"`
单引号内不做转义；Windows 用 PowerShell 把字节数组写 stdout 句柄 —— **不能**用 cmd 的
`echo`/`set /p`，控制字符会被吃掉）。新增 `TestShellCmdScript` 直接断言两个编码器，
因为用例在任一平台上都只会走一个分支，光跑用例证明不了另一端拼得对。

> 坑：**gofmt 会重排 doc comment 并对之做 "smart quotes" 替换**。把 shell 的四段引号写法
> （闭引号 + 反斜杠转义的单引号 + 重开引号）写进文档注释里，会被改成语法错误的弯引号；
> 现已移进函数体的普通注释。仓库里其他 `—`/`…` 是既有文风，不是 gofmt 弄的（全仓扫过，无弯引号）。

**验证**：`go test -tags browser_e2e -v ./internal/browser/` → 5/5 PASS（此前 4 个 SKIP）。
