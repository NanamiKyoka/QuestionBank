import { del, get, post, type PageResult } from './http'
import type { QuestionBrief } from './types'

/**
 * 导入任务的类型与接口。
 *
 * 单独成文件而不是并进 api/ai.ts：`ai.ts` 讲的是"和模型对话"，
 * 这里讲的是"导入任务的生命周期"——两者会各自演化，
 * 混在一个文件里迟早互相牵连。
 *
 * ⚠️ 语义提醒（与后端 model.ImportTask 的注释同源）：
 * 抽取目前是**同步**执行的，队列里没有 worker 在消费。
 * 因此 retry 只是把任务复位成"等待处理"，cancel 只是**放弃**这条记录，
 * 两者都不会中断任何正在进行的抽取。`queueStatus().worker_online` 恒为 false。
 */

export type ImportTaskStatus = 'pending' | 'processing' | 'completed' | 'failed' | 'cancelled'

export type ImportTaskSource = 'manual' | 'upload' | 'paper' | 'ai_assist'

export interface ImportTask {
  id: number
  subject_id: number | null
  user_id: number
  source: ImportTaskSource
  status: ImportTaskStatus
  status_label: string
  /** 这次导入的是什么：原始文件名，或服务端生成的可读描述（如「粘贴文本」）。 */
  original_filename: string
  file_type: string
  mode: string
  total_count: number
  saved_count: number
  failed_count: number
  /** 已执行次数。 */
  attempt: number
  /** 由服务端按状态机算好，界面据此决定是否亮起按钮。 */
  retryable: boolean
  cancellable: boolean
  error_message?: string
  created_at: string
  started_at: string | null
  finished_at: string | null
}

export interface ImportStatusCounts {
  pending: number
  processing: number
  completed: number
  failed: number
  cancelled: number
  total: number
}

export interface ImportQueueItem {
  task_id: number
  original_filename: string
  status: ImportTaskStatus
  status_label: string
  /** FIFO 位置，1 表示下一个会被执行。 */
  queue_position: number
  attempt: number
  created_at: string
}

export interface ImportQueueStatus {
  stats: ImportStatusCounts
  active_tasks: ImportQueueItem[]
  /** 恒为 false：抽取同步执行，没有后台 worker 消费队列。 */
  worker_online: boolean
  /** 面向用户的一句话说明（当前是"队列不会自动开始"）。 */
  hint: string
}

export const importTaskApi = {
  /** 导入历史。当前用户只看得到自己的记录，管理员默认看全部。 */
  list: (
    subjectId: number,
    params: { status?: ImportTaskStatus | ''; user_id?: number; page?: number; page_size?: number } = {},
  ) => get<PageResult<ImportTask>>(`/subjects/${subjectId}/imports`, params),

  /** 放弃任务：把它移出队列并标记为已放弃（不中断任何进行中的抽取）。 */
  cancel: (subjectId: number, taskId: number) =>
    post<ImportTask>(`/subjects/${subjectId}/imports/${taskId}/cancel`),

  /** 重新排队。没有 worker 时不会自动重跑，重跑请回到智能录题页。 */
  retry: (subjectId: number, taskId: number) =>
    post<ImportTask>(`/subjects/${subjectId}/imports/${taskId}/retry`),

  /** 删除记录（不动题目）。 */
  remove: (subjectId: number, taskId: number) => del<void>(`/subjects/${subjectId}/imports/${taskId}`),

  queueStatus: (subjectId: number) => get<ImportQueueStatus>(`/subjects/${subjectId}/imports/queue-status`),

  /** 这次导入产生的题目。 */
  questions: (
    subjectId: number,
    taskId: number,
    params: { page?: number; page_size?: number } = {},
  ) => get<PageResult<QuestionBrief>>(`/subjects/${subjectId}/imports/${taskId}/questions`, params),
}
