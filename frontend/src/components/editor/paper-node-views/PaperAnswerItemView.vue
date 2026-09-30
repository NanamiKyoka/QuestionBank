<script setup lang="ts">
import { computed } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import RichContent from '@/components/RichContent.vue'
import type { PaperQuestionProjection } from '@/api/paper'
import { answerTextOf } from '@/lib/paperView'
import { paperCanvasStateOf } from './canvasState'

/**
 * 解析条目的 NodeView。
 *
 * 服务端派生的**只读**节点：点不动、拖不动、删不掉（schema 里已经
 * 关掉 draggable / selectable，这里再把控件去掉，让它一眼就是"只读"）。
 *
 * 条目与题目块的配对靠 `sourceQuestionNodeId`（软指针）：它指向题目块的
 * nodeId，据此反查 question_id 再从投影取答案。为什么要反查而不是直接
 * 存 question_id：同一道题在卷子里可能排两处，直接存 id 会让两条解析
 * 都指向同一个题目，导出时无法判断哪条解析配哪个题块。
 *
 * 指针指向的题目块被删掉时取不到投影，按"对应题目不可用"降级显示，
 * 而不是渲染成空白——空白会让人以为解析本来就是空的。
 */
const props = defineProps(nodeViewProps)

const state = computed(() => paperCanvasStateOf(props.editor))

const sourceNodeID = computed(() => {
  const raw = props.node.attrs.sourceQuestionNodeId
  return typeof raw === 'string' && raw ? raw : null
})

const projection = computed<PaperQuestionProjection | undefined>(() => {
  const target = sourceNodeID.value
  if (!target) return undefined
  let qid: number | null = null
  // 在**当前**文档里反查，而不是在初始节点数组里查：
  // 用户刚把题目拖到别处、刚删掉一个块，投影都得跟着当前状态走。
  props.editor.state.doc.descendants((node) => {
    if (qid !== null) return false
    if (node.type.name === 'paperQuestion' && node.attrs.nodeId === target) {
      const raw = node.attrs.questionId
      if (typeof raw === 'number' && raw > 0) qid = raw
      return false
    }
    return true
  })
  return qid === null ? undefined : state.value.projections[qid]
})

const answer = computed(() => answerTextOf(projection.value))
const number = computed(() => state.value.numbers.get(sourceNodeID.value ?? '') ?? '?')
</script>

<template>
  <NodeViewWrapper class="qm-canvas-answeritem" data-qm-node="answer_item" contenteditable="false">
    <div class="qm-canvas-answeritem__title">第 {{ number }} 题解析</div>

    <template v-if="projection">
      <div v-if="answer" class="qm-canvas-answeritem__row">答案：{{ answer }}</div>
      <RichContent v-if="projection.analysis" :doc="projection.analysis" compact />
      <div v-if="projection.solution" class="qm-canvas-answeritem__row">
        <RichContent :doc="projection.solution" compact />
      </div>
      <div v-if="!projection.analysis && !projection.solution" class="qm-canvas-answeritem__hint">
        这道题还没有解析内容
      </div>
    </template>

    <div v-else class="qm-canvas-answeritem__hint">对应题目不可用</div>
  </NodeViewWrapper>
</template>

<style scoped>
.qm-canvas-answeritem {
  padding: 0.375rem 0.5rem;
  border-left: 2px solid var(--el-border-color);
  margin: 0.25rem 0;
  user-select: none;
}
.qm-canvas-answeritem__title {
  font-size: 0.75rem;
  font-weight: 500;
  color: var(--el-text-color-secondary);
}
.qm-canvas-answeritem__row,
.qm-canvas-answeritem__hint {
  font-size: 0.75rem;
  color: var(--el-text-color-placeholder);
}
</style>
