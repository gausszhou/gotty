<template>
  <div ref="barEl" class="tab-bar" @dragover="onBarDragOver" @drop.prevent="onDrop">
    <!-- 新建会话(固定在左侧) -->
    <div class="tab-actions tab-actions-left">
      <button class="icon-btn" :title="t('tab.new')" @click="create">＋</button>
    </div>
    <div
      v-for="(item, index) in displayList"
      :key="item.session.id"
      class="tab"
      :data-session-id="item.session.id"
      :class="[
        { active: item.session.id === activeSessionId },
        { dragging: dragId === item.session.id },
        { 'drop-left': dropIndex === index },
        { 'drop-right': dropIndex === index + 1 && dropIndex === displayList.length },
      ]"
      draggable="true"
      :title="t('tab.dragHint')"
      @click="open(item)"
      @dragstart="onDragStart($event, item.session.id)"
      @dragend="onDragEnd"
    >
      <span class="state-dot" :class="stateClass(item.session)"></span>
      <span class="tab-title">{{ item.title }}</span>
      <button class="tab-close" :title="t('tab.destroy')" @click.stop="destroy(item.session)">✕</button>
    </div>

    <!-- 右侧:网络状态 + 设置(主题/语言收纳在设置弹窗内) -->
    <div class="tab-actions">
      <div
        v-if="latency != null"
        class="net-status"
        :class="netClass"
        :title="t('tab.latency')"
      >
        {{ latency }}ms
      </div>
      <button
        class="icon-btn"
        :title="t('settings.open')"
        @click="emit('settings')"
      >
        <Settings :size="15" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, onBeforeUnmount } from 'vue'
import { Settings } from 'lucide-vue-next'
import { destroySession, type SessionInfo } from '../utils/api'
import { removeFromManifest, type ManifestEntry } from '../utils/manifest'
import { loadTabOrder, saveTabOrder } from '../utils/tabOrder'
import { t } from '../utils/i18n'
import { logger } from '../utils/logger'

const props = defineProps<{
    // 设备会话清单(localStorage)
    entries: ManifestEntry[]
    // 清单中服务端存活的会话(status 轮询结果)
    alive: SessionInfo[]
    // 本设备各打开视图的 WS 连接状态(id → 已附着),圆点即时变绿
    connected?: Record<string, boolean>
    activeSessionId?: string
    // 当前激活会话的实测 RTT(毫秒),展示在标题栏(顶部栏)右侧,颜色按延迟分级。
    latency?: number | null
}>()

// 延迟颜色分级:绿(<30ms) / 黄(30~100ms) / 红(≥100ms)
const netClass = computed(() => {
    const l = props.latency
    if (l == null) return ''
    if (l < 30) return 'net-good'
    if (l < 100) return 'net-fair'
    return 'net-bad'
})

const emit = defineEmits<{
    (e: 'open', detail: { session: SessionInfo; title: string }): void
    (e: 'destroy', session: SessionInfo): void
    // 请求新建会话(创建逻辑在上层 App 统一,顶部 ＋ 与空态卡片共用)
    (e: 'create'): void
    // 请求打开设置弹窗(主题 + 语言,由 App 统一管理)
    (e: 'settings'): void
    // 清单/存活状态已改变,请求上层重新拉取
    (e: 'changed'): void
}>()

const entryById = computed(() => new Map(props.entries.map((e) => [e.id, e])))

// commandBasename 取命令名作为默认标题(如 /bin/bash → "bash")。
function commandBasename(command: string): string {
    const base = command.split('/').pop() || ''
    return base || command
}

// 页签标题 = 程序最近设置的标题(OSC 0/2,Gnome-Shell 风格自动命名);
// 无程序标题时回退命令名。旧版遗留的 "会话N" 编号视为无标题。
function displayTitle(entry: ManifestEntry | undefined, command: string): string {
    const t = entry?.title
    if (t && !/^会话\d+$/.test(t)) return t
    return commandBasename(command)
}

// 页签 = 清单中存活的会话;默认按服务端 created_at 升序,
// 用户拖拽过的顺序(tab order,localStorage)优先生效,新会话追加在末尾。
const tabOrder = ref<string[]>(loadTabOrder())
const displayList = computed(() => {
    const alive = [...props.alive].sort((a, b) => (a.created_at > b.created_at ? 1 : -1))
    const known = alive
        .filter((s) => tabOrder.value.includes(s.id))
        .sort((a, b) => tabOrder.value.indexOf(a.id) - tabOrder.value.indexOf(b.id))
    const unknown = alive.filter((s) => !tabOrder.value.includes(s.id))
    return [...known, ...unknown].map((session) => {
        const entry = entryById.value.get(session.id)
        return { session, title: displayTitle(entry, session.command) }
    })
})

// ── 拖拽排序 ──
// dragId:正在拖拽的会话 id;dropIndex:当前悬停插入位置(0..displayList.length)。
// drag 结束后浏览器会在原处补发 click,用时间窗吞掉它,避免误切换页签。
const dragId = ref<string | null>(null)
const dropIndex = ref<number | null>(null)
let suppressClickUntil = 0

// 页签栏元素(dragover 冒泡到容器统一计算插入位,避免逐页签绑定)。
const barEl = ref<HTMLElement | null>(null)

function onDragStart(e: DragEvent, id: string) {
    // 从关闭按钮发起的"拖动"不视为页签拖拽:取消本次 dragstart,
    // 点击关闭仍走 click(已在关闭按钮上 @click.stop)。
    if ((e.target as HTMLElement).closest('.tab-close')) {
        e.preventDefault()
        return
    }
    if (!e.dataTransfer) return
    dragId.value = id
    e.dataTransfer.effectAllowed = 'move'
    // Firefox 要求 dragstart 中写入 data,否则不进入拖拽
    e.dataTransfer.setData('text/plain', id)
    logger.info('tab', 'drag start session=%s', id)
}

// 容器统一 dragover:计算鼠标所在的插入位(0..n),仅活动拖拽时响应
function onBarDragOver(e: DragEvent) {
    if (dragId.value === null || !e.dataTransfer) return
    const target = e.target as HTMLElement
    // 新建按钮/右侧操作区不参与拖拽落点
    if (target.closest('.tab-actions')) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    const bar = barEl.value
    const tabs = bar ? Array.from(bar.querySelectorAll('.tab')) : []
    const x = e.clientX
    let idx = tabs.length
    for (let i = 0; i < tabs.length; i++) {
        const rect = tabs[i].getBoundingClientRect()
        if (x < rect.left) {
            idx = i
            break
        }
        if (x <= rect.right) {
            idx = x < rect.left + rect.width / 2 ? i : i + 1
            break
        }
    }
    dropIndex.value = idx
    // 边缘自动滚动:接近左右边界时滚动页签栏,露出更多页签
    if (bar && x < bar.getBoundingClientRect().left + 20) {
        bar.scrollLeft -= 12
    } else if (bar && x > bar.getBoundingClientRect().right - 20) {
        bar.scrollLeft += 12
    }
}

// 落点:把拖拽会话移动到 dropIndex 处,持久化顺序
function onDrop(e: DragEvent) {
    e.preventDefault()
    const id = dragId.value
    const to = dropIndex.value
    // drop 后浏览器会补发 click,时间窗内吞掉,避免误切换页签
    suppressClickUntil = Date.now() + 350
    if (id === null || to === null) return resetDrag()
    const ids = displayList.value.map((d) => d.session.id)
    const from = ids.indexOf(id)
    if (from === -1 || from === to) return resetDrag() // 无效/原位放下,不变
    const next = [...ids]
    next.splice(from, 1)
    next.splice(from < to ? to - 1 : to, 0, id)
    tabOrder.value = next
    saveTabOrder(next)
    logger.info('tab', 'dragged session=%s %d -> %d, order=%s', id, from, to, next.join(','))
    resetDrag()
}

function onDragEnd() {
    // 拖拽取消/完成都清理状态;dragend 后同样可能补发 click
    suppressClickUntil = Date.now() + 350
    resetDrag()
}

function resetDrag() {
    dragId.value = null
    dropIndex.value = null
}

onBeforeUnmount(() => {
    dragId.value = null
    dropIndex.value = null
})

function open(item: { session: SessionInfo; title: string }) {
    if (Date.now() < suppressClickUntil) return // 吞掉拖拽结束后的补发 click
    if (item.session.state === 'destroyed' || item.session.exited) return
    emit('open', { session: item.session, title: item.title })
}

// 新建:交由上层 App(生成 id + 创建 + 写清单 + 打开)
function create() {
    logger.info('tab', 'create request → app')
    emit('create')
}

// 销毁:服务端销毁(记录保留,可凭 id 复活)+ 本设备清单移除
async function destroy(s: SessionInfo) {
    logger.info('tab', 'destroy session=%s (record kept, manifest entry removed)', s.id)
    await destroySession(s.id)
    removeFromManifest(s.id)
    emit('destroy', s)
    emit('changed')
}

function stateClass(s: SessionInfo): string {
    // 本设备已成功附着 → 即时绿(不等 status 轮询)
    if (props.connected?.[s.id]) return 'dot-running'
    if (s.exited || s.state === 'destroyed') return 'dot-dead'
    if (s.state === 'running') return 'dot-running'
    return 'dot-idle'
}
</script>

<style scoped>
.tab-bar {
    display: flex;
    align-items: stretch;
    height: var(--bar-h);
    flex: 0 0 auto;
    background: var(--bg-bar);
    border-bottom: 1px solid var(--bg-bar-border);
    overflow-x: auto;
    overflow-y: hidden;
    position: relative;
    user-select: none;
}

/* 取值全部对齐 VSCode Dark/Light Modern 的 tab.* 与 editorGroupHeader.tabs*:
   非活动页签与页签栏同色,活动页签与内容区同色,靠底色区分而不是靠边框。 */
.tab {
    display: flex;
    align-items: center;
    gap: var(--gap-sm);
    max-width: 200px;
    min-width: 100px;
    padding: 0 var(--gap-md);
    background: var(--bg-tab);              /* tab.inactiveBackground */
    color: var(--fg-tab);                   /* tab.inactiveForeground */
    border-right: 1px solid var(--border-tab); /* tab.border */
    /* 非活动页签没有顶条。用 transparent 而不是不写,是为了让活动页签的
       1px 强调条不改变文字基线(fit 模式下所有页签等高)。 */
    border-top: 1px solid transparent;
    font-size: var(--font-size-md);
    cursor: grab;
    white-space: nowrap;
    flex: 0 0 auto;
    /* box-shadow 不能叠加,只能整体覆盖;把"额外阴影"抽成变量,
       让 drop 指示线与活动页签底部盖线能共存。 */
    --tab-extra-shadow: 0 0 transparent;
    box-shadow: var(--tab-extra-shadow), inset 0 -1px 0 transparent;
    transition: background-color var(--transition-fast) linear;
}

.tab:hover {
    background: var(--bg-tab-hover);        /* tab.hoverBackground */
}

/* 拖拽中的页签:半透明 + 抓取指针(VSCode 没有这种拖拽视觉,产品自有) */
.tab.dragging {
    opacity: 0.45;
    cursor: grabbing;
}

.tab.active {
    background: var(--bg-tab-active);
    color: var(--fg-bright);                /* tab.activeForeground */
    border-top-color: var(--accent);        /* tab.activeBorderTop */
    /* tab.activeBorder(= 页签自身底色):盖掉页签栏的下边线,
       让活动页签与内容区连成一体。 */
    box-shadow: var(--tab-extra-shadow), inset 0 -1px 0 var(--bg-tab-active);
}

/* ── 拖拽插入指示:在插入位置的页签边缘画一条 2px 强调线 ── */
.tab.drop-left {
    --tab-extra-shadow: inset 2px 0 0 var(--accent);
}

.tab.drop-right {
    --tab-extra-shadow: inset -2px 0 0 var(--accent);
}

.tab-title {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
}

/* ── 网络状态(延迟,颜色分级) ── */
.net-status {
    display: flex;
    align-items: center;
    gap: var(--gap-xs);
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    white-space: nowrap;
    flex: 0 0 auto;
}

.net-good {
    color: var(--net-good); /* 绿:<30ms */
}

.net-fair {
    color: var(--net-fair); /* 黄:30~100ms */
}

.net-bad {
    color: var(--net-bad); /* 红:≥100ms */
}

.state-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex: 0 0 auto;
}

.dot-idle {
    background: var(--dot-idle);
}

.dot-running {
    background: var(--dot-running);
}

.dot-dead {
    background: var(--dot-dead);
}

.tab-close {
    background: none;
    border: none;
    color: var(--fg-2);
    font-size: var(--font-size-xs);
    line-height: 1;
    padding: 2px 4px;
    border-radius: var(--radius-sm);
    cursor: pointer;
    flex: 0 0 auto;
    /* 常驻显示:VSCode 默认 tab.closeButton 也是常驻(不依赖 hover) */
}

.tab-close:hover {
    background: var(--hover-toolbar); /* toolbar.hoverBackground */
    color: var(--fg-bright);
}

.tab-actions {
    display: flex;
    align-items: center;
    gap: var(--gap-xs);
    margin-left: auto;
    padding: 0 var(--gap-sm);
    flex: 0 0 auto;
    position: sticky;
    right: 0;
    background: var(--bg-bar);
}

/* 新建会话固定在左侧,滚动时保持可见 */
.tab-actions-left {
    margin-left: 0;
    position: sticky;
    left: 0;
    z-index: 2;
}

/* 工具按钮:设置入口改用 lucide 图标(原来是一个 "⚙" 文本字符 —— 字形与笔画
   随字体走,在部分字体里又细又小,和旁边的延迟徽标同为灰色时很难被发现)。
   按钮几何与悬停反馈对齐 gossh 页签栏的工具按钮。 */
.icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 26px;
    height: 26px;
    padding: 0 6px;
    background: none;
    border: none;
    color: var(--fg); /* icon.foreground */
    font-size: var(--font-size-sm);
    cursor: pointer;
    line-height: 1;
    border-radius: var(--radius-md);
}

.icon-btn:hover {
    background: var(--hover-toolbar);
    color: var(--fg-bright);
}
</style>