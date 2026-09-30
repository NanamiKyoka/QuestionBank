<script setup lang="ts">
import { computed } from 'vue'
import { NodeViewContent, NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import { HEADING_MAX_LEVEL, HEADING_MIN_LEVEL } from '@/lib/paperDocument'
import { paperCanvasStateOf } from './canvasState'

/**
 * 标题的 NodeView。
 *
 * 与说明文字的区别只有两处：一级/二级标题的视觉分量不同，
 * 以及给一个就地切换层级的入口。
 *
 * 层级改的是 attrs（`updateAttributes`），于是变更会走一次正规的
 * ProseMirror 事务——撤销能退回来，自动保存也能看到它。
 * 直接改外层 DOM 的样式是做不到这两件事的。
 */
const props = defineProps(nodeViewProps)

const state = computed(() => paperCanvasStateOf(props.editor))

const level = computed(() => {
  const raw = props.node.attrs.level
  return typeof raw === 'number' ? raw : 1
})

const levelOptions = Array.from(
  { length: HEADING_MAX_LEVEL - HEADING_MIN_LEVEL + 1 },
  (_, i) => HEADING_MIN_LEVEL + i,
)

function setLevel(next: number) {
  if (!state.value.canEdit) return
  props.updateAttributes({ level: next })
}
</script>

<template>
  <NodeViewWrapper
    class="qm-canvas-block qm-canvas-heading"
    :class="[`qm-canvas-heading--h${level}`, selected ? 'qm-canvas-block--selected' : '']"
    data-qm-node="heading"
    :data-level="level"
  >
    <div class="qm-canvas-heading__controls" contenteditable="false">
      <el-select
        :model-value="level"
        :disabled="!state.canEdit"
        size="small"
        class="!w-16"
        @update:model-value="(v: number) => setLevel(v)"
      >
        <el-option v-for="n in levelOptions" :key="n" :label="`H${n}`" :value="n" />
      </el-select>
    </div>
    <NodeViewContent class="qm-canvas-heading__body" />
  </NodeViewWrapper>
</template>

<style scoped>
.qm-canvas-heading {
  position: relative;
  padding: 0.4rem 0.75rem;
  border-radius: 4px;
  border: 1px solid transparent;
}
.qm-canvas-heading:hover {
  border-color: var(--el-border-color-lighter);
}
.qm-canvas-heading__body {
  outline: none;
  font-weight: 600;
}
.qm-canvas-heading--h1 .qm-canvas-heading__body {
  font-size: 1.25rem;
}
.qm-canvas-heading--h2 .qm-canvas-heading__body {
  font-size: 1.1rem;
}
.qm-canvas-heading--h3 .qm-canvas-heading__body {
  font-size: 1rem;
}
.qm-canvas-heading__controls {
  position: absolute;
  top: 0.25rem;
  right: 0.25rem;
  opacity: 0;
  transition: opacity 0.15s;
}
.qm-canvas-heading:hover .qm-canvas-heading__controls,
.qm-canvas-heading--selected .qm-canvas-heading__controls {
  opacity: 1;
}
</style>
