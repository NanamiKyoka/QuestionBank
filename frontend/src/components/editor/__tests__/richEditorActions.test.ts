// @vitest-environment jsdom
/**
 * 域逻辑测试：富文本插入动作。
 *
 * 为什么这些动作值得单独测：它们是**题库的核心内容形态**的入口——
 * 公式、填空、表格、图片。画布切换时工具栏一度没跟着搬过去，
 * 结果是"能敲字但插不了公式"。这个坑不会报错、不会崩，
 * 只是老师发现公式按钮没了。所以这里把每条入口都钉住。
 *
 * 弹输入框的部分（requestInlineMath / requestBlockMath）不测：
 * 它们只负责问一句再把结果交给下面这些函数，而下面这些才是会写坏数据的地方。
 */
import { afterEach, describe, expect, it } from 'vitest'
import { Editor } from '@tiptap/core'
import { canvasViewExtensions } from '../canvasViewExtensions'
import { docToNodes, nodesToDoc } from '@/lib/paperDocument'
import {
  clearFormat,
  insertBlank,
  insertBlockMath,
  insertImage,
  insertInlineMath,
  insertTable,
  selectionInRichBlock,
} from '../richEditorActions'
import { plainToRichDoc } from '@/lib/richdoc'
import type { NodeType, PaperNode } from '@/api/paper'
import { kindOf } from '@/lib/paperDocument'

// ---- 夹具 ----

function node(id: string, nodeType: NodeType, position: number, extra: Partial<PaperNode> = {}): PaperNode {
  return {
    id,
    parent_id: null,
    position,
    node_kind: kindOf(nodeType),
    node_type: nodeType,
    content: null,
    props: {},
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
    ...extra,
  }
}

let editor: Editor | null = null

function mount(nodes: PaperNode[]) {
  editor = new Editor({
    element: document.createElement('div'),
    content: nodesToDoc(nodes),
    extensions: canvasViewExtensions(),
  })
  return editor
}

/**
 * 让光标进入指定的顶层块（nodeId）。
 *
 * 原子节点（题目块、答题区、分页符）没有行内内容，对它们设文本选区会
 * 让 ProseMirror 打一条 "endpoint not pointing into a node with inline
 * content" 的警告——测试输出里不该混着这种噪音。所以原子节点走 NodeSelection。
 */
function selectInside(e: Editor, nodeId: string) {
  let before = -1
  let isAtom = false
  let offset = 0
  e.state.doc.forEach((child) => {
    if (before === -1 && child.attrs.nodeId === nodeId) {
      before = offset
      isAtom = child.isAtom
    }
    offset += child.nodeSize
  })
  if (before < 0) throw new Error(`找不到块 ${nodeId}`)

  if (isAtom) {
    e.commands.setNodeSelection(before)
    return
  }
  // +1 越过节点自身的起始边界，落在它的第一个子节点之前。
  e.commands.setTextSelection(before + 1)
}

/** 当前文档里有没有某种节点。 */
function hasNode(e: Editor, name: string): boolean {
  let found = false
  e.state.doc.descendants((child) => {
    if (child.type.name === name) found = true
    return true
  })
  return found
}

/** 取某个顶层块的富文本内容（回存后的形状）。 */
function richDocOf(e: Editor, nodeId: string) {
  const { nodes } = docToNodes(e.getJSON())
  return nodes.find((n) => n.id === nodeId)?.content
}

afterEach(() => {
  editor?.destroy()
  editor = null
})

const richPaper = () => [node('t', 'rich_text', 0, { content: plainToRichDoc('说明文字') })]

// ---- 公式 ----

describe('insertInlineMath', () => {
  it('插入行内公式节点，latex 原样保存', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    expect(insertInlineMath(e, 'x^2+y^2')).toBe(true)
    expect(hasNode(e, 'inlineMath')).toBe(true)
    const doc = richDocOf(e, 't') as { content: unknown[] }
    expect(JSON.stringify(doc)).toContain('x^2+y^2')
  })

  it('latex 两端空白被去掉', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    insertInlineMath(e, '  a+b  ')
    expect(JSON.stringify(richDocOf(e, 't'))).toContain('a+b')
  })

  it('空 latex 不插入，也不崩', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    expect(insertInlineMath(e, '   ')).toBe(false)
    expect(hasNode(e, 'inlineMath')).toBe(false)
  })

  it('编辑器为 null 时返回 false', () => {
    expect(insertInlineMath(null, 'x')).toBe(false)
  })
})

describe('insertBlockMath', () => {
  it('插入独立公式节点', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    expect(insertBlockMath(e, '\\frac{1}{2}')).toBe(true)
    expect(hasNode(e, 'blockMath')).toBe(true)
  })

  it('空 latex 不插入', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    expect(insertBlockMath(e, '')).toBe(false)
    expect(hasNode(e, 'blockMath')).toBe(false)
  })
})

// ---- 填空 ----

describe('insertBlank', () => {
  it('插入结构化的 blank 节点，而不是一串下划线', () => {
    // 靠下划线去猜"这里要填一个空"在导出与自动判分时都不可靠。
    const e = mount(richPaper())
    selectInside(e, 't')
    expect(insertBlank(e)).toBe(true)
    expect(hasNode(e, 'blank')).toBe(true)
  })

  it('回存后仍是可识别的节点', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    insertBlank(e)
    expect(JSON.stringify(richDocOf(e, 't'))).toContain('"type":"blank"')
  })
})

// ---- 表格与图片 ----

describe('insertTable', () => {
  it('插入表格，且带表头', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    expect(insertTable(e)).toBe(true)
    expect(hasNode(e, 'table')).toBe(true)
    expect(hasNode(e, 'tableHeader')).toBe(true)
  })

  it('回存后表格结构完整（导出端就能取到行列表）', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    insertTable(e)
    const doc = JSON.stringify(richDocOf(e, 't'))
    expect(doc).toContain('tableRow')
    expect(doc).toContain('tableCell')
  })
})

describe('insertImage', () => {
  it('插入图片节点并带上 src', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    expect(insertImage(e, 'https://example.test/a.png')).toBe(true)
    expect(hasNode(e, 'image')).toBe(true)
    expect(JSON.stringify(richDocOf(e, 't'))).toContain('https://example.test/a.png')
  })

  it('空 src 不插入', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    expect(insertImage(e, '')).toBe(false)
    expect(hasNode(e, 'image')).toBe(false)
  })
})

// ---- 清除格式 ----

describe('clearFormat', () => {
  it('清掉标题这类块级类型', () => {
    const e = mount([
      node('h', 'heading', 0, { content: plainToRichDoc('一、选择题'), props: { level: 2 } }),
    ])
    selectInside(e, 'h')
    expect(clearFormat(e)).toBe(true)
    // 标题块内部的段落被清掉类型后仍然是段落，不应再是 heading。
    let innerHeading = false
    e.state.doc.descendants((child) => {
      if (child.type.name === 'heading') innerHeading = true
      return true
    })
    expect(innerHeading).toBe(false)
  })
})

// ---- 工具栏的显示规则 ----

describe('selectionInRichBlock', () => {
  it('光标在说明文字里时为真', () => {
    const e = mount(richPaper())
    selectInside(e, 't')
    expect(selectionInRichBlock(e)).toBe(true)
  })

  it('光标在标题里时为真', () => {
    const e = mount([node('h', 'heading', 0, { content: plainToRichDoc('标题'), props: { level: 1 } })])
    selectInside(e, 'h')
    expect(selectionInRichBlock(e)).toBe(true)
  })

  it('光标在题目块里时为假', () => {
    // 题目块有自己的题号/分值控件，露出公式按钮只会让人以为点了没反应。
    const e = mount([
      node('q', 'question', 0, { content: { question_id: 7, content_revision: 1 } }),
    ])
    selectInside(e, 'q')
    expect(selectionInRichBlock(e)).toBe(false)
  })

  it('光标在答题区/分页符里时为假', () => {
    const e = mount([
      node('pb', 'page_break', 0),
      node('as', 'answer_space', 1, { props: { lines: 3, style: 'blank' } }),
    ])
    selectInside(e, 'pb')
    expect(selectionInRichBlock(e)).toBe(false)
    selectInside(e, 'as')
    expect(selectionInRichBlock(e)).toBe(false)
  })

  it('编辑器为 null 时为假而不是抛错', () => {
    expect(selectionInRichBlock(null)).toBe(false)
    expect(selectionInRichBlock(undefined)).toBe(false)
  })
})
