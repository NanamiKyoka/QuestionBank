import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { authApi } from '@/api'
import { setAccessToken } from '@/api/http'
import type { MyPermissions, Permission, SubjectRole, User } from '@/api/types'

const TOKEN_KEY = 'qm_token'

/**
 * 认证与权限。
 *
 * 权限判定的唯一依据是后端返回的 /auth/me/permissions，
 * 前端**不自行推导角色**——否则后端改了权限映射而前端没跟上，
 * 就会出现"按钮能点但接口 403"这种令人困惑的状态。
 */
export const useAuthStore = defineStore('auth', () => {
  const token = ref<string | null>(localStorage.getItem(TOKEN_KEY))
  const user = ref<User | null>(null)
  const permissions = ref<MyPermissions | null>(null)
  const loaded = ref(false)

  // 先把已存的 token 注入 axios，否则刷新页面后的第一个请求会漏掉鉴权头。
  setAccessToken(token.value)

  const isAuthenticated = computed(() => !!token.value)
  const isSuperuser = computed(() => permissions.value?.is_superuser ?? false)
  const displayName = computed(() => user.value?.nickname || user.value?.username || '')

  function setToken(value: string | null) {
    token.value = value
    setAccessToken(value)
    if (value) {
      localStorage.setItem(TOKEN_KEY, value)
    } else {
      localStorage.removeItem(TOKEN_KEY)
    }
  }

  async function login(username: string, password: string) {
    const result = await authApi.login(username, password)
    setToken(result.access_token)
    user.value = result.user
    permissions.value = await authApi.permissions()
    loaded.value = true
    return result
  }

  /** 应用启动或刷新后恢复登录态。失败时清空 token，让路由守卫送去登录页。 */
  async function restore() {
    if (!token.value) {
      loaded.value = true
      return
    }
    try {
      const [me, perms] = await Promise.all([authApi.me(), authApi.permissions()])
      user.value = me
      permissions.value = perms
    } catch {
      setToken(null)
      user.value = null
      permissions.value = null
    } finally {
      loaded.value = true
    }
  }

  async function refreshPermissions() {
    permissions.value = await authApi.permissions()
  }

  async function logout() {
    try {
      await authApi.logout()
    } catch {
      // 后端调不通也要让本地登出，否则用户会被卡在登录态里。
    }
    setToken(null)
    user.value = null
    permissions.value = null
  }

  /** 角色查询，仅用于展示"你是本学科的什么角色"。 */
  function roleIn(subjectId: number): SubjectRole | null {
    const membership = permissions.value?.memberships.find((m) => m.subject_id === subjectId)
    return membership?.role ?? null
  }

  /** 权限判定。与后端 can() 的语义保持一致：管理员全部通过。 */
  function can(permission: Permission, subjectId?: number): boolean {
    if (isSuperuser.value) return true
    if (!subjectId) return false
    const membership = permissions.value?.memberships.find((m) => m.subject_id === subjectId)
    return membership?.permissions.includes(permission) ?? false
  }

  /** 是否在任一学科拥有某权限，用于决定"新建"类按钮是否显示。 */
  function canAny(permission: Permission): boolean {
    if (isSuperuser.value) return true
    return (
      permissions.value?.memberships.some((m) => m.permissions.includes(permission)) ?? false
    )
  }

  const accessibleSubjects = computed(() => permissions.value?.memberships ?? [])

  return {
    token,
    user,
    permissions,
    loaded,
    isAuthenticated,
    isSuperuser,
    displayName,
    accessibleSubjects,
    setToken,
    login,
    restore,
    refreshPermissions,
    logout,
    roleIn,
    can,
    canAny,
  }
})
