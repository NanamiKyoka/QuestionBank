<script setup lang="ts">
/**
 * AI 改动的确认条。
 *
 * 存在的唯一理由：**AI 的改动不许悄悄落笔**。
 * 组卷工具算出的是一份"如果用户点应用才会发生"的候选树，
 * 这条 bar 就是那份候选的出口——用户看得见改了什么，才能决定留不留。
 */
import { ref } from 'vue'
import type { PaperNodeChange } from '@/composables/usePaperAiTools'

defineProps<{
  summary: string
  changes: PaperNodeChange[]
  applying?: boolean
}>()

const emit = defineEmits<{ apply: []; discard: [] }>()

const expanded = ref(false)

const KIND_LABELS: Record<PaperNodeChange['kind'], string> = {
  added: '新增',
  removed: '删除',
  moved: '移动',
  modified: '修改',
}
</script>

<template>
  <div class="rounded border border-accent-border bg-accent-soft px-3 py-2 text-sm">
    <div class="flex flex-wrap items-center gap-2">
      <el-icon class="text-accent-text"><MagicStick /></el-icon>
      <div class="flex min-w-0 flex-1 flex-col">
        <span class="font-medium">助手改动了 {{ changes.length }} 处，尚未应用</span>
        <span class="truncate text-xs text-ink-muted">{{ summary }}</span>
      </div>
      <el-button text size="small" @click="expanded = !expanded">
        {{ expanded ? '收起' : '查看改动' }}
      </el-button>
      <el-button size="small" :disabled="applying" @click="emit('discard')">放弃</el-button>
      <el-button size="small" type="primary" :loading="applying" @click="emit('apply')">
        应用
      </el-button>
    </div>

    <ul v-if="expanded" class="mt-2 space-y-1 border-t border-accent-border pt-2">
      <li v-for="change in changes" :key="`${change.kind}-${change.nodeId}`" class="flex items-start gap-2">
        <el-tag size="small" effect="plain" class="mt-0.5 shrink-0">{{ KIND_LABELS[change.kind] }}</el-tag>
        <span class="min-w-0 flex-1 break-words text-xs text-ink-regular">{{ change.label }}</span>
      </li>
    </ul>

    <div class="mt-1 text-xs text-ink-subtle">
      应用后会立即保存这份试卷；如果期间别人也改过，保存会被拦下并交给你决定。
    </div>
  </div>
</template>
