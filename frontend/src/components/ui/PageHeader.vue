<script setup lang="ts">
defineProps<{
  title: string
  description?: string
}>()
</script>

<template>
  <header class="flex flex-wrap items-start justify-between gap-3 pb-1">
    <div class="min-w-0">
      <h1 class="truncate text-lg font-semibold tracking-tight text-ink">{{ title }}</h1>
      <p v-if="description" class="mt-1 max-w-[65ch] text-sm text-ink-subtle">
        {{ description }}
      </p>
    </div>
    <div class="flex shrink-0 items-center gap-2">
      <slot name="actions" />
    </div>
  </header>
</template>