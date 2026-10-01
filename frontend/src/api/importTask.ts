import type { AxiosRequestConfig } from 'axios'
import { del, get, post, type PageResult } from './http'
import type { Draft } from './ai'
import type { QuestionBrief } from './types'

/**
 * 导入任务的类型与接口。
 *
 * 单独成文件而不是并进 api/ai.ts：`ai.ts` 讲的是"和模型对话"，
 * 这里讲的是"导入任务的生命周期"——两者会各自演化，
 * 混在一个文件里迟早互相牵连。
 *
 * 本文件同时是**后端导入契约在前端唯一落地的地方**：抽取与上传两个端点
 * （含 `method` / `source_file` / `source_files` 这些新字段）都只在这里表达，
 * 视图层不自己拼 URL，也不在别处重复定义响应类型。
 *
 * ⚠️ 语义提醒（与后端 model.ImportTask 的注释同源）：
 * 抽取跑在后端的进程内 worker 上，并发度是 1，队列里没有 worker 在消费的
 * 那种"没人管"状态不会发生。cancel 是**放弃**这条记录、不是中断，
 * retry 则由 worker 重新认领并重跑原文。
 */

export type ImportTaskStatus = 'pending' | 'processing' | 'completed' | 'failed' | 'cancelled'

export type ImportTaskSource = 'manual' | 'upload' | 'paper' | 'ai_assist'

// ---- 抽取 / 上传（后端 schema.ExtractRequest + schema.ExtractResponse） ----

/**
 * 解析方式。
 *
 * `template` 是确定性规则解析：按【题目】【答案】这类标签逐字读，
 * 不调用任何模型，因此**没配 AI 也能跑**，结果可复现、零成本。
 * 后端默认是 `ai`，但默认值由服务端决定，客户端这里不替它兜底。
 */
export type ImportParseMethod = 'ai' | 'template'

/**
 * extract = 抽取新题（材料里有答案就连答案一起抽）；
 * solve = 保留你给的原题、只补答案与解析；
 * record = 仅录题：只识别题目本身，不求解，答案与解析事后补。
 */
export type ImportMode = 'extract' | 'solve' | 'record'

/**
 * 一道待审核的草稿。
 *
 * 写成 `ai.ts` 里 `Draft` 的增量而不是整份复制：草稿的结构由后端
 * `importing.Draft` 决定，复制一份只会让两边悄悄漂移。
 * 后端把 `source_file` 落进 `ai.ts` 的 `Draft` 之后，把这个交叉类型
 * 换成直接 `import type { Draft }` 即可，调用方无需改动。
 */
export type ImportDraft = Draft & {
  /**
   * 这道题来自哪个文件。
   *
   * 一次响应可能覆盖多个文件（上传 .zip 时），所以草稿级的来源和
   * 响应级的 `source_files` 都要有：前者回答"这道题出自哪"，后者回答
   * "这一次到底喂了哪几份材料"。旧后端不返回该字段，界面据此隐藏来源标签。
   */
  source_file?: string
}

/** 抽取结果。`method` / `source_files` 在后端上线前可能缺席，故为可选。 */
export interface ImportExtractResponse {
  task_id: number
  drafts: ImportDraft[]
  total: number
  warning_count: number
  /** 本次实际跑的解析路径（"ai" | "template"）。用户据此判断该不该信这次结果。 */
  method?: string
  /** 本次覆盖的来源文件；.zip 会有多个。 */
  source_files?: string[]
  /** 版面大纲：标题、说明文字与题目引用的顺序。整卷导入靠它还原卷面。 */
  outline?: ImportOutline[]
  /** 文档级提示（例如压缩包里跳过了不安全的条目），与逐题警告不同。 */
  warnings?: string[]
  model: string
  elapsed_ms: number
}

/**
 * 版面大纲的一个节点。
 *
 * 与草稿是两套东西：草稿回答"这里有哪些题"，大纲回答"这份卷子长什么样"。
 * 合并成一个结构会让只想导题的人也被迫面对版面概念。
 */
export interface ImportOutline {
  kind: 'heading' | 'rich_text' | 'question' | 'page_break' | 'answer_space' | 'degraded'
  text?: string
  /** heading 专用，1 起。 */
  level?: number
  /** question 专用：指向 drafts 数组里的第几道题。 */
  draft_index?: number
  /** answer_space 专用。 */
  lines?: number
  style?: string
  /** degraded 专用：说明丢了什么、为什么。 */
  note?: string
}

/**
 * 批量排队的返回。
 *
 * 刻意是"部分成功"：整批因为一个坏文件全部退回，等于强迫用户自己
 * 挨个试出是哪个有问题，所以后端逐个收下并把失败的列出来。
 */
export interface EnqueueResult {
  tasks: ImportTask[]
  accepted: number
  rejected: number
  failures: string[]
}

// ---- 草稿层级（视图模型，不是后端契约） ----

/** 审核列表里的一个节点：一道草稿 + 挂在它下面的小问。 */
export interface ImportDraftNode {
  /** 在扁平 `drafts` 数组里的下标——勾选、编辑、删除都以它为准。 */
  index: number
  draft: ImportDraft
  children: ImportDraftNode[]
}

/**
 * 按 `temp_id` / `parent_temp_id` 把扁平草稿组装成树。
 *
 * 材料题（1 道母题 + 20 道小问）不分组的话，在审核列表里和 21 道互不相干的
 * 独立题长得一模一样，用户根本看不出哪几道是一组。
 *
 * 规则刻意与后端 `draftDepths` 对齐，否则界面画出来的层级和入库结果会对不上：
 *   - 重复的 `temp_id` 只认第一次出现的（谁才是母题无从判断，猜错更糟）；
 *   - 自引用、指向批次外的母题一律降级为独立题；
 *   - 成环时把整串摊平——后端对成环是整串拒绝，界面提前摊平好过递归渲染时死循环。
 * 另外保证**每一道草稿都恰好出现在树里一次**，不会因为层级问题把题弄丢。
 */
export function groupImportDrafts(drafts: ImportDraft[]): ImportDraftNode[] {
  const nodes: ImportDraftNode[] = drafts.map((draft, index) => ({
    index,
    draft,
    children: [],
  }))

  const byTempId = new Map<string, number>()
  for (const node of nodes) {
    const id = (node.draft.temp_id ?? '').trim()
    if (id && !byTempId.has(id)) byTempId.set(id, node.index)
  }

  const parentOf = new Array<number>(nodes.length).fill(-1)
  for (const node of nodes) {
    const parentId = (node.draft.parent_temp_id ?? '').trim()
    if (!parentId || parentId === (node.draft.temp_id ?? '').trim()) continue
    const parent = byTempId.get(parentId)
    if (parent === undefined || parent === node.index) continue
    parentOf[node.index] = parent
  }

  // 环检测：沿母题链上溯，重复经过的节点说明成环，就地降级为独立题。
  for (let i = 0; i < parentOf.length; i++) {
    if (parentOf[i] < 0) continue
    const seen = new Set<number>([i])
    let cur = parentOf[i]
    while (cur >= 0) {
      if (seen.has(cur)) {
        parentOf[i] = -1
        break
      }
      seen.add(cur)
      cur = parentOf[cur]
    }
  }

  // 按下标顺序装配，保证同级保持原文顺序——审核顺序即入库顺序。
  const roots: ImportDraftNode[] = []
  for (const node of nodes) {
    const parent = parentOf[node.index]
    if (parent < 0) roots.push(node)
    else nodes[parent].children.push(node)
  }
  return roots
}

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
  /** 是否有后台 worker 在消费队列。它掉线时队列会停在原地不动。 */
  worker_online: boolean
  /** 面向用户的一句话说明（通常是并发度与轮询间隔带来的排队预期）。 */
  hint: string
}

/**
 * 抽取/上传的专用超时。
 *
 * 一次长卷的抽取可能要跑满后端那个三分钟的预算，而 http 实例的默认值是
 * 两分钟——超时的表现是浏览器先断开，用户看到"请求超时"，但服务端其实
 * 还在跑，题也照样入库了，于是界面上一次失败、库里多出几十道题。
 */
const SLOW_TIMEOUT: AxiosRequestConfig = { timeout: 300_000 }

export const importTaskApi = {
  /**
   * 从粘贴文本 / 图片地址抽取草稿。
   *
   * `method` 决定走模型还是确定性规则解析；留空由后端按自己的默认值决定
   * （当前是 `ai`），客户端不替服务端承诺。
   */
  extract: (
    subjectId: number,
    payload: {
      content?: string
      images?: string[]
      method?: ImportParseMethod
      mode?: ImportMode
      solve_for_content?: string
    },
  ) => post<ImportExtractResponse>(`/subjects/${subjectId}/imports/extract`, payload, SLOW_TIMEOUT),

  /**
   * 上传**一个**文档（.docx / .md / .txt / .pdf / .zip）并抽取。
   *
   * 响应体与 /extract 完全一致，因此调用方拿到的处理路径（多文件过滤、
   * 方法说明、逐题审核）两边共用，不需要为文件上传另写一套。
   *
   * `answerImages` 是**已经传过一次**的图片地址（走 /upload/image 拿到），
   * 不是文件本身：让用户在两个地方各传一次同一张图，
   * 只会让"到底传了没有"变得无法核对。
   */
  upload: (
    subjectId: number,
    payload: {
      file: File
      method: ImportParseMethod
      mode: ImportMode
      answerImages?: string[]
    },
  ) => {
    const form = new FormData()
    form.append('file', payload.file)
    form.append('method', payload.method)
    form.append('mode', payload.mode)
    for (const ref of payload.answerImages ?? []) {
      // 每个地址作为一个独立部分，字段名固定为 images。
      form.append('images', new Blob([ref], { type: 'text/plain' }), ref)
    }
    return post<ImportExtractResponse>(`/subjects/${subjectId}/imports/upload`, form, SLOW_TIMEOUT)
  },

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

  /**
   * 批量排队：一次提交多个文件，立刻返回 202 与任务列表。
   *
   * 与 `upload` 的分工：upload 是"我现在就要看结果"（同步返回草稿），
   * enqueue 是"这一堆先收着"——整个文件夹的卷子该用后者。
   */
  enqueue: (
    subjectId: number,
    payload: { files: File[]; method: ImportParseMethod; mode: ImportMode },
  ) => {
    const form = new FormData()
    for (const f of payload.files) form.append('file', f)
    form.append('method', payload.method)
    form.append('mode', payload.mode)
    return post<EnqueueResult>(`/subjects/${subjectId}/imports`, form, SLOW_TIMEOUT)
  },

  /**
   * 取回某个排队任务抽出来的草稿。
   *
   * worker 跑完时 HTTP 响应早就返回了，草稿存在任务记录里，
   * 审核界面靠这条接口把结果取回来。
   */
  drafts: (subjectId: number, taskId: number) =>
    get<ImportExtractResponse>(`/subjects/${subjectId}/imports/${taskId}/drafts`),
}
