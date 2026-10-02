<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  importTaskApi,
  type ImportQueueStatus,
  type ImportTask,
  type ImportTaskStatus,
} from '@/api/importTask'
import type { QuestionBrief } from '@/api/types'
import RichContent from '@/components/RichContent.vue'
import PageHeader from '@/components/ui/PageHeader.vue'
import TableSkeleton from '@/components/ui/TableSkeleton.vue'
import { useSubjectStore } from '@/stores/subject'

/**
 * 导入历史。
 *
 * 回答三个问题：**我导入过什么**、**哪一次出了问题**、**那次导出来的题在哪**。
 * 抽取跑在后端的进程内 worker 上（并发 1），这一页刻意不假装自己是一个
 * 任务看板：队列区只如实展示计数与位置，并明确告诉用户
 * "排队中不等于马上会开始"。
 *
 * 状态相关的判断（能不能重试、能不能放弃）全部由后端随任务一起下发，
 * 前端不复制状态机——两份规则必然漂移，漂移的表象是"按钮能点，点了报 409"。
 */
const router = useRouter()
const subject = useSubjectStore()

const loading = ref(false)
const rows = ref<ImportTask[]>([])
const total = ref(0)
const queue = ref<ImportQueueStatus | null>(null)

const query = reactive({
  status: '' as ImportTaskStatus | '',
  page: 1,
  page_size: 20,
})

/** 能改数据的操作沿用录题的权限：能录题就能放弃/重试/删除自己的导入记录。 */
const canOperate = computed(() => subject.can('edit_question'))

const STAT_TILES = [
  { key: 'pending', label: '等待处理', type: 'warning' },
  { key: 'processing', label: '处理中', type: 'primary' },
  { key: 'completed', label: '已完成', type: 'success' },
  { key: 'failed', label: '失败', type: 'danger' },
  { key: 'cancelled', label: '已放弃', type: 'info' },
] as const

type StatTile = (typeof STAT_TILES)[number]

function statValue(tile: StatTile): number {
  const stats = queue.value?.stats
  if (!stats) return 0
  return stats[tile.key]
}

onMounted(() => {
  void load()
})

watch(
  () => subject.currentId,
  () => {
    query.page = 1
    void load()
  },
)

async function load() {
  if (!subject.currentId) {
    rows.value = []
    total.value = 0
    queue.value = null
    return
  }
  loading.value = true
  try {
    const [list, status] = await Promise.all([
      importTaskApi.list(subject.currentId, {
        status: query.status,
        page: query.page,
        page_size: query.page_size,
      }),
      importTaskApi.queueStatus(subject.currentId),
    ])
    rows.value = list.list ?? []
    total.value = list.total ?? 0
    queue.value = status
  } catch {
    rows.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function search() {
  query.page = 1
  void load()
}

/** 放弃：语义是"不再等它了"，不是"停掉正在跑的抽取"。 */
async function cancelTask(row: ImportTask) {
  try {
    await ElMessageBox.confirm(
      `确定放弃「${row.original_filename || '这次导入'}」吗？` +
        '放弃后这条任务会离开队列，但不会中断任何已经开始的识别。',
      '放弃确认',
      { type: 'warning', confirmButtonText: '放弃', cancelButtonText: '返回' },
    )
  } catch {
    return
  }
  try {
    await importTaskApi.cancel(subject.currentId as number, row.id)
    ElMessage.success('已放弃')
  } catch {
    // 接口层已弹提示，这里只负责刷新。
  }
  await load()
}

async function retryTask(row: ImportTask) {
  try {
    await ElMessageBox.confirm(
      `确定重新排队「${row.original_filename || '这次导入'}」吗？`,
      '重试确认',
      { type: 'info', confirmButtonText: '重新排队', cancelButtonText: '返回' },
    )
  } catch {
    return
  }
  try {
    await importTaskApi.retry(subject.currentId as number, row.id)
    // 抽取是同步的，队列里没有 worker：重排队不等于重跑，必须说清楚。
    ElMessage.warning('已重新排队。识别目前是同步执行的，需要重跑请回到智能录题页重新发起。')
  } catch {
    // 接口层已弹提示。
  }
  await load()
}

async function removeTask(row: ImportTask) {
  const created = row.saved_count
  try {
    await ElMessageBox.confirm(
      created > 0
        ? `删除后这次导入的记录会消失，但它已经入库的 ${created} 道题目会保留在题库里。确定删除吗？`
        : '确定删除这条导入记录吗？',
      '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '返回' },
    )
  } catch {
    return
  }
  try {
    await importTaskApi.remove(subject.currentId as number, row.id)
    ElMessage.success('已删除')
  } catch {
    // 接口层已弹提示。
  }
  // 删掉的是当前页最后一条时往前翻一页，避免停在空白页。
  if (rows.value.length === 1 && query.page > 1) query.page -= 1
  await load()
}

// ---- 这次导入产生的题目 ----

const drawer = reactive({
  visible: false,
  loading: false,
  task: null as ImportTask | null,
  rows: [] as QuestionBrief[],
  total: 0,
  page: 1,
  page_size: 20,
})

async function openQuestions(row: ImportTask) {
  drawer.task = row
  drawer.page = 1
  drawer.visible = true
  await loadQuestions()
}

async function loadQuestions() {
  const task = drawer.task
  if (!task || !subject.currentId) return
  drawer.loading = true
  try {
    const res = await importTaskApi.questions(subject.currentId, task.id, {
      page: drawer.page,
      page_size: drawer.page_size,
    })
    drawer.rows = res.list ?? []
    drawer.total = res.total ?? 0
  } catch {
    drawer.rows = []
    drawer.total = 0
  } finally {
    drawer.loading = false
  }
}

function goImport() {
  void router.push({ name: 'import' })
}

/**
 * 把某个任务抽出来的草稿拉进审核界面。
 *
 * 队列任务跑完时 HTTP 响应早就返回了，草稿存在任务记录里。
 * 不给这一步，排队抽出来的题就永远没人能审核入库——队列也就白做了。
 */
function reviewTask(row: ImportTask) {
  void router.push({ name: 'import', query: { task: String(row.id) } })
}

function statusTag(status: ImportTaskStatus): 'primary' | 'success' | 'warning' | 'danger' | 'info' {
  switch (status) {
    case 'completed':
      return 'success'
    case 'failed':
      return 'danger'
    case 'processing':
      return 'primary'
    case 'pending':
      return 'warning'
    default:
      return 'info'
  }
}

function formatTime(value: string | null): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/** 数量摘要：抽取看 total_count，入库看 saved/total，避免两行读起来像 bug。 */
function countText(row: ImportTask): string {
  if (row.mode === 'commit') {
    return `入库 ${row.saved_count}/${row.total_count}` + (row.failed_count ? ` · 失败 ${row.failed_count}` : '')
  }
  return `抽取 ${row.total_count} 题`
}
</script>

<template>
  <div class="space-y-3">
    <PageHeader title="导入历史" description="每一批导入产出了什么、失败在哪，处理完之后从这里回看。" />

    <!-- 队列概览 -->
    <div v-if="queue" class="flex flex-wrap gap-2">
      <el-card v-for="tile in STAT_TILES" :key="tile.key" shadow="never" class="!w-40 flex-1">
        <div class="text-xs text-ink-subtle">{{ tile.label }}</div>
        <div class="mt-1 text-2xl font-medium" :class="statValue(tile) > 0 ? 'text-ink' : 'text-ink-subtle/60'">
          {{ statValue(tile) }}
        </div>
      </el-card>
    </div>

    <!-- worker 掉线时必须说在前面：否则用户会对着不动的队列反复刷新 -->
    <el-alert
      v-if="queue && !queue.worker_online"
      type="info"
      show-icon
      :closable="false"
      :title="queue.hint"
      description="队列暂时不会推进：worker 未在运行。可稍后刷新，或回到智能录题页重新发起。"
    />

    <el-card shadow="never">
      <div class="flex flex-wrap items-center gap-2">
        <el-select v-model="query.status" placeholder="状态" clearable class="w-36" @change="search">
          <el-option label="等待处理" value="pending" />
          <el-option label="处理中" value="processing" />
          <el-option label="已完成" value="completed" />
          <el-option label="失败" value="failed" />
          <el-option label="已放弃" value="cancelled" />
        </el-select>

        <el-button @click="search">刷新</el-button>
        <el-button type="primary" plain @click="goImport">去录题</el-button>

        <span v-if="!subject.currentId" class="ml-auto text-xs text-ink-subtle">请先在右上角选择学科</span>
      </div>

      <TableSkeleton v-if="loading" :rows="6" :cols="6" />
      <el-table v-else :data="rows" row-key="id" class="mt-3">
        <el-table-column label="导入内容" min-width="220">
          <template #default="{ row }">
            <div class="text-sm">{{ row.original_filename || '（未命名）' }}</div>
            <div class="mt-0.5 text-xs text-ink-subtle">
              {{ countText(row) }}
              <span v-if="row.attempt > 0"> · 第 {{ row.attempt }} 次尝试</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusTag(row.status)">{{ row.status_label }}</el-tag>
          </template>
        </el-table-column>

        <el-table-column label="失败原因" min-width="200">
          <template #default="{ row }">
            <span v-if="row.error_message" class="text-xs text-danger-text">{{ row.error_message }}</span>
            <span v-else class="text-xs text-ink-subtle/60">—</span>
          </template>
        </el-table-column>

        <el-table-column label="创建时间" width="140">
          <template #default="{ row }">
            <span class="text-xs text-ink-muted">{{ formatTime(row.created_at) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="结束时间" width="140">
          <template #default="{ row }">
            <span class="text-xs text-ink-muted">{{ formatTime(row.finished_at) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="280" fixed="right">
          <template #default="{ row }">
            <el-button text size="small" @click="openQuestions(row)">
              查看题目<span v-if="row.saved_count > 0">（{{ row.saved_count }}）</span>
            </el-button>

            <template v-if="canOperate">
              <!--
                只有"已完成但还没入库"的任务才需要审核入口。
                已经入库的（saved_count > 0）再点进去只会拿到空草稿，
                给一个必然落空的按钮比不给更糟。
              -->
              <el-button
                v-if="row.status === 'completed' && row.saved_count === 0"
                text
                size="small"
                type="primary"
                @click="reviewTask(row)"
              >
                审核草稿
              </el-button>
              <el-button v-if="row.retryable" text size="small" type="warning" @click="retryTask(row)">
                重试
              </el-button>
              <el-button v-if="row.cancellable" text size="small" type="info" @click="cancelTask(row)">
                放弃
              </el-button>
              <el-button text size="small" type="danger" @click="removeTask(row)">删除</el-button>
            </template>
            <span v-else class="text-xs text-ink-subtle">无操作权限</span>
          </template>
        </el-table-column>

        <template #empty>
          <EmptyState
            :description="
              !subject.currentId
                ? '请先选择学科'
                : query.status
                  ? '没有该状态的导入记录'
                  : '还没有导入记录'
            "
            hint="每次智能录题都会留下一条记录，可以在这里看到产出了多少题、失败在哪里。"
            :action-label="subject.currentId ? '去录题' : undefined"
            @action="goImport"
          />
        </template>
      </el-table>

      <div class="mt-3 flex justify-end">
        <el-pagination
          v-model:current-page="query.page"
          v-model:page-size="query.page_size"
          :total="total"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="load"
          @size-change="search"
        />
      </div>
    </el-card>

    <!-- 这次导入产出的题目 -->
    <el-drawer v-model="drawer.visible" :title="`导入记录 #${drawer.task?.id ?? ''} 产出的题目`" size="60%">
      <el-alert
        v-if="drawer.task"
        type="info"
        :closable="false"
        class="mb-3"
        :title="drawer.task.original_filename || '（未命名）'"
        :description="`状态：${drawer.task.status_label} · 共 ${drawer.total} 题`"
      />

      <TableSkeleton v-if="drawer.loading" :rows="6" :cols="6" />
      <el-table v-else :data="drawer.rows" row-key="id">
        <el-table-column label="题干" min-width="320">
          <template #default="{ row }">
            <RichContent :doc="row.stem_doc" compact class="stem-clamp text-sm" />
          </template>
        </el-table-column>
        <el-table-column label="题型" width="90">
          <template #default="{ row }">
            <el-tag size="small" effect="plain">{{ row.question_type_label }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" effect="plain">{{ row.status_label }}</el-tag>
          </template>
        </el-table-column>

        <template #empty>
          <EmptyState
            description="这次导入没有产出题目"
            hint="可能只做了抽取、还没有入库，或者所有草稿都被取消勾选了。"
          />
        </template>
      </el-table>

      <div class="mt-3 flex justify-end">
        <el-pagination
          v-model:current-page="drawer.page"
          v-model:page-size="drawer.page_size"
          :total="drawer.total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next"
          @current-change="loadQuestions"
          @size-change="loadQuestions"
        />
      </div>
    </el-drawer>
  </div>
</template>
