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

/** 答案的判别联合，用 kind 区分结构。 */
export interface AnswerSpec {
  kind: 'single' | 'multiple' | 'boolean' | 'blanks' | 'reference' | 'unresolved'
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
  stem_text: string
  tags: TagBrief[]
  knowledge_points: KnowledgePointBrief[]
  child_count: number
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

/** 列表查询参数。 */
export interface QuestionQuery {
  keyword?: string
  type?: QuestionType | ''
  status?: QuestionStatus | ''
  difficulty?: number | null
  knowledge_point_id?: number | null
  include_sub_knowledge?: boolean
  tag_ids?: number[]
  tag_match?: 'and' | 'or'
  created_by?: number | null
  root_only?: boolean
  include_deleted?: boolean
  sort?: string
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
  change_note?: string
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
