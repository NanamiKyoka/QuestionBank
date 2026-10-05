// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { authApi } from '@/api'
import type { MyPermissions } from '@/api/types'
import { useAuthStore } from '@/stores/auth'

vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    authApi: {
      ...actual.authApi,
      me: vi.fn(),
      permissions: vi.fn(),
      logout: vi.fn(),
    },
  }
})

function perms(partial: Partial<MyPermissions>): MyPermissions {
  return {
    is_superuser: false,
    accessible_subject_ids: null,
    memberships: [],
    capabilities: [],
    ...partial,
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
  vi.mocked(authApi.me).mockReset()
  vi.mocked(authApi.permissions).mockReset()
  vi.mocked(authApi.logout).mockReset()
})

describe('登录态恢复', () => {
  it('没有 token 时不去后端问，只标记恢复完成', async () => {
    const auth = useAuthStore()

    await auth.restore()

    expect(authApi.me).not.toHaveBeenCalled()
    expect(auth.loaded).toBe(true)
    expect(auth.isAuthenticated).toBe(false)
  })

  it('token 已失效时清空本地登录态，让路由守卫送去登录页', async () => {
    const auth = useAuthStore()
    auth.setToken('stale-token')
    vi.mocked(authApi.me).mockRejectedValue(new Error('401'))
    vi.mocked(authApi.permissions).mockRejectedValue(new Error('401'))

    await auth.restore()

    expect(auth.token).toBeNull()
    expect(localStorage.getItem('qm_token')).toBeNull()
    expect(auth.isAuthenticated).toBe(false)
    // 无论成功失败都必须收尾：守卫靠这个标志决定还要不要重试。
    expect(auth.loaded).toBe(true)
  })

  it('后端调不通也照样本地登出，不会把用户卡在登录态里', async () => {
    const auth = useAuthStore()
    auth.setToken('token')
    vi.mocked(authApi.logout).mockRejectedValue(new Error('网络炸了'))

    await auth.logout()

    expect(auth.token).toBeNull()
    expect(auth.user).toBeNull()
    expect(localStorage.getItem('qm_token')).toBeNull()
  })
})

describe('权限判定', () => {
  it('超管放行；没有指定学科时不替用户猜', () => {
    const auth = useAuthStore()
    auth.permissions = perms({
      memberships: [
        {
          subject_id: 1,
          subject_name: '数学',
          subject_code: 'math',
          role: 'editor',
          role_label: '编辑',
          permissions: ['edit_question', 'review_question'],
        },
        {
          subject_id: 2,
          subject_name: '语文',
          subject_code: 'cn',
          role: 'viewer',
          role_label: '只读',
          permissions: ['view_question'],
        },
      ],
    })

    expect(auth.can('edit_question', 1)).toBe(true)
    expect(auth.can('manage_members', 1)).toBe(false)
    // 没给学科就判不了：漏传会被判成"无权限"，表现为按钮莫名消失。
    expect(auth.can('edit_question')).toBe(false)
    // 权限按学科算，不能从 A 学科带到 B 学科。
    expect(auth.can('edit_question', 2)).toBe(false)

    expect(auth.canAny('edit_question')).toBe(true)
    expect(auth.canAny('manage_members')).toBe(false)

    expect(auth.roleIn(2)).toBe('viewer')
    expect(auth.roleIn(99)).toBeNull()

    auth.permissions = perms({ is_superuser: true })
    expect(auth.can('manage_members', 99)).toBe(true)
    expect(auth.canAny('manage_members')).toBe(true)
  })
})
