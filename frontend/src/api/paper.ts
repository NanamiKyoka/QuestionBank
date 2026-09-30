import { ApiError, del, downloadFile, get, post, put, type PageResult } from './http'
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

/**
 * 题目块的内容结构。
 *
 * **只有两个字段，而且必须是两个。**
 *
 * 曾经这里还声明过 `score?` 与 `numbering?`，但全仓没有任何地方写入它们，
 * 后端 `assemble.go` 与 `paper_nodes.go` 读的也始终是 `props`。留着它们
 * 会诱使后来者把分值写进 content——而那样写的结果是整卷总分永远是 0、
 * 卷面上也没有题号，两个都不会报错。
 *
 * 分值与题号一律走 `QuestionNodeProps`。
 */
export interface QuestionRef {
  question_id: number
  /**
   * 入卷时冻结的题目修订号。
   *
   * 服务端据此判断"题库里的这道题后来改过没有"，改过就在界面上提示
   * 「内容已更新」，由用户决定是否同步。**不要在前端改写它**：
   * 同步时由服务端重算（`paper_nodes.go` 的 syncQuestionNodes）。
   */
  content_revision: number
}

/**
 * 题目节点的 props。
 *
 * 分值与题号都必须落在 props 上而不是 content 里：服务端的 sumScore 与
 * 导出器都只读 props，写进 content 的话整卷分值永远是 0、题号也永远不会
 * 出现在卷面上。题号是**字符串**，因为分组编号要的是 "1.1" 这种十进制写法，
 * 用数值存就只能表达 1。
 */
export interface QuestionNodeProps {
  score?: number
  /** 手动指定的题号。留空表示交给自动编号。 */
  number?: string
}

/** 题号长度上限，与后端节点 props 的契约一致：再多也不会更好读。 */
export const QUESTION_NUMBER_MAX_LENGTH = 16

/**
 * 批量编号方式。
 *
 * 刻意只有这两种：'global' 整卷顺序；'heading' 以**二级标题**为分界，
 * 组内编成 1.1、1.2…，下一节从 2.1 重新开始。一级标题不参与分组——
 * 卷面顶层通常只有试卷名，用它分组等于整卷只有一组。
 *
 * 不用"一、二、三"：那是老师自己打在标题节点里的文字，不是编号。
 */
export type NumberingMode = 'global' | 'heading'


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

/** 试卷列表的查询参数。 */
export interface PaperQuery {
  keyword?: string
  scope?: PaperScope | ''
  folder_id?: number | null
  unfiled?: boolean
  status?: PaperStatus | ''
  /** 混入已软删除的试卷。与 only_deleted 互斥。 */
  include_deleted?: boolean
  /** 只返回已软删除的试卷——回收站列表用它。 */
  only_deleted?: boolean
  page?: number
  page_size?: number
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

/**
 * 快照里的节点。
 *
 * 与 PaperNode 字段完全一致，只是定稿时把整行 model 序列化了，
 * 因此多出 paper_id / created_at / updated_at 三个渲染用不到的字段。
 * 显式写出来是为了让"快照契约"和"编辑器契约"的差异看得见，
 * 而不是让读代码的人以为快照节点少东西。
 */
export interface SnapshotNode extends PaperNode {
  paper_id: number
  created_at: string
  updated_at: string
}

/**
 * 定稿快照：**只读**。
 *
 * 里面冻结了节点树、题目内容投影与排版开关，因此渲染它不需要（也不应该）
 * 再查一次题库——这正是定稿的意义。改动题库后这个页面看到的内容不会变。
 */
export interface PaperSnapshot {
  title: string
  nodes: SnapshotNode[]
  /** 题目内容投影。Go 的 map[uint]X 序列化后是字符串键。 */
  questions: Record<string, PaperQuestionProjection>
  numbering_enabled: boolean
  scoring_enabled: boolean
  question_display: QuestionDisplay
  total_score: number
  source_revision: number
  schema_version: number
}

export interface PaperVersionDetail {
  version: PaperVersion
  snapshot: PaperSnapshot
}

/** 追加题目的结果。 */
export interface AppendQuestionsResult {
  paper_id: number
  paper_title: string
  /** 写回后的新修订号。 */
  revision: number
  added: number
}

/** 把服务端返回的节点折成提交结构。 */
export function toNodeInput(node: PaperNode): NodeInput {
  return {
    id: node.id,
    parent_id: node.parent_id,
    node_kind: node.node_kind,
    node_type: node.node_type,
    content: node.content,
    props: node.props,
    schema_version: node.schema_version || 1,
    // 软指针由服务端重算：解析条目（reference）会按当前题目顺序重新派生，
    // 客户端回传的旧值只会覆盖掉服务端刚算出来的结果。
    source_question_node_id: null,
    anchor_before_node_id: null,
  }
}

/**
 * 生成一个未被占用的节点 id。
 *
 * 节点主键是客户端生成的 UUID，而保存是**整树覆盖**：撞 id 不会被合并，
 * 会被 domain.NormalizeTree 当成"节点 id 重复"整棵拒绝。所以这里要把
 * 本次提交里已经用掉的 id 一起喂进来，而不是只查服务端那份。
 */
function newNodeID(taken: Set<string>): string {
  let id = crypto.randomUUID()
  while (taken.has(id)) id = crypto.randomUUID()
  taken.add(id)
  return id
}

/**
 * 把一批题目追加到试卷末尾（解析模块之前）。
 *
 * 后端没有"批量加题"接口，只有整树覆盖的 `PUT /papers/:id/nodes`，
 * 因此"追加"只能在客户端实现成「读一次 → 本地拼接 → 带 expected_revision 写回」。
 * 代价是这一步不是原子的：读到的整棵树如果在此期间被别人改过，
 * 写回会被乐观锁挡下（409）——这正是我们要的行为。
 *
 * 冲突时**不自动重试**：重试等于拿一份已经过期的整树覆盖回去，
 * 恰好是乐观锁要防的那件事。抛给调用方，由界面提示用户重新确认目标。
 */
async function appendQuestions(
  paperId: number,
  questionIds: number[],
): Promise<AppendQuestionsResult> {
  if (questionIds.length === 0) {
    throw new ApiError(400, 40001, '没有要加入的题目')
  }
  // 先看权限再读整棵树：不能编辑的卷子没必要拉全量内容。
  const detail = await get<PaperDetail>(`/papers/${paperId}`)
  if (!detail.permissions.can_edit) {
    throw new ApiError(403, 40301, '你没有编辑这份试卷的权限')
  }

  const taken = new Set(detail.nodes.map((n) => n.id))
  const roots = detail.nodes
    .filter((n) => n.parent_id === null && n.node_kind !== 'reference')
    .slice()
    .sort((a, b) => a.position - b.position)

  // 解析模块固定排在最后：它是卷末汇总，插在题目中间会打断题序。
  const module = roots.find((n) => n.node_type === 'question_details') ?? null
  const head = roots.filter((n) => n.node_type !== 'question_details')

  const additions: NodeInput[] = questionIds.map((questionID) => ({
    id: newNodeID(taken),
    parent_id: null,
    node_kind: 'block',
    node_type: 'question',
    // content_revision 记 0：题库列表接口不返回这个字段，为它逐题再拉一次
    // 详情（N 次请求）并不划算。服务端把 0 视为"未记录"（CheckQuestionRevisions），
    // 所以不会误报"内容已更新"，之后在编辑台点一次同步就补上了。
    content: { question_id: questionID, content_revision: 0 } satisfies QuestionRef,
    props: {},
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
  }))

  const nodes: NodeInput[] = [
    ...head.map(toNodeInput),
    ...additions,
    ...(module ? [toNodeInput(module)] : []),
  ]

  const saved = await put<ReplaceNodesResult>(`/papers/${paperId}/nodes`, {
    expected_revision: detail.revision,
    nodes,
  })

  return {
    paper_id: detail.id,
    paper_title: detail.title,
    revision: saved.revision,
    added: questionIds.length,
  }
}

export const paperApi = {
  // ---- 目录 ----
  folderTree: (subjectId: number) =>
    get<PaperFolder[]>(`/subjects/${subjectId}/paper-folders`),
  createFolder: (subjectId: number, payload: { name: string; parent_id?: number | null; scope?: PaperScope }) =>
    post<PaperFolder>(`/subjects/${subjectId}/paper-folders`, payload),
  /**
   * 重命名或移动目录。
   *
   * 契约：传 parent_id 表示移动到该目录下，clear_parent 表示移回根，
   * 只传 name 就是重命名。同级重名与"移动到自己的子目录下"由服务端拒绝（409）。
   *
   * 注意这里**没有** restore：后端只对题目和试卷提供了恢复接口，
   * 目录删掉之后界面再也列不出来，删除确认框因此不能承诺可恢复。
   */
  updateFolder: (id: number, payload: { name?: string; parent_id?: number; clear_parent?: boolean }) =>
    put<PaperFolder>(`/paper-folders/${id}`, payload),
  /** 删除目录。目录非空（含子目录或试卷）时后端返回 409。 */
  deleteFolder: (id: number) => del<void>(`/paper-folders/${id}`),

  // ---- 试卷 ----
  // 展开成新对象再传：接口类型没有索引签名，直接传 interface 给
  // Record<string, unknown> 形参过不了类型检查（api/index.ts 的 list 同理）。
  list: (subjectId: number, params: PaperQuery) =>
    get<PageResult<PaperBrief>>(`/subjects/${subjectId}/papers`, { ...params }),

  /**
   * 回收站列表：只返回已软删除的试卷。
   *
   * 用 `only_deleted` 而不是 `include_deleted`：后者只是去掉 deleted_at 过滤，
   * 返回的是"活的 + 删掉的"混在一起的合集，而 DeletedAt 是 `json:"-"`，
   * 客户端分不出哪条已经进了回收站。
   *
   * 后端没有彻底删除（硬删）接口，`DELETE /papers/:id` 本身就是软删除，
   * 所以这里只提供恢复。
   */
  listDeleted: (subjectId: number, params: Omit<PaperQuery, 'include_deleted'>) =>
    get<PageResult<PaperBrief>>(`/subjects/${subjectId}/papers`, { ...params, only_deleted: true }),

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
  /** 从回收站恢复。传入的试卷必须处于已软删除状态，否则后端返回 422。 */
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
    get<PaperVersionDetail>(`/papers/${id}/versions/${versionNo}`),

  /**
   * 操作时间线，按时间倒序的**键集分页**。
   *
   * 游标是 before_id 而不是 page：时间线一直在追加，用页码分页会导致
   * 翻页时新事件插到前面、已经看过的条目被挤走。翻到 hasMore=false 即止。
   */
  events: (id: number, params: { limit?: number; before_id?: number } = {}) =>
    get<PaperEvent[]>(`/papers/${id}/events`, { ...params }),

  /** 追加题目到试卷（读整树 → 拼接 → 带乐观锁写回）。见函数注释。 */
  appendQuestions,

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
