import { del, get, post, put } from './http'

/**
 * 提示词模板。整组接口仅全局管理员可用。
 *
 * 这一组接口对应的是「智能录题抽取」「智能录题补答案」「AI 助手人设」
 * 三个**系统里真实存在**的提示词。后端不接受目录之外的键：
 * 存一条没有读取方的模板只会让管理员以为改动生效了。
 */

/** 提示词来源，即解析链上"最终是谁赢了"。 */
export type PromptSource = 'subject' | 'global' | 'setting' | 'builtin'

/** 一个占位符的说明，界面上的"插入"按钮直接用 token。 */
export interface PromptPlaceholder {
  /** 占位符名，不含大括号，如 subject_name。 */
  name: string
  desc: string
  /** 为真时取值为空会连带删掉整行。 */
  optional: boolean
  /** 带大括号的写法，如 {{subject_name}}。 */
  token: string
}

/** 一条已保存的模板（全局或学科覆盖）。 */
export interface PromptTemplate {
  id: number
  key: string
  name: string
  /** 分组用，取值 import / chat。 */
  scene: string
  description: string
  placeholders: PromptPlaceholder[]
  /** 为空表示全局模板；非空表示该学科的覆盖。 */
  subject_id: number | null
  system_prompt: string
  updated_by: number
  created_at: string
  updated_at: string
}

/** 目录里的一条提示词，连同"当前有没有配"。 */
export interface PromptCatalogEntry {
  key: string
  name: string
  scene: string
  description: string
  placeholders: PromptPlaceholder[]
  /** 内置默认模板正文，供对照与复制。 */
  default: string
  /** 当前保存的全局模板；null 表示尚未配置。 */
  template: PromptTemplate | null
  /** 全局模板当前实际来自哪一级。 */
  effective_source: PromptSource
  /** 非空说明当前实际用的不是保存的模板。 */
  fallback: string
  /** 该提示词在系统设置里的兜底键；空串表示没有。 */
  setting_key: string
  /** 兜底键里当前的值，只读，仅供了解解析链。 */
  setting_value: string
  /** 已配置的学科覆盖数量。 */
  override_count: number
}

export interface PromptList {
  items: PromptCatalogEntry[]
}

/** 某学科在某条提示词上的最终状态。 */
export interface SubjectPrompt {
  key: string
  name: string
  scene: string
  description: string
  placeholders: PromptPlaceholder[]
  default: string
  /** 该学科最终会用的模板正文。 */
  effective: string
  /** 把学科名/说明代进去之后的渲染结果；知识点与标签块在预览里为空。 */
  preview: string
  source: PromptSource
  /** 该学科覆盖行的 id；没有覆盖时为 0。 */
  override_id: number
  override_updated_at: string | null
  /** 非空说明为什么最终用的不是覆盖模板。 */
  fallback: string
}

export interface SubjectPrompts {
  subject_id: number
  subject_name: string
  prompts: SubjectPrompt[]
}

export const promptApi = {
  /** GET /prompts —— 提示词目录 + 各自当前保存的全局模板。 */
  list: () => get<PromptList>('/prompts'),

  /** POST /prompts —— 新建全局模板；subject_id 非空时建的是学科覆盖。 */
  create: (payload: { key: string; subject_id?: number | null; system_prompt: string }) =>
    post<PromptTemplate>('/prompts', payload),

  /** PUT /prompts/:id —— 改模板正文。 */
  update: (id: number, systemPrompt: string) =>
    put<PromptTemplate>(`/prompts/${id}`, { system_prompt: systemPrompt }),

  /** DELETE /prompts/:id —— 删掉这条模板，作用域自动回退到下一级来源。 */
  remove: (id: number) => del<void>(`/prompts/${id}`),

  /** GET /subjects/:sid/prompts —— 某学科的覆盖情况与最终生效值。 */
  subjectList: (subjectId: number) => get<SubjectPrompts>(`/subjects/${subjectId}/prompts`),

  /**
   * PUT /subjects/:sid/prompts —— 批量写入/删除学科覆盖。
   *
   * system_prompt 传空串表示删除该学科的覆盖、回退到全局模板。
   * 整个批次在一个事务里，要么全成要么全不成。
   */
  subjectSave: (subjectId: number, prompts: { key: string; system_prompt: string }[]) =>
    put<SubjectPrompts>(`/subjects/${subjectId}/prompts`, { prompts }),
}
