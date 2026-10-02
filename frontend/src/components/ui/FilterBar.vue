<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    modelValue: string
    placeholder?: string
    /** 存在已生效但输入框里看不见的筛选条件时为 true。 */
    dirty?: boolean
  }>(),
  { placeholder: '搜索', dirty: false },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
  search: []
  reset: []
}>()

const keyword = computed({
  get: () => props.modelValue,
  set: (value: string) => emit('update:modelValue', value),
})
</script>

<template>
  <div class="flex flex-wrap items-center gap-2">
    <el-input
      v-model="keyword"
      :placeholder="placeholder"
      clearable
      class="w-56"
      @keyup.enter="emit('search')"
      @clear="emit('search')"
    >
      <template #prefix><el-icon><Search /></el-icon></template>
    </el-input>

    <slot name="filters" />

    <el-button text :disabled="!dirty" @click="emit('reset')">重置</el-button>

    <div class="ml-auto flex items-center gap-2">
      <slot name="actions" />
    </div>
  </div>
</template>