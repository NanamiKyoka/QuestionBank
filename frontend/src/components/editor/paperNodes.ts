/**
 * 组卷画布的 Tiptap 节点定义。
 *
 * 只有 schema，没有 Vue 组件：NodeView（看得见的部分）放在 paper-node-views/ 下，
 * 这样本文件可以像 paperDocument.ts 一样被直接测。
 *
 * 三条设计约束：
 *
 *  1. **nodeId 是主键，不是装饰。** ProseMirror 自己也给节点身份（它用来做
 *     selection mapping），但那是它内部的 uid，和后端的主键对不上。拖动一次、
 *     撤销一次之后两者就再也合不上了。nodeId 每次都跟着 attrs 走，是整条
 *     链路（AI diff、脏检查、定时保存比对）能继续工作的前提。
 *
 *  2. **画布节点不进 `block` 组。** 富文本块的内容表达式白名单里只列了
 *     StarterKit 那些块，故意不写 `block`——否则「说明文字」里就能塞进一道题，
 *     两层 AST 立刻被破坏，后端按 parent_id 重建时会把题目当成块的内容丢掉。
 *
 *  3. **凡是服务端维护的都不做成 attr。** QuestionContent（冻结的题目内容）
 *     不进画布：它是服务端在入卷时拍的快照，前端改它等于伪造快照。
 *     导出与预览一律走 `/papers/:id` 返回的 projections。
 */
import { Node, mergeAttributes } from '@tiptap/core'
import {
  ANSWER_MAX_LINES,
  ANSWER_MIN_LINES,
  HEADING_MAX_LEVEL,
  HEADING_MIN_LEVEL,
} from '@/lib/paperDocument'

/**
 * 富文本块允许的内容。
 *
 * 三个坑都在这一行里，每一个都只有 `check()` 能发现：
 *
 *  1. `|` 是**选择**，空格是**序列**。`paragraph blockquote` 读作
 *     「先一个段落、紧接着一个引用块」，不是「段落或引用块」；
 *     写成空格会让一个普通段落都被判非法。
 *  2. `|` 只匹配**一个**节点。要允许「一段说明 + 一个块级公式 + 一张表」
 *     这样的组合，必须整体加 `*`（这里是 `(…)*`，允许任意多个、任意顺序）。
 *  3. 只能列**块级**节点。`hardBreak` 与 `inlineMath`/`blank` 是行内节点，
 *     它们属于段落内部；混进来会被 ProseMirror 以
 *     「Mixing inline and block content」直接拒绝建 schema。
 *
 * 白名单里刻意没有 paper* 节点：画布节点因此无法被拖进说明文字里，
 * 「题目只能是顶层块」这条两层 AST 的底线由 schema 本身兜住。
 */
const RICH_CONTENT = `(${[
  'paragraph',
  'blockquote',
  'bulletList',
  'orderedList',
  'codeBlock',
  'table',
  'blockMath',
  'image',
].join(' | ')})*`

/** nodeId / schemaVersion 是一切画布节点共有的两个属性。 */
function commonAttributes() {
  return {
    nodeId: {
      default: '',
      parseHTML: (element: HTMLElement) => element.getAttribute('data-node-id') ?? '',
      renderHTML: (attributes: Record<string, unknown>) => ({ 'data-node-id': attributes.nodeId }),
    },
    schemaVersion: {
      default: 1,
      parseHTML: (element: HTMLElement) => Number(element.getAttribute('data-schema-version') ?? 1) || 1,
      renderHTML: (attributes: Record<string, unknown>) => ({
        'data-schema-version': String(attributes.schemaVersion ?? 1),
      }),
    },
  }
}

// ---- 可编辑的富文本块 ----

/**
 * 说明文字。
 *
 * 排在整份试卷的骨架里，本身就是能直接敲字的文本块——不是卡片，
 * 也不是弹窗。敲字、换行、插公式都在这里发生。
 */
export const PaperRichText = Node.create({
  name: 'paperRichText',
  group: 'block',
  content: RICH_CONTENT,
  defining: true,

  addAttributes: commonAttributes,

  parseHTML() {
    return [{ tag: 'div[data-qm-node="rich_text"]' }]
  },

  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-qm-node': 'rich_text' }), 0]
  },
})

/**
 * 标题块。
 *
 * 内容表达式与说明文字相同（都允许多个块），而不是收窄成单段落：
 * 老卷子里可能存着多段落的标题，收窄会在**读取**时就丢掉后面的段落，
 * 而丢内容这件事在任何阶段都不可接受。多段落标题会由 NodeView 放进同一个
 * 标题容器里渲染。
 */
export const PaperHeading = Node.create({
  name: 'paperHeading',
  group: 'block',
  content: RICH_CONTENT,
  defining: true,

  addAttributes() {
    return {
      ...commonAttributes(),
      level: {
        default: 1,
        parseHTML: (element: HTMLElement) => {
          const raw = Number(element.getAttribute('data-level') ?? 1)
          return Math.min(Math.max(raw || 1, HEADING_MIN_LEVEL), HEADING_MAX_LEVEL)
        },
        renderHTML: (attributes: Record<string, unknown>) => ({
          'data-level': String(attributes.level ?? 1),
        }),
      },
    }
  },

  parseHTML() {
    return [{ tag: 'div[data-qm-node="heading"]' }]
  },

  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-qm-node': 'heading' }), 0]
  },
})

// ---- 原子块 ----

/**
 * 题目引用。
 *
 * atom：光标不会跑进题目内部，也就不会出现"题干被改得半残"。
 * 题干、选项、答案都是从 projections 读的，卷子本身只持有
 * questionId / contentRevision 与分值、题号。
 */
export const PaperQuestion = Node.create({
  name: 'paperQuestion',
  group: 'block',
  atom: true,
  draggable: true,
  selectable: true,
  isolating: true,

  addAttributes() {
    return {
      ...commonAttributes(),
      questionId: {
        default: 0,
        parseHTML: (element: HTMLElement) => Number(element.getAttribute('data-question-id') ?? 0),
        renderHTML: (attributes: Record<string, unknown>) => ({
          'data-question-id': String(attributes.questionId ?? 0),
        }),
      },
      contentRevision: {
        default: 0,
        parseHTML: (element: HTMLElement) => Number(element.getAttribute('data-content-revision') ?? 0),
        renderHTML: (attributes: Record<string, unknown>) => ({
          'data-content-revision': String(attributes.contentRevision ?? 0),
        }),
      },
      score: {
        default: null,
        parseHTML: (element: HTMLElement) => {
          const raw = element.getAttribute('data-score')
          return raw === null ? null : Number(raw)
        },
        renderHTML: (attributes: Record<string, unknown>) =>
          attributes.score === null || attributes.score === undefined
            ? {}
            : { 'data-score': String(attributes.score) },
      },
      number: {
        default: null,
        parseHTML: (element: HTMLElement) => element.getAttribute('data-number'),
        renderHTML: (attributes: Record<string, unknown>) =>
          attributes.number ? { 'data-number': String(attributes.number) } : {},
      },
    }
  },

  parseHTML() {
    return [{ tag: 'div[data-qm-node="question"]' }]
  },

  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-qm-node': 'question' })]
  },
})

/** 分页符。 */
export const PaperPageBreak = Node.create({
  name: 'paperPageBreak',
  group: 'block',
  atom: true,
  draggable: true,
  selectable: true,

  addAttributes: commonAttributes,

  parseHTML() {
    return [{ tag: 'div[data-qm-node="page_break"]' }]
  },

  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-qm-node': 'page_break' })]
  },
})

/**
 * 答题区。
 *
 * lines 存的是**行数**而不是像素：老数据把高度写在 content.height 上，
 * 换算在 paperDocument.answerLinesOf 里做，存进 attrs 时已经是行数了。
 * 这样导出端读到的和画布上看到的是同一个量。
 */
export const PaperAnswerSpace = Node.create({
  name: 'paperAnswerSpace',
  group: 'block',
  atom: true,
  draggable: true,
  selectable: true,

  addAttributes() {
    return {
      ...commonAttributes(),
      lines: {
        default: 4,
        parseHTML: (element: HTMLElement) => {
          const raw = Number(element.getAttribute('data-lines') ?? 4)
          return Math.min(Math.max(raw || 4, ANSWER_MIN_LINES), ANSWER_MAX_LINES)
        },
        renderHTML: (attributes: Record<string, unknown>) => ({
          'data-lines': String(attributes.lines ?? 4),
        }),
      },
      style: {
        default: 'blank',
        parseHTML: (element: HTMLElement) => element.getAttribute('data-style') ?? 'blank',
        renderHTML: (attributes: Record<string, unknown>) => ({
          'data-style': String(attributes.style ?? 'blank'),
        }),
      },
    }
  },

  parseHTML() {
    return [{ tag: 'div[data-qm-node="answer_space"]' }]
  },

  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-qm-node': 'answer_space' })]
  },
})

// ---- 解析模块 ----

/**
 * 解析模块：卷末的答案与解析汇总。
 *
 * 它是唯一一个带子节点的画布节点。子节点只能是 answerItem，
 * 且内容表达式用 `*` 而不是 `+`——空模块是合法状态
 * （用户开了模块但还没定稿，条目要等服务端派生）。
 */
export const PaperQuestionDetails = Node.create({
  name: 'paperQuestionDetails',
  group: 'block',
  content: 'paperAnswerItem*',
  draggable: true,
  selectable: true,

  addAttributes() {
    return {
      ...commonAttributes(),
      scope: {
        default: 'all',
        parseHTML: (element: HTMLElement) => element.getAttribute('data-scope') ?? 'all',
        renderHTML: (attributes: Record<string, unknown>) => ({
          'data-scope': String(attributes.scope ?? 'all'),
        }),
      },
      title: {
        default: null,
        parseHTML: (element: HTMLElement) => element.getAttribute('data-title'),
        renderHTML: (attributes: Record<string, unknown>) =>
          attributes.title ? { 'data-title': String(attributes.title) } : {},
      },
    }
  },

  parseHTML() {
    return [{ tag: 'div[data-qm-node="question_details"]' }]
  },

  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-qm-node': 'question_details' }), 0]
  },
})

/**
 * 单题解析条目。
 *
 * 服务端派生，**只读**：不可编辑、不可拖动、不可删除。
 * dragging / selectable 都关掉，是为了让它在界面上"看得见但碰不着"，
 * 而不是只在删除时才发现点不动。
 */
export const PaperAnswerItem = Node.create({
  name: 'paperAnswerItem',
  atom: true,
  draggable: false,
  selectable: false,
  defining: false,

  addAttributes() {
    return {
      ...commonAttributes(),
      sourceQuestionNodeId: {
        default: null,
        parseHTML: (element: HTMLElement) => element.getAttribute('data-source-node-id'),
        renderHTML: (attributes: Record<string, unknown>) =>
          attributes.sourceQuestionNodeId
            ? { 'data-source-node-id': String(attributes.sourceQuestionNodeId) }
            : {},
      },
      anchorBeforeNodeId: {
        default: null,
        parseHTML: (element: HTMLElement) => element.getAttribute('data-anchor-node-id'),
        renderHTML: (attributes: Record<string, unknown>) =>
          attributes.anchorBeforeNodeId
            ? { 'data-anchor-node-id': String(attributes.anchorBeforeNodeId) }
            : {},
      },
    }
  },

  parseHTML() {
    return [{ tag: 'div[data-qm-node="answer_item"]' }]
  },

  renderHTML({ HTMLAttributes }) {
    return ['div', mergeAttributes(HTMLAttributes, { 'data-qm-node': 'answer_item' })]
  },
})

/** 画布要注册的全部节点。顺序无关，但别漏。 */
export const PAPER_NODES = [
  PaperRichText,
  PaperHeading,
  PaperQuestion,
  PaperPageBreak,
  PaperAnswerSpace,
  PaperQuestionDetails,
  PaperAnswerItem,
]

/** 富文本块里可以敲进去的节点类型，供斜杠命令筛选用。 */
export const RICH_TEXT_BLOCK_TYPES = new Set(['paragraph', 'bulletList', 'orderedList', 'blockquote'])
