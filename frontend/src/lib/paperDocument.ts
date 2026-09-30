/**
 * 组卷画布的文档模型：PaperNode[] ⇄ Tiptap 文档。
 *
 * 这是画布迁移的核心接缝，必须是**纯函数**：它不碰 DOM、不依赖 Vue、
 * 不认识任何组件，因此可以像后端那样被穷尽地测。
 *
 * 为什么要有一层转换，而不是让画布直接吃 PaperNode：
 * 画布要的是「能敲字的连续文档」——光标能停在两个块之间、Enter 能续写上一块、
 * 撤销能一步退掉一次输入。而 PaperNode[] 是「一堆彼此独立的块」。
 * 两者形状不同，但**必须是同一个东西的两种表示**：
 * 转换必须无损往返，否则排版一改就丢节点。
 *
 * 无损往返靠两条硬约定：
 *
 *  1. 每个画布节点的 attrs.nodeId 就是 PaperNode.id。Tiptap 自己也会生成
 *     节点身份，但那是它自己的 uid，和后端的主键对不上；用 nodeId 兜住
 *     之后，AI 工具算出来的 diff、乐观锁的脏检查、定时保存的差异比对
 *     全部还能按原样工作。
 *  2. 后端维护的字段（QuestionContent、Position）**不进转换**。
 *     前端画布只是 PaperNode 的一个视图，没有资格决定这两样。
 *
 * 关于「往返无损」的确切含义：往返保证**语义无损**，允许**默认值实体化**。
 * 例如 props 为空的解析模块，回来时会带上 `scope: "all"`——这不是丢了什么，
 * 是把后端 `domain/paper.go:230` 本来就要做的归一提前做掉，好让画布上的
 * 节点永远有确定的值可渲染。归一化只发生一次，不会逐轮长字段。
 */
import type { JSONContent } from '@tiptap/core'
import type { NodeKind, NodeType, PaperNode, QuestionRef } from '@/api/paper'
import type { RichDoc } from './richdoc'

// ---- 画布节点名 ----

/**
 * 画布节点名。
 *
 * 一律带 `paper` 前缀：画布文档和题目富文本共用同一套 schema
 * （paragraph / table / inlineMath…），不加前缀就会和 StarterKit 撞名，
 * 撞名的后果是解析器认错节点、导出的富文本里混进排版节点。
 */
export const CANVAS_NODE = {
  richText: 'paperRichText',
  heading: 'paperHeading',
  question: 'paperQuestion',
  pageBreak: 'paperPageBreak',
  answerSpace: 'paperAnswerSpace',
  questionDetails: 'paperQuestionDetails',
  answerItem: 'paperAnswerItem',
} as const

/** 画布节点名 → 后端节点类型。 */
const TYPE_BY_CANVAS: Record<string, NodeType> = {
  [CANVAS_NODE.richText]: 'rich_text',
  [CANVAS_NODE.heading]: 'heading',
  [CANVAS_NODE.question]: 'question',
  [CANVAS_NODE.pageBreak]: 'page_break',
  [CANVAS_NODE.answerSpace]: 'answer_space',
  [CANVAS_NODE.questionDetails]: 'question_details',
  [CANVAS_NODE.answerItem]: 'answer_item',
}

const CANVAS_BY_TYPE: Record<string, string> = Object.fromEntries(
  Object.entries(TYPE_BY_CANVAS).map(([canvas, type]) => [type, canvas]),
)

/**
 * 后端节点类型 → 节点层级。
 *
 * 与 `model/enums.go` 的 `nodeKindOf` 一一对应。这里再写一遍而不是
 * 从接口拿，是因为层级决定画布怎么排：block 是可编辑的排版单元，
 * reference 是服务端派生的展示条目，画布里不可编辑。
 */
const KIND_BY_TYPE: Record<string, NodeKind> = {
  rich_text: 'block',
  heading: 'block',
  question: 'block',
  page_break: 'block',
  answer_space: 'block',
  question_details: 'module',
  answer_item: 'reference',
}

// ---- 契约常量 ----

/** 标题层级范围，与 `PaperEditorView` 的 clampLevel 一致。 */
export const HEADING_MIN_LEVEL = 1
export const HEADING_MAX_LEVEL = 6

/** 答题区行数范围，与编辑器弹窗一致。 */
export const ANSWER_MIN_LINES = 1
export const ANSWER_MAX_LINES = 50
export const ANSWER_DEFAULT_LINES = 4

/** 解析模块的挂载范围。 */
export const MODULE_SCOPES = ['all', 'before'] as const
export type ModuleScopeValue = (typeof MODULE_SCOPES)[number]

/** 旧数据把留白高度写在 content.height 上，换算成行数的行高。 */
const ANSWER_LINE_HEIGHT_PX = 30
const LEGACY_ANSWER_HEIGHT_PX = 120

// ---- 读取工具 ----

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function attr(attrs: unknown, key: string): unknown {
  return isRecord(attrs) ? attrs[key] : undefined
}

function attrString(attrs: unknown, key: string): string {
  const value = attr(attrs, key)
  return typeof value === 'string' ? value : ''
}

function attrNumber(attrs: unknown, key: string): number | undefined {
  const value = attr(attrs, key)
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value)
    if (Number.isFinite(parsed)) return parsed
  }
  return undefined
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(Math.round(value), min), max)
}

/** 只在有值时写进对象——空字段会让 JSON 体积翻倍，也让 diff 噪声变大。 */
function compact<T extends Record<string, unknown>>(input: T): T {
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(input)) {
    if (value === undefined || value === null || value === '') continue
    out[key] = value
  }
  return out as T
}

function asRecord(value: unknown): Record<string, unknown> {
  return isRecord(value) ? { ...value } : {}
}

function isRichDoc(value: unknown): value is RichDoc {
  return isRecord(value) && value.type === 'doc' && Array.isArray(value.content)
}

/** 节点内容 → 画布节点的 content。空文档给一个空段落，否则 Tiptap 会拒绝。 */
function toCanvasContent(content: unknown): JSONContent[] {
  if (isRichDoc(content) && content.content.length) {
    return content.content as JSONContent[]
  }
  return [{ type: 'paragraph' }]
}

/** 画布节点的 content → 节点内容。空内容补一个空段落，保持 doc 的最小合法形状。 */
function fromCanvasContent(node: JSONContent): RichDoc {
  const content = Array.isArray(node.content) ? node.content : []
  return { type: 'doc', content: (content.length ? content : [{ type: 'paragraph' }]) as RichDoc['content'] }
}

// ---- 单节点：PaperNode → 画布节点 ----

/**
 * 一个 PaperNode 转成一个画布节点。
 *
 * props 的取舍是这一层最要紧的事：
 * **score 与 number 必须落在 props 上，不能挪进画布节点的其它属性再在
 * 反向转换时塞回来**——那样会与服务端 `sumScore` 和导出器读的位置分叉。
 * 它们在画布里就以 attrs 的形式存在，但 attrs 只是「往返的载体」，
 * 真正的家仍是 props。
 */
export function nodeToCanvas(node: PaperNode): JSONContent | null {
  const props = asRecord(node.props)
  const content = asRecord(node.content)
  const base = { nodeId: node.id, schemaVersion: node.schema_version || 1 }

  switch (node.node_type) {
    case 'rich_text':
      return { type: CANVAS_NODE.richText, attrs: base, content: toCanvasContent(node.content) }

    case 'heading':
      return {
        type: CANVAS_NODE.heading,
        attrs: { ...base, level: clamp(Number(props.level ?? 1), HEADING_MIN_LEVEL, HEADING_MAX_LEVEL) },
        content: toCanvasContent(node.content),
      }

    case 'question': {
      const ref = content as unknown as QuestionRef
      return {
        type: CANVAS_NODE.question,
        attrs: compact({
          ...base,
          questionId: Number(ref?.question_id ?? 0),
          contentRevision: Number(ref?.content_revision ?? 0),
          score: typeof props.score === 'number' ? props.score : undefined,
          number: typeof props.number === 'string' && props.number.trim() ? props.number.trim() : undefined,
        }),
      }
    }

    case 'page_break':
      return { type: CANVAS_NODE.pageBreak, attrs: base }

    case 'answer_space':
      return {
        type: CANVAS_NODE.answerSpace,
        attrs: {
          ...base,
          lines: answerLinesOf(node),
          style: props.style === 'lined' ? 'lined' : 'blank',
        },
      }

    case 'question_details':
      return {
        type: CANVAS_NODE.questionDetails,
        attrs: compact({
          ...base,
          scope: MODULE_SCOPES.includes(props.scope as ModuleScopeValue) ? props.scope : 'all',
          title: typeof props.title === 'string' ? props.title : undefined,
        }),
        // 内容由子节点填充；解析模块可以暂时没有条目。
        content: [],
      }

    case 'answer_item':
      return {
        type: CANVAS_NODE.answerItem,
        attrs: compact({
          ...base,
          sourceQuestionNodeId: node.source_question_node_id ?? undefined,
          anchorBeforeNodeId: node.anchor_before_node_id ?? undefined,
        }),
      }

    default:
      return null
  }
}

/**
 * 答题区行数。
 *
 * 兼容两种来源：新数据写 props.lines，老数据把像素高度写在 content.height。
 * 两者对不上时以 props 为准——它是现行契约，老字段只是别删了而已。
 */
function answerLinesOf(node: PaperNode): number {
  const props = asRecord(node.props)
  if (typeof props.lines === 'number') {
    return clamp(props.lines, ANSWER_MIN_LINES, ANSWER_MAX_LINES)
  }
  const content = asRecord(node.content)
  if (typeof content.height === 'number') {
    return clamp(content.height / ANSWER_LINE_HEIGHT_PX, ANSWER_MIN_LINES, ANSWER_MAX_LINES)
  }
  // 老卷子默认 120px，与导出端硬编码的 4 个空段落吻合。
  return LEGACY_ANSWER_HEIGHT_PX / ANSWER_LINE_HEIGHT_PX || ANSWER_DEFAULT_LINES
}

// ---- 单节点：画布节点 → PaperNode ----

/**
 * 一个画布节点转回 PaperNode。
 *
 * `parentID` 显式传入而不是从上下文猜：画布里父节点一定先于子节点出现，
 * 但「先出现」不等于「已经写进结果」，把 parentID 作为参数能让这个约束
 * 在类型上就是显式的。
 */
export function canvasToNode(node: JSONContent, parentID: string | null, position: number): PaperNode | null {
  // JSONContent.type 在类型上就是可选的（手搓的 JSON 真的可能没有），
  // 而 Record 的索引不能吃 undefined。
  const type = node.type ? TYPE_BY_CANVAS[node.type] : undefined
  if (!type) return null

  const attrs = node.attrs
  const id = attrString(attrs, 'nodeId')
  // 没有 nodeId 的节点无处安放（后端主键是客户端给的 UUID）。
  // 正常流程不会产生这种节点，直接丢掉比造一个假 id 安全。
  if (!id) return null

  const schemaVersion = attrNumber(attrs, 'schemaVersion') ?? 1
  const props = asRecord(node.props)
  const base: PaperNode = {
    id,
    parent_id: parentID,
    position,
    node_kind: KIND_BY_TYPE[type] ?? 'block',
    node_type: type,
    content: null,
    props: {},
    schema_version: schemaVersion,
    source_question_node_id: null,
    anchor_before_node_id: null,
  }

  switch (node.type) {
    case CANVAS_NODE.richText:
      base.content = fromCanvasContent(node)
      base.props = {}
      break

    case CANVAS_NODE.heading:
      base.content = fromCanvasContent(node)
      base.props = { level: clamp(attrNumber(attrs, 'level') ?? Number(props.level ?? 1), HEADING_MIN_LEVEL, HEADING_MAX_LEVEL) }
      break

    case CANVAS_NODE.question:
      base.content = compact({
        question_id: attrNumber(attrs, 'questionId') ?? 0,
        content_revision: attrNumber(attrs, 'contentRevision') ?? 0,
      }) as unknown as PaperNode['content']
      base.props = compact({
        score: attrNumber(attrs, 'score'),
        number: attrString(attrs, 'number') || undefined,
      })
      break

    case CANVAS_NODE.pageBreak:
      base.content = null
      base.props = {}
      break

    case CANVAS_NODE.answerSpace:
      base.content = null
      base.props = {
        lines: clamp(attrNumber(attrs, 'lines') ?? ANSWER_DEFAULT_LINES, ANSWER_MIN_LINES, ANSWER_MAX_LINES),
        style: attrString(attrs, 'style') === 'lined' ? 'lined' : 'blank',
      }
      break

    case CANVAS_NODE.questionDetails: {
      const scope = attr(attrs, 'scope')
      base.content = null
      base.props = compact({
        scope: MODULE_SCOPES.includes(scope as ModuleScopeValue) ? (scope as ModuleScopeValue) : 'all',
        title: attrString(attrs, 'title') || undefined,
      })
      break
    }

    case CANVAS_NODE.answerItem:
      base.content = null
      base.props = {}
      base.source_question_node_id = attrString(attrs, 'sourceQuestionNodeId') || null
      base.anchor_before_node_id = attrString(attrs, 'anchorBeforeNodeId') || null
      break

    default:
      return null
  }

  return base
}

// ---- 整棵树 ----

/**
 * 整棵树：PaperNode[] → 画布文档。
 *
 * 只输出顶层节点；解析模块的条目作为它的 content 嵌进去。
 * 理由：画布上「解析模块」是一个可整体拖动的盒子，条目是盒子里的内容，
 * 而不是和模块平级的兄弟块。服务端派生出来的层级在界面上也该长这样。
 *
 * **空树给一份含一个空段落的文档。** ProseMirror 的 `doc` 要求 `block+`，
 * 一个不含任何块的 doc 是非法的（`check()` 直接报 Invalid content），
 * 编辑器挂载时会炸；而「刚新建、还没排任何块」的试卷是完全正常的状态。
 * 补出来的那个空段落**不带 nodeId**，回程时会被当成编辑残留忽略掉
 * （见 docToNodes 的判定），所以它不会变成一颗幽灵节点回到保存请求里。
 */
export function nodesToDoc(nodes: PaperNode[]): JSONContent {
  const childrenByParent = groupByParent(nodes)
  const content = topLevelOf(nodes).flatMap((node) => {
    const canvas = nodeToCanvas(node)
    if (!canvas) return []
    if (node.node_type === 'question_details') {
      const children = childrenByParent.get(node.id) ?? []
      canvas.content = children
        .slice()
        .sort((a, b) => a.position - b.position)
        .flatMap((child) => {
          const drawn = nodeToCanvas(child)
          return drawn ? [drawn] : []
        })
    }
    return [canvas]
  })

  return {
    type: 'doc',
    content: content.length ? content : [{ type: 'paragraph' }],
  }
}

export interface DocConversion {
  nodes: PaperNode[]
  /**
   * 被丢弃的节点。
   *
   * 正常情况下恒为空：后端的 NodeTypes 是闭集（`model/enums.go` 的
   * `NodeTypes`），能存进库的节点类型都是画布认识的。留这个字段是为了
   * 「万一不是」时能喊出来，而不是静默吞掉用户的排版。
   */
  dropped: { id: string; type: string }[]
}

/** 画布文档 → PaperNode[]。前序展开，父节点先于子节点。 */
export function docToNodes(doc: JSONContent): DocConversion {
  const nodes: PaperNode[] = []
  const dropped: { id: string; type: string }[] = []
  const rootContent = Array.isArray(doc?.content) ? doc.content : []

  rootContent.forEach((child, index) => {
    walk(child, null, index, nodes, dropped)
  })

  return { nodes, dropped }
}

function walk(
  node: JSONContent,
  parentID: string | null,
  position: number,
  out: PaperNode[],
  dropped: { id: string; type: string }[],
): void {
  const id = attrString(node?.attrs, 'nodeId')
  const known = node?.type !== undefined && TYPE_BY_CANVAS[node.type] !== undefined

  if (!known) {
    // 不是画布节点。两种可能，处理方式不同：
    //   - 编辑器补出来的编辑残留（空段落等）：没有任何试卷含义，静静忽略。
    //     只有带 nodeId 的才说明它自认为是一颗试卷节点，那样必须喊出来，
    //     否则排版会被悄悄吞掉。
    if (id) dropped.push({ id, type: String(node?.type ?? '') })
    return
  }

  const converted = canvasToNode(node, parentID, position)
  if (!converted) {
    // 是画布节点类型却没有 nodeId：无处安放（后端主键是客户端给的 UUID）。
    dropped.push({ id, type: String(node?.type ?? '') })
    return
  }
  out.push(converted)

  // 解析模块的条目按前序跟着父节点一起落库；其余节点没有子节点。
  if (node.type !== CANVAS_NODE.questionDetails) return
  const children = Array.isArray(node.content) ? node.content : []
  children.forEach((child, index) => walk(child, converted.id, index, out, dropped))
}

// ---- 共享工具 ----

/** 顶层节点。判定与 PaperEditorView、usePaperAiTools 完全同一套。 */
export function topLevelOf(nodes: PaperNode[]): PaperNode[] {
  return nodes
    .filter((n) => n.parent_id === null && n.node_kind !== 'reference')
    .slice()
    .sort((a, b) => a.position - b.position)
}

function groupByParent(nodes: PaperNode[]): Map<string, PaperNode[]> {
  const map = new Map<string, PaperNode[]>()
  for (const node of nodes) {
    if (!node.parent_id) continue
    const bucket = map.get(node.parent_id)
    if (bucket) bucket.push(node)
    else map.set(node.parent_id, [node])
  }
  return map
}

/** 节点类型 → 画布节点名。新建节点时用它，不必先造 PaperNode 再转。 */
export function canvasNameOf(type: NodeType): string {
  return CANVAS_BY_TYPE[type] ?? CANVAS_NODE.richText
}

/** 节点层级。渲染时用它决定「可编辑」还是「只读」。 */
export function kindOf(type: NodeType): NodeKind {
  return KIND_BY_TYPE[type] ?? 'block'
}

/** 节点是否由服务端派生（画布里只读，不给拖拽/删除）。 */
export function isDerived(type: NodeType): boolean {
  return kindOf(type) === 'reference'
}

/**
 * 保存时要提交的节点。
 *
 * 规则来自 `api/paper.ts` 的约定：**不要把 answer_item 一起回传**。
 * 解析条目是服务端按当前题目顺序派生的，回传上去会和它自己派生的那批
 * 打架——顺序变了就重排，顺序没变也可能留下孤儿。
 *
 * 这条规则以前只写在 `PaperEditorView.toInputs()` 一行里，画布接进来之后
 * 会有第二个提交入口。放在转换层才能保证两个入口遵守同一条。
 */
export function saveableNodes(nodes: PaperNode[]): PaperNode[] {
  return nodes.filter((n) => n.node_kind !== 'reference')
}
