import { ref } from 'vue'
import { defineStore } from 'pinia'

const THEME_KEY = 'qm_theme'
const DARK_CLASS = 'dark'

export type ThemePreference = 'light' | 'dark' | 'system'

function readPreference(): ThemePreference {
  const stored = localStorage.getItem(THEME_KEY)
  return stored === 'light' || stored === 'dark' ? stored : 'system'
}

function prefersDark(): boolean {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

function resolvesToDark(preference: ThemePreference): boolean {
  return preference === 'dark' || (preference === 'system' && prefersDark())
}

/** 在 Vue 挂载之前把主题落到 <html>，否则首屏会闪一下浅色。 */
export function applyInitialTheme() {
  document.documentElement.classList.toggle(DARK_CLASS, resolvesToDark(readPreference()))
}

export const useThemeStore = defineStore('theme', () => {
  const preference = ref<ThemePreference>(readPreference())
  const isDark = ref(resolvesToDark(preference.value))

  function apply() {
    isDark.value = resolvesToDark(preference.value)
    document.documentElement.classList.toggle(DARK_CLASS, isDark.value)
  }

  function setPreference(value: ThemePreference) {
    preference.value = value
    if (value === 'system') {
      localStorage.removeItem(THEME_KEY)
    } else {
      localStorage.setItem(THEME_KEY, value)
    }
    apply()
  }

  // 用户显式选过 light/dark 之后，系统改主题不应该把他们的选择顶掉。
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (preference.value === 'system') apply()
  })

  return { preference, isDark, setPreference }
})