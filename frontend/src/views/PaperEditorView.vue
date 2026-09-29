<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, reactive, ref } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ApiError } from '@/api/http'
import { questionApi } from '@/api'
import {
  paperApi,
  type NodeInput,
  type PaperDetail,
  type PaperNode,
  type PaperVersion,
  type QuestionRef,
  type ModuleContent,
} from '@/api/paper'
import type { QuestionBrief } from '@/api/types'
import RichContent from '@/components/RichContent.vue'
import RichTextEditor from '@/components/editor/RichTextEditor.vue'
import { emptyDoc } from '@/lib/richdoc'

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

// ---- 块编辑 ----
const blockVisible = ref(false)
const blockForm = reactive({
  id: '' as string,
  nodeType: 'rich_text' as 'rich_text' | 'heading',
  level: 1,
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

/** 题号：按顶层题目块的出现顺序。 */
const questionNumbers = computed(() => {
  const map = new Map<string, number>()
  let n = 0
  for (const node of topNodes.value) {
    if (node.node_type === 'question') {
      n++
      map.set(node.id, n)
    }
  }
  return map
})

const questionCount = computed(() => questionNumbers.value.size)

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
    content: { height: 120 },
    props: {},
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

function openBlockEditor(node: PaperNode | null, type: 'rich_text' | 'heading') {
  if (node) {
    blockForm.id = node.id
    blockForm.nodeType = node.node_type as 'rich_text' | 'heading'
    blockForm.content = node.content ?? emptyDoc()
    const props = (node.props ?? {}) as { level?: number }
    blockForm.level = props.level ?? 1
  } else {
    blockForm.id = ''
    blockForm.nodeType = type
    blockForm.content = type === 'heading' ? { type: 'doc', content: [{ type: 'heading', attrs: { level: 1 } }] } : emptyDoc()
    blockForm.level = 1
  }
  blockVisible.value = true
}

function submitBlock() {
  const props = blockForm.nodeType === 'heading' ? { level: blockForm.level } : {}
  if (blockForm.id) {
    const node = nodes.value.find((n) => n.id === blockForm.id)
    if (node) {
      node.content = blockForm.content
      node.props = props
      dirty.value = true
    }
  } else {
    appendNode({
      id: newID(),
      parent_id: null,
      node_kind: 'block',
      node_type: blockForm.nodeType,
      content: blockForm.content,
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
  return nodes.value
    .filter((n) => n.node_kind !== 'reference')
    .map((n) => ({
      id: n.id,
      parent_id: n.parent_id,
      node_kind: n.node_kind,
      node_type: n.node_type,
      content: n.content,
      props: n.props,
      schema_version: n.schema_version || 1,
      source_question_node_id: null,
      anchor_before_node_id: null,
    }))
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

function blockLabel(node: PaperNode): string {
  switch (node.node_type) {
    case 'rich_text':
      return '说明文字'
    case 'heading':
      return '标题'
    case 'page_break':
      return '分页符'
    case 'answer_space':
      return '答题区'
    case 'question_details':
      return '解析模块'
    default:
      return node.node_type
  }
}

function answerText(questionID: number): string {
  const q = questions.value[questionID]
  if (!q) return ''
  const answer = q.answer
  switch (answer.kind) {
    case 'single':
    case 'multiple':
      return (answer.correct ?? [])
        .map((id) => q.options.find((o) => o.id === id)?.label ?? id)
        .join('、')
    case 'boolean':
      return answer.bool_answer ? '正确' : '错误'
    case 'blanks':
      return (answer.blanks ?? [])
        .map((b) => (b.accepts ?? []).map((group) => group.map(() => '').join('')).join('或'))
        .join('；')
    case 'reference':
      return '见参考答案'
    default:
      return ''
  }
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
            <template v-if="paper?.scoring_enabled"> · 总分 {{ paper.total_score }}</template>
          </div>
        </div>

        <div class="ml-auto flex items-center gap-2">
          <el-button v-if="canEdit" :disabled="!dirty" :loading="saving" type="primary" @click="save()">
            保存
          </el-button>
          <el-button @click="openSettings">设置</el-button>
          <el-button @click="openVersions">版本</el-button>
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
                    <span class="text-sm font-medium">
                      第 {{ questionNumbers.get(node.id) }} 题
                    </span>
                    <el-tag v-if="questions[(node.content as QuestionRef)?.question_id]?.stale" size="small" type="warning">
                      内容已更新
                    </el-tag>
                    <span class="text-xs text-gray-400">
                      {{ questions[(node.content as QuestionRef)?.question_id]?.question_type_label }}
                      ·
                      {{ '★'.repeat(questions[(node.content as QuestionRef)?.question_id]?.difficulty ?? 0) }}
                    </span>
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
                  <div class="rounded border border-dashed border-gray-300 py-6 text-center text-xs text-gray-400">
                    答题区（{{ (node.content as { height?: number })?.height ?? 120 }}px）
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
                  v-if="node.node_type === 'rich_text' || node.node_type === 'heading'"
                  text
                  size="small"
                  @click="openBlockEditor(node, node.node_type)"
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
      :title="blockForm.nodeType === 'heading' ? '编辑标题' : '编辑说明文字'"
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
        <el-form-item label="内容">
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
            <el-checkbox v-model="settingsForm.numbering_enabled">自动编号题号</el-checkbox>
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
        </div>
        <div v-if="version.note" class="mt-1 text-xs text-gray-500">{{ version.note }}</div>
        <div class="mt-1 text-xs text-gray-400">{{ new Date(version.created_at).toLocaleString('zh-CN') }}</div>
      </div>
    </el-drawer>

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
