<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { knowledgePointApi, questionApi, subjectApi, tagApi } from '@/api'
import type {
  KnowledgePoint,
  QuestionBatchAction,
  QuestionBrief,
  QuestionDetail,
  QuestionQuery,
  QuestionSort,
  QuestionStatus,
  QuestionType,
  ReviewLog,
  SubjectMember,
  Tag,
} from '@/api/types'
import RichContent from '@/components/RichContent.vue'
import QuestionEditDialog from '@/components/QuestionEditDialog.vue'
import QuestionAiChangesBar from '@/components/QuestionAiChangesBar.vue'
import {
  applyQuestionEdits,
  useQuestionAiTools,
  type PendingQuestionEdits,
} from '@/composables/useQuestionAiTools'
import DifficultyStars from '@/components/ui/DifficultyStars.vue'
import PageHeader from '@/components/ui/PageHeader.vue'
import TableSkeleton from '@/components/ui/TableSkeleton.vue'
import { useSubjectStore } from '@/stores/subject'
import { useBackgroundTaskStore } from '@/stores/backgroundTasks'

const route = useRoute()
const router = useRouter()
const subject = useSubjectStore()

const loading = ref(false)
const rows = ref<QuestionBrief[]>([])
const total = ref(0)
const selection = ref<QuestionBrief[]>([])

const knowledgePoints = ref<KnowledgePoint[]>([])
const tags = ref<Tag[]>([])
const members = ref<SubjectMember[]>([])
/** 「更多筛选」默认收起：多数人只用关键词 + 题型 + 状态。 */
const moreFiltersVisible = ref(false)

const editVisible = ref(false)
const editingId = ref<number | null>(null)
/** 「添加小问」带进弹窗的母题。 */
const presumingParentId = ref<number | null>(null)

const detailVisible = ref(false)
const detail = ref<QuestionDetail | null>(null)
const detailLoading = ref(false)
const reviewLogs = ref<ReviewLog[]>([])
const reviewLogsLoading = ref(false)

const filters = reactive<QuestionQuery>({
  keyword: '',
  type: (route.query.type as QuestionType) || '',
  status: (route.query.status as QuestionStatus) || '',
  difficulty: null,
  knowledge_point_ids: [],
  include_sub_knowledge: true,
  tag_ids: [],
  root_only: true,
  sort: 'updated_desc',
  page: 1,
  page_size: 20,
})

/**
 * 排序选项。
 *
 * 取值必须逐字等于后端白名单 validQuestionSorts
 * （backend/internal/model/question.go:208-215）。白名单外的值会被
 * Normalize 静默回落到 created_desc——所以选项写错一次，
 * 「更新于」这一列就会开始撒谎。这里由 QuestionSort 联合类型在编译期兜底，
 * label 只负责展示。
 */
const SORT_OPTIONS: { value: QuestionSort; label: string }[] = [
  { value: 'updated_desc', label: '最近更新' },
  { value: 'created_desc', label: '最新创建' },
  { value: 'created_asc', label: '最早创建' },
  { value: 'difficulty_desc', label: '难度由高到低' },
  { value: 'difficulty_asc', label: '难度由低到高' },
]

/** 「更新于」列只有在按更新时间排序时才有意义。 */
const sortedByUpdatedAt = computed(() => filters.sort === 'updated_desc')

const canEdit = computed(() => subject.can('edit_question'))
const canReview = computed(() => subject.can('review_question'))

/**
 * 学科的复核阈值。
 *
 * 阈值=1 时"还差几次复核"永远是 0，界面上就没有任何信息量，
 * 所以只在阈值 > 1 时才展示进度。
 */
const reviewThreshold = computed(() => subject.current?.review_threshold ?? 1)
const showReviewProgress = computed(() => reviewThreshold.value > 1)

onMounted(async () => {
  await Promise.all([loadOptions(), load()])
})

watch(
  () => subject.currentId,
  async () => {
    filters.page = 1
    // 这些 id 属于上一个学科，而下拉选项已经用新学科重填了：
    // 留着旧 id 会得到一个清不掉的筛选片和 0 行结果。
    filters.knowledge_point_ids = []
    filters.tag_ids = []
    filters.created_by = null
    filters.reviewer_id = null
    // AI 改动预览里是旧学科的题目 id，留着「应用」会按原 id 打到别的学科上。
    pendingAiEdits.value = null
    selection.value = []
    await Promise.all([loadOptions(), load()])
  },
)

async function loadOptions() {
  if (!subject.currentId) return
  // 先清空：换学科之后如果接口失败，不能把上一个学科的选项留在下拉里——
  // 用户会以为那是本学科的标签，用它筛选却怎么都是 0 行。
  knowledgePoints.value = []
  tags.value = []
  members.value = []
  try {
    // 成员表同时用作「创建人 / 审核人」下拉的数据源：
    // 审题的人一定是学科成员，而 userApi.list 只有管理员能调，
    // 让普通学科负责人筛选审核人会直接 403。
    const [tree, tagList, memberList] = await Promise.all([
      knowledgePointApi.tree(subject.currentId),
      tagApi.list(subject.currentId),
      subjectApi.members(subject.currentId).catch(() => [] as SubjectMember[]),
    ])
    knowledgePoints.value = flatten(tree)
    tags.value = tagList
    members.value = memberList
  } catch {
    // 列表本身照常展示，但这里必须出声：接口照样返回 200 却不带标签时，
    // 用户没法自己分清"这个学科还没有标签"和"查询挂了"。
    ElMessage.warning('标签 / 知识点加载失败，下拉框暂时是空的')
  }
}

function flatten(nodes: KnowledgePoint[], depth = 0): KnowledgePoint[] {
  const out: KnowledgePoint[] = []
  for (const node of nodes) {
    out.push({ ...node, name: `${'　'.repeat(depth)}${node.name}` })
    if (node.children?.length) out.push(...flatten(node.children, depth + 1))
  }
  return out
}

async function load() {
  if (!subject.currentId) return
  loading.value = true
  try {
    const result = await questionApi.list(subject.currentId, filters)
    rows.value = result.list ?? []
    total.value = result.total ?? 0
  } catch {
    rows.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

/** 改筛选条件后回到第一页：停在第 5 页却只筛出 3 条会很困惑。 */
function reload() {
  filters.page = 1
  void load()
}

function resetFilters() {
  filters.keyword = ''
  filters.type = ''
  filters.status = ''
  filters.difficulty = null
  filters.knowledge_point_ids = []
  filters.tag_ids = []
  filters.tag_match = 'or'
  filters.source = ''
  filters.created_by = null
  filters.reviewer_id = null
  filters.review_count = null
  // 回到"只看独立题"：重置的语义是回到默认视图，
  // 而不是把用户刚才特意打开的小问开关留在原地。
  filters.root_only = true
  filters.sort = 'updated_desc'
  reload()
}

/** 已生效的"更多筛选"条件数量，用角标告诉用户筛选确实在起作用。 */
const activeMoreFilterCount = computed(
  () =>
    (filters.source ? 1 : 0) +
    (filters.review_count !== null && filters.review_count !== undefined ? 1 : 0) +
    (filters.created_by ? 1 : 0) +
    (filters.reviewer_id ? 1 : 0),
)

/** 是否存在任何非默认筛选，决定"重置"该不该可点。 */
const activeFilterCount = computed(
  () =>
    activeMoreFilterCount.value +
    (filters.keyword ? 1 : 0) +
    (filters.type ? 1 : 0) +
    (filters.status ? 1 : 0) +
    (filters.difficulty !== null && filters.difficulty !== undefined ? 1 : 0) +
    (filters.knowledge_point_ids?.length ? 1 : 0) +
    (filters.tag_ids?.length ? 1 : 0) +
    (filters.root_only ? 0 : 1),
)

function openCreate() {
  editingId.value = null
  presumingParentId.value = null
  editVisible.value = true
}

function openEdit(row: QuestionBrief) {
  editingId.value = row.id
  presumingParentId.value = null
  editVisible.value = true
}

/** 以当前题为母题新建一道小问——材料题的入口。 */
function openAddChild(row: QuestionBrief) {
  editingId.value = null
  presumingParentId.value = row.id
  editVisible.value = true
}

async function openDetail(id: number) {
  detailVisible.value = true
  detailLoading.value = true
  detail.value = null
  reviewLogs.value = []
  try {
    detail.value = await questionApi.get(id)
    void loadReviewLogs(id)
  } finally {
    detailLoading.value = false
  }
}

/** 抽屉内跳到另一道题（母题 / 小问），避免为了一次跳转先关抽屉再重开。 */
async function jumpTo(id: number) {
  await openDetail(id)
}

/** 审核记录按需拉取：列表页不预取，只有打开详情才去拿。 */
async function loadReviewLogs(id: number) {
  reviewLogsLoading.value = true
  try {
    reviewLogs.value = await questionApi.reviewLogs(id)
  } catch {
    reviewLogs.value = []
  } finally {
    reviewLogsLoading.value = false
  }
}

async function onSaved(saved: QuestionDetail) {
  // 打开弹窗时带的母题只对这一次新建有效，保存完就清掉，
  // 否则下一次点「新建题目」会莫名其妙又挂到上一道题下面。
  presumingParentId.value = null
  await load()
  // 正在看的正是刚编辑的那道题时，详情要跟着刷新（content_revision 变了）。
  if (detail.value?.id === saved.id) {
    await openDetail(saved.id)
  }
}

// ---- AI 解题 ----

const background = useBackgroundTaskStore()

/**
 * 解题状态住在全局 store 里。页面组件只负责启动与展示，
 * 因此切到其他页面再回来时，进度和本轮结果仍在。
 */
const solveRun = computed(() => background.solveFor(subject.currentId))
const solveActive = computed(() => solveRun.value?.status === 'processing')
const solveReport = computed(() => solveRun.value?.report ?? [])

/** 停止批量解题：只在两道题之间生效，手头这道照常跑完并落库。 */
function stopSolve() {
  const subjectId = subject.currentId
  if (subjectId) background.requestStopSolve(subjectId)
}

/** 逐题调用；单题与批量共用同一条路径，行为不会两样。 */
async function runSolve(subjectId: number, rows: QuestionBrief[], overwrite: boolean) {
  for (let i = 0; i < rows.length; i++) {
    const run = background.solveFor(subjectId)
    if (!run || run.status !== 'processing') break
    if (run.stopping) {
      background.addSkippedByStop(subjectId, rows.length - i)
      break
    }

    const row = rows[i]
    background.setSolveCurrent(subjectId, row.stem_text || `#${row.id}`, row.stem_doc ?? null)
    try {
      const res = await questionApi.solve(row.id, overwrite)
      background.recordSolveResult(subjectId, {
        id: row.id,
        stem: row.stem_text || `#${row.id}`,
        doc: row.stem_doc,
        status: res.status,
        message: res.message || (res.answered_text ? `答案：${res.answered_text}` : ''),
      })
    } catch (err) {
      // 服务端返回的错误（没配模型、没权限）已经由 http 层弹过提示，
      // 这里只把它记进清单，让循环能继续走完剩下的题。
      background.recordSolveResult(subjectId, {
        id: row.id,
        stem: row.stem_text || `#${row.id}`,
        doc: row.stem_doc,
        status: 'failed',
        message: err instanceof Error ? err.message : '请求失败',
      })
    }
  }
}

/**
 * AI 解题（单题入口与批量入口都走这里）。
 *
 * 两遍走法：先按"不覆盖"跑一遍，把已有答案的题目原样保下来；
 * 服务端如实回报有哪些被跳过，再由用户决定要不要覆盖。
 */
async function solveQuestions(targets: QuestionBrief[]) {
  const subjectId = subject.currentId
  if (!subjectId || !targets.length || solveActive.value) return

  background.beginSolve(subjectId, targets.length)
  await runSolve(subjectId, targets, false)

  const run = background.solveFor(subjectId)
  if (!run) return
  // 用户已经喊停：既不再问"要不要覆盖"，也不要有第二趟。
  if (run.stopping) {
    background.finishSolve(subjectId, 'cancelled')
    await finishSolve(subjectId)
    return
  }

  const skipped = run.report.filter((r) => r.status === 'skipped')
  if (skipped.length) {
    try {
      await ElMessageBox.confirm(
        `其中 ${skipped.length} 道已经有答案，未被改动。要用 AI 给出的答案覆盖它们吗？`,
        '已有答案的题目',
        { type: 'warning', confirmButtonText: '覆盖', cancelButtonText: '保持原样' },
      )
    } catch {
      background.finishSolve(subjectId)
      await finishSolve(subjectId)
      return
    }
    const ids = new Set(skipped.map((r) => r.id))
    background.removeSolveResults(subjectId, ids)
    await runSolve(
      subjectId,
      targets.filter((t) => ids.has(t.id)),
      true,
    )
  }

  background.finishSolve(subjectId)
  await finishSolve(subjectId)
}

/** 收尾：刷新列表、同步打开着的详情，并把结果说清楚。 */
async function finishSolve(subjectId: number) {
  const run = background.solveFor(subjectId)
  if (!run) return
  const report = run.report
  const solved = report.filter((r) => r.status === 'solved').length
  const failed = report.filter((r) => r.status === 'failed')
  const kept = report.filter((r) => r.status === 'skipped').length
  const stopped = run.skippedByStop

  // 用户可能已经切到别的页面：旧组件没必要继续刷新一份看不见的列表。
  if (subject.currentId === subjectId) {
    await load()
    if (detail.value && report.some((r) => r.id === detail.value?.id)) {
      await openDetail(detail.value.id)
    }
  }

  if (!report.length) {
    if (stopped) ElMessage.info(`已停止解题，剩余 ${stopped} 道未处理`)
    return
  }
  const parts = [`${stopped ? 'AI 解题已停止' : 'AI 解题完成'}：${solved} 道已写入`]
  if (kept) parts.push(`${kept} 道保持原答案`)
  if (failed.length) parts.push(`${failed.length} 道未成功`)
  if (stopped) parts.push(`${stopped} 道未处理`)
  if (failed.length) {
    ElMessage.warning(parts.join('，') + '，详见题库页的解题清单')
  } else if (stopped) {
    ElMessage.warning(parts.join('，'))
  } else {
    ElMessage.success(parts.join('，'))
  }
}

/** 关闭本轮结果清单；关闭不影响已经写入题库的答案。 */
function clearSolveReport() {
  const subjectId = subject.currentId
  if (subjectId && !solveActive.value) background.clearSolve(subjectId)
}

/** 列表按钮的解题入口：每次只解当前这一行。 */
function solveRow(row: QuestionBrief) {
  void solveQuestions([row])
}

/** 批量入口：解选中的那一批（多选）。 */
function solveSelected() {
  void solveQuestions([...selection.value])
}

function onRowCommand(command: string, row: QuestionBrief) {
  if (command === 'add-child') openAddChild(row)
  else if (command === 'reject') void review(row, 'reject')
  else if (command === 'toggle-archive') void toggleArchive(row)
  else if (command === 'delete') void removeOne(row)
}

async function removeOne(row: QuestionBrief) {
  try {
    await ElMessageBox.confirm(`确定删除这道题吗？删除后可在回收站中恢复。`, '删除确认', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  await questionApi.remove(row.id)
  ElMessage.success('已删除')
  await load()
}

async function removeSelected() {
  if (selection.value.length === 0) return
  try {
    await ElMessageBox.confirm(
      `确定删除选中的 ${selection.value.length} 道题吗？`,
      '批量删除',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  const result = await questionApi.batchDelete(selection.value.map((r) => r.id))
  // 部分成功是常态（无权限的会被跳过），因此要如实告诉用户跳过了几道。
  if (result.skipped_count) {
    ElMessage.warning(`已删除 ${result.deleted_count} 道，跳过 ${result.skipped_count} 道（无权限）`)
  } else {
    ElMessage.success(`已删除 ${result.deleted_count} 道`)
  }
  await load()
}

async function submitForReview(row: QuestionBrief) {
  await questionApi.submit(row.id)
  ElMessage.success('已提交审核')
  await load()
}

// ---- AI 改动的确认通道 ----

const pendingAiEdits = ref<PendingQuestionEdits | null>(null)
const aiApplying = ref(false)

useQuestionAiTools({
  canEdit: () => canEdit.value,
  // 删除与归档没有独立的权限位，服务端按「是否本人建的 / 有没有编辑权」逐题判定，
  // 这里用 edit_question 当闸只是提前拦掉整块无权限的情况。
  canDelete: () => canEdit.value,
  hasPending: () => pendingAiEdits.value !== null,
  stagePending: (change) => {
    pendingAiEdits.value = change
  },
  refresh: load,
  openDetail,
})

async function applyPendingAiEdits() {
  const pending = pendingAiEdits.value
  if (!pending) return
  aiApplying.value = true
  try {
    const { applied, failed } = await applyQuestionEdits(pending.edits)
    pendingAiEdits.value = null
    // 部分成功是常态（无权限、已被别人改动），因此要如实报失败而不是只报成功数。
    if (failed.length) {
      ElMessage.warning(`已应用 ${applied} 处，${failed.length} 处失败：${failed[0]}`)
    } else {
      ElMessage.success(`已应用 ${applied} 处改动`)
    }
    await load()
  } finally {
    aiApplying.value = false
  }
}

function discardPendingAiEdits() {
  pendingAiEdits.value = null
}

async function inspectPendingEdit(questionId: number) {
  await openDetail(questionId)
}

/**
 * 归档 / 取消归档。
 *
 * 归档只对已发布的题开放，理由写在归档理由里——它是审核日志的一部分，
 * 三个月后有人问"这道题为什么不见了"，答案就在这里。
 */
async function toggleArchive(row: QuestionBrief) {
  const archiving = row.status === 'published'
  if (archiving) {
    let comment = ''
    try {
      const { value } = await ElMessageBox.prompt('请填写归档理由（会记入审核日志）', '归档题目', {
        confirmButtonText: '归档',
        cancelButtonText: '取消',
        inputPlaceholder: '例如：与新版教材口径不一致',
        inputValidator: (v) => (v && v.trim() ? true : '归档必须填写理由'),
      })
      comment = value.trim()
    } catch {
      return
    }
    await questionApi.archive(row.id, comment)
    ElMessage.success('已归档')
  } else {
    await questionApi.unarchive(row.id)
    ElMessage.success('已恢复为已发布')
  }
  await load()
  if (detail.value?.id === row.id) {
    await openDetail(row.id)
  }
}

/**
 * 批量提交审核 / 通过 / 驳回。
 *
 * 驳回必须逐条写原因，所以先问一次；服务端会逐条判权限与状态，
 * 混进来的不合条件的题被跳过，提示里要把跳过数说出来。
 */
async function batchReview(action: QuestionBatchAction) {
  const ids = selection.value.map((r) => r.id)
  if (ids.length === 0) return

  const verb = { submit: '提交审核', approve: '通过', reject: '驳回' }[action]
  let comment = ''
  if (action === 'reject') {
    try {
      const { value } = await ElMessageBox.prompt(
        `请输入驳回原因，将写入选中的 ${ids.length} 道题的审核日志`,
        `批量${verb}`,
        {
          confirmButtonText: verb,
          cancelButtonText: '取消',
          inputValidator: (v) => (v && v.trim() ? true : '驳回必须填写原因'),
        },
      )
      comment = value.trim()
    } catch {
      return
    }
  } else {
    try {
      await ElMessageBox.confirm(`确定对选中的 ${ids.length} 道题批量${verb}吗？`, `批量${verb}`, {
        type: 'warning',
        confirmButtonText: verb,
        cancelButtonText: '取消',
      })
    } catch {
      return
    }
  }

  const result = await questionApi.batchReview(ids, action, comment)
  if (result.skipped_count) {
    ElMessage.warning(`已${verb} ${result.handled_count} 道，跳过 ${result.skipped_count} 道（非草稿 / 无权限）`)
  } else {
    ElMessage.success(`已${verb} ${result.handled_count} 道`)
  }
  await load()
}

/** 批量改来源：接口早就有了，此前界面上一个调用点都没有。 */
async function batchSource() {
  const ids = selection.value.map((r) => r.id)
  if (ids.length === 0) return
  let source: string
  try {
    const { value } = await ElMessageBox.prompt(`为选中的 ${ids.length} 道题设置来源`, '批量修改来源', {
      confirmButtonText: '保存',
      cancelButtonText: '取消',
      inputPlaceholder: '例如：2024 期中试卷.pdf',
      inputValidator: (v) => (v && v.trim() ? true : '来源不能为空'),
    })
    source = value.trim()
  } catch {
    return
  }
  const result = await questionApi.batchUpdateSource(ids, source)
  if (result.skipped_ids.length) {
    ElMessage.warning(`已修改 ${result.updated_count} 道，跳过 ${result.skipped_ids.length} 道（无权限）`)
  } else {
    ElMessage.success(`已修改 ${result.updated_count} 道`)
  }
  await load()
}

async function review(row: QuestionBrief, action: 'approve' | 'reject') {
  let comment = ''
  if (action === 'reject') {
    try {
      const { value } = await ElMessageBox.prompt('请输入驳回原因', '驳回', {
        confirmButtonText: '驳回',
        cancelButtonText: '取消',
        inputPlaceholder: '例如：选项 B 与题干不符',
        inputValidator: (v) => (v && v.trim() ? true : '驳回必须填写原因'),
      })
      comment = value.trim()
    } catch {
      return
    }
  }
  await questionApi.review(row.id, action, comment)
  ElMessage.success(action === 'approve' ? '已通过' : '已驳回')
  await load()
  if (detail.value?.id === row.id) {
    detail.value = await questionApi.get(row.id)
    await loadReviewLogs(row.id)
  }
}

function typeTag(type: QuestionType): 'primary' | 'success' | 'warning' | 'info' | 'danger' {
  const map: Record<QuestionType, 'primary' | 'success' | 'warning' | 'info' | 'danger'> = {
    single_choice: 'primary',
    multiple_choice: 'success',
    fill_blank: 'warning',
    true_false: 'info',
    essay: 'danger',
  }
  return map[type] ?? 'info'
}

function statusTag(status: QuestionStatus): 'success' | 'warning' | 'info' | 'danger' {
  const map: Record<QuestionStatus, 'success' | 'warning' | 'info' | 'danger'> = {
    draft: 'info',
    pending: 'warning',
    published: 'success',
    archived: 'danger',
  }
  return map[status] ?? 'info'
}

function formatTime(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/**
 * 列表上的状态文案。
 *
 * 阈值 > 1 时把审核进度写进标签：审核者一眼就能看出"还差几次复核"，
 * 否则只能逐题点开详情——这正是学科阈值这个配置最容易白设的地方。
 */
function statusLabel(row: { status: QuestionStatus; status_label: string; review_count: number }): string {
  if (row.status === 'pending' && showReviewProgress.value) {
    return `${row.status_label} (${row.review_count}/${reviewThreshold.value})`
  }
  return row.status_label
}

const REVIEW_ACTION_LABEL: Record<string, string> = {
  approve: '通过',
  reject: '驳回',
  archive: '归档',
  restore: '取消归档',
  edit: '内容修改',
}

function reviewActionLabel(action: string): string {
  return REVIEW_ACTION_LABEL[action] ?? action
}

/** 审核动作对应的标签色，让"被打回过"在一列日志里一眼可辨。 */
function reviewActionTag(action: string): 'success' | 'danger' | 'info' | 'warning' {
  if (action === 'approve') return 'success'
  if (action === 'reject') return 'danger'
  if (action === 'archive') return 'warning'
  return 'info'
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}
</script>

<template>
  <div class="space-y-3">
    <PageHeader title="题库">
      <template #actions>
        <el-button text size="small" @click="router.push({ name: 'recycle-bin' })">
          <el-icon class="mr-1"><Delete /></el-icon>回收站
        </el-button>
        <el-button v-if="canEdit" type="primary" :disabled="!subject.currentId" @click="openCreate">
          <el-icon class="mr-1"><Plus /></el-icon>新建题目
        </el-button>
      </template>
    </PageHeader>

    <!-- 筛选栏 -->
    <el-card shadow="never">
      <div class="flex flex-wrap items-center gap-2">
        <el-input
          v-model="filters.keyword"
          placeholder="搜索题干"
          clearable
          class="w-56"
          @keyup.enter="reload"
          @clear="reload"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>

        <el-select v-model="filters.type" placeholder="题型" clearable class="w-28" @change="reload">
          <el-option label="单选题" value="single_choice" />
          <el-option label="多选题" value="multiple_choice" />
          <el-option label="填空题" value="fill_blank" />
          <el-option label="判断题" value="true_false" />
          <el-option label="解答题" value="essay" />
        </el-select>

        <el-select v-model="filters.status" placeholder="状态" clearable class="w-28" @change="reload">
          <el-option label="草稿" value="draft" />
          <el-option label="待审核" value="pending" />
          <el-option label="已发布" value="published" />
          <el-option label="已归档" value="archived" />
        </el-select>

        <el-select v-model="filters.difficulty" placeholder="难度" clearable class="w-24" @change="reload">
          <el-option v-for="n in 5" :key="n" :label="'★'.repeat(n)" :value="n" />
        </el-select>

        <el-select v-model="filters.sort" class="w-32" @change="reload">
          <el-option
            v-for="option in SORT_OPTIONS"
            :key="option.value"
            :label="option.label"
            :value="option.value"
          />
        </el-select>

        <el-select
          v-model="filters.knowledge_point_ids"
          placeholder="知识点"
          multiple
          collapse-tags
          clearable
          filterable
          class="w-44"
          @change="reload"
        >
          <el-option v-for="kp in knowledgePoints" :key="kp.id" :label="kp.name" :value="kp.id" />
        </el-select>

        <el-select
          v-model="filters.tag_ids"
          placeholder="标签"
          multiple
          collapse-tags
          clearable
          class="w-36"
          @change="reload"
        >
          <el-option v-for="tag in tags" :key="tag.id" :label="tag.name" :value="tag.id" />
        </el-select>

        <!-- 多个标签之间是"同时具备"还是"任一具备"，后端两种都支持，
             但只有一个下拉框时用户无从表达，因此显式给一个开关。 -->
        <el-select
          v-if="(filters.tag_ids?.length ?? 0) > 1"
          v-model="filters.tag_match"
          class="w-28"
          @change="reload"
        >
          <el-option label="标签任一" value="or" />
          <el-option label="标签全部" value="and" />
        </el-select>

        <!-- 小问默认不可见：材料题一道母题带 N 个小问，
             不打开这个开关，列表会被母题占满，用户根本不知道小问存在。 -->
        <el-checkbox v-model="filters.root_only" class="ml-1" @change="reload">
          只看母题
        </el-checkbox>

        <el-button text size="small" @click="moreFiltersVisible = !moreFiltersVisible">
          更多筛选
          <el-badge
            v-if="activeMoreFilterCount"
            :value="activeMoreFilterCount"
            class="ml-1 align-middle"
            type="primary"
          />
        </el-button>

        <el-button :disabled="!activeFilterCount" @click="resetFilters">重置</el-button>
      </div>

      <div
        v-if="moreFiltersVisible"
        class="mt-2 flex flex-wrap items-center gap-2 border-t border-hairline pt-2"
      >
        <el-input
          v-model="filters.source"
          placeholder="来源（文件名模糊）"
          clearable
          class="w-52"
          @keyup.enter="reload"
          @clear="reload"
        />
        <el-input-number
          v-model="filters.review_count"
          :min="0"
          :max="99"
          :controls="false"
          placeholder="审核次数"
          class="w-28"
          @change="reload"
        />

        <el-select
          v-model="filters.created_by"
          placeholder="创建人"
          clearable
          filterable
          class="w-36"
          @change="reload"
        >
          <el-option
            v-for="m in members"
            :key="m.user_id"
            :label="m.nickname || m.username"
            :value="m.user_id"
          />
        </el-select>

        <el-select
          v-model="filters.reviewer_id"
          placeholder="审核人"
          clearable
          filterable
          class="w-36"
          @change="reload"
        >
          <el-option
            v-for="m in members"
            :key="m.user_id"
            :label="m.nickname || m.username"
            :value="m.user_id"
          />
        </el-select>
      </div>

      <div v-if="selection.length" class="mt-2 flex flex-wrap items-center gap-2 border-t border-hairline pt-2">
        <span class="text-sm text-ink-muted">已选 {{ selection.length }} 项</span>
        <template v-if="canEdit">
          <el-button size="small" type="primary" plain :loading="solveActive" @click="solveSelected">
            AI 解题（{{ selection.length }} 道）
          </el-button>
          <el-button size="small" @click="batchReview('submit')">批量提交审核</el-button>
          <el-button size="small" @click="batchSource">修改来源</el-button>
        </template>
        <template v-if="canReview">
          <el-button size="small" type="success" plain @click="batchReview('approve')">批量通过</el-button>
          <el-button size="small" type="danger" plain @click="batchReview('reject')">批量驳回</el-button>
        </template>
        <el-button v-if="canEdit" size="small" type="danger" plain @click="removeSelected">
          批量删除
        </el-button>
      </div>

      <QuestionAiChangesBar
        v-if="pendingAiEdits"
        class="mt-2"
        :summary="pendingAiEdits.summary"
        :lines="pendingAiEdits.lines"
        :applying="aiApplying"
        @apply="applyPendingAiEdits"
        @discard="discardPendingAiEdits"
        @inspect="inspectPendingEdit"
      />
    </el-card>

    <!-- AI 解题：进度与结果 -->
    <el-card v-if="solveActive || solveReport.length" shadow="never" class="mb-3">
      <template #header>
        <div class="flex items-center gap-2">
          <span class="text-sm font-medium">AI 解题</span>
          <el-tag v-if="solveActive" size="small" type="primary" effect="plain">
            {{ solveRun?.done ?? 0 }}/{{ solveRun?.total ?? 0 }}
          </el-tag>
          <el-button
            v-if="!solveActive"
            text
            size="small"
            class="ml-auto"
            @click="clearSolveReport"
          >
            关闭
          </el-button>
        </div>
      </template>

      <div v-if="solveActive" class="text-xs text-ink-muted">
        <div class="flex items-center gap-2">
          <el-progress
            class="min-w-0 flex-1"
            :percentage="solveRun?.total ? Math.round((solveRun.done / solveRun.total) * 100) : 0"
            :stroke-width="10"
          />
          <el-button size="small" :disabled="solveRun?.stopping" @click="stopSolve">
            {{ solveRun?.stopping ? '正在收尾…' : '停止' }}
          </el-button>
        </div>
        <div class="mt-1">
          正在解题：
          <RichContent
            v-if="solveRun?.currentDoc"
            :doc="solveRun.currentDoc"
            compact
            class="stem-clamp-1 inline"
          />
          <span v-else>{{ solveRun?.current || '准备中' }}</span>
          。每道题各自落库，可以中途离开。
          <span v-if="solveRun?.stopping">已请求停止，跑完手头这道就收工。</span>
        </div>
      </div>

      <div v-else class="text-xs">
        <div class="mb-1 text-ink-muted">
          已写入 {{ solveReport.filter((r) => r.status === 'solved').length }} 道；
          保持原答案 {{ solveReport.filter((r) => r.status === 'skipped').length }} 道；
          未成功 {{ solveReport.filter((r) => r.status === 'failed').length }} 道<template
            v-if="solveRun?.skippedByStop"
            >；未处理 {{ solveRun?.skippedByStop ?? 0 }} 道</template
          >。
        </div>
        <div
          v-for="item in solveReport.filter((r) => r.status !== 'solved')"
          :key="item.id"
          class="mt-1 flex gap-2"
        >
          <el-tag size="small" :type="item.status === 'skipped' ? 'info' : 'danger'">
            {{ item.status === 'skipped' ? '保持原答案' : '未成功' }}
          </el-tag>
          <RichContent
            v-if="item.doc"
            :doc="item.doc"
            compact
            class="stem-clamp-1 text-ink-muted"
          />
          <span v-else class="text-ink-muted">{{ item.stem }}</span>
          <span class="text-ink-subtle">{{ item.message }}</span>
        </div>
      </div>
    </el-card>

    <!-- 列表 -->
    <el-card shadow="never">
      <TableSkeleton v-if="loading" :rows="6" :cols="6" />
      <el-table v-else
        :data="rows"
        row-key="id"
        @selection-change="(val: QuestionBrief[]) => (selection = val)"
      >
        <el-table-column type="selection" width="42" />
        <el-table-column label="题干" min-width="360">
          <template #default="{ row }">
            <div class="cursor-pointer" @click="openDetail(row.id)">
              <!-- 渲染富文本而不是 stem_text：后者是给检索用的纯文本投影，
                   公式会被摊平成 LaTeX 源码（\boldsymbol{a} 直接显示出来）。 -->
              <RichContent
                v-if="row.stem_doc"
                :doc="row.stem_doc"
                compact
                class="stem-clamp text-sm"
              />
              <div v-else class="stem-clamp text-sm text-ink-subtle">
                {{ row.stem_text || '（无题干）' }}
              </div>
              <div class="mt-1 flex flex-wrap items-center gap-1">
                <el-tag v-if="row.parent_id" size="small" type="warning" effect="plain">子题</el-tag>
                <el-tag v-if="row.child_count" size="small" type="primary" effect="plain">母题</el-tag>
                <el-tag v-for="kp in row.knowledge_points" :key="kp.id" size="small" effect="plain">
                  {{ kp.name }}
                </el-tag>
                <el-tag
                  v-for="tag in row.tags"
                  :key="tag.id"
                  size="small"
                  :color="tag.color || undefined"
                  effect="plain"
                >
                  {{ tag.name }}
                </el-tag>
                <span v-if="row.child_count" class="text-xs text-ink-subtle">
                  含 {{ row.child_count }} 个小问
                </span>
              </div>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="题型" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="typeTag(row.question_type)">
              {{ row.question_type_label }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column label="难度" width="110">
          <template #default="{ row }">
            <DifficultyStars :level="row.difficulty" />
          </template>
        </el-table-column>

        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="statusTag(row.status)">{{ statusLabel(row) }}</el-tag>
          </template>
        </el-table-column>

        <el-table-column label="更新于" width="140">
          <template #header>
            <span>更新于</span>
            <el-tooltip
              v-if="!sortedByUpdatedAt"
              content="当前列表没有按更新时间排序，这一列只显示每道题各自的最后更新时间。"
              placement="top"
            >
              <el-icon class="ml-1 align-middle text-ink-subtle"><InfoFilled /></el-icon>
            </el-tooltip>
          </template>
          <template #default="{ row }">
            <span class="text-xs text-ink-muted">{{ formatTime(row.updated_at) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="270" fixed="right">
          <template #default="{ row }">
            <div class="flex items-center gap-1 whitespace-nowrap">
              <el-button text size="small" @click="openDetail(row.id)">查看</el-button>

              <template v-if="canEdit">
                <!-- 不按状态收窄：服务端对已发布/待审的题同样允许编辑，
                     界面此前把按钮锁在 draft 上，是在替服务端撒一个它没撒的谎。
                     改动的后果（试卷提示题目已改动 + 审核记录）在弹窗里说明。 -->
                <el-button text size="small" @click="openEdit(row)">编辑</el-button>
                <!-- 「仅录题」的另一半：为这道题补写答案与解析。
                     已有答案的题不会被无声覆盖，服务端会回报"跳过"再问一次。 -->
                <el-button
                  text
                  size="small"
                  type="primary"
                  :disabled="solveActive"
                  @click="solveRow(row)"
                >
                  解题
                </el-button>
              </template>

              <el-button
                v-if="canEdit && row.status === 'draft'"
                text
                size="small"
                type="primary"
                @click="submitForReview(row)"
              >
                提交审核
              </el-button>
              <el-button
                v-else-if="canReview && row.status === 'pending'"
                text
                size="small"
                type="success"
                @click="review(row, 'approve')"
              >
                通过
              </el-button>

              <el-dropdown
                v-if="canEdit || canReview"
                @command="(c: string) => onRowCommand(c, row)"
              >
                <el-button text size="small" aria-label="更多操作">
                  <el-icon><MoreFilled /></el-icon>
                </el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item v-if="canEdit" command="add-child">添加小问</el-dropdown-item>
                    <el-dropdown-item
                      v-if="canReview && row.status === 'pending'"
                      command="reject"
                    >
                      <span class="text-danger-text">驳回</span>
                    </el-dropdown-item>
                    <el-dropdown-item
                      v-if="canReview && (row.status === 'published' || row.status === 'archived')"
                      command="toggle-archive"
                    >
                      {{ row.status === 'archived' ? '取消归档' : '归档' }}
                    </el-dropdown-item>
                    <el-dropdown-item v-if="canEdit" command="delete" divided>
                      <span class="text-danger-text">删除</span>
                    </el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </div>
          </template>
        </el-table-column>

        <template #empty>
          <EmptyState
            :description="subject.currentId ? '这个筛选条件下还没有题目' : '请先选择学科'"
            :hint="
              subject.currentId
                ? '可以从「智能录题」批量导入，或直接新建一道。母题与子题按「只看母题」开关区分。'
                : '学科是题库的一级边界，请先在右上角选择或创建一个学科。'
            "
            :action-label="subject.currentId && canEdit ? '新建题目' : undefined"
            @action="openCreate"
          />
        </template>
      </el-table>

      <div class="mt-3 flex justify-end">
        <el-pagination
          v-model:current-page="filters.page"
          v-model:page-size="filters.page_size"
          :total="total"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="load"
          @size-change="reload"
        />
      </div>
    </el-card>

    <!-- 详情抽屉 -->
    <el-drawer v-model="detailVisible" title="题目详情" size="640px">
      <div v-loading="detailLoading">
        <template v-if="detail">
          <div class="mb-3 flex flex-wrap items-center gap-2">
            <el-tag size="small" :type="typeTag(detail.question_type)">
              {{ detail.question_type_label }}
            </el-tag>
            <el-tag size="small" :type="statusTag(detail.status)">{{ statusLabel(detail) }}</el-tag>
            <DifficultyStars :level="detail.difficulty" />
            <el-tag v-if="detail.visibility === 'private'" size="small" type="warning">私有</el-tag>
            <span class="num ml-auto text-xs text-ink-subtle">v{{ detail.content_version }} · r{{ detail.content_revision }}</span>
          </div>

          <!-- 母子题结构：母题可点回详情，小问就地列出并可点开。
               服务端早就把 children 一起返回了，此前界面上没有任何地方渲染它。 -->
          <div v-if="detail.parent_id" class="mb-2 rounded border border-warning-border bg-warning-soft p-2 text-sm">
            <span class="mr-2 text-xs text-warning-text">子题</span>
            母题：
            <el-link type="primary" class="num" @click="jumpTo(detail.parent_id)">#{{ detail.parent_id }}</el-link>
          </div>

          <div v-if="detail.children?.length" class="mb-3 rounded border border-accent-border bg-accent-soft p-2">
            <div class="mb-1 text-xs text-accent-text">母题 · 含 {{ detail.children.length }} 个小问</div>
            <div class="space-y-1">
              <div
                v-for="child in detail.children"
                :key="child.id"
                class="flex cursor-pointer items-center gap-2 text-sm hover:underline"
                @click="jumpTo(child.id)"
              >
                <el-tag size="small" :type="statusTag(child.status)">{{ child.status_label }}</el-tag>
                <RichContent
                  :doc="child.stem_doc"
                  compact
                  class="stem-clamp-1 flex-1 text-sm"
                />
              </div>
            </div>
            <el-button
              v-if="canEdit"
              size="small"
              text
              type="primary"
              class="mt-1"
              @click="openAddChild(detail)"
            >
              + 添加小问
            </el-button>
          </div>

          <div class="mb-1 text-xs text-ink-muted">题干</div>
          <div class="mb-4 rounded border border-hairline p-3">
            <RichContent :doc="detail.stem" />
          </div>

          <template v-if="detail.options?.length">
            <div class="mb-1 text-xs text-ink-muted">选项</div>
            <div class="mb-4 space-y-1">
              <div
                v-for="option in detail.options"
                :key="option.id"
                class="flex gap-2 rounded border border-hairline p-2"
              >
                <span class="font-medium">{{ option.label }}.</span>
                <RichContent :doc="option.content" class="flex-1" />
              </div>
            </div>
          </template>

          <div class="mb-1 text-xs text-ink-muted">答案</div>
          <div class="mb-4 rounded bg-success-soft p-3 text-sm">
            <template v-if="detail.answer.kind === 'single' || detail.answer.kind === 'multiple'">
              {{
                (detail.answer.correct ?? [])
                  .map((id) => detail?.options?.find((o) => o.id === id)?.label ?? id)
                  .join('、')
              }}
            </template>
            <template v-else-if="detail.answer.kind === 'boolean'">
              {{ detail.answer.bool_answer ? '正确' : '错误' }}
            </template>
            <template v-else-if="detail.answer.kind === 'blanks'">
              <div v-for="(blank, index) in detail.answer.blanks ?? []" :key="blank.id">
                第 {{ index + 1 }} 空：
                <span v-for="(group, gi) in blank.accepts ?? []" :key="gi">
                  <RichContent v-for="(item, ii) in group" :key="ii" :doc="item" class="inline" />
                  <span v-if="gi < (blank.accepts?.length ?? 0) - 1" class="text-ink-subtle"> 或 </span>
                </span>
              </div>
            </template>
            <template v-else-if="detail.answer.kind === 'reference'">
              <RichContent :doc="detail.answer.reference" />
            </template>
            <template v-else-if="detail.answer.kind === 'pending'">
              <span class="text-ink-muted">待补充答案</span>
              <span class="ml-1 text-xs text-ink-subtle">
                这道题由智能录题的「仅录题」模式录入，答案与解析由老师填写。
              </span>
            </template>
            <template v-else>
              <span class="text-ink-muted">未能识别答案结构</span>
            </template>
          </div>

          <template v-if="detail.analysis">
            <div class="mb-1 text-xs text-ink-muted">解析</div>
            <div class="mb-4 rounded border border-hairline p-3">
              <RichContent :doc="detail.analysis" />
            </div>
          </template>

          <!-- 审核记录：端点与审核日志表早就存在，界面上却一个入口都没有。
               审核透明度为零的表现就是"这道题被谁驳回过、为什么"无从查证。 -->
          <div class="mb-1 text-xs text-ink-muted">审核记录</div>
          <div v-loading="reviewLogsLoading" class="mb-4 rounded border border-hairline p-3">
            <div v-if="!reviewLogs.length && !reviewLogsLoading" class="text-sm text-ink-subtle">
              暂无审核记录
            </div>
            <div v-else class="space-y-2">
              <div
                v-for="log in reviewLogs"
                :key="log.id"
                class="flex flex-wrap items-baseline gap-2 text-sm"
              >
                <el-tag size="small" :type="reviewActionTag(log.action)">
                  {{ reviewActionLabel(log.action) }}
                </el-tag>
                <span class="text-ink-muted">{{ log.reviewer || '未知' }}</span>
                <span class="text-xs text-ink-subtle">{{ formatTime(log.created_at) }}</span>
                <span v-if="log.comment" class="w-full text-xs text-ink-regular">「{{ log.comment }}」</span>
              </div>
            </div>
          </div>

          <div class="flex flex-wrap gap-2 border-t border-hairline pt-3">
            <el-button v-if="detail.permissions.can_edit" type="primary" @click="openEdit(detail)">
              编辑
            </el-button>
            <el-button
              v-if="detail.permissions.can_edit"
              @click="openAddChild(detail)"
            >
              添加小问
            </el-button>
            <el-button
              v-if="canEdit && detail.status === 'draft'"
              @click="submitForReview(detail)"
            >
              提交审核
            </el-button>
            <el-button
              v-if="canReview && detail.status === 'pending'"
              type="success"
              @click="review(detail, 'approve')"
            >
              通过
            </el-button>
            <el-button
              v-if="canReview && detail.status === 'pending'"
              type="danger"
              plain
              @click="review(detail, 'reject')"
            >
              驳回
            </el-button>
            <el-button
              v-if="
                canReview &&
                (detail.status === 'published' || detail.status === 'archived')
              "
              type="warning"
              plain
              @click="toggleArchive(detail)"
            >
              {{ detail.status === 'archived' ? '取消归档' : '归档' }}
            </el-button>
          </div>
        </template>
      </div>
    </el-drawer>

    <QuestionEditDialog
      v-if="subject.currentId"
      v-model="editVisible"
      :subject-id="subject.currentId"
      :question-id="editingId"
      :presume-parent-id="presumingParentId"
      @saved="onSaved"
    />
  </div>
</template>
