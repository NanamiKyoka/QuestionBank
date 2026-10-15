// @vitest-environment jsdom
/**
 * 域逻辑测试：画布上的块级操作。
 *
 * 这一层不是纯函数——它要真的动 ProseMirror 事务，所以跑在 jsdom 下，
 * 用真实编辑器实例验证。
 *
 * 为什么值得为它开一个 DOM 环境：上移/下移的下标计算是**最容易错一格**的
 * 地方，而且错了以后表现是"点了没反应"或者"跳到奇怪的位置"，
 * 靠肉眼看界面很难定位。给真实文档喂几个块，就能把它钉死。
 */
import { afterEach, describe, expect, it } from 'vitest'
import { Editor } from '@tiptap/core'
import { canvasViewExtensions } from '../canvasViewExtensions'
import { docToNodes, nodesToDoc } from '@/lib/paperDocument'
import {
  canMoveTopLevelBlock,
  deleteTopLevelBlock,
  moveTopLevelBlock,
  moveTopLevelBlockToIndex,
  topLevelCount,
  topLevelIndexOf,
} from '../paperBlockOps'
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

function text(id: string, pos: number, body: string): PaperNode {
  return node(id, 'rich_text', pos, { content: plainToRichDoc(body) })
}

function question(id: string, pos: number, qid: number): PaperNode {
  return node(id, 'question', pos, { content: { question_id: qid, content_revision: 1 } })
}

/**
 * 当前文档里**试卷块**的 nodeId 顺序。
 *
 * 按 nodeId 过滤是必须的：ProseMirror 在删除之后会补一个空段落给光标落脚
 * （它没有 nodeId），那是编辑器的脚手架、不是试卷的块。`docToNodes` 对这类
 * 残留正是静默忽略的（见 paperDocument 的 walk），所以断言也该忽略它们——
 * 否则测试会去追一个不属于数据模型的东西。
 */
function order(editor: Editor): string[] {
  const out: string[] = []
  editor.state.doc.forEach((child) => {
    const id = child.attrs.nodeId
    if (typeof id === 'string' && id) out.push(id)
  })
  return out
}

/** 某个顶层块的起始位置。找不到时返回 -1。 */
function posOf(editor: Editor, nodeId: string): number {
  let found = -1
  let offset = 0
  editor.state.doc.forEach((child) => {
    if (found === -1 && child.attrs.nodeId === nodeId) found = offset
    offset += child.nodeSize
  })
  return found
}

/** 文档里真正的试卷块数量（不含编辑器补出来的空段落）。 */
function countPaperBlocks(editor: Editor): number {
  return order(editor).length
}

let editor: Editor | null = null

function mount(nodes: PaperNode[]) {
  editor = new Editor({ element: document.createElement('div'), content: nodesToDoc(nodes), extensions: canvasViewExtensions() })
  return editor
}

afterEach(() => {
  editor?.destroy()
  editor = null
})

const threeBlocks = () => [text('a', 0, '甲'), question('b', 1, 1), text('c', 2, '丙')]

// ---- 读 ----

describe('topLevelIndexOf / topLevelCount', () => {
  it('给出顶层块的下标', () => {
    const e = mount(threeBlocks())
    expect(topLevelIndexOf(e, posOf(e, 'a'))).toBe(0)
    expect(topLevelIndexOf(e, posOf(e, 'b'))).toBe(1)
    expect(topLevelIndexOf(e, posOf(e, 'c'))).toBe(2)
  })

  it('块数正确', () => {
    expect(topLevelCount(mount(threeBlocks()))).toBe(3)
  })

  it('越界位置返回 -1 而不是抛错', () => {
    const e = mount(threeBlocks())
    expect(topLevelIndexOf(e, -1)).toBe(-1)
    expect(topLevelIndexOf(e, e.state.doc.content.size + 10)).toBe(-1)
  })

  it('解析模块的条目不算顶层块（不参与块级移动）', () => {
    const e = mount([
      question('q', 0, 1),
      node('m', 'question_details', 1),
      node('a1', 'answer_item', 0, { parent_id: 'm', source_question_node_id: 'q' }),
    ])
    // 顶层只有题目与模块两块。
    expect(topLevelCount(e)).toBe(2)
    // 条目在其父节点内部，深度不为 0。
    let insideItem = -1
    e.state.doc.descendants((child, pos) => {
      if (child.type.name === 'paperAnswerItem') insideItem = pos
      return true
    })
    expect(insideItem).toBeGreaterThan(-1)
    expect(topLevelIndexOf(e, insideItem)).toBe(-1)
  })
})

// ---- 移动 ----

describe('moveTopLevelBlock', () => {
  it('下移一格', () => {
    const e = mount(threeBlocks())
    expect(moveTopLevelBlock(e, posOf(e, 'a'), 1)).toBe(true)
    expect(order(e)).toEqual(['b', 'a', 'c'])
  })

  it('上移一格', () => {
    const e = mount(threeBlocks())
    expect(moveTopLevelBlock(e, posOf(e, 'c'), -1)).toBe(true)
    expect(order(e)).toEqual(['a', 'c', 'b'])
  })

  it('可以把第一个块一路移到最后', () => {
    // 回归：这类"连续下移"曾经因为下标补偿写错而永远差一格。
    const e = mount(threeBlocks())
    moveTopLevelBlock(e, posOf(e, 'a'), 1)
    moveTopLevelBlock(e, posOf(e, 'a'), 1)
    expect(order(e)).toEqual(['b', 'c', 'a'])
  })

  it('可以把最后一个块一路移到最前', () => {
    const e = mount(threeBlocks())
    moveTopLevelBlock(e, posOf(e, 'c'), -1)
    moveTopLevelBlock(e, posOf(e, 'c'), -1)
    expect(order(e)).toEqual(['c', 'a', 'b'])
  })

  it('在顶端继续上移是空操作并返回 false', () => {
    // "上移"在第一个块上不该变成"移到最后"。
    const e = mount(threeBlocks())
    const before = order(e)
    expect(moveTopLevelBlock(e, posOf(e, 'a'), -1)).toBe(false)
    expect(order(e)).toEqual(before)
  })

  it('在底端继续下移是空操作并返回 false', () => {
    const e = mount(threeBlocks())
    const before = order(e)
    expect(moveTopLevelBlock(e, posOf(e, 'c'), 1)).toBe(false)
    expect(order(e)).toEqual(before)
  })

  it('delta 为 0 时不动', () => {
    const e = mount(threeBlocks())
    expect(moveTopLevelBlock(e, posOf(e, 'a'), 0)).toBe(false)
  })

  it('移动不改变块的内容', () => {
    const e = mount(threeBlocks())
    moveTopLevelBlock(e, posOf(e, 'a'), 1)
    // 找到移动后的 a，确认还是那段文字。
    let textOfA = ''
    e.state.doc.forEach((child) => {
      if (child.attrs.nodeId === 'a') textOfA = child.textContent
    })
    expect(textOfA).toBe('甲')
  })

  it('移动不改变块的总数', () => {
    const e = mount(threeBlocks())
    moveTopLevelBlock(e, posOf(e, 'a'), 1)
    // 用"试卷块数"而不是 childCount：任何一次结构性改动之后，
    // ProseMirror 都可能补一个没有 nodeId 的空段落给光标落脚。
    expect(countPaperBlocks(e)).toBe(3)
  })

  it('结构性改动之后补出来的脚手架不影响试卷块', () => {
    const e = mount(threeBlocks())
    moveTopLevelBlock(e, posOf(e, 'a'), 1)
    const conversion = docToNodes(e.getJSON())
    expect(conversion.nodes.map((n) => n.id)).toEqual(['b', 'a', 'c'])
    expect(conversion.dropped).toEqual([])
  })

  it('三块反复移动后始终是同一批 id', () => {
    const e = mount(threeBlocks())
    for (let i = 0; i < 12; i++) {
      moveTopLevelBlock(e, posOf(e, 'a'), 1)
      moveTopLevelBlock(e, posOf(e, 'c'), -1)
      moveTopLevelBlock(e, posOf(e, 'b'), 1)
      expect([...order(e)].sort()).toEqual(['a', 'b', 'c'])
    }
  })

  it('解析模块也能移动（它是普通顶层块）', () => {
    const e = mount([question('q', 0, 1), node('m', 'question_details', 1)])
    expect(moveTopLevelBlock(e, posOf(e, 'm'), -1)).toBe(true)
    expect(order(e)).toEqual(['m', 'q'])
  })

  it('把块拖到指定插入位置', () => {
    const e = mount(threeBlocks())
    expect(moveTopLevelBlockToIndex(e, posOf(e, 'a'), 2)).toBe(true)
    expect(order(e)).toEqual(['b', 'a', 'c'])
  })

  it('拖到文档末尾与拖到文档开头', () => {
    const e = mount(threeBlocks())
    expect(moveTopLevelBlockToIndex(e, posOf(e, 'a'), 3)).toBe(true)
    expect(order(e)).toEqual(['b', 'c', 'a'])
    expect(moveTopLevelBlockToIndex(e, posOf(e, 'a'), 0)).toBe(true)
    expect(order(e)).toEqual(['a', 'b', 'c'])
  })
})

describe('canMoveTopLevelBlock', () => {
  it('与 move 的结果一致', () => {
    const e = mount(threeBlocks())
    expect(canMoveTopLevelBlock(e, posOf(e, 'a'), -1)).toBe(false)
    expect(canMoveTopLevelBlock(e, posOf(e, 'a'), 1)).toBe(true)
    expect(canMoveTopLevelBlock(e, posOf(e, 'c'), 1)).toBe(false)
    expect(canMoveTopLevelBlock(e, posOf(e, 'c'), -1)).toBe(true)
  })
})

// ---- 删除 ----

describe('deleteTopLevelBlock', () => {
  it('删除一个顶层块', () => {
    const e = mount(threeBlocks())
    expect(deleteTopLevelBlock(e, posOf(e, 'b'))).toBe(true)
    expect(order(e)).toEqual(['a', 'c'])
  })

  it('解析模块不许删（它是界面开关管的）', () => {
    const e = mount([question('q', 0, 1), node('m', 'question_details', 1)])
    const before = order(e)
    expect(deleteTopLevelBlock(e, posOf(e, 'm'))).toBe(false)
    expect(order(e)).toEqual(before)
  })

  it('删掉题目块时不碰同一层的其它块', () => {
    const e = mount([question('a', 0, 1), question('b', 1, 2), question('c', 2, 3)])
    deleteTopLevelBlock(e, posOf(e, 'b'))
    expect(order(e)).toEqual(['a', 'c'])
    expect(countPaperBlocks(e)).toBe(2)
    // 中间那块的内容确实没了，而不是只剩个空壳。
    expect(e.state.doc.textContent).not.toContain('b')
  })

  it('删到只剩一个块仍可继续', () => {
    const e = mount([text('a', 0, '甲'), text('b', 1, '乙')])
    deleteTopLevelBlock(e, posOf(e, 'a'))
    expect(order(e)).toEqual(['b'])
    deleteTopLevelBlock(e, posOf(e, 'b'))
    // 试卷块清空了，但编辑器里还会留一个空段落给光标落脚。
    expect(order(e)).toEqual([])
    expect(countPaperBlocks(e)).toBe(0)
  })

  it('删空之后补出来的脚手架段落没有 nodeId，回存时会被忽略', () => {
    const e = mount([text('a', 0, '甲')])
    deleteTopLevelBlock(e, posOf(e, 'a'))
    const conversion = docToNodes(e.getJSON())
    expect(conversion.nodes).toEqual([])
    // 脚手架不该被报成"丢了节点"。
    expect(conversion.dropped).toEqual([])
  })

  it('位置非法时返回 false 而不是抛错', () => {
    const e = mount(threeBlocks())
    expect(deleteTopLevelBlock(e, -5)).toBe(false)
  })
})
