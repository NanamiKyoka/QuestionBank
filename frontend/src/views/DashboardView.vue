<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { statsApi } from '@/api'
import type { GlobalStats, SubjectStats } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { useSubjectStore } from '@/stores/subject'

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
  <div v-loading="loading" class="space-y-4">
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
      <!-- 学科概览 -->
      <el-row :gutter="12">
        <el-col v-for="card in [
          { label: '题目总数', value: stats?.questions.total ?? 0, tone: 'blue' },
          { label: '待审核', value: stats?.questions.pending ?? 0, tone: 'orange' },
          { label: '知识点', value: stats?.knowledge_points ?? 0, tone: 'green' },
          { label: '试卷', value: stats?.papers ?? 0, tone: 'purple' },
        ]" :key="card.label" :xs="12" :sm="12" :md="6">
          <el-card shadow="never" class="mb-3">
            <div class="text-xs text-gray-500">{{ card.label }}</div>
            <div class="mt-1 text-2xl font-semibold">{{ formatNumber(card.value) }}</div>
          </el-card>
        </el-col>
      </el-row>

      <el-row :gutter="12">
        <!-- 建题趋势 -->
        <el-col :xs="24" :md="14">
          <el-card shadow="never" class="mb-3">
            <template #header>
              <div class="flex items-center justify-between">
                <span class="text-sm font-medium">近 30 天建题趋势</span>
                <span class="text-xs text-gray-400">
                  共 {{ formatNumber(trend.reduce((sum, d) => sum + d.total, 0)) }} 道
                </span>
              </div>
            </template>

            <div class="flex h-32 items-end gap-[3px]">
              <div
                v-for="point in trend"
                :key="point.day"
                class="group relative flex-1 rounded-t bg-blue-500/80 transition-colors hover:bg-blue-600"
                :style="{ height: `${Math.max(point.total / maxTrend * 100, point.total ? 4 : 1)}%` }"
                :title="`${point.day}：${point.total} 道`"
              />
            </div>
            <div class="mt-2 flex justify-between text-xs text-gray-400">
              <span>{{ trend[0]?.day }}</span>
              <span>{{ trend[trend.length - 1]?.day }}</span>
            </div>
          </el-card>
        </el-col>

        <!-- 审核进度 -->
        <el-col :xs="24" :md="10">
          <el-card shadow="never" class="mb-3">
            <template #header>
              <span class="text-sm font-medium">审核进度</span>
            </template>
            <div class="space-y-3">
              <div
                v-for="row in [
                  { label: '草稿', value: stats?.questions.draft ?? 0, color: '#909399' },
                  { label: '待审核', value: stats?.questions.pending ?? 0, color: '#e6a23c' },
                  { label: '已发布', value: stats?.questions.published ?? 0, color: '#67c23a' },
                  { label: '已归档', value: stats?.questions.archived ?? 0, color: '#c0c4cc' },
                ]"
                :key="row.label"
              >
                <div class="mb-1 flex justify-between text-xs">
                  <span>{{ row.label }}</span>
                  <span class="text-gray-500">{{ row.value }}</span>
                </div>
                <div class="h-1.5 overflow-hidden rounded bg-gray-100">
                  <div
                    class="h-full rounded transition-all"
                    :style="{
                      width: `${(row.value / Math.max(1, stats?.questions.total ?? 1)) * 100}%`,
                      backgroundColor: row.color,
                    }"
                  />
                </div>
              </div>
            </div>

            <el-divider class="my-3" />

            <el-button
              text
              type="primary"
              size="small"
              @click="router.push({ name: 'questions', query: { status: 'pending' } })"
            >
              去处理待审核题目 →
            </el-button>
          </el-card>
        </el-col>
      </el-row>

      <el-row :gutter="12">
        <!-- 题型分布 -->
        <el-col :xs="24" :md="8">
          <el-card shadow="never" class="mb-3">
            <template #header><span class="text-sm font-medium">题型分布</span></template>
            <div v-if="typeRows.length === 0" class="py-4 text-center text-xs text-gray-400">
              暂无数据
            </div>
            <div v-for="row in typeRows" :key="row.key" class="mb-2">
              <div class="mb-1 flex justify-between text-xs">
                <span>{{ row.label }}</span>
                <span class="text-gray-500">{{ row.total }}</span>
              </div>
              <div class="h-1.5 overflow-hidden rounded bg-gray-100">
                <div
                  class="h-full rounded bg-blue-500"
                  :style="{ width: `${(row.total / maxType) * 100}%` }"
                />
              </div>
            </div>
          </el-card>
        </el-col>

        <!-- 难度分布 -->
        <el-col :xs="24" :md="8">
          <el-card shadow="never" class="mb-3">
            <template #header><span class="text-sm font-medium">难度分布</span></template>
            <div v-for="row in difficultyRows" :key="row.level" class="mb-2">
              <div class="mb-1 flex justify-between text-xs">
                <span>{{ '★'.repeat(row.level) }}</span>
                <span class="text-gray-500">{{ row.total }}</span>
              </div>
              <div class="h-1.5 overflow-hidden rounded bg-gray-100">
                <div
                  class="h-full rounded bg-amber-400"
                  :style="{ width: `${(row.total / maxDifficulty) * 100}%` }"
                />
              </div>
            </div>
          </el-card>
        </el-col>

        <!-- 最近试卷 -->
        <el-col :xs="24" :md="8">
          <el-card shadow="never" class="mb-3">
            <template #header><span class="text-sm font-medium">最近试卷</span></template>
            <div v-if="!stats?.recent_papers.length" class="py-4 text-center text-xs text-gray-400">
              还没有试卷
            </div>
            <div
              v-for="paper in stats?.recent_papers ?? []"
              :key="paper.id"
              class="flex items-center justify-between border-b border-gray-100 py-2 last:border-0"
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
      <el-row :gutter="12">
        <el-col
          v-for="item in [
            { label: '学科', value: globalStats.subjects },
            { label: '全库题目', value: globalStats.questions },
            { label: '用户', value: globalStats.users },
            { label: 'AI 调用（30 天）', value: globalStats.ai.runs },
            { label: 'Token 用量（30 天）', value: globalStats.ai.total_tokens },
          ]"
          :key="item.label"
          :xs="12"
          :sm="8"
          :md="4"
        >
          <div class="mb-2">
            <div class="text-xs text-gray-500">{{ item.label }}</div>
            <div class="text-lg font-semibold">{{ formatNumber(item.value) }}</div>
          </div>
        </el-col>
      </el-row>

      <template v-if="globalStats.top_actions.length">
        <el-divider class="my-3" />
        <div class="mb-2 text-xs text-gray-500">最近 30 天最常见的操作</div>
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
