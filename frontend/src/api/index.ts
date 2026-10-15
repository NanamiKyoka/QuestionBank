import { get, patch, post, put, del, AI_TIMEOUT, type PageResult } from './http'
import type {
  KnowledgePoint,
  LoginResponse,
  MyPermissions,
  QuestionBatchAction,
  QuestionBatchResult,
  QuestionBrief,
  QuestionCreatePayload,
  QuestionDetail,
  QuestionQuery,
  QuestionUpdatePayload,
  ReviewLog,
  SetupStatus,
  SolveResult,
  Subject,
  SubjectMember,
  SubjectRole,
  SystemVersion,
  Tag,
  TagCategory,
  User,
} from './types'

// ---- 系统与初始化 ----

export const systemApi = {
  setupStatus: () => get<SetupStatus>('/system/setup-status'),
  version: () => get<SystemVersion>('/system/version'),
  setup: (payload: {
    username: string
    password: string
    nickname?: string
    email?: string
    first_subject_name?: string
  }) => post<LoginResponse>('/auth/setup', payload),
}

// ---- 认证 ----

export const authApi = {
  login: (username: string, password: string) =>
    post<LoginResponse>('/auth/login', { username, password }),
  logout: () => post<void>('/auth/logout'),
  me: () => get<User>('/auth/me'),
  updateMe: (payload: { nickname?: string; email?: string; avatar?: string }) =>
    patch<User>('/auth/me', payload),
  changePassword: (current_password: string, new_password: string) =>
    post<void>('/auth/me/password', { current_password, new_password }),
  setLastSubject: (subject_id: number) => post<void>('/auth/me/last-subject', { subject_id }),
  /** 前端权限判定的唯一真相源。 */
  permissions: () => get<MyPermissions>('/auth/me/permissions'),
}

// ---- 学科与成员 ----

export const subjectApi = {
  list: () => get<Subject[]>('/subjects'),
  get: (id: number) => get<Subject>(`/subjects/${id}`),
  create: (payload: {
    name: string
    code?: string
    description?: string
    review_threshold?: number
  }) => post<Subject>('/subjects', payload),
  update: (
    id: number,
    payload: { name?: string; code?: string; description?: string; review_threshold?: number },
  ) => put<Subject>(`/subjects/${id}`, payload),
  remove: (id: number) => del<void>(`/subjects/${id}`),

  members: (id: number) => get<SubjectMember[]>(`/subjects/${id}/members`),
  upsertMember: (id: number, userId: number, role: SubjectRole) =>
    put<void>(`/subjects/${id}/members/${userId}`, { role }),
  removeMember: (id: number, userId: number) => del<void>(`/subjects/${id}/members/${userId}`),
}

// ---- 知识点 ----

export const knowledgePointApi = {
  tree: (subjectId: number) => get<KnowledgePoint[]>(`/subjects/${subjectId}/knowledge-points`),
  create: (
    subjectId: number,
    payload: { name: string; parent_id?: number | null; description?: string; sort_order?: number },
  ) => post<KnowledgePoint>(`/subjects/${subjectId}/knowledge-points`, payload),
  update: (
    id: number,
    payload: { name?: string; description?: string; sort_order?: number },
  ) => put<KnowledgePoint>(`/knowledge-points/${id}`, payload),
  move: (id: number, parentId: number | null) =>
    patch<KnowledgePoint>(`/knowledge-points/${id}/move`, { parent_id: parentId }),
  remove: (id: number) => del<void>(`/knowledge-points/${id}`),

  vectorStatus: (subjectId: number) =>
    get<{
      total: number
      indexed: number
      missing: number
      ready: boolean
      model_name: string
      needs_rebuild: boolean
      message: string
    }>(`/subjects/${subjectId}/knowledge-points/vector-status`),
  reindex: (subjectId: number, force = false) =>
    post<{ total: number; indexed: number; skipped: number; failed: number; elapsed_ms: number }>(
      `/subjects/${subjectId}/knowledge-points/reindex`,
      { force },
    ),
  search: (subjectId: number, query: string, topK = 8) =>
    post<{ query: string; model_name: string; results: { knowledge_point_id: number; name: string; path: string; score: number }[] }>(
      `/subjects/${subjectId}/knowledge-points/search`,
      { query, top_k: topK },
    ),
}

// ---- 标签 ----

export const tagApi = {
  list: (subjectId: number, categoryId?: number | null) =>
    get<Tag[]>(`/subjects/${subjectId}/tags`, { category_id: categoryId ?? undefined }),
  create: (
    subjectId: number,
    payload: { name: string; category_id?: number | null; color?: string },
  ) => post<Tag>(`/subjects/${subjectId}/tags`, payload),
  update: (
    id: number,
    payload: { name?: string; color?: string; category_id?: number; clear_category?: boolean },
  ) => put<Tag>(`/tags/${id}`, payload),
  remove: (id: number) => del<void>(`/tags/${id}`),

  categories: (subjectId: number) => get<TagCategory[]>(`/subjects/${subjectId}/tag-categories`),
  createCategory: (subjectId: number, name: string) =>
    post<TagCategory>(`/subjects/${subjectId}/tag-categories`, { name }),
  updateCategory: (id: number, payload: { name?: string; sort_order?: number }) =>
    put<TagCategory>(`/tag-categories/${id}`, payload),
  removeCategory: (id: number) => del<void>(`/tag-categories/${id}`),
}

// ---- 题目 ----

export const questionApi = {
  list: (subjectId: number, query: QuestionQuery) =>
    get<PageResult<QuestionBrief>>(`/subjects/${subjectId}/questions`, {
      ...query,
      // 数组参数用逗号拼成重复键，gin 的 form 绑定能解析重复键。
      tag_ids: query.tag_ids?.length ? query.tag_ids : undefined,
      knowledge_point_ids: query.knowledge_point_ids?.length
        ? query.knowledge_point_ids
        : undefined,
    }),

  /**
   * 回收站列表：只返回已软删除的题目。
   *
   * 用 `only_deleted` 而不是 `include_deleted`：后者只是去掉 deleted_at 过滤，
   * 返回的是"活的 + 删掉的"混在一起的合集，而 DeletedAt 是 `json:"-"`，
   * 客户端拿到的每一行都长得一样，分不出哪条已经进了回收站。
   *
   * 后端没有彻底删除（硬删）接口——`DELETE /questions/:id` 不存在，
   * `POST /questions/batch-delete` 也是软删除，所以这里只提供恢复。
   */
  listDeleted: (subjectId: number, query: Omit<QuestionQuery, 'include_deleted'>) =>
    get<PageResult<QuestionBrief>>(`/subjects/${subjectId}/questions`, {
      ...query,
      only_deleted: true,
      tag_ids: query.tag_ids?.length ? query.tag_ids : undefined,
    }),

  get: (id: number) => get<QuestionDetail>(`/questions/${id}`),
  batchRead: (ids: number[]) => post<QuestionDetail[]>('/questions/batch-read', { ids }),
  create: (subjectId: number, payload: QuestionCreatePayload) =>
    post<QuestionDetail>(`/subjects/${subjectId}/questions`, payload),
  update: (id: number, payload: QuestionUpdatePayload) =>
    put<QuestionDetail>(`/questions/${id}`, payload),
  remove: (id: number) => post<{ deleted_count: number }>('/questions/batch-delete', { ids: [id] }),
  batchDelete: (ids: number[]) =>
    post<{ deleted_count: number; skipped_count: number; skipped_ids: number[] }>(
      '/questions/batch-delete',
      { ids },
    ),
  batchUpdateSource: (ids: number[], source: string) =>
    post<{ updated_count: number; skipped_ids: number[] }>('/questions/batch-source', {
      ids,
      source,
    }),

  /**
   * 批量提交审核 / 通过 / 驳回。
   *
   * 权限与状态守卫由服务端逐条判定，混进来的已发布题、别人学科的题会被跳过，
   * 所以调用方要按 `skipped_count` 如实提示，而不是只报成功的那部分。
   */
  batchReview: (ids: number[], action: QuestionBatchAction, comment = '') =>
    post<QuestionBatchResult>('/questions/batch-review', { ids, action, comment }),

  submit: (id: number) => post<QuestionDetail>(`/questions/${id}/submit`),

  /**
   * AI 解题：让模型为这道题补写答案与解析。
   *
   * 默认不动已有答案（`overwrite: false`）——「仅录题」进来的题目是空的，
   * 那才是这个入口的主场；覆盖一份人工核对过的答案是另一回事，
   * 需要调用方显式要求。批量解题由调用方循环调用本接口：
   * 每道题各自落库，中途中断只丢没做到的那几道。
   */
  solve: (id: number, overwrite = false) =>
    post<SolveResult>(`/questions/${id}/solve`, { overwrite }, AI_TIMEOUT),
  review: (id: number, action: 'approve' | 'reject', comment = '') =>
    post<QuestionDetail>(`/questions/${id}/review`, { action, comment }),
  reviewLogs: (id: number) => get<ReviewLog[]>(`/questions/${id}/review-logs`),
  restore: (id: number) => post<void>(`/questions/${id}/restore`),

  /** 归档：仅「已发布 → 已归档」。不递增 content_revision，试卷不会误报题目已更新。 */
  archive: (id: number, comment = '') =>
    post<QuestionDetail>(`/questions/${id}/archive`, { comment }),
  /** 取消归档：回到已发布。 */
  unarchive: (id: number) => post<QuestionDetail>(`/questions/${id}/unarchive`),

  stats: (subjectId: number) =>
    get<{
      total: number
      by_status: Record<string, number>
      by_type: Record<string, number>
    }>(`/subjects/${subjectId}/questions/stats`),
}

// ---- 统计 ----

export const statsApi = {
  subject: (subjectId: number) => get<import('./types').SubjectStats>(`/subjects/${subjectId}/stats`),
  global: () => get<import('./types').GlobalStats>('/stats/overview'),
}

// ---- 用户管理 ----

export const userApi = {
  list: (params: { keyword?: string; subject_id?: number; page?: number; page_size?: number }) =>
    get<PageResult<User>>('/users', params),
  create: (payload: {
    username: string
    password: string
    nickname?: string
    email?: string
    is_superuser?: boolean
    subject_id?: number
    subject_role?: SubjectRole
  }) => post<User>('/users', payload),
  update: (
    id: number,
    payload: {
      nickname?: string
      email?: string
      is_active?: boolean
      is_superuser?: boolean
    },
  ) => put<User>(`/users/${id}`, payload),
  remove: (id: number) => del<void>(`/users/${id}`),
  resetPassword: (id: number, newPassword: string) =>
    post<void>(`/users/${id}/password`, { new_password: newPassword }),
}
