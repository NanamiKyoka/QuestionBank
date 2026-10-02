/**
 * 域逻辑测试：AI 题目改动工具的纯函数与落库编排。
 *
 * describeQuestionEdit 决定用户点「应用」之前看到什么——
 * 一行写错字段名，用户核对的就是错的，那这道闸门就等于没有。
 * applyQuestionEdits 决定真正落库时调用哪些接口、怎么汇报部分失败。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { applyQuestionEdits, describeQuestionEdit, type QuestionEdit } from '../useQuestionAiTools'

const api = vi.hoisted(() => ({
  update: vi.fn(),
  remove: vi.fn(),
  archive: vi.fn(),
}))

vi.mock('@/api', () => ({ questionApi: api }))

function before(partial: Partial<NonNullable<QuestionEdit['before']>> = {}) {
  return {
    stem: '已知 f(x)=x^2',
    difficulty: 2,
    source: '2023',
    tags: ['重点'],
    tag_ids: [1],
    points: ['导数'],
    point_ids: [7],
    status: 'published',
    status_label: '已发布',
    ...partial,
  }
}

describe('describeQuestionEdit', () => {
  it('改字段时逐项给出旧值与新值', () => {
    const lines = describeQuestionEdit({
      op: 'update',
      question_id: 1,
      before: before(),
      payload: { difficulty: 4, source: '2024 全国甲卷', stem_markdown: '已知 f(x)=x^2，求 f(2)' },
    })
    const text = lines.join('\n')
    expect(text).toContain('难度：2 → 4')
    expect(text).toContain('来源：2023 → 2024 全国甲卷')
    expect(text).toContain('题干：')
    expect(text).toContain('求 f(2)')
  })

  it('清空标签与知识点要说成"清空"，而不是"0 个"', () => {
    const text = describeQuestionEdit({
      op: 'update',
      question_id: 1,
      before: before(),
      payload: { tag_ids: [], knowledge_point_ids: [] },
    }).join('\n')
    expect(text).toContain('标签：重点 → 0 个')
    expect(text).toContain('知识点：导数 → 0 个')
  })

  it('原本就是空的要如实显示"（空）"', () => {
    const text = describeQuestionEdit({
      op: 'update',
      question_id: 1,
      before: before({ source: '', tags: [], points: [] }),
      payload: { source: '期中' },
    }).join('\n')
    expect(text).toContain('来源：（空） → 期中')
  })

  it('归档与删除要带上当前状态，用户才看得出这一步的落点', () => {
    expect(
      describeQuestionEdit({ op: 'archive', question_id: 1, before: before() }).join(''),
    ).toContain('已发布')
    expect(
      describeQuestionEdit({ op: 'delete', question_id: 1, before: before() }).join(''),
    ).toContain('回收站')
  })

  it('值没变的改动也要有一行说明，界面上不留空白', () => {
    const text = describeQuestionEdit({
      op: 'update',
      question_id: 1,
      before: before(),
      payload: { fields: ['difficulty'] },
    }).join('')
    expect(text).toContain('难度')
    expect(text).toContain('值未变')
  })
})

describe('applyQuestionEdits', () => {
  beforeEach(() => {
    api.update.mockReset().mockResolvedValue({})
    api.remove.mockReset().mockResolvedValue({})
    api.archive.mockReset().mockResolvedValue({})
  })

  it('按操作类型分别落到对应的接口', async () => {
    const edits: QuestionEdit[] = [
      { op: 'update', question_id: 1, payload: { difficulty: 4, stem: { type: 'doc' } } },
      { op: 'archive', question_id: 2, comment: '过时' },
      { op: 'delete', question_id: 3 },
    ]
    const result = await applyQuestionEdits(edits)

    expect(result).toEqual({ applied: 3, failed: [] })
    expect(api.update).toHaveBeenCalledWith(1, { difficulty: 4, stem: { type: 'doc' } })
    expect(api.archive).toHaveBeenCalledWith(2, '过时')
    expect(api.remove).toHaveBeenCalledWith(3)
  })

  it('标签与知识点是整份替换，载荷里要原样带上', async () => {
    await applyQuestionEdits([
      { op: 'update', question_id: 1, payload: { tag_ids: [], knowledge_point_ids: [7, 8] } },
    ])
    expect(api.update).toHaveBeenCalledWith(1, { tag_ids: [], knowledge_point_ids: [7, 8] })
  })

  it('空改动不发请求——发了也是一次无谓的写入', async () => {
    const result = await applyQuestionEdits([
      { op: 'update', question_id: 1, payload: { fields: ['difficulty'] } },
    ])
    expect(api.update).not.toHaveBeenCalled()
    expect(result.applied).toBe(0)
  })

  it('单条失败不中断整批，失败项要带题目 id', async () => {
    api.update.mockRejectedValueOnce(new Error('状态冲突：只有草稿才能提交审核'))
    const result = await applyQuestionEdits([
      { op: 'update', question_id: 1, payload: { difficulty: 4 } },
      { op: 'update', question_id: 2, payload: { difficulty: 4 } },
    ])

    expect(result.applied).toBe(1)
    expect(result.failed).toHaveLength(1)
    expect(result.failed[0]).toContain('题目 1')
    expect(result.failed[0]).toContain('状态冲突')
    expect(api.update).toHaveBeenCalledTimes(2)
  })

  it('change_note 只在非空时带上', async () => {
    await applyQuestionEdits([
      { op: 'update', question_id: 1, payload: { difficulty: 4, change_note: '  ' } },
    ])
    expect(api.update).toHaveBeenCalledWith(1, { difficulty: 4 })
  })
})
