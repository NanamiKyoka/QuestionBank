<script setup lang="ts">
import { computed } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import RichContent from '@/components/RichContent.vue'
import PaperBlockActions from './PaperBlockActions.vue'
import { QUESTION_NUMBER_MAX_LENGTH, type PaperQuestionProjection } from '@/api/paper'
import { answerTextOf } from '@/lib/paperView'
import { paperCanvasStateOf } from './canvasState'

/**
 * 题目块的 NodeView。
 *
 * 这是画布上信息最密的一块。三个要点：
 *
 *  1. **题干、选项、答案都从投影读，不从节点 attrs 读。**
 *     节点里只存 question_id 与冻结修订号；内容归服务端管
 *     （`QuestionContent` 那两列刻意不进 Content，见 model/paper.go）。
 *     在这里"顺手"把题干复制进 attrs，等于让前端能伪造冻结快照。
 *
 *  2. **分值写 props.score、题号写 props.number**，与服务端 `sumScore`
 *     和导出器读的位置完全一致。写进别处会让整卷分值永远是 0。
 *
 *  3. 答案在组卷时**总是**显示：老师需要边排边核对。导出时答案出不出现
 *     由解析模块与展示字段决定，与这里无关。
 */
const props = defineProps(nodeViewProps)

const state = computed(() => paperCanvasStateOf(props.editor))

const questionId = computed(() => {
  const raw = props.node.attrs.questionId
  return typeof raw === 'number' && raw > 0 ? raw : null
})

const projection = computed<PaperQuestionProjection | undefined>(() =>
  questionId.value === null ? undefined : state.value.projections[questionId.value],
)

const stale = computed(() => projection.value?.stale === true)

/** 手填的题号。留空表示交给自动编号。 */
const number = computed(() => {
  const raw = props.node.attrs.number
  return typeof raw === 'string' ? raw : ''
})

/** 自动编号算出来的号，用作输入框的占位符。 */
const numberPlaceholder = computed(() => state.value.numbers.get(String(props.node.attrs.nodeId)) ?? '')

const score = computed(() => {
  const raw = props.node.attrs.score
  return typeof raw === 'number' ? raw : undefined
})

const answer = computed(() => answerTextOf(projection.value))

const optionColumns = computed(() => {
  const count = projection.value?.options?.length ?? 0
  if (count >= 4) return 2
  return 1
})

function setNumber(next: string) {
  if (!state.value.canEdit) return
  // 截断而不是拒绝：老师在框里敲到第 17 个字时，拦下整次输入反而更烦。
  const trimmed = (next ?? '').trim().slice(0, QUESTION_NUMBER_MAX_LENGTH)
  // 清空等于「这题不指定题号」：写 null 而不是空串，
  // 空串会被导出器当成有效值，卷面上会出现一个空题号。
  props.updateAttributes({ number: trimmed || null })
}

function setScore(next: number | undefined) {
  if (!state.value.canEdit) return
  props.updateAttributes({ score: typeof next === 'number' && Number.isFinite(next) ? next : null })
}
</script>

<template>
  <NodeViewWrapper
    class="qm-canvas-question"
    :class="selected ? 'qm-canvas-block--selected' : ''"
    data-qm-node="question"
  >
    <PaperBlockActions v-bind="props" />
    <div class="qm-canvas-question__head" contenteditable="false">
      <template v-if="state.numberingEnabled">
        <span class="qm-canvas-question__hint">第</span>
        <el-input
          :model-value="number"
          :placeholder="numberPlaceholder"
          :disabled="!state.canEdit"
          :maxlength="QUESTION_NUMBER_MAX_LENGTH"
          size="small"
          class="!w-20"
          @update:model-value="setNumber"
        />
        <span class="qm-canvas-question__hint">题</span>
      </template>
      <span v-else class="qm-canvas-question__number">第 {{ numberPlaceholder }} 题</span>

      <el-tag v-if="stale" size="small" type="warning">内容已更新</el-tag>

      <span class="qm-canvas-question__meta">
        {{ projection?.question_type_label }}
        <template v-if="projection?.difficulty"> · {{ '★'.repeat(projection.difficulty) }}</template>
      </span>

      <div v-if="state.scoringEnabled" class="qm-canvas-question__score">
        <span class="qm-canvas-question__hint">分值</span>
        <el-input-number
          :model-value="score"
          :min="0"
          :max="999"
          :step="0.5"
          :disabled="!state.canEdit"
          size="small"
          class="!w-24"
          controls-position="right"
          placeholder="未设"
          @update:model-value="setScore"
        />
      </div>
    </div>

    <div class="qm-canvas-question__body" contenteditable="false">
      <template v-if="projection">
        <RichContent :doc="projection.stem" />
        <div
          v-if="projection.options.length"
          class="qm-canvas-question__options"
          :style="{ columns: optionColumns }"
        >
          <div v-for="option in projection.options" :key="option.id" class="qm-canvas-question__option">
            <span class="qm-canvas-question__option-label">{{ option.label }}.</span>
            <RichContent :doc="option.content" class="flex-1" />
          </div>
        </div>
        <div v-if="answer" class="qm-canvas-question__answer">答案：{{ answer }}</div>
      </template>
      <div v-else class="qm-canvas-question__missing">题目内容不可用（可能已被删除）</div>
    </div>
  </NodeViewWrapper>
</template>

<style scoped>
.qm-canvas-question {
  padding: 0.5rem 0.75rem;
  border: 1px solid var(--el-border-color-lighter);
  border-left: 4px solid var(--el-color-primary-light-3);
  border-radius: 4px;
  background: #fff;
}
.qm-canvas-question__head {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
  margin-bottom: 0.25rem;
}
.qm-canvas-question__hint,
.qm-canvas-question__meta {
  font-size: 0.75rem;
  color: var(--el-text-color-placeholder);
}
.qm-canvas-question__number {
  font-size: 0.875rem;
  font-weight: 500;
}
.qm-canvas-question__score {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  margin-left: auto;
}
.qm-canvas-question__options {
  margin-top: 0.25rem;
}
.qm-canvas-question__option {
  display: flex;
  gap: 0.25rem;
  font-size: 0.875rem;
}
.qm-canvas-question__option-label {
  font-weight: 500;
}
.qm-canvas-question__answer {
  margin-top: 0.25rem;
  font-size: 0.75rem;
  color: var(--el-color-success);
}
.qm-canvas-question__missing {
  font-size: 0.875rem;
  color: var(--el-text-color-placeholder);
}
</style>
