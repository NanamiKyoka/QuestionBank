/**
 * 与后端 DTO 对齐的类型定义。
 *
 * 字段命名保持后端的 snake_case，不做 camelCase 转换：
 * 转换层会带来"这个字段到底是哪种写法"的持续困惑，
 * 而两边完全一致时，对照后端代码排查问题的成本最低。
 */

// ---- 通用 ----

export type QuestionType =
  | 'single_choice'
  | 'multiple_choice'
  | 'fill_blank'
  | 'true_false'
  | 'essay'

export type QuestionStatus = 'draft' | 'pending' | 'published' | 'archived'
export type Visibility = 'public' | 'private'
export type SubjectRole = 'viewer' | 'editor' | 'manager'
export type PaperScope = 'personal' | 'shared'
export type PaperStatus = 'draft' | 'finalized' | 'archived'
export type Permission =
  | 'view_question'
  | 'edit_question'
  | 'review_question'
  | 'manage_subject'
  | 'manage_members'
  | 'view_private_any'

/** 富文本文档（Tiptap 兼容）。具体结构由编辑器决定，这里只约束外壳。 */
export interface RichDoc {
  type: 'doc'
  content?: unknown[]
}

// ---- 用户与权限 ----

export interface Membership {
  subject_id: number
  subject_name: string
  subject_code: string
  role: SubjectRole
  role_label: string
  permissions: Permission[]
}

export interface User {
  id: number
  username: string
  nickname: string
  email: string
  avatar: string
  is_superuser: boolean
  is_active: boolean
  last_login_at: string | null
  login_count: number
  last_subject_id: number | null
  created_at: string
  memberships?: Membership[]
}

export interface LoginResponse {
  access_token: string
  token_type: string
  expires_at: string
  user: User
}

export interface MyPermissions {
  is_superuser: boolean
  /** null 表示不受限（管理员）。 */
  accessible_subject_ids: number[] | null
  memberships: Membership[]
  capabilities: string[]
}

export interface SetupStatus {
  needs_setup: boolean
  registration_enabled: boolean
  app_name: string
  version: string
}

export interface SystemVersion {
  version: string
  commit: string
  build_time: string
  go_version: string
  platform: string
}

// ---- 学科 / 知识点 / 标签 ----

export interface Subject {
  id: number
  name: string
  code: string
  description: string
  review_threshold: number
  sort_order: number
  created_by: number
  role: SubjectRole
  role_label: string
  created_at: string
}

export interface SubjectMember {
  user_id: number
  username: string
  nickname: string
  avatar: string
  role: SubjectRole
  role_label: string
  joined_at: string
}

export interface KnowledgePoint {
  id: number
  subject_id: number
  parent_id: number | null
  name: string
  description: string
  path: string
  level: number
  sort_order: number
  question_count: number
  children?: KnowledgePoint[]
  created_at: string
}

export interface TagBrief {
  id: number
  name: string
  color: string
}

export interface KnowledgePointBrief {
  id: number
  name: string
  path: string
}

export interface Tag {
  id: number
  subject_id: number
  category_id: number | null
  name: string
  color: string
  sort_order: number
  category_name?: string
  question_count: number
  created_at: string
}

export interface TagCategory {
  id: number
  subject_id: number
  name: string
  sort_order: number
  tag_count: number
  created_at: string
}

// ---- 题目 ----

export interface Option {
  id: string
  label: string
  content: RichDoc | null
}

/**
 * 答案的判别联合，用 kind 区分结构。
 *
 * `pending` 表示这道题还没有答案：智能录题的「仅录题」模式只录题目，
 * 答案与解析留给老师事后填写。它与 `unresolved` 相反——后者带着一段
 * 读不懂的原文，是需要人收拾的残局。
 */
export interface AnswerSpec {
  kind: 'single' | 'multiple' | 'boolean' | 'blanks' | 'reference' | 'pending' | 'unresolved'
  correct?: string[]
  bool_answer?: boolean | null
  blanks?: Blank[]
  reference?: RichDoc | null
  raw?: string
}

export interface Blank {
  id: string
  /** 外层是"或"，内层是"与"。 */
  accepts: RichDoc[][]
}

export interface QuestionBrief {
  id: number
  subject_id: number
  parent_id: number | null
  question_type: QuestionType
  question_type_label: string
  difficulty: number
  status: QuestionStatus
  status_label: string
  visibility: Visibility
  /** 题干纯文本投影，只用于检索与不需要排版的地方。 */
  stem_text: string
  /**
   * 题干富文本，列表用它渲染。
   *
   * 列表曾经只显示 `stem_text`，而那是把公式摊平的纯文本：
   * "已知平面向量 $\boldsymbol{a}$" 在题库里显示成字面的 \boldsymbol{a}。
   * 详情接口不带这个字段（那里有 stem）。
   */
  stem_doc?: RichDoc | null
  tags: TagBrief[]
  knowledge_points: KnowledgePointBrief[]
  child_count: number
  /** 已通过的审核次数，配合学科阈值显示「还差几次复核」。 */
  review_count: number
  source: string
  created_by: number
  created_at: string
  updated_at: string
}

export interface QuestionPermissions {
  can_edit: boolean
  can_delete: boolean
  can_review: boolean
}

export interface QuestionDetail extends QuestionBrief {
  stem: RichDoc
  options: Option[]
  answer: AnswerSpec
  analysis: RichDoc | null
  solution: RichDoc | null
  summary: RichDoc | null
  content_version: number
  content_revision: number
  review_count: number
  children: QuestionBrief[]
  permissions: QuestionPermissions
}

/**
 * 题库内 AI 解题的结果。
 *
 * 三种 status 都是**成功响应**：逐题循环调用时，一道题答不出来
 * （模型抽风、空位对不上）不该让后面九道一起做不下去。
 * 真正的硬错误（没权限、题目不存在、没配模型）才会以 HTTP 错误返回。
 */
export interface SolveResult {
  question_id: number
  status: 'solved' | 'skipped' | 'failed'
  message?: string
  answer: AnswerSpec
  analysis?: RichDoc | null
  /** 模型给的解析原文，富文本不便在弹窗里预览时用它。 */
  analysis_markdown?: string
  /** 人可读的答案摘要，例如 "B"、"第1空 3；第2空 x=1"。 */
  answered_text?: string
  warnings?: string[]
  /** 写入后的题目详情，用来就地刷新列表行与详情抽屉。 */
  question?: QuestionDetail
  model?: string
}

/**
 * 题目排序方式，必须与后端白名单逐字一致。
 *
 * 白名单是 `validQuestionSorts`（backend/internal/model/question.go:208-215），
 * `Normalize()`（同文件 :225-236）遇到白名单外的值会**静默回落**到 created_desc——
 * 早期这里既不报错也不打日志，于是前端传 'updated_at_desc' 时，
 * 界面显示「更新于」而实际按创建时间排序，没有任何异常可见。
 * 用联合类型把这份契约钉在编译期：拼错一个键就是 typecheck 失败，
 * 而不是线上悄悄排错序。
 *
 * 新增取值前先改后端白名单，否则这里会先红。
 */
export type QuestionSort =
  | 'created_desc'
  | 'created_asc'
  | 'updated_desc'
  /** `updated_desc` 的历史别名，后端刻意保留（两种写法产生同一条 ORDER BY）。 */
  | 'updated_at_desc'
  | 'difficulty_asc'
  | 'difficulty_desc'

/** 列表查询参数。 */
export interface QuestionQuery {
  keyword?: string
  type?: QuestionType | ''
  status?: QuestionStatus | ''
  difficulty?: number | null
  difficulty_min?: number | null
  difficulty_max?: number | null
  /**
   * 知识点多选。后端接受重复的 `knowledge_point_ids` 参数，
   * 单值的 `knowledge_point_id` 仍作为别名保留给老调用方。
   */
  knowledge_point_ids?: number[]
  /** @deprecated 单值别名，新代码请用 knowledge_point_ids。 */
  knowledge_point_id?: number | null
  include_sub_knowledge?: boolean
  tag_ids?: number[]
  tag_match?: 'and' | 'or'
  created_by?: number | null
  /** 来源模糊匹配（原始文件名等）。 */
  source?: string
  /** 精确匹配已通过的审核次数；0 = 还没被任何人审过。 */
  review_count?: number | null
  /** 被此人审核过。 */
  reviewer_id?: number | null
  /**
   * 按导入批次筛选。
   *
   * 后端字段由另一条工作流补齐（见 QuestionFilter.ImportTaskID）；
   * 类型与透传先就位，参数在服务端支持之前不会生效。
   */
  import_task_id?: number | null
  root_only?: boolean
  /** 混入已软删除的题目。与 only_deleted 互斥，同时传以后端为准。 */
  include_deleted?: boolean
  /** 只返回已软删除的题目——回收站列表用它（见 views/RecycleBinView.vue）。 */
  only_deleted?: boolean
  sort?: QuestionSort
  page?: number
  page_size?: number
}

export interface OptionInput {
  label: string
  content: RichDoc
}

export interface QuestionCreatePayload {
  question_type: QuestionType
  stem: RichDoc
  /** 母题。null / 不传 = 独立题。 */
  parent_id?: number | null
  options?: OptionInput[]
  answer: AnswerSpec
  analysis?: RichDoc | null
  solution?: RichDoc | null
  summary?: RichDoc | null
  difficulty?: number
  visibility?: Visibility
  tag_ids?: number[]
  knowledge_point_ids?: number[]
  source?: string
  status?: QuestionStatus
}

export type QuestionUpdatePayload = Partial<QuestionCreatePayload> & {
  /**
   * 改挂母题：`0` 表示解除挂载（变回独立题），`null`/不传表示不动。
   *
   * 三种含义必须分得开，裸 number 做不到，所以用 `number | null` 在类型上钉住。
   */
  parent_id?: number | null
  change_note?: string
}

/** 批量审核动作，与单题的 submit / review 同一套语义。 */
export type QuestionBatchAction = 'submit' | 'approve' | 'reject'

export interface QuestionBatchResult {
  handled_count: number
  skipped_count: number
  skipped_ids: number[]
}

export interface ReviewLog {
  id: number
  action: string
  from_status: QuestionStatus
  to_status: QuestionStatus
  comment: string
  reviewer_id: number
  reviewer: string
  created_at: string
}

// ---- 统计 ----

export interface SubjectStats {
  subject_id: number
  subject_name: string
  questions: {
    total: number
    draft: number
    pending: number
    published: number
    archived: number
    by_type: Record<string, number>
    by_difficulty: Record<string, number>
  }
  knowledge_points: number
  tags: number
  papers: number
  papers_by_status: Record<string, number>
  import_total: number
  import_saved: number
  question_trend: { day: string; total: number }[]
  recent_papers: { id: number; title: string; status: string; updated_at: string }[]
}

export interface GlobalStats {
  subjects: number
  questions: number
  knowledge_points: number
  tags: number
  papers: number
  users: number
  ai: {
    runs: number
    prompt_tokens: number
    completion_tokens: number
    total_tokens: number
  }
  top_actions: { action: string; total: number }[]
}
