<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    level: number
    max?: number
    size?: number
  }>(),
  { max: 5, size: 13 },
)

const filled = computed(() => Math.max(0, Math.min(props.max, Math.round(props.level || 0))))
const label = computed(() => `难度 ${filled.value} / ${props.max}`)
</script>

<template>
  <span class="inline-flex items-center gap-px align-middle text-ink-regular" :aria-label="label" role="img">
    <el-icon
      v-for="n in max"
      :key="n"
      :size="size"
      :class="n <= filled ? 'text-ink-regular' : 'text-ink-subtle/35'"
    >
      <component :is="n <= filled ? 'StarFilled' : 'Star'" />
    </el-icon>
  </span>
</template>