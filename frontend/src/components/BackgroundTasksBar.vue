<template>
  <div
    v-if="items.length"
    class="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-accent-border bg-accent-soft/50 px-3 py-2"
  >
    <span class="inline-flex items-center gap-1 text-sm font-medium text-ink">
      <el-icon class="is-loading"><Loading /></el-icon>
      后台任务
    </span>
    <el-button
      v-for="item in items"
      :key="item.key"
      link
      type="primary"
      size="small"
      @click="openItem(item)"
    >
      {{ item.text }}
    </el-button>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { importTaskApi, type ImportQueueStatus } from '@/api/importTask'
import { useBackgroundTaskStore } from '@/stores/backgroundTasks'
import { useSubjectStore } from '@/stores/subject'

interface TaskItem {
  key: string
  kind: 'import' | 'queue' | 'solve'
  subjectId: number
  text: string
}

const router = useRouter()
const subject = useSubjectStore()
const tasks = useBackgroundTaskStore()

const queue = ref<ImportQueueStatus | null>(null)
let pollTimer: ReturnType<typeof setInterval> | null = null
const POLL_INTERVAL = 5_000

const items = computed<TaskItem[]>(() => {
  const out: TaskItem[] = []
  const localImportIds = new Set<number>()

  for (const run of tasks.activeImports) {
    localImportIds.add(run.subjectId)
    out.push({
      key: `import-${run.subjectId}`,
      kind: 'import',
      subjectId: run.subjectId,
      text: `智能录题：${run.label}（已等待 ${formatElapsed(run.elapsedSec)}）`,
    })
  }

  // 队列状态是服务端真相；本次会话没有本地任务时仍然要显示出来，
  // 例如用户刷新过页面、或者队列是另一个标签页刚提交的。
  if (
    queue.value &&
    subject.currentId &&
    !localImportIds.has(subject.currentId) &&
    queue.value.stats.pending + queue.value.stats.processing > 0
  ) {
    const parts = []
    if (queue.value.stats.processing) parts.push(`处理中 ${queue.value.stats.processing}`)
    if (queue.value.stats.pending) parts.push(`等待 ${queue.value.stats.pending}`)
    out.push({
      key: `queue-${subject.currentId}`,
      kind: 'queue',
      subjectId: subject.currentId,
      text: `智能录题队列：${parts.join('，')}`,
    })
  }

  for (const run of tasks.activeSolves) {
    out.push({
      key: `solve-${run.subjectId}`,
      kind: 'solve',
      subjectId: run.subjectId,
      text: `AI 解题：${run.done}/${run.total}`,
    })
  }
  return out
})

function formatElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds} 秒`
  return `${Math.floor(seconds / 60)} 分 ${String(seconds % 60).padStart(2, '0')} 秒`
}

function queueIsActive(): boolean {
  const stats = queue.value?.stats
  if (!stats) return false
  return stats.pending > 0 || stats.processing > 0
}

function stopPolling() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = null
}

function syncPolling() {
  if (subject.currentId && (queueIsActive() || tasks.activeImports.length > 0)) {
    if (!pollTimer) pollTimer = setInterval(() => void refreshQueue(), POLL_INTERVAL)
  } else {
    stopPolling()
  }
}

async function refreshQueue() {
  if (!subject.currentId || !subject.can('edit_question')) {
    queue.value = null
    stopPolling()
    return
  }
  try {
    queue.value = await importTaskApi.queueStatus(subject.currentId)
  } catch {
    // 顶部任务条是辅助信息，不应因为一次轮询抖动弹错或清空页面。
  } finally {
    syncPolling()
  }
}

async function openItem(item: TaskItem) {
  if (item.subjectId !== subject.currentId) {
    try {
      await subject.switchTo(item.subjectId)
    } catch {
      return
    }
  }
  if (item.kind === 'solve') {
    await router.push({ name: 'questions' })
  } else if (item.kind === 'queue') {
    await router.push({ name: 'import-history' })
  } else {
    await router.push({ name: 'import' })
  }
}

onMounted(() => void refreshQueue())
onBeforeUnmount(stopPolling)

watch(() => subject.currentId, () => void refreshQueue())
watch(() => tasks.activeImports.length, syncPolling)
</script>
