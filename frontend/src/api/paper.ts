import { del, downloadFile, get, post, put, type PageResult } from './http'
import type { RichDoc } from './types'

/**
 * 组卷相关的类型与接口。
 *
 * 节点的组织方式刻意只有两层（与后端 domain 的规则一致）：
 *
 *   block     顶层块：rich_text / heading / question / page_break / answer_space
 *   module    解析模块（question_details），其子节点由**服务端派生**
 *   reference 解析条目（answer_item），必须挂在某个 module 下
 *
 * 客户端保存时只提交 block 与 module，不要把 answer_item 一起回传——
 * 服务端会按当前题目顺序重新派生它们，并复用已存在的条目。
 */

export type NodeKind = 'block' | 'module' | 'reference'

export type NodeType =
  | 'rich_text'
  | 'heading'
  | 'question'
  | 'page_break'
  | 'answer_space'
  | 'question_details'
  | 'answer_item'

export type PaperScope = 'personal' | 'shared'
export type PaperStatus = 'draft' | 'finalized' | 'archived'
export type ModuleScope = 'all' | 'before'

export interface PaperNode {
  id: string
  parent_id: string | null
  position: number
  node_kind: NodeKind
  node_type: NodeType
  content: unknown
  props: unknown
  schema_version: number
  source_question_node_id: string | null
  anchor_before_node_id: string | null
}

/** 提交给服务端的节点（不含服务端派生的 position / reference）。 */
export interface NodeInput {
  id: string
  parent_id: string | null
  node_kind: NodeKind
  node_type: NodeType
  content?: unknown
  props?: unknown
  schema_version?: number
  source_question_node_id?: string | null
  anchor_before_node_id?: string | null
}

/** 题目块的内容结构。 */
export interface QuestionRef {
  question_id: number
  content_revision: number
  score?: number
  /** 题号是否参与自动编号。 */
  numbering?: boolean
}

/** 解析模块的内容结构。 */
export interface ModuleContent {
  scope: ModuleScope
  title?: string
}

export interface QuestionDisplay {
  show_analysis: boolean
  show_solution: boolean
  show_summary: boolean
  show_knowledge: boolean
}

export interface PaperBrief {
  id: number
  subject_id: number
  scope: PaperScope
  owner_id: number | null
  folder_id: number | null
  title: string
  description: string
  status: PaperStatus
  status_label: string
  question_count: number
  total_score: number
  numbering_enabled: boolean
  scoring_enabled: boolean
  revision: number
  finalized_version: number
  created_by: number
  updated_by: number
  created_at: string
  updated_at: string
}

export interface PaperQuestionProjection {
  id: number
  question_type: string
  question_type_label: string
  difficulty: number
  stem: RichDoc
  options: { id: string; label: string; content: RichDoc }[]
  answer: {
    kind: string
    correct?: string[]
    bool_answer?: boolean | null
    blanks?: { id: string; accepts: RichDoc[][] }[]
    reference?: RichDoc | null
  }
  analysis: RichDoc | null
  solution: RichDoc | null
  summary: RichDoc | null
  stem_text: string
  content_revision: number
  /** 引用的题目在入卷之后被改过，界面应提示"同步最新内容"。 */
  stale: boolean
}

export interface PaperPermissions {
  can_edit: boolean
  can_delete: boolean
  can_finalize: boolean
}

export interface PaperDetail extends PaperBrief {
  question_display: QuestionDisplay
  nodes: PaperNode[]
  questions: Record<number, PaperQuestionProjection>
  permissions: PaperPermissions
}

export interface PaperFolder {
  id: number
  subject_id: number
  scope: PaperScope
  owner_id: number | null
  parent_id: number | null
  name: string
  sort_order: number
  paper_count: number
  children?: PaperFolder[]
  created_at: string
}

export interface ReplaceNodesResult {
  revision: number
  nodes: PaperNode[]
}

export interface PaperVersion {
  id: number
  paper_id: number
  version_no: number
  title: string
  note: string
  schema_version: number
  created_by: number
  created_at: string
  question_count: number
}

export interface PaperEvent {
  id: number
  event_type: string
  summary: string
  actor_id: number
  actor: string
  payload: unknown
  created_at: string
}

export const paperApi = {
  // ---- 目录 ----
  folderTree: (subjectId: number) =>
    get<PaperFolder[]>(`/subjects/${subjectId}/paper-folders`),
  createFolder: (subjectId: number, payload: { name: string; parent_id?: number | null; scope?: PaperScope }) =>
    post<PaperFolder>(`/subjects/${subjectId}/paper-folders`, payload),
  deleteFolder: (id: number) => del<void>(`/paper-folders/${id}`),

  // ---- 试卷 ----
  list: (
    subjectId: number,
    params: {
      keyword?: string
      scope?: PaperScope | ''
      folder_id?: number | null
      status?: PaperStatus | ''
      page?: number
      page_size?: number
    },
  ) => get<PageResult<PaperBrief>>(`/subjects/${subjectId}/papers`, params),

  create: (
    subjectId: number,
    payload: {
      title: string
      description?: string
      scope?: PaperScope
      folder_id?: number | null
      numbering_enabled?: boolean
      scoring_enabled?: boolean
    },
  ) => post<PaperDetail>(`/subjects/${subjectId}/papers`, payload),

  get: (id: number) => get<PaperDetail>(`/papers/${id}`),

  update: (
    id: number,
    payload: {
      title?: string
      description?: string
      status?: PaperStatus
      numbering_enabled?: boolean
      scoring_enabled?: boolean
      folder_id?: number
      clear_folder?: boolean
      question_display?: QuestionDisplay
    },
  ) => put<PaperDetail>(`/papers/${id}`, payload),

  remove: (id: number) => del<void>(`/papers/${id}`),
  restore: (id: number) => post<void>(`/papers/${id}/restore`),
  duplicate: (id: number, title?: string) =>
    post<PaperDetail>(`/papers/${id}/duplicate`, title ? { title } : {}),

  /**
   * 整树替换。
   *
   * expected_revision 是乐观锁：与服务端不一致时返回 409（code 40901），
   * 说明别处已经改过这份卷子，界面必须提示用户而不是覆盖别人的改动。
   */
  replaceNodes: (id: number, expectedRevision: number, nodes: NodeInput[], batchId?: string) =>
    put<ReplaceNodesResult>(`/papers/${id}/nodes`, {
      expected_revision: expectedRevision,
      batch_id: batchId,
      nodes,
    }),

  /** 检查引用的题目是否已被改动。 */
  questionRevisions: (id: number) =>
    get<{ stale: number[]; missing: number[] }>(`/papers/${id}/question-revisions`),

  /** 把题目节点的修订号刷新到最新。 */
  syncQuestionNodes: (id: number, questionIds: number[]) =>
    post<{ revision: number; synced: number[] }>(`/papers/${id}/question-nodes/sync`, {
      question_ids: questionIds,
    }),

  // ---- 版本 ----
  listVersions: (id: number) => get<PaperVersion[]>(`/papers/${id}/versions`),
  finalize: (id: number, note: string) =>
    post<PaperVersion>(`/papers/${id}/versions`, { note }),
  getVersion: (id: number, versionNo: number) =>
    get<{ version: PaperVersion; snapshot: unknown }>(`/papers/${id}/versions/${versionNo}`),

  events: (id: number) => get<PaperEvent[]>(`/papers/${id}/events`),

  // ---- 导出 ----
  exportPaper: (id: number, format: 'docx' | 'latex', title: string) =>
    downloadFile(
      `/papers/${id}/export`,
      { format },
      `${title}.${format === 'docx' ? 'docx' : 'tex'}`,
    ),
  exportVersion: (id: number, versionNo: number, format: 'docx' | 'latex', title: string) =>
    downloadFile(
      `/papers/${id}/versions/${versionNo}/export`,
      { format },
      `${title}-v${versionNo}.${format === 'docx' ? 'docx' : 'tex'}`,
    ),
}
