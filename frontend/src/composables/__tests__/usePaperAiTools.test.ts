/**
 * 域逻辑测试：AI 客户端工具的纯函数。
 *
 * 这三个函数是组卷画布迁移时**不能动**的地基：
 * applyPaperOperations / diffPaperNodes / renderPaperOutline 全部是
 * 作用于 PaperNode[] 的纯函数，与渲染层解耦。画布从"卡片列表"换成
 * "Tiptap 文档"之后，它们仍要给出完全相同的结果——这正是本文件的作用。
 */
import { beforeEach, describe, expect, it } from 'vitest'
import {
  applyPaperOperations,
  diffPaperNodes,
  renderPaperOutline,
  type PreparedPaperOp,
} from '../usePaperAiTools'
import { plainToRichDoc } from '@/lib/richdoc'
import type { PaperNode, PaperQuestionProjection, QuestionRef } from '@/api/paper'

// ---- 夹具 ----

let seq = 0
function node(partial: Partial<PaperNode> & { id: string; node_type: PaperNode['node_type'] }): PaperNode {
  return {
    parent_id: null,
    position: seq++,
    node_kind: partial.node_type === 'answer_item' ? 'reference' : 'block',
    content: null,
    props: {},
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
    ...partial,
  } as PaperNode
}

function question(id: string, qid: number, pos: number, props: Record<string, unknown> = {}): PaperNode {
  return node({
    id,
    node_type: 'question',
    position: pos,
    content: { question_id: qid, content_revision: 1 } satisfies QuestionRef,
    props,
  })
}

function projections(entries: Record<number, string>): Record<number, PaperQuestionProjection> {
  const out: Record<number, PaperQuestionProjection> = {}
  for (const [id, text] of Object.entries(entries)) {
    out[Number(id)] = { id: Number(id), stem_text: text } as PaperQuestionProjection
  }
  return out
}

function ids(list: PaperNode[]): string[] {
  return list.map((n) => n.id)
}

function positions(list: PaperNode[]): number[] {
  return list.map((n) => n.position)
}

const OPTS = { scoringEnabled: false, dirty: false, revision: 1 }

beforeEach(() => {
  seq = 0
})

// ---- applyPaperOperations ----

describe('applyPaperOperations · 插入', () => {
  it('insert_text 建出 rich_text 块，content 走富文本', () => {
    const { nodes, results } = applyPaperOperations([], [
      { op: 'insert_text', content: plainToRichDoc('说明一句') },
    ])
    expect(results).toEqual([{ op: 'insert_text', ok: true, message: '已插入说明文字' }])
    expect(nodes).toHaveLength(1)
    expect(nodes[0].node_type).toBe('rich_text')
    expect(nodes[0].node_kind).toBe('block')
    expect(nodes[0].parent_id).toBeNull()
    expect(nodes[0].position).toBe(0)
  })

  it('insert_heading 把 level 夹到 1..6 并落在 props 上', () => {
    const { nodes } = applyPaperOperations([], [{ op: 'insert_heading', level: 99 }])
    expect(nodes[0].node_type).toBe('heading')
    expect(nodes[0].props).toEqual({ level: 6 })
  })

  it('level 缺失或非有限数时兜底为 1', () => {
    expect(applyPaperOperations([], [{ op: 'insert_heading' }]).nodes[0].props).toEqual({ level: 1 })
    expect(
      applyPaperOperations([], [{ op: 'insert_heading', level: Number.NaN }]).nodes[0].props,
    ).toEqual({ level: 1 })
  })

  it('insert_page_break 的 content 为 null', () => {
    const { nodes } = applyPaperOperations([], [{ op: 'insert_page_break' }])
    expect(nodes[0].node_type).toBe('page_break')
    expect(nodes[0].content).toBeNull()
  })

  it('before_id 指定锚点时插到它前面', () => {
    const base = [question('a', 1, 0), question('b', 2, 1)]
    const { nodes } = applyPaperOperations(base, [{ op: 'insert_text', before_id: 'b' }])
    expect(ids(nodes)).toEqual(['a', expect.any(String) as unknown as string, 'b'])
    expect(nodes[1].node_type).toBe('rich_text')
  })

  it('锚点不存在时不插入，并回收已建好的节点', () => {
    const base = [question('a', 1, 0)]
    const { nodes, results } = applyPaperOperations(base, [
      { op: 'insert_text', before_id: '不存在' },
    ])
    expect(results[0].ok).toBe(false)
    expect(results[0].message).toContain('找不到节点')
    expect(ids(nodes)).toEqual(['a'])
  })

  it('解析模块永远排在最后，"插到末尾"落在它前面', () => {
    // 卷末解析模块插在题目中间会打断题序，因此末尾 = 模块之前。
    const base = [question('a', 1, 0), node({ id: 'm', node_type: 'question_details', position: 1 })]
    const { nodes } = applyPaperOperations(base, [{ op: 'insert_text' }])
    expect(ids(nodes)).toEqual(['a', expect.any(String) as unknown as string, 'm'])
  })

  it('生成的节点 id 不与既有 id 撞车', () => {
    const base = Array.from({ length: 20 }, (_, i) => question(`q${i}`, i + 1, i))
    const { nodes } = applyPaperOperations(base, [
      { op: 'insert_text' },
      { op: 'insert_text' },
      { op: 'insert_text' },
    ])
    const all = ids(nodes)
    expect(new Set(all).size).toBe(all.length)
  })
})

describe('applyPaperOperations · 删除', () => {
  it('删带子节点的块会连子节点一起清掉，不留孤儿', () => {
    // 用非解析模块的父节点：解析模块本身是界面开关管的，AI 删不掉（见下一条）。
    // 这里覆盖的是"父没了子要跟着走"这条防御性清理路径。
    const parent = node({ id: 'p', node_type: 'rich_text', position: 1 })
    const child = node({ id: 'c1', node_type: 'answer_item', parent_id: 'p', position: 0 })
    const base = [question('a', 1, 0), parent, child]
    const { nodes } = applyPaperOperations(base, [{ op: 'remove_node', node_id: 'p' }])
    expect(ids(nodes)).toEqual(['a'])
  })

  it('删掉别块时，存活块的子节点必须留下', () => {
    const base = [
      question('a', 1, 0),
      question('b', 2, 1),
      node({ id: 'm', node_type: 'question_details', position: 2 }),
      node({ id: 'c1', node_type: 'answer_item', parent_id: 'm', position: 0 }),
    ]
    const { nodes } = applyPaperOperations(base, [{ op: 'remove_node', node_id: 'a' }])
    expect(ids(nodes)).toEqual(['b', 'm', 'c1'])
  })

  it('解析模块本身不许 AI 删', () => {
    const base = [question('a', 1, 0), node({ id: 'm', node_type: 'question_details', position: 1 })]
    const { nodes, results } = applyPaperOperations(base, [{ op: 'remove_node', node_id: 'm' }])
    expect(results[0].ok).toBe(false)
    expect(results[0].message).toContain('界面上用开关')
    expect(ids(nodes)).toEqual(['a', 'm'])
  })

  it('节点不存在时如实报错', () => {
    const { results } = applyPaperOperations([question('a', 1, 0)], [
      { op: 'remove_node', node_id: '幽灵' },
    ])
    expect(results[0].ok).toBe(false)
  })
})

describe('applyPaperOperations · 移动', () => {
  it('向前移：摘掉自己后下标前移一位，落点要补偿', () => {
    // 这是最容易写错的一处：不补偿就会整体后移一格。
    const base = [question('a', 1, 0), question('b', 2, 1), question('c', 3, 2)]
    const { nodes, results } = applyPaperOperations(base, [
      { op: 'move_node', node_id: 'c', position: 0 },
    ])
    expect(ids(nodes)).toEqual(['c', 'a', 'b'])
    expect(results[0].message).toContain('第 1 位')
  })

  it('向后移不受摘除影响', () => {
    const base = [question('a', 1, 0), question('b', 2, 1), question('c', 3, 2)]
    const { nodes } = applyPaperOperations(base, [{ op: 'move_node', node_id: 'a', position: 2 }])
    expect(ids(nodes)).toEqual(['b', 'c', 'a'])
  })

  it('可以移到末位', () => {
    // 回归：position 曾经被当成"插入前的下标"补偿一格，
    // 结果夹到末位时前后同下标，直接退化成空操作。
    const base = [question('a', 1, 0), question('b', 2, 1), question('c', 3, 2)]
    const { nodes, results } = applyPaperOperations(base, [
      { op: 'move_node', node_id: 'a', position: 2 },
    ])
    expect(ids(nodes)).toEqual(['b', 'c', 'a'])
    expect(results[0].message).toContain('第 3 位')
  })

  it('before_id 与 position 是两套坐标系，结果本就该不同', () => {
    // before_id=c → "放到 c 之前" → a 在 c 前面：[b, a, c]
    // position=2  → "最终落在第 3 位" → a 在 c 后面：[b, c, a]
    // 两者都正确，但落点不同。把它写成一条不变量可以防止有人"顺手统一"
    // 成一种，而没发现另一种跟着坏了。
    const mk = () => [question('a', 1, 0), question('b', 2, 1), question('c', 3, 2)]
    expect(ids(applyPaperOperations(mk(), [{ op: 'move_node', node_id: 'a', before_id: 'c' }]).nodes)).toEqual([
      'b',
      'a',
      'c',
    ])
    expect(ids(applyPaperOperations(mk(), [{ op: 'move_node', node_id: 'a', position: 2 }]).nodes)).toEqual([
      'b',
      'c',
      'a',
    ])
  })

  it('不变量：position 为 N 时，节点最终一定在第 N 位', () => {
    // 穷举 3×3 的移动组合。这条是 move_node 的核心契约，
    // 也是之前"向后移永远差一位 / 移不到末位"那个 bug 的照妖镜。
    const ids0 = ['a', 'b', 'c']
    for (const id of ids0) {
      for (let to = 0; to < 3; to++) {
        const base = ids0.map((nid, i) => question(nid, i + 1, i))
        const { nodes } = applyPaperOperations(base, [{ op: 'move_node', node_id: id, position: to }])
        expect(ids(nodes), `${id} → position ${to}`).toHaveLength(3)
        expect(ids(nodes)[to], `${id} → position ${to}`).toBe(id)
        // 顺序被彻底打乱时，每个 id 仍然各出现一次。
        expect(new Set(ids(nodes)).size).toBe(3)
      }
    }
  })

  it('不变量：before_id=X 时，节点最终一定排在 X 前面', () => {
    const ids0 = ['a', 'b', 'c']
    for (const id of ids0) {
      for (const anchor of ids0) {
        if (anchor === id) continue
        const base = ids0.map((nid, i) => question(nid, i + 1, i))
        const { nodes } = applyPaperOperations(base, [
          { op: 'move_node', node_id: id, before_id: anchor },
        ])
        expect(ids(nodes).indexOf(id), `${id} → before ${anchor}`).toBeLessThan(
          ids(nodes).indexOf(anchor),
        )
      }
    }
  })

  it('before_id 形式按锚点定位', () => {
    const base = [question('a', 1, 0), question('b', 2, 1), question('c', 3, 2)]
    const { nodes } = applyPaperOperations(base, [{ op: 'move_node', node_id: 'a', before_id: 'c' }])
    expect(ids(nodes)).toEqual(['b', 'a', 'c'])
  })

  it('移到原位是幂等的', () => {
    const base = [question('a', 1, 0), question('b', 2, 1)]
    const { nodes, results } = applyPaperOperations(base, [
      { op: 'move_node', node_id: 'a', position: 0 },
    ])
    expect(results[0]).toEqual({ op: 'move_node', ok: true, message: '已在目标位置' })
    expect(ids(nodes)).toEqual(['a', 'b'])
  })

  it('position 越界被夹住而不是报错', () => {
    const base = [question('a', 1, 0), question('b', 2, 1)]
    expect(ids(applyPaperOperations(base, [{ op: 'move_node', node_id: 'a', position: 99 }]).nodes)).toEqual([
      'b',
      'a',
    ])
    expect(ids(applyPaperOperations(base, [{ op: 'move_node', node_id: 'b', position: -5 }]).nodes)).toEqual([
      'b',
      'a',
    ])
  })

  it('锚点不存在时报错且不改变顺序', () => {
    const base = [question('a', 1, 0), question('b', 2, 1)]
    const { nodes, results } = applyPaperOperations(base, [
      { op: 'move_node', node_id: 'a', before_id: '幽灵' },
    ])
    expect(results[0].ok).toBe(false)
    expect(ids(nodes)).toEqual(['a', 'b'])
  })
})

describe('applyPaperOperations · 不变量', () => {
  it('不就地修改传入的数组', () => {
    // 用户点"放弃"时只需丢掉候选，不该去回滚真数据。
    const base = [question('a', 1, 0), question('b', 2, 1)]
    const snapshot = JSON.parse(JSON.stringify(base))
    applyPaperOperations(base, [
      { op: 'move_node', node_id: 'a', position: 1 },
      { op: 'insert_text' },
      { op: 'remove_node', node_id: 'b' },
    ])
    expect(base).toEqual(snapshot)
  })

  it('position 重排成连续的 0..n-1', () => {
    const base = [question('a', 1, 5), question('b', 2, 9), question('c', 3, 40)]
    const { nodes } = applyPaperOperations(base, [
      { op: 'move_node', node_id: 'c', position: 0 },
      { op: 'insert_text' },
    ])
    expect(positions(nodes)).toEqual([0, 1, 2, 3])
  })

  it('按 position 而非数组顺序排列', () => {
    const base = [question('a', 1, 2), question('b', 2, 0), question('c', 3, 1)]
    const { nodes } = applyPaperOperations(base, [])
    expect(ids(nodes)).toEqual(['b', 'c', 'a'])
  })

  it('空输入空输出', () => {
    const { nodes, results } = applyPaperOperations([], [])
    expect(nodes).toEqual([])
    expect(results).toEqual([])
  })

  it('多个操作按给定顺序串行生效', () => {
    const base = [question('a', 1, 0), question('b', 2, 1)]
    const ops: PreparedPaperOp[] = [
      { op: 'insert_text', before_id: 'a' },
      { op: 'move_node', node_id: 'b', position: 0 },
    ]
    const { nodes } = applyPaperOperations(base, ops)
    // 第一步得到 [新块, a, b]；第二步把 b 移到最前，于是 [b, 新块, a]。
    // 注意后一步看到的是**前一步的结果**——这正是"串行"要验证的东西。
    expect(nodes).toHaveLength(3)
    expect(nodes[0].id).toBe('b')
    expect(nodes[1].node_type).toBe('rich_text')
    expect(nodes[2].id).toBe('a')
    expect(positions(nodes)).toEqual([0, 1, 2])
  })
})

// ---- diffPaperNodes ----

describe('diffPaperNodes', () => {
  const proj = projections({ 1: '题干一', 2: '题干二' })

  it('空对比没有任何差异', () => {
    const base = [question('a', 1, 0), question('b', 2, 1)]
    expect(diffPaperNodes(base, base, proj)).toEqual([])
  })

  it('识别新增与删除', () => {
    const before = [question('a', 1, 0)]
    const after = [question('a', 1, 0), question('b', 2, 1)]
    const changes = diffPaperNodes(before, after, proj)
    expect(changes).toEqual([{ kind: 'added', nodeId: 'b', label: '题目：题干二' }])
  })

  it('移动会把位置写进 label', () => {
    const before = [question('a', 1, 0), question('b', 2, 1)]
    const after = [question('b', 2, 0), question('a', 1, 1)]
    const changes = diffPaperNodes(before, after, proj)
    expect(changes).toEqual([
      { kind: 'moved', nodeId: 'b', label: '题目：题干二：第 2 位 → 第 1 位' },
      { kind: 'moved', nodeId: 'a', label: '题目：题干一：第 1 位 → 第 2 位' },
    ])
  })

  it('位置与内容同时变时只报 moved，不重复报 modified', () => {
    const before = [question('a', 1, 0, { score: 1 }), question('b', 2, 1)]
    const after = [question('b', 2, 0), question('a', 1, 1, { score: 5 })]
    const changes = diffPaperNodes(before, after, proj)
    expect(changes.filter((c) => c.nodeId === 'a')).toHaveLength(1)
    expect(changes.find((c) => c.nodeId === 'a')?.kind).toBe('moved')
  })

  it('内容变化但位置不变才报 modified', () => {
    const before = [question('a', 1, 0, { score: 1 })]
    const after = [question('a', 1, 0, { score: 5 })]
    expect(diffPaperNodes(before, after, proj)).toEqual([
      { kind: 'modified', nodeId: 'a', label: '题目：题干一' },
    ])
  })

  it('子节点（解析条目）不参与差异', () => {
    const before = [
      question('a', 1, 0),
      node({ id: 'c1', node_type: 'answer_item', parent_id: 'a', position: 0 }),
    ]
    const after = [question('a', 1, 0)]
    expect(diffPaperNodes(before, after, proj)).toEqual([])
  })

  it('投影缺失时退回按 id 标注，不崩', () => {
    const before = [question('a', 1, 0)]
    const after = [question('a', 1, 0), question('z', 99, 1)]
    expect(diffPaperNodes(before, after, {})).toEqual([
      { kind: 'added', nodeId: 'z', label: '题目 #99' },
    ])
  })
})

// ---- renderPaperOutline ----

describe('renderPaperOutline', () => {
  it('空卷给出可用的说明而不是空串', () => {
    const out = renderPaperOutline('期中', [], {}, OPTS)
    expect(out).toContain('共 0 题')
    expect(out).toContain('空试卷')
  })

  it('每行都带 node_id，供模型回填 node_id / before_id', () => {
    const base = [question('a', 1, 0), question('b', 2, 1)]
    const out = renderPaperOutline('期中', base, projections({ 1: '甲', 2: '乙' }), OPTS)
    expect(out).toContain('node_id=a')
    expect(out).toContain('node_id=b')
  })

  it('显式题号优先于顺序号', () => {
    const base = [question('a', 1, 0, { number: '1.1' }), question('b', 2, 1)]
    const out = renderPaperOutline('期中', base, projections({ 1: '甲', 2: '乙' }), OPTS)
    expect(out).toContain('(第 1.1 题)')
    expect(out).toContain('(第 2 题)')
  })

  it('分值合计只在开启分值时出现', () => {
    const base = [question('a', 1, 0, { score: 2.5 }), question('b', 2, 1, { score: 2.5 })]
    const off = renderPaperOutline('期中', base, projections({ 1: '甲', 2: '乙' }), {
      ...OPTS,
      scoringEnabled: false,
    })
    const on = renderPaperOutline('期中', base, projections({ 1: '甲', 2: '乙' }), {
      ...OPTS,
      scoringEnabled: true,
    })
    expect(off).not.toContain('合计')
    expect(on).toContain('合计 5')
  })

  it('未保存的改动要如实告诉模型', () => {
    const base = [question('a', 1, 0)]
    expect(renderPaperOutline('期中', base, projections({ 1: '甲' }), OPTS)).not.toContain('未保存')
    expect(
      renderPaperOutline('期中', base, projections({ 1: '甲' }), { ...OPTS, dirty: true }),
    ).toContain('含未保存改动')
  })

  it('非题目节点也进大纲，并按类型命名', () => {
    const base = [
      node({ id: 'h', node_type: 'heading', position: 0, content: plainToRichDoc('一、选择题'), props: { level: 2 } }),
      question('a', 1, 1),
      node({ id: 'pb', node_type: 'page_break', position: 2 }),
      node({ id: 'as', node_type: 'answer_space', position: 3 }),
      node({ id: 'm', node_type: 'question_details', position: 4 }),
    ]
    const out = renderPaperOutline('期中', base, projections({ 1: '甲' }), OPTS)
    expect(out).toContain('标题 H2：一、选择题')
    expect(out).toContain('分页符')
    expect(out).toContain('答题区')
    expect(out).toContain('解析模块')
  })

  it('空标题与空说明如实标注，不编内容', () => {
    const base = [
      node({ id: 'h', node_type: 'heading', position: 0, content: plainToRichDoc('') }),
      node({ id: 't', node_type: 'rich_text', position: 1, content: plainToRichDoc('') }),
    ]
    const out = renderPaperOutline('期中', base, {}, OPTS)
    expect(out).toContain('空标题')
    expect(out).toContain('空说明文字')
  })

  it('标题的 level 缺失时按 H1 标注', () => {
    const base = [node({ id: 'h', node_type: 'heading', position: 0, content: plainToRichDoc('大题') })]
    expect(renderPaperOutline('期中', base, {}, OPTS)).toContain('标题 H1：大题')
  })
})
