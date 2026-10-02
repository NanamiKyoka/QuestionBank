<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    label: string
    value: number | string
    /** 语义色。默认跟随强调色。 */
    tone?: 'accent' | 'success' | 'warning' | 'danger' | 'neutral'
    /** 0-100。给了就画一条占比条。 */
    bar?: number
    /** 值为 0 时压低视觉权重。 */
    muted?: boolean
  }>(),
  { tone: 'accent', bar: undefined, muted: false },
)

const FILL: Record<string, string> = {
  accent: 'var(--qm-accent)',
  success: 'var(--qm-success)',
  warning: 'var(--qm-warning)',
  danger: 'var(--qm-danger)',
  neutral: 'var(--qm-ink-subtle)',
}

const fill = computed(() => FILL[props.tone])
</script>

<template>
  <div class="min-w-0">
    <div class="text-xs text-ink-subtle">{{ label }}</div>
    <div
      class="num mt-1 text-2xl font-semibold leading-tight"
      :class="muted ? 'text-ink-subtle/50' : 'text-ink'"
    >
      {{ typeof value === 'number' ? value.toLocaleString('zh-CN') : value }}
    </div>
    <div
      v-if="bar !== undefined"
      class="mt-2 h-1.5 overflow-hidden rounded-full bg-surface-sunken"
      role="presentation"
    >
      <div
        class="h-full rounded-full transition-[width] duration-500"
        :style="{ width: `${Math.max(0, Math.min(100, bar))}%`, backgroundColor: fill }"
      />
    </div>
  </div>
</template>