<script setup lang="ts">
import { computed, nextTick, onMounted, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ApiError } from '@/api/http'
import { questionApi } from '@/api'
import { chatApi } from '@/api/ai'
import {
  paperApi,
  toNodeInput,
  type NodeInput,
  type NumberingMode,
  type PaperDetail,
  type PaperNode,
  type PaperVersion,
  type QuestionRef,
  type ModuleContent,
  type PaperQuestionProjection,
} from '@/api/paper'
import type { QuestionBrief } from '@/api/types'
import RichContent from '@/components/RichContent.vue'
import CompositionNumberingPanel from '@/components/CompositionNumberingPanel.vue'
import CompositionTimelineSheet from '@/components/CompositionTimelineSheet.vue'
import CompositionAiChangesBar from '@/components/CompositionAiChangesBar.vue'
import PaperTiptapCanvas from '@/components/editor/PaperTiptapCanvas.vue'
import { emptyDoc } from '@/lib/richdoc'
import {
  computeNumbers,
  numberOf,
  propsWithNumber,
  propsWithScore,
  roundScore,
  totalScoreOf,
} from '@/lib/paperView'
import { abandonInflightClientTools, answerClientToolFrame } from '@/composables/useAiClientTools'
import { streamChatReply, type ChatStreamFrame } from '@/composables/useChatStream'
import { addUsage, stopNotice, usageNote, type RunUsage } from '@/lib/aiRun'
import { usePaperAiTools, type PaperAiHost, type PendingPaperChange } from '@/composables/usePaperAiTools'
import { useUnsavedGuard } from '@/composables/useUnsavedGuard'
import DifficultyStars from '@/components/ui/DifficultyStars.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import MarkdownContent from '@/components/MarkdownContent.vue'
import TableSkeleton from '@/components/ui/TableSkeleton.vue'
import { useSubjectStore } from '@/stores/subject'
import {
  createQuestionNode,
  loadPaperQuestionProjections,
  mergeQuestionProjections,
} from '@/lib/paperQuestion'

/**
 * 组卷工作台。
 *
 * 文档模型（与后端 domain 的规则一致）刻意只有两层：
 *   block    顶层块：说明文字 / 标题 / 题目 / 分页符 / 答题区
 *   module   解析模块（question_details），其子节点由服务端派生
 *   reference 解析条目（answer_item），保存时不回传
 *
 * 保存采用**整树覆盖 + revision 乐观锁**：排版天然是整篇重排
 * （插入一题会让后面所有题号变化），增量补丁反而更难保证一致。
 */
const route = useRoute()
const router = useRouter()

const paperId = computed(() => Number(route.params.id))

const loading = ref(false)
const saving = ref(false)
const exporting = ref(false)
const paper = ref<PaperDetail | null>(null)

/** 本地工作副本。保存成功后会用服务端返回的节点整体替换。 */
const nodes = ref<PaperNode[]>([])
const revision = ref(0)
const dirty = ref(false)

/** 目录里被引用的题目内容投影。 */
const questions = computed(() => paper.value?.questions ?? {})

// ---- 题目选择器 ----
const pickerVisible = ref(false)
const pickerLoading = ref(false)
const pickerRows = ref<QuestionBrief[]>([])
const pickerTotal = ref(0)
const pickerSelected = ref<QuestionBrief[]>([])
const addingQuestions = ref(false)
const pickerQuery = reactive({
  keyword: '',
  type: '',
  status: 'published' as string,
  page: 1,
  page_size: 10,
})
const pickerHasMore = computed(() => pickerRows.value.length < pickerTotal.value)

// ---- 节点属性契约 ----

/** 答题区样式。取值与后端 domain / 导出 IR 一一对应，不要自造新词。 */
type AnswerSpaceStyle = 'blank' | 'lined'

/** 题目节点的 props 类型来自 api/paper.ts（QuestionNodeProps）：
 *
 * 分值必须落在 props 上而不是 content 里——服务端 sumScore 与导出器都只读
 * props.score，写进 content 的话整卷总分永远是 0（这正是原来的死功能）。
 * 题号走同一个契约，导出器按"props.number 优先、位置计数兜底"的顺序取值。
 */

/** 答题区节点的 props。 */
interface AnswerSpaceProps {
  lines?: number
  style?: AnswerSpaceStyle
}

/** 答题区默认行数。
 *
 * 老数据把留白高度写在 content.height 上（默认 120px），120/30 = 4 行，
 * 与导出端一直硬编码的 4 个空段落吻合，所以老卷子翻译过来时版面不会跳。
 * 像素→行数的换算与行数上下限都在 `paperDocument` 里（画布的 NodeView
 * 用的就是它），这里只剩新建块时的默认值。
 */
const DEFAULT_ANSWER_LINES = 4

/**
 * 题号、分值、文案的计算全部来自 `@/lib/paperView`。
 *
 * 这些曾经是本文件里的局部函数。搬到共享模块是因为画布（Tiptap 单一文档）
 * 马上要用同一套算法：留在视图里的话，另一套渲染只能抄一遍，
 * 而抄第二遍的那次多半就是两边开始不一致的时候。
 *
 * 写入类的操作仍然留在这里，因为它们要顺带标脏——`dirty` 是视图的状态，
 * 不属于纯计算。
 */

// ---- 设置 ----
const settingsVisible = ref(false)
const settingsForm = reactive({
  title: '',
  description: '',
  numbering_enabled: true,
  scoring_enabled: false,
  question_display: {
    show_analysis: true,
    show_solution: false,
    show_summary: false,
    show_knowledge: false,
  },
})

// ---- 版本 ----
const versionsVisible = ref(false)
const versions = ref<PaperVersion[]>([])

// ---- 操作时间线 ----
const timelineVisible = ref(false)

const staleQuestions = ref<number[]>([])

const canEdit = computed(() => paper.value?.permissions.can_edit ?? false)
const canFinalize = computed(() => paper.value?.permissions.can_finalize ?? false)

/** 顶层节点，按 position 排序。 */
const topNodes = computed(() =>
  nodes.value
    .filter((n) => n.parent_id === null && n.node_kind !== 'reference')
    .slice()
    .sort((a, b) => a.position - b.position),
)

/** 解析模块（至多一个）。 */
const answerModule = computed(() => topNodes.value.find((n) => n.node_type === 'question_details') ?? null)

/** 顶层题目块，按出现顺序。 */
const questionNodes = computed(() => topNodes.value.filter((n) => n.node_type === 'question'))

/** 题号。
 *
 * 优先用节点上的 `props.number`——那是老师手填的，或者被"自动填充题号"
 * 写进去的；没填过才按出现顺序数一遍。优先级必须和导出器一致
 * （backend/internal/export/assemble.go 的 numberLabelFromProps），
 * 否则界面上写着 1.1、导出来却是 3.，比没有覆盖还难查。
 *
 * 卷面题号现在由画布的 NodeView 自己算（`displayNumbers`），
 * 这个视图不再需要一份——留两份只会各自过期。
 */

const questionCount = computed(() => questionNodes.value.length)

// ---- 题号 ----

/** 题号写回后的编号方式。面板是"选方式 + 点填充"，所以这里只留选择。 */
const numberingMode = ref<NumberingMode>('global')

function setNumber(node: PaperNode, value: string) {
  node.props = propsWithNumber(node, value)
  dirty.value = true
}

/**
 * 自动填充题号。
 *
 * 整体覆盖是危险操作（手改过的题号会一起没），所以先确认再写；
 * 写完只是标脏，仍要按「保存」才落库。
 */
async function applyNumbering(mode: NumberingMode) {
  const targets = questionNodes.value
  if (!targets.length) {
    ElMessage.warning('试卷里还没有题目')
    return
  }
  const wanted = computeNumbers(topNodes.value, mode)
  const label = mode === 'global' ? '全局顺序' : '按二级标题分组'
  try {
    await ElMessageBox.confirm(
      `将按「${label}」重排 ${targets.length} 道题的题号，已有的题号会被覆盖。之后仍可逐题修改。`,
      '自动填充题号',
      { type: 'warning', confirmButtonText: '填充', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  let changed = 0
  for (const node of targets) {
    const next = wanted.get(node.id) ?? ''
    if (numberOf(node) === next) continue
    setNumber(node, next)
    changed += 1
  }
  numberingMode.value = mode
  if (changed === 0) ElMessage.info('题号已经与所选方式一致，无需改动')
  else ElMessage.success(`已改写 ${changed} 道题的题号，记得保存`)
}

// ---- 分值 ----

/** 「启用分值统计」没开时整条分值链路都是死的：服务端 sumScore 直接返回 0，
 *  导出器也不输出「（N 分）」。这时就不该让用户填一个不会被采用的数字。 */
const scoringEnabled = computed(() => paper.value?.scoring_enabled ?? false)

/** 题号总开关的当前可见值。
 *
 * 取"设置弹窗里勾的"与"已保存的"二者的或：设置是批量提交，勾完到点保存
 * 之间有一段时间，如果只看已保存值，老师刚打开的题号入口会凭空消失。
 */
const numberingEnabled = computed(
  () => settingsForm.numbering_enabled || (paper.value?.numbering_enabled ?? false),
)

/** 单题分值。没设过或值非法时返回 null，让输入框显示占位符而不是摆一个 0——
 *  「0 分」和「没设」是两回事，必须在数据上就分得开。
 *
 *  0 本身是合法输入（这题不计分），照原样存回去：否则用户敲个 0 会发现
 *  输入框自己清空了，像是界面在跟他作对。 */
function setScore(node: PaperNode, value: number | undefined | null) {
  node.props = propsWithScore(node, value)
  dirty.value = true
}

/** 本地分值合计。
 *
 * paper.total_score 是服务端在**保存时**重算的，编辑过程中一直是旧值；
 * 直接展示它会让人以为分值根本没写进去，所以这里实时求和。
 */
const localTotalScore = computed(() => roundScore(totalScoreOf(nodes.value)))

/** 顶栏展示的总分：有未保存改动时用本地实时值，否则用服务端已持久化的值。 */
const displayTotalScore = computed(() =>
  dirty.value ? localTotalScore.value : (paper.value?.total_score ?? 0),
)

/** 整卷统一分值。
 *
 * 选择题卷常常整卷同分，逐题点既慢又容易漏；而它无非是把同一个值写进各题的
 * props.score，之后仍可逐题覆盖，所以给一个批量入口而不是做成对话框流程。
 */
async function setUniformScore() {
  const targets = topNodes.value.filter((n) => n.node_type === 'question')
  if (!targets.length) {
    ElMessage.warning('试卷里还没有题目')
    return
  }
  const avg = Math.round((localTotalScore.value / targets.length) * 10) / 10
  try {
    const { value } = await ElMessageBox.prompt(
      `把 ${targets.length} 道题的分值统一设为（留空表示清除分值）：`,
      '统一分值',
      {
        confirmButtonText: '应用',
        cancelButtonText: '取消',
        inputValue: avg > 0 ? String(avg) : '5',
        inputPattern: /^\d{0,3}(\.\d{1,2})?$/,
        inputErrorMessage: '请输入 0 ~ 999 之间的数字',
      },
    )
    const num = Number(String(value ?? '').trim())
    const parsed = Number.isFinite(num) && num > 0 ? num : null
    for (const node of targets) setScore(node, parsed)
    ElMessage.success(
      parsed ? `已把 ${targets.length} 道题设为 ${parsed} 分` : `已清除 ${targets.length} 道题的分值`,
    )
  } catch {
    // 用户取消
  }
}

onMounted(load)

// 路由组件没有 key：同名路由换参数（改地址栏、前进后退）时组件不会重建。
// 换了卷子就要重新拉取，否则屏幕上是 A 卷的草稿、地址栏却是 B 卷，
// 助手会话还会继续挂在 A 卷上。
watch(paperId, async () => {
  chatController?.abort()
  newChatSession()
  await load()
})

// 有未保存改动时离开页面要拦一下：路由与刷新两种离开都接上。
useUnsavedGuard(() => dirty.value, '当前试卷有未保存的改动，确定离开吗？')

async function load() {
  loading.value = true
  try {
    const detail = await paperApi.get(paperId.value)
    applyDetail(detail)
    // 顺带检查引用的题目有没有被改过，有就提示同步。
    void checkStale()
  } finally {
    loading.value = false
  }
}

function applyDetail(detail: PaperDetail) {
  paper.value = detail
  nodes.value = detail.nodes.map((n) => ({ ...n }))
  revision.value = detail.revision
  dirty.value = false

  settingsForm.title = detail.title
  settingsForm.description = detail.description
  settingsForm.numbering_enabled = detail.numbering_enabled
  settingsForm.scoring_enabled = detail.scoring_enabled
  settingsForm.question_display = { ...detail.question_display }
}

async function checkStale() {
  try {
    const result = await paperApi.questionRevisions(paperId.value)
    staleQuestions.value = result.stale ?? []
    if (result.missing?.length) {
      ElMessage.warning(`有 ${result.missing.length} 道题已被删除，请在卷中移除对应题目`)
    }
  } catch {
    staleQuestions.value = []
  }
}

// ---- 节点操作 ----

function newID(): string {
  // 客户端生成节点 id：这样在保存之前就能稳定引用，
  // 不必等一次往返拿到服务端 id 才能做拖拽与删除。
  return crypto.randomUUID()
}

/** 重排同级节点的 position，保证连续无空洞。 */
function renormalize() {
  const byParent = new Map<string, PaperNode[]>()
  for (const node of nodes.value) {
    const key = node.parent_id ?? ''
    const list = byParent.get(key) ?? []
    list.push(node)
    byParent.set(key, list)
  }
  for (const list of byParent.values()) {
    list.sort((a, b) => a.position - b.position)
    list.forEach((node, index) => {
      node.position = index
    })
  }
}

function appendNode(input: Omit<NodeInput, 'position'>) {
  const maxPosition = topNodes.value.length
  nodes.value.push({
    ...input,
    position: maxPosition,
  } as PaperNode)
  // 解析模块必须始终排在最后：它是卷末汇总，插在题目中间会打断题序。
  if (answerModule.value) {
    answerModule.value.position = topNodes.value.length
  }
  renormalize()
  dirty.value = true
}

function applyQuestionProjections(projections: Record<number, PaperQuestionProjection>) {
  if (!paper.value || Object.keys(projections).length === 0) return
  paper.value = {
    ...paper.value,
    questions: mergeQuestionProjections(questions.value, projections),
  }
}

async function addQuestions(rows: QuestionBrief[]) {
  if (!rows.length || addingQuestions.value) return
  const ids = [...new Set(rows.map((row) => row.id))]
  const duplicateIDs = ids.filter((id) => usedQuestionIds.value.has(id))
  if (duplicateIDs.length) {
    ElMessage.warning(`有 ${duplicateIDs.length} 道题已在试卷中，请移除后重试`)
    return
  }

  addingQuestions.value = true
  try {
    const projections = await loadPaperQuestionProjections(ids)
    const availableRows = rows.filter((row) => projections[row.id])
    if (availableRows.length === 0) {
      ElMessage.error('所选题目已不可用，请刷新选择题库后重试')
      return
    }

    applyQuestionProjections(projections)
    for (const row of availableRows) {
      appendNode(createQuestionNode(newID(), row.id, projections[row.id].content_revision))
    }

    const skipped = rows.length - availableRows.length
    pickerVisible.value = false
    pickerSelected.value = []
    scheduleAutoSave()
    if (skipped > 0) ElMessage.warning(`已加入 ${availableRows.length} 道题，另有 ${skipped} 道题不可用`)
    else ElMessage.success(`已加入 ${availableRows.length} 道题`)
  } catch (error) {
    if (!(error instanceof ApiError)) ElMessage.error('加载题目失败，请稍后重试')
  } finally {
    addingQuestions.value = false
  }
}

/**
 * 插入一个说明文字块。
 *
 * 直接插入、就地编辑，不再弹对话框：画布上这个块本身就是可敲字的文本块，
 * 弹窗等于把内容从它所在的位置拿走再放回来，反而绕远。
 */
function addRichText() {
  appendNode({
    id: newID(),
    parent_id: null,
    node_kind: 'block',
    node_type: 'rich_text',
    content: emptyDoc(),
    props: {},
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
  })
}

/** 插入一个标题块。层级用默认的一级，插进去后可在块内切换。 */
function addHeading() {
  appendNode({
    id: newID(),
    parent_id: null,
    node_kind: 'block',
    node_type: 'heading',
    content: emptyDoc(),
    props: { level: 1 },
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
  })
}

function addPageBreak() {
  appendNode({
    id: newID(),
    parent_id: null,
    node_kind: 'block',
    node_type: 'page_break',
    content: null,
    props: {},
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
  })
}

function addAnswerSpace() {
  appendNode({
    id: newID(),
    parent_id: null,
    node_kind: 'block',
    node_type: 'answer_space',
    // 行数与样式写在 props 里，这是导出器唯一认的契约；
    // content 保持空，否则会把留白高度留在 JSON 里成为没人读的脏数据。
    content: null,
    props: { lines: DEFAULT_ANSWER_LINES, style: 'blank' } satisfies AnswerSpaceProps,
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
  })
}

function toggleAnswerModule() {
  if (answerModule.value) {
    nodes.value = nodes.value.filter(
      (n) => n.id !== answerModule.value?.id && n.parent_id !== answerModule.value?.id,
    )
  } else {
    const content: ModuleContent = { scope: 'all' }
    nodes.value.push({
      id: newID(),
      parent_id: null,
      position: topNodes.value.length,
      node_kind: 'module',
      node_type: 'question_details',
      content,
      props: {},
      schema_version: 1,
      source_question_node_id: null,
      anchor_before_node_id: null,
    })
  }
  renormalize()
  dirty.value = true
}

// ---- 画布 ----

/**
 * 画布报上来的新节点树。
 *
 * 画布不持有"当前节点数组"，事实源始终是这里的 `nodes`：它每次改动都把
 * **完整**的树交回来，由这一层决定标脏与保存时机。
 *
 * 直接整体替换而不是逐块合并：排版是全局的，插入一道题会让后面所有题号
 * 变化，逐块合并反而要处理"哪些块变了"这个更难的问题。
 */
function onCanvasUpdate(next: PaperNode[]) {
  nodes.value = next
  dirty.value = true
  validationWarned = false
  scheduleAutoSave()
}

/**
 * 画布转换时丢了节点（正常恒为空）。
 *
 * 不静默吞掉：会走到这里说明文档里出现了画布不认识的节点类型，
 * 继续编辑就会把它写没了。宁可打断用户，也不能让排版悄悄消失。
 */
function onDroppedNodes(dropped: { id: string; type: string }[]) {
  if (!dropped.length) return
  ElMessage.warning(
    `有 ${dropped.length} 个块的类型无法识别（${dropped.map((d) => d.type).join('、')}），已跳过；请保存前先确认内容。`,
  )
}

// ---- 自动保存 ----

/** 自动保存的去抖窗口。 */
const AUTO_SAVE_DELAY_MS = 1500
let autoSaveTimer: ReturnType<typeof setTimeout> | null = null

/**
 * 排一次自动保存。
 *
 * 只在"有编辑权限 + 没有待确认的 AI 改动"时才排：
 *  - 没有权限时保存必然被拒，排了只会反复弹错；
 *  - AI 的改动还挂在确认条上时落库，会把用户还没决定要不要的那版写进去。
 *    （这条与参考实现的自动保存闸门一致。）
 */
function scheduleAutoSave() {
  if (!canEdit.value) return
  if (pendingChange.value) return
  if (autoSaveTimer) clearTimeout(autoSaveTimer)
  autoSaveTimer = setTimeout(() => {
    autoSaveTimer = null
    // silent：自动保存不该每次弹一个"已保存"打断输入。
    void save({ silent: true })
  }, AUTO_SAVE_DELAY_MS)
}

/** 取消尚未触发的自动保存（离开页面、手动保存、应用 AI 改动时用）。 */
function cancelAutoSave() {
  if (autoSaveTimer) {
    clearTimeout(autoSaveTimer)
    autoSaveTimer = null
  }
}

// ---- 保存 ----

function contentHasText(value: unknown): boolean {
  if (typeof value === 'string') return value.trim().length > 0
  if (Array.isArray(value)) return value.some(contentHasText)
  if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>
    if (record.type === 'text') return String(record.text ?? '').trim().length > 0
    return contentHasText(record.content) || contentHasText(record.text)
  }
  return false
}

function saveIssues(): string[] {
  const issues: string[] = []
  const seenQuestions = new Set<number>()
  for (const node of nodes.value) {
    if (node.node_kind === 'reference') continue
    if (node.node_type === 'question') {
      const questionID = Number((node.content as { question_id?: number } | null)?.question_id ?? 0)
      if (!questionID || !questions.value[questionID]) {
        issues.push('存在未绑定有效题目的题目块')
      } else if (seenQuestions.has(questionID)) {
        issues.push(`题目 #${questionID} 在卷面上出现了多次`)
      } else {
        seenQuestions.add(questionID)
      }
    }
    if (node.node_type === 'rich_text' || node.node_type === 'heading') {
      if (!contentHasText(node.content)) issues.push('存在空白标题或说明块')
    }
    if (node.node_type === 'answer_space') {
      const lines = Number((node.props as { lines?: number } | null)?.lines ?? 0)
      if (lines < 1) issues.push('存在行数无效的答题区')
    }
  }
  return [...new Set(issues)]
}

let validationWarned = false

/** 转成提交结构：不带 position（服务端分配），不带服务端派生的 reference 节点。 */
function toInputs(): NodeInput[] {
  return nodes.value.filter((n) => n.node_kind !== 'reference').map(toNodeInput)
}

async function save(options: { silent?: boolean } = {}) {
  if (!canEdit.value) {
    ElMessage.warning('你没有编辑这份试卷的权限')
    return false
  }
  const issues = saveIssues()
  if (issues.length) {
    cancelAutoSave()
    if (options.silent) {
      if (!validationWarned) {
        validationWarned = true
        ElMessage.warning(`暂未保存：${issues[0]}。请修正后再保存。`)
      }
      return false
    }
    try {
      await ElMessageBox.confirm(
        `${issues.join('；')}。仍要继续保存吗？`,
        '保存前检查',
        { type: 'warning', confirmButtonText: '仍然保存', cancelButtonText: '返回修正' },
      )
    } catch {
      return false
    }
  }
  // 手动保存要掐掉已经排上的自动保存：否则保存完成后 1.5 秒又来一次，
  // 白白多一轮请求，还会把 revision 又推一格。
  cancelAutoSave()
  saving.value = true
  try {
    const result = await paperApi.replaceNodes(paperId.value, revision.value, toInputs())
    nodes.value = result.nodes.map((n) => ({ ...n }))
    revision.value = result.revision
    dirty.value = false
    if (!options.silent) ElMessage.success('已保存')
    await checkStale()
    return true
  } catch (error) {
    if (error instanceof ApiError && error.isConflict) {
      // 乐观锁冲突：别人已经改过这份卷子。绝不能直接覆盖，
      // 也不能静默丢弃本地改动——交给用户决定。
      try {
        await ElMessageBox.confirm(
          '这份试卷已被他人修改。重新加载会丢弃你当前的改动；取消则保留本地改动，你可以先复制内容再重载。',
          '内容已被他人修改',
          { type: 'warning', confirmButtonText: '重新加载', cancelButtonText: '保留我的改动' },
        )
        await load()
      } catch {
        // 用户选择保留本地改动
      }
    }
    return false
  } finally {
    saving.value = false
  }
}

function goToImportSource() {
  if (!paper.value?.import_task_id) return
  void router.push({ name: 'import-history', query: { task: String(paper.value.import_task_id) } })
}

async function syncStaleQuestions() {
  if (!staleQuestions.value.length) return
  const result = await paperApi.syncQuestionNodes(paperId.value, staleQuestions.value)
  // 同步会改动服务端的节点修订，因此本地必须整体重载，不能只改内存。
  await load()
  ElMessage.success(`已同步 ${result.synced.length} 道题的最新内容（修订 r${result.revision}）`)
}

// ---- AI 改动的暂存区 ----
//
// 组卷工具算出的是一份**候选树**：AI 落笔之前用户也还没点头。
// 应用之前工作副本一个字节都不动；放弃只需要丢掉这份候选，不用回滚任何东西。

/** 等待用户确认的改动。同一时刻只允许存在一批：叠第二批会攒出一份没人看得懂的差异。 */
const pendingChange = ref<PendingPaperChange | null>(null)
const applyingChange = ref(false)

/** 把候选树并入工作副本。 */
function adoptPendingChange(change: PendingPaperChange) {
  nodes.value = change.nodes
  const ids = Object.keys(change.projections)
  if (ids.length && paper.value) {
    paper.value = {
      ...paper.value,
      questions: { ...questions.value, ...change.projections },
    }
  }
  dirty.value = true
}

async function applyPendingChange() {
  const change = pendingChange.value
  if (!change) return
  applyingChange.value = true
  try {
    adoptPendingChange(change)
    pendingChange.value = null
    // 应用完立即落库：模型已经告诉用户"改动已排入待确认"，
    // 只停在工作副本里的话，用户一关页面这批改动就没了。
    // 整树覆盖带 revision 乐观锁——期间别人改过会被 409 挡下，
    // save() 内部已经处理好冲突弹窗，绝不会拿一份过期整树覆盖回去。
    await save({ silent: true })
  } finally {
    applyingChange.value = false
  }
}

function discardPendingChange() {
  // 候选树是一次性产物：直接丢掉，工作副本从未被碰过。
  pendingChange.value = null
  // 放弃之后再排一次自动保存：用户在待确认期间可能还在编辑，
  // 那段时间 scheduleAutoSave 是被挂起的（见它的注释），
  // 不在这里补一次，那些改动就只剩手动保存这一条路了。
  if (dirty.value) scheduleAutoSave()
}

const aiHost: PaperAiHost = {
  getTitle: () => paper.value?.title ?? '',
  getNodes: () => nodes.value,
  getProjections: () => questions.value,
  isScoringEnabled: () => scoringEnabled.value,
  isDirty: () => dirty.value,
  getRevision: () => revision.value,
  canEdit: () => canEdit.value,
  hasPending: () => pendingChange.value !== null,
  stagePending: (change) => {
    pendingChange.value = change
    // 助手算完就把面板拉开：用户得能立刻看到"有什么改动等我点头"，
    // 否则一个静静躺在别处的确认条等于没有。
    chatVisible.value = true
  },
}

usePaperAiTools(aiHost)

// ---- 内嵌对话 ----
//
// 为什么必须在这里：read_paper_outline / add_questions_to_paper / edit_paper
// 是客户端工具，只有"用户正在编辑的那份卷子"能执行它们。
// 对话若只存在于一个独立路由里，模型拿到票据时页面上根本没有编辑器，
// 每一次调用都只能回一句"当前页面无法执行"。

const subject = useSubjectStore()

const chatVisible = ref(false)
const chatSessionId = ref<number | null>(null)
const chatInput = ref('')

const CHAT_EXAMPLES = [
  '这份卷子现在什么结构？',
  '找 3 道二次函数的题加进来',
  '在每道大题前加一行「（本大题 12 分）」',
]
const chatSending = ref(false)
const chatScroll = ref<HTMLElement | null>(null)
const chatBubbles = ref<ChatBubble[]>([])

interface ChatToolTrace {
  name: string
  output?: string
  running: boolean
}

interface ChatBubble {
  role: 'user' | 'assistant'
  content: string
  streaming?: boolean
  tools: ChatToolTrace[]
  error?: string
  /** 逐轮累加的 token 用量，来自 usage 帧。 */
  usage?: RunUsage
  /** 气泡下方的收尾说明：这次运行为什么停（预算耗尽、被取消……）。 */
  notice?: string | null
  /** 气泡下方的用量说明。 */
  usageText?: string | null
}

let chatController: AbortController | null = null

async function scrollChatToBottom() {
  await nextTick()
  const box = chatScroll.value
  if (box) box.scrollTop = box.scrollHeight
}

function newChatSession() {
  chatSessionId.value = null
  chatBubbles.value = []
}

/**
 * 处理一帧对话事件。
 *
 * 客户端工具那一支只负责"把票据交出去"（answerClientToolFrame 保证它一定有回执），
 * 工具轨迹卡片的收尾交给随后必然到达的 tool_result 帧。
 */
function handleChatFrame(frame: ChatStreamFrame, assistant: ChatBubble) {
  const payload = frame.data
  switch (frame.event) {
    case 'message':
      assistant.content += String(payload.text ?? '')
      break
    case 'tool_call':
      assistant.tools.push({ name: String(payload.tool ?? '工具'), running: true })
      break
    case 'tool_result': {
      const name = String(payload.tool ?? '')
      for (let i = assistant.tools.length - 1; i >= 0; i--) {
        if (assistant.tools[i].name === name && assistant.tools[i].running) {
          assistant.tools[i].running = false
          assistant.tools[i].output = String(payload.output ?? '完成')
          break
        }
      }
      break
    }
    case 'client_tool':
      assistant.tools.push({ name: String(payload.tool ?? '客户端工具'), running: true })
      answerClientToolFrame(payload)
      break
    case 'error':
      assistant.error = String(payload.message ?? '出错了')
      break
    case 'proposal':
      assistant.content += `\n（另有 ${((payload.ids ?? []) as number[]).length} 道候选题目待定）`
      break
    case 'usage': {
      // 每一轮模型调用上报一次增量，气泡上要展示的是整次运行的合计。
      const usage = addUsage(assistant.usage, payload)
      assistant.usage = usage
      assistant.usageText = usageNote(usage)
      break
    }
    case 'done':
      // 墙钟超时时气泡可能是空的：这行收尾说明就是用户唯一能看到的东西。
      assistant.notice = stopNotice(payload)
      break
    default:
      break
  }
  void scrollChatToBottom()
}

async function sendChat() {
  const content = chatInput.value.trim()
  if (!content || chatSending.value) return
  const subjectID = paper.value?.subject_id ?? subject.currentId
  if (!subjectID) {
    ElMessage.warning('请先选择学科')
    return
  }

  let sessionId = chatSessionId.value
  if (sessionId === null) {
    const session = await chatApi.createSession({
      subject_id: subjectID,
      title: '组卷助手',
      // 固定组卷场景：只有这一组工具能在编辑器里落地，
      // 在这里放开其它场景只会换来一串"当前页面无法执行"。
      scene: 'composition_editor',
    })
    chatSessionId.value = session.id
    sessionId = session.id
  }

  chatBubbles.value.push({ role: 'user', content, tools: [] })
  chatInput.value = ''
  const assistant: ChatBubble = { role: 'assistant', content: '', streaming: true, tools: [] }
  chatBubbles.value.push(assistant)
  await scrollChatToBottom()

  chatSending.value = true
  chatController = new AbortController()
  try {
    await streamChatReply({
      sessionId,
      content,
      scene: 'composition_editor',
      signal: chatController.signal,
      onFrame: (frame) => handleChatFrame(frame, assistant),
    })
  } catch (error) {
    if ((error as Error).name === 'AbortError') {
      assistant.error = '已停止生成'
    } else if (error instanceof ApiError) {
      assistant.error = error.detail ? `${error.message}：${error.detail}` : error.message
    } else {
      assistant.error = (error as Error).message || '请求失败'
    }
  } finally {
    assistant.streaming = false
    chatSending.value = false
    chatController = null
    await scrollChatToBottom()
  }
}

async function onChatKeydown(event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
    event.preventDefault()
    await sendChat()
  }
}

/** 中断本轮流式生成。 */
function stopChat() {
  chatController?.abort()
}

onBeforeUnmount(() => {
  // 先掐掉待触发的自动保存：组件都拆了还去改 nodes/revision 没有意义，
  // 而 silent 保存失败时的提示也无人可看。
  cancelAutoSave()
  chatController?.abort()
  // 离开页面时把还在途的票据结清：处理器多半只是没跑完，
  // 留着不管等于让后端白等满 30 秒。
  abandonInflightClientTools('离开组卷页面')
})

// ---- 设置 / 定稿 / 导出 ----

function openSettings() {
  if (!paper.value) return
  settingsForm.title = paper.value.title
  settingsForm.description = paper.value.description
  settingsForm.numbering_enabled = paper.value.numbering_enabled
  settingsForm.scoring_enabled = paper.value.scoring_enabled
  settingsForm.question_display = { ...paper.value.question_display }
  settingsVisible.value = true
}

async function submitSettings() {
  if (!settingsForm.title.trim()) {
    ElMessage.warning('标题不能为空')
    return
  }
  const detail = await paperApi.update(paperId.value, {
    title: settingsForm.title.trim(),
    description: settingsForm.description,
    numbering_enabled: settingsForm.numbering_enabled,
    scoring_enabled: settingsForm.scoring_enabled,
    question_display: settingsForm.question_display,
  })
  applyDetail(detail)
  settingsVisible.value = false
  ElMessage.success('设置已保存')
}

async function finalize() {
  if (dirty.value) {
    const ok = await save({ silent: true })
    if (!ok) return
  }
  try {
    const { value } = await ElMessageBox.prompt(
      '定稿会冻结当前内容为一个不可变的版本快照。以后的改动不会影响该版本。',
      '定稿',
      {
        confirmButtonText: '定稿',
        cancelButtonText: '取消',
        inputPlaceholder: '版本说明，如：第一次月考最终版',
      },
    )
    const version = await paperApi.finalize(paperId.value, value ?? '')
    ElMessage.success(`已定稿为 v${version.version_no}`)
    await load()
  } catch {
    // 用户取消
  }
}

async function openVersions() {
  versionsVisible.value = true
  versions.value = await paperApi.listVersions(paperId.value)
}

/** 打开只读版本预览。关闭抽屉再回到这里，工作副本不会因为导航丢失。 */
function openVersion(versionNo: number) {
  void router.push({ name: 'paper-version', params: { id: paperId.value, versionNo } })
}

async function exportPaper(format: 'docx' | 'latex', versionNo?: number) {
  if (!paper.value) return
  if (dirty.value && !versionNo) {
    ElMessage.warning('请先保存再导出')
    return
  }
  exporting.value = true
  try {
    if (versionNo) {
      await paperApi.exportVersion(paperId.value, versionNo, format, paper.value.title)
    } else {
      await paperApi.exportPaper(paperId.value, format, paper.value.title)
    }
    ElMessage.success('已开始下载')
  } finally {
    exporting.value = false
  }
}

// ---- 题目选择器 ----

function openPicker() {
  pickerVisible.value = true
  pickerSelected.value = []
  pickerQuery.page = 1
  pickerRows.value = []
  void searchPicker()
}

async function searchPicker() {
  if (!paper.value) return
  pickerLoading.value = true
  try {
    const result = await questionApi.list(paper.value.subject_id, {
      keyword: pickerQuery.keyword,
      type: (pickerQuery.type || '') as never,
      status: (pickerQuery.status || '') as never,
      // 已发布的题才能入卷：把草稿编进正式试卷没有意义。
      root_only: true,
      page: pickerQuery.page,
      page_size: pickerQuery.page_size,
    })
    pickerRows.value = pickerQuery.page === 1 ? (result.list ?? []) : [...pickerRows.value, ...(result.list ?? [])]
    pickerTotal.value = result.total ?? 0
  } finally {
    pickerLoading.value = false
  }
}

function loadMorePicker() {
  pickerQuery.page += 1
  void searchPicker()
}

function togglePick(row: QuestionBrief) {
  if (isUsed(row)) {
    ElMessage.warning('这道题已在试卷中')
    return
  }
  const index = pickerSelected.value.findIndex((r) => r.id === row.id)
  if (index >= 0) pickerSelected.value.splice(index, 1)
  else pickerSelected.value.push(row)
}

function isPicked(row: QuestionBrief): boolean {
  return pickerSelected.value.some((r) => r.id === row.id)
}

/** 已在卷中的题目 id，用于在选择器里标注。 */
const usedQuestionIds = computed(() => {
  const set = new Set<number>()
  for (const node of nodes.value) {
    if (node.node_type === 'question') {
      const ref = node.content as QuestionRef | null
      if (ref?.question_id) set.add(ref.question_id)
    }
  }
  return set
})

function isUsed(row: QuestionBrief): boolean {
  return usedQuestionIds.value.has(row.id)
}

</script>

<template>
  <div class="space-y-3">
    <!-- 顶部工具栏 -->
    <el-card shadow="never" body-class="!px-3 !py-2.5">
      <div class="flex flex-wrap items-center gap-3">
        <el-button text aria-label="返回试卷列表" @click="router.push({ name: 'papers' })">
          <el-icon><ArrowLeft /></el-icon>
        </el-button>

        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <h1 class="truncate text-base font-semibold tracking-tight text-ink">
              {{ paper?.title ?? '加载中' }}
            </h1>
            <el-tag v-if="paper" size="small" :type="paper.status === 'finalized' ? 'success' : 'info'">
              {{ paper.status_label }}
            </el-tag>
            <el-tag v-if="paper?.finalized_version" size="small" effect="plain" class="num">
              v{{ paper.finalized_version }}
            </el-tag>
            <el-tag v-if="dirty" size="small" type="warning">未保存</el-tag>
          </div>
          <div class="num text-xs text-ink-subtle">
            共 {{ questionCount }} 题 · 修订 r{{ revision }}
            <template v-if="scoringEnabled">
              · 总分 {{ displayTotalScore }}<template v-if="dirty">（未保存）</template>
            </template>
          </div>
        </div>

        <div class="ml-auto flex items-center gap-2">
          <el-button v-if="paper?.import_task_id" text @click="goToImportSource">
            来源导入 #{{ paper.import_task_id }}
          </el-button>
          <el-badge :value="pendingChange ? pendingChange.changes.length : 0" :hidden="!pendingChange" type="warning">
            <el-button @click="chatVisible = true">AI 助手</el-button>
          </el-badge>
          <el-button v-if="canEdit" :disabled="!dirty" :loading="saving" type="primary" @click="save()">
            保存
          </el-button>
          <el-button @click="openSettings">设置</el-button>
          <el-button @click="openVersions">版本</el-button>
          <el-button @click="timelineVisible = true">时间线</el-button>
          <el-dropdown :disabled="exporting" @command="(f: 'docx' | 'latex') => exportPaper(f)">
            <el-button :loading="exporting">
              导出<el-icon class="ml-1"><ArrowDown /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="docx">Word (.docx)</el-dropdown-item>
                <el-dropdown-item command="latex">LaTeX 压缩包 (.zip)</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <el-button v-if="canFinalize" @click="finalize">定稿</el-button>
        </div>
      </div>
    </el-card>

    <!-- 题目被改动的提示 -->
    <el-alert v-if="staleQuestions.length" type="warning" :closable="false" show-icon>
      <template #title>
        <div class="flex items-center justify-between">
          <span class="text-sm">
            有 {{ staleQuestions.length }} 道题在入卷之后被修改过，试卷里仍是旧内容。
          </span>
          <el-button v-if="canEdit" size="small" type="primary" @click="syncStaleQuestions">
            同步最新内容
          </el-button>
        </div>
      </template>
    </el-alert>

    <!-- AI 改动闸门：候选树在用户点「应用」之前，一个字节都没进工作副本 -->
    <CompositionAiChangesBar
      v-if="pendingChange"
      :summary="pendingChange.summary"
      :changes="pendingChange.changes"
      :applying="applyingChange"
      @apply="applyPendingChange"
      @discard="discardPendingChange"
    />

    <div class="flex gap-3">
      <!-- 左：题目选择器 -->
      <el-card v-if="canEdit" shadow="never" class="w-80 shrink-0">
        <template #header>
          <div class="flex items-center justify-between">
            <span class="text-sm font-medium">从题库选题</span>
            <el-button text size="small" aria-label="从题库添加题目" @click="openPicker">
              <el-icon><Plus /></el-icon>
            </el-button>
          </div>
        </template>

        <el-input
          v-model="pickerQuery.keyword"
          placeholder="搜索题干"
          clearable
          size="small"
          class="mb-2"
          @keyup.enter="pickerQuery.page = 1; searchPicker()"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>

        <div v-loading="pickerLoading" class="max-h-[420px] overflow-y-auto">
          <div
            v-for="row in pickerRows"
            :key="row.id"
            class="mb-1 cursor-pointer rounded border p-2 text-xs transition-colors"
            :class="isPicked(row) ? 'border-accent bg-accent-soft' : 'border-hairline hover:bg-surface-sunken'"
            @click="togglePick(row)"
          >
            <div class="flex items-start gap-1">
              <el-tag v-if="isUsed(row)" size="small" type="info" effect="plain">已入卷</el-tag>
              <RichContent
                :doc="row.stem_doc"
                compact
                class="stem-clamp-1 flex-1"
              />
            </div>
          </div>

          <EmptyState v-if="!pickerLoading && pickerRows.length === 0" description="没有匹配的题目" />

          <el-button v-if="pickerHasMore" text size="small" class="w-full" @click="loadMorePicker">
            加载更多
          </el-button>
        </div>

        <el-button
          type="primary"
          class="mt-2 w-full"
          :disabled="pickerSelected.length === 0"
          :loading="addingQuestions"
          @click="addQuestions(pickerSelected)"
        >
          加入选中 {{ pickerSelected.length ? `(${pickerSelected.length})` : '' }}
        </el-button>
      </el-card>

      <!-- 右：文档结构 -->
      <el-card shadow="never" class="min-w-0 flex-1">
        <template #header>
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-sm font-medium">试卷结构</span>
            <div v-if="canEdit" class="ml-auto flex flex-wrap gap-1">
              <el-button size="small" @click="addHeading">+ 标题</el-button>
              <el-button size="small" @click="addRichText">+ 说明</el-button>
              <el-button size="small" @click="addPageBreak">+ 分页符</el-button>
              <el-button size="small" @click="addAnswerSpace">+ 答题区</el-button>
              <el-button v-if="scoringEnabled" size="small" @click="setUniformScore">统一分值</el-button>
              <el-button size="small" :type="answerModule ? 'warning' : 'default'" @click="toggleAnswerModule">
                {{ answerModule ? '移除解析模块' : '+ 解析模块' }}
              </el-button>
            </div>
          </div>
        </template>

        <!--
          整份试卷是一份 Tiptap 文档：块之间可以连续编辑、能拖动整块搬家、
          撤销能一步退掉一次输入。旧的卡片列表（每个块一个独立 DOM +
          弹窗编辑）已经被它取代，空卷的引导也由画布自己给。
        -->
        <PaperTiptapCanvas
          :nodes="nodes"
          :projections="questions"
          :can-edit="canEdit"
          :numbering-enabled="numberingEnabled"
          :scoring-enabled="scoringEnabled"
          @update="onCanvasUpdate"
          @dropped-nodes="onDroppedNodes"
        />
      </el-card>
    </div>


    <!-- 设置 -->
    <el-dialog v-model="settingsVisible" title="试卷设置" width="520px">
      <el-form label-width="90px">
        <el-form-item label="标题" required>
          <el-input v-model="settingsForm.title" maxlength="200" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="settingsForm.description" type="textarea" :rows="2" maxlength="512" />
        </el-form-item>
        <el-form-item label="排版选项">
          <div class="space-y-1">
            <CompositionNumberingPanel
              v-model:mode="numberingMode"
              :enabled="settingsForm.numbering_enabled"
              :disabled="!canEdit"
              :question-count="questionCount"
              class="w-full"
              @update:enabled="(v: boolean) => (settingsForm.numbering_enabled = v)"
              @autofill="applyNumbering"
            />
            <el-checkbox v-model="settingsForm.scoring_enabled">启用分值统计</el-checkbox>
          </div>
        </el-form-item>
        <el-form-item label="展示字段">
          <div class="space-y-1">
            <el-checkbox v-model="settingsForm.question_display.show_analysis">显示解析</el-checkbox>
            <el-checkbox v-model="settingsForm.question_display.show_solution">显示解题过程</el-checkbox>
            <el-checkbox v-model="settingsForm.question_display.show_summary">显示小结</el-checkbox>
            <el-checkbox v-model="settingsForm.question_display.show_knowledge">显示知识点</el-checkbox>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="settingsVisible = false">取消</el-button>
        <el-button type="primary" @click="submitSettings">保存</el-button>
      </template>
    </el-dialog>

    <!-- 版本 -->
    <el-drawer v-model="versionsVisible" title="版本历史" size="520px">
      <EmptyState v-if="versions.length === 0" description="还没有定稿版本" />
      <div v-for="version in versions" :key="version.id" class="mb-2 rounded border border-hairline p-3">
        <div class="flex items-center justify-between">
          <div>
            <span class="num font-medium">v{{ version.version_no }}</span>
            <span class="ml-2 text-xs text-ink-subtle">{{ version.question_count }} 题</span>
          </div>
          <el-dropdown @command="(f: 'docx' | 'latex') => exportPaper(f, version.version_no)">
            <el-button text size="small">导出</el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="docx">Word</el-dropdown-item>
                <el-dropdown-item command="latex">LaTeX</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <el-button text size="small" type="primary" @click="openVersion(version.version_no)">
            查看
          </el-button>
        </div>
        <div v-if="version.note" class="mt-1 text-xs text-ink-muted">{{ version.note }}</div>
        <div class="mt-1 text-xs text-ink-subtle">{{ new Date(version.created_at).toLocaleString('zh-CN') }}</div>
      </div>
    </el-drawer>

    <!-- 操作时间线 -->
    <CompositionTimelineSheet
      v-model="timelineVisible"
      :paper-id="paperId"
      :title="paper?.title ?? ''"
    />

    <!-- 大选择器 -->
    <el-dialog v-model="pickerVisible" title="从题库选题" width="760px">
      <div class="mb-3 flex items-center gap-2">
        <el-input
          v-model="pickerQuery.keyword"
          placeholder="搜索题干"
          class="w-64"
          @keyup.enter="pickerQuery.page = 1; searchPicker()"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-select v-model="pickerQuery.status" placeholder="状态" clearable class="w-32" @change="pickerQuery.page = 1; searchPicker()">
          <el-option label="已发布" value="published" />
          <el-option label="待审核" value="pending" />
        </el-select>
        <el-button type="primary" @click="pickerQuery.page = 1; searchPicker()">搜索</el-button>
      </div>

      <TableSkeleton v-if="pickerLoading" :rows="6" :cols="6" />
      <el-table v-else
        :data="pickerRows"
        height="380"
        @row-click="togglePick"
        :row-class-name="({ row }: { row: QuestionBrief }) => (isPicked(row) ? 'picked-row' : '')"
      >
        <el-table-column width="50">
          <template #default="{ row }">
            <el-checkbox :model-value="isPicked(row)" @click.stop="togglePick(row)" />
          </template>
        </el-table-column>
        <el-table-column label="题干" min-width="320">
          <template #default="{ row }">
            <RichContent :doc="row.stem_doc" compact class="stem-clamp text-sm" />
            <div class="mt-0.5 flex gap-1">
              <el-tag v-if="isUsed(row)" size="small" type="info" effect="plain">已入卷</el-tag>
              <el-tag size="small" effect="plain">{{ row.question_type_label }}</el-tag>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="难度" width="100">
          <template #default="{ row }">
            <DifficultyStars :level="row.difficulty" />
          </template>
        </el-table-column>
      </el-table>

      <div class="mt-2 flex items-center justify-between">
        <span class="text-xs text-ink-subtle">已选 {{ pickerSelected.length }} 道</span>
        <el-button v-if="pickerHasMore" text size="small" @click="loadMorePicker">加载更多</el-button>
      </div>

      <template #footer>
        <el-button @click="pickerVisible = false">取消</el-button>
        <el-button type="primary" :disabled="pickerSelected.length === 0" :loading="addingQuestions" @click="addQuestions(pickerSelected)">
          加入试卷（{{ pickerSelected.length }}）
        </el-button>
      </template>
    </el-dialog>
  <!--
      内嵌对话。放在组卷编辑器里而不是只留一个 /chat 路由，
      是因为三个客户端工具只认"用户正在编辑的那份卷子"——
      对话不在这里，票据就永远没有执行器。
    -->
    <el-drawer v-model="chatVisible" title="组卷助手" size="420px" append-to-body>
      <template #header>
        <div class="flex w-full items-center justify-between pr-2">
          <span class="text-sm font-medium">组卷助手</span>
          <el-button text size="small" @click="newChatSession">新对话</el-button>
        </div>
      </template>

      <div class="flex h-full flex-col">
        <div ref="chatScroll" class="flex-1 space-y-3 overflow-y-auto pr-1">
          <EmptyState v-if="chatBubbles.length === 0" description="让助手帮你排这份卷子">
            <div class="flex flex-wrap justify-center gap-2">
              <button
                v-for="example in CHAT_EXAMPLES"
                :key="example"
                type="button"
                class="rounded-full border border-hairline px-3 py-1.5 text-xs text-ink-muted transition-colors hover:border-accent-border hover:bg-accent-soft hover:text-accent-text"
                @click="chatInput = example"
              >
                {{ example }}
              </button>
            </div>
          </EmptyState>

          <div
            v-for="(bubble, index) in chatBubbles"
            :key="index"
            class="flex"
            :class="bubble.role === 'user' ? 'justify-end' : 'justify-start'"
          >
            <div
              class="max-w-[85%] rounded-lg px-3 py-2"
              :class="bubble.role === 'user' ? 'bg-accent text-white' : 'bg-surface-sunken'"
            >
              <div v-if="bubble.tools.length" class="mb-1.5 space-y-1">
                <div
                  v-for="(tool, ti) in bubble.tools"
                  :key="ti"
                  class="flex items-center gap-1 rounded bg-surface/70 px-2 py-1 text-xs text-ink-regular"
                >
                  <el-icon v-if="tool.running" class="is-loading"><Loading /></el-icon>
                  <el-icon v-else><Select /></el-icon>
                  <span>{{ tool.name }}</span>
                  <span v-if="tool.output" class="truncate text-ink-subtle">{{ tool.output }}</span>
                </div>
              </div>

              <MarkdownContent v-if="bubble.content" :content="bubble.content" />

              <div v-if="bubble.streaming && !bubble.content" class="flex items-center gap-1 text-xs text-ink-subtle">
                <el-icon class="is-loading"><Loading /></el-icon>
                思考中…
              </div>

              <div v-if="bubble.error" class="mt-1 text-xs text-danger-text">{{ bubble.error }}</div>

              <!-- 收尾信息：为什么停、花了多少 tokens。正常收尾时两行都不出现。 -->
              <div
                v-if="bubble.notice || bubble.usageText"
                class="mt-1.5 space-y-0.5 text-xs text-ink-subtle"
              >
                <div v-if="bubble.notice">{{ bubble.notice }}</div>
                <div v-if="bubble.usageText">{{ bubble.usageText }}</div>
              </div>
            </div>
          </div>
        </div>

        <div class="mt-3 border-t border-hairline pt-3">
          <el-input
            v-model="chatInput"
            type="textarea"
            :rows="3"
            resize="none"
            placeholder="问助手，或直接说「把这几道题加进来」"
            :disabled="chatSending"
            @keydown="onChatKeydown"
          />
          <div class="mt-2 flex items-center gap-2">
            <el-button v-if="chatSending" type="danger" plain @click="stopChat">停止生成</el-button>
            <el-button v-else type="primary" :disabled="!chatInput.trim()" @click="sendChat">发送</el-button>
            <span class="text-xs text-ink-subtle">改动会先预览，等你点「应用」。</span>
          </div>
        </div>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
:deep(.picked-row) {
  background-color: var(--el-color-primary-light-9);
}
.bg-dashed {
  background-image: linear-gradient(to right, #d1d5db 50%, transparent 50%);
  background-size: 8px 1px;
  background-color: transparent;
}
</style>
