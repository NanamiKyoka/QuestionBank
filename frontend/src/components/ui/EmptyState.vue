<script setup lang="ts">
defineProps<{
  description: string
  /** 主操作：空状态里最重要的一句话。 */
  actionLabel?: string
  /** 附属说明，解释用户为什么会落进这个状态。 */
  hint?: string
}>()

const emit = defineEmits<{ action: [] }>()
</script>

<template>
  <div class="flex flex-col items-center justify-center gap-3 px-6 py-10 text-center">
    <slot name="illustration">
      <el-icon class="text-3xl text-ink-subtle/50"><Box /></el-icon>
    </slot>

    <p class="text-sm font-medium text-ink-regular">{{ description }}</p>

    <p v-if="hint" class="max-w-[48ch] text-xs leading-relaxed text-ink-subtle">{{ hint }}</p>

    <slot name="default" />

    <el-button v-if="actionLabel" type="primary" @click="emit('action')">
      {{ actionLabel }}
    </el-button>
  </div>
</template>