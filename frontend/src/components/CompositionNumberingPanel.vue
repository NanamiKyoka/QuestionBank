<script setup lang="ts">
import { ref, watch } from 'vue'
import type { NumberingMode } from '@/api/paper'

/**
 * 题号面板。
 *
 * 题号以前只有"开/关"一个开关，编号由导出器在渲染时现算，
 * 于是卷面上没法出现 1.1 / 2.1，也没法让某个大题从 1 重新开始。
 * 这里补上第二步：选一种编号方式，然后一次性把结果写进各题的
 * `props.number`——写进去之后它就是节点属性，导出与预览都读它。
 *
 * 面板本身不发"改题号"的指令，只发 autofill：
 * 批量重编号是一次性、不可撤销的动作，交给调用方弹确认框，
 * 面板只负责把"用哪种方式"这个选择表达清楚。
 */
const model = defineModel<NumberingMode>('mode', { default: 'global' })

const props = withDefaults(
  defineProps<{
    /** 是否开启题号。 */
    enabled: boolean
    /** 没有编辑权时整体禁用。 */
    disabled?: boolean
    /** 卷面题目数，用来提示"会自动改写几道题"。 */
    questionCount?: number
  }>(),
  { disabled: false, questionCount: 0 },
)

const emit = defineEmits<{
  'update:enabled': [value: boolean]
  autofill: [mode: NumberingMode]
}>()

/** 面板可能被父组件在别处重置，这里跟随外部值。 */
const mode = ref<NumberingMode>(model.value)
watch(model, (value) => (mode.value = value))

function onEnabled(value: string | number | boolean) {
  emit('update:enabled', value === true)
}

function onMode(value: string | number | boolean | undefined) {
  // 单选组只会回 global / heading，其余取值一律按 global 处理，
  // 免得外部存了脏值把编号方式带偏。
  const next: NumberingMode = value === 'heading' ? 'heading' : 'global'
  mode.value = next
  model.value = next
}
</script>

<template>
  <div>
    <div class="flex items-center justify-between">
      <span class="text-sm font-medium">题号</span>
      <el-switch
        :model-value="props.enabled"
        :disabled="props.disabled"
        inline-prompt
        active-text="开"
        inactive-text="关"
        @update:model-value="onEnabled"
      />
    </div>

    <div v-if="props.enabled" class="mt-2 space-y-2 pl-1">
      <el-radio-group :model-value="mode" :disabled="props.disabled" @update:model-value="onMode">
        <div class="flex items-start">
          <el-radio value="global">全局顺序（1、2、3…）</el-radio>
        </div>
        <div class="flex items-start">
          <el-radio value="heading">按二级标题分组（1.1、2.1…）</el-radio>
        </div>
      </el-radio-group>

      <el-button
        size="small"
        :disabled="props.disabled || props.questionCount === 0"
        @click="emit('autofill', mode)"
      >
        <el-icon class="mr-1"><MagicStick /></el-icon>自动填充题号
      </el-button>

      <p class="text-xs text-ink-subtle">
        一次性操作：会整体覆盖
        <span class="font-medium">{{ props.questionCount }}</span>
        道题的题号，之后仍可逐题手改。
      </p>
    </div>
    <p v-else class="mt-1 text-xs text-ink-subtle">
      开启后每道题前显示题号，导出与打印都会带上。
    </p>
  </div>
</template>
