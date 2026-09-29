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
  type: (route.query.status ? '' : '') as QuestionType | '',
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
    await Promise.all([loadOptions(), load()])
  },
)

async function loadOptions() {
  if (!subject.currentId) return
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

      <div
        v-if="moreFiltersVisible"
        class="mt-2 flex flex-wrap items-center gap-2 border-t border-gray-100 pt-2"
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

      <div v-if="selection.length" class="mt-2 flex flex-wrap items-center gap-2 border-t border-gray-100 pt-2">
        <span class="text-sm text-gray-500">已选 {{ selection.length }} 项</span>
        <template v-if="canEdit">
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
            <div class="cursor-pointer" @click="openDetail(row.id)">
              <div class="stem-clamp text-sm">
                <!-- 列表项只有纯文本投影（QuestionBrief 刻意不返回富文本），
                     公式在列表里显示为 LaTeX 源码，详情抽屉才做排版渲染。 -->
                <span v-if="row.stem_text">{{ row.stem_text }}</span>
                <span v-else class="text-gray-400">（无题干）</span>
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
              <el-icon class="ml-1 align-middle text-gray-400"><InfoFilled /></el-icon>
            </el-tooltip>
          </template>
          <template #default="{ row }">
            <span class="text-xs text-gray-500">{{ formatTime(row.updated_at) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="270" fixed="right">
          <template #default="{ row }">
            <el-button text size="small" @click="openDetail(row.id)">查看</el-button>

            <template v-if="canEdit">
              <!-- 不按状态收窄：服务端对已发布/待审的题同样允许编辑，
                   界面此前把按钮锁在 draft 上，是在替服务端撒一个它没撒的谎。
                   改动的后果（试卷提示题目已改动 + 审核记录）在弹窗里说明。 -->
              <el-button text size="small" @click="openEdit(row)">编辑</el-button>
              <el-button text size="small" @click="openAddChild(row)">小问</el-button>
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

            <el-button
              v-if="canReview && (row.status === 'published' || row.status === 'archived')"
              text
              size="small"
              type="warning"
              @click="toggleArchive(row)"
            >
              {{ row.status === 'archived' ? '取消归档' : '归档' }}
            </el-button>

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
            <el-tag size="small" :type="statusTag(detail.status)">{{ statusLabel(detail) }}</el-tag>
            <span class="text-amber-500">{{ '★'.repeat(detail.difficulty) }}</span>
            <el-tag v-if="detail.visibility === 'private'" size="small" type="warning">私有</el-tag>
            <span class="ml-auto text-xs text-gray-400">v{{ detail.content_version }} · r{{ detail.content_revision }}</span>
          </div>

          <!-- 母子题结构：母题可点回详情，小问就地列出并可点开。
               服务端早就把 children 一起返回了，此前界面上没有任何地方渲染它。 -->
          <div v-if="detail.parent_id" class="mb-2 rounded border border-amber-200 bg-amber-50 p-2 text-sm">
            <span class="mr-2 text-xs text-amber-700">子题</span>
            母题：
            <el-link type="primary" @click="jumpTo(detail.parent_id)">#{{ detail.parent_id }}</el-link>
          </div>

          <div v-if="detail.children.length" class="mb-3 rounded border border-blue-200 bg-blue-50 p-2">
            <div class="mb-1 text-xs text-blue-700">母题 · 含 {{ detail.children.length }} 个小问</div>
            <div class="space-y-1">
              <div
                v-for="child in detail.children"
                :key="child.id"
                class="flex cursor-pointer items-center gap-2 text-sm hover:underline"
                @click="jumpTo(child.id)"
              >
                <el-tag size="small" :type="statusTag(child.status)">{{ child.status_label }}</el-tag>
                <span class="flex-1 truncate">{{ child.stem_text || '（无题干）' }}</span>
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

          <!-- 审核记录：端点与审核日志表早就存在，界面上却一个入口都没有。
               审核透明度为零的表现就是"这道题被谁驳回过、为什么"无从查证。 -->
          <div class="mb-1 text-xs text-gray-500">审核记录</div>
          <div v-loading="reviewLogsLoading" class="mb-4 rounded border border-gray-200 p-3">
            <div v-if="!reviewLogs.length && !reviewLogsLoading" class="text-sm text-gray-400">
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
                <span class="text-gray-500">{{ log.reviewer || '未知' }}</span>
                <span class="text-xs text-gray-400">{{ formatTime(log.created_at) }}</span>
                <span v-if="log.comment" class="w-full text-xs text-gray-600">「{{ log.comment }}」</span>
              </div>
            </div>
          </div>

          <div class="flex flex-wrap gap-2 border-t border-gray-100 pt-3">
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
