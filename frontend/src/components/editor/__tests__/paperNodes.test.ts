/**
 * 域逻辑测试：画布节点 schema。
 *
 * 这一层回答的是一个具体问题：**转换器产出的文档，schema 认不认？**
 *
 * 转换器（paperDocument.ts）只管产出 JSON，schema 只管声明形状，两边各自
 * 测试都能过，但合起来可能对不上——比如转换器写出 `paperRichText` 而 schema
 * 里叫 `papertext`，或者 schema 的内容表达式太宽/太窄。这类错误在真正
 * 打开画布之前不会暴露，而那时用户已经看到了一屏排版错乱的卷子。
 *
 * 所以这里把两层**接在一起**测：建 schema → 吃转换器的输出 → 转回节点。
 */
import { describe, expect, it } from 'vitest'
import { getSchema } from '@tiptap/core'
import { canvasExtensions } from '../canvasExtensions'
import { CANVAS_NODE, docToNodes, kindOf, nodesToDoc, type ModuleScopeValue } from '@/lib/paperDocument'
import { plainToRichDoc } from '@/lib/richdoc'
import type { NodeType, PaperNode } from '@/api/paper'

// ---- schema ----

/**
 * 用**画布真正会用的那份扩展清单**建 schema。
 *
 * 不在这里自己拼数组：自己拼就等于测试和组件各拿一份清单，
 * 少注册一个 Table 只会在真打开画布时才炸（这条路径已经踩过一次）。
 * 也不传任何「只读」开关——schema 不随模式变化，理由见 canvasExtensions.ts。
 */
const canvasSchema = getSchema(canvasExtensions())

/**
 * 用 schema 校验一个 JSON 文档。
 *
 * 必须显式调 `check()`：`nodeFromJSON` **不校验内容表达式**，
 * 它只按名字把 JSON 变成节点树（ProseMirror 的 `NodeType.create` 不调
 * `check()`）。只靠 nodeFromJSON 的话，一个把 `position` 放错层的文档
 * 会被静静接受，直到编辑器真正挂载才炸——那时用户已经在看一屏空白的卷子。
 * `check()` 会按内容表达式逐层核对，不合法直接抛错。
 */
function validate(doc: unknown): void {
  canvasSchema.nodeFromJSON(doc).check()
}

/**
 * 去掉值为 null/undefined 的 attrs，用于比较"过了一遍 schema"的结果。
 *
 * 为什么需要它：`TextAlign` 扩展给 paragraph / heading 挂了一个
 * `textAlign` 属性，schema 解析时会把缺省值**物化**成 `attrs: {textAlign: null}`。
 * 这是既有行为，不是这次改造引入的——`RichTextEditor.vue` 同样配了
 * TextAlign，所以题目保存时早就在写这个空属性。
 *
 * 比较前抹掉它，是为了让断言盯住**真东西**（节点有没有丢、文字与公式有没有变），
 * 而不是被一个等价的空属性搅成全红。
 * 注意这不影响 paperDocument.test.ts —— 那边不经过 schema，是逐字段严格比较。
 */
function stripNullAttrs(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(stripNullAttrs)
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {}
    for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
      if (key === 'attrs' && item && typeof item === 'object') {
        const attrs: Record<string, unknown> = {}
        for (const [attrKey, attrValue] of Object.entries(item as Record<string, unknown>)) {
          if (attrValue === null || attrValue === undefined) continue
          attrs[attrKey] = stripNullAttrs(attrValue)
        }
        // 全被抹掉时连 attrs 这个键一起去掉，与"本来就没有 attrs"对齐。
        if (Object.keys(attrs).length) out.attrs = attrs
        continue
      }
      out[key] = stripNullAttrs(item)
    }
    return out
  }
  return value
}

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

function question(id: string, pos: number, qid: number, props: Record<string, unknown> = {}): PaperNode {
  return node(id, 'question', pos, { content: { question_id: qid, content_revision: 3 }, props })
}

function answerItem(id: string, parentID: string, pos: number, source: string): PaperNode {
  return node(id, 'answer_item', pos, { parent_id: parentID, source_question_node_id: source })
}

function samplePaper(): PaperNode[] {
  return [
    node('h', 'heading', 0, { content: plainToRichDoc('一、选择题'), props: { level: 2 } }),
    question('q1', 1, 11, { score: 5, number: '1.1' }),
    text('t', 2, '请从下面选项中选出一个正确答案，公式 $E=mc^2$ 也要能用'),
    question('q2', 3, 12),
    node('pb', 'page_break', 4),
    node('as', 'answer_space', 5, { props: { lines: 6, style: 'lined' } }),
    node('m', 'question_details', 6, { props: { scope: 'before', title: '参考答案' } }),
    answerItem('a1', 'm', 0, 'q1'),
    answerItem('a2', 'm', 1, 'q2'),
  ]
}

// ---- schema 能吃下转换器的输出 ----

describe('schema 与转换器对接', () => {
  it('全部七种节点都被 schema 认识', () => {
    for (const name of Object.values(CANVAS_NODE)) {
      expect(canvasSchema.nodes[name], `schema 缺少 ${name}`).toBeDefined()
    }
  })

  it('典型试卷的文档能通过 schema 校验', () => {
    expect(() => validate(nodesToDoc(samplePaper()))).not.toThrow()
  })

  it('空卷能通过校验', () => {
    expect(() => validate(nodesToDoc([]))).not.toThrow()
  })

  it('只有一道题的最简卷能通过校验', () => {
    expect(() => validate(nodesToDoc([question('q', 0, 1)]))).not.toThrow()
  })

  it('富文本块里的公式、填空、表格、代码块能被 schema 识别', () => {
    const rich = node('t', 'rich_text', 0, {
      content: {
        type: 'doc',
        content: [
          {
            type: 'paragraph',
            content: [
              { type: 'text', text: '解 ' },
              { type: 'inlineMath', attrs: { latex: 'x^2' } },
              { type: 'blank' },
            ],
          },
          { type: 'blockMath', attrs: { latex: 'a=b' } },
          { type: 'codeBlock', content: [{ type: 'text', text: 'int main() {}' }] },
          {
            type: 'table',
            content: [
              {
                type: 'tableRow',
                content: [{ type: 'tableHeader', content: [{ type: 'paragraph' }] }],
              },
            ],
          },
        ],
      },
    })
    expect(() => validate(nodesToDoc([rich]))).not.toThrow()
  })

  it('代码块能原样穿过一轮往返', () => {
    // 回归：codeBlock 曾被关掉，于是导出器认识、编辑器不认，
    // 一道含代码块的题目在编辑器里直接抛错打不开。
    const rich = node('t', 'rich_text', 0, {
      content: {
        type: 'doc',
        content: [{ type: 'codeBlock', content: [{ type: 'text', text: 'int main() {}' }] }],
      },
    })
    const { nodes } = docToNodes(canvasSchema.nodeFromJSON(nodesToDoc([rich])).toJSON())
    expect(stripNullAttrs(nodes[0].content)).toEqual(stripNullAttrs(rich.content))
  })
})

// ---- 内容表达式：画布节点不能被塞进富文本块 ----

describe('内容表达式', () => {
  it('说明文字里塞进题目节点会被 schema 拒绝', () => {
    // 这是两层 AST 的底线：题目块只能是顶层块。
    const doc = {
      type: 'doc',
      content: [
        {
          type: CANVAS_NODE.richText,
          attrs: { nodeId: 't' },
          content: [{ type: CANVAS_NODE.pageBreak, attrs: { nodeId: 'pb' } }],
        },
      ],
    }
    expect(() => validate(doc)).toThrow()
  })

  it('解析模块里塞进非条目节点会被拒绝', () => {
    const doc = {
      type: 'doc',
      content: [
        {
          type: CANVAS_NODE.questionDetails,
          attrs: { nodeId: 'm' },
          content: [{ type: CANVAS_NODE.richText, attrs: { nodeId: 't' }, content: [] }],
        },
      ],
    }
    expect(() => validate(doc)).toThrow()
  })

  it('空解析模块合法（条目等服务端派生）', () => {
    const doc = nodesToDoc([node('m', 'question_details', 0)])
    expect(() => validate(doc)).not.toThrow()
  })

  it('条目不能有子节点', () => {
    const doc = {
      type: 'doc',
      content: [
        {
          type: CANVAS_NODE.questionDetails,
          attrs: { nodeId: 'm' },
          content: [
            {
              type: CANVAS_NODE.answerItem,
              attrs: { nodeId: 'a' },
              content: [{ type: 'paragraph' }],
            },
          ],
        },
      ],
    }
    expect(() => validate(doc)).toThrow()
  })
})

// ---- 节点规格 ----

describe('节点规格', () => {
  it('解析条目不可拖动也不可选中——服务端派生，界面上碰不着', () => {
    const spec = canvasSchema.nodes[CANVAS_NODE.answerItem]
    expect(spec.spec.draggable).toBe(false)
    expect(spec.spec.selectable).toBe(false)
  })

  it('可编辑的顶层块可拖动', () => {
    for (const name of [CANVAS_NODE.question, CANVAS_NODE.pageBreak, CANVAS_NODE.answerSpace]) {
      expect(canvasSchema.nodes[name].spec.draggable, name).toBe(true)
    }
  })

  it('说明文字与标题不是原子节点，内部能容纳段落', () => {
    // 它们装的是**块级**内容（段落等），不是直接装行内文本，
    // 所以 inlineContent 是 false；能敲字是因为里面那个段落能敲字。
    for (const name of [CANVAS_NODE.richText, CANVAS_NODE.heading]) {
      const spec = canvasSchema.nodes[name]
      expect(spec.isAtom, name).toBe(false)
      expect(spec.contentMatch.matchType(canvasSchema.nodes.paragraph), name).toBeTruthy()
    }
  })

  it('富文本块接受一个普通段落（内容表达式是"或"不是"序列"）', () => {
    // 回归：表达式一度写成空格分隔，被解析成"先段落、再引用块、再列表…"
    // 的序列，于是连一个孤零零的段落都判为非法。
    const doc = nodesToDoc([text('t', 0, '就一句话')])
    expect(() => validate(doc)).not.toThrow()
  })

  it('节点名都带 paper 前缀，不与 StarterKit 撞名', () => {
    for (const name of Object.values(CANVAS_NODE)) {
      expect(name).toMatch(/^paper/)
      expect(canvasSchema.nodes.paragraph).toBeDefined()
    }
  })
})

// ---- 属性默认值与解析 ----

/**
 * 取某个画布节点的属性定义。
 *
 * 需要收窄：Tiptap 在**运行时**把 `addAttributes()` 的结果挂到 NodeType 上，
 * 但 ProseMirror 的 `NodeType` 类型里没有 `attrs` 这个成员。
 */
function attrsOf(name: string): Record<string, { default?: unknown }> {
  return (canvasSchema.nodes[name] as unknown as { attrs: Record<string, { default?: unknown }> }).attrs
}

describe('属性', () => {
  it('题目块默认不显示分值与题号', () => {
    expect(attrsOf(CANVAS_NODE.question).score.default).toBeNull()
    expect(attrsOf(CANVAS_NODE.question).number.default).toBeNull()
  })

  it('标题层级默认 1，行数默认 4，范围默认 blank / all', () => {
    expect(attrsOf(CANVAS_NODE.heading).level.default).toBe(1)
    expect(attrsOf(CANVAS_NODE.answerSpace).lines.default).toBe(4)
    expect(attrsOf(CANVAS_NODE.answerSpace).style.default).toBe('blank')
    expect(attrsOf(CANVAS_NODE.questionDetails).scope.default).toBe('all')
  })

  it('nodeId 默认空串——没有它就无法回存后端', () => {
    for (const name of Object.values(CANVAS_NODE)) {
      expect(attrsOf(name).nodeId.default, name).toBe('')
    }
  })

  it('分值、题号、题号 id 都能被 NodeView 从 attrs 读到', () => {
    // 画布走的是 JSON 路径（getJSON），NodeView 直接从 attrs 取这些值来渲染，
    // 所以这里断言的是"转换器把值放进了 attrs"，而不是 HTML 序列化的结果。
    const drawn = nodesToDoc([question('q', 0, 42, { score: 4, number: '1.1' })])
    const attrs = drawn.content?.[0]?.attrs ?? {}
    expect(attrs.nodeId).toBe('q')
    expect(attrs.questionId).toBe(42)
    expect(attrs.contentRevision).toBe(3)
    expect(attrs.score).toBe(4)
    expect(attrs.number).toBe('1.1')
  })

  it('无分值/题号时 attrs 里没有这两个键，而不是 null 占位', () => {
    const drawn = nodesToDoc([question('q', 0, 42)])
    const attrs = drawn.content?.[0]?.attrs ?? {}
    expect('score' in attrs).toBe(false)
    expect('number' in attrs).toBe(false)
  })

  it('答题区的行数与样式能读到', () => {
    const drawn = nodesToDoc([node('as', 'answer_space', 0, { props: { lines: 6, style: 'lined' } })])
    const attrs = drawn.content?.[0]?.attrs ?? {}
    expect(attrs.lines).toBe(6)
    expect(attrs.style).toBe('lined')
  })
})

// ---- 端到端：转换 → schema → 转换 ----

describe('端到端', () => {
  it('经 schema 一轮之后，节点结构与字段仍然一致', () => {
    const original = samplePaper()
    const doc = nodesToDoc(original)
    // 先按内容表达式真正校验一遍（nodeFromJSON 不做这件事，见 validate）。
    expect(() => validate(doc)).not.toThrow()
    const { nodes, dropped } = docToNodes(canvasSchema.nodeFromJSON(doc).toJSON())
    expect(dropped).toEqual([])
    // 抹掉 schema 物化的空属性（见 stripNullAttrs 的注释）之后必须逐字段相等：
    // 节点一个不少、顺序不变、props 不变、文字与公式不变。
    expect(stripNullAttrs(nodes)).toEqual(stripNullAttrs(original))
  })

  it('schema 不会悄悄丢掉任何节点', () => {
    const original = samplePaper()
    const parsed = canvasSchema.nodeFromJSON(nodesToDoc(original))
    const { nodes } = docToNodes(parsed.toJSON())
    expect(nodes.map((n) => n.id)).toEqual(original.map((n) => n.id))
    expect(nodes.map((n) => n.node_type)).toEqual(original.map((n) => n.node_type))
    expect(nodes.map((n) => n.position)).toEqual(original.map((n) => n.position))
  })

  it('往返两轮稳定', () => {
    const once = docToNodes(canvasSchema.nodeFromJSON(nodesToDoc(samplePaper())).toJSON()).nodes
    const twice = docToNodes(canvasSchema.nodeFromJSON(nodesToDoc(once)).toJSON()).nodes
    expect(stripNullAttrs(twice)).toEqual(stripNullAttrs(once))
  })

  it('非法 scope 经 schema 往返后仍归一为 all', () => {
    const bad = node('m', 'question_details', 0, { props: { scope: '瞎写' as ModuleScopeValue } })
    const parsed = canvasSchema.nodeFromJSON(nodesToDoc([bad]))
    expect(docToNodes(parsed.toJSON()).nodes[0].props).toEqual({ scope: 'all' })
  })
})
