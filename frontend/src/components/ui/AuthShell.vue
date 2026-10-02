<script setup lang="ts">
defineProps<{ title: string; subtitle: string }>()
</script>

<template>
  <div class="relative flex h-full items-center justify-center bg-surface-sunken px-4 py-8">
    <div class="absolute right-4 top-4">
      <slot name="corner" />
    </div>
    <el-card class="w-full max-w-[440px]" shadow="never">
      <div class="mb-6 text-center">
        <div
          class="mx-auto mb-3 flex h-11 w-11 items-center justify-center rounded-lg bg-accent text-sm font-bold text-white"
        >
          QM
        </div>
        <h1 class="text-xl font-semibold tracking-tight text-ink">{{ title }}</h1>
        <p class="mt-1.5 text-sm text-ink-muted">{{ subtitle }}</p>
      </div>
      <slot />
    </el-card>
  </div>
</template>