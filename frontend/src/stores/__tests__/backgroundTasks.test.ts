// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useBackgroundTaskStore } from '@/stores/backgroundTasks'

beforeEach(() => {
  setActivePinia(createPinia())
  vi.useFakeTimers()
})

afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
})

describe('跨页面后台任务状态', () => {
  it('录题任务离开组件后仍在，并按终态停止计时', () => {
    const tasks = useBackgroundTaskStore()

    tasks.beginImport(1, '2026 数学卷.pdf', true)
    expect(tasks.activeImports).toHaveLength(1)
    expect(tasks.importFor(1)?.status).toBe('processing')

    vi.advanceTimersByTime(2_100)
    expect(tasks.importFor(1)?.elapsedSec).toBeGreaterThanOrEqual(2)

    tasks.completeImport(1, 42, 19)
    expect(tasks.activeImports).toHaveLength(0)
    expect(tasks.importFor(1)).toMatchObject({
      status: 'completed',
      taskId: 42,
      total: 19,
    })

    // 已完成的计时器不会继续累加。
    const elapsed = tasks.importFor(1)?.elapsedSec
    vi.advanceTimersByTime(2_000)
    expect(tasks.importFor(1)?.elapsedSec).toBe(elapsed)
  })

  it('AI 解题进度与结果清单由 store 持有，可跨组件恢复', () => {
    const tasks = useBackgroundTaskStore()

    tasks.beginSolve(2, 3)
    tasks.setSolveCurrent(2, '第二题', { type: 'doc' })
    tasks.recordSolveResult(2, {
      id: 2,
      stem: '第二题',
      doc: { type: 'doc' },
      status: 'solved',
      message: '答案：B',
    })

    const restored = tasks.solveFor(2)
    expect(restored).toMatchObject({
      status: 'processing',
      total: 3,
      done: 1,
      current: '第二题',
    })
    expect(restored?.report).toHaveLength(1)

    tasks.requestStopSolve(2)
    tasks.addSkippedByStop(2, 2)
    tasks.finishSolve(2, 'cancelled')

    expect(tasks.activeSolves).toHaveLength(0)
    expect(tasks.solveFor(2)).toMatchObject({
      status: 'cancelled',
      done: 1,
      skippedByStop: 2,
    })

    tasks.clearSolve(2)
    expect(tasks.solveFor(2)).toBeNull()
  })

  it('覆盖已有答案前能移除上一轮的 skipped 结果，避免统计重复', () => {
    const tasks = useBackgroundTaskStore()
    tasks.beginSolve(3, 2)
    tasks.recordSolveResult(3, { id: 1, stem: '一', doc: null, status: 'skipped', message: '' })
    tasks.recordSolveResult(3, { id: 2, stem: '二', doc: null, status: 'solved', message: '' })

    tasks.removeSolveResults(3, new Set([1]))

    expect(tasks.solveFor(3)?.report.map((item) => item.id)).toEqual([2])
    tasks.finishSolve(3)
  })
})
