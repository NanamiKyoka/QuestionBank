<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { activityApi, type ActivityLog, type ActivityLogQuery } from '@/api/activity'
import { userApi } from '@/api'
import type { User } from '@/api/types'
import PageHeader from '@/components/ui/PageHeader.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import TableSkeleton from '@/components/ui/TableSkeleton.vue'
import { useAuthStore } from '@/stores/auth'

/**
 * 操作审计日志。仅全局管理员。
 *
 * 数据来自 GET /api/v1/activity-logs（router.go:373）——后端对每一次写操作
 * 自动记录 13 个字段，并支持按用户 / 动作 / 资源 / 关键字 / 成败 / 时间范围筛选。
 * 这里只做界面，不再补任何"后端没记录"的东西。
 */

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const loading = ref(false)
const rows = ref<ActivityLog[]>([])
const total = ref(0)
const users = ref<User[]>([])

/**
 * 筛选条件。
 *
 * 全部字段都和路由 query 双向同步：这样一条"某用户昨天删了哪些题"的
 * 筛选结果可以直接把链接发给别人，而不用口头复述一遍条件。
 */
const query = reactive({
  keyword: '',
  user_id: null as number | null,
  action: '',
  resource: '',
  success: '' as '' | 'true' | 'false',
  /**
   * el-date-picker 的值（value-format="x"，即毫秒时间戳）。
   * 类型放宽成 string | number：同一个格式化选项在不同 Element Plus 版本上
   * 可能给出字符串或数字，统一用 Number() 收口，免得运行时才发现是字符串。
   */
  range: null as [string | number, string | number] | null,
  page: 1,
  page_size: 20,
})

/**
 * 动作名候选。
 *
 * 后端没有下发"出现过哪些动作"的接口（repository.DistinctActions 存在但没接路由），
 * 所以这里给一份按 service/activity.go 的 DeriveAction 规则整理的常用清单，
 * 只用于输入提示——筛选框同时允许自由输入，管理员可以按完整路径得到的
 * 任何动作名搜索。
 */
const actionCandidates = [
  'question.create', 'question.update', 'question.delete', 'question.review',
  'question.submit', 'question.restore',
  'paper.create', 'paper.update', 'paper.delete', 'paper.duplicate',
  'paper.finalize_version', 'paper.restore', 'paper.update_nodes',
  'subject.create', 'subject.update', 'subject.delete',
  'user.create', 'user.update', 'user.delete', 'user.reset_password',
  'knowledge_point.create', 'knowledge_point.update', 'knowledge_point.delete', 'knowledge_point.move',
  'tag.create', 'tag.update', 'tag.delete', 'tag_category.create', 'tag_category.update',
  'import.extract', 'import.commit', 'import.sync',
  'session.create', 'session.send_message', 'session.update', 'session.delete',
  'provider.create', 'provider.update', 'provider.delete', 'provider.test',
  'model.create', 'model.update', 'model.delete',
  'system_setting.update', 'activity_log.view',
]

/** 资源名候选。命名规则同样来自 service/activity.go 的 resourceAliases。 */
const resourceCandidates = [
  'question', 'paper', 'paper_folder', 'paper_node', 'subject', 'member', 'user',
  'knowledge_point', 'tag', 'tag_category', 'import', 'session', 'run',
  'provider', 'model', 'system_setting', 'activity_log', 'ai_config', 'stat',
]

const selected = ref<ActivityLog | null>(null)
const detailVisible = ref(false)

onMounted(async () => {
  applyRouteQuery()
  await load()
  if (auth.isSuperuser) void loadUsers()
})

// 用户筛选项只取前 200 个：审计日志是排障用的，
// 真要按某个用户查，直接用关键字搜他的用户名更快。
async function loadUsers() {
  try {
    const result = await userApi.list({ page: 1, page_size: 200 })
    users.value = result.list ?? []
  } catch {
    // 拿不到用户列表不影响主功能：下拉框留空，照样可以用关键字搜。
    users.value = []
  }
}

/** 时间范围 → RFC3339。gin 的 form 绑定只认 RFC3339，别的格式直接 400。 */
function toRFC3339(value: string | number): string {
  return new Date(Number(value)).toISOString()
}

function buildQuery(): ActivityLogQuery {
  const q: ActivityLogQuery = {
    keyword: query.keyword.trim() || undefined,
    user_id: query.user_id ?? undefined,
    action: query.action.trim() || undefined,
    resource: query.resource.trim() || undefined,
    success: query.success === '' ? undefined : query.success === 'true',
    page: query.page,
    page_size: query.page_size,
  }
  if (query.range) {
    q.since = toRFC3339(query.range[0])
    q.until = toRFC3339(query.range[1])
  }
  return q
}

async function load() {
  loading.value = true
  try {
    const result = await activityApi.list(buildQuery())
    rows.value = result.list ?? []
    total.value = result.total ?? 0
  } catch {
    // 错误提示由接口层统一处理
  } finally {
    loading.value = false
  }
}

/** 回到第一页重新查询。 */
function search() {
  query.page = 1
  void apply()
}

/** 写回路由 → 查询。 */
async function apply() {
  // 自己 replace 出来的 query 也会触发 watcher，用这个标志跳过一次重复请求。
  selfNavigating = true
  try {
    await router.replace({ query: routeQuery() })
  } finally {
    selfNavigating = false
  }
  await load()
}

function reset() {
  query.keyword = ''
  query.user_id = null
  query.action = ''
  query.resource = ''
  query.success = ''
  query.range = null
  void search()
}

/** 当前筛选条件对应的路由 query（空值不写进 URL）。 */
function routeQuery(): Record<string, string> {
  const q: Record<string, string> = {}
  if (query.keyword.trim()) q.keyword = query.keyword.trim()
  if (query.user_id !== null) q.user_id = String(query.user_id)
  if (query.action.trim()) q.action = query.action.trim()
  if (query.resource.trim()) q.resource = query.resource.trim()
  if (query.success !== '') q.success = query.success
  if (query.range) {
    q.since = toRFC3339(query.range[0])
    q.until = toRFC3339(query.range[1])
  }
  if (query.page > 1) q.page = String(query.page)
  return q
}

/** 路由 query → 筛选条件。首次进入与浏览器前进/后退都走这里。 */
function applyRouteQuery() {
  const raw = route.query
  const str = (key: string): string => {
    const v = raw[key]
    return typeof v === 'string' ? v : ''
  }
  const num = (key: string): number | null => {
    const v = str(key)
    const n = Number(v)
    return v !== '' && Number.isFinite(n) && n > 0 ? n : null
  }

  query.keyword = str('keyword')
  query.user_id = num('user_id')
  query.action = str('action')
  query.resource = str('resource')
  query.success = str('success') === 'true' ? 'true' : str('success') === 'false' ? 'false' : ''

  const since = Date.parse(str('since'))
  const until = Date.parse(str('until'))
  query.range = Number.isFinite(since) && Number.isFinite(until) ? [since, until] : null

  const page = Number(str('page'))
  query.page = Number.isFinite(page) && page > 0 ? page : 1
}

// 浏览器前进/后退时按新的 query 重新取数。
// apply() 自己写回 URL 的那次导航不算——它已经在 apply() 里查过了。
let selfNavigating = false
watch(
  () => route.query,
  () => {
    if (selfNavigating) return
    applyRouteQuery()
    void load()
  },
)

function openDetail(row: ActivityLog) {
  selected.value = row
  detailVisible.value = true
}

/** detail 是结构随写入方而变的 JSON，原样格式化展示。 */
function detailText(row: ActivityLog): string {
  if (row.detail === undefined || row.detail === null) return ''
  try {
    return JSON.stringify(row.detail, null, 2)
  } catch {
    return String(row.detail)
  }
}

function methodTag(method: string): 'success' | 'warning' | 'danger' | 'info' {
  switch (method) {
    case 'POST':
      return 'success'
    case 'PUT':
    case 'PATCH':
      return 'warning'
    case 'DELETE':
      return 'danger'
    default:
      return 'info'
  }
}

function formatTime(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}
</script>

<template>
  <div class="space-y-3">
    <PageHeader title="操作日志" description="记录每一次写操作，读取不记录。高负载时队列满会丢弃少量记录，因此这是排障线索而不是合规凭证。" />

    <el-alert
      type="info"
      show-icon
      :closable="false"
      title="系统会自动记录每一次写操作（新增、修改、删除、审核等），读取操作不记录。"
      description="写入是异步的：高负载时队列满会丢弃少量记录，因此这里是排障线索，不是合规审计凭证。"
    />

    <el-card shadow="never">
      <template #header>
        <div class="flex flex-wrap items-center gap-2">
          <el-input
            v-model="query.keyword"
            placeholder="用户名或请求路径"
            clearable
            class="w-56"
            @keyup.enter="search"
            @clear="search"
          />
          <el-select
            v-model="query.user_id"
            placeholder="全部用户"
            clearable
            filterable
            class="w-44"
            @change="search"
          >
            <el-option
              v-for="u in users"
              :key="u.id"
              :label="u.nickname || u.username"
              :value="u.id"
            />
          </el-select>
          <el-select
            v-model="query.action"
            placeholder="动作"
            clearable
            filterable
            allow-create
            default-first-option
            class="w-52"
            @change="search"
          >
            <el-option v-for="a in actionCandidates" :key="a" :label="a" :value="a" />
          </el-select>
          <el-select
            v-model="query.resource"
            placeholder="资源"
            clearable
            filterable
            allow-create
            default-first-option
            class="w-40"
            @change="search"
          >
            <el-option v-for="r in resourceCandidates" :key="r" :label="r" :value="r" />
          </el-select>
          <el-select
            v-model="query.success"
            placeholder="结果"
            clearable
            class="w-28"
            @change="search"
          >
            <el-option label="全部" value="" />
            <el-option label="成功" value="true" />
            <el-option label="失败" value="false" />
          </el-select>
          <el-date-picker
            v-model="query.range"
            type="datetimerange"
            value-format="x"
            start-placeholder="开始时间"
            end-placeholder="结束时间"
            range-separator="至"
            class="!w-96"
          />
          <el-button type="primary" @click="search">查询</el-button>
          <el-button @click="reset">重置</el-button>
        </div>
      </template>

      <EmptyState
        v-if="!loading && rows.length === 0"
        description="没有符合条件的记录"
        hint="调整时间范围或状态码筛选，或者清空条件重新查一次。"
      />

      <TableSkeleton v-if="loading" :rows="8" :cols="6" />

      <el-table v-else :data="rows" row-key="id" @row-click="openDetail">
        <el-table-column label="时间" width="160">
          <template #default="{ row }">
            <span class="text-xs text-ink-muted">{{ formatTime(row.created_at) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="用户" min-width="140">
          <template #default="{ row }">
            <span>{{ row.username || '（已删除）' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="动作" min-width="200">
          <template #default="{ row }">
            <div class="flex items-center gap-1">
              <el-tag size="small" :type="methodTag(row.method)" effect="plain">
                {{ row.method }}
              </el-tag>
              <span class="text-sm">{{ row.action }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="资源" min-width="160">
          <template #default="{ row }">
            <span class="text-sm">{{ row.resource }}</span>
            <span v-if="row.resource_id" class="ml-1 text-xs text-ink-subtle">
              #{{ row.resource_id }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.success ? 'success' : 'danger'">
              {{ row.success ? '成功' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态码" width="90" align="center">
          <template #default="{ row }">
            <span class="num" :class="row.success ? '' : 'text-danger-text'">{{ row.status_code }}</span>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="90" align="right">
          <template #default="{ row }">
            <span class="num text-xs text-ink-muted">{{ row.duration_ms }}ms</span>
          </template>
        </el-table-column>
        <el-table-column label="IP" width="140">
          <template #default="{ row }">
            <span class="text-xs text-ink-muted">{{ row.ip || '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="80" fixed="right">
          <template #default="{ row }">
            <el-button text size="small" @click.stop="openDetail(row)">详情</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div v-if="rows.length > 0" class="mt-3 flex justify-end">
        <el-pagination
          v-model:current-page="query.page"
          v-model:page-size="query.page_size"
          :total="total"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next"
          @current-change="apply"
          @size-change="search"
        />
      </div>
    </el-card>

    <!-- 详情抽屉 -->
    <el-drawer v-model="detailVisible" title="记录详情" size="560px">
      <div v-if="selected" class="space-y-3 text-sm">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item label="记录 ID">{{ selected.id }}</el-descriptions-item>
          <el-descriptions-item label="时间">{{ formatTime(selected.created_at) }}</el-descriptions-item>
          <el-descriptions-item label="用户">
            {{ selected.username || '（已删除）' }}
            <span v-if="selected.user_id" class="num text-xs text-ink-subtle">#{{ selected.user_id }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="动作">{{ selected.action }}</el-descriptions-item>
          <el-descriptions-item label="资源">
            {{ selected.resource }}
            <span v-if="selected.resource_id" class="num">#{{ selected.resource_id }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="请求">
            <el-tag size="small" :type="methodTag(selected.method)" effect="plain">
              {{ selected.method }}
            </el-tag>
            <span class="ml-2 break-all">{{ selected.path }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="结果">
            <el-tag size="small" :type="selected.success ? 'success' : 'danger'">
              {{ selected.success ? '成功' : '失败' }}
            </el-tag>
            <span class="ml-2">HTTP {{ selected.status_code }}</span>
            <span class="ml-2 text-ink-subtle">{{ selected.duration_ms }}ms</span>
          </el-descriptions-item>
          <el-descriptions-item label="来源 IP">{{ selected.ip || '-' }}</el-descriptions-item>
          <el-descriptions-item label="User-Agent">
            <span class="break-all text-xs">{{ selected.user_agent || '-' }}</span>
          </el-descriptions-item>
        </el-descriptions>

        <template v-if="detailText(selected)">
          <div class="text-xs text-ink-muted">附加信息</div>
          <pre class="max-h-80 overflow-auto rounded bg-surface-sunken p-3 text-xs">{{ detailText(selected) }}</pre>
        </template>
      </div>
    </el-drawer>
  </div>
</template>
