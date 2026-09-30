/**
 * 域逻辑测试：组卷的展示计算（题号 / 分值 / 合计）。
 *
 * 这些数字会**出现在卷面上并导出给学生**。算错一位不是"界面小瑕疵"，
 * 是一份发出去的卷子题号错乱。所以这里的用例密度刻意高于平均。
 *
 * 另一层意义：画布迁移后会有两套渲染共用这些函数。把它们先钉死，
 * 新画布才有一个可比对的标准答案。
 */
import { describe, expect, it } from 'vitest'
import {
  answerTextOf,
  blockLabel,
  computeNumbers,
  displayNumbers,
  headingLevelOf,
  numberOf,
  propsWithNumber,
  propsWithScore,
  questionIDOf,
  questionNodesOf,
  roundScore,
  scoreOf,
  totalScoreOf,
} from '../paperView'
import { QUESTION_NUMBER_MAX_LENGTH, type NodeType, type PaperNode } from '@/api/paper'
import { plainToRichDoc } from '../richdoc'

// ---- 夹具 ----

function node(id: string, nodeType: NodeType, position: number, extra: Partial<PaperNode> = {}): PaperNode {
  return {
    id,
    parent_id: null,
    position,
    node_kind: nodeType === 'answer_item' ? 'reference' : nodeType === 'question_details' ? 'module' : 'block',
    node_type: nodeType,
    content: null,
    props: {},
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
    ...extra,
  }
}

function q(id: string, pos: number, props: Record<string, unknown> = {}, qid = 1): PaperNode {
  return node(id, 'question', pos, { content: { question_id: qid, content_revision: 1 }, props })
}

function h(id: string, pos: number, level: number): PaperNode {
  return node(id, 'heading', pos, { content: plainToRichDoc('标题'), props: { level } })
}

const ids = (list: PaperNode[]) => list.map((n) => n.id)
const pairs = (m: Map<string, string>) => [...m.entries()]

// ---- 读取 ----

describe('numberOf / headingLevelOf / scoreOf', () => {
  it('未设题号返回空串', () => {
    expect(numberOf(q('a', 0))).toBe('')
    expect(numberOf(q('a', 0, { number: '   ' }))).toBe('')
    expect(numberOf(q('a', 0, { number: 7 }))).toBe('')
  })

  it('题号两端空白被去掉', () => {
    expect(numberOf(q('a', 0, { number: '  1.1  ' }))).toBe('1.1')
  })

  it('标题层级缺失或非法时按 1 级', () => {
    expect(headingLevelOf(h('h', 0, 1))).toBe(1)
    expect(headingLevelOf(h('h', 0, 3))).toBe(3)
    expect(headingLevelOf(node('h', 'heading', 0, { props: {} }))).toBe(1)
    expect(headingLevelOf(node('h', 'heading', 0, { props: { level: 0 } }))).toBe(1)
    expect(headingLevelOf(node('h', 'heading', 0, { props: { level: -2 } }))).toBe(1)
    expect(headingLevelOf(node('h', 'heading', 0, { props: { level: '3' } }))).toBe(1)
  })

  it('分值 null 与 0 分是两件事', () => {
    expect(scoreOf(q('a', 0))).toBeNull()
    expect(scoreOf(q('a', 0, { score: 0 }))).toBe(0)
    expect(scoreOf(q('a', 0, { score: 4.5 }))).toBe(4.5)
    expect(scoreOf(q('a', 0, { score: '5' }))).toBeNull()
  })

  it('负分按未设处理——负分没有业务含义，多半是输入事故', () => {
    expect(scoreOf(q('a', 0, { score: -1 }))).toBeNull()
    expect(totalScoreOf([q('a', 0, { score: -5 }), q('b', 1, { score: 3 })])).toBe(3)
  })

  it('question_id 拿得到才算数', () => {
    expect(questionIDOf(q('a', 0, {}, 42))).toBe(42)
    expect(questionIDOf(node('a', 'question', 0, { content: null }))).toBeNull()
    expect(questionIDOf(node('a', 'question', 0, { content: { question_id: 0 } }))).toBeNull()
  })
})

// ---- 题目清单 ----

describe('questionNodesOf', () => {
  it('只挑题目块，标题与说明不进清单', () => {
    const list = [h('h', 0, 1), q('a', 1), node('t', 'rich_text', 2), q('b', 3)]
    expect(ids(questionNodesOf(list))).toEqual(['a', 'b'])
  })
})

// ---- computeNumbers ----

describe('computeNumbers · 全局顺序', () => {
  it('按卷面顺序从 1 开始', () => {
    const list = [q('a', 0), q('b', 1), q('c', 2)]
    expect(pairs(computeNumbers(list, 'global'))).toEqual([
      ['a', '1'],
      ['b', '2'],
      ['c', '3'],
    ])
  })

  it('非题目节点不占号', () => {
    const list = [h('h', 0, 1), q('a', 1), node('t', 'rich_text', 2), q('b', 3), node('pb', 'page_break', 4)]
    expect(pairs(computeNumbers(list, 'global'))).toEqual([
      ['a', '1'],
      ['b', '2'],
    ])
  })

  it('空卷得到空映射', () => {
    expect(computeNumbers([], 'global').size).toBe(0)
    expect(computeNumbers([h('h', 0, 2), node('t', 'rich_text', 1)], 'global').size).toBe(0)
  })
})

describe('computeNumbers · 按二级标题分组', () => {
  it('二级标题分界，组内编 1.1、1.2', () => {
    const list = [h('h1', 0, 2), q('a', 1), q('b', 2), h('h2', 3, 2), q('c', 4), q('d', 5)]
    expect(pairs(computeNumbers(list, 'heading'))).toEqual([
      ['a', '1.1'],
      ['b', '1.2'],
      ['c', '2.1'],
      ['d', '2.2'],
    ])
  })

  it('一级标题不分组——卷面顶层通常只有卷名，用它分组等于只有一组', () => {
    const list = [h('h1', 0, 1), q('a', 1), h('h2', 2, 1), q('b', 3)]
    expect(pairs(computeNumbers(list, 'heading'))).toEqual([
      ['a', '1.1'],
      ['b', '1.2'],
    ])
  })

  it('首个二级标题之前的题目归第 1 组', () => {
    const list = [q('a', 0), h('h', 1, 2), q('b', 2)]
    expect(pairs(computeNumbers(list, 'heading'))).toEqual([
      ['a', '1.1'],
      ['b', '2.1'],
    ])
  })

  it('两个标题之间没有题目的空组被跳过，编号不多出一段', () => {
    // 中间那个二级标题下面一道题都没有：它不该占掉第 2 组。
    const list = [h('h1', 0, 2), q('a', 1), h('empty', 2, 2), h('h2', 3, 2), q('b', 4)]
    expect(pairs(computeNumbers(list, 'heading'))).toEqual([
      ['a', '1.1'],
      ['b', '2.1'],
    ])
  })

  it('连续的多个二级标题只开一组', () => {
    const list = [h('h1', 0, 2), h('h2', 1, 2), h('h3', 2, 2), q('a', 3)]
    expect(pairs(computeNumbers(list, 'heading'))).toEqual([['a', '1.1']])
  })

  it('两个模式下同一份卷子的顺序都一致', () => {
    const list = [q('a', 0), q('b', 1), h('h', 2, 2), q('c', 3)]
    const global = [...computeNumbers(list, 'global').keys()]
    const heading = [...computeNumbers(list, 'heading').keys()]
    expect(global).toEqual(heading)
    expect(global).toEqual(['a', 'b', 'c'])
  })
})

// ---- displayNumbers ----

describe('displayNumbers', () => {
  it('手填题号优先，没填的按顺序号兜底', () => {
    const list = [q('a', 0), q('b', 1, { number: '2.5' }), q('c', 2)]
    expect(pairs(displayNumbers(list))).toEqual([
      ['a', '1'],
      ['b', '2.5'],
      ['c', '3'],
    ])
  })

  it('兜底序号按题目在卷内的次序，不受手填题号影响', () => {
    // 第 2 题手填了 2.5，第 3 题的兜底号仍应是 3（而不是 4）。
    const list = [q('a', 0), q('b', 1, { number: '2.5' }), q('c', 2), q('d', 3)]
    expect(displayNumbers(list).get('d')).toBe('4')
  })

  it('与导出器的取值顺序一致：props.number 优先、位置计数兜底', () => {
    const list = [q('a', 0, { number: '一' }), q('b', 1)]
    expect(pairs(displayNumbers(list))).toEqual([
      ['a', '一'],
      ['b', '2'],
    ])
  })
})

// ---- 写入 ----

describe('propsWithNumber', () => {
  it('写入后题号带上', () => {
    expect(propsWithNumber(q('a', 0), '1.1')).toEqual({ number: '1.1' })
  })

  it('超长题号被截断而不是拒绝', () => {
    // 老师在框里敲到第 17 个字时，拦下整次输入反而更烦。
    const long = '1'.repeat(50)
    const out = propsWithNumber(q('a', 0), long)
    expect((out.number as string).length).toBe(QUESTION_NUMBER_MAX_LENGTH)
  })

  it('清空等于不指定题号：删键而不是写空串', () => {
    // 空串会被导出器当成有效值，卷面上会出现一个空题号。
    const out = propsWithNumber(q('a', 0, { number: '1.1' }), '   ')
    expect('number' in out).toBe(false)
  })

  it('不改动其它 props', () => {
    const out = propsWithNumber(q('a', 0, { score: 5, number: '1' }), '2')
    expect(out).toEqual({ score: 5, number: '2' })
  })

  it('不就地改原节点', () => {
    const target = q('a', 0, { number: '1' })
    const snapshot = JSON.parse(JSON.stringify(target))
    propsWithNumber(target, '9')
    expect(target).toEqual(snapshot)
  })

  it('首次写入也能工作（props 里本来什么都没有）', () => {
    expect(propsWithNumber(q('a', 0), '3')).toEqual({ number: '3' })
  })
})

describe('propsWithScore', () => {
  it('写入合法分值', () => {
    expect(propsWithScore(q('a', 0), 4.5)).toEqual({ score: 4.5 })
  })

  it('0 分是合法值，保留', () => {
    expect(propsWithScore(q('a', 0), 0)).toEqual({ score: 0 })
  })

  it('null / undefined / 负数 / NaN 都等于不计分，删键', () => {
    for (const bad of [null, undefined, -1, Number.NaN]) {
      const out = propsWithScore(q('a', 0, { score: 5 }), bad)
      expect('score' in out, String(bad)).toBe(false)
    }
  })

  it('不改动其它 props', () => {
    expect(propsWithScore(q('a', 0, { number: '1', score: 2 }), 7)).toEqual({ number: '1', score: 7 })
  })

  it('不就地改原节点', () => {
    const target = q('a', 0, { score: 2 })
    const snapshot = JSON.parse(JSON.stringify(target))
    propsWithScore(target, 9)
    expect(target).toEqual(snapshot)
  })
})

// ---- 合计 ----

describe('totalScoreOf / roundScore', () => {
  it('按题目块求和', () => {
    expect(totalScoreOf([q('a', 0, { score: 5 }), q('b', 1, { score: 2.5 })])).toBe(7.5)
  })

  it('未填分值的题按 0 计，不影响其它题', () => {
    expect(totalScoreOf([q('a', 0, { score: 5 }), q('b', 1), q('c', 2, { score: 3 })])).toBe(8)
  })

  it('非题目节点不参与合计', () => {
    const list = [h('h', 0, 1), q('a', 1, { score: 5 }), node('t', 'rich_text', 2, { props: { score: 100 } })]
    expect(totalScoreOf(list)).toBe(5)
  })

  it('空清单合计为 0', () => {
    expect(totalScoreOf([])).toBe(0)
  })

  it('浮点噪声被收敛到两位小数', () => {
    // 0.1 + 0.2 在二进制浮点下是 0.30000000000000004，
    // 直接显示会让老师以为系统算错了分。
    expect(roundScore(totalScoreOf([q('a', 0, { score: 0.1 }), q('b', 1, { score: 0.2 })]))).toBe(0.3)
    expect(roundScore(0.1 + 0.2)).toBe(0.3)
  })

  it('常规两位小数收敛正确', () => {
    expect(roundScore(2.344)).toBe(2.34)
    expect(roundScore(2.346)).toBe(2.35)
    expect(roundScore(7.5)).toBe(7.5)
    expect(roundScore(8)).toBe(8)
  })

  it('恰好落在 .xx5 上时按浮点实际值走，不保证"四舍五入进位"', () => {
    // 这是刻意的边界，写出来是为了别有人日后当成 bug 去"修"：
    // 1.005 在二进制浮点里其实是 1.00499999...，乘以 100 得 100.4999...，
    // Math.round 取 100，于是得到 1。
    // 想强行进位就得引入 epsilon，而 epsilon 会在这条线的另一侧
    // （2.675 这类）制造新的错误——那个方向更糟。
    // 分值在真实场景里是 0.5 的整数倍，落不到这个边界上；
    // 这个函数要解决的是 0.1+0.2=0.30000000000000004 那类噪声。
    expect(roundScore(1.005)).toBe(1)
  })
})

// ---- 文案 ----

describe('blockLabel', () => {
  it('每种块给出可读类型名', () => {
    expect(blockLabel(node('a', 'rich_text', 0))).toBe('说明文字')
    expect(blockLabel(node('a', 'heading', 0))).toBe('标题')
    expect(blockLabel(node('a', 'page_break', 0))).toBe('分页符')
    expect(blockLabel(node('a', 'answer_space', 0))).toBe('答题区')
    expect(blockLabel(node('a', 'question_details', 0))).toBe('解析模块')
  })

  it('不认识时退回节点类型本身，不显示空白', () => {
    expect(blockLabel(node('a', 'mystery' as NodeType, 0))).toBe('mystery')
  })
})

describe('answerTextOf', () => {
  const options = [
    { id: 'o1', label: 'A' },
    { id: 'o2', label: 'B' },
    { id: 'o3', label: 'C' },
  ]

  it('单选题把正确选项 id 翻成标签', () => {
    expect(answerTextOf({ options, answer: { kind: 'single', correct: ['o2'] } })).toBe('B')
  })

  it('多选题用顿号连接', () => {
    expect(answerTextOf({ options, answer: { kind: 'multiple', correct: ['o1', 'o3'] } })).toBe('A、C')
  })

  it('找不到对应选项时退回 id，不吞掉答案', () => {
    expect(answerTextOf({ options, answer: { kind: 'single', correct: ['幽灵'] } })).toBe('幽灵')
  })

  it('判断题翻成中文', () => {
    expect(answerTextOf({ answer: { kind: 'boolean', bool_answer: true } })).toBe('正确')
    expect(answerTextOf({ answer: { kind: 'boolean', bool_answer: false } })).toBe('错误')
  })

  it('填空题把富文本片段真的转成文字', () => {
    // 回归：这里曾经是 `group.map(() => '')`，等于把每个片段换成空串，
    // 于是填空题的答案永远显示成空白——老师看到"答案："后面什么都没有，
    // 会以为是数据没存上。
    const text = answerTextOf({
      answer: {
        kind: 'blanks',
        blanks: [
          { accepts: [[plainToRichDoc('甲'), plainToRichDoc('的平方')]] },
          { accepts: [[plainToRichDoc('乙')], [plainToRichDoc('B')]] },
        ],
      },
    })
    expect(text).toBe('甲的平方；乙或B')
  })

  it('填空题含公式时公式以 $…$ 还原', () => {
    const text = answerTextOf({
      answer: { kind: 'blanks', blanks: [{ accepts: [[plainToRichDoc('$x^2$')]] }] },
    })
    expect(text).toBe('$x^2$')
  })

  it('填空题数组缺失时得到空串而不是崩溃', () => {
    expect(answerTextOf({ answer: { kind: 'blanks' } })).toBe('')
    expect(answerTextOf({ answer: { kind: 'blanks', blanks: [{}] } })).toBe('')
  })

  it('解答题给出指针文案', () => {
    expect(answerTextOf({ answer: { kind: 'reference' } })).toBe('见参考答案')
  })

  it('缺失、空答案、未知类型都返回空串而不是抛错', () => {
    expect(answerTextOf(undefined)).toBe('')
    expect(answerTextOf(null)).toBe('')
    expect(answerTextOf({})).toBe('')
    expect(answerTextOf({ answer: null })).toBe('')
    expect(answerTextOf({ answer: { kind: '外星类型' } })).toBe('')
  })

  it('选项为空时退回 id', () => {
    expect(answerTextOf({ answer: { kind: 'single', correct: ['o1'] } })).toBe('o1')
  })
})
