<script setup lang="ts">
withDefaults(defineProps<{ rows?: number; cols?: number }>(), { rows: 6, cols: 5 })
</script>

<template>
  <div class="space-y-2 p-4" aria-busy="true" aria-live="polite">
    <span class="sr-only">加载中</span>
    <div v-for="row in rows" :key="row" class="flex items-center gap-3">
      <el-skeleton :rows="0" animated class="flex-1">
        <template #template>
          <div class="flex w-full items-center gap-3">
            <el-skeleton-item variant="text" class="!h-3 w-1/3" />
            <el-skeleton-item
              v-for="col in cols - 1"
              :key="col"
              variant="text"
              class="!h-3 flex-1"
            />
          </div>
        </template>
      </el-skeleton>
    </div>
  </div>
</template>

<style scoped>
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}
</style>