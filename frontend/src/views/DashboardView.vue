<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { statsApi } from '@/api'
import type { GlobalStats, SubjectStats } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { useSubjectStore } from '@/stores/subject'
import PageHeader from '@/components/ui/PageHeader.vue'
import StatTile from '@/components/ui/StatTile.vue'

const router = useRouter()
const auth = useAuthStore()
const subject = useSubjectStore()

const loading = ref(false)
const stats = ref<SubjectStats | null>(null)
const globalStats = ref<GlobalStats | null>(null)

onMounted(load)
watch(() => subject.currentId, load)

async function load() {
  loading.value = true
  try {
    const tasks: Promise<unknown>[] = []
    if (subject.currentId) {
      tasks.push(
        statsApi.subject(subject.currentId).then((data) => {
          stats.value = data
        }),
      )
    } else {
      stats.value = null
    }
    // 全局统计仅管理员可见，普通用户请求会拿到 403；
    // 这里主动判断，避免每次都产生一条无意义的 403 日志。
    if (auth.isSuperuser) {
      tasks.push(
        statsApi.global().then((data) => {
          globalStats.value = data
        }),
      )
    }
    await Promise.all(tasks)
  } catch {
    // 错误提示由接口层统一处理
  } finally {
    loading.value = false
  }
}

/** 题型分布，按数量降序，便于一眼看出主力题型。 */
const typeRows = computed(() => {
  const dist = stats.value?.questions.by_type ?? {}
  return Object.entries(dist)
    .map(([key, total]) => ({ key, label: typeLabel(key), total }))
    .sort((a, b) => b.total - a.total)
})

const difficultyRows = computed(() => {
  const dist = stats.value?.questions.by_difficulty ?? {}
  return [1, 2, 3, 4, 5].map((level) => ({
    level,
    total: Number(dist[String(level)] ?? 0),
  }))
})

const maxType = computed(() => Math.max(1, ...typeRows.value.map((r) => r.total)))
const maxDifficulty = computed(() => Math.max(1, ...difficultyRows.value.map((r) => r.total)))

type Tone = 'accent' | 'success' | 'warning' | 'danger' | 'neutral'

const overviewTiles = computed<{ label: string; value: number; tone: Tone }[]>(() => [
  { label: '题目总数', value: stats.value?.questions.total ?? 0, tone: 'accent' },
  { label: '待审核', value: stats.value?.questions.pending ?? 0, tone: 'warning' },
  { label: '知识点', value: stats.value?.knowledge_points ?? 0, tone: 'success' },
  { label: '试卷', value: stats.value?.papers ?? 0, tone: 'neutral' },
])

const reviewRows = computed<{ label: string; value: number; tone: Tone }[]>(() => [
  { label: '草稿', value: stats.value?.questions.draft ?? 0, tone: 'neutral' },
  { label: '待审核', value: stats.value?.questions.pending ?? 0, tone: 'warning' },
  { label: '已发布', value: stats.value?.questions.published ?? 0, tone: 'success' },
  { label: '已归档', value: stats.value?.questions.archived ?? 0, tone: 'neutral' },
])

/**
 * 趋势图数据。
 *
 * 后端只返回"有数据的日期"，这里补齐空缺日期并填 0——
 * 补零是展示层的决定，服务端不替前端做，否则换图表库时会被绑住。
 */
const trend = computed(() => {
  const raw = new Map((stats.value?.question_trend ?? []).map((p) => [p.day, p.total]))
  const days: { day: string; total: number }[] = []
  for (let i = 29; i >= 0; i--) {
    const date = new Date()
    date.setDate(date.getDate() - i)
    const key = formatDay(date)
    days.push({ day: key, total: raw.get(key) ?? 0 })
  }
  return days
})

const maxTrend = computed(() => Math.max(1, ...trend.value.map((d) => d.total)))

function formatDay(date: Date): string {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}

function typeLabel(key: string): string {
  const map: Record<string, string> = {
    single_choice: '单选题',
    multiple_choice: '多选题',
    fill_blank: '填空题',
    true_false: '判断题',
    essay: '解答题',
  }
  return map[key] ?? key
}

function statusLabel(key: string): string {
  const map: Record<string, string> = {
    draft: '草稿',
    finalized: '已定稿',
    archived: '已归档',
  }
  return map[key] ?? key
}

function formatNumber(n: number): string {
  return n.toLocaleString('zh-CN')
}
</script>

<template>
  <div class="space-y-4">
    <PageHeader
      title="总览"
      :description="subject.currentId ? `${subject.current?.name ?? ''}的题库与组卷情况` : ''"
    >
      <template #actions>
        <el-button :loading="loading" @click="load"><el-icon class="mr-1"><Refresh /></el-icon>刷新</el-button>
      </template>
    </PageHeader>

    <el-alert
      v-if="!subject.hasSubjects"
      type="info"
      :closable="false"
      show-icon
      title="还没有可用的学科"
    >
      <template #default>
        <span class="text-sm">
          学科是题库的一级边界。请先在「学科管理」中创建一个学科。
        </span>
      </template>
    </el-alert>

    <template v-else>
      <!-- 学科概览：一条指标带，而不是四个互相竞争的白盒 -->
      <el-card shadow="never" body-class="!p-0">
        <div class="grid grid-cols-2 divide-hairline md:grid-cols-4 md:divide-x">
          <div
            v-for="tile in overviewTiles"
            :key="tile.label"
            class="px-4 py-3.5"
          >
            <StatTile
              :label="tile.label"
              :value="tile.value"
              :tone="tile.tone"
              :muted="tile.value === 0"
            />
          </div>
        </div>
      </el-card>

      <el-row :gutter="12">
        <!-- 建题趋势 -->
        <el-col :xs="24" :md="14" class="flex">
          <el-card shadow="never" class="mb-3 flex w-full flex-col">
            <template #header>
              <div class="flex items-center justify-between">
                <span class="text-sm font-medium">近 30 天建题趋势</span>
                <span class="num text-xs text-ink-subtle">
                  共 {{ formatNumber(trend.reduce((sum, d) => sum + d.total, 0)) }} 道
                </span>
              </div>
            </template>

            <div class="flex flex-1 flex-col">
              <div class="flex min-h-32 flex-1 items-end gap-[3px]">
                <div
                  v-for="point in trend"
                  :key="point.day"
                  class="group relative flex-1 rounded-t bg-accent/80 transition-colors hover:bg-accent"
                  :style="{ height: `${Math.max(point.total / maxTrend * 100, point.total ? 4 : 1)}%` }"
                  :title="`${point.day}：${point.total} 道`"
                />
              </div>
              <div class="mt-2 flex justify-between text-xs text-ink-subtle">
                <span>{{ trend[0]?.day }}</span>
                <span>{{ trend[trend.length - 1]?.day }}</span>
              </div>
            </div>
          </el-card>
        </el-col>

        <!-- 审核进度 -->
        <el-col :xs="24" :md="10">
          <el-card shadow="never" class="mb-3">
            <template #header>
              <span class="text-sm font-medium">审核进度</span>
            </template>
            <StatTile
              v-for="row in reviewRows"
              :key="row.label"
              :label="row.label"
              :value="row.value"
              :tone="row.tone"
              :bar="(row.value / Math.max(1, stats?.questions.total ?? 1)) * 100"
              :muted="row.value === 0"
              class="mb-3"
            />

            <el-button
              text
              type="primary"
              size="small"
              @click="router.push({ name: 'questions', query: { status: 'pending' } })"
            >
              去处理待审核题目
              <el-icon class="ml-1"><ArrowRight /></el-icon>
            </el-button>
          </el-card>
        </el-col>
      </el-row>

      <el-row :gutter="12">
        <!-- 题型分布 -->
        <el-col :xs="24" :md="8">
          <el-card shadow="never" class="mb-3">
            <template #header><span class="text-sm font-medium">题型分布</span></template>
            <div v-if="typeRows.length === 0" class="py-4 text-center text-xs text-ink-subtle">
              暂无数据
            </div>
            <StatTile
              v-for="row in typeRows"
              :key="row.key"
              :label="row.label"
              :value="row.total"
              tone="accent"
              :bar="(row.total / maxType) * 100"
              :muted="row.total === 0"
              class="mb-3"
            />
          </el-card>
        </el-col>

        <!-- 难度分布 -->
        <el-col :xs="24" :md="8">
          <el-card shadow="never" class="mb-3">
            <template #header><span class="text-sm font-medium">难度分布</span></template>
            <StatTile
              v-for="row in difficultyRows"
              :key="row.level"
              :label="`难度 ${row.level}`"
              :value="row.total"
              tone="neutral"
              :bar="(row.total / maxDifficulty) * 100"
              :muted="row.total === 0"
              class="mb-3"
            />
          </el-card>
        </el-col>

        <!-- 最近试卷 -->
        <el-col :xs="24" :md="8" class="flex">
          <el-card shadow="never" class="mb-3 flex w-full flex-col">
            <template #header><span class="text-sm font-medium">最近试卷</span></template>
            <div v-if="!stats?.recent_papers.length" class="py-4 text-center text-xs text-ink-subtle">
              还没有试卷
            </div>
            <div
              v-for="paper in stats?.recent_papers ?? []"
              :key="paper.id"
              class="flex items-center justify-between border-b border-hairline py-2 last:border-0"
            >
              <span class="truncate text-sm">{{ paper.title }}</span>
              <el-tag size="small" type="info">{{ statusLabel(paper.status) }}</el-tag>
            </div>
          </el-card>
        </el-col>
      </el-row>
    </template>

    <!-- 全局统计（管理员） -->
    <el-card v-if="auth.isSuperuser && globalStats" shadow="never">
      <template #header>
        <span class="text-sm font-medium">系统总览（管理员）</span>
      </template>
      <div class="grid grid-cols-2 gap-4 md:grid-cols-5">
        <StatTile
          v-for="item in [
            { label: '学科', value: globalStats.subjects },
            { label: '全库题目', value: globalStats.questions },
            { label: '用户', value: globalStats.users },
            { label: 'AI 调用（30 天）', value: globalStats.ai.runs },
            { label: 'Token 用量（30 天）', value: globalStats.ai.total_tokens },
          ]"
          :key="item.label"
          :label="item.label"
          :value="item.value"
          tone="neutral"
        />
      </div>

      <template v-if="globalStats.top_actions.length">
        <el-divider class="my-3" />
        <div class="mb-2 text-xs text-ink-muted">最近 30 天最常见的操作</div>
        <div class="flex flex-wrap gap-2">
          <el-tag
            v-for="action in globalStats.top_actions"
            :key="action.action"
            size="small"
            type="info"
          >
            {{ action.action }} · {{ action.total }}
          </el-tag>
        </div>
      </template>
    </el-card>
  </div>
</template>
