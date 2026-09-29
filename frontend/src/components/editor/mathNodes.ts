import { Node, mergeAttributes } from '@tiptap/core'

/**
 * 自定义节点：行内公式、独立公式、填空空位。
 *
 * 这三个节点的 JSON 结构必须与后端的 RichDoc 完全一致
 * （{ type: 'inlineMath', attrs: { latex } } 等）。因此它们不能用
 * 普通文本代替——那样保存到后端就变成了普通文字，导出 Word 时
 * 也不会被转成 OMML 公式。
 *
 * 节点都是 atom（原子）：内部不可编辑，整体选中与删除。
 * 这样光标不会跑进公式中间，也就不会出现"公式被改得半残"的情况。
 */

export const InlineMath = Node.create({
  name: 'inlineMath',
  group: 'inline',
  inline: true,
  atom: true,
  selectable: true,

  addAttributes() {
    return {
      latex: {
        default: '',
        parseHTML: (element) => element.getAttribute('data-latex') ?? '',
        renderHTML: (attributes) => ({ 'data-latex': attributes.latex }),
      },
    }
  },

  parseHTML() {
    return [{ tag: 'span[data-latex]' }]
  },

  renderHTML({ HTMLAttributes }) {
    const latex = (HTMLAttributes['data-latex'] as string) ?? ''
    return [
      'span',
      mergeAttributes(HTMLAttributes, { class: 'qm-inline-math' }),
      `$${latex}$`,
    ]
  },
})

export const BlockMath = Node.create({
  name: 'blockMath',
  group: 'block',
  atom: true,
  selectable: true,

  addAttributes() {
    return {
      latex: {
        default: '',
        parseHTML: (element) => element.getAttribute('data-latex') ?? '',
        renderHTML: (attributes) => ({ 'data-latex': attributes.latex }),
      },
    }
  },

  parseHTML() {
    return [{ tag: 'div[data-latex]' }]
  },

  renderHTML({ HTMLAttributes }) {
    const latex = (HTMLAttributes['data-latex'] as string) ?? ''
    return [
      'div',
      mergeAttributes(HTMLAttributes, { class: 'qm-block-math' }),
      `$$${latex}$$`,
    ]
  },
})

/**
 * 填空空位。
 *
 * 与后端 RichDoc 的 blank 节点对应。它让"这里要填一个空"
 * 成为结构化信息，而不是靠一串下划线去猜——后者在导出与自动判分时
 * 都没法可靠识别。
 */
export const Blank = Node.create({
  name: 'blank',
  group: 'inline',
  inline: true,
  atom: true,
  selectable: true,

  parseHTML() {
    return [{ tag: 'span[data-blank]' }]
  },

  renderHTML() {
    return [
      'span',
      mergeAttributes({ 'data-blank': 'true', class: 'qm-blank' }),
      '______',
    ]
  },
})
