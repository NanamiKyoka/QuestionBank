<script setup lang="ts">
import { computed } from 'vue'
import { useThemeStore, type ThemePreference } from '@/stores/theme'

const theme = useThemeStore()

const current = computed<ThemePreference>(() => theme.preference)

const OPTIONS: { value: ThemePreference; label: string; icon: string }[] = [
  { value: 'light', label: '浅色', icon: 'Sunny' },
  { value: 'dark', label: '深色', icon: 'Moon' },
  { value: 'system', label: '跟随系统', icon: 'Monitor' },
]

const activeLabel = computed(() => OPTIONS.find((o) => o.value === current.value)?.label ?? '')
const activeIcon = computed(() => OPTIONS.find((o) => o.value === current.value)?.icon ?? 'Monitor')
</script>

<template>
  <el-dropdown @command="theme.setPreference">
    <button
      type="button"
      class="flex h-8 w-8 items-center justify-center rounded text-ink-regular transition-colors hover:bg-surface-sunken"
      :aria-label="`主题：${activeLabel}`"
    >
      <el-icon><component :is="activeIcon" /></el-icon>
    </button>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item
          v-for="option in OPTIONS"
          :key="option.value"
          :command="option.value"
          :class="{ 'is-active': option.value === current }"
        >
          <el-icon class="mr-2"><component :is="option.icon" /></el-icon>
          {{ option.label }}
        </el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>

<style scoped>
.is-active {
  color: var(--qm-accent-text);
  font-weight: 500;
}
</style>