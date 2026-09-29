<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { importApi, uploadApi, type CommitResult, type Draft } from '@/api/ai'
import { knowledgePointApi, tagApi } from '@/api'
import type { KnowledgePoint, QuestionStatus, Tag } from '@/api/types'
import RichContent from '@/components/RichContent.vue'
import { useSubjectStore } from '@/stores/subject'

/**
 * 智能录题。
 *
 * 流程刻意分成两步：**抽取**不写任何数据，只产出草稿；
 * 用户在草稿上逐题核对、修改、勾选后再**入库**。
 * 让模型直接写库看起来更"自动"，但录题是低成本高风险的环节——
 * 错一道题的代价远高于多点一次按钮。
 */
const router = useRouter()
const subject = useSubjectStore()

const tab = ref<'text' | 'image'>('text')
const loading = ref(false)
const committing = ref(false)

const form = reactive({
  content: '',
  mode: 'extract' as 'extract' | 'solve',
  solve_for_content: '',
  images: [] as string[],
})

const uploading = ref(false)

const drafts = ref<Draft[]>([])
const selected = ref<Set<number>>(new Set())
const extractMeta = ref<{ model: string; elapsed_ms: number; task_id: number } | null>(null)
const commitResult = ref<CommitResult | null>(null)

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

onMounted(async () => {
  await loadOptions()
  loaded.value = true
})

const loaded = ref(false)

watch(
  () => subject.currentId,
  async () => {
    if (!loaded.value) return
    drafts.value = []
    selected.value = new Set()
    commitResult.value = null
    await loadOptions()
  },
)

async function loadOptions() {
  if (!subject.currentId) return
  try {
    const [tree, tagList] = await Promise.all([
      knowledgePointApi.tree(subject.currentId),
      tagApi.list(subject.currentId),
    ])
    knowledgePoints.value = flatten(tree)
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

// ---- 图片上传 ----

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

function removeImage(index: number) {
  form.images.splice(index, 1)
}

// ---- 抽取 ----

async function extract() {
  if (!subject.currentId) {
    ElMessage.warning('请先选择学科')
    return
  }
  if (tab.value === 'text' && !form.content.trim()) {
    ElMessage.warning('请粘贴题目内容')
    return
  }
  if (tab.value === 'image' && form.images.length === 0) {
    ElMessage.warning('请先上传题目图片')
    return
  }
  if (form.mode === 'solve' && !form.solve_for_content.trim()) {
    ElMessage.warning('“只求解”模式需要提供原题内容')
    return
  }

  loading.value = true
  commitResult.value = null
  try {
    const result = await importApi.extract(subject.currentId, {
      content: tab.value === 'text' ? form.content : '',
      images: tab.value === 'image' ? form.images : undefined,
      mode: form.mode,
      solve_for_content: form.mode === 'solve' ? form.solve_for_content : undefined,
    })
    drafts.value = result.drafts ?? []
    // 默认全选：绝大多数情况下用户是想全部入库的，逐题勾选是负担。
    selected.value = new Set(drafts.value.map((_, i) => i))
    extractMeta.value = { model: result.model, elapsed_ms: result.elapsed_ms, task_id: result.task_id }
    ElMessage.success(`识别出 ${result.total} 道题`)
  } finally {
    loading.value = false
  }
}

function toggleSelect(index: number) {
  const next = new Set(selected.value)
  if (next.has(index)) next.delete(index)
  else next.add(index)
  selected.value = next
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

function removeDraft(index: number) {
  drafts.value.splice(index, 1)
  const next = new Set<number>()
  for (const i of selected.value) {
    if (i < index) next.add(i)
    else if (i > index) next.add(i - 1)
  }
  selected.value = next
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
    const result = await importApi.commit(subject.currentId, {
      drafts: selectedDrafts.value,
      default_tag_ids: defaults.default_tag_ids,
      default_knowledge_point_ids: defaults.default_knowledge_point_ids,
      status: defaults.status,
    })
    commitResult.value = result

    // 部分成功是常态：某道题校验不过不该让整批失败。
    // 这里如实告知两边各有多少，并把成功的那部分从草稿里摘掉，
    // 剩下的留给你修——比整批重来省事得多。
    if (result.failed_count > 0) {
      const failedSet = new Set(result.failures.map((f) => f.index))
      const remaining: Draft[] = []
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

function answerText(draft: Draft): string {
  const answer = draft.answer
  if (!answer) return ''
  switch (answer.kind) {
    case 'single':
    case 'multiple':
      return (answer.correct ?? [])
        .map((id) => draft.options.find((o) => o.id === id)?.label ?? id)
        .join('、')
    case 'boolean':
      return answer.bool_answer ? '正确' : '错误'
    case 'blanks':
      return (answer.blanks ?? []).map((b) => b.accepts.length).join(' / ') + ' 个空'
    case 'reference':
      return '见参考答案'
    default:
      return answer.kind
  }
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
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">① 提供题目内容</span>
          <el-radio-group v-model="form.mode" size="small">
            <el-radio-button value="extract">完整抽取</el-radio-button>
            <el-radio-button value="solve">只求解</el-radio-button>
          </el-radio-group>
        </div>
      </template>

      <el-tabs v-model="tab">
        <el-tab-pane label="粘贴文本" name="text">
          <el-input
            v-model="form.content"
            type="textarea"
            :rows="10"
            placeholder="直接粘贴试卷内容。支持 Markdown、编号列表、选项与答案混排，公式可写 LaTeX（$x^2$ 或 \[...\]）。"
            maxlength="200000"
          />
          <div class="mt-1 text-right text-xs text-gray-400">
            {{ form.content.length }} / 200000
          </div>
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
            适用于拍照或扫描的试卷。需要配置视觉模型，否则请改用文本模式。
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

      <div class="mt-3 flex items-center gap-2">
        <el-button type="primary" :loading="loading" :disabled="!canEdit || !subject.currentId" @click="extract">
          <el-icon class="mr-1"><MagicStick /></el-icon>
          开始识别
        </el-button>
        <span v-if="extractMeta" class="text-xs text-gray-400">
          模型 {{ extractMeta.model }} · 耗时 {{ extractMeta.elapsed_ms }}ms · 任务 #{{ extractMeta.task_id }}
        </span>
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

      <div class="space-y-2">
        <div
          v-for="(draft, index) in drafts"
          :key="draft.temp_id || index"
          class="rounded border p-3"
          :class="selected.has(index) ? 'border-blue-300 bg-blue-50/40' : 'border-gray-200'"
        >
          <div class="flex items-start gap-2">
            <el-checkbox
              :model-value="selected.has(index)"
              class="mt-0.5"
              @change="toggleSelect(index)"
            />

            <div class="min-w-0 flex-1">
              <div class="mb-1 flex flex-wrap items-center gap-2">
                <span class="text-sm font-medium">第 {{ index + 1 }} 题</span>
                <el-tag size="small">{{ draft.type_label || draft.question_type }}</el-tag>
                <span class="text-amber-500">{{ '★'.repeat(draft.difficulty || 0) }}</span>
                <el-tag v-if="draft.source_number" size="small" type="info" effect="plain">
                  原卷第 {{ draft.source_number }} 题
                </el-tag>
              </div>

              <div class="rounded bg-white p-2">
                <RichContent :doc="draft.stem" />
                <div v-if="draft.options?.length" class="mt-1 space-y-0.5">
                  <div v-for="option in draft.options" :key="option.id" class="flex gap-1 text-sm">
                    <span class="font-medium">{{ option.label }}.</span>
                    <RichContent :doc="option.content" class="flex-1" />
                  </div>
                </div>
              </div>

              <div class="mt-1 text-xs">
                <span class="text-gray-500">答案：</span>
                <span class="text-green-700">{{ answerText(draft) || '未识别' }}</span>
              </div>

              <!-- 解析预览 -->
              <el-collapse v-if="draft.analysis" class="mt-1">
                <el-collapse-item title="查看解析">
                  <div class="rounded bg-gray-50 p-2 text-sm">
                    <RichContent :doc="draft.analysis" />
                  </div>
                </el-collapse-item>
              </el-collapse>

              <!-- 知识点建议：模型自述与向量检索分开呈现 -->
              <div class="mt-2 flex flex-wrap items-center gap-1 text-xs">
                <template v-if="draft.suggested_knowledge_points?.length">
                  <span class="text-gray-500">知识点候选：</span>
                  <el-tag
                    v-for="s in draft.suggested_knowledge_points"
                    :key="s.knowledge_point_id"
                    size="small"
                    type="success"
                    effect="plain"
                  >
                    {{ s.name }} · {{ (s.score * 100).toFixed(0) }}%
                  </el-tag>
                </template>
                <template v-if="draft.knowledge_points?.length">
                  <span class="ml-2 text-gray-500">模型提及：</span>
                  <el-tag v-for="name in draft.knowledge_points" :key="name" size="small" type="info" effect="plain">
                    {{ name }}
                  </el-tag>
                </template>
              </div>

              <!-- 提示 -->
              <el-alert
                v-if="draft.warnings?.length"
                type="warning"
                :closable="false"
                class="mt-2"
                :title="draft.warnings.join('；')"
              />
            </div>

            <el-button text size="small" type="danger" @click="removeDraft(index)">移除</el-button>
          </div>
        </div>
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
          <div class="mt-1 text-xs text-gray-400">
            建议先入库为草稿，逐题确认后再提交审核。
          </div>
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
        <span class="text-xs text-gray-400">
          只写入你勾选的题目。未匹配到的知识点名不会自动创建。
        </span>
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

      <el-table v-if="commitResult.failures.length" :data="commitResult.failures" size="small">
        <el-table-column prop="index" label="#" width="60" />
        <el-table-column prop="stem" label="题干" show-overflow-tooltip />
        <el-table-column prop="error" label="失败原因" min-width="240" />
      </el-table>

      <div v-if="commitResult.unresolved_names?.length" class="mt-2 text-xs text-gray-500">
        未匹配到的名称：{{ commitResult.unresolved_names.join('、') }}
      </div>
    </el-card>
  </div>
</template>
