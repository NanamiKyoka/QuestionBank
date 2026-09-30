import { del, get, post, put, type PageResult } from './http'
import type { AnswerSpec, QuestionStatus, RichDoc } from './types'

// ---- AI 供应商与模型 ----

export type ModelType = 'text' | 'vision' | 'embedding'

export interface ModelInfo {
  id: number
  provider_id: number
  provider_name: string
  name: string
  display_name: string
  model_type: ModelType
  max_tokens: number
  context_window: number
  is_active: boolean
  sort_order: number
  /** 是否被选为当前默认模型。 */
  in_use: boolean
  created_at: string
}

/**
 * 供应商接口类型。
 *
 * 刻意不写成字面量联合（如 'openai' | 'gemini'）：可用协议由后端的
 * `GET /ai/interface-types` 决定，并在保存时强校验。写死一份联合等于在前端
 * 复制一份会过期的清单——那正是"界面能选、后端不认"的由来。
 * 用 string 让服务端成为唯一权威，界面直接读那个端点。
 */
export type ProviderInterfaceType = string

export interface Provider {
  id: number
  name: string
  interface_type: ProviderInterfaceType
  base_url: string
  /**
   * 密钥掩码（形如 sk-abc…wxyz）。
   *
   * 后端**永不返回明文**。编辑时把掩码原样回传表示"不修改"——
   * 所以界面上不能把掩码当成真密钥显示，否则用户以为看到的是密钥。
   */
  key_hint: string
  has_key: boolean
  is_active: boolean
  sort_order: number
  models: ModelInfo[]
  created_at: string
}

export interface ActiveConfig {
  text_model_id: number
  vision_model_id: number
  embedding_model_id: number
  text?: ModelInfo
  vision?: ModelInfo
  embedding?: ModelInfo
  ready: boolean
  message: string
}

export interface ProviderTestResult {
  ok: boolean
  model?: string
  reply?: string
  error?: string
  latency_ms: number
}

export const aiApi = {
  listProviders: () => get<Provider[]>('/ai/providers'),
  createProvider: (payload: {
    name: string
    interface_type: ProviderInterfaceType
    base_url?: string
    api_key: string
    is_active?: boolean
  }) => post<Provider>('/ai/providers', payload),

  /**
   * 更新供应商。
   *
   * api_key 的三种语义（由后端约定）：
   *   undefined / '' → 不修改密钥
   *   掩码串        → 同样视为不修改
   *   新明文        → 覆盖
   * 因此这里只在用户真的输入了新密钥时才带上该字段。
   */
  updateProvider: (
    id: number,
    payload: {
      name?: string
      interface_type?: ProviderInterfaceType
      base_url?: string
      api_key?: string
      is_active?: boolean
    },
  ) => put<Provider>(`/ai/providers/${id}`, payload),
  deleteProvider: (id: number) => del<void>(`/ai/providers/${id}`),
  testProvider: (id: number, payload: { model_id?: number; prompt?: string } = {}) =>
    post<ProviderTestResult>(`/ai/providers/${id}/test`, payload),

  createModel: (payload: {
    provider_id: number
    name: string
    display_name?: string
    model_type: ModelType
    max_tokens?: number
    context_window?: number
    is_active?: boolean
  }) => post<ModelInfo>('/ai/models', payload),
  updateModel: (
    id: number,
    payload: {
      name?: string
      display_name?: string
      model_type?: ModelType
      max_tokens?: number
      context_window?: number
      is_active?: boolean
    },
  ) => put<ModelInfo>(`/ai/models/${id}`, payload),
  deleteModel: (id: number) => del<void>(`/ai/models/${id}`),

  activeConfig: () => get<ActiveConfig>('/ai/active-config'),
  setActiveConfig: (payload: {
    text_model_id?: number
    vision_model_id?: number
    embedding_model_id?: number
  }) => put<ActiveConfig>('/ai/active-config', payload),
}

// ---- 智能录题 ----

export interface KnowledgeSuggestion {
  knowledge_point_id: number
  name: string
  path: string
  score: number
}

/** 抽取出的草稿。结构与后端 importing.Draft 对齐。 */
export interface Draft {
  temp_id?: string
  parent_temp_id?: string
  source_number?: number

  question_type: string
  type_label: string

  stem: RichDoc
  stem_markdown: string
  stem_text: string

  options: { id: string; label: string; content: RichDoc }[]
  answer: AnswerSpec

  analysis: RichDoc | null
  solution: RichDoc | null
  summary: RichDoc | null

  difficulty: number

  /** 模型自己说出的知识点名（自由文本，未必存在）。 */
  knowledge_points: string[]
  tags: string[]

  /** 向量检索找到的候选，比模型自述更可靠。 */
  suggested_knowledge_points?: KnowledgeSuggestion[]

  /** 归一化时发现的问题，审核界面据此高亮。 */
  warnings: string[]
}

export interface ImportTask {
  id: number
  subject_id: number | null
  user_id: number
  source: string
  status: string
  status_label: string
  file_type: string
  mode: string
  total_count: number
  saved_count: number
  failed_count: number
  error_message?: string
  created_at: string
  finished_at: string | null
}

export interface ExtractResult {
  task_id: number
  drafts: Draft[]
  total: number
  warning_count: number
  model: string
  elapsed_ms: number
}
export interface CommitResult {
  task_id: number
  created_count: number
  failed_count: number
  created_ids: number[]
  failures: { index: number; temp_id?: string; stem?: string; error: string }[]
  /** 未能匹配到的知识点/标签名。系统不会自动创建它们。 */
  unresolved_names?: string[]

  /** 整卷导入建出来的试卷 id；没勾选建卷或建卷失败时缺席。 */
  paper_id?: number
  /**
   * 建卷失败的原因。题目此刻**已经入库**，所以这不是导入失败，
   * 界面上必须把两者分开说，否则用户会以为整批白导了而重复导入。
   */
  paper_error?: string
}

export const importApi = {
  extract: (
    subjectId: number,
    payload: {
      content?: string
      images?: string[]
      mode?: 'extract' | 'solve'
      solve_for_content?: string
    },
  ) => post<ExtractResult>(`/subjects/${subjectId}/imports/extract`, payload),

  commit: (
    subjectId: number,
    payload: {
      drafts: Draft[]
      default_tag_ids?: number[]
      default_knowledge_point_ids?: number[]
      status?: QuestionStatus
      /**
       * 整卷导入：入库的同时按大纲建一份可编辑的试卷。
       * 默认关闭——只想把题存进题库的人不该凭空多出一份试卷。
       */
      save_as_paper?: boolean
      paper_title?: string
      /** 版面大纲，draft_index 指向本次提交的 drafts 下标。 */
      outline?: {
        kind: 'heading' | 'rich_text' | 'question' | 'page_break' | 'answer_space' | 'degraded'
        text?: string
        level?: number
        draft_index?: number
        lines?: number
        style?: string
        note?: string
      }[]
    },
  ) => post<CommitResult>(`/subjects/${subjectId}/imports/commit`, payload),

  listTasks: (subjectId: number, limit = 20) =>
    get<PageResult<ImportTask> | ImportTask[]>(`/subjects/${subjectId}/imports`, {
      page: 1,
      page_size: limit,
    }),
}

// ---- AI 助手对话 ----

export interface ChatSession {
  id: number
  user_id: number
  subject_id: number | null
  title: string
  scene: string
  message_count: number
  last_message_at: string | null
  created_at: string
  updated_at: string
}

export interface ChatMessage {
  id: number
  session_id: number
  role: 'user' | 'assistant' | 'system' | 'tool'
  content: string
  tool_calls?: unknown
  images?: string[]
  agent_run_id?: string
  prompt_tokens: number
  completion_tokens: number
  created_at: string
}

export const chatApi = {
  listSessions: (subjectId?: number) =>
    get<PageResult<ChatSession>>('/chat/sessions', {
      subject_id: subjectId,
      page: 1,
      page_size: 50,
    }),
  createSession: (payload: { title?: string; subject_id?: number; scene?: string }) =>
    post<ChatSession>('/chat/sessions', payload),
  getSession: (id: number) => get<ChatSession>(`/chat/sessions/${id}`),
  updateSession: (id: number, payload: { title: string }) =>
    put<ChatSession>(`/chat/sessions/${id}`, payload),
  deleteSession: (id: number) => del<void>(`/chat/sessions/${id}`),
  listMessages: (id: number, limit = 100) =>
    get<ChatMessage[]>(`/chat/sessions/${id}/messages`, { limit }),

  /**
   * 回传客户端工具的执行结果，唤醒正在等待的后端运行。
   *
   * 这不是普通请求：后端的 SSE 流此刻正阻塞在等待这个回执上，
   * 超时（默认 30 秒）后就再也送不进去了。因此前端必须尽快回传，
   * 失败时也要回传一个"失败"结果，而不是默默丢下不管。
   */
  resolveClientTool: (
    runId: string,
    payload: { ticket: string; ok: boolean; content: string; data?: unknown },
  ) => post<void>(`/chat/runs/${runId}/tool-result`, payload),
}

// ---- 图片上传 ----

export const uploadApi = {
  /** 上传单张图片，返回可访问地址。 */
  image: async (file: File): Promise<{ url: string; width: number; height: number }> => {
    const form = new FormData()
    form.append('files', file)
    const { http } = await import('./http')
    const response = await http.post('/uploads/images', form, {
      headers: { 'Content-Type': 'multipart/form-data' },
    })
    return response.data.data
  },
}
