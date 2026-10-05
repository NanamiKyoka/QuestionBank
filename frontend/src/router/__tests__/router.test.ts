// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { systemApi } from '@/api'
import { markSetupComplete, router } from '@/router'
import { useAuthStore } from '@/stores/auth'

vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    systemApi: { ...actual.systemApi, setupStatus: vi.fn() },
    authApi: { ...actual.authApi, me: vi.fn(), permissions: vi.fn(), logout: vi.fn() },
  }
})

/**
 * 守卫里的状态是模块级的（探测过一次就不再探测），
 * 所以这些用例必须按顺序跑：第一条消耗掉"未初始化"的探测，
 * 之后用 markSetupComplete() 把系统切回"已装好"。
 */
setActivePinia(createPinia())

describe('路由守卫', () => {
  it('系统未初始化时把任何页面都带去安装向导', async () => {
    vi.mocked(systemApi.setupStatus).mockResolvedValue({
      needs_setup: true,
      registration_enabled: true,
      app_name: 'QuestionMind',
      version: 'test',
    })

    await router.push('/questions')

    expect(router.currentRoute.value.name).toBe('setup')
  })

  it('装好之后，安装向导自己要退出去登录页', async () => {
    markSetupComplete()

    // 先离开安装页：vue-router 对同址 push 判为冗余，压根不会跑守卫。
    await router.push('/login')
    await router.push('/setup')

    expect(router.currentRoute.value.name).toBe('login')
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('未登录访问受保护页面会被送去登录页，并记住原本要去哪', async () => {
    await router.push('/questions')

    expect(router.currentRoute.value.name).toBe('login')
    expect(router.currentRoute.value.query.redirect).toBe('/questions')
  })

  it('已经登录还去登录页就直接回首页', async () => {
    const auth = useAuthStore()
    auth.setToken('token')
    auth.loaded = true

    await router.push('/login')

    expect(router.currentRoute.value.name).toBe('dashboard')
  })

  it('非管理员进不了管理页，会被带回首页', async () => {
    const auth = useAuthStore()
    auth.permissions = {
      is_superuser: false,
      accessible_subject_ids: null,
      memberships: [],
      capabilities: [],
    }

    await router.push('/settings')

    expect(router.currentRoute.value.name).toBe('dashboard')
  })
})
