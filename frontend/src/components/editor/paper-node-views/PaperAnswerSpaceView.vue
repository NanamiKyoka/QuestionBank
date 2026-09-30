<script setup lang="ts">
import { computed } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import { ANSWER_MAX_LINES, ANSWER_MIN_LINES } from '@/lib/paperDocument'
import PaperBlockActions from './PaperBlockActions.vue'
import { paperCanvasStateOf } from './canvasState'

/**
 * 答题区的 NodeView。
 *
 * 行数存的是**行数**不是像素：导出端按每行一个空段落来排版，
 * 画布上按行数画横线或留白，两边于是天然对齐（老数据里的像素高度
 * 在 paperDocument.answerLinesOf 里已经换算成行数了）。
 *
 * 行高常量与导出端的 baselineskip 视觉相近，改这里要注意跟着改导出端。
 */
const LINE_HEIGHT_PX = 24

const props = defineProps(nodeViewProps)

const state = computed(() => paperCanvasStateOf(props.editor))

const lines = computed(() => {
  const raw = props.node.attrs.lines
  return typeof raw === 'number' ? raw : ANSWER_MIN_LINES
})

const style = computed(() => (props.node.attrs.style === 'lined' ? 'lined' : 'blank'))

function setLines(next: number | undefined) {
  if (!state.value.canEdit || typeof next !== 'number') return
  const clamped = Math.min(Math.max(Math.round(next), ANSWER_MIN_LINES), ANSWER_MAX_LINES)
  props.updateAttributes({ lines: clamped })
}

function setStyle(next: string) {
  if (!state.value.canEdit) return
  props.updateAttributes({ style: next })
}
</script>

<template>
  <NodeViewWrapper
    class="qm-canvas-block qm-canvas-answerspace"
    :class="selected ? 'qm-canvas-block--selected' : ''"
    data-qm-node="answer_space"
  >
    <PaperBlockActions v-bind="props" />
    <div class="qm-canvas-answerspace__controls" contenteditable="false">
      <span class="text-xs text-gray-400">答题区</span>
      <el-input-number
        :model-value="lines"
        :min="ANSWER_MIN_LINES"
        :max="ANSWER_MAX_LINES"
        :disabled="!state.canEdit"
        size="small"
        class="!w-24"
        controls-position="right"
        @update:model-value="setLines"
      />
      <el-radio-group
        :model-value="style"
        :disabled="!state.canEdit"
        size="small"
        @update:model-value="setStyle"
      >
        <el-radio-button value="blank">留白</el-radio-button>
        <el-radio-button value="lined">横线</el-radio-button>
      </el-radio-group>
    </div>
    <div class="qm-canvas-answerspace__area">
      <template v-if="style === 'lined'">
        <div
          v-for="line in lines"
          :key="line"
          class="qm-canvas-answerspace__line"
          :style="{ height: `${LINE_HEIGHT_PX}px` }"
        />
      </template>
      <div v-else :style="{ height: `${lines * LINE_HEIGHT_PX}px` }" />
    </div>
  </NodeViewWrapper>
</template>

<style scoped>
.qm-canvas-answerspace {
  padding: 0.5rem 0.75rem;
  border: 1px dashed var(--el-border-color);
  border-radius: 4px;
}
.qm-canvas-answerspace__controls {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.25rem;
}
.qm-canvas-answerspace__line {
  border-bottom: 1px dashed var(--el-border-color);
}
</style>
