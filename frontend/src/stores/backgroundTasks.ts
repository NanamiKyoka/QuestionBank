import { computed, reactive } from 'vue'
import { defineStore } from 'pinia'

export type BackgroundTaskStatus = 'processing' | 'completed' | 'failed' | 'cancelled'

export interface ImportActivity {
  subjectId: number
  label: string
  status: BackgroundTaskStatus
  startedAt: number
  elapsedSec: number
  autoCommit: boolean
  taskId?: number
  total?: number
  error?: string
}

export interface SolveReportItem {
  id: number
  stem: string
  doc: unknown
  status: string
  message: string
}

export interface SolveActivity {
  subjectId: number
  status: BackgroundTaskStatus
  total: number
  done: number
  stopping: boolean
  skippedByStop: number
  current: string
  currentDoc: unknown
  report: SolveReportItem[]
  startedAt: number
}

const importTimers = new Map<number, ReturnType<typeof setInterval>>()
const importAborts = new Map<number, AbortController>()

function stopImportTimer(subjectId: number) {
  const timer = importTimers.get(subjectId)
  if (!timer) return
  clearInterval(timer)
  importTimers.delete(subjectId)
}

function stopAllImportTimers() {
  for (const subjectId of importTimers.keys()) stopImportTimer(subjectId)
}

// 页面卸载时不能取消后台任务；真正退出整个应用时才清掉计时器，
// 否则开发热更新和测试环境会留下永不结束的 interval。
if (typeof window !== 'undefined') {
  window.addEventListener('beforeunload', stopAllImportTimers, { once: true })
}

/**
 * 跨页面的后台任务状态。
 *
 * 它不是“把所有请求塞进 store”：真正发请求的代码仍留在各业务页面里。
 * 这里只负责保存用户离开页面后仍需要看见的状态，因此页面组件卸载不会
 * 带走进度。导入任务最终仍以服务端 ImportTask 为真相；这张表是会话内的
 * 即时视图，用来覆盖“页面已经切走、任务还在跑的这段时间”。
 */
export const useBackgroundTaskStore = defineStore('background-tasks', () => {
  const imports = reactive<Record<number, ImportActivity>>({})
  const solves = reactive<Record<number, SolveActivity>>({})

  const activeImports = computed(() =>
    Object.values(imports).filter((run) => run.status === 'processing'),
  )
  const activeSolves = computed(() =>
    Object.values(solves).filter((run) => run.status === 'processing'),
  )
  const activeCount = computed(() => activeImports.value.length + activeSolves.value.length)

  function importFor(subjectId: number | null | undefined): ImportActivity | null {
    if (!subjectId) return null
    return imports[subjectId] ?? null
  }

  function solveFor(subjectId: number | null | undefined): SolveActivity | null {
    if (!subjectId) return null
    return solves[subjectId] ?? null
  }

  function beginImport(subjectId: number, label: string, autoCommit: boolean) {
    importAborts.get(subjectId)?.abort()
    importAborts.delete(subjectId)
    stopImportTimer(subjectId)
    const startedAt = Date.now()
    imports[subjectId] = {
      subjectId,
      label,
      status: 'processing',
      startedAt,
      elapsedSec: 0,
      autoCommit,
    }
    importTimers.set(
      subjectId,
      setInterval(() => {
        const run = imports[subjectId]
        if (!run || run.status !== 'processing') {
          stopImportTimer(subjectId)
          return
        }
        run.elapsedSec = Math.floor((Date.now() - run.startedAt) / 1000)
      }, 1000),
    )
  }

  function completeImport(subjectId: number, taskId: number, total: number) {
    const run = imports[subjectId]
    if (!run) return
    importAborts.delete(subjectId)
    stopImportTimer(subjectId)
    run.status = 'completed'
    run.taskId = taskId
    run.total = total
    run.elapsedSec = Math.floor((Date.now() - run.startedAt) / 1000)
  }

  function failImport(subjectId: number, error: string) {
    const run = imports[subjectId]
    if (!run) return
    importAborts.delete(subjectId)
    stopImportTimer(subjectId)
    run.status = 'failed'
    run.error = error
    run.elapsedSec = Math.floor((Date.now() - run.startedAt) / 1000)
  }

  function cancelImport(subjectId: number) {
    const run = imports[subjectId]
    if (!run) return
    importAborts.delete(subjectId)
    stopImportTimer(subjectId)
    run.status = 'cancelled'
    run.elapsedSec = Math.floor((Date.now() - run.startedAt) / 1000)
  }

  function clearImport(subjectId: number) {
    importAborts.delete(subjectId)
    stopImportTimer(subjectId)
    delete imports[subjectId]
  }

  function setImportAbort(subjectId: number, controller: AbortController) {
    importAborts.set(subjectId, controller)
  }

  function clearImportAbort(subjectId: number) {
    importAborts.delete(subjectId)
  }

  function cancelImportRequest(subjectId: number) {
    importAborts.get(subjectId)?.abort()
    importAborts.delete(subjectId)
    cancelImport(subjectId)
  }

  function beginSolve(subjectId: number, total: number) {
    solves[subjectId] = {
      subjectId,
      status: 'processing',
      total,
      done: 0,
      stopping: false,
      skippedByStop: 0,
      current: '',
      currentDoc: null,
      report: [],
      startedAt: Date.now(),
    }
  }

  function setSolveCurrent(subjectId: number, stem: string, doc: unknown) {
    const run = solves[subjectId]
    if (!run) return
    run.current = stem
    run.currentDoc = doc
  }

  function recordSolveResult(subjectId: number, item: SolveReportItem) {
    const run = solves[subjectId]
    if (!run) return
    run.report.push(item)
    run.done += 1
  }

  function requestStopSolve(subjectId: number) {
    const run = solves[subjectId]
    if (run?.status === 'processing') run.stopping = true
  }

  /** 二次覆盖时把上一轮的 skipped 条目移出报告，避免最终统计重复。 */
  function removeSolveResults(subjectId: number, ids: Set<number>) {
    const run = solves[subjectId]
    if (!run) return
    run.report = run.report.filter((item) => !ids.has(item.id))
  }

  function addSkippedByStop(subjectId: number, count: number) {
    const run = solves[subjectId]
    if (run) run.skippedByStop += count
  }

  function finishSolve(subjectId: number, status: BackgroundTaskStatus = 'completed') {
    const run = solves[subjectId]
    if (!run) return
    run.status = status
    run.current = ''
    run.currentDoc = null
  }

  function clearSolve(subjectId: number) {
    delete solves[subjectId]
  }

  return {
    imports,
    solves,
    activeImports,
    activeSolves,
    activeCount,
    importFor,
    solveFor,
    beginImport,
    completeImport,
    failImport,
    cancelImport,
    clearImport,
    setImportAbort,
    clearImportAbort,
    cancelImportRequest,
    beginSolve,
    setSolveCurrent,
    recordSolveResult,
    requestStopSolve,
    removeSolveResults,
    addSkippedByStop,
    finishSolve,
    clearSolve,
  }
})
