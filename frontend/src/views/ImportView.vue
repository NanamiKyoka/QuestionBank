<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { importApi, uploadApi, type CommitResult } from '@/api/ai'
import { knowledgePointApi, tagApi } from '@/api'
import {
  groupImportDrafts,
  importTaskApi,
  type ImportDraft,
  type ImportDraftNode,
  type ImportExtractResponse,
  type ImportMode,
  type ImportOutline,
  type ImportParseMethod,
} from '@/api/importTask'
import type { KnowledgePoint, QuestionStatus, Tag } from '@/api/types'
import ImportDraftCard from '@/components/ImportDraftCard.vue'
import ImportDraftEditDialog from '@/components/ImportDraftEditDialog.vue'
import ImportFileDropzone from '@/components/ImportFileDropzone.vue'
import ImportParseOptions from '@/components/ImportParseOptions.vue'
import { useSubjectStore } from '@/stores/subject'

/**
 * 智能录题。
 *
 * 流程刻意分成两步：**抽取**不写任何数据，只产出草稿；
 * 用户在草稿上逐题核对、修改、勾选后再**入库**。
 * 让模型直接写库看起来更"自动"，但录题是低成本高风险的环节——
 * 错一道题的代价远高于多点一次按钮。
 *
 * 三个来源（文件 / 粘贴文本 / 图片截图）都汇到同一份草稿上，
 * 因为下游的审核、修改、入库对它们是完全一样的。
 */
const router = useRouter()
const route = useRoute()
const subject = useSubjectStore()

const tab = ref<'file' | 'text' | 'image'>('file')
const loading = ref(false)
const committing = ref(false)

const form = reactive({
  content: '',
  mode: 'extract' as ImportMode,
  solve_for_content: '',
  images: [] as string[],
  method: 'ai' as ImportParseMethod,
  /** 待上传的文档。文件与文本是二选一：文档优先，因为格式信息只有文件里有。 */
  file: null as File | null,
})

const uploading = ref(false)

const drafts = ref<ImportDraft[]>([])
const selected = ref<Set<number>>(new Set())
/** 来源文件筛选；空串 = 全部。一次响应可能覆盖多个文件（.zip）。 */
const fileFilter = ref('')
/** 正在编辑的草稿下标。 */
const editingIndex = ref<number | null>(null)
/**
 * 本次抽取的如实记录。
 *
 * `methodReported` 区分"服务端说的"和"我们请求的"：后端还没上新字段时，
 * 界面上不能把"我请求了规则解析"显示成"这次跑的是规则解析"。
 */
const runMeta = ref<{
  method: string
  methodReported: boolean
  model: string
  elapsed_ms: number
  task_id: number
  source_files: string[]
  /** 版面大纲：标题、说明文字与题目引用的顺序。整卷导入靠它还原卷面。 */
  outline: ImportOutline[]
  total: number
} | null>(null)
const commitResult = ref<CommitResult | null>(null)

/**
 * 整卷导入：把这份卷子还原成可编辑的试卷。
 *
 * 默认关闭。只想把 200 道题存进题库的用户，凭空多出一份试卷只是垃圾数据。
 */
const wholePaperEnabled = ref(false)
const wholePaperTitle = ref('')

const knowledgePoints = ref<KnowledgePoint[]>([])
const tags = ref<Tag[]>([])
const defaults = reactive({
  default_tag_ids: [] as number[],
  default_knowledge_point_ids: [] as number[],
  status: 'draft' as QuestionStatus,
})

const canEdit = computed(() => subject.can('edit_question'))
const selectedDrafts = computed(() => drafts.value.filter((_, i) => selected.value.has(i)))
const warningCount = computed(() => drafts.value.filter((d) => d.warnings?.length).length)
/** 按母/小问组装好的树。审核列表、来源筛选都在这上面做。 */
const draftTree = computed(() => groupImportDrafts(drafts.value))

/** 本次抽取覆盖的来源文件。草稿上的 source_file 才是权威，响应级字段只是提示。 */
const sourceFiles = computed(() => {
  const found = new Set(drafts.value.map((d) => d.source_file).filter((f): f is string => !!f))
  for (const f of runMeta.value?.source_files ?? []) found.add(f)
  return [...found]
})

/** 当前来源文件下每道题的分布，让"这个文件到底出了几道题"一眼可见。 */
const fileCounts = computed(() => {
  const counts = new Map<string, number>()
  for (const draft of drafts.value) {
    if (!draft.source_file) continue
    counts.set(draft.source_file, (counts.get(draft.source_file) ?? 0) + 1)
  }
  return counts
})

/** 本次实际跑的解析路径：服务端说了算，没回就用我们请求的那个顶上。 */
const ranTemplate = computed(() => (runMeta.value?.method ?? form.method) === 'template')

const editingDraft = computed<ImportDraft | null>(() =>
  editingIndex.value === null ? null : (drafts.value[editingIndex.value] ?? null),
)

const editingVisible = computed({
  get: () => editingIndex.value !== null,
  set: (open: boolean) => {
    if (!open) editingIndex.value = null
  },
})

/**
 * 可见的树：按来源文件裁剪。
 *
 * 母题只要有小问命中就保留，层级才不会在筛选时断掉——孤零零一个小问
 * 挂在没有材料的地方，用户根本没法判断它在说什么。
 */
const visibleTree = computed<ImportDraftNode[]>(() => filterTree(draftTree.value, fileFilter.value))

function filterTree(nodes: ImportDraftNode[], file: string): ImportDraftNode[] {
  if (!file) return nodes
  const out: ImportDraftNode[] = []
  for (const node of nodes) {
    const children = filterTree(node.children, file)
    if (node.draft.source_file === file || children.length) out.push({ ...node, children })
  }
  return out
}

/** 母题因小问命中而被带出来时，要标出来——否则用户以为整组都来自当前文件。 */
function shownForChild(node: ImportDraftNode): boolean {
  if (!fileFilter.value || node.draft.source_file === fileFilter.value) return false
  return node.children.length > 0
}

onMounted(async () => {
  await loadOptions()
  loaded.value = true
  await loadFromRoute()
})

/**
 * 从导入历史跳过来时带着 ?task=<id>：把那个任务抽出的草稿拉进审核界面。
 *
 * 队列任务的 HTTP 响应早就返回了，草稿存在任务记录里；
 * 没有这一步，排队抽出来的题就没人能审核，队列等于白做。
 */
async function loadFromRoute() {
  const raw = route.query.task
  const id = Number(Array.isArray(raw) ? raw[0] : raw)
  if (!Number.isFinite(id) || id <= 0 || !subject.currentId) return
  loading.value = true
  try {
    acceptResult(await importTaskApi.drafts(subject.currentId, id))
    // 用 replace 清掉地址栏里的 task，否则刷新会再拉一次。
    void router.replace({ name: 'import' })
  } finally {
    loading.value = false
  }
}

const loaded = ref(false)

watch(
  () => subject.currentId,
  async () => {
    if (!loaded.value) return
    drafts.value = []
    selected.value = new Set()
    commitResult.value = null
    runMeta.value = null
    fileFilter.value = ''
    await loadOptions()
  },
)

async function loadOptions() {
  if (!subject.currentId) return
  try {
    const [tree_, tagList] = await Promise.all([
      knowledgePointApi.tree(subject.currentId),
      tagApi.list(subject.currentId),
    ])
    knowledgePoints.value = flatten(tree_)
    tags.value = tagList
  } catch {
    // 选项加载失败不阻断录题
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

// ---- 图片来源 ----

async function onPickImages(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  if (!files.length) return

  uploading.value = true
  try {
    for (const file of files) {
      const saved = await uploadApi.image(file)
      form.images.push(saved.url)
    }
    ElMessage.success(`已上传 ${files.length} 张图片`)
  } finally {
    uploading.value = false
  }
}

/**
 * 拖进来 / 粘进来的是图片。
 *
 * 图片不能走 /imports/upload（那个端点只收文档），所以这里把它们
 * 接到已有的「上传图片 → 图片地址」链路上，用户不必知道自己换了一条路。
 */
async function onImageFile(file: File) {
  if (form.method === 'template') {
    ElMessage.warning('规则解析不读图片，请先切换到「模型识别」')
    return
  }
  tab.value = 'image'
  uploading.value = true
  try {
    const saved = await uploadApi.image(file)
    form.images.push(saved.url)
    ElMessage.success('已添加 1 张图片')
  } finally {
    uploading.value = false
  }
}

function removeImage(index: number) {
  form.images.splice(index, 1)
}

// ---- 解析方式 ----

function onMethodChange(value: ImportParseMethod) {
  form.method = value
  // 规则解析既不读图片也不解题：把不可行的组合直接改回可行的那一个，
  // 而不是留着让用户提交后才吃一个 400。
  if (value === 'template') {
    form.mode = 'extract'
    if (form.images.length && tab.value === 'image') tab.value = 'file'
  }
}

/**
 * 规则解析的格式预检。
 *
 * 解析器只认【题目】这类标签，文本里一个都没有时它会**静默返回空列表**——
 * 界面上就是"识别出 0 道题"，用户完全不知道该去改文档还是在怀疑系统坏了。
 * 这条检查能提前把原因说清楚。只能对能读到原文的来源做（粘贴文本 / .md / .txt），
 * .docx / .zip 读不了，跳过而不是假装检查过。
 */
const STRUCTURED_TAG_PATTERN = /[【\[]\s*(题目|题干|question|title)\s*[】\]]/i
const plainTextPreview = ref<string | null>(null)
const structuredTagWarning = computed(() => {
  if (form.method !== 'template') return null
  const text = tab.value === 'text' ? form.content : (plainTextPreview.value ?? '')
  if (!text.trim()) return null
  if (STRUCTURED_TAG_PATTERN.test(text)) return null
  return '这段内容里没有【题目】标签，规则解析很可能一道题也识别不出来。'
})

// 读一次 .md / .txt 的原文用于上面的预检；读不到就算了，不是必需信息。
watch(
  () => form.file,
  async (file) => {
    plainTextPreview.value = null
    if (!file || !/\.(md|txt|markdown)$/i.test(file.name)) return
    try {
      plainTextPreview.value = await file.text()
    } catch {
      plainTextPreview.value = null
    }
  },
)

watch(tab, () => {
  if (tab.value !== 'file') return
  // 切到文件来源时，预检文本只能来自文件本身。
  void (form.file ? readPlainText(form.file) : Promise.resolve())
})

function readPlainText(file: File): Promise<void> {
  return file
    .text()
    .then((text) => {
      plainTextPreview.value = text
    })
    .catch(() => {
      plainTextPreview.value = null
    })
}

// ---- 抽取 ----

function canSubmit(): boolean {
  if (tab.value === 'file') return form.file !== null
  if (tab.value === 'image') return form.images.length > 0
  return form.content.trim().length > 0
}

/** 缺东西时说清是哪一样缺了，而不是把按钮灰掉让人猜。 */
const submitHint = computed(() => {
  if (canSubmit()) return ''
  if (tab.value === 'file') return '请先选择要导入的文件'
  if (tab.value === 'image') return '请先添加图片'
  return '请粘贴题目内容'
})

async function extract() {
  if (!subject.currentId) {
    ElMessage.warning('请先选择学科')
    return
  }
  if (form.mode === 'solve' && !form.solve_for_content.trim()) {
    ElMessage.warning('“只求解”模式需要提供原题内容')
    return
  }
  if (!canSubmit()) {
    ElMessage.warning(submitHint.value)
    return
  }

  loading.value = true
  commitResult.value = null
  try {
    const result =
      tab.value === 'file' && form.file
        ? await importTaskApi.upload(subject.currentId, {
            file: form.file,
            method: form.method,
            mode: form.mode,
          })
        : await importTaskApi.extract(subject.currentId, {
            content: tab.value === 'text' ? form.content : '',
            images: tab.value === 'image' ? form.images : undefined,
            method: form.method,
            mode: form.mode,
            solve_for_content: form.mode === 'solve' ? form.solve_for_content : undefined,
          })
    acceptResult(result)
  } finally {
    loading.value = false
  }
}

function acceptResult(result: ImportExtractResponse) {
  drafts.value = result.drafts ?? []
  // 默认全选：绝大多数情况下用户是想全部入库的，逐题勾选是负担。
  selected.value = new Set(drafts.value.map((_, i) => i))
  fileFilter.value = ''
  editingIndex.value = null

  const reported = result.method?.trim() ?? ''
  runMeta.value = {
    method: reported || form.method,
    methodReported: !!reported,
    model: result.model,
    elapsed_ms: result.elapsed_ms,
    task_id: result.task_id,
    source_files: result.source_files ?? [],
    outline: result.outline ?? [],
    total: result.total,
  }

  if (drafts.value.length === 0) {
    ElMessage.warning(
      form.method === 'template'
        ? '没有解析出任何题目，请确认内容使用了【题目】标签'
        : '没有识别出任何题目，请检查内容是否完整',
    )
    return
  }
  ElMessage.success(`识别出 ${result.total} 道题`)
}

// ---- 草稿编辑 ----

function toggleSelect(index: number) {
  const next = new Set(selected.value)
  if (next.has(index)) next.delete(index)
  else next.add(index)
  selected.value = next
}

function isChecked(index: number): boolean {
  return selected.value.has(index)
}

function selectAll() {
  selected.value = new Set(drafts.value.map((_, i) => i))
}

function selectNone() {
  selected.value = new Set()
}

function selectWarningsOnly() {
  selected.value = new Set(
    drafts.value.map((d, i) => (d.warnings?.length ? i : -1)).filter((i) => i >= 0),
  )
}

/** 应用草稿修改：整体替换那一项，其余原样不动。 */
function applyDraftEdit(updated: ImportDraft) {
  if (editingIndex.value === null) return
  drafts.value.splice(editingIndex.value, 1, updated)
}

/**
 * 移除一道草稿。
 *
 * 删母题要先问一句：它的小问会变成指向空引用，而后端入库时会把这些小问
 * 一并判失败（"母题未能入库"）。所以明确告诉用户会发生什么，
 * 并把**直接**小问降级为独立题——用户删掉的是那一道题，不一定是那二十道。
 *
 * 只处理直接子级：更深一层的小问指向的是自己的母题，那道母题还在，
 * 关系不受影响，跟着一起平移即可。
 */
async function removeDraft(index: number) {
  const draft = drafts.value[index]
  if (!draft) return
  const tempId = (draft.temp_id ?? '').trim()
  const childIndexes = tempId
    ? drafts.value
        .map((d, i) => ({ d, i }))
        .filter(({ d }) => (d.parent_temp_id ?? '').trim() === tempId)
        .map(({ i }) => i)
    : []

  if (childIndexes.length) {
    try {
      await ElMessageBox.confirm(
        `这道材料题下面有 ${childIndexes.length} 个小问。删除母题后，这些小问会作为独立题入库。`,
        '确认删除母题？',
        { type: 'warning', confirmButtonText: '删除母题', cancelButtonText: '取消' },
      )
    } catch {
      return
    }
  }

  drafts.value.splice(index, 1)
  // 下标全部前移，勾选状态要跟着平移，否则会整体错位一位。
  const next = new Set<number>()
  for (const i of selected.value) {
    if (i < index) next.add(i)
    else if (i > index) next.add(i - 1)
  }
  selected.value = next

  for (const childIndex of childIndexes) {
    const shifted = childIndex > index ? childIndex - 1 : childIndex
    const child = drafts.value[shifted]
    if (child) child.parent_temp_id = ''
  }
  if (editingIndex.value === index) editingIndex.value = null
}

function openEditor(index: number) {
  editingIndex.value = index
}

function onModeChange(value: ImportMode) {
  form.mode = value
}

// ---- 入库 ----

async function commit() {
  if (!subject.currentId) return
  if (selectedDrafts.value.length === 0) {
    ElMessage.warning('请至少选择一道题')
    return
  }
  committing.value = true
  try {
    // 整卷导入：只把**已勾选**的草稿按大纲排进新试卷。
    // 大纲里的下标是相对整个草稿数组的，这里必须换算成
    // "第几个被勾选的"，否则跳选题会让整张卷子错位。
    const pickedIndexes = drafts.value
      .map((_, i) => i)
      .filter((i) => selected.value.has(i))
    const outline: ImportOutline[] = runMeta.value?.outline ?? []
    const remapped: ImportOutline[] = wholePaperEnabled.value
      ? outline
          .map((it: ImportOutline): ImportOutline => {
            if (it.kind !== 'question') return it
            const picked = pickedIndexes[it.draft_index ?? -1]
            // 被取消勾选的题在这次提交里不存在，换成 -1 让后端跳过它，
            // 而不是把后面的题整体前移一位。
            return { ...it, draft_index: picked ?? -1 }
          })
          .filter((it: ImportOutline) => it.kind !== 'question' || it.draft_index !== -1)
      : []

    const result = await importApi.commit(subject.currentId, {
      drafts: selectedDrafts.value,
      default_tag_ids: defaults.default_tag_ids,
      default_knowledge_point_ids: defaults.default_knowledge_point_ids,
      status: defaults.status,
      save_as_paper: wholePaperEnabled.value,
      paper_title: wholePaperTitle.value.trim() || undefined,
      outline: remapped,
    })
    commitResult.value = result

    // 整卷导入失败**不影响题目入库**：题目此刻已经在库里了，
    // 这里必须把话说清楚，否则用户会以为整批白导了。
    if (result.paper_error) {
      ElMessage.warning(`题目已入库，但试卷没能建出来：${result.paper_error}`)
    }

    // 部分成功是常态：某道题校验不过不该让整批失败。
    // 这里如实告知两边各有多少，并把成功的那部分从草稿里摘掉，
    // 剩下的留给你修——比整批重来省事得多。
    if (result.failed_count > 0) {
      const failedSet = new Set(result.failures.map((f) => f.index))
      const remaining: ImportDraft[] = []
      let cursor = 0
      for (let i = 0; i < drafts.value.length; i++) {
        if (!selected.value.has(i)) {
          remaining.push(drafts.value[i])
          continue
        }
        if (!failedSet.has(cursor)) {
          // 已成功入库，从草稿中移除
        } else {
          remaining.push(drafts.value[i])
        }
        cursor++
      }
      drafts.value = remaining
      selected.value = new Set(remaining.map((_, i) => i))
      ElMessage.warning(`入库 ${result.created_count} 道，${result.failed_count} 道未通过，已保留待修改`)
    } else {
      ElMessage.success(`已入库 ${result.created_count} 道题`)
      drafts.value = []
      selected.value = new Set()
    }

    if (result.unresolved_names?.length) {
      ElMessageBox.alert(
        `以下名称在题库中不存在，已跳过（系统不会自动创建知识点或标签）：\n${result.unresolved_names.join('、')}`,
        '未匹配的名称',
        { type: 'warning' },
      )
    }
  } finally {
    committing.value = false
  }
}

function goToQuestions() {
  void router.push({ name: 'questions' })
}

/** 打开整卷导入建出来的那份试卷，直接进编辑台。 */
function goToPaper(paperId: number) {
  void router.push({ name: 'paper-editor', params: { id: String(paperId) } })
}
</script>

<template>
  <div class="space-y-3">
    <el-alert v-if="!canEdit" type="warning" :closable="false" show-icon>
      <template #title>你在当前学科没有录入权限</template>
    </el-alert>

    <!-- 输入区 -->
    <el-card shadow="never">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="text-sm font-medium">① 提供题目内容</span>
          <el-tag v-if="tab === 'file' && form.file" size="small" type="info" effect="plain">
            待导入：{{ form.file.name }}
          </el-tag>
        </div>
      </template>

      <el-tabs v-model="tab">
        <el-tab-pane label="上传文件" name="file">
          <ImportFileDropzone
            v-model="form.file"
            :disabled="loading || !canEdit"
            @image="onImageFile"
          />
        </el-tab-pane>

        <el-tab-pane label="粘贴文本" name="text">
          <el-input
            v-model="form.content"
            type="textarea"
            :rows="10"
            placeholder="直接粘贴试卷内容。支持 Markdown、编号列表、选项与答案混排，公式可写 LaTeX（$x^2$ 或 \[...\]）。"
            maxlength="200000"
          />
          <div class="mt-1 text-right text-xs text-gray-400">{{ form.content.length }} / 200000</div>
        </el-tab-pane>

        <el-tab-pane label="上传图片" name="image">
          <div class="mb-2 flex flex-wrap gap-2">
            <div v-for="(img, index) in form.images" :key="img" class="relative">
              <img :src="img" class="h-24 w-24 rounded border border-gray-200 object-cover" />
              <el-icon
                class="absolute -right-1 -top-1 cursor-pointer rounded-full bg-red-500 text-white"
                @click="removeImage(index)"
              >
                <Close />
              </el-icon>
            </div>

            <label
              class="flex h-24 w-24 cursor-pointer flex-col items-center justify-center rounded border border-dashed border-gray-300 text-xs text-gray-400 hover:border-blue-400"
            >
              <input type="file" accept="image/*" multiple class="hidden" @change="onPickImages" />
              <el-icon v-if="!uploading" :size="20"><Plus /></el-icon>
              <el-icon v-else class="is-loading" :size="20"><Loading /></el-icon>
              <span class="mt-1">{{ uploading ? '上传中' : '添加图片' }}</span>
            </label>
          </div>
          <div class="text-xs text-gray-400">
            适用于拍照或扫描的试卷，也可以在「上传文件」区直接 Ctrl+V 粘贴截图。需要配置视觉模型，否则请改用文本模式。
          </div>
        </el-tab-pane>
      </el-tabs>

      <el-form-item v-if="form.mode === 'solve'" label="原题内容" class="mt-3">
        <el-input
          v-model="form.solve_for_content"
          type="textarea"
          :rows="6"
          placeholder="“只求解”模式会保留你提供的原题，只让模型补上答案与解析。"
        />
      </el-form-item>

      <el-divider content-position="left">
        <span class="text-xs text-gray-400">解析设置</span>
      </el-divider>

      <ImportParseOptions
        :method="form.method"
        :mode="form.mode"
        :disabled="loading"
        @update:method="onMethodChange"
        @update:mode="onModeChange"
      />

      <el-alert v-if="structuredTagWarning" type="warning" :closable="false" show-icon class="mt-2">
        <template #title>{{ structuredTagWarning }}</template>
      </el-alert>

      <div class="mt-3 flex flex-wrap items-center gap-2">
        <el-button
          type="primary"
          :loading="loading"
          :disabled="!canEdit || !subject.currentId"
          @click="extract"
        >
          <el-icon class="mr-1"><MagicStick /></el-icon>
          {{ form.method === 'template' ? '开始解析' : '开始识别' }}
        </el-button>
        <span v-if="loading" class="text-xs text-gray-400">
          正在解析，大文件可能需要几分钟，请不要关闭页面。
        </span>
        <span v-else-if="submitHint" class="text-xs text-gray-400">{{ submitHint }}</span>
      </div>
    </el-card>

    <!-- 草稿审核 -->
    <el-card v-if="drafts.length" shadow="never">
      <template #header>
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm font-medium">② 核对草稿</span>
          <el-tag size="small" type="info">{{ drafts.length }} 道</el-tag>
          <el-tag v-if="warningCount" size="small" type="warning">{{ warningCount }} 道有提示</el-tag>
          <el-tag size="small" type="success">已选 {{ selectedDrafts.length }}</el-tag>

          <div class="ml-auto flex gap-1">
            <el-button size="small" @click="selectAll">全选</el-button>
            <el-button size="small" @click="selectNone">全不选</el-button>
            <el-button v-if="warningCount" size="small" type="warning" plain @click="selectWarningsOnly">
              只选有提示的
            </el-button>
          </div>
        </div>
      </template>

      <!-- 本次抽取的如实记录：走没走模型、覆盖了哪几个文件 -->
      <div v-if="runMeta" class="mb-3 rounded border border-gray-200 bg-gray-50 p-2 text-xs">
        <div class="flex flex-wrap items-center gap-2">
          <el-tag v-if="ranTemplate" size="small" type="warning">
            <el-icon class="mr-1"><Cpu /></el-icon>
            规则解析
          </el-tag>
          <el-tag v-else size="small" type="primary" effect="plain">
            <el-icon class="mr-1"><MagicStick /></el-icon>
            模型识别
          </el-tag>
          <span v-if="ranTemplate" class="text-gray-500">
            本次由确定性规则解析器完成，<b>没有调用任何模型</b>。结果只认标签格式，请重点核对题型与答案。
          </span>
          <span v-else-if="runMeta.model" class="text-gray-500">模型 {{ runMeta.model }}</span>
          <span class="text-gray-400">耗时 {{ runMeta.elapsed_ms }}ms · 任务 #{{ runMeta.task_id }}</span>
          <el-tag v-if="!runMeta.methodReported" size="small" type="info" effect="plain">
            服务端未回报实际路径
          </el-tag>
        </div>
        <div v-if="sourceFiles.length" class="mt-1 flex flex-wrap items-center gap-2">
          <span class="text-gray-500">来源文件：</span>
          <el-tag
            v-for="file in sourceFiles"
            :key="file"
            size="small"
            :type="fileFilter === file ? 'primary' : 'info'"
            :effect="fileFilter === file ? 'dark' : 'plain'"
            class="cursor-pointer"
            @click="fileFilter = fileFilter === file ? '' : file"
          >
            {{ file }} · {{ fileCounts.get(file) ?? 0 }} 道
          </el-tag>
          <el-button v-if="fileFilter" text size="small" @click="fileFilter = ''">显示全部</el-button>
        </div>
        <div v-if="fileFilter" class="mt-1 text-gray-400">
          筛选只影响展示，勾选与入库范围不变。
        </div>
      </div>

      <el-empty v-if="!visibleTree.length" description="当前筛选下没有题目" :image-size="60" />

      <div v-else class="space-y-2">
        <ImportDraftCard
          v-for="node in visibleTree"
          :key="node.index"
          :draft="node.draft"
          :index="node.index"
          :children="node.children"
          :is-checked="isChecked"
          :shown-for-child="shownForChild(node)"
          @toggle="toggleSelect"
          @edit="openEditor"
          @remove="removeDraft"
        />
      </div>
    </el-card>

    <!-- 入库设置 -->
    <el-card v-if="drafts.length" shadow="never">
      <template #header>
        <span class="text-sm font-medium">③ 入库设置</span>
      </template>

      <el-row :gutter="12">
        <el-col :xs="24" :md="8">
          <div class="mb-1 text-sm">入库状态</div>
          <el-radio-group v-model="defaults.status">
            <el-radio value="draft">草稿</el-radio>
            <el-radio value="pending">待审核</el-radio>
            <el-radio v-if="subject.can('review_question')" value="published">直接发布</el-radio>
          </el-radio-group>
          <div class="mt-1 text-xs text-gray-400">建议先入库为草稿，逐题确认后再提交审核。</div>
        </el-col>

        <el-col :xs="24" :md="8">
          <div class="mb-1 text-sm">统一添加标签</div>
          <el-select v-model="defaults.default_tag_ids" multiple clearable placeholder="选填" class="w-full">
            <el-option v-for="tag in tags" :key="tag.id" :label="tag.name" :value="tag.id" />
          </el-select>
        </el-col>

        <el-col :xs="24" :md="8">
          <div class="mb-1 text-sm">统一关联知识点</div>
          <el-select
            v-model="defaults.default_knowledge_point_ids"
            multiple
            filterable
            clearable
            placeholder="选填"
            class="w-full"
          >
            <el-option v-for="kp in knowledgePoints" :key="kp.id" :label="kp.name" :value="kp.id" />
          </el-select>
        </el-col>
      </el-row>

      <div class="mt-3 flex items-center gap-2">
        <el-button
          type="primary"
          :loading="committing"
          :disabled="selectedDrafts.length === 0"
          @click="commit"
        >
          确认入库（{{ selectedDrafts.length }} 道）
        </el-button>
        <span class="text-xs text-gray-400">只写入你勾选的题目。未匹配到的知识点名不会自动创建。</span>
      </div>

      <!-- 整卷导入 -->
      <div class="mt-3 border-t border-gray-100 pt-3">
        <el-checkbox v-model="wholePaperEnabled">
          同时把这份卷子还原成可编辑的试卷
        </el-checkbox>
        <div class="mt-1 text-xs text-gray-400">
          保留大题标题与题间说明。建卷失败不影响题目入库。
        </div>
        <el-input
          v-if="wholePaperEnabled"
          v-model="wholePaperTitle"
          placeholder="试卷标题（留空则用来源文件名）"
          class="mt-2 max-w-md"
          clearable
        />
      </div>
    </el-card>

    <!-- 结果 -->
    <el-card v-if="commitResult" shadow="never">
      <template #header><span class="text-sm font-medium">入库结果</span></template>
      <div class="mb-2 flex items-center gap-3 text-sm">
        <el-tag type="success">成功 {{ commitResult.created_count }}</el-tag>
        <el-tag v-if="commitResult.failed_count" type="danger">失败 {{ commitResult.failed_count }}</el-tag>
        <el-button text type="primary" size="small" @click="goToQuestions">去题库查看 →</el-button>
      </div>

      <!-- 建卷结果必须和入库结果分开说：题目已经进库了 -->
      <el-alert
        v-if="commitResult.paper_id"
        type="success"
        :closable="false"
        show-icon
        class="mb-2"
      >
        已按版面创建试卷。
        <el-button text type="primary" size="small" @click="goToPaper(commitResult.paper_id)">
          打开试卷 →
        </el-button>
      </el-alert>
      <el-alert
        v-else-if="commitResult.paper_error"
        type="warning"
        :closable="false"
        show-icon
        class="mb-2"
        :title="`题目已入库，但试卷没能建出来：${commitResult.paper_error}`"
      />

      <el-table v-if="commitResult.failures.length" :data="commitResult.failures" size="small">
        <el-table-column prop="index" label="#" width="60" />
        <el-table-column prop="stem" label="题干" show-overflow-tooltip />
        <el-table-column prop="error" label="失败原因" min-width="240" />
      </el-table>

      <div v-if="commitResult.unresolved_names?.length" class="mt-2 text-xs text-gray-500">
        未匹配到的名称：{{ commitResult.unresolved_names.join('、') }}
      </div>
    </el-card>

    <ImportDraftEditDialog v-model="editingVisible" :draft="editingDraft" @save="applyDraftEdit" />
  </div>
</template>
