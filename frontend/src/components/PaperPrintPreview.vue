<script setup lang="ts">
import { computed } from 'vue'
import type { PaperNode, PaperQuestionProjection, QuestionRef } from '@/api/paper'
import type { QuestionDisplay } from '@/api/paper'
import RichContent from '@/components/RichContent.vue'
import { answerTextOf } from '@/lib/paperView'

const props = withDefaults(defineProps<{
  title: string
  nodes: PaperNode[]
  questions: Record<number, PaperQuestionProjection>
  numberingEnabled?: boolean
  scoringEnabled?: boolean
  questionDisplay?: QuestionDisplay
}>(), {
  numberingEnabled: true,
  scoringEnabled: false,
  questionDisplay: () => ({
    show_analysis: false,
    show_solution: false,
    show_summary: false,
    show_knowledge: false,
  }),
})

const roots = computed(() =>
  props.nodes
    .filter((node) => node.parent_id === null && node.node_kind !== 'reference')
    .slice()
    .sort((a, b) => a.position - b.position),
)

const pages = computed(() => {
  const out: PaperNode[][] = []
  let page: PaperNode[] = []
  for (const node of roots.value) {
    if (node.node_type === 'page_break') {
      if (page.length) out.push(page)
      page = []
      continue
    }
    page.push(node)
  }
  if (page.length || out.length === 0) out.push(page)
  return out
})

const totalScore = computed(() =>
  roots.value.reduce((sum, node) => sum + (scoreOf(node) ?? 0), 0),
)

const positionNumbers = computed(() => {
  const map = new Map<string, number>()
  let n = 0
  for (const node of roots.value) {
    if (node.node_type !== 'question') continue
    n += 1
    map.set(node.id, n)
  }
  return map
})

const anchoredItems = computed(() => {
  const map = new Map<string, PaperNode[]>()
  for (const node of props.nodes) {
    if (node.node_kind !== 'reference' || !node.anchor_before_node_id) continue
    const list = map.get(node.anchor_before_node_id) ?? []
    list.push(node)
    map.set(node.anchor_before_node_id, list)
  }
  return map
})

function moduleChildren(moduleID: string): PaperNode[] {
  return props.nodes
    .filter((node) => node.parent_id === moduleID)
    .slice()
    .sort((a, b) => a.position - b.position)
}

function isAnchoredModule(moduleID: string): boolean {
  const items = moduleChildren(moduleID)
  return items.length > 0 && items.every((item) => item.anchor_before_node_id)
}

function propsOf<T extends Record<string, unknown>>(node: PaperNode): T {
  return (node.props && typeof node.props === 'object' ? node.props : {}) as T
}

function contentOf<T>(node: PaperNode): T | null {
  return node.content && typeof node.content === 'object' ? (node.content as T) : null
}

function numberLabel(node: PaperNode): string {
  const explicit = propsOf<{ number?: string }>(node).number
  if (typeof explicit === 'string' && explicit.trim()) return explicit.trim()
  return String(positionNumbers.value.get(node.id) ?? '')
}

function scoreOf(node: PaperNode): number | null {
  const score = propsOf<{ score?: number }>(node).score
  return typeof score === 'number' && Number.isFinite(score) ? score : null
}

function headingLevel(node: PaperNode): number {
  const level = propsOf<{ level?: number }>(node).level
  return typeof level === 'number' ? Math.min(Math.max(level, 1), 3) : 1
}

function answerLines(node: PaperNode): number {
  const lines = propsOf<{ lines?: number }>(node).lines
  if (typeof lines === 'number' && lines > 0) return Math.min(Math.round(lines), 50)
  const legacy = contentOf<{ height?: number }>(node)?.height
  return typeof legacy === 'number' ? Math.max(1, Math.round(legacy / 30)) : 4
}

function isLined(node: PaperNode): boolean {
  return propsOf<{ style?: string }>(node).style === 'lined'
}

function questionOf(node: PaperNode): PaperQuestionProjection | undefined {
  const ref = contentOf<QuestionRef>(node)
  return ref?.question_id ? props.questions[ref.question_id] : undefined
}

function answerItemProjection(node: PaperNode): PaperQuestionProjection | undefined {
  const sourceID = node.source_question_node_id
  if (!sourceID) return undefined
  const source = roots.value.find((root) => root.id === sourceID)
  return source ? questionOf(source) : undefined
}

function itemNumber(node: PaperNode): string {
  const sourceID = node.source_question_node_id
  if (!sourceID) return ''
  const source = roots.value.find((root) => root.id === sourceID)
  return source ? numberLabel(source) : ''
}

function hasDoc(doc?: unknown): boolean {
  return !!doc && Array.isArray((doc as { content?: unknown[] }).content) && ((doc as { content?: unknown[] }).content?.length ?? 0) > 0
}
</script>

<template>
  <div class="paper-preview">
    <section v-for="(page, pageIndex) in pages" :key="pageIndex" class="paper-preview__page">
      <template v-if="pageIndex === 0">
        <h1 class="paper-preview__title">{{ title || '未命名试卷' }}</h1>
        <p v-if="scoringEnabled" class="paper-preview__subtitle">
          （满分 {{ totalScore }} 分）
        </p>
      </template>

      <div class="paper-preview__body">
        <template v-for="node in page" :key="node.id">
          <RichContent v-if="node.node_type === 'rich_text'" :doc="node.content" compact />

          <div
            v-else-if="node.node_type === 'heading'"
            class="paper-preview__heading"
            :class="`paper-preview__heading--h${headingLevel(node)}`"
          >
            <RichContent :doc="node.content" compact />
          </div>

          <div v-else-if="node.node_type === 'answer_space'" class="paper-preview__answer-space">
            <template v-if="isLined(node)">
              <div v-for="line in answerLines(node)" :key="line" class="paper-preview__answer-line" />
            </template>
            <div v-else :style="{ height: `${answerLines(node) * 24}px` }" />
          </div>

          <div v-else-if="node.node_type === 'question'" class="paper-preview__question">
            <div v-if="numberingEnabled || (scoringEnabled && scoreOf(node) !== null)" class="paper-preview__question-head">
              <span v-if="numberingEnabled" class="paper-preview__number">{{ numberLabel(node) }}．</span>
              <span v-if="scoringEnabled && scoreOf(node) !== null" class="paper-preview__score">（{{ scoreOf(node) }} 分）</span>
            </div>

            <template v-if="questionOf(node)">
              <RichContent :doc="questionOf(node)?.stem" class="paper-preview__stem" />
              <div v-if="questionOf(node)?.options?.length" class="paper-preview__options">
                <div v-for="option in questionOf(node)?.options" :key="option.id" class="paper-preview__option">
                  <span class="font-medium">{{ option.label }}．</span>
                  <RichContent :doc="option.content" compact class="min-w-0 flex-1" />
                </div>
              </div>
              <div v-if="questionDisplay.show_analysis && hasDoc(questionOf(node)?.analysis)" class="paper-preview__extra">
                <span class="font-semibold">【解析】</span>
                <RichContent :doc="questionOf(node)?.analysis" compact />
              </div>
              <div v-if="questionDisplay.show_solution && hasDoc(questionOf(node)?.solution)" class="paper-preview__extra">
                <span class="font-semibold">【思路】</span>
                <RichContent :doc="questionOf(node)?.solution" compact />
              </div>
              <div v-if="questionDisplay.show_summary && hasDoc(questionOf(node)?.summary)" class="paper-preview__extra">
                <span class="font-semibold">【小结】</span>
                <RichContent :doc="questionOf(node)?.summary" compact />
              </div>
            </template>
            <div v-else class="paper-preview__missing">（题目内容不可用）</div>

            <div v-for="item in anchoredItems.get(node.id) ?? []" :key="item.id" class="paper-preview__extra">
              <span class="font-semibold">{{ itemNumber(item) }}．【答案】</span>
              <span>{{ answerTextOf(answerItemProjection(item)) }}</span>
              <RichContent v-if="answerItemProjection(item)?.analysis" :doc="answerItemProjection(item)?.analysis" compact />
            </div>
          </div>

          <div v-else-if="node.node_type === 'question_details' && !isAnchoredModule(node.id)" class="paper-preview__answers">
            <h2 class="paper-preview__answers-title">参考答案与解析</h2>
            <div v-for="item in moduleChildren(node.id)" :key="item.id" class="paper-preview__answer-item">
              <div>
                <span class="font-semibold">{{ itemNumber(item) }}．【答案】</span>
                <span>{{ answerTextOf(answerItemProjection(item)) }}</span>
              </div>
              <div v-if="answerItemProjection(item)?.analysis">
                <span class="font-semibold">【解析】</span>
                <RichContent :doc="answerItemProjection(item)?.analysis" compact />
              </div>
            </div>
          </div>
        </template>
      </div>
    </section>
  </div>
</template>

<style scoped>
.paper-preview {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 1.5rem;
  padding: 1.5rem;
  background: var(--el-fill-color-light);
}
.paper-preview__page {
  box-sizing: border-box;
  width: 794px;
  min-height: 1123px;
  padding: 56px 64px;
  background: white;
  color: #111827;
  box-shadow: 0 10px 30px rgb(15 23 42 / 12%);
  font-family: "Times New Roman", SimSun, serif;
  font-size: 10.5pt;
  line-height: 1.7;
}
.paper-preview__title {
  margin: 0;
  text-align: center;
  font-size: 18pt;
  font-weight: 700;
}
.paper-preview__subtitle {
  margin: 0.35rem 0 1.5rem;
  text-align: center;
}
.paper-preview__body {
  display: flex;
  flex-direction: column;
  gap: 0.65rem;
}
.paper-preview__heading--h1 {
  font-size: 15pt;
  font-weight: 700;
}
.paper-preview__heading--h2 {
  font-size: 13pt;
  font-weight: 700;
}
.paper-preview__heading--h3 {
  font-size: 11pt;
  font-weight: 700;
}
.paper-preview__question-head {
  margin-bottom: 0.15rem;
}
.paper-preview__number,
.paper-preview__score {
  font-weight: 700;
}
.paper-preview__stem {
  display: inline;
}
.paper-preview__options {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.1rem 1.5rem;
  margin-top: 0.25rem;
}
.paper-preview__option {
  display: flex;
  gap: 0.25rem;
}
.paper-preview__extra {
  margin-top: 0.3rem;
}
.paper-preview__answer-space {
  margin: 0.5rem 0;
}
.paper-preview__answer-line {
  height: 1.7em;
  border-bottom: 1px solid #9ca3af;
}
.paper-preview__answers {
  margin-top: 1rem;
  border-top: 1px solid #d1d5db;
  padding-top: 1rem;
}
.paper-preview__answers-title {
  margin: 0 0 0.75rem;
  text-align: center;
  font-size: 13pt;
  font-weight: 700;
}
.paper-preview__answer-item {
  margin-bottom: 0.6rem;
}
.paper-preview__missing {
  color: #9ca3af;
  font-style: italic;
}
</style>
