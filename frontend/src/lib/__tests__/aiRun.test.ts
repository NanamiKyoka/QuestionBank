import { describe, expect, it } from 'vitest'
import { addUsage, stopNotice, usageNote } from '../aiRun'

describe('stopNotice', () => {
  it('正常收尾不插话', () => {
    expect(stopNotice({ stop_reason: 'completed', budget: { max_turns: 5 } })).toBeNull()
    expect(stopNotice({})).toBeNull()
    expect(stopNotice({ stop_reason: 'failed' })).toBeNull()
  })

  it('墙钟超时要说清是时间预算，而不是笼统的"某个预算"', () => {
    const notice = stopNotice({
      stop_reason: 'time_limit',
      budget: { max_turns: 5, max_tool_calls: 20, wall_clock_seconds: 180 },
    })
    expect(notice).toContain('3 分钟')
    expect(notice).toContain('时间预算')
  })

  it('轮次与工具上限分别报出具体数字', () => {
    expect(stopNotice({ stop_reason: 'budget_exhausted', budget: { max_turns: 5 } })).toContain('5 轮')
    expect(stopNotice({ stop_reason: 'tool_limit', budget: { max_tool_calls: 20 } })).toContain('20 次')
  })

  it('预算字段缺失时退回通用说法，不产生 undefined 或 NaN', () => {
    expect(stopNotice({ stop_reason: 'time_limit' })).toBe('已达时间预算，助手被迫停止')
    expect(stopNotice({ stop_reason: 'budget_exhausted', budget: null })).toBe('已达轮次上限，助手被迫停止')
    expect(stopNotice({ stop_reason: 'tool_limit', budget: { max_tool_calls: 0 } })).toBe(
      '已达工具调用上限，助手被迫停止',
    )
  })

  it('取消要显示"已停止生成"', () => {
    expect(stopNotice({ stop_reason: 'canceled' })).toBe('已停止生成')
  })

  it('字段类型不对（脏帧）当作没有理由', () => {
    expect(stopNotice({ stop_reason: 42 })).toBeNull()
    expect(stopNotice({ stop_reason: 'time_limit', budget: 'oops' })).toBe('已达时间预算，助手被迫停止')
  })
})

describe('addUsage / usageNote', () => {
  it('多轮用量累加', () => {
    const first = addUsage(undefined, { prompt_tokens: 1200, completion_tokens: 300 })
    const second = addUsage(first, { prompt_tokens: 800, completion_tokens: 100 })
    expect(second).toEqual({ prompt: 2000, completion: 400 })
  })

  it('缺字段按 0 算，不写出 NaN', () => {
    expect(addUsage(undefined, {})).toEqual({ prompt: 0, completion: 0 })
    expect(addUsage(undefined, { prompt_tokens: 'x' })).toEqual({ prompt: 0, completion: 0 })
  })

  it('一行里同时给出输入与输出', () => {
    expect(usageNote(undefined)).toBeNull()
    expect(usageNote({ prompt: 0, completion: 0 })).toBeNull()
    expect(usageNote({ prompt: 1234, completion: 567 })).toBe('输入 1.2k · 输出 567 tokens')
    expect(usageNote({ prompt: 42000, completion: 700 })).toContain('42k')
  })
})
