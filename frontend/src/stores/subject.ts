import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { authApi, subjectApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import type { Permission, Subject } from '@/api/types'

/**
 * 当前学科上下文。
 *
 * 除了"题库总览"这类跨学科页面，几乎所有功能都在某个学科下工作，
 * 因此把"当前学科"提到全局状态，避免每个页面各自维护一份。
 */
export const useSubjectStore = defineStore('subject', () => {
  const auth = useAuthStore()

  const subjects = ref<Subject[]>([])
  const currentId = ref<number | null>(null)
  const loading = ref(false)

  const current = computed(() => subjects.value.find((s) => s.id === currentId.value) ?? null)
  const hasSubjects = computed(() => subjects.value.length > 0)
  const currentRole = computed(() => current.value?.role ?? null)

  async function load() {
    loading.value = true
    try {
      subjects.value = await subjectApi.list()
      currentId.value = pickInitial(subjects.value)
    } finally {
      loading.value = false
    }
  }

  /** 决定进入应用时默认选中哪个学科。 */
  function pickInitial(list: Subject[]): number | null {
    if (list.length === 0) return null

    // 优先级：本地记住的 > 当前选中的 > 第一个可见的。
    //
    // 本地记住的值可能已经不可见（被移出学科、或换了账号），
    // 所以必须校验它仍在列表里——否则会选中一个用户打不开的学科，
    // 表现为每个请求都 403。
    const remembered = readRemembered()
    if (remembered !== null && list.some((s) => s.id === remembered)) {
      return remembered
    }
    if (currentId.value !== null && list.some((s) => s.id === currentId.value)) {
      return currentId.value
    }
    return list[0]?.id ?? null
  }

  function readRemembered(): number | null {
    const raw = localStorage.getItem('qm_last_subject')
    if (!raw) return null
    const parsed = Number(raw)
    return Number.isFinite(parsed) && parsed > 0 ? parsed : null
  }

  /** 切换学科，并让后端记住，下次登录直接进入该学科。 */
  async function switchTo(id: number) {
    if (currentId.value === id) return
    currentId.value = id
    localStorage.setItem('qm_last_subject', String(id))
    // 记住偏好失败不影响使用，静默忽略。
    void authApi.setLastSubject(id).catch(() => undefined)
  }

  /**
   * 当前学科的权限判定。
   *
   * 包装一层是为了让页面写 `subject.can('edit_question')`，
   * 不必每次手动传 currentId——漏传会被判成"无权限"，
   * 这种 bug 表现为按钮莫名消失，很难排查。
   */
  function can(permission: Permission): boolean {
    if (auth.isSuperuser) return true
    if (currentId.value === null) return false
    return auth.can(permission, currentId.value)
  }

  return {
    subjects,
    currentId,
    current,
    currentRole,
    loading,
    hasSubjects,
    load,
    switchTo,
    can,
  }
})
