<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import type { ImportDraft } from '@/api/importTask'
import type { AnswerSpec, QuestionType, RichDoc } from '@/api/types'
import RichTextEditor from '@/components/editor/RichTextEditor.vue'
import { emptyDoc, plainToRichDoc, richDocToPlain } from '@/lib/richdoc'

/**
 * 草稿的原地修改。
 *
 * 以前草稿是只读的：识别错一道题，唯一的选择是把整批丢掉重来——
 * 几十道本来没问题的题陪葬。录题是低频高风险的活，人工核对是必经环节，
 * 所以这里必须能改。
 *
 * 交互与取值刻意照抄 QuestionEditDialog：选项用纯文本框（`$...$` 存公式），
 * 题干与解析用完整编辑器。同一套心智模型，切换页面不用重新学一遍。
 * 差别只在收尾——这里改的是草稿而不是数据库里的题，所以**不碰**母题、
 * 知识点与标签：那些是入库设置的事，改草稿时顺手一起改会让"我改了什么"变得说不清。
 */
const props = defineProps<{
  modelValue: boolean
  /** 为 null 表示没有正在编辑的草稿。 */
  draft: ImportDraft | null
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  /** 提交修改后的草稿（整体替换上层数组里的那一项）。 */
  save: [draft: ImportDraft]
}>()

const visible = computed({
  get: () => props.modelValue,
  set: (value) => emit('update:modelValue', value),
})

const QUESTION_TYPES: { value: QuestionType; label: string }[] = [
  { value: 'single_choice', label: '单选题' },
  { value: 'multiple_choice', label: '多选题' },
  { value: 'fill_blank', label: '填空题' },
  { value: 'true_false', label: '判断题' },
  { value: 'essay', label: '解答题' },
]

const TYPE_LABELS: Record<QuestionType, string> = {
  single_choice: '单选题',
  multiple_choice: '多选题',
  fill_blank: '填空题',
  true_false: '判断题',
  essay: '解答题',
}

interface OptionRow {
  label: string
  text: string
}

const form = reactive({
  question_type: 'single_choice' as QuestionType,
  difficulty: 3,
  stem: emptyDoc() as RichDoc,
  options: [] as OptionRow[],
  /** 选择/判断类答案，以**标签**（A/B/C）表达，与 QuestionEditDialog 一致。 */
  correctLabels: [] as string[],
  boolAnswer: true,
  blanks: [''] as string[],
  reference: emptyDoc() as RichDoc,
  analysis: null as RichDoc | null,
  /**
   * 这道题的答案仍然欠着（智能录题的「仅录题」模式）。
   *
   * 不单独给它一个入口的话，老师为了改一句题干就得先把答案编出来——
   * 于是「仅录题」录进来的题一道都改不了，模式也就白做了。
   */
  answerPending: false,
})

/** 原答案里引用了草稿中没有的选项 id。保存会丢掉它们，必须先说一声。 */
const danglingAnswers = ref<string[]>([])
/** 抽取给的题型不在可选范围里（理论上后端已归一化，这里只是兜底）。 */
const unknownType = ref('')
/** 这道题本来就是「待补充答案」的：只有这种题才给"稍后补充"这个开关。 */
const answerWasPending = ref(false)
/** 草稿原始的 选项标签 → 选项 id：标签是界面上的东西，答案要按 id 落回去。 */
const idLookup = ref<Map<string, string>>(new Map())

const isChoice = computed(
  () => form.question_type === 'single_choice' || form.question_type === 'multiple_choice',
)
const isSingle = computed(() => form.question_type === 'single_choice')

watch(
  () => props.modelValue,
  (open) => {
    if (open && props.draft) load(props.draft)
  },
)

/**
 * 题型切换后原来的答案多半已经不合理了（单选切多选会多出一个正确答案）。
 *
 * `flush: 'sync'` 是必须的：回填草稿时（load）会先赋题型、再赋答案，
 * 默认的 pre 刷新要等到下一个 tick 才跑，那时答案已经填好了，
 * 这一句会把刚恢复出来的正确答案又清空。
 */
watch(
  () => form.question_type,
  (type, previous) => {
    if (previous === undefined || type === previous) return
    form.correctLabels = []
    if (type === 'fill_blank' && form.blanks.length === 0) form.blanks = ['']
  },
  { flush: 'sync' },
)

function load(draft: ImportDraft) {
  unknownType.value = ''
  danglingAnswers.value = []

  const matched = QUESTION_TYPES.find((t) => t.value === draft.question_type)
  if (matched) {
    form.question_type = matched.value
  } else {
    // 不认识的题型没法在 <select> 里显示，落到解答题（最宽松的容器），
    // 同时把原值摆出来，免得用户以为是自己改的。
    form.question_type = 'essay'
    unknownType.value = draft.question_type
  }
  form.difficulty = draft.difficulty || 3
  form.stem = (draft.stem as RichDoc) ?? emptyDoc()
  form.analysis = draft.analysis ?? null
  form.options = draft.options?.length
    ? draft.options.map((o) => ({ label: o.label, text: richDocToPlain(o.content) }))
    : [
        { label: 'A', text: '' },
        { label: 'B', text: '' },
        { label: 'C', text: '' },
        { label: 'D', text: '' },
      ]
  idLookup.value = new Map((draft.options ?? []).map((o) => [o.label, o.id]))

  const labelOf = new Map((draft.options ?? []).map((o) => [o.id, o.label]))
  form.correctLabels = []
  form.boolAnswer = true
  form.blanks = ['']
  form.reference = emptyDoc()
  form.answerPending = false
  answerWasPending.value = draft.answer?.kind === 'pending'

  switch (draft.answer?.kind) {
    case 'pending':
      form.answerPending = true
      break
    case 'single':
    case 'multiple': {
      const labels: string[] = []
      for (const id of draft.answer.correct ?? []) {
        const label = labelOf.get(id)
        if (label) labels.push(label)
        else danglingAnswers.value.push(id)
      }
      form.correctLabels = labels
      break
    }
    case 'boolean':
      form.boolAnswer = draft.answer.bool_answer ?? true
      break
    case 'blanks':
      form.blanks = (draft.answer.blanks ?? []).map((b) =>
        (b.accepts ?? []).map((group) => group.map((item) => richDocToPlain(item)).join(' ')).join(' 或 '),
      )
      if (form.blanks.length === 0) form.blanks = ['']
      break
    case 'reference':
      form.reference = (draft.answer.reference as RichDoc | null) ?? emptyDoc()
      break
    default:
      break
  }
}

function addOption() {
  if (form.options.length >= 8) {
    ElMessage.warning('最多支持 8 个选项')
    return
  }
  form.options.push({
    label: String.fromCharCode(65 + form.options.length),
    text: '',
  })
}

function removeOption(index: number) {
  if (form.options.length <= 2) {
    ElMessage.warning('选择题至少需要两个选项')
    return
  }
  const removed = form.options[index]?.label
  form.options.splice(index, 1)
  // 重排标签，避免出现 A、C、D 这种空洞。
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

/** 标签 → 选项 id。选择题的答案必须落回 id，标签只是界面上的东西。 */
function buildAnswer(kept: { id: string; label: string }[]): AnswerSpec {
  // 明确选择"稍后补充"时原样交还 pending，不按题型编一个空壳答案：
  // 空的 single 既过不了后端校验，也会被界面显示成"没识别出来"。
  if (form.answerPending) return { kind: 'pending' }

  switch (form.question_type) {
    case 'single_choice':
    case 'multiple_choice': {
      const idOf = new Map(kept.map((o) => [o.label, o.id]))
      return {
        kind: isSingle.value ? 'single' : 'multiple',
        correct: form.correctLabels
          .map((label) => idOf.get(label))
          .filter((id): id is string => !!id),
      }
    }
    case 'true_false':
      return { kind: 'boolean', bool_answer: form.boolAnswer }
    case 'fill_blank':
      return {
        kind: 'blanks',
        blanks: form.blanks.map((text, index) => ({
          id: `blk_${index + 1}`,
          // 同一个空内用「或」分隔多个可接受答案。
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

function onSubmit() {
  const draft = props.draft
  if (!draft) return

  if (richDocToPlain(form.stem).trim().length === 0) {
    ElMessage.warning('题干不能为空')
    return
  }
  // 答案待补时不查答案：这一轮改的是题干、选项或知识点。
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
    // 多选题至少两个：只有一个正确答案的"多选题"与单选没有区别，多半是题型选错了。
    if (!isSingle.value && form.correctLabels.length < 2) {
      ElMessage.warning('多选题至少需要两个正确答案；若只有一个请改用单选题')
      return
    }
  }

  // 非选择题不带选项：抽取时猜错的题型经常残留着一堆选项，
  // 原样提交会被后端的题型/答案自洽校验挡下来，白白让用户再跑一次。
  const kept = isChoice.value ? form.options.filter((o) => o.text.trim()) : []
  // 选项 id 必须稳定：答案里按 id 引用它们，id 一变答案就对不上了。
  const options = kept.map((o) => ({
    id: idLookup.value.get(o.label) ?? `opt_${o.label}`,
    label: o.label,
    content: plainToRichDoc(o.text),
  }))
  emit('save', {
    ...draft,
    question_type: form.question_type,
    type_label: TYPE_LABELS[form.question_type],
    difficulty: form.difficulty,
    stem: form.stem as RichDoc,
    /**
     * 清空 markdown 原文，这是本次修改里最容易被忽略、后果最严重的一行。
     *
     * 后端入库时优先按 `stem_markdown` 重新解析题干（因为那是模型吐出来的原文，
     * 比富文本结构更可信）。只要它非空，用户在编辑器里改的题干就会被**静默覆盖**——
     * 界面上明明改成功了，入库后还是旧题面。因此改过之后必须把原文清掉，
     * 让后端直接采用这里提交的 `stem`。
     */
    stem_markdown: '',
    stem_text: richDocToPlain(form.stem),
    options,
    answer: buildAnswer(options),
    analysis: form.analysis,
  })
  visible.value = false
}
</script>

<template>
  <el-dialog v-model="visible" title="修改草稿" width="760px" top="6vh" destroy-on-close>
    <el-alert
      v-if="unknownType"
      type="warning"
      :closable="false"
      show-icon
      class="mb-3"
      :title="`抽取给出的题型「${unknownType}」不在可选范围内，已按解答题处理，请确认。`"
    />
    <el-alert
      v-if="danglingAnswers.length"
      type="warning"
      :closable="false"
      show-icon
      class="mb-3"
      :title="`原答案引用了草稿中不存在的选项（${danglingAnswers.join('、')}），请重新选择正确答案。`"
    />

    <el-form label-width="80px">
      <el-row :gutter="12">
        <el-col :span="12">
          <el-form-item label="题型">
            <el-select v-model="form.question_type" class="w-full">
              <el-option v-for="t in QUESTION_TYPES" :key="t.value" :label="t.label" :value="t.value" />
            </el-select>
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="难度">
            <el-rate v-model="form.difficulty" :max="5" />
          </el-form-item>
        </el-col>
      </el-row>

      <el-form-item label="题干" required>
        <RichTextEditor v-model="form.stem" placeholder="输入题干，公式用 $...$" />
      </el-form-item>

      <el-form-item v-if="isChoice" label="选项">
        <div class="w-full space-y-2">
          <div v-for="(option, index) in form.options" :key="index" class="flex items-center gap-2">
            <el-tag class="w-8 shrink-0 justify-center" type="info">{{ option.label }}</el-tag>
            <el-input v-model="option.text" placeholder="选项内容，公式写 $x^2$" class="flex-1" />
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
        <el-checkbox v-if="answerWasPending" v-model="form.answerPending" class="mb-2">
          答案稍后补充（由老师在题库里填写）
        </el-checkbox>

        <div v-if="form.answerPending" class="text-xs text-ink-subtle">
          保持为空即可保存：入库后这道题显示为「待补充答案」，
          在题库里随时能补上。
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
              <el-button text type="danger" :disabled="form.blanks.length <= 1" @click="removeBlank(index)">
                删除
              </el-button>
            </div>
            <el-button text type="primary" @click="addBlank">+ 添加空位</el-button>
            <div class="text-xs text-ink-subtle">
              提示：题干里用工具栏的「填空位」按钮插入空位，数量需与答案个数一致。
            </div>
          </div>

          <RichTextEditor v-else v-model="form.reference" placeholder="参考答案" min-height="100px" />
        </template>
      </el-form-item>

      <el-form-item label="解析">
        <RichTextEditor v-model="form.analysis" placeholder="解析（选填）" min-height="100px" />
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" @click="onSubmit">应用修改</el-button>
    </template>
  </el-dialog>
</template>
