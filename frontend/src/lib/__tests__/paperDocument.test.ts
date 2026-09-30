/**
 * 域逻辑测试：画布文档模型。
 *
 * 这一层的价值全在**往返无损**上。画布改一次排版就是
 * 「PaperNode[] → 文档 → 用户拖动 → 文档 → PaperNode[]」，
 * 中间任何一环把字段丢了，用户按一下保存就永久丢内容。
 * 所以测试的重心不是「某个函数返回了什么」，而是
 * 「喂进去一棵树，原样吐回来还是不是同一棵树」。
 */
import { describe, expect, it } from 'vitest'
import {
  ANSWER_MAX_LINES,
  CANVAS_NODE,
  canvasToNode,
  canvasNameOf,
  docToNodes,
  isDerived,
  kindOf,
  nodeToCanvas,
  nodesToDoc,
  saveableNodes,
  topLevelOf,
  type ModuleScopeValue,
} from '../paperDocument'
import { plainToRichDoc } from '../richdoc'
import type { NodeType, PaperNode } from '@/api/paper'

// ---- 夹具 ----

function node(
  id: string,
  nodeType: NodeType,
  position: number,
  extra: Partial<PaperNode> = {},
): PaperNode {
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

function heading(id: string, pos: number, body: string, level: number): PaperNode {
  return node(id, 'heading', pos, { content: plainToRichDoc(body), props: { level } })
}

function question(id: string, pos: number, qid: number, props: Record<string, unknown> = {}): PaperNode {
  return node(id, 'question', pos, {
    content: { question_id: qid, content_revision: 3 },
    props,
  })
}

function answerItem(
  id: string,
  parentID: string | null,
  pos: number,
  source: string,
  anchor: string | null,
): PaperNode {
  return node(id, 'answer_item', pos, {
    parent_id: parentID,
    source_question_node_id: source,
    anchor_before_node_id: anchor,
  })
}

function byID(list: PaperNode[]): Map<string, PaperNode> {
  return new Map(list.map((n) => [n.id, n]))
}

/**
 * PaperNode 的 props/content 在 API 类型上是 unknown（服务端说什么形状就什么形状），
 * 断言具体字段前先收窄。刻意不为省事去改 API 类型——那会把
 * "这是服务端契约" 这件事藏起来。
 */
function propsOf(target: PaperNode): Record<string, unknown> {
  return (target.props ?? {}) as Record<string, unknown>
}

// ---- 往返 ----

describe('往返', () => {
  it('空树往返仍是空树', () => {
    const doc = nodesToDoc([])
    expect(doc.type).toBe('doc')
    // ProseMirror 的 doc 要求 block+，空文档非法；补一个不带 nodeId 的空段落，
    // 但不能让它变成一颗幽灵节点回到保存请求里。
    expect(doc.content).toEqual([{ type: 'paragraph' }])
    expect(docToNodes(doc)).toEqual({ nodes: [], dropped: [] })
  })

  it('单块往返后逐字段相等', () => {
    const original = text('n1', 0, '这是一段说明，里面有 $x^2$ 公式')
    const { nodes } = docToNodes(nodesToDoc([original]))
    expect(nodes).toEqual([original])
  })

  it('全类型混合的树往返无损', () => {
    const original: PaperNode[] = [
      heading('h', 0, '一、选择题', 2),
      question('q1', 1, 11, { score: 5, number: '1.1' }),
      text('t', 2, '注意：下列说法正确的是'),
      question('q2', 3, 12),
      node('pb', 'page_break', 4),
      node('as', 'answer_space', 5, { props: { lines: 6, style: 'lined' } }),
      node('m', 'question_details', 6, { props: { scope: 'before', title: '答案与解析' } }),
      answerItem('a1', 'm', 0, 'q1', 'q2'),
      answerItem('a2', 'm', 1, 'q2', null),
    ]
    const { nodes, dropped } = docToNodes(nodesToDoc(original))
    expect(dropped).toEqual([])
    expect(nodes).toEqual(original)
  })

  it('position 乱序时按 position 排，不按数组顺序', () => {
    const original = [text('c', 2, '第三'), text('a', 0, '第一'), text('b', 1, '第二')]
    const doc = nodesToDoc(original)
    expect((doc.content ?? []).map((n) => n.attrs?.nodeId)).toEqual(['a', 'b', 'c'])
    const { nodes } = docToNodes(doc)
    expect(nodes.map((n) => n.id)).toEqual(['a', 'b', 'c'])
    expect(nodes.map((n) => n.position)).toEqual([0, 1, 2])
  })

  it('往返两次结果稳定（幂等）', () => {
    const original = [heading('h', 0, '大题', 1), question('q', 1, 5, { score: 2 })]
    const once = docToNodes(nodesToDoc(original)).nodes
    const twice = docToNodes(nodesToDoc(once)).nodes
    expect(twice).toEqual(once)
  })

  it('schema_version 原样往返', () => {
    const original = [text('n1', 0, 'x'), { ...text('n2', 1, 'y'), schema_version: 7 }]
    const { nodes } = docToNodes(nodesToDoc(original))
    expect(nodes.map((n) => n.schema_version)).toEqual([1, 7])
  })
})

// ---- 节点类型 ----

describe('节点类型映射', () => {
  const cases: [NodeType, string][] = [
    ['rich_text', CANVAS_NODE.richText],
    ['heading', CANVAS_NODE.heading],
    ['question', CANVAS_NODE.question],
    ['page_break', CANVAS_NODE.pageBreak],
    ['answer_space', CANVAS_NODE.answerSpace],
    ['question_details', CANVAS_NODE.questionDetails],
    ['answer_item', CANVAS_NODE.answerItem],
  ]

  for (const [type, canvas] of cases) {
    it(`${type} ⇄ ${canvas} 往返一致`, () => {
      expect(canvasNameOf(type)).toBe(canvas)
      const original = node('n', type, 0, {
        content: type === 'rich_text' || type === 'heading' ? plainToRichDoc('x') : null,
      })
      const drawn = nodeToCanvas(original)
      expect(drawn?.type).toBe(canvas)
      expect(canvasToNode(drawn!, null, 0)?.node_type).toBe(type)
    })
  }

  it('画布节点名都带 paper 前缀，避免与 StarterKit 撞名', () => {
    for (const name of Object.values(CANVAS_NODE)) {
      expect(name.startsWith('paper')).toBe(true)
    }
  })

  it('节点层级与后端 nodeKindOf 一致', () => {
    expect(kindOf('rich_text')).toBe('block')
    expect(kindOf('heading')).toBe('block')
    expect(kindOf('question')).toBe('block')
    expect(kindOf('page_break')).toBe('block')
    expect(kindOf('answer_space')).toBe('block')
    expect(kindOf('question_details')).toBe('module')
    expect(kindOf('answer_item')).toBe('reference')
  })

  it('只有 answer_item 是服务端派生的', () => {
    expect(isDerived('answer_item')).toBe(true)
    expect(isDerived('question')).toBe(false)
    expect(isDerived('question_details')).toBe(false)
  })
})

// ---- 题目块 ----

describe('题目块', () => {
  it('question_id 与 content_revision 落在 content', () => {
    const { nodes } = docToNodes(nodesToDoc([question('q', 0, 42)]))
    expect(nodes[0].content).toEqual({ question_id: 42, content_revision: 3 })
  })

  it('分值与题号落在 props —— 与服务端 sumScore、导出器读的位置一致', () => {
    const { nodes } = docToNodes(nodesToDoc([question('q', 0, 42, { score: 4.5, number: '1.1' })]))
    expect(nodes[0].props).toEqual({ score: 4.5, number: '1.1' })
    // 反过来也不能跑偏：绝不能把分值塞进 content。
    expect((nodes[0].content as Record<string, unknown>).score).toBeUndefined()
  })

  it('没有分值/题号时不写空键', () => {
    const { nodes } = docToNodes(nodesToDoc([question('q', 0, 42)]))
    expect(nodes[0].props).toEqual({})
  })

  it('分值为 0 是合法值，必须保留', () => {
    const { nodes } = docToNodes(nodesToDoc([question('q', 0, 42, { score: 0 })]))
    expect(nodes[0].props).toEqual({ score: 0 })
  })

  it('空题号视为未指定', () => {
    const { nodes } = docToNodes(nodesToDoc([question('q', 0, 42, { number: '   ' })]))
    expect(nodes[0].props).toEqual({})
  })

  it('题号是字符串，1.1 这种十进制写法能原样往返', () => {
    const { nodes } = docToNodes(nodesToDoc([question('q', 0, 42, { number: '1.1' })]))
    expect(typeof propsOf(nodes[0]).number).toBe('string')
    expect(propsOf(nodes[0]).number).toBe('1.1')
  })
})

// ---- 标题 ----

describe('标题', () => {
  it('层级原样往返', () => {
    const { nodes } = docToNodes(nodesToDoc([heading('h', 0, '大题', 3)]))
    expect(nodes[0].props).toEqual({ level: 3 })
  })

  it('越界层级被夹住而不是丢弃', () => {
    expect(docToNodes(nodesToDoc([heading('h', 0, 'x', 99)])).nodes[0].props).toEqual({ level: 6 })
    expect(docToNodes(nodesToDoc([heading('h', 0, 'x', -4)])).nodes[0].props).toEqual({ level: 1 })
  })

  it('标题内容保留公式', () => {
    const original = heading('h', 0, '关于 $f(x)$ 的第 $k$ 问', 1)
    const { nodes } = docToNodes(nodesToDoc([original]))
    expect(nodes[0].content).toEqual(original.content)
  })
})

// ---- 答题区 ----

describe('答题区', () => {
  it('行数与样式原样往返', () => {
    const original = node('as', 'answer_space', 0, { props: { lines: 8, style: 'lined' } })
    const { nodes } = docToNodes(nodesToDoc([original]))
    expect(nodes[0].props).toEqual({ lines: 8, style: 'lined' })
  })

  it('未知样式落回 blank', () => {
    const { nodes } = docToNodes(nodesToDoc([node('as', 'answer_space', 0, { props: { style: '花里胡哨' } })]))
    expect(propsOf(nodes[0]).style).toBe('blank')
  })

  it('老数据的 content.height 换算成行数', () => {
    // 老卷子把留白高度写在 content.height 上（默认 120px），
    // 120/30 = 4 行，与导出端硬编码的 4 个空段落吻合。
    const legacy = node('as', 'answer_space', 0, { content: { height: 120 } })
    expect(propsOf(docToNodes(nodesToDoc([legacy])).nodes[0])).toEqual({ lines: 4, style: 'blank' })
  })

  it('props.lines 优先于老字段', () => {
    const mixed = node('as', 'answer_space', 0, { content: { height: 120 }, props: { lines: 9 } })
    expect(propsOf(docToNodes(nodesToDoc([mixed])).nodes[0]).lines).toBe(9)
  })

  it('行数被夹在 1..50，滑不出一整页空白', () => {
    const tooMany = node('as', 'answer_space', 0, { props: { lines: 9999 } })
    expect(propsOf(docToNodes(nodesToDoc([tooMany])).nodes[0]).lines).toBe(ANSWER_MAX_LINES)
    const tooFew = node('as', 'answer_space', 0, { props: { lines: 0 } })
    expect(propsOf(docToNodes(nodesToDoc([tooFew])).nodes[0]).lines).toBe(1)
  })
})

// ---- 解析模块 ----

describe('解析模块', () => {
  it('条目嵌在模块的 content 里，不是平级兄弟', () => {
    const nodes: PaperNode[] = [
      question('q1', 0, 1),
      node('m', 'question_details', 1, { props: { scope: 'all' } }),
      answerItem('a1', 'm', 0, 'q1', null),
    ]
    const doc = nodesToDoc(nodes)
    const module = (doc.content ?? []).find((n) => n.type === CANVAS_NODE.questionDetails)
    expect(module).toBeDefined()
    expect((module?.content ?? []).map((n) => n.attrs?.nodeId)).toEqual(['a1'])
  })

  it('条目往返后 parent_id 与软指针都对得上', () => {
    const original: PaperNode[] = [
      question('q1', 0, 1),
      question('q2', 1, 2),
      node('m', 'question_details', 2, { props: { scope: 'before' } }),
      answerItem('a1', 'm', 0, 'q1', 'q2'),
      answerItem('a2', 'm', 1, 'q2', null),
    ]
    const { nodes } = docToNodes(nodesToDoc(original))
    const got = byID(nodes)
    expect(got.get('a1')?.parent_id).toBe('m')
    expect(got.get('a1')?.source_question_node_id).toBe('q1')
    expect(got.get('a1')?.anchor_before_node_id).toBe('q2')
    expect(got.get('a2')?.anchor_before_node_id).toBeNull()
    expect(nodes).toEqual(original)
  })

  it('条目按 position 排序', () => {
    const nodes: PaperNode[] = [
      node('m', 'question_details', 0),
      answerItem('a2', 'm', 1, 'q2', null),
      answerItem('a1', 'm', 0, 'q1', null),
    ]
    const doc = nodesToDoc(nodes)
    const module = (doc.content ?? []).find((n) => n.type === CANVAS_NODE.questionDetails)
    expect((module?.content ?? []).map((n) => n.attrs?.nodeId)).toEqual(['a1', 'a2'])
  })

  it('scope 与 title 往返', () => {
    const original = node('m', 'question_details', 0, { props: { scope: 'before', title: '参考答案' } })
    const { nodes } = docToNodes(nodesToDoc([original]))
    expect(nodes[0].props).toEqual({ scope: 'before', title: '参考答案' })
  })

  it('非法 scope 落回 all', () => {
    const bad = node('m', 'question_details', 0, { props: { scope: '瞎写' as ModuleScopeValue } })
    expect(propsOf(docToNodes(nodesToDoc([bad])).nodes[0]).scope).toBe('all')
  })

  it('空模块往返后仍然存在', () => {
    const original = node('m', 'question_details', 0)
    const { nodes } = docToNodes(nodesToDoc([original]))
    expect(nodes).toHaveLength(1)
    expect(nodes[0].id).toBe('m')
    expect(nodes[0].node_kind).toBe('module')
  })

  it('props 为空的模块会被补上 scope: all', () => {
    // 这是**归一化**而不是信息丢失：后端 domain/paper.go:230 本来就把空 scope
    // 归一为 all（`mc.Scope = ModuleScopeAll`），画布只是把这件事提前做掉，
    // 让画布上的模块始终有确定的值可渲染。
    const bare = node('m', 'question_details', 0)
    expect(docToNodes(nodesToDoc([bare])).nodes[0].props).toEqual({ scope: 'all' })
    // 归一化只发生一次：再来一轮不会继续长字段。
    const once = docToNodes(nodesToDoc([bare])).nodes
    expect(docToNodes(nodesToDoc(once)).nodes[0].props).toEqual({ scope: 'all' })
  })
})

// ---- 降级与防御 ----

describe('降级与防御', () => {
  it('不认识的画布节点被丢掉并被记录', () => {
    const { nodes, dropped } = docToNodes({
      type: 'doc',
      content: [
        { type: 'paperRichText', attrs: { nodeId: 'a' }, content: [{ type: 'paragraph' }] },
        { type: 'paperFromTheFuture', attrs: { nodeId: 'b' } },
      ],
    })
    expect(nodes.map((n) => n.id)).toEqual(['a'])
    expect(dropped).toEqual([{ id: 'b', type: 'paperFromTheFuture' }])
  })

  it('编辑器补出来的空段落被静静忽略，不算"丢节点"', () => {
    // 空试卷会被补一个空段落（见 nodesToDoc）。它没有任何试卷含义，
    // 如果它出现在 dropped 里，调用方会以为排版丢了东西。
    const { nodes, dropped } = docToNodes({
      type: 'doc',
      content: [{ type: 'paragraph' }, { type: 'paperQuestion', attrs: { nodeId: 'q', questionId: 1 } }],
    })
    expect(nodes.map((n) => n.id)).toEqual(['q'])
    expect(dropped).toEqual([])
  })

  it('画布节点缺 nodeId 时被记录（无处安放），不造假 id', () => {
    const { nodes, dropped } = docToNodes({ type: 'doc', content: [{ type: 'paperPageBreak' }] })
    expect(nodes).toEqual([])
    expect(dropped).toEqual([{ id: '', type: 'paperPageBreak' }])
  })

  it('空 content 的富文本块补一个空段落', () => {
    const { nodes } = docToNodes({
      type: 'doc',
      content: [{ type: CANVAS_NODE.richText, attrs: { nodeId: 'a' } }],
    })
    expect(nodes[0].content).toEqual({ type: 'doc', content: [{ type: 'paragraph' }] })
  })

  it('content 缺 content 数组时给出最小合法文档', () => {
    const { nodes } = docToNodes({
      type: 'doc',
      content: [{ type: CANVAS_NODE.richText, attrs: { nodeId: 'a' }, content: [] }],
    })
    expect(nodes[0].content).toEqual({ type: 'doc', content: [{ type: 'paragraph' }] })
  })

  it('顶层 node 缺 content 数组时按空文档处理', () => {
    expect(docToNodes({ type: 'doc' })).toEqual({ nodes: [], dropped: [] })
    expect(docToNodes({ type: 'doc', content: [] })).toEqual({ nodes: [], dropped: [] })
  })

  it('nodeToCanvas 对未知类型返回 null 而不是抛错', () => {
    const bad = { ...text('a', 0, 'x'), node_type: 'mystery' as NodeType }
    expect(nodeToCanvas(bad)).toBeNull()
  })
})

// ---- 保存契约 ----

describe('saveableNodes', () => {
  it('剔除服务端派生的条目，保留块与模块', () => {
    const nodes: PaperNode[] = [
      question('q1', 0, 1),
      node('m', 'question_details', 1),
      answerItem('a1', 'm', 0, 'q1', null),
    ]
    expect(saveableNodes(nodes).map((n) => n.id)).toEqual(['q1', 'm'])
  })

  it('没有条目时原样返回', () => {
    const nodes = [question('q1', 0, 1), text('t', 1, 'x')]
    expect(saveableNodes(nodes)).toEqual(nodes)
  })
})

// ---- topLevelOf ----

describe('topLevelOf', () => {
  it('与编辑器、AI 工具用的是同一套判定', () => {
    const nodes: PaperNode[] = [
      question('q', 5, 1),
      answerItem('a', 'm', 0, 'q', null),
      { ...text('t', 0, 'x'), parent_id: 'm' },
    ]
    expect(topLevelOf(nodes).map((n) => n.id)).toEqual(['q'])
  })

  it('平级的 reference 也被排除', () => {
    const orphan = { ...answerItem('a', null, 0, 'q', null) }
    expect(topLevelOf([question('q', 0, 1), orphan])).toHaveLength(1)
  })
})
