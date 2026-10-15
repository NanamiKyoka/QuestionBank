/**
 * 组卷编辑器暴露给 AI 的三个客户端工具。
 *
 * 为什么必须在**前端**执行，而且要对着**未保存的工作副本**执行：
 * 用户在编辑器里改了一半还没点保存，后端只看得见上一次落库的状态。
 * AI 直接改数据库的话，用户眼前的卷子一点动静都没有，而下一次自动保存
 * 还会把 AI 的改动整体覆盖掉。读写不同源还会让模型看到的 node_id
 * 和实际编辑目标对不上。
 *
 * 为什么工具**立即**返回：后端的票据 30 秒过期，运行预算更短，
 * 而"用户看一眼再点应用"可能要几分钟。等确认完再回执等于每次都白等一次超时。
 * 因此写操作一律先挂成待确认预览（stagePending），
 * 界面上的应用/放弃闸门（components/CompositionAiChangesBar.vue）才是真正落笔的地方。
 *
 * SECURITY：这三个工具只认结构化操作，**没有任何一个**接受模型给的路径或路由。
 * 智能录题会把外部文档喂给模型，那正是提示注入的载体；
 * 一旦这里出现"打开某页/读某文件"式的能力，就必须是固定枚举而不是模型给什么跳什么。
 */
import { onBeforeUnmount, onMounted } from 'vue'
import type { PaperNode, PaperQuestionProjection, QuestionRef } from '@/api/paper'
import { loadPaperQuestionProjections } from '@/lib/paperQuestion'

export { toPaperProjection } from '@/lib/paperQuestion'
import { plainToRichDoc, richDocToPlain } from '@/lib/richdoc'
import { registerClientTool, unregisterClientTool } from './useAiClientTools'
import type { ClientToolResult } from './useAiClientTools'

export const AI_TOOL_READ_OUTLINE = 'read_paper_outline'
export const AI_TOOL_ADD_QUESTIONS = 'add_questions_to_paper'
export const AI_TOOL_EDIT = 'edit_paper'

// ---- 类型 ----

/** 服务端 prepareEditPaper 富化后的操作。与后端枚举逐字对齐。 */
export type PreparedOpKind =
  | 'insert_text'
  | 'insert_heading'
  | 'insert_page_break'
  | 'remove_node'
  | 'move_node'

const OP_KINDS: readonly PreparedOpKind[] = [
  'insert_text',
  'insert_heading',
  'insert_page_break',
  'remove_node',
  'move_node',
]

export interface PreparedPaperOp {
  op: PreparedOpKind
  /** 服务端转好的富文本（Markdown → RichDoc）。 */
  content?: unknown
  /** 原始 Markdown，供改动清单给人看。 */
  markdown?: string
  node_id?: string
  before_id?: string
  position?: number
  level?: number
}

export interface PaperNodeChange {
  kind: 'added' | 'removed' | 'moved' | 'modified'
  nodeId: string
  label: string
}

/** 一批等待用户确认的改动。 */
export interface PendingPaperChange {
  /** 触发它的工具名，用于界面文案。 */
  tool: string
  summary: string
  /** 应用后的整棵顶层节点（含 position）。 */
  nodes: PaperNode[]
  changes: PaperNodeChange[]
  /** 应用后需要并入的题目内容投影（新增题目时才有）。 */
  projections: Record<number, PaperQuestionProjection>
}

/** 宿主（PaperEditorView.vue）必须提供的读写接口。 */
export interface PaperAiHost {
  getTitle(): string
  getNodes(): PaperNode[]
  getProjections(): Record<number, PaperQuestionProjection>
  isScoringEnabled(): boolean
  /** 工作副本是否有未保存改动——大纲要如实告诉模型它看到的不是落库状态。 */
  isDirty(): boolean
  /** 当前乐观锁修订号。 */
  getRevision(): number
  /** 没有编辑权限的用户只读大纲。 */
  canEdit(): boolean
  /** 已有未确认改动：叠第二批会攒出一份没人看得懂的差异。 */
  hasPending(): boolean
  stagePending(change: PendingPaperChange): void
}

// ---- 小工具 ----

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

/** 深拷贝。节点内容全是 JSON，structuredClone 在个别环境缺失，JSON 往返最省心。 */
function deepCopy<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function clip(text: string, n: number): string {
  const runes = [...text]
  return runes.length <= n ? text : `${runes.slice(0, n).join('')}…`
}

function nodeProps(node: PaperNode): Record<string, unknown> {
  return isRecord(node.props) ? node.props : {}
}

function questionIDOf(node: PaperNode): number | null {
  const content = node.content
  if (!isRecord(content)) return null
  const id = content.question_id
  return typeof id === 'number' && id > 0 ? id : null
}

/** 顶层节点：与 PaperEditorView 的 topNodes 完全同一套判定。 */
function topLevelOf(nodes: PaperNode[]): PaperNode[] {
  return nodes
    .filter((n) => n.parent_id === null && n.node_kind !== 'reference')
    .slice()
    .sort((a, b) => a.position - b.position)
}

const MODULE_TYPE = 'question_details'

function labelOf(node: PaperNode, projections: Record<number, PaperQuestionProjection>): string {
  const qid = questionIDOf(node)
  switch (node.node_type) {
    case 'question': {
      const projection = qid ? projections[qid] : undefined
      const stem = projection?.stem_text?.trim()
      return stem ? `题目：${clip(stem, 40)}` : `题目 #${qid ?? '?'}`
    }
    case 'heading': {
      const level = nodeProps(node).level
      const text = richDocToPlain(node.content).trim()
      return text ? `标题 H${typeof level === 'number' ? level : 1}：${clip(text, 40)}` : '空标题'
    }
    case 'rich_text': {
      const text = richDocToPlain(node.content).trim()
      return text ? `说明文字：${clip(text, 40)}` : '空说明文字'
    }
    case 'page_break':
      return '分页符'
    case 'answer_space':
      return '答题区'
    case MODULE_TYPE:
      return '解析模块'
    default:
      return node.node_type
  }
}

// ---- 读：大纲 ----

/**
 * 把工作副本渲染成给模型看的大纲。
 *
 * 每行都必须带 node_id：edit_paper 的 node_id / before_id 只能用这里的值，
 * 模型凭空编一个 id 只会让前端回一句"找不到节点"。
 */
export function renderPaperOutline(
  title: string,
  nodes: PaperNode[],
  projections: Record<number, PaperQuestionProjection>,
  options: { scoringEnabled: boolean; dirty: boolean; revision: number },
): string {
  const top = topLevelOf(nodes)
  const questions = top.filter((n) => n.node_type === 'question')
  const total = questions.reduce((sum, n) => {
    const score = nodeProps(n).score
    return sum + (typeof score === 'number' && Number.isFinite(score) ? score : 0)
  }, 0)

  const head: string[] = [
    `试卷《${title || '未命名'}》：共 ${questions.length} 题` +
      (options.scoringEnabled ? `，已填分值合计 ${Math.round(total * 100) / 100}` : '') +
      `，修订 r${options.revision}${options.dirty ? '（含未保存改动）' : ''}。`,
    '结构自上而下如下，node_id 可直接用于 edit_paper 的 node_id / before_id：',
  ]

  if (!top.length) {
    head.push('（空试卷，还没有何节点）')
    return head.join('\n')
  }

  const numbers = new Map<string, string>()
  questions.forEach((node, index) => {
    const explicit = nodeProps(node).number
    numbers.set(node.id, typeof explicit === 'string' && explicit.trim() ? explicit.trim() : String(index + 1))
  })

  top.forEach((node, index) => {
    const parts = [`${index + 1}.`, labelOf(node, projections)]
    if (node.node_type === 'question') parts.push(`(第 ${numbers.get(node.id)} 题)`)
    parts.push(`node_id=${node.id}`)
    head.push(parts.join(' '))
  })

  return head.join('\n')
}

// ---- 写：操作应用 ----

export interface OpResult {
  op: PreparedOpKind
  ok: boolean
  message: string
}

function parseOps(input: unknown): { ops: PreparedPaperOp[]; problems: string[] } {
  const ops: PreparedPaperOp[] = []
  const problems: string[] = []
  if (!Array.isArray(input)) {
    return { ops, problems: ['operations 不是数组'] }
  }
  input.forEach((raw, index) => {
    const at = `第 ${index + 1} 个操作`
    if (!isRecord(raw) || typeof raw.op !== 'string' || !OP_KINDS.includes(raw.op as PreparedOpKind)) {
      problems.push(`${at}：op 不受支持`)
      return
    }
    const op: PreparedPaperOp = { op: raw.op as PreparedOpKind }
    if (typeof raw.markdown === 'string') op.markdown = raw.markdown
    if (typeof raw.node_id === 'string') op.node_id = raw.node_id
    if (typeof raw.before_id === 'string') op.before_id = raw.before_id
    if (typeof raw.position === 'number' && Number.isFinite(raw.position)) op.position = raw.position
    if (typeof raw.level === 'number' && Number.isFinite(raw.level)) op.level = raw.level

    if (op.op === 'insert_text' || op.op === 'insert_heading') {
      // 服务端已经把 Markdown 转成富文本下发；这里只兜底它没给的情况，
      // 免得整条操作因为一个字段缺失被丢弃。
      const content = raw.content
      if (isRecord(content) && content.type === 'doc') {
        op.content = content
      } else if (typeof raw.markdown === 'string' && raw.markdown.trim()) {
        op.content = plainToRichDoc(raw.markdown)
      } else {
        problems.push(`${at}：缺少内容`)
        return
      }
    }
    ops.push(op)
  })
  return { ops, problems }
}

/** 客户端生成的节点 id，要避开当前树里已有的（保存是整树覆盖，撞 id 会被整棵拒绝）。 */
function freshNodeID(taken: Set<string>): string {
  let id = crypto.randomUUID()
  while (taken.has(id)) id = crypto.randomUUID()
  taken.add(id)
  return id
}

/**
 * 在一份工作副本上执行一串操作，返回**新的**节点数组。
 *
 * 不就地改传入的数组：改动要先变成一份"如果用户点应用才会发生"的候选，
 * 用户的取消操作只需要丢掉这份候选，不该去回滚已经动过的真数据。
 */
export function applyPaperOperations(
  nodes: PaperNode[],
  ops: PreparedPaperOp[],
): { nodes: PaperNode[]; results: OpResult[] } {
  const taken = new Set(nodes.map((n) => n.id))
  const byID = new Map<string, PaperNode>()
  for (const node of deepCopy(nodes)) byID.set(node.id, node)

  const order = topLevelOf(nodes).map((n) => n.id)
  const moduleID = topLevelOf(nodes).find((n) => n.node_type === MODULE_TYPE)?.id
  const results: OpResult[] = []

  for (const op of ops) {
    switch (op.op) {
      case 'insert_text':
      case 'insert_heading':
      case 'insert_page_break': {
        const nodeType = op.op === 'insert_text' ? 'rich_text' : op.op === 'insert_heading' ? 'heading' : 'page_break'
        const node: PaperNode = {
          id: freshNodeID(taken),
          parent_id: null,
          position: order.length,
          node_kind: 'block',
          node_type: nodeType,
          content: nodeType === 'page_break' ? null : (op.content ?? plainToRichDoc('')),
          props: nodeType === 'heading' ? { level: clampLevel(op.level) } : {},
          schema_version: 1,
          source_question_node_id: null,
          anchor_before_node_id: null,
        }
        byID.set(node.id, node)
        const at = insertionIndex(order, op.before_id, moduleID)
        if (at === -1) {
          // 找不到锚点就没有插入位置，但节点已经建好了，得收回。
          byID.delete(node.id)
          taken.delete(node.id)
          results.push({ op: op.op, ok: false, message: `找不到节点 ${op.before_id}，未插入` })
          break
        }
        order.splice(at, 0, node.id)
        results.push({ op: op.op, ok: true, message: `已插入${blockWord(nodeType)}` })
        break
      }

      case 'remove_node': {
        const id = op.node_id ?? ''
        const index = order.indexOf(id)
        if (index === -1) {
          results.push({ op: op.op, ok: false, message: `找不到节点 ${id}，未删除` })
          break
        }
        if (id === moduleID) {
          // 解析模块是界面上一个开关控制的，它的子条目由服务端派生；
          // 交给 AI 删会留下一堆无人维护的孤儿条目。
          results.push({ op: op.op, ok: false, message: '解析模块请在界面上用开关增删' })
          break
        }
        order.splice(index, 1)
        for (const node of byID.values()) {
          if (node.parent_id === id) byID.delete(node.id)
        }
        results.push({ op: op.op, ok: true, message: '已删除该节点' })
        break
      }

      case 'move_node': {
        const id = op.node_id ?? ''
        const from = order.indexOf(id)
        if (from === -1) {
          results.push({ op: op.op, ok: false, message: `找不到节点 ${id}，未移动` })
          break
        }
        // 两种定位方式活在**不同的坐标系**里，混用会错位一格：
        //
        //   before_id —— 「放到某个节点**之前**」，解出来的是**摘除前**的下标。
        //                 把 [a,b,c] 的 a 放到 c 之前，c 当时在 2；摘掉 a 之后
        //                 那个位置变成 1，所以要补偿。
        //   position  —— 「最终落在第几位」，就是目标下标本身，**不补偿**。
        //                 摘除只改变 from 之后那些节点的下标，不改变"自己
        //                 该落在哪"。
        //
        // 之前两者共用一条补偿，结果 position 永远差一位：向后移落到目标前
        // 一格，而 position 被 clamp 到末位时前后同下标，直接变成空操作——
        // 「把第一题移到最后」这种最常见的诉求做不出来。
        const anchor = op.before_id ?? ''
        const byAnchor = anchor !== ''
        const to = byAnchor ? order.indexOf(anchor) : clampIndex(op.position ?? from, order.length)
        if (to === -1) {
          results.push({ op: op.op, ok: false, message: `找不到节点 ${anchor}，未移动` })
          break
        }
        if (to === from) {
          results.push({ op: op.op, ok: true, message: '已在目标位置' })
          break
        }
        order.splice(from, 1)
        const target = byAnchor && to > from ? to - 1 : to
        order.splice(target, 0, id)
        results.push({ op: op.op, ok: true, message: `已移动到第 ${target + 1} 位` })
        break
      }
    }
  }

  const alive = new Set(order)
  const rebuilt: PaperNode[] = order.map((id, index) => {
    const node = byID.get(id)
    const copy = { ...(node as PaperNode), position: index }
    byID.set(id, copy)
    return copy
  })
  // 非顶层节点（目前只有解析条目）：父节点被删了就跟着走，
  // 否则会留下一批永远不会渲染的孤儿节点。
  for (const node of byID.values()) {
    if (alive.has(node.id)) continue
    if (node.parent_id !== null && alive.has(node.parent_id)) rebuilt.push(node)
  }

  return { nodes: rebuilt, results }
}

function clampLevel(level: number | undefined): number {
  if (typeof level !== 'number' || !Number.isFinite(level)) return 1
  return Math.min(Math.max(Math.round(level), 1), 6)
}

function clampIndex(position: number, length: number): number {
  return Math.min(Math.max(Math.round(position), 0), Math.max(length - 1, 0))
}

/**
 * 插入位置。
 *
 * 解析模块永远排在最后（它是卷末汇总，插在题目中间会打断题序），
 * 因此"插到末尾"实际是插到解析模块之前。
 */
function insertionIndex(order: string[], beforeID: string | undefined, moduleID: string | undefined): number {
  if (beforeID !== undefined && beforeID !== '') {
    const at = order.indexOf(beforeID)
    return at === -1 ? -1 : at
  }
  if (moduleID && order.includes(moduleID)) {
    const at = order.indexOf(moduleID)
    if (at !== -1) return at
  }
  return order.length
}

function blockWord(nodeType: string): string {
  switch (nodeType) {
    case 'heading':
      return '标题'
    case 'page_break':
      return '分页符'
    default:
      return '说明文字'
  }
}

// ---- 差异 ----

/** 比较操作前后的两棵树，供确认条逐条展示。 */
export function diffPaperNodes(
  before: PaperNode[],
  after: PaperNode[],
  projections: Record<number, PaperQuestionProjection>,
): PaperNodeChange[] {
  const beforeTop = topLevelOf(before)
  const afterTop = topLevelOf(after)
  const beforeByID = new Map(beforeTop.map((n) => [n.id, n]))
  const afterByID = new Map(afterTop.map((n) => [n.id, n]))
  const beforePos = new Map(beforeTop.map((n, i) => [n.id, i]))
  const afterPos = new Map(afterTop.map((n, i) => [n.id, i]))
  const changes: PaperNodeChange[] = []

  for (const [id, node] of beforeByID) {
    if (afterByID.has(id)) continue
    changes.push({ kind: 'removed', nodeId: id, label: labelOf(node, projections) })
  }
  for (const [id, node] of afterByID) {
    if (beforeByID.has(id)) continue
    changes.push({ kind: 'added', nodeId: id, label: labelOf(node, projections) })
  }
  for (const [id, node] of afterByID) {
    const old = beforeByID.get(id)
    if (!old) continue
    const newPos = afterPos.get(id) ?? 0
    const oldPos = beforePos.get(id) ?? 0
    if (oldPos !== newPos) {
      changes.push({
        kind: 'moved',
        nodeId: id,
        label: `${labelOf(node, projections)}：第 ${oldPos + 1} 位 → 第 ${newPos + 1} 位`,
      })
      continue
    }
    if (JSON.stringify(old.content) !== JSON.stringify(node.content) ||
        JSON.stringify(old.props) !== JSON.stringify(node.props)) {
      changes.push({ kind: 'modified', nodeId: id, label: labelOf(node, projections) })
    }
  }
  return changes
}

// ---- 三个执行器 ----

function fail(content: string): ClientToolResult {
  return { ok: false, content }
}

function parseQuestionIDs(input: unknown): number[] {
  if (!Array.isArray(input)) return []
  const out: number[] = []
  for (const raw of input) {
    const id = typeof raw === 'number' ? raw : Number(raw)
    if (Number.isInteger(id) && id > 0 && !out.includes(id)) out.push(id)
  }
  return out
}

function readSummary(args: Record<string, unknown>): string {
  const raw = args.summary
  return typeof raw === 'string' ? clip(raw.trim(), 60) : ''
}

export function usePaperAiTools(host: PaperAiHost): void {
  const readOutline = (): ClientToolResult => {
    const nodes = host.getNodes()
    return {
      ok: true,
      content: renderPaperOutline(host.getTitle(), nodes, host.getProjections(), {
        scoringEnabled: host.isScoringEnabled(),
        dirty: host.isDirty(),
        revision: host.getRevision(),
      }),
      data: { node_count: nodes.length },
    }
  }

  const addQuestions = async (args: Record<string, unknown>): Promise<ClientToolResult> => {
    if (!host.canEdit()) return fail('你没有编辑这份试卷的权限，无法加入题目。')
    if (host.hasPending()) {
      return fail('上一批改动还等着用户点「应用」或「放弃」，请先让用户处理，再提交新的改动。')
    }
    const ids = parseQuestionIDs(args.question_ids)
    if (!ids.length) return fail('question_ids 是空的，没有要加入的题目。')

    // 内容投影尽力而为：拉不到也不该让整批改动失败，
    // 那时节点照样能加，只是预览里显示不出题目正文。
    const projections = await loadPaperQuestionProjections(ids)

    const before = host.getNodes()
    const next = [...before]
    const taken = new Set(next.map((n) => n.id))
    const moduleID = topLevelOf(next).find((n) => n.node_type === MODULE_TYPE)?.id
    const order = topLevelOf(next)
    const added: PaperNode[] = []

    for (const id of ids) {
      const content: QuestionRef = {
        question_id: id,
        // 已经在这份卷子里的题，沿用它自己的修订号（界面上的加题也是这么做的）；
        // 新题没有可抄的，记 0 —— 服务端把 0 视为"未记录"，不会误报"内容已更新"。
        content_revision: host.getProjections()[id]?.content_revision ?? 0,
      }
      const node: PaperNode = {
        id: freshNodeID(taken),
        parent_id: null,
        position: 0,
        node_kind: 'block',
        node_type: 'question',
        content,
        props: {},
        schema_version: 1,
        source_question_node_id: null,
        anchor_before_node_id: null,
      }
      added.push(node)
      const at = moduleID && order.some((n) => n.id === moduleID)
        ? order.findIndex((n) => n.id === moduleID)
        : order.length
      order.splice(at === -1 ? order.length : at, 0, node)
      next.push(node)
    }

    const after = rebuildPositions(order, next)
    const changes = diffPaperNodes(before, after, { ...host.getProjections(), ...projections })
    if (!changes.length) return fail('这些题目已经在卷子末尾了，没有产生任何变化。')

    host.stagePending({
      tool: AI_TOOL_ADD_QUESTIONS,
      summary: readSummary(args) || `加入 ${added.length} 道题`,
      nodes: after,
      changes,
      projections,
    })

    return {
      ok: true,
      content: [
        `已生成 ${changes.length} 处改动的预览（加入 ${added.length} 道题），等待用户点「应用」后才会保存：`,
        ...changes.map((c) => `- ${c.label}`),
      ].join('\n'),
      data: { changes: changes.length, added: added.length },
    }
  }

  const editPaper = (args: Record<string, unknown>): ClientToolResult => {
    if (!host.canEdit()) return fail('你没有编辑这份试卷的权限，无法修改试卷结构。')
    if (host.hasPending()) {
      return fail('上一批改动还等着用户点「应用」或「放弃」，请先让用户处理，再提交新的改动。')
    }
    const { ops, problems } = parseOps(args.operations)
    if (!ops.length) {
      return fail(problems.length ? problems.join('；') : 'operations 是空的，没有可执行的操作。')
    }

    const before = host.getNodes()
    const projections = host.getProjections()
    const { nodes, results } = applyPaperOperations(before, ops)
    const failed = results.filter((r) => !r.ok)
    const changes = diffPaperNodes(before, nodes, projections)

    if (!changes.length) {
      const reason = failed.length
        ? failed.map((r) => r.message).join('；')
        : '这批操作没有产生任何实际变化。'
      return fail(reason)
    }

    host.stagePending({
      tool: AI_TOOL_EDIT,
      summary: readSummary(args) || '来自助手的改动',
      nodes,
      changes,
      projections: {},
    })

    const lines = [
      `已生成 ${changes.length} 处改动的预览，等待用户点「应用」确认后才会保存：`,
      ...changes.map((c) => `- ${c.label}`),
    ]
    if (failed.length || problems.length) {
      lines.push('', '以下操作失败，其余已生效：')
      lines.push(...failed.map((r) => `- ${r.op}：${r.message}`))
      lines.push(...problems.map((p) => `- ${p}`))
    }
    return {
      ok: true,
      content: lines.join('\n'),
      data: { changes: changes.length, failed: failed.length + problems.length },
    }
  }

  /**
   * 注册时机。
   *
   * 本项目的路由没有 keep-alive（layouts/DefaultLayout.vue 里是裸 RouterView），
   * 所以 mount/unmount 就等于 activate/deactivate。若将来给路由加上
   * <KeepAlive>，这里必须换成 onActivated/onDeactivated —— 否则用户
   * 切走的页面仍在 mounted 状态，助手会继续去改一块没人看着的卷子。
   */
  function registerAll(): void {
    registerClientTool(AI_TOOL_READ_OUTLINE, readOutline)
    registerClientTool(AI_TOOL_ADD_QUESTIONS, addQuestions)
    registerClientTool(AI_TOOL_EDIT, editPaper)
  }
  function unregisterAll(): void {
    unregisterClientTool(AI_TOOL_READ_OUTLINE)
    unregisterClientTool(AI_TOOL_ADD_QUESTIONS)
    unregisterClientTool(AI_TOOL_EDIT)
  }

  onMounted(registerAll)
  onBeforeUnmount(unregisterAll)
}

/** 重排顶层节点的 position，并把 order 里的节点写回原数组。 */
function rebuildPositions(order: PaperNode[], all: PaperNode[]): PaperNode[] {
  const kept = new Set(order.map((n) => n.id))
  const out: PaperNode[] = order.map((node, index) => ({ ...node, position: index }))
  for (const node of all) {
    if (kept.has(node.id)) continue
    if (node.parent_id === null) continue
    out.push(node)
  }
  return out
}
