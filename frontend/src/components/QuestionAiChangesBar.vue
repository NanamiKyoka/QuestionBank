<script setup lang="ts">
/**
 * AI 改动题目的确认条。
 *
 * 与 CompositionAiChangesBar 是同一套交互，但展示的是题库里的题目：
 * 一条改动对应一道题，而一道题可能同时改多个字段，因此每行给的是
 * 「字段：旧 → 新」而不是一个笼统的标签。
 */
import { ref } from 'vue'
import type { QuestionEditLine } from '@/composables/useQuestionAiTools'

defineProps<{
  summary: string
  lines: QuestionEditLine[]
  applying?: boolean
}>()

const emit = defineEmits<{
  apply: []
  discard: []
  inspect: [questionId: number]
}>()

const expanded = ref(false)

const OP_LABELS: Record<QuestionEditLine['op'], string> = {
  update: '修改',
  archive: '归档',
  delete: '删除',
}
</script>

<template>
  <div class="rounded border border-purple-300 bg-purple-50 px-3 py-2 text-sm">
    <div class="flex flex-wrap items-center gap-2">
      <el-icon class="text-purple-500"><MagicStick /></el-icon>
      <div class="flex min-w-0 flex-1 flex-col">
        <span class="font-medium">助手改动了 {{ lines.length }} 道题，尚未应用</span>
        <span class="truncate text-xs text-gray-500">{{ summary }}</span>
      </div>
      <el-button text size="small" @click="expanded = !expanded">
        {{ expanded ? '收起' : '查看改动' }}
      </el-button>
      <el-button size="small" :disabled="applying" @click="emit('discard')">放弃</el-button>
      <el-button size="small" type="primary" :loading="applying" @click="emit('apply')">
        应用
      </el-button>
    </div>

    <ul v-if="expanded" class="mt-2 max-h-64 space-y-1 overflow-y-auto border-t border-purple-200 pt-2">
      <li
        v-for="line in lines"
        :key="line.key"
        class="flex items-start gap-2"
      >
        <el-tag
          size="small"
          effect="plain"
          class="mt-0.5 shrink-0"
          :type="line.op === 'delete' ? 'danger' : line.op === 'archive' ? 'warning' : 'primary'"
        >
          {{ OP_LABELS[line.op] }}
        </el-tag>
        <div class="min-w-0 flex-1">
          <div class="text-xs text-gray-700">{{ line.stem }}</div>
          <div
            v-for="(label, i) in line.labels"
            :key="i"
            class="break-words text-xs text-gray-500"
          >
            {{ label }}
          </div>
        </div>
        <el-button text size="small" @click="emit('inspect', line.questionId)">查看</el-button>
      </li>
    </ul>

    <div class="mt-1 text-xs text-gray-400">
      应用后会立刻写进题库；改动题干会让引用了这些题的试卷提示「题目已更新」。
    </div>
  </div>
</template>
