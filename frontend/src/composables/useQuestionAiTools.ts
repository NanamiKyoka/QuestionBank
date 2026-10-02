/**
 * 题库页面暴露给 AI 的确认通道。
 *
 * 与组卷的三个客户端工具不同，这里处理的是**题库**里已经落库的数据，
 * 前端能读到题目 id，但改之前仍然要先把改动摆给用户看：
 * AI 说"我已经帮你加了两道题"而界面没动静，或者用户点了应用才发现
 * 改错了题，两种都不可接受。
 *
 * 为什么工具**立即**返回：后端的票据 30 秒过期，运行预算更短，
 * 而"用户看一眼再点应用"可能要几分钟。等确认完再回执等于每次都白等一次超时。
 * 因此写操作一律先挂成待确认预览（stagePending），
 * 界面上的应用/放弃闸门（QuestionAiChangesBar.vue）才是真正落笔的地方。
 *
 * SECURITY：这三个操作只认结构化字段，**没有任何一个**接受模型给的路径或路由。
 * 智能录题会把外部文档喂给模型，那正是提示注入的载体。
 */
import { onBeforeUnmount, onMounted } from 'vue'
import { questionApi } from '@/api'
import { registerClientTool, unregisterClientTool } from './useAiClientTools'
import type { ClientToolResult } from './useAiClientTools'

export const AI_TOOL_PREVIEW_EDITS = 'preview_question_edits'

/** 服务端 prepareQuestionEdits 下发的操作类型。与后端枚举逐字对齐。 */
export type QuestionEditOp = 'update' | 'archive' | 'delete'

const EDIT_OPS: readonly QuestionEditOp[] = ['update', 'archive', 'delete']

/** 改前值。字段名与后端 beforeSnapshot 对齐。 */
export interface EditBefore {
  stem?: string
  difficulty?: number
  source?: string
  tags?: string[]
  tag_ids?: number[]
  points?: string[]
  point_ids?: number[]
  status?: string
  status_label?: string
}

export interface UpdatePayload {
  stem?: unknown
  stem_markdown?: string
  difficulty?: number
  source?: string
  tag_ids?: number[]
  knowledge_point_ids?: number[]
  change_note?: string
  /** 后端算好的改动字段名，供差异标题用。 */
  fields?: string[]
}

export interface QuestionEdit {
  op: QuestionEditOp
  question_id: number
  before?: EditBefore
  payload?: UpdatePayload
  comment?: string
}

/** 一处改动，供确认条逐条展示。 */
export interface QuestionEditLine {
  key: string
  op: QuestionEditOp
  questionId: number
  stem: string
  /** 「字段：旧 → 新」形式的逐条说明。 */
  labels: string[]
}

/** 一批等待用户确认的改动。 */
export interface PendingQuestionEdits {
  tool: string
  summary: string
  edits: QuestionEdit[]
  lines: QuestionEditLine[]
}

/** 宿主（QuestionsView.vue）必须提供的接口。 */
export interface QuestionAiHost {
  canEdit(): boolean
  canDelete(): boolean
  /** 已有未确认改动：叠第二批会攒出一份没人看得懂的差异。 */
  hasPending(): boolean
  stagePending(change: PendingQuestionEdits): void
  /** 改动落库后刷新列表。 */
  refresh(): Promise<void>
  /** 打开一道题的详情抽屉，让用户核对改动是否落在对的题上。 */
  openDetail(id: number): void
}

// ---- 解析 ----

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function asString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function asInt(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : 0
}

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((s): s is string => typeof s === 'string' && s !== '')
}

function clip(text: string, n: number): string {
  const runes = [...text]
  return runes.length <= n ? text : `${runes.slice(0, n).join('')}…`
}

function parseEdits(input: unknown): { edits: QuestionEdit[]; problems: string[] } {
  const edits: QuestionEdit[] = []
  const problems: string[] = []
  if (!Array.isArray(input)) {
    return { edits, problems: ['operations 不是数组'] }
  }

  input.forEach((raw, index) => {
    const at = `第 ${index + 1} 处改动`
    if (!isRecord(raw)) {
      problems.push(`${at}：格式不正确`)
      return
    }
    const op = raw.op
    if (typeof op !== 'string' || !EDIT_OPS.includes(op as QuestionEditOp)) {
      problems.push(`${at}：op 不受支持`)
      return
    }
    const questionId = asInt(raw.question_id)
    if (!questionId) {
      problems.push(`${at}：缺少 question_id`)
      return
    }

    const edit: QuestionEdit = { op: op as QuestionEditOp, question_id: questionId }
    if (isRecord(raw.before)) edit.before = raw.before as EditBefore
    if (op === 'update') {
      if (!isRecord(raw.payload)) {
        problems.push(`${at}：update 缺少载荷`)
        return
      }
      edit.payload = raw.payload as UpdatePayload
    }
    if (op === 'archive') edit.comment = asString(raw.comment)
    edits.push(edit)
  })
  return { edits, problems }
}

// ---- 差异 ----

const FIELD_LABELS: Record<string, string> = {
  stem: '题干',
  difficulty: '难度',
  source: '来源',
  tag_ids: '标签',
  knowledge_point_ids: '知识点',
}

function names(before: EditBefore | undefined, key: 'tags' | 'points'): string {
  if (!before) return ''
  const list = key === 'tags' ? asStringArray(before.tags) : asStringArray(before.points)
  return list.length ? list.join('、') : '（空）'
}

/** 把一条操作翻译成给人看的「字段：旧 → 新」。 */
export function describeQuestionEdit(edit: QuestionEdit): string[] {
  const before = edit.before
  if (edit.op === 'archive') {
    return [`归档（当前${before?.status_label ?? before?.status ?? '已发布'}）`]
  }
  if (edit.op === 'delete') {
    return [`移入回收站（当前${before?.status_label ?? before?.status ?? '未知状态'}）`]
  }

  const payload = edit.payload
  if (!payload) return []
  const lines: string[] = []

  if (typeof payload.stem_markdown === 'string' && payload.stem_markdown.trim()) {
    lines.push(`题干：${clip(before?.stem?.trim() || '（空）', 40)} → ${clip(payload.stem_markdown.trim(), 40)}`)
  }
  if (typeof payload.difficulty === 'number') {
    lines.push(`难度：${before?.difficulty ?? '?'} → ${payload.difficulty}`)
  }
  if (typeof payload.source === 'string') {
    lines.push(`来源：${clip(before?.source || '（空）', 30)} → ${clip(payload.source || '（空）', 30)}`)
  }
  if (Array.isArray(payload.tag_ids)) {
    lines.push(`标签：${names(before, 'tags')} → ${payload.tag_ids.length} 个`)
  }
  if (Array.isArray(payload.knowledge_point_ids)) {
    lines.push(`知识点：${names(before, 'points')} → ${payload.knowledge_point_ids.length} 个`)
  }
  // 服务端已经核对过"至少改了一个字段"，这里仍留一道：
  // 差异清单里出现一行"无改动"，比什么都不显示更让人困惑。
  if (!lines.length && Array.isArray(payload.fields)) {
    lines.push(`字段：${payload.fields.map((f) => FIELD_LABELS[f] ?? f).join('、')}（值未变）`)
  }
  return lines
}

/** 应用一批改动。返回实际落库的结果，失败项如实带回。 */
export async function applyQuestionEdits(
  edits: QuestionEdit[],
): Promise<{ applied: number; failed: string[] }> {
  let applied = 0
  const failed: string[] = []

  for (const edit of edits) {
    try {
      if (edit.op === 'delete') {
        await questionApi.remove(edit.question_id)
      } else if (edit.op === 'archive') {
        await questionApi.archive(edit.question_id, edit.comment ?? '')
      } else {
        const payload: Record<string, unknown> = {}
        const src = edit.payload ?? {}
        if (src.stem !== undefined) payload.stem = src.stem
        if (src.difficulty !== undefined) payload.difficulty = src.difficulty
        if (src.source !== undefined) payload.source = src.source
        if (src.tag_ids !== undefined) payload.tag_ids = src.tag_ids
        if (src.knowledge_point_ids !== undefined) payload.knowledge_point_ids = src.knowledge_point_ids
        const note = typeof src.change_note === 'string' ? src.change_note.trim() : ''
        if (note) payload.change_note = note
        // 一处都没剩说明是"值没变"的空改动，跳过比发一次空请求好。
        if (!Object.keys(payload).length) continue
        await questionApi.update(edit.question_id, payload)
      }
      applied += 1
    } catch (error) {
      failed.push(`题目 ${edit.question_id}：${describeError(error)}`)
    }
  }
  return { applied, failed }
}

function describeError(error: unknown): string {
  if (error instanceof Error && error.message) return error.message
  return String(error)
}

function fail(content: string): ClientToolResult {
  return { ok: false, content }
}

export function useQuestionAiTools(host: QuestionAiHost): void {
  const previewEdits = (args: Record<string, unknown>): ClientToolResult => {
    if (!host.canEdit() && !host.canDelete()) {
      return fail('你在当前学科没有编辑权限，无法改动题目。')
    }
    if (host.hasPending()) {
      return fail('上一批改动还等着用户点「应用」或「放弃」，请先让用户处理，再提交新的改动。')
    }

    const { edits, problems } = parseEdits(args.operations)
    if (!edits.length) {
      return fail(problems.length ? problems.join('；') : 'operations 是空的，没有可执行的改动。')
    }

    const lines: QuestionEditLine[] = edits.map((edit, index) => ({
      key: `${edit.op}-${edit.question_id}-${index}`,
      op: edit.op,
      questionId: edit.question_id,
      stem: clip(edit.before?.stem?.trim() || `题目 #${edit.question_id}`, 50),
      labels: describeQuestionEdit(edit),
    }))

    host.stagePending({
      tool: AI_TOOL_PREVIEW_EDITS,
      summary: args.summary ? clip(String(args.summary).trim(), 60) : `来自助手的 ${edits.length} 处改动`,
      edits,
      lines,
    })

    const destructive = edits.some((e) => e.op === 'delete' || e.op === 'archive')
    const head =
      `已生成 ${edits.length} 处改动的预览，等待用户点「应用」后才会保存：` +
      (destructive ? '\n其中包含归档或删除，请特别确认。' : '')
    return {
      ok: true,
      content: [head, ...lines.map((l) => `- [题目 ${l.questionId}] ${l.labels.join('；')}`)].join('\n'),
      data: { changes: edits.length },
    }
  }

  /**
   * 注册时机与组卷的 usePaperAiTools 一致：本项目的路由没有 keep-alive，
   * mount/unmount 就等于 activate/deactivate。若将来给路由加上
   * <KeepAlive>，这里必须换成 onActivated/onDeactivated ——
   * 否则用户切走的页面仍在 mounted 状态，助手会继续去改一块没人看着的题库。
   */
  function registerAll(): void {
    registerClientTool(AI_TOOL_PREVIEW_EDITS, previewEdits)
  }
  function unregisterAll(): void {
    unregisterClientTool(AI_TOOL_PREVIEW_EDITS)
  }

  onMounted(registerAll)
  onBeforeUnmount(unregisterAll)
}
