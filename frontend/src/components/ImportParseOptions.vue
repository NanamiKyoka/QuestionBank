<script setup lang="ts">
import type { ImportMode, ImportParseMethod } from '@/api/importTask'

/**
 * 解析方式与处理模式。
 *
 * 把这两件事放在一起而不是分开放，是因为它们互相约束：
 * 规则解析既不读图片也不解题，选了它之后再让用户去选「只求解」，
 * 得到的是一个注定失败的组合。这里直接把组合讲清楚，
 * 剩下不可行的选项直接关掉，而不是让人点了才吃报错。
 */
withDefaults(
  defineProps<{
    method: ImportParseMethod
    mode: ImportMode
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:method': [value: ImportParseMethod]
  'update:mode': [value: ImportMode]
}>()

/**
 * 各模式的代价与去向。
 *
 * 「仅录题」值得单列一句：省下的不是一点钱，而是整份卷子最慢的那部分输出。
 * 求解才是抽取里的大头（实测约 1370 输出 token/题），把它拿掉之后
 * 一次识别的耗时基本只剩读材料的时间。
 */
const modeHint: Record<ImportMode, string> = {
  extract: '「完整抽取」连材料里印着的答案与解析一起抽。',
  record: '「仅录题」只认题干、选项与知识点，不写答案与解析——题量大或材料本身不带答案时用它。',
  solve: '「只求解」保留你提供的原题，只让模型补上答案与解析。',
}
</script>

<template>
  <div class="space-y-2">
    <div class="text-sm font-medium">解析方式</div>
    <div class="grid gap-2 sm:grid-cols-2">
      <div
        role="radio"
        tabindex="0"
        :aria-checked="method === 'ai'"
        class="cursor-pointer rounded-lg border-2 p-3 transition-colors"
        :class="
          method === 'ai'
            ? 'border-blue-400 bg-blue-50/50'
            : 'border-gray-200 bg-white hover:border-gray-300'
        "
        @click="!disabled && emit('update:method', 'ai')"
        @keydown.enter.prevent="!disabled && emit('update:method', 'ai')"
        @keydown.space.prevent="!disabled && emit('update:method', 'ai')"
      >
        <div class="flex items-center gap-2 text-sm font-medium">
          <span
            class="inline-block h-3.5 w-3.5 shrink-0 rounded-full border"
            :class="method === 'ai' ? 'border-blue-500 bg-blue-500' : 'border-gray-300'"
          />
          模型识别
        </div>
        <div class="mt-1 pl-6 text-xs text-gray-500">
          自动识别各种排版与截图，不要求固定格式。逐题消耗模型额度，耗时随题量增长。
        </div>
      </div>

      <div
        role="radio"
        tabindex="0"
        :aria-checked="method === 'template'"
        class="cursor-pointer rounded-lg border-2 p-3 transition-colors"
        :class="
          method === 'template'
            ? 'border-blue-400 bg-blue-50/50'
            : 'border-gray-200 bg-white hover:border-gray-300'
        "
        @click="!disabled && emit('update:method', 'template')"
        @keydown.enter.prevent="!disabled && emit('update:method', 'template')"
        @keydown.space.prevent="!disabled && emit('update:method', 'template')"
      >
        <div class="flex items-center gap-2 text-sm font-medium">
          <span
            class="inline-block h-3.5 w-3.5 shrink-0 rounded-full border"
            :class="method === 'template' ? 'border-blue-500 bg-blue-500' : 'border-gray-300'"
          />
          规则解析
        </div>
        <div class="mt-1 pl-6 text-xs text-gray-500">
          按标签逐字解析，<span class="text-green-700">不调用任何模型</span>：没配置 AI
          也能录题，零成本且结果可复现。仅认下面的标签格式。
        </div>
      </div>
    </div>

    <!-- 规则解析的格式约定：不说清楚的话，用户只会看到"一道题都没解析出来" -->
    <el-alert v-if="method === 'template'" type="info" :closable="false" show-icon>
      <template #title>规则解析只认标签，不猜排版</template>
      <div class="mt-1 space-y-1 text-xs">
        <div>
          每道题必须以
          <el-tag size="small">【题目】</el-tag>
          开头，其余标签可省略：
          <el-tag size="small" class="ml-1">【选项】</el-tag>
          <el-tag size="small" class="ml-1">【答案】</el-tag>
          <el-tag size="small" class="ml-1">【解析】</el-tag>
          <el-tag size="small" class="ml-1">【答案区】</el-tag>
        </div>
        <div class="text-gray-500">
          没有【题目】标签时解析结果会是空的。规则解析不读图片、也不解题——这两种情况请切回模型识别。
        </div>
      </div>
    </el-alert>

    <div class="pt-1">
      <div class="mb-1 text-sm font-medium">处理模式</div>
      <el-radio-group
        :model-value="mode"
        size="small"
        :disabled="disabled || method === 'template'"
        @update:model-value="emit('update:mode', $event as ImportMode)"
      >
        <el-radio-button value="extract">完整抽取</el-radio-button>
        <el-radio-button value="record">仅录题</el-radio-button>
        <el-radio-button value="solve">只求解</el-radio-button>
      </el-radio-group>
      <div class="mt-1 text-xs text-gray-400">
        {{ method === 'template' ? '「只求解」需要模型补答案，规则解析下不可用。' : modeHint[mode] }}
      </div>
    </div>
  </div>
</template>
