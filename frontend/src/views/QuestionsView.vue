<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { knowledgePointApi, questionApi, tagApi } from '@/api'
import type {
  KnowledgePoint,
  QuestionBrief,
  QuestionDetail,
  QuestionQuery,
  QuestionSort,
  QuestionStatus,
  QuestionType,
  Tag,
} from '@/api/types'
import RichContent from '@/components/RichContent.vue'
import QuestionEditDialog from '@/components/QuestionEditDialog.vue'
import { useSubjectStore } from '@/stores/subject'

const route = useRoute()
const router = useRouter()
const subject = useSubjectStore()

const loading = ref(false)
const rows = ref<QuestionBrief[]>([])
const total = ref(0)
const selection = ref<QuestionBrief[]>([])

const knowledgePoints = ref<KnowledgePoint[]>([])
const tags = ref<Tag[]>([])

const editVisible = ref(false)
const editingId = ref<number | null>(null)

const detailVisible = ref(false)
const detail = ref<QuestionDetail | null>(null)
const detailLoading = ref(false)

const filters = reactive<QuestionQuery>({
  keyword: '',
  type: (route.query.status ? '' : '') as QuestionType | '',
  status: (route.query.status as QuestionStatus) || '',
  difficulty: null,
  knowledge_point_id: null,
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

onMounted(async () => {
  await Promise.all([loadOptions(), load()])
})

watch(
  () => subject.currentId,
  async () => {
    filters.page = 1
    await Promise.all([loadOptions(), load()])
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
    // 选项加载失败不影响列表本身
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
  filters.knowledge_point_id = null
  filters.tag_ids = []
  filters.sort = 'updated_desc'
  reload()
}

function openCreate() {
  editingId.value = null
  editVisible.value = true
}

function openEdit(row: QuestionBrief) {
  editingId.value = row.id
  editVisible.value = true
}

async function openDetail(row: QuestionBrief) {
  detailVisible.value = true
  detailLoading.value = true
  detail.value = null
  try {
    detail.value = await questionApi.get(row.id)
  } finally {
    detailLoading.value = false
  }
}

async function onSaved() {
  await load()
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

function pad(n: number): string {
  return String(n).padStart(2, '0')
}
</script>

<template>
  <div class="space-y-3">
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
          v-model="filters.knowledge_point_id"
          placeholder="知识点"
          clearable
          filterable
          class="w-40"
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
          class="w-40"
          @change="reload"
        >
          <el-option v-for="tag in tags" :key="tag.id" :label="tag.name" :value="tag.id" />
        </el-select>

        <el-button @click="resetFilters">重置</el-button>

        <div class="ml-auto flex items-center gap-2">
          <el-button text size="small" @click="router.push({ name: 'recycle-bin' })">
            <el-icon class="mr-1"><Delete /></el-icon>回收站
          </el-button>
          <el-button
            v-if="canEdit"
            type="primary"
            :disabled="!subject.currentId"
            @click="openCreate"
          >
            <el-icon class="mr-1"><Plus /></el-icon>新建题目
          </el-button>
        </div>
      </div>

      <div v-if="selection.length" class="mt-2 flex items-center gap-2 border-t border-gray-100 pt-2">
        <span class="text-sm text-gray-500">已选 {{ selection.length }} 项</span>
        <el-button v-if="canEdit" size="small" type="danger" plain @click="removeSelected">
          批量删除
        </el-button>
      </div>
    </el-card>

    <!-- 列表 -->
    <el-card shadow="never">
      <el-table
        v-loading="loading"
        :data="rows"
        row-key="id"
        @selection-change="(val: QuestionBrief[]) => (selection = val)"
      >
        <el-table-column type="selection" width="42" />
        <el-table-column label="题干" min-width="360">
          <template #default="{ row }">
            <div class="cursor-pointer" @click="openDetail(row)">
              <div class="stem-clamp text-sm">
                <!-- 优先渲染结构化题干；stem_text 只是纯文本兜底。 -->
                <RichContent v-if="row.stem_text" :doc="{ type: 'doc', content: [] }" compact />
                <span v-if="row.stem_text">{{ row.stem_text }}</span>
                <span v-else class="text-gray-400">（无题干）</span>
              </div>
              <div class="mt-1 flex flex-wrap items-center gap-1">
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
                <span v-if="row.child_count" class="text-xs text-gray-400">
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
            <span class="text-amber-500">{{ '★'.repeat(row.difficulty) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="statusTag(row.status)">{{ row.status_label }}</el-tag>
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
              <el-icon class="ml-1 align-middle text-gray-400"><InfoFilled /></el-icon>
            </el-tooltip>
          </template>
          <template #default="{ row }">
            <span class="text-xs text-gray-500">{{ formatTime(row.updated_at) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button text size="small" @click="openDetail(row)">查看</el-button>

            <template v-if="canEdit">
              <el-button v-if="row.status === 'draft'" text size="small" @click="openEdit(row)">
                编辑
              </el-button>
              <el-button
                v-if="row.status === 'draft'"
                text
                size="small"
                type="primary"
                @click="submitForReview(row)"
              >
                提交审核
              </el-button>
            </template>

            <template v-if="canReview && row.status === 'pending'">
              <el-button text size="small" type="success" @click="review(row, 'approve')">通过</el-button>
              <el-button text size="small" type="danger" @click="review(row, 'reject')">驳回</el-button>
            </template>

            <el-button v-if="canEdit" text size="small" type="danger" @click="removeOne(row)">
              删除
            </el-button>
          </template>
        </el-table-column>

        <template #empty>
          <el-empty :description="subject.currentId ? '还没有题目' : '请先选择学科'" />
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
            <el-tag size="small" :type="statusTag(detail.status)">{{ detail.status_label }}</el-tag>
            <span class="text-amber-500">{{ '★'.repeat(detail.difficulty) }}</span>
            <el-tag v-if="detail.visibility === 'private'" size="small" type="warning">私有</el-tag>
            <span class="ml-auto text-xs text-gray-400">v{{ detail.content_version }} · r{{ detail.content_revision }}</span>
          </div>

          <div class="mb-1 text-xs text-gray-500">题干</div>
          <div class="mb-4 rounded border border-gray-200 p-3">
            <RichContent :doc="detail.stem" />
          </div>

          <template v-if="detail.options.length">
            <div class="mb-1 text-xs text-gray-500">选项</div>
            <div class="mb-4 space-y-1">
              <div
                v-for="option in detail.options"
                :key="option.id"
                class="flex gap-2 rounded border border-gray-200 p-2"
              >
                <span class="font-medium">{{ option.label }}.</span>
                <RichContent :doc="option.content" class="flex-1" />
              </div>
            </div>
          </template>

          <div class="mb-1 text-xs text-gray-500">答案</div>
          <div class="mb-4 rounded bg-green-50 p-3 text-sm">
            <template v-if="detail.answer.kind === 'single' || detail.answer.kind === 'multiple'">
              {{
                (detail.answer.correct ?? [])
                  .map((id) => detail?.options.find((o) => o.id === id)?.label ?? id)
                  .join('、')
              }}
            </template>
            <template v-else-if="detail.answer.kind === 'boolean'">
              {{ detail.answer.bool_answer ? '正确' : '错误' }}
            </template>
            <template v-else-if="detail.answer.kind === 'blanks'">
              <div v-for="(blank, index) in detail.answer.blanks ?? []" :key="blank.id">
                第 {{ index + 1 }} 空：
                <span v-for="(group, gi) in blank.accepts" :key="gi">
                  <RichContent v-for="(item, ii) in group" :key="ii" :doc="item" class="inline" />
                  <span v-if="gi < blank.accepts.length - 1" class="text-gray-400"> 或 </span>
                </span>
              </div>
            </template>
            <template v-else-if="detail.answer.kind === 'reference'">
              <RichContent :doc="detail.answer.reference" />
            </template>
            <template v-else>
              <span class="text-gray-500">未能识别答案结构</span>
            </template>
          </div>

          <template v-if="detail.analysis">
            <div class="mb-1 text-xs text-gray-500">解析</div>
            <div class="mb-4 rounded border border-gray-200 p-3">
              <RichContent :doc="detail.analysis" />
            </div>
          </template>

          <div class="flex flex-wrap gap-2 border-t border-gray-100 pt-3">
            <el-button v-if="canEdit && detail.status === 'draft'" type="primary" @click="openEdit(detail)">
              编辑
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
          </div>
        </template>
      </div>
    </el-drawer>

    <QuestionEditDialog
      v-if="subject.currentId"
      v-model="editVisible"
      :subject-id="subject.currentId"
      :question-id="editingId"
      @saved="onSaved"
    />
  </div>
</template>
