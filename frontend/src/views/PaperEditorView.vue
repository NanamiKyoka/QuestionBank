<script setup lang="ts">
import { computed, nextTick, onMounted, onBeforeUnmount, reactive, ref } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
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
} from '@/api/paper'
import type { QuestionBrief } from '@/api/types'
import RichContent from '@/components/RichContent.vue'
import CompositionNumberingPanel from '@/components/CompositionNumberingPanel.vue'
import CompositionTimelineSheet from '@/components/CompositionTimelineSheet.vue'
import CompositionAiChangesBar from '@/components/CompositionAiChangesBar.vue'
import RichTextEditor from '@/components/editor/RichTextEditor.vue'
import { emptyDoc } from '@/lib/richdoc'
import {
  answerTextOf,
  blockLabel,
  computeNumbers,
  displayNumbers,
  headingLevelOf,
  nodeProps,
  numberOf,
  propsWithNumber,
  propsWithScore,
  roundScore,
  scoreOf,
  totalScoreOf,
} from '@/lib/paperView'
import { abandonInflightClientTools, answerClientToolFrame } from '@/composables/useAiClientTools'
import { streamChatReply, type ChatStreamFrame } from '@/composables/useChatStream'
import { usePaperAiTools, type PaperAiHost, type PendingPaperChange } from '@/composables/usePaperAiTools'
import { useSubjectStore } from '@/stores/subject'

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

/** 块编辑器覆盖的节点类型。
 *
 * 答题区也在其中：它只有"行数 + 样式"两个块属性，和标题的级别同性质，
 * 再单开一个弹窗只会让工具栏多一个入口。
 */
type BlockEditorType = 'rich_text' | 'heading' | 'answer_space'

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
 */
const ANSWER_LINE_HEIGHT_PX = 30
const DEFAULT_ANSWER_LINES = 4
/** 行数上下限，与参考实现的 1..50 一致，避免手滑输出一整页空白。 */
const MIN_ANSWER_LINES = 1
const MAX_ANSWER_LINES = 50

function clampAnswerLines(n: number): number {
  if (!Number.isFinite(n)) return DEFAULT_ANSWER_LINES
  return Math.min(Math.max(Math.round(n), MIN_ANSWER_LINES), MAX_ANSWER_LINES)
}

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

// ---- 块编辑 ----
const blockVisible = ref(false)
const blockForm = reactive({
  id: '' as string,
  nodeType: 'rich_text' as BlockEditorType,
  level: 1,
  lines: DEFAULT_ANSWER_LINES,
  answerStyle: 'blank' as AnswerSpaceStyle,
  content: emptyDoc() as unknown,
})

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
 */
const questionNumbers = computed(() => displayNumbers(topNodes.value))

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

/** 有未保存改动时离开页面要拦一下。 */
onBeforeRouteLeave(async () => {
  if (!dirty.value) return true
  try {
    await ElMessageBox.confirm('当前试卷有未保存的改动，确定离开吗？', '未保存的改动', {
      type: 'warning',
      confirmButtonText: '离开并丢弃',
      cancelButtonText: '留在这里',
    })
    return true
  } catch {
    return false
  }
})

function onBeforeUnload(event: BeforeUnloadEvent) {
  if (dirty.value) {
    event.preventDefault()
    // 现代浏览器忽略自定义文案，但必须调用才能触发原生确认。
    event.returnValue = ''
  }
}
onMounted(() => window.addEventListener('beforeunload', onBeforeUnload))
onBeforeUnmount(() => window.removeEventListener('beforeunload', onBeforeUnload))

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

function addQuestions(rows: QuestionBrief[]) {
  if (!rows.length) return
  for (const row of rows) {
    const content: QuestionRef = {
      question_id: row.id,
      // 记录入卷时的题目修订号，后续据此判断"题目已被改动"。
      content_revision: (questions.value[row.id]?.content_revision ?? 0) || 0,
    }
    appendNode({
      id: newID(),
      parent_id: null,
      node_kind: 'block',
      node_type: 'question',
      content,
      props: {},
      schema_version: 1,
      source_question_node_id: null,
      anchor_before_node_id: null,
    })
  }
  pickerVisible.value = false
  pickerSelected.value = []
  ElMessage.success(`已加入 ${rows.length} 道题，记得保存`)
}

function addRichText() {
  openBlockEditor(null, 'rich_text')
}

function addHeading() {
  openBlockEditor(null, 'heading')
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

function setModuleScope(scope: 'all' | 'before') {
  const module = answerModule.value
  if (!module) return
  module.content = { scope } satisfies ModuleContent
  dirty.value = true
}

function moveNode(node: PaperNode, delta: number) {
  const list = topNodes.value
  const index = list.findIndex((n) => n.id === node.id)
  const target = index + delta
  if (index < 0 || target < 0 || target >= list.length) return

  const other = list[target]
  const tmp = node.position
  node.position = other.position
  other.position = tmp
  renormalize()
  dirty.value = true
}

function removeNode(node: PaperNode) {
  // 连同其子节点（解析条目）一起移除，否则会留下孤儿节点。
  nodes.value = nodes.value.filter((n) => n.id !== node.id && n.parent_id !== node.id)
  renormalize()
  dirty.value = true
}

function removeNodeWithConfirm(node: PaperNode) {
  const label =
    node.node_type === 'question'
      ? `第 ${questionNumbers.value.get(node.id) ?? '?'} 题`
      : node.node_type === 'question_details'
        ? '解析模块'
        : '该内容块'
  ElMessageBox.confirm(`确定从试卷中移除${label}吗？`, '移除确认', {
    type: 'warning',
    confirmButtonText: '移除',
    cancelButtonText: '取消',
  })
    .then(() => removeNode(node))
    .catch(() => undefined)
}

/** 返回类型谓词而非 boolean：模板里的 v-if 才能顺带把 node_type 收窄成
 *  块编辑器的可编辑类型，省掉一次手动断言。 */
function canEditBlock(node: PaperNode): node is PaperNode & { node_type: BlockEditorType } {
  return (
    node.node_type === 'rich_text' || node.node_type === 'heading' || node.node_type === 'answer_space'
  )
}

function openEditableBlock(node: PaperNode) {
  if (!canEditBlock(node)) return
  openBlockEditor(node, node.node_type)
}

/** 答题区行数。老节点把留白高度写在 content.height 上，
 *  这里读的时候顺手翻译成行数——否则一次编辑就会把用户先前设的
 *  留白静默改回默认值，而界面上看不出任何异常。 */
function answerLinesOf(node: PaperNode): number {
  const { lines } = nodeProps<AnswerSpaceProps>(node)
  if (typeof lines === 'number' && lines > 0) {
    return clampAnswerLines(lines)
  }
  const legacyHeight = (node.content as { height?: number } | null)?.height
  if (typeof legacyHeight === 'number' && legacyHeight > 0) {
    return clampAnswerLines(legacyHeight / ANSWER_LINE_HEIGHT_PX)
  }
  return DEFAULT_ANSWER_LINES
}

function answerStyleOf(node: PaperNode): AnswerSpaceStyle {
  return nodeProps<AnswerSpaceProps>(node).style === 'lined' ? 'lined' : 'blank'
}

function openBlockEditor(node: PaperNode | null, type: BlockEditorType) {
  if (node) {
    blockForm.id = node.id
    blockForm.nodeType = node.node_type as BlockEditorType
    blockForm.content = node.content ?? emptyDoc()
    blockForm.level = headingLevelOf(node)
    blockForm.lines = answerLinesOf(node)
    blockForm.answerStyle = answerStyleOf(node)
  } else {
    blockForm.id = ''
    blockForm.nodeType = type
    blockForm.content = type === 'heading' ? { type: 'doc', content: [{ type: 'heading', attrs: { level: 1 } }] } : emptyDoc()
    blockForm.level = 1
    blockForm.lines = DEFAULT_ANSWER_LINES
    blockForm.answerStyle = 'blank'
  }
  blockVisible.value = true
}

const blockDialogTitle = computed(() =>
  blockForm.nodeType === 'heading'
    ? '编辑标题'
    : blockForm.nodeType === 'answer_space'
      ? '编辑答题区'
      : '编辑说明文字',
)

/** 把块编辑器的表单折成节点 props。 */
function blockPropsOf(): Record<string, unknown> {
  if (blockForm.nodeType === 'heading') return { level: blockForm.level }
  if (blockForm.nodeType === 'answer_space') {
    return { lines: clampAnswerLines(blockForm.lines), style: blockForm.answerStyle }
  }
  return {}
}

function submitBlock() {
  const props = blockPropsOf()
  // 答题区没有富文本正文，content 必须置空，否则会把上一次编辑标题时
  // 留在表单里的富文本写进答题区节点。
  const content = blockForm.nodeType === 'answer_space' ? null : blockForm.content
  if (blockForm.id) {
    const node = nodes.value.find((n) => n.id === blockForm.id)
    if (node) {
      node.content = content
      node.props = props
      dirty.value = true
    }
  } else {
    appendNode({
      id: newID(),
      parent_id: null,
      node_kind: 'block',
      node_type: blockForm.nodeType,
      content,
      props,
      schema_version: 1,
      source_question_node_id: null,
      anchor_before_node_id: null,
    })
  }
  blockVisible.value = false
}

// ---- 保存 ----

/** 转成提交结构：不带 position（服务端分配），不带服务端派生的 reference 节点。 */
function toInputs(): NodeInput[] {
  return nodes.value.filter((n) => n.node_kind !== 'reference').map(toNodeInput)
}

async function save(options: { silent?: boolean } = {}) {
  if (!canEdit.value) {
    ElMessage.warning('你没有编辑这份试卷的权限')
    return false
  }
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
    case 'usage':
    case 'done':
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

/** 卷内某道题的答案文本，供组卷时核对。 */
function answerText(questionID: number): string {
  return answerTextOf(questions.value[questionID])
}
</script>

<template>
  <div v-loading="loading" class="space-y-3">
    <!-- 顶部工具栏 -->
    <el-card shadow="never">
      <div class="flex flex-wrap items-center gap-3">
        <el-button text @click="router.push({ name: 'papers' })">
          <el-icon><ArrowLeft /></el-icon>
        </el-button>

        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <span class="truncate text-base font-semibold">{{ paper?.title ?? '加载中…' }}</span>
            <el-tag v-if="paper" size="small" :type="paper.status === 'finalized' ? 'success' : 'info'">
              {{ paper.status_label }}
            </el-tag>
            <el-tag v-if="paper?.finalized_version" size="small" effect="plain">
              v{{ paper.finalized_version }}
            </el-tag>
            <el-tag v-if="dirty" size="small" type="warning">未保存</el-tag>
          </div>
          <div class="text-xs text-gray-400">
            共 {{ questionCount }} 题 · 修订 r{{ revision }}
            <template v-if="scoringEnabled">
              · 总分 {{ displayTotalScore }}<template v-if="dirty">（未保存）</template>
            </template>
          </div>
        </div>

        <div class="ml-auto flex items-center gap-2">
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
            <el-button :loading="exporting">导出<el-icon class="ml-1"><ArrowDown /></el-icon></el-button>
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
            <el-button text size="small" @click="openPicker">
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
            :class="isPicked(row) ? 'border-blue-400 bg-blue-50' : 'border-gray-200 hover:bg-gray-50'"
            @click="togglePick(row)"
          >
            <div class="flex items-start gap-1">
              <el-tag v-if="isUsed(row)" size="small" type="info" effect="plain">已入卷</el-tag>
              <span class="stem-clamp flex-1">{{ row.stem_text || '（无题干）' }}</span>
            </div>
          </div>

          <el-empty v-if="!pickerLoading && pickerRows.length === 0" description="没有匹配的题目" :image-size="48" />

          <el-button v-if="pickerHasMore" text size="small" class="w-full" @click="loadMorePicker">
            加载更多
          </el-button>
        </div>

        <el-button
          type="primary"
          class="mt-2 w-full"
          :disabled="pickerSelected.length === 0"
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

        <div v-if="topNodes.length === 0" class="py-10">
          <el-empty description="试卷还是空的，从左侧题库选题加入" />
        </div>

        <div v-else class="space-y-2">
          <div
            v-for="node in topNodes"
            :key="node.id"
            class="group rounded border border-gray-200 bg-white p-3"
            :class="node.node_type === 'question' ? 'border-l-4 border-l-blue-400' : ''"
          >
            <div class="flex items-start gap-2">
              <div class="min-w-0 flex-1">
                <!-- 题目块 -->
                <template v-if="node.node_type === 'question'">
                  <div class="mb-1 flex items-center gap-2">
                    <span v-if="!numberingEnabled" class="text-sm font-medium">
                      第 {{ questionNumbers.get(node.id) }} 题
                    </span>
                    <!-- 题号是节点属性：占位符显示"自动编出来的那个"，
                         填了就用填的，清空则退回自动编号。 -->
                    <div v-else class="flex shrink-0 items-center gap-0.5">
                      <span class="text-xs text-gray-400">第</span>
                      <el-input
                        :model-value="numberOf(node)"
                        :placeholder="questionNumbers.get(node.id)"
                        size="small"
                        class="!w-20"
                        :disabled="!canEdit"
                        maxlength="16"
                        @update:model-value="(v: string) => setNumber(node, v)"
                      />
                      <span class="text-xs text-gray-400">题</span>
                    </div>
                    <el-tag v-if="questions[(node.content as QuestionRef)?.question_id]?.stale" size="small" type="warning">
                      内容已更新
                    </el-tag>
                    <span class="text-xs text-gray-400">
                      {{ questions[(node.content as QuestionRef)?.question_id]?.question_type_label }}
                      ·
                      {{ '★'.repeat(questions[(node.content as QuestionRef)?.question_id]?.difficulty ?? 0) }}
                    </span>
                    <!-- 分值只写在 props.score 上，服务端 sumScore 与导出器都只认这个键。 -->
                    <div v-if="scoringEnabled" class="ml-auto flex shrink-0 items-center gap-1">
                      <span class="text-xs text-gray-400">分值</span>
                      <el-input-number
                        :model-value="scoreOf(node) ?? undefined"
                        :min="0"
                        :max="999"
                        :step="0.5"
                        :disabled="!canEdit"
                        size="small"
                        class="!w-24"
                        controls-position="right"
                        placeholder="未设"
                        @update:model-value="(v: number | undefined) => setScore(node, v)"
                      />
                    </div>
                  </div>

                  <template v-if="questions[(node.content as QuestionRef)?.question_id]">
                    <RichContent :doc="questions[(node.content as QuestionRef).question_id].stem" />
                    <div v-if="questions[(node.content as QuestionRef).question_id].options.length" class="mt-1 space-y-0.5">
                      <div
                        v-for="option in questions[(node.content as QuestionRef).question_id].options"
                        :key="option.id"
                        class="flex gap-1 text-sm"
                      >
                        <span class="font-medium">{{ option.label }}.</span>
                        <RichContent :doc="option.content" class="flex-1" />
                      </div>
                    </div>
                    <!-- 组卷时把答案显出来：老师需要边排边核对答案，
                         而导出时答案是否出现由「解析模块」与展示字段决定。 -->
                    <div
                      v-if="answerText((node.content as QuestionRef).question_id)"
                      class="mt-1 text-xs text-green-700"
                    >
                      答案：{{ answerText((node.content as QuestionRef).question_id) }}
                    </div>
                  </template>
                  <div v-else class="text-sm text-gray-400">题目内容不可用（可能已被删除）</div>
                </template>

                <!-- 标题 / 说明 -->
                <template v-else-if="node.node_type === 'rich_text' || node.node_type === 'heading'">
                  <div class="mb-1 text-xs text-gray-400">{{ blockLabel(node) }}</div>
                  <RichContent :doc="node.content" :compact="node.node_type === 'rich_text'" />
                </template>

                <!-- 分页符 -->
                <template v-else-if="node.node_type === 'page_break'">
                  <div class="flex items-center gap-2 text-xs text-gray-400">
                    <div class="h-px flex-1 bg-dashed bg-gray-300" />
                    分页符
                    <div class="h-px flex-1 bg-gray-300" />
                  </div>
                </template>

                <!-- 答题区 -->
                <template v-else-if="node.node_type === 'answer_space'">
                  <div class="mb-1 text-xs text-gray-400">
                    {{ blockLabel(node) }}（{{ answerLinesOf(node) }} 行 ·
                    {{ answerStyleOf(node) === 'lined' ? '横线' : '留白' }}）
                  </div>
                  <div class="rounded border border-dashed border-gray-300 px-2 py-1">
                    <template v-if="answerStyleOf(node) === 'lined'">
                      <div
                        v-for="line in answerLinesOf(node)"
                        :key="line"
                        class="h-6 border-b border-dashed border-gray-300"
                      />
                    </template>
                    <div v-else :style="{ height: `${answerLinesOf(node) * 24}px` }" />
                  </div>
                </template>

                <!-- 解析模块 -->
                <template v-else-if="node.node_type === 'question_details'">
                  <div class="mb-1 flex items-center gap-2">
                    <span class="text-sm font-medium">解析模块</span>
                    <el-select
                      :model-value="(node.content as ModuleContent)?.scope ?? 'all'"
                      size="small"
                      class="w-32"
                      :disabled="!canEdit"
                      @change="(v: 'all' | 'before') => setModuleScope(v)"
                    >
                      <el-option label="卷末汇总" value="all" />
                      <el-option label="每题之后" value="before" />
                    </el-select>
                  </div>
                  <div class="text-xs text-gray-400">
                    解析条目由服务端按当前题目顺序自动生成，共 {{ questionCount }} 条，无需手动维护。
                  </div>
                </template>
              </div>

              <!-- 节点操作 -->
              <div v-if="canEdit" class="flex shrink-0 flex-col gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
                <el-button text size="small" :disabled="topNodes[0]?.id === node.id" @click="moveNode(node, -1)">
                  <el-icon><Top /></el-icon>
                </el-button>
                <el-button
                  text
                  size="small"
                  :disabled="topNodes[topNodes.length - 1]?.id === node.id"
                  @click="moveNode(node, 1)"
                >
                  <el-icon><Bottom /></el-icon>
                </el-button>
                <el-button
                  v-if="canEditBlock(node)"
                  text
                  size="small"
                  @click="openEditableBlock(node)"
                >
                  <el-icon><Edit /></el-icon>
                </el-button>
                <el-button text size="small" type="danger" @click="removeNodeWithConfirm(node)">
                  <el-icon><Delete /></el-icon>
                </el-button>
              </div>
            </div>
          </div>
        </div>
      </el-card>
    </div>

    <!-- 块编辑 -->
    <el-dialog
      v-model="blockVisible"
      :title="blockDialogTitle"
      width="720px"
    >
      <el-form label-width="70px">
        <el-form-item v-if="blockForm.nodeType === 'heading'" label="级别">
          <el-radio-group v-model="blockForm.level">
            <el-radio :value="1">一级</el-radio>
            <el-radio :value="2">二级</el-radio>
            <el-radio :value="3">三级</el-radio>
          </el-radio-group>
        </el-form-item>
        <template v-if="blockForm.nodeType === 'answer_space'">
          <el-form-item label="行数">
            <el-input-number
              v-model="blockForm.lines"
              :min="MIN_ANSWER_LINES"
              :max="MAX_ANSWER_LINES"
              :step="1"
              size="small"
            />
            <span class="ml-2 text-xs text-gray-400">
              导出时按这个行数预留作答空间
            </span>
          </el-form-item>
          <el-form-item label="样式">
            <el-radio-group v-model="blockForm.answerStyle">
              <el-radio value="blank">留白</el-radio>
              <el-radio value="lined">横线</el-radio>
            </el-radio-group>
            <span class="ml-2 text-xs text-gray-400">横线样式会在每行下面画一条答题线</span>
          </el-form-item>
        </template>
        <el-form-item v-if="blockForm.nodeType !== 'answer_space'" label="内容">
          <RichTextEditor v-model="blockForm.content" placeholder="输入内容" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="blockVisible = false">取消</el-button>
        <el-button type="primary" @click="submitBlock">确定</el-button>
      </template>
    </el-dialog>

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
      <el-empty v-if="versions.length === 0" description="还没有定稿版本" />
      <div v-for="version in versions" :key="version.id" class="mb-2 rounded border border-gray-200 p-3">
        <div class="flex items-center justify-between">
          <div>
            <span class="font-medium">v{{ version.version_no }}</span>
            <span class="ml-2 text-xs text-gray-400">{{ version.question_count }} 题</span>
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
        <div v-if="version.note" class="mt-1 text-xs text-gray-500">{{ version.note }}</div>
        <div class="mt-1 text-xs text-gray-400">{{ new Date(version.created_at).toLocaleString('zh-CN') }}</div>
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

      <el-table
        v-loading="pickerLoading"
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
            <div class="stem-clamp text-sm">{{ row.stem_text || '（无题干）' }}</div>
            <div class="mt-0.5 flex gap-1">
              <el-tag v-if="isUsed(row)" size="small" type="info" effect="plain">已入卷</el-tag>
              <el-tag size="small" effect="plain">{{ row.question_type_label }}</el-tag>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="难度" width="100">
          <template #default="{ row }">
            <span class="text-amber-500">{{ '★'.repeat(row.difficulty) }}</span>
          </template>
        </el-table-column>
      </el-table>

      <div class="mt-2 flex items-center justify-between">
        <span class="text-xs text-gray-400">已选 {{ pickerSelected.length }} 道</span>
        <el-button v-if="pickerHasMore" text size="small" @click="loadMorePicker">加载更多</el-button>
      </div>

      <template #footer>
        <el-button @click="pickerVisible = false">取消</el-button>
        <el-button type="primary" :disabled="pickerSelected.length === 0" @click="addQuestions(pickerSelected)">
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
          <el-empty v-if="chatBubbles.length === 0" description="让助手帮你排这份卷子" :image-size="48">
            <div class="space-y-1 text-left text-xs text-gray-500">
              <div>· 这份卷子现在什么结构？</div>
              <div>· 找 3 道二次函数的题加进来</div>
              <div>· 在每道大题前加一行「（本大题 12 分）」</div>
            </div>
          </el-empty>

          <div
            v-for="(bubble, index) in chatBubbles"
            :key="index"
            class="flex"
            :class="bubble.role === 'user' ? 'justify-end' : 'justify-start'"
          >
            <div
              class="max-w-[85%] rounded-lg px-3 py-2"
              :class="bubble.role === 'user' ? 'bg-blue-600 text-white' : 'bg-gray-100'"
            >
              <div v-if="bubble.tools.length" class="mb-1.5 space-y-1">
                <div
                  v-for="(tool, ti) in bubble.tools"
                  :key="ti"
                  class="flex items-center gap-1 rounded bg-white/70 px-2 py-1 text-xs text-gray-600"
                >
                  <el-icon v-if="tool.running" class="is-loading"><Loading /></el-icon>
                  <el-icon v-else><Select /></el-icon>
                  <span>{{ tool.name }}</span>
                  <span v-if="tool.output" class="truncate text-gray-400">{{ tool.output }}</span>
                </div>
              </div>

              <div v-if="bubble.content" class="whitespace-pre-wrap break-words text-sm">
                {{ bubble.content }}
              </div>

              <div v-if="bubble.streaming && !bubble.content" class="flex items-center gap-1 text-xs text-gray-400">
                <el-icon class="is-loading"><Loading /></el-icon>
                思考中…
              </div>

              <div v-if="bubble.error" class="mt-1 text-xs text-red-500">{{ bubble.error }}</div>
            </div>
          </div>
        </div>

        <div class="mt-3 border-t border-gray-200 pt-3">
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
            <span class="text-xs text-gray-400">改动会先预览，等你点「应用」。</span>
          </div>
        </div>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
.stem-clamp {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-word;
}
:deep(.picked-row) {
  background-color: var(--el-color-primary-light-9);
}
.bg-dashed {
  background-image: linear-gradient(to right, #d1d5db 50%, transparent 50%);
  background-size: 8px 1px;
  background-color: transparent;
}
</style>
