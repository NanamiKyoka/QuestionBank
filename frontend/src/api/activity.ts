import { get, type PageResult } from './http'

/**
 * 操作审计日志。
 *
 * 与后端 schema/activity.go 的 ActivityLogResponse 一一对应。
 * 注意后端比参考项目多记了 method / status_code / duration_ms / user_agent，
 * 这四列在排障时往往比"谁做了什么"更有用，因此都保留在类型里。
 */
export interface ActivityLog {
  id: number
  /** 用户被删除后为 null（username 仍然冗余存着，日志照样可读）。 */
  user_id: number | null
  username: string
  /** 语义化动作名，如 question.create、paper.export。 */
  action: string
  resource: string
  resource_id: string
  method: string
  path: string
  ip: string
  user_agent: string
  status_code: number
  duration_ms: number
  success: boolean
  /** 额外上下文（错误信息、变更字段等），结构由写入方决定，故为 unknown。 */
  detail?: unknown
  created_at: string
}

/**
 * 筛选条件。
 *
 * since / until 必须是 **RFC3339**（如 2024-01-01T00:00:00Z）：
 * 后端 schema.ActivityLogQuery 的两个字段是 *time.Time，
 * gin 的 form 绑定按 RFC3339 解析，传 `2024-01-01 00:00:00` 会被
 * 直接判为 400「查询参数不合法」。
 *
 * success 用 boolean 而不是字符串：后端是 *bool，nil 表示不筛选，
 * 而 http.ts 的 cleanParams 刻意保留 false（它是有意义的取值）。
 */
export interface ActivityLogQuery {
  user_id?: number
  action?: string
  resource?: string
  /** 模糊匹配用户名或请求路径。 */
  keyword?: string
  success?: boolean
  since?: string
  until?: string
  page?: number
  page_size?: number
}

export const activityApi = {
  /** GET /activity-logs —— 仅全局管理员可见。 */
  list: (query: ActivityLogQuery) =>
    get<PageResult<ActivityLog>>('/activity-logs', { ...query }),
}
