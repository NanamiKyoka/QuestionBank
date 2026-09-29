<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { paperApi, type PaperEvent } from '@/api/paper'

/**
 * 操作时间线。
 *
 * 每一次改动后端都会落一条流水（created / updated / nodes_replaced /
 * questions_synced / finalized …），但界面从来没有展示过它。
 *
 * 分页用 `before_id` 游标而不是页码：时间线只增不减，页码分页会在翻页途中
 * 插入新事件、导致已经读过的条目被挤到下一页甚至跳过。游标分页天然稳定。
 */
const open = defineModel<boolean>({ default: false })

const props = defineProps<{
  paperId: number
  /** 试卷标题，只用于空态里的说明文字。 */
  title?: string
}>()

/** 与后端 ListEvents 的默认页长一致：一次给够，少一次往返。 */
const PAGE_SIZE = 30

/** 事件类型 → 中文标签。未登记的类型原样显示，避免新类型在界面上变成空白。 */
const EVENT_LABELS: Record<string, string> = {
  created: '创建试卷',
  updated: '更新信息',
  moved: '移动目录',
  deleted: '删除试卷',
  restored: '恢复试卷',
  duplicated: '复制试卷',
  nodes_replaced: '保存排版',
  questions_synced: '同步题目',
  finalized: '定稿',
}

function labelOf(type: string): string {
  return EVENT_LABELS[type] ?? type
}

const loading = ref(false)
const loadingMore = ref(false)
const failed = ref(false)
const events = ref<PaperEvent[]>([])
/** 服务端没有 has_more 字段，用"这页取满"作为还有更多的判据。 */
const hasMore = ref(false)

const isEmpty = computed(() => !loading.value && !failed.value && events.value.length === 0)

function formatTime(value: string): string {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString('zh-CN')
}

async function load() {
  loading.value = true
  failed.value = false
  try {
    const list = await paperApi.events(props.paperId, { limit: PAGE_SIZE })
    events.value = list
    hasMore.value = list.length >= PAGE_SIZE
  } catch {
    // 接口层已经弹过提示，这里只把面板切到"可重试"的错误态。
    failed.value = true
  } finally {
    loading.value = false
  }
}

async function loadMore() {
  const cursor = events.value.at(-1)?.id
  if (cursor == null || loadingMore.value) return
  loadingMore.value = true
  try {
    const list = await paperApi.events(props.paperId, { limit: PAGE_SIZE, before_id: cursor })
    events.value = [...events.value, ...list]
    hasMore.value = list.length >= PAGE_SIZE
  } catch {
    // 静默失败：已经加载出来的内容仍然可读，用户再点一次就能重试。
  } finally {
    loadingMore.value = false
  }
}

// 每次打开都重新拉第一页：老师是来看"刚才发生了什么"的，
// 停留期间新产生的几条恰恰是最需要看到的。
watch(open, (isOpen) => {
  if (!isOpen) return
  events.value = []
  hasMore.value = false
  void load()
})
</script>

<template>
  <el-drawer v-model="open" title="操作时间线" size="460px">
    <div v-loading="loading" class="min-h-[120px]">
      <el-alert v-if="failed" type="error" :closable="false" show-icon title="加载时间线失败">
        <template #default>
          <el-button text size="small" class="mt-1" @click="load">重试</el-button>
        </template>
      </el-alert>

      <el-empty
        v-else-if="isEmpty"
        :description="props.title ? `《${props.title}》还没有操作记录` : '还没有操作记录'"
        :image-size="72"
      />

      <template v-else-if="events.length">
        <el-timeline>
          <el-timeline-item
            v-for="event in events"
            :key="event.id"
            :timestamp="formatTime(event.created_at)"
            placement="top"
            size="normal"
          >
            <div class="text-sm font-medium">{{ labelOf(event.event_type) }}</div>
            <div class="text-xs text-gray-500">{{ event.summary }}</div>
            <div class="text-xs text-gray-400">{{ event.actor || `用户 #${event.actor_id}` }}</div>
          </el-timeline-item>
        </el-timeline>

        <div v-if="hasMore" class="flex justify-center pb-2">
          <el-button text size="small" :loading="loadingMore" @click="loadMore">加载更早</el-button>
        </div>
      </template>
    </div>
  </el-drawer>
</template>
