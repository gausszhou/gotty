<template>
  <div
    v-if="node.kind === 'split'"
    class="split-view"
    :class="node.dir"
  >
    <div class="split-child" :style="{ flexBasis: pct(node.ratio) }">
      <SplitView
        :node="node.a"
        :focused-id="focusedId"
        :session-of-pane="sessionOfPane"
        @focus="emit('focus', $event)"
        @close="emit('close', $event)"
        @destroy="emit('destroy', $event)"
      />
    </div>
    <div
      class="split-gutter"
      :class="node.dir"
      @pointerdown.prevent="startDrag($event, node)"
    ></div>
    <div class="split-child" :style="{ flex: '1 1 0%', flexBasis: pct(1 - node.ratio) }">
      <SplitView
        :node="node.b"
        :focused-id="focusedId"
        :session-of-pane="sessionOfPane"
        @focus="emit('focus', $event)"
        @close="emit('close', $event)"
        @destroy="emit('destroy', $event)"
      />
    </div>
  </div>

  <TerminalPane
    v-else
    :pane-id="node.id"
    :session-id="sessionOfPane[node.id]"
    :focused="focusedId === node.id"
    @focus="emit('focus', $event)"
    @close="emit('close', $event)"
    @destroy="emit('destroy', $event)"
  />
</template>

<script setup lang="ts">
import { defineOptions } from 'vue'
import type { LayoutNode, SplitNode } from '../utils/split'
import TerminalPane from './TerminalPane.vue'

defineOptions({ name: 'SplitView' })

const props = defineProps<{
    node: LayoutNode
    focusedId: string | null
    sessionOfPane: Record<string, string>
}>()

const emit = defineEmits<{
    (e: 'focus', paneId: string): void
    (e: 'close', paneId: string): void
    (e: 'destroy', paneId: string): void
}>()

const pct = (r: number) => `${Math.round(r * 1000) / 10}%`

// 拖动分隔条:直接写回响应式的 node.ratio,Vue 自动触发重渲染。
function startDrag(event: PointerEvent, node: SplitNode) {
    const gutter = event.currentTarget as HTMLElement
    const parent = gutter.parentElement!
    const rect = parent.getBoundingClientRect()
    const horizontal = node.dir === 'row'

    const onMove = (e: PointerEvent) => {
        const pos = horizontal ? e.clientX - rect.left : e.clientY - rect.top
        const size = horizontal ? rect.width : rect.height
        node.ratio = Math.min(0.9, Math.max(0.1, pos / size))
    }
    const onUp = () => {
        window.removeEventListener('pointermove', onMove)
        window.removeEventListener('pointerup', onUp)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
}
</script>

<style scoped>
.split-view {
    display: flex;
    width: 100%;
    height: 100%;
    min-width: 0;
    min-height: 0;
}

.split-view.row {
    flex-direction: row;
}

.split-view.column {
    flex-direction: column;
}

.split-child {
    flex: 0 0 auto;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
    display: flex;
}

.split-gutter {
    flex: 0 0 4px;
    background: #1e1e1e;
    /* 拖动时避免选中文本 */
    touch-action: none;
    user-select: none;
}

.split-gutter.row {
    cursor: col-resize;
    border-left: 1px solid #333;
    border-right: 1px solid #333;
}

.split-gutter.column {
    cursor: row-resize;
    border-top: 1px solid #333;
    border-bottom: 1px solid #333;
}

.split-gutter:hover {
    background: #007fd4;
}
</style>