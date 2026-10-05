// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { authApi, subjectApi } from '@/api'
import type { MyPermissions, Subject } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { useSubjectStore } from '@/stores/subject'

vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    subjectApi: { ...actual.subjectApi, list: vi.fn() },
    authApi: { ...actual.authApi, setLastSubject: vi.fn() },
  }
})

function subjectFixture(id: number, name: string): Subject {
  return {
    id,
    name,
    code: `s${id}`,
    description: '',
    review_threshold: 3,
    sort_order: id,
    created_by: 1,
    role: 'editor',
    role_label: '编辑',
    created_at: '2026-01-01T00:00:00Z',
  }
}

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
  vi.mocked(subjectApi.list).mockReset()
  vi.mocked(authApi.setLastSubject).mockReset().mockResolvedValue(undefined)
})

describe('当前学科的初始选择', () => {
  it('没有本地记忆时选第一个学科', async () => {
    vi.mocked(subjectApi.list).mockResolvedValue([subjectFixture(1, '数学'), subjectFixture(2, '语文')])
    const subject = useSubjectStore()

    await subject.load()

    expect(subject.currentId).toBe(1)
  })

  it('记住的学科仍在列表里就沿用它', async () => {
    localStorage.setItem('qm_last_subject', '2')
    vi.mocked(subjectApi.list).mockResolvedValue([subjectFixture(1, '数学'), subjectFixture(2, '语文')])
    const subject = useSubjectStore()

    await subject.load()

    expect(subject.currentId).toBe(2)
  })

  it('记住的学科已经不可见时退回第一个——否则每个请求都 403', async () => {
    localStorage.setItem('qm_last_subject', '99')
    vi.mocked(subjectApi.list).mockResolvedValue([subjectFixture(1, '数学'), subjectFixture(2, '语文')])
    const subject = useSubjectStore()

    await subject.load()

    expect(subject.currentId).toBe(1)
  })

  it('学科列表为空时不猜一个 id 出来', async () => {
    vi.mocked(subjectApi.list).mockResolvedValue([])
    const subject = useSubjectStore()

    await subject.load()

    expect(subject.currentId).toBeNull()
  })
})

describe('切换学科', () => {
  it('写本地记忆并让后端记住，重复切同一个学科不再上报', async () => {
    vi.mocked(subjectApi.list).mockResolvedValue([subjectFixture(1, '数学'), subjectFixture(2, '语文')])
    const subject = useSubjectStore()
    await subject.load()

    await subject.switchTo(2)

    expect(subject.currentId).toBe(2)
    expect(localStorage.getItem('qm_last_subject')).toBe('2')
    expect(authApi.setLastSubject).toHaveBeenCalledWith(2)

    await subject.switchTo(2)
    expect(authApi.setLastSubject).toHaveBeenCalledTimes(1)
  })
})

describe('当前学科的权限判定', () => {
  it('以成员权限为准：超管放行，没选学科时一律拒绝', async () => {
    vi.mocked(subjectApi.list).mockResolvedValue([subjectFixture(1, '数学'), subjectFixture(2, '语文')])
    const subject = useSubjectStore()
    const auth = useAuthStore()
    await subject.load()
    auth.permissions = perms({
      memberships: [
        {
          subject_id: 1,
          subject_name: '数学',
          subject_code: 'math',
          role: 'editor',
          role_label: '编辑',
          permissions: ['edit_question'],
        },
      ],
    })

    expect(subject.can('edit_question')).toBe(true)
    expect(subject.can('manage_members')).toBe(false)

    // 换到没加入的学科：权限不能跟着学科一起"带过去"。
    subject.currentId = 2
    expect(subject.can('edit_question')).toBe(false)

    subject.currentId = null
    expect(subject.can('edit_question')).toBe(false)

    auth.permissions = perms({ is_superuser: true })
    expect(subject.can('manage_members')).toBe(true)
  })
})
