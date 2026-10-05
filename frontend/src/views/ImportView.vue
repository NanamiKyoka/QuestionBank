<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { importApi, uploadApi, type CommitResult } from '@/api/ai'
import { ApiError } from '@/api/http'
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
import { useUnsavedGuard } from '@/composables/useUnsavedGuard'
import EmptyState from '@/components/ui/EmptyState.vue'
import PageHeader from '@/components/ui/PageHeader.vue'

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

/**
 * 正在进行的那次识别。有了句柄才谈得上"取消"与"离开拦截"。
 *
 * 抽取是全站最长的同步请求（预算 20 分钟），此前唯一的反馈是按钮转圈：
 * 用户既不能取消，刷新页面还会白白让服务端把活干完。
 */
const extractAbort = ref<AbortController | null>(null)

/** 已等待秒数：转圈没有时间感，计时才能把"慢"和"卡死"区分开。 */
const elapsed = ref(0)
let elapsedTimer: ReturnType<typeof setInterval> | null = null

const elapsedLabel = computed(() => {
  const s = elapsed.value
  if (s < 60) return `${s} 秒`
  return `${Math.floor(s / 60)} 分 ${String(s % 60).padStart(2, '0')} 秒`
})

const form = reactive({
  content: '',
  mode: 'extract' as ImportMode,
  solve_for_content: '',
  images: [] as string[],
  method: 'ai' as ImportParseMethod,
  /** 待上传的文档。文件与文本是二选一：文档优先，因为格式信息只有文件里有。 */
  file: null as File | null,
  /**
   * 答案页 / 解析页的图片（已上传，拿到的地址）。
   *
   * 单列一个字段而不是复用 images：images 是"题目本身就是图片"，
   * 这里 是"题目有文本、答案在图上"。两者混在一起的话，
   * "这次到底识别了什么"在界面上说不清楚。
   */
  answerImages: [] as string[],
})

const uploading = ref(false)

/**
 * 一次选中的多份文档，等用户决定怎么处理。
 *
 * 单份识别和整批排队是两条完全不同的路：前者当场返回草稿，
 * 后端要一路等到模型返回；后者立刻返回任务列表，后台依次跑。
 * 替用户挑一条都不合适——整份文件夹里可能只有一份是他现在要看的。
 */
const batchFiles = ref<File[]>([])
const enqueuing = ref(false)

function onBatch(files: File[]) {
  form.file = null
  batchFiles.value = files
}

function discardBatch() {
  batchFiles.value = []
}

function sizeLabel(bytes: number): string {
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`
}

/** 整批排队：后台 worker 依次处理，结果在导入历史里看。 */
async function enqueueBatch() {
  if (!subject.currentId || !batchFiles.value.length) return
  enqueuing.value = true
  try {
    const res = await importTaskApi.enqueue(subject.currentId, {
      files: batchFiles.value,
      method: form.method,
      mode: form.mode,
    })
    const rejected = res.failures?.length
      ? `，${res.rejected} 个未收下：${res.failures.join('；')}`
      : ''
    ElMessage.success(`已排队 ${res.accepted} 个文件${rejected}`)
    batchFiles.value = []
    void router.push({ name: 'import-history' })
  } finally {
    enqueuing.value = false
  }
}

/** 只识别第一份：其余留在原地不清空，用户可以再点一次排队。 */
function takeFirstOnly() {
  const [first] = batchFiles.value
  if (!first) return
  batchFiles.value = []
  form.file = first
}

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
  mode: ImportMode
  model: string
  elapsed_ms: number
  task_id: number
  source_files: string[]
  /** 版面大纲：标题、说明文字与题目引用的顺序。整卷导入靠它还原卷面。 */
  outline: ImportOutline[]
  total: number
  /** 文档级告警：哪几页/哪几个文件没抽出来。为空表示这次是完整的。 */
  warnings: string[]
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

// ---- 取消与离开拦截 ----

/** 中止进行中的识别：请求断开，结果不再回传，本次不产生任何草稿。 */
function cancelExtract() {
  extractAbort.value?.abort()
}

/** 收掉句柄与计时：请求回来（或被取消）之后就不再算"进行中"。 */
function endExtract() {
  extractAbort.value = null
  if (elapsedTimer) {
    clearInterval(elapsedTimer)
    elapsedTimer = null
  }
}

/**
 * 识别进行中离开页面要拦一下。
 *
 * 站内跳转和刷新不同——它是可以被准确告知代价的。说清楚"这次识别作废"，
 * 比一句没人能验证的"请不要关闭页面"有用得多。
 */
onBeforeRouteLeave(async () => {
  if (!extractAbort.value) return true
  try {
    await ElMessageBox.confirm(
      '正在识别中，离开会中断本次操作，已识别的内容不会保留。',
      '识别尚未完成',
      { type: 'warning', confirmButtonText: '中断并离开', cancelButtonText: '留在这里' },
    )
  } catch {
    return false
  }
  cancelExtract()
  return true
})

function onBeforeUnload(event: BeforeUnloadEvent) {
  if (!extractAbort.value) return
  event.preventDefault()
  // 现代浏览器忽略自定义文案，但必须调用才能触发原生确认。
  event.returnValue = ''
}

onMounted(() => window.addEventListener('beforeunload', onBeforeUnload))
onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
  cancelExtract()
  if (elapsedTimer) clearInterval(elapsedTimer)
})

// 同步抽取的草稿只活在这一页，不入库就等于白抽——离开前要说一声。
// 队列任务的草稿在服务端有存根，随时能从导入历史再打开，不用拦。
const draftsArePersisted = ref(false)
useUnsavedGuard(
  () => drafts.value.length > 0 && !draftsArePersisted.value,
  '已抽取的草稿还没有入库，离开会丢弃它们，确定离开吗？',
)

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
    // 服务端存着这次任务的草稿：离开也不会丢，不值得拦。
    draftsArePersisted.value = true
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
    batchFiles.value = []
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

// ---- 答案页 ----

/** 答案页数量上限，与后端 maxAnswerImages 一致。 */
const MAX_ANSWER_IMAGES = 10

/**
 * 加入答案页图片。
 *
 * 没有答案页时，模型只能把每道解答题自己算一遍——实测约 1370 输出
 * token/题，一份二十题的卷子近三万 token，几分钟起步且极易撞上
 * 供应商的超时上限。有答案页时它只需转写，快一个数量级。
 */
async function onPickAnswerImages(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  if (!files.length) return

  const room = MAX_ANSWER_IMAGES - form.answerImages.length
  if (room <= 0) {
    ElMessage.warning(`一次最多 ${MAX_ANSWER_IMAGES} 张答案页`)
    return
  }
  const picked = files.slice(0, room)
  if (picked.length < files.length) {
    ElMessage.warning(`一次最多 ${MAX_ANSWER_IMAGES} 张，已取前 ${room} 张`)
  }

  uploading.value = true
  try {
    for (const file of picked) {
      const saved = await uploadApi.image(file)
      form.answerImages.push(saved.url)
    }
  } finally {
    uploading.value = false
  }
}

function removeAnswerImage(index: number) {
  form.answerImages.splice(index, 1)
}

/** 换文件时答案页要一并清掉：上一份卷子的答案对新卷子毫无意义。 */
watch(
  () => form.file,
  (file) => {
    form.answerImages = []
    // 用户明确选了单份，之前那批待分流的文档就作废了。
    if (file) batchFiles.value = []
  },
)

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
  if (fileIsPDF.value) return null // PDF 里根本不会有【题目】标签，提示没有意义
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

/** 选中的文件是不是一份 PDF。 */
const fileIsPDF = computed(() => !!form.file && /\.pdf$/i.test(form.file.name))

/**
 * PDF + 规则解析是个行不通的组合。
 *
 * PDF 现在会渲染成页图交给视觉模型，规则解析器没有图片这条路。与其让它
 * 跑一遍再失败，不如在提交前就说清楚。
 */
const pdfNeedsModel = computed(
  () => fileIsPDF.value && form.method === 'template' && tab.value === 'file',
)

/**
 * PDF 会走视觉模型，比 .md/.txt 慢不少。
 *
 * 提前说一句"慢在哪儿"，免得用户等三分钟后以为卡死了——那次等待是真的，
 * 但它有明确的理由（逐页读图），说清楚和让它自己猜，结果完全不同。
 */
const pdfIsSlow = computed(
  () => fileIsPDF.value && form.method !== 'template' && tab.value === 'file',
)

/** 缺东西或选法不成立时说清是哪一样不对，而不是把按钮灰掉让人猜。 */
const submitHint = computed(() => {
  if (pdfNeedsModel.value) return '规则解析不认 PDF 的排版文字，请改用「模型识别」'
  if (canSubmit()) return ''
  if (tab.value === 'file') return '请先选择要导入的文件'
  if (tab.value === 'image') return '请先添加图片'
  return '请粘贴题目内容'
})

/**
 * 「仅录题」识别成功后是否直接入库。
 *
 * 默认开启，因为这个模式的产物就是"还没有答案的题目本身"：
 * 它不需要像完整抽取那样先逐题核对模型写的答案，而多出来的那一次点击
 * 恰恰是最容易被漏掉的一步——用户看到"识别成功"就去题库找题，
 * 结果题库里一道都没有（草稿还停在录题页上）。
 *
 * 关掉它就回到原来的两步流程：先核对草稿，再手动入库。
 */
const autoCommit = ref(true)

/** 主按钮文案：直接告知这一次点击会产生什么后果。 */
const submitLabel = computed(() => {
  if (form.method === 'template') return '开始解析'
  return form.mode === 'record' && autoCommit.value ? '识别并入库' : '开始识别'
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
  if (pdfNeedsModel.value) {
    ElMessage.warning(submitHint.value)
    return
  }
  if (!canSubmit()) {
    ElMessage.warning(submitHint.value)
    return
  }

  const controller = new AbortController()
  extractAbort.value = controller
  const signal = controller.signal
  loading.value = true
  elapsed.value = 0
  elapsedTimer = setInterval(() => {
    elapsed.value += 1
  }, 1000)
  commitResult.value = null
  const auto = form.mode === 'record' && autoCommit.value
  try {
    const result =
      tab.value === 'file' && form.file
        ? await importTaskApi.upload(
            subject.currentId,
            {
              file: form.file,
              method: form.method,
              mode: form.mode,
              // 仅录题不读答案页：不发过去，省下的不只是一次读图，
              // 还有模型照着答案页把每道题"顺手"算一遍的可能。
              answerImages: form.mode === 'record' ? undefined : form.answerImages,
            },
            { signal },
          )
        : await importTaskApi.extract(
            subject.currentId,
            {
              content: tab.value === 'text' ? form.content : '',
              images: tab.value === 'image' ? form.images : undefined,
              method: form.method,
              mode: form.mode,
              solve_for_content: form.mode === 'solve' ? form.solve_for_content : undefined,
            },
            { signal },
          )
    // 请求已经回来了：接下来是入库，那一步不该再显示"取消"。
    endExtract()
    // 自动入库时要吞掉"识别出 N 道题"这句：它读起来像"完事了"，
    // 而真正的结果紧接着由入库那一步给出。两句一起弹只会让人以为重复执行了。
    acceptResult(result, auto)
    if (auto && drafts.value.length) {
      await doCommit(true)
    }
  } catch (err) {
    // 取消不是失败：交代清楚"什么都没留下"，也不把它抛成未处理的 rejection。
    if (err instanceof ApiError && err.isCancelled) {
      ElMessage.info('已取消识别，本次没有产生草稿')
      return
    }
    throw err
  } finally {
    loading.value = false
    endExtract()
  }
}

function acceptResult(result: ImportExtractResponse, quiet = false) {
  drafts.value = result.drafts ?? []
  // 这批草稿来自刚跑完的同步抽取：服务端没有存根，离开就没了。
  draftsArePersisted.value = false
  // 默认全选：绝大多数情况下用户是想全部入库的，逐题勾选是负担。
  selected.value = new Set(drafts.value.map((_, i) => i))
  fileFilter.value = ''
  editingIndex.value = null

  const reported = result.method?.trim() ?? ''
  runMeta.value = {
    method: reported || form.method,
    methodReported: !!reported,
    mode: form.mode,
    model: result.model,
    elapsed_ms: result.elapsed_ms,
    task_id: result.task_id,
    source_files: result.source_files ?? [],
    outline: result.outline ?? [],
    total: result.total,
    warnings: result.warnings ?? [],
  }

  if (drafts.value.length === 0) {
    ElMessage.warning(
      form.method === 'template'
        ? '没有解析出任何题目，请确认内容使用了【题目】标签'
        : '没有识别出任何题目，请检查内容是否完整',
    )
    return
  }
  // 有文档级告警时不能弹"成功"：部分页/部分文件没抽出来，
  // 绿色的那句话会让人以为整份都齐了。
  if (!quiet) {
    if (runMeta.value.warnings.length) {
      ElMessage.warning(`识别出 ${result.total} 道题，但有 ${runMeta.value.warnings.length} 项未完成`)
    } else {
      ElMessage.success(`识别出 ${result.total} 道题`)
    }
  }
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

/** 模板里绑的入口。不接参数：`@click="commit"` 会把事件对象传进来。 */
function commit() {
  void doCommit(false)
}

/**
 * 确认入库。
 *
 * `auto` 表示这是"仅录题识别成功后自动入库"那一步，界面上的措辞要跟着变：
 * 用户在自动流程里没有点过任何按钮，成功提示必须自己说清
 * "题现在在哪、下一步去哪儿"，否则"已入库 8 道题"这行字出现得毫无上下文。
 */
async function doCommit(auto: boolean) {
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
      // 只重新勾选"本来就勾着且没入库成功"的那些：用户主动取消勾选的题
      // 不能因为一次失败就被捞回来，否则再点一次就把它们也导进去了。
      const kept = new Set<number>()
      let cursor = 0
      for (let i = 0; i < drafts.value.length; i++) {
        if (!selected.value.has(i)) {
          remaining.push(drafts.value[i])
          continue
        }
        if (failedSet.has(cursor)) {
          kept.add(remaining.length)
          remaining.push(drafts.value[i])
        }
        cursor++
      }
      drafts.value = remaining
      selected.value = kept
      ElMessage.warning(`入库 ${result.created_count} 道，${result.failed_count} 道未通过，已保留待修改`)
    } else {
      // "仅录题"的默认路径下用户没有点过任何按钮，这一句要把话说完整：
      // 题进去了、答案还是空的、去哪儿补。
      if (auto) {
        ElMessage.success(
          `已录入题库 ${result.created_count} 道题（答案待补充）。可在题库里补写，或选中它们让 AI 解题。`,
        )
      } else {
        ElMessage.success(`已入库 ${result.created_count} 道题`)
      }
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
    <PageHeader
      title="智能录题"
      description="上传试卷文件、截图或直接粘贴文本，模型识别题型与选项后逐题确认再入库。"
    />

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
            @batch="onBatch"
          />

          <!-- 多份文档的分流口：同步识别与排队是两条耗时差一个量级的路。 -->
          <div
            v-if="batchFiles.length"
            class="mt-3 rounded-lg border border-accent-border bg-accent-soft/40 p-3"
          >
            <div class="flex flex-wrap items-center gap-2">
              <el-tag type="primary" size="small">已选 {{ batchFiles.length }} 份文档</el-tag>
              <span class="text-xs text-ink-muted">选一个处理方式：</span>
            </div>
            <div class="mt-2 max-h-32 overflow-y-auto text-xs text-ink-regular">
              <div v-for="f in batchFiles" :key="f.name" class="truncate">
                {{ f.name }} · {{ sizeLabel(f.size) }}
              </div>
            </div>
            <div class="mt-2 flex flex-wrap gap-2">
              <el-button
                type="primary"
                size="small"
                :loading="enqueuing"
                :disabled="!canEdit"
                @click="enqueueBatch"
              >
                整批排队，后台依次识别
              </el-button>
              <el-button size="small" :disabled="loading || !canEdit" @click="takeFirstOnly">
                只识别第一份（{{ batchFiles[0]?.name }}）
              </el-button>
              <el-button size="small" text @click="discardBatch">取消</el-button>
            </div>
            <div class="mt-1 text-xs text-ink-subtle">
              排队后可以离开本页，进度与重试都在「导入历史」里。
            </div>
          </div>

          <!-- 答案页。放在文件页而不是图片页，是因为它的意义完全依附于文档：
               有试卷才有对应它的答案。 -->
          <div class="mt-3 rounded border border-dashed border-hairline p-3">
            <div class="mb-2 flex flex-wrap items-center gap-2">
              <span class="text-sm text-ink-regular">
                {{ form.mode === 'record' ? '答案页（仅录题模式下不读取）' : '答案页（选填，可加快识别）' }}
              </span>
              <el-tag size="small" type="info" effect="plain">
                {{ form.answerImages.length }} / {{ MAX_ANSWER_IMAGES }}
              </el-tag>
            </div>

            <template v-if="form.mode === 'record'">
              <div class="text-xs text-ink-subtle">
                「仅录题」不读答案页：题目之外的答案与解析由老师事后填写，
                随本次请求提交时也会被忽略。要让模型转写答案页，请切回「完整抽取」。
              </div>
            </template>

            <template v-else>
              <div v-if="form.answerImages.length" class="mb-2 flex flex-wrap gap-2">
                <div v-for="(img, index) in form.answerImages" :key="img" class="relative">
                  <img
                    :src="img"
                    alt="答案页截图"
                    class="h-16 w-16 rounded border border-hairline object-cover"
                  />
                  <el-icon
                    class="absolute -right-1 -top-1 cursor-pointer rounded-full bg-danger text-white"
                    role="button"
                    tabindex="0"
                    :aria-label="`移除第 ${index + 1} 张答案页截图`"
                    @click="removeAnswerImage(index)"
                    @keydown.enter="removeAnswerImage(index)"
                  >
                    <Close />
                  </el-icon>
                </div>
              </div>

              <label
                class="inline-flex cursor-pointer items-center gap-1 rounded border border-hairline-strong px-2 py-1 text-xs text-ink-muted hover:border-accent"
              >
                <input
                  type="file"
                  accept="image/*"
                  multiple
                  class="sr-only focus-visible:not-sr-only"
                  :disabled="loading || !canEdit"
                  @change="onPickAnswerImages"
                />
                <el-icon :size="14"><Plus /></el-icon>
                添加答案页图片
              </label>

              <div class="mt-2 text-xs text-ink-subtle">
                试卷本身通常不带答案。没有答案页时模型要把每道解答题自己算一遍；
                附上答案页后它可以改为<b>转写</b>，输出量因此下降。
                但模型还要先"读图"，这部分时间由模型速度决定——
                实测某些模型读两张图就要多花一分多钟，此时反而更慢。
                图片对不上的题目会留空答案并标注，不会硬凑。
              </div>
            </template>
          </div>
        </el-tab-pane>

        <el-tab-pane label="粘贴文本" name="text">
          <el-input
            v-model="form.content"
            type="textarea"
            :rows="10"
            placeholder="直接粘贴试卷内容。支持 Markdown、编号列表、选项与答案混排，公式可写 LaTeX（$x^2$ 或 \[...\]）。"
            maxlength="200000"
          />
          <div class="mt-1 text-right text-xs text-ink-subtle">{{ form.content.length }} / 200000</div>
        </el-tab-pane>

        <el-tab-pane label="上传图片" name="image">
          <div class="mb-2 flex flex-wrap gap-2">
            <div v-for="(img, index) in form.images" :key="img" class="relative">
              <img
                :src="img"
                alt="试卷截图"
                class="h-24 w-24 rounded border border-hairline object-cover"
              />
              <el-icon
                class="absolute -right-1 -top-1 cursor-pointer rounded-full bg-danger text-white"
                role="button"
                tabindex="0"
                :aria-label="`移除第 ${index + 1} 张试卷截图`"
                @click="removeImage(index)"
                @keydown.enter="removeImage(index)"
              >
                <Close />
              </el-icon>
            </div>

            <label
              class="flex h-24 w-24 cursor-pointer flex-col items-center justify-center rounded border border-dashed border-hairline-strong text-xs text-ink-subtle hover:border-accent"
            >
              <input
                type="file"
                accept="image/*"
                multiple
                class="sr-only focus-visible:not-sr-only"
                @change="onPickImages"
              />
              <el-icon v-if="!uploading" :size="20"><Plus /></el-icon>
              <el-icon v-else class="is-loading" :size="20"><Loading /></el-icon>
              <span class="mt-1">{{ uploading ? '上传中' : '添加图片' }}</span>
            </label>
          </div>
          <div class="text-xs text-ink-subtle">
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
        <span class="text-xs text-ink-subtle">解析设置</span>
      </el-divider>

      <ImportParseOptions
        :method="form.method"
        :mode="form.mode"
        :disabled="loading"
        @update:method="onMethodChange"
        @update:mode="onModeChange"
      />

      <el-alert v-if="pdfNeedsModel" type="warning" :closable="false" show-icon class="mt-2">
        <template #title>{{ submitHint }}</template>
      </el-alert>

      <el-alert v-else-if="structuredTagWarning" type="warning" :closable="false" show-icon class="mt-2">
        <template #title>{{ structuredTagWarning }}</template>
      </el-alert>

      <!-- 仅录题的收尾方式。必须在点「开始识别」之前就能选：
           识别完才出现这个开关的话，"识别后自动入库"对本次已经晚了。 -->
      <div v-if="form.mode === 'record'" class="mt-3 rounded border border-hairline bg-surface-sunken p-2">
        <el-checkbox v-model="autoCommit" :disabled="loading">
          识别成功后 <b>直接入库</b>
        </el-checkbox>
        <div class="mt-1 text-xs text-ink-subtle">
          勾选后不必再点一次「确认入库」：识别出来的题目直接进题库（状态由「入库设置」决定），
          待补充的答案在题库里补写，或者在那里对选中的题让 AI 解题。
          关闭它则回到"先核对草稿、再入库"的两步流程。
        </div>
      </div>

      <div class="mt-3 flex flex-wrap items-center gap-2">
        <el-button
          type="primary"
          :loading="loading"
          :disabled="!canEdit || !subject.currentId"
          @click="extract"
        >
          <el-icon class="mr-1"><MagicStick /></el-icon>
          {{ submitLabel }}
        </el-button>
        <el-button v-if="extractAbort" @click="cancelExtract">取消识别</el-button>
        <span v-if="extractAbort" class="text-xs text-ink-subtle">
          {{ pdfIsSlow
            ? 'PDF 正在逐页渲染并交给模型识别，通常要几分钟。'
            : '正在解析，大文件可能需要几分钟。' }}
          已等待 {{ elapsedLabel }}，随时可以取消。
        </span>
        <span v-else-if="!loading && submitHint" class="text-xs text-ink-subtle">{{ submitHint }}</span>
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

      <!-- 文档级告警必须一直挂在最显眼处：部分抽取失败和全成功长得太像，
           只靠一条会自己消失的 toast 说不清楚。 -->
      <el-alert
        v-if="runMeta?.warnings.length"
        type="warning"
        :closable="false"
        show-icon
        class="mb-3"
        title="这次抽取并不完整，缺失的部分不会出现在下面的草稿里"
      >
        <div v-for="(w, i) in runMeta.warnings" :key="i" class="text-xs leading-5">{{ w }}</div>
      </el-alert>

      <!-- 本次抽取的如实记录：走没走模型、覆盖了哪几个文件 -->
      <div v-if="runMeta" class="mb-3 rounded border border-hairline bg-surface-sunken p-2 text-xs">
        <div class="flex flex-wrap items-center gap-2">
          <el-tag v-if="ranTemplate" size="small" type="warning">
            <el-icon class="mr-1"><Cpu /></el-icon>
            规则解析
          </el-tag>
          <el-tag v-else size="small" type="primary" effect="plain">
            <el-icon class="mr-1"><MagicStick /></el-icon>
            模型识别
          </el-tag>
          <span v-if="ranTemplate" class="text-ink-muted">
            本次由确定性规则解析器完成，<b>没有调用任何模型</b>。结果只认标签格式，请重点核对题型与答案。
          </span>
          <span v-if="runMeta.model" class="text-ink-muted">模型 {{ runMeta.model }}</span>
          <el-tag v-if="runMeta.mode === 'record'" size="small" type="warning" effect="plain">
            仅录题 · 未求解
          </el-tag>
          <span class="num text-ink-subtle">耗时 {{ runMeta.elapsed_ms }}ms · 任务 #{{ runMeta.task_id }}</span>
          <el-tag v-if="!runMeta.methodReported" size="small" type="info" effect="plain">
            服务端未回报实际路径
          </el-tag>
        </div>
        <div v-if="sourceFiles.length" class="mt-1 flex flex-wrap items-center gap-2">
          <span class="text-ink-muted">来源文件：</span>
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
        <div v-if="fileFilter" class="mt-1 text-ink-subtle">
          筛选只影响展示，勾选与入库范围不变。
        </div>
      </div>

      <EmptyState v-if="!visibleTree.length" description="当前筛选下没有题目" />

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
          <div class="mt-1 text-xs text-ink-subtle">建议先入库为草稿，逐题确认后再提交审核。</div>
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
        <span class="text-xs text-ink-subtle">只写入你勾选的题目。未匹配到的知识点名不会自动创建。</span>
      </div>

      <el-alert
        v-if="runMeta?.mode === 'record'"
        type="info"
        :closable="false"
        show-icon
        class="mt-2"
        title="本次是「仅录题」：没有答案的题目以「待补充答案」入库，之后在题库里补写即可。"
      />

      <!-- 整卷导入 -->
      <div class="mt-3 border-t border-hairline pt-3">
        <el-checkbox v-model="wholePaperEnabled">
          同时把这份卷子还原成可编辑的试卷
        </el-checkbox>
        <div class="mt-1 text-xs text-ink-subtle">
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

      <div v-if="commitResult.unresolved_names?.length" class="mt-2 text-xs text-ink-muted">
        未匹配到的名称：{{ commitResult.unresolved_names.join('、') }}
      </div>
    </el-card>

    <ImportDraftEditDialog v-model="editingVisible" :draft="editingDraft" @save="applyDraftEdit" />
  </div>
</template>
