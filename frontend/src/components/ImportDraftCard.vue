<script setup lang="ts">
import { computed } from 'vue'
import type { ImportDraft, ImportDraftNode } from '@/api/importTask'
import DifficultyStars from '@/components/ui/DifficultyStars.vue'
import RichContent from '@/components/RichContent.vue'

/**
 * 审核列表里的一道草稿。
 *
 * 组件**会递归渲染自己**（小问用同一个组件再画一遍），因此材料题
 * 「1 道母题 + 20 道小问」在列表里是真正嵌套的一棵树，而不是 21 张平铺的卡片——
 * 平铺时用户完全看不出哪几道是一组，入库后才发现它们本该是一道材料题。
 *
 * 这里只负责"如实呈现"：勾选、编辑、删除都把下标抛给上层，
 * 组件自己不碰数组。删掉一道题要连带处理它的小问，那是集合操作，放在上层更安全。
 */
const props = withDefaults(
  defineProps<{
    draft: ImportDraft
    /** 在扁平草稿数组里的下标。 */
    index: number
    /** 0 = 独立题/母题，1+ = 小问。 */
    depth?: number
    children?: ImportDraftNode[]
    /** 勾选状态按**下标**查：每道题各自独立，母题勾上不代表小问也勾上了。 */
    isChecked?: (index: number) => boolean
    /**
     * 因为「按来源文件筛选」之外的命中才被带出来（母题本身不匹配，
     * 但它下面有小问匹配）。这时要说明白，否则用户会以为这组题都来自当前文件。
     */
    shownForChild?: boolean
  }>(),
  { depth: 0, children: () => [], isChecked: () => false, shownForChild: false },
)

const emit = defineEmits<{
  toggle: [index: number]
  edit: [index: number]
  remove: [index: number]
}>()

const isChild = computed(() => props.depth > 0)
const checked = computed(() => props.isChecked(props.index))

/**
 * 「原卷第 N 题」只在编号与列表位置**不同时**才显示。
 *
 * 过去 source_number 装的是数组下标，于是同一张卡片上"第 3 题"和
 * "原卷第 3 题"并排出现，标着"原卷"的那一个纯属自说自话。
 * 后端改成真实题号后两者才可能不同；后端还没更新的这段时间，
 * 宁可什么都不显示，也不要显示一个假 provenance。
 */
const sourceNumber = computed(() => {
  const n = props.draft.source_number
  if (!n || n === props.index + 1) return null
  return n
})

function answerText(draft: ImportDraft): string {
  const answer = draft.answer
  if (!answer) return ''
  switch (answer.kind) {
    case 'single':
    case 'multiple':
      return (answer.correct ?? [])
        .map((id) => draft.options?.find((o) => o.id === id)?.label ?? id)
        .join('、')
    case 'boolean':
      return answer.bool_answer ? '正确' : '错误'
    case 'blanks':
      return (answer.blanks ?? []).map((b) => b.accepts.length).join(' / ') + ' 个空'
    case 'reference':
      return '见参考答案'
    case 'pending':
      return '待老师补充'
    default:
      return answer.kind
  }
}
</script>

<template>
  <div
    class="rounded border"
    :class="[
      checked ? 'border-accent-border bg-accent-soft/40' : 'border-hairline',
      isChild ? 'border-l-4 border-l-indigo-300' : '',
    ]"
  >
    <div class="flex items-start gap-2 p-3">
      <el-checkbox
        :model-value="checked"
        class="mt-0.5"
        @change="emit('toggle', index)"
      />

      <div class="min-w-0 flex-1">
        <div class="mb-1 flex flex-wrap items-center gap-2">
          <span class="text-sm font-medium">{{ isChild ? '小问' : '第' }} {{ index + 1 }} {{ isChild ? '' : '题' }}</span>
          <el-tag size="small">{{ draft.type_label || draft.question_type }}</el-tag>
          <DifficultyStars :level="draft.difficulty || 0" />
          <el-tag v-if="sourceNumber" size="small" type="info" effect="plain">
            原卷第 {{ sourceNumber }} 题
          </el-tag>
          <el-tag v-if="draft.source_file" size="small" type="info" effect="plain" class="max-w-40">
            <el-icon class="mr-1"><Document /></el-icon>
            <span class="truncate">{{ draft.source_file }}</span>
          </el-tag>
          <el-tag v-if="children.length" size="small" type="warning">
            材料题 · {{ children.length }} 个小问
          </el-tag>
          <el-tag v-if="shownForChild" size="small" type="info" effect="plain">
            母题本身不属于当前文件，因小问命中而显示
          </el-tag>
        </div>

        <div class="rounded bg-surface p-2">
          <RichContent :doc="draft.stem" />
          <div v-if="draft.options?.length" class="mt-1 space-y-0.5">
            <div v-for="option in draft.options" :key="option.id" class="flex gap-1 text-sm">
              <span class="font-medium">{{ option.label }}.</span>
              <RichContent :doc="option.content" class="flex-1" />
            </div>
          </div>
        </div>

        <div class="mt-1 text-xs">
          <span class="text-ink-muted">答案：</span>
          <!-- 待补充的答案不着绿色：绿色是"识别到了"的信号，
               涂成一样的颜色，一屏题目就分不出哪些答案还欠着。 -->
          <span :class="draft.answer?.kind === 'pending' ? 'text-ink-subtle' : 'text-success-text'">
            {{ answerText(draft) || '未识别' }}
          </span>
        </div>

        <!-- 解析预览 -->
        <el-collapse v-if="draft.analysis" class="mt-1">
          <el-collapse-item title="查看解析">
            <div class="rounded bg-surface-sunken p-2 text-sm">
              <RichContent :doc="draft.analysis" />
            </div>
          </el-collapse-item>
        </el-collapse>

        <!-- 知识点建议：模型自述与向量检索分开呈现 -->
        <div class="mt-2 flex flex-wrap items-center gap-1 text-xs">
          <template v-if="draft.suggested_knowledge_points?.length">
            <span class="text-ink-muted">知识点候选：</span>
            <el-tag
              v-for="s in draft.suggested_knowledge_points"
              :key="s.knowledge_point_id"
              size="small"
              type="success"
              effect="plain"
            >
              {{ s.name }} · {{ (s.score * 100).toFixed(0) }}%
            </el-tag>
          </template>
          <template v-if="draft.knowledge_points?.length">
            <span class="ml-2 text-ink-muted">模型提及：</span>
            <el-tag
              v-for="name in draft.knowledge_points"
              :key="name"
              size="small"
              type="info"
              effect="plain"
            >
              {{ name }}
            </el-tag>
          </template>
        </div>

        <!-- 提示：编辑不消除它——改了也得再看一眼 -->
        <el-alert
          v-if="draft.warnings?.length"
          type="warning"
          :closable="false"
          class="mt-2"
          :title="draft.warnings.join('；')"
        />
      </div>

      <div class="flex shrink-0 flex-col items-end gap-1">
        <el-button text size="small" type="primary" @click="emit('edit', index)">
          <el-icon class="mr-1"><EditPen /></el-icon>
          修改
        </el-button>
        <el-button text size="small" type="danger" @click="emit('remove', index)">移除</el-button>
      </div>
    </div>

    <!-- 小问：缩进 + 竖线，让层级一眼可辨 -->
    <div v-if="children.length" class="border-t border-dashed border-hairline px-3 pb-2 pl-5">
      <div class="space-y-2 pt-2">
        <ImportDraftCard
          v-for="child in children"
          :key="child.index"
          :draft="child.draft"
          :index="child.index"
          :depth="depth + 1"
          :children="child.children"
          :is-checked="isChecked"
          :shown-for-child="shownForChild"
          @toggle="(i: number) => emit('toggle', i)"
          @edit="(i: number) => emit('edit', i)"
          @remove="(i: number) => emit('remove', i)"
        />
      </div>
    </div>
  </div>
</template>
