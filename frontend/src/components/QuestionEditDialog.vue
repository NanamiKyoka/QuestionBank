<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { knowledgePointApi, questionApi, tagApi } from '@/api'
import type {
  AnswerSpec,
  KnowledgePoint,
  QuestionBrief,
  QuestionCreatePayload,
  QuestionDetail,
  QuestionStatus,
  QuestionType,
  QuestionUpdatePayload,
  RichDoc,
  Tag,
} from '@/api/types'
import RichTextEditor from '@/components/editor/RichTextEditor.vue'
import { emptyDoc, plainToRichDoc, richDocToPlain } from '@/lib/richdoc'

/**
 * 题目编辑对话框。
 *
 * 一处关键设计：选项内容用**纯文本输入框**而不是富文本编辑器。
 * 四个选项各挂一个完整编辑器会让弹窗又重又卡，而选项绝大多数是一行短文本；
 * 需要公式时写 `$x^2$` 即可——保存时由 plainToRichDoc 转成行内公式节点。
 * 题干与解析则用完整编辑器，因为它们确实需要表格、列表与独立公式。
 */
const props = defineProps<{
  modelValue: boolean
  subjectId: number
  /** 为空表示新建。 */
  questionId: number | null
  /**
   * 新建时预置的母题。
   *
   * 列表上的「添加小问」用它把新题直接挂到当前题下——
   * 这是材料题唯一真正需要的入口，让用户先去筛选里找出母题再手填 id
   * 等于把两步操作藏起来。
   */
  presumeParentId?: number | null
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  saved: [question: QuestionDetail]
}>()

const visible = computed({
  get: () => props.modelValue,
  set: (value) => emit('update:modelValue', value),
})

const formRef = ref<FormInstance>()
const loading = ref(false)
const submitting = ref(false)

const knowledgePoints = ref<KnowledgePoint[]>([])
const tags = ref<Tag[]>([])
/** 母题候选：当前学科下的独立题。 */
const parentCandidates = ref<QuestionBrief[]>([])
/** 编辑已有题目时的原始状态，用于决定要不要提示"改动已发布内容"。 */
const originalStatus = ref<QuestionStatus | null>(null)

interface OptionRow {
  label: string
  text: string
}

const form = reactive({
  question_type: 'single_choice' as QuestionType,
  difficulty: 3,
  visibility: 'public' as 'public' | 'private',
  /** 母题；null 表示独立题。 */
  parent_id: null as number | null,
  stem: emptyDoc() as RichDoc,
  options: [] as OptionRow[],
  // 选择/判断类答案
  correctLabels: [] as string[],
  boolAnswer: true,
  // 填空题：每个空的可接受答案（用「或」分隔）
  blanks: [''] as string[],
  // 解答题参考答案
  reference: emptyDoc() as RichDoc,
  analysis: null as RichDoc | null,
  tag_ids: [] as number[],
  knowledge_point_ids: [] as number[],
  /** 改动已发布题目时的说明，写入审核日志。 */
  change_note: '',
  /**
   * 这道题还没有答案（智能录题的「仅录题」模式录进来的）。
   *
   * 保留这个开关是为了改别处时不被迫先编一个答案——不然老师想给这道题
   * 打个标签，都得先把它做出来。
   */
  answerPending: false,
})

const rules: FormRules = {
  question_type: [{ required: true, message: '请选择题型', trigger: 'change' }],
}

const isChoice = computed(
  () => form.question_type === 'single_choice' || form.question_type === 'multiple_choice',
)
const isSingle = computed(() => form.question_type === 'single_choice')

/**
 * 改已发布的题目要给出提示，不是拦住。
 *
 * 服务端从来没有"已发布不可改"的守卫，界面却把编辑按钮藏了起来——
 * 结果一个错别字只能删除重建，题目 id 一丢，所有引用它的试卷跟着断。
 * 与其让按钮撒谎，不如把后果说清楚：改完 content_revision 会 +1，
 * 引用了这道题的卷子会提示"题目已被改动"，改动本身还会写进审核记录。
 */
const editsPublished = computed(
  () => props.questionId !== null && originalStatus.value === 'published',
)

/** 打开时初始化：新建给默认选项，编辑则回填。 */
watch(
  () => props.modelValue,
  async (open) => {
    if (!open) return
    await loadOptions()
    reset()
    if (props.questionId) await loadQuestion(props.questionId)
  },
)

async function loadOptions() {
  if (!props.subjectId) return
  try {
    const [tree, tagList, parents] = await Promise.all([
      knowledgePointApi.tree(props.subjectId),
      tagApi.list(props.subjectId),
      loadParentCandidates(),
    ])
    knowledgePoints.value = flatten(tree)
    tags.value = tagList
    parentCandidates.value = parents
  } catch {
    // 下拉选项加载失败不阻断编辑，用户仍可保存题目本身。
  }
}

/**
 * 母题候选：只列**当前学科下的独立题**。
 *
 * 限定 root_only=true 顺带解决了环的问题，省掉了在界面上做可达性分析的麻烦：
 * 候选的 parent_id 全为空，因此它不可能是自己的祖先，
 * 无论当前题挂在哪儿，挂上去都不成环。服务端还会再校验一次同学科与环，
 * 这里只是让正常路径下根本触发不到那个错误。
 *
 * 排除自身是必须的：否则编辑一道独立题时，它会出现在自己的母题列表里。
 */
async function loadParentCandidates(): Promise<QuestionBrief[]> {
  const result = await questionApi.list(props.subjectId, {
    root_only: true,
    sort: 'updated_desc',
    page: 1,
    // 母题数量级通常远小于题目总量；200 条足够选，又不至于把弹窗拖慢。
    page_size: 200,
  })
  return (result.list ?? []).filter((q) => q.id !== props.questionId)
}

function parentLabel(q: QuestionBrief): string {
  const stem = (q.stem_text || '（无题干）').replace(/\s+/g, ' ').trim()
  return `#${q.id} ${stem.length > 40 ? `${stem.slice(0, 40)}…` : stem}`
}

/** 知识点是树，下拉框需要扁平列表 + 缩进表示层级。 */
function flatten(nodes: KnowledgePoint[], depth = 0): KnowledgePoint[] {
  const out: KnowledgePoint[] = []
  for (const node of nodes) {
    out.push({ ...node, name: `${'　'.repeat(depth)}${node.name}` })
    if (node.children?.length) out.push(...flatten(node.children, depth + 1))
  }
  return out
}

function reset() {
  form.question_type = 'single_choice'
  form.difficulty = 3
  form.visibility = 'public'
  form.parent_id = props.presumeParentId ?? null
  form.stem = emptyDoc()
  form.options = [
    { label: 'A', text: '' },
    { label: 'B', text: '' },
    { label: 'C', text: '' },
    { label: 'D', text: '' },
  ]
  form.correctLabels = []
  form.boolAnswer = true
  form.blanks = ['']
  form.reference = emptyDoc()
  form.analysis = null
  form.tag_ids = []
  form.knowledge_point_ids = []
  form.change_note = ''
  form.answerPending = false
  originalStatus.value = null
}

async function loadQuestion(id: number) {
  loading.value = true
  try {
    const detail = await questionApi.get(id)
    form.question_type = detail.question_type
    form.difficulty = detail.difficulty
    form.visibility = detail.visibility
    form.parent_id = detail.parent_id
    form.stem = detail.stem as RichDoc
    form.analysis = (detail.analysis as RichDoc | null) ?? null
    form.tag_ids = detail.tags.map((t) => t.id)
    form.knowledge_point_ids = detail.knowledge_points.map((k) => k.id)
    originalStatus.value = detail.status

    // 选项：把富文本还原成 `$...$` 纯文本，保证"读出来再写回去"不丢公式。
    form.options = detail.options.length
      ? detail.options.map((o) => ({ label: o.label, text: richDocToPlain(o.content) }))
      : [
          { label: 'A', text: '' },
          { label: 'B', text: '' },
          { label: 'C', text: '' },
          { label: 'D', text: '' },
        ]

    // 答案：后端存的是选项 id，界面显示的是标签，需要一次映射。
    const labelOf = new Map(detail.options.map((o) => [o.id, o.label]))
    switch (detail.answer.kind) {
      case 'pending':
        form.answerPending = true
        break
      case 'single':
      case 'multiple':
        form.correctLabels = (detail.answer.correct ?? [])
          .map((id) => labelOf.get(id))
          .filter((v): v is string => !!v)
        break
      case 'boolean':
        form.boolAnswer = detail.answer.bool_answer ?? true
        break
      case 'blanks':
        form.blanks = (detail.answer.blanks ?? []).map((b) =>
          (b.accepts ?? [])
            .map((group) => group.map((item) => richDocToPlain(item)).join(' '))
            .join(' 或 '),
        )
        if (form.blanks.length === 0) form.blanks = ['']
        break
      case 'reference':
        form.reference = (detail.answer.reference as RichDoc | null) ?? emptyDoc()
        break
      default:
        break
    }
  } finally {
    loading.value = false
  }
}

/** 题型切换时整理答案结构：切换后原来的答案多半已经不合理了。 */
watch(
  () => form.question_type,
  (type, previous) => {
    if (previous === undefined || type === previous) return
    form.correctLabels = []
    if (type === 'fill_blank' && form.blanks.length === 0) form.blanks = ['']
  },
)

function addOption() {
  const label = String.fromCharCode(65 + form.options.length)
  if (form.options.length >= 8) {
    ElMessage.warning('最多支持 8 个选项')
    return
  }
  form.options.push({ label, text: '' })
}

function removeOption(index: number) {
  if (form.options.length <= 2) {
    ElMessage.warning('选择题至少需要两个选项')
    return
  }
  const removed = form.options[index]?.label
  form.options.splice(index, 1)
  // 重新编排标签，避免出现 A、C、D 这种空洞。
  form.options.forEach((option, i) => {
    option.label = String.fromCharCode(65 + i)
  })
  form.correctLabels = form.correctLabels.filter((label) => label !== removed)
}

function addBlank() {
  form.blanks.push('')
}

function removeBlank(index: number) {
  if (form.blanks.length <= 1) {
    ElMessage.warning('填空题至少需要一个空')
    return
  }
  form.blanks.splice(index, 1)
}

/** 组装答案结构。选择/判断以**标签**提交，由后端解析成选项 id。 */
function buildAnswer(): AnswerSpec {
  // 仍是「待补充答案」时原样交还，别按题型编一个空壳：空的 single
  // 既过不了后端校验，也会被界面显示成"没识别出来"。
  if (form.answerPending) return { kind: 'pending' }

  switch (form.question_type) {
    case 'single_choice':
    case 'multiple_choice':
      return {
        kind: isSingle.value ? 'single' : 'multiple',
        correct: [...form.correctLabels],
      }
    case 'true_false':
      return { kind: 'boolean', bool_answer: form.boolAnswer }
    case 'fill_blank':
      return {
        kind: 'blanks',
        blanks: form.blanks.map((text, index) => ({
          id: `blk_${index + 1}`,
          // 同一个空内用「或」分隔多个可接受答案；每个答案是一个"与"组（这里恒为单元素）。
          accepts: text
            .split('或')
            .map((item) => item.trim())
            .filter(Boolean)
            .map((item) => [plainToRichDoc(item)]),
        })),
      }
    case 'essay':
      return { kind: 'reference', reference: form.reference }
    default:
      return { kind: 'reference', reference: emptyDoc() }
  }
}

async function onSubmit() {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  // 提交前做几项本地校验：把明显的问题挡在请求之前，
  // 用户能立刻看到原因，而不是等一次往返才被后端拒绝。
  // 答案标着"稍后补充"时不查答案：这一轮改的是别的字段。
  if (isChoice.value && !form.answerPending) {
    if (form.options.filter((o) => o.text.trim()).length < 2) {
      ElMessage.warning('选择题至少需要两个有内容的选项')
      return
    }
    if (form.correctLabels.length === 0) {
      ElMessage.warning('请选择正确答案')
      return
    }
    if (isSingle.value && form.correctLabels.length > 1) {
      ElMessage.warning('单选题只能有一个正确答案')
      return
    }
    // 多选题至少两个：与后端 domain 的校验保持一致。
    // 只有一个正确答案的"多选题"与单选没有区别，多半是题型选错了。
    if (!isSingle.value && form.correctLabels.length < 2) {
      ElMessage.warning('多选题至少需要两个正确答案；若只有一个请改用单选题')
      return
    }
  }

  const payload: QuestionCreatePayload = {
    question_type: form.question_type,
    stem: form.stem,
    difficulty: form.difficulty,
    visibility: form.visibility,
    parent_id: form.parent_id,
    answer: buildAnswer(),
    analysis: form.analysis,
    tag_ids: form.tag_ids,
    knowledge_point_ids: form.knowledge_point_ids,
  }
  if (isChoice.value) {
    payload.options = form.options
      .filter((o) => o.text.trim())
      .map((o) => ({ label: o.label, content: plainToRichDoc(o.text) }))
  }

  submitting.value = true
  try {
    let saved: QuestionDetail
    if (props.questionId) {
      // parent_id 在更新接口里三态：null=不动，数字=改挂，0=解除挂载。
      // 新建接口用 null 表达"独立题"，两边语义不同，不能共用一个值。
      const updatePayload: QuestionUpdatePayload = {
        ...payload,
        parent_id: form.parent_id === null ? 0 : form.parent_id,
      }
      if (form.change_note.trim()) updatePayload.change_note = form.change_note.trim()
      saved = await questionApi.update(props.questionId, updatePayload)
    } else {
      saved = await questionApi.create(props.subjectId, payload)
    }
    ElMessage.success(props.questionId ? '已保存' : '已创建')
    emit('saved', saved)
    visible.value = false
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="questionId ? '编辑题目' : '新建题目'"
    width="860px"
    top="5vh"
    destroy-on-close
  >
    <el-form ref="formRef" v-loading="loading" :model="form" :rules="rules" label-width="80px">
      <el-row :gutter="12">
        <el-col :span="8">
          <el-form-item label="题型" prop="question_type">
            <el-select v-model="form.question_type" class="w-full">
              <el-option label="单选题" value="single_choice" />
              <el-option label="多选题" value="multiple_choice" />
              <el-option label="填空题" value="fill_blank" />
              <el-option label="判断题" value="true_false" />
              <el-option label="解答题" value="essay" />
            </el-select>
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="难度">
            <el-rate v-model="form.difficulty" :max="5" />
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="可见性">
            <el-radio-group v-model="form.visibility">
              <el-radio value="public">公开</el-radio>
              <el-radio value="private">私有</el-radio>
            </el-radio-group>
          </el-form-item>
        </el-col>
      </el-row>

      <el-form-item label="母题">
        <el-select
          v-model="form.parent_id"
          placeholder="不选则为独立题"
          clearable
          filterable
          class="w-full"
        >
          <el-option
            v-for="q in parentCandidates"
            :key="q.id"
            :label="parentLabel(q)"
            :value="q.id"
          />
        </el-select>
        <div class="w-full text-xs text-gray-400">
          母题/小问（材料题）结构：母题放共用材料，小问挂在它下面。清空即可解除挂载。
        </div>
      </el-form-item>

      <el-alert
        v-if="editsPublished"
        type="warning"
        :closable="false"
        show-icon
        class="mb-3"
        title="这道题已发布"
      >
        <div class="text-xs">
          保存会直接改线上题面，不会重新走审核。改动会让引用它的试卷提示「题目已被改动」，
          并写入审核记录。建议在下面说明改动原因。
        </div>
      </el-alert>

      <el-form-item v-if="editsPublished" label="变更说明">
        <el-input
          v-model="form.change_note"
          placeholder="例如：修正上标笔误"
          maxlength="255"
          clearable
        />
      </el-form-item>

      <el-form-item label="题干" required>
        <RichTextEditor v-model="form.stem" placeholder="输入题干，公式用工具栏插入" />
      </el-form-item>

      <!-- 选项 -->
      <el-form-item v-if="isChoice" label="选项">
        <div class="w-full space-y-2">
          <div v-for="(option, index) in form.options" :key="index" class="flex items-center gap-2">
            <el-tag class="w-8 shrink-0 justify-center" type="info">{{ option.label }}</el-tag>
            <el-input
              v-model="option.text"
              placeholder="选项内容，公式写 $x^2$"
              class="flex-1"
            />
            <el-button text type="danger" :disabled="form.options.length <= 2" @click="removeOption(index)">
              删除
            </el-button>
          </div>
          <el-button text type="primary" :disabled="form.options.length >= 8" @click="addOption">
            + 添加选项
          </el-button>
        </div>
      </el-form-item>

      <!-- 答案：按题型给出对应的录入方式 -->
      <el-form-item label="正确答案" :required="!form.answerPending">
        <div v-if="form.answerPending" class="w-full">
          <el-alert type="info" :closable="false" show-icon>
            <template #title>这道题还没有答案，保存后会继续显示为「待补充答案」。</template>
          </el-alert>
          <el-button text type="primary" size="small" class="mt-1" @click="form.answerPending = false">
            现在填写答案
          </el-button>
        </div>

        <template v-else>
          <el-radio-group v-if="isSingle" v-model="form.correctLabels[0]">
            <el-radio v-for="option in form.options" :key="option.label" :value="option.label">
              {{ option.label }}
            </el-radio>
          </el-radio-group>

          <el-checkbox-group
            v-else-if="form.question_type === 'multiple_choice'"
            v-model="form.correctLabels"
          >
            <el-checkbox v-for="option in form.options" :key="option.label" :value="option.label">
              {{ option.label }}
            </el-checkbox>
          </el-checkbox-group>

          <el-radio-group v-else-if="form.question_type === 'true_false'" v-model="form.boolAnswer">
            <el-radio :value="true">正确</el-radio>
            <el-radio :value="false">错误</el-radio>
          </el-radio-group>

          <div v-else-if="form.question_type === 'fill_blank'" class="w-full space-y-2">
            <div v-for="(_, index) in form.blanks" :key="index" class="flex items-center gap-2">
              <el-tag class="w-14 shrink-0 justify-center" type="info">第 {{ index + 1 }} 空</el-tag>
              <el-input
                v-model="form.blanks[index]"
                placeholder="多个可接受答案用「或」分隔，如：3 或 三"
                class="flex-1"
              />
              <el-button
                text
                type="danger"
                :disabled="form.blanks.length <= 1"
                @click="removeBlank(index)"
              >
                删除
              </el-button>
            </div>
            <el-button text type="primary" @click="addBlank">+ 添加空位</el-button>
            <div class="text-xs text-gray-400">
              提示：题干里用工具栏的「填空位」按钮插入空位，数量需与答案个数一致。
            </div>
          </div>

          <RichTextEditor
            v-else
            v-model="form.reference"
            placeholder="参考答案"
            min-height="100px"
          />
        </template>
      </el-form-item>

      <el-form-item label="解析">
        <RichTextEditor v-model="form.analysis" placeholder="解析（选填）" min-height="100px" />
      </el-form-item>

      <el-form-item label="知识点">
        <el-select
          v-model="form.knowledge_point_ids"
          multiple
          filterable
          clearable
          placeholder="可多选"
          class="w-full"
        >
          <el-option
            v-for="kp in knowledgePoints"
            :key="kp.id"
            :label="kp.name"
            :value="kp.id"
          />
        </el-select>
      </el-form-item>

      <el-form-item label="标签">
        <el-select v-model="form.tag_ids" multiple filterable clearable placeholder="可多选" class="w-full">
          <el-option v-for="tag in tags" :key="tag.id" :label="tag.name" :value="tag.id" />
        </el-select>
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="onSubmit">保存</el-button>
    </template>
  </el-dialog>
</template>
