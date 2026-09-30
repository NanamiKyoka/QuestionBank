/**
 * 组卷的**展示计算**：题号、分值、题目清单。
 *
 * 从 PaperEditorView.vue 里抽出来，理由不是"文件太长"，而是画布迁移的
 * 硬需求：同一份卷子马上会有两套渲染（旧的卡片列表与新的 Tiptap 画布），
 * 两边必须算出**一模一样**的题号与合计分。逻辑留在某个 .vue 里，
 * 另一套渲染就只能抄一遍——而抄第二遍的那次，多半就是它们开始不一致的时候。
 *
 * 这里全是纯函数：不改入参、不碰响应式、不认识 Vue。
 */
import type {
  NodeKind,
  NumberingMode,
  PaperNode,
  QuestionNodeProps,
  QuestionRef,
} from '@/api/paper'
import { QUESTION_NUMBER_MAX_LENGTH } from '@/api/paper'

// ---- 读取 ----

/** 节点 props 的宽松读取。props 在契约上是 unknown（服务端说什么形状就什么形状）。 */
export function nodeProps<T>(node: PaperNode): T {
  return (node.props ?? {}) as T
}

/** 节点上的显式题号。交给自动编号时返回空串。 */
export function numberOf(node: PaperNode): string {
  const raw = nodeProps<QuestionNodeProps>(node).number
  return typeof raw === 'string' ? raw.trim() : ''
}

/** 标题层级，缺失或非法时按 1 级算。 */
export function headingLevelOf(node: PaperNode): number {
  const level = nodeProps<{ level?: unknown }>(node).level
  return typeof level === 'number' && Number.isFinite(level) && level >= 1 ? Math.round(level) : 1
}

/** 题目块的分值。未填写时返回 null（null 与 0 分是两件事）。 */
export function scoreOf(node: PaperNode): number | null {
  const raw = nodeProps<QuestionNodeProps>(node).score
  return typeof raw === 'number' && Number.isFinite(raw) ? raw : null
}

/** 卷内的题目块，按顺序。 */
export function questionNodesOf(topNodes: PaperNode[]): PaperNode[] {
  return topNodes.filter((n) => n.node_type === 'question')
}

/** 题目节点引用的 question_id。拿不到时返回 null。 */
export function questionIDOf(node: PaperNode): number | null {
  const ref = (node.content ?? null) as QuestionRef | null
  const id = ref?.question_id
  return typeof id === 'number' && id > 0 ? id : null
}

// ---- 题号计算 ----

/**
 * 按所选方式算出每个题目块该有的题号。
 *
 * 'heading' 以**二级标题**为分界：一级标题不分组（卷面顶层通常只有卷名，
 * 用它分组等于整卷只有一组）。首个二级标题之前的题目归第 1 组；
 * 两个二级标题之间没有题目的空组会被跳过，编号不会白白多出一段。
 */
export function computeNumbers(list: PaperNode[], mode: NumberingMode): Map<string, string> {
  const out = new Map<string, string>()
  let global = 0
  let group = 0
  let inGroup = 0
  let pendingNewGroup = false

  for (const node of list) {
    if (node.node_type === 'heading' && headingLevelOf(node) === 2) {
      // 还没开过组就先记着，等真有题进来时再开第 1 组。
      if (group > 0) pendingNewGroup = true
      continue
    }
    if (node.node_type !== 'question') continue

    if (mode === 'global') {
      global += 1
      out.set(node.id, String(global))
      continue
    }
    if (group === 0) {
      group = 1
      inGroup = 0
    } else if (pendingNewGroup) {
      group += 1
      inGroup = 0
      pendingNewGroup = false
    }
    inGroup += 1
    out.set(node.id, `${group}.${inGroup}`)
  }
  return out
}

/**
 * 卷面上实际显示的题号：手填的优先，没填的按顺序号兜底。
 *
 * 与导出器取的顺序一致（`props.number` 优先、位置计数兜底）。
 * 这个一致性很重要：界面上显示 5、导出出来也必须是 5。
 */
export function displayNumbers(topNodes: PaperNode[]): Map<string, string> {
  const map = new Map<string, string>()
  questionNodesOf(topNodes).forEach((node, index) => {
    map.set(node.id, numberOf(node) || String(index + 1))
  })
  return map
}

// ---- 题号与分值的写入（返回新 props，不就地改） ----

/**
 * 写入题号，返回新的 props。
 *
 * 截断而不是拒绝：老师在框里敲到第 17 个字时，拦下整次输入反而更烦。
 * 清空等于「这题不指定题号」，**删键而不是写空串**——空串会被导出器
 * 当成有效值，卷面上就会出现一个空题号。
 */
export function propsWithNumber(node: PaperNode, value: string): Record<string, unknown> {
  const next = value.trim().slice(0, QUESTION_NUMBER_MAX_LENGTH)
  const props: Record<string, unknown> = { ...nodeProps<Record<string, unknown>>(node) }
  if (next) props.number = next
  else delete props.number
  return props
}

/**
 * 写入分值，返回新的 props。
 *
 * null / 非法值等于「这题不计分」，同样是删键而不是写 0——
 * 0 分是一道真实存在的、得分为零的题，和"没设分值"不同。
 */
export function propsWithScore(node: PaperNode, value: number | null | undefined): Record<string, unknown> {
  const props: Record<string, unknown> = { ...nodeProps<Record<string, unknown>>(node) }
  if (typeof value === 'number' && Number.isFinite(value) && value >= 0) props.score = value
  else delete props.score
  return props
}

// ---- 合计 ----

/** 已填分值之和。未填分值的题按 0 计（不影响其它题的合计）。 */
export function totalScoreOf(nodes: PaperNode[]): number {
  return nodes.reduce((sum, node) => {
    if (node.node_type !== 'question') return sum
    return sum + (scoreOf(node) ?? 0)
  }, 0)
}

/**
 * 把浮点合计收敛到两位小数。
 *
 * 0.1 + 0.2 在二进制浮点下是 0.30000000000000004，直接显示会让老师
 * 以为系统算错了分。导出的合计也走同一个收敛。
 */
export function roundScore(value: number): number {
  return Math.round(value * 100) / 100
}

// ---- 层级 ----

/** 顶层块（与 paperDocument.topLevelOf 同一套判定，这里只做只读筛选）。 */
export function topLevelOf(nodes: PaperNode[]): PaperNode[] {
  return nodes
    .filter((n) => n.parent_id === null && n.node_kind !== 'reference')
    .slice()
    .sort((a, b) => a.position - b.position)
}

/** 节点是否属于某个层级。用于渲染时决定可编辑性。 */
export function isKind(node: PaperNode, kind: NodeKind): boolean {
  return node.node_kind === kind
}
