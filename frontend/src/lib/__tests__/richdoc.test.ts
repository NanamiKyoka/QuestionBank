/**
 * 域逻辑测试：富文本 ↔ 纯文本的往返。
 *
 * 往返保真是题库编辑的地基：选题、编辑选项、AI 补写解析，
 * 全都是"读出来给人看、改完再写回去"。一旦公式在某一环被拍平成
 * 排版字形，用户点一次保存，题目里的公式就永久变成普通文字了。
 */
import { describe, expect, it } from 'vitest'
import {
  emptyDoc,
  isDocEmpty,
  plainToRichDoc,
  richDocToPlain,
  type RichDoc,
} from '../richdoc'

describe('plainToRichDoc', () => {
  it('空串得到最小合法文档', () => {
    expect(plainToRichDoc('')).toEqual({ type: 'doc', content: [{ type: 'paragraph' }] })
    expect(plainToRichDoc('   \n  ')).toEqual({ type: 'doc', content: [{ type: 'paragraph' }] })
  })

  it('成对的 $ 被识别成行内公式', () => {
    const doc = plainToRichDoc('当 $x>0$ 时')
    const para = doc.content[0]
    expect(para.content?.map((n) => n.type)).toEqual(['text', 'inlineMath', 'text'])
    expect(para.content?.[1].attrs?.latex).toBe('x>0')
  })

  it('落单的 $ 当普通字符，不会把后半句吞成公式', () => {
    // 真实场景：题干里出现裸美元符号（"价格是 $100"）。
    // 如果这里贪婪匹配到下一个 $，后面的正文会整段变成公式内容。
    const doc = plainToRichDoc('价格是 $100')
    expect(doc.content[0].content).toEqual([{ type: 'text', text: '价格是 $100' }])
    expect(richDocToPlain(doc)).toBe('价格是 $100')
  })

  it('空公式 $$…$$ 落回普通文本', () => {
    const doc = plainToRichDoc('a $$ b')
    expect(richDocToPlain(doc)).toBe('a $$ b')
  })

  it('换行变成 hardBreak', () => {
    const doc = plainToRichDoc('第一行\n第二行')
    expect(doc.content[0].content?.map((n) => n.type)).toEqual(['text', 'hardBreak', 'text'])
  })
})

describe('richDocToPlain', () => {
  it('公式还原成 $latex$，保证"读出再写回"不丢公式', () => {
    const doc: RichDoc = {
      type: 'doc',
      content: [
        {
          type: 'paragraph',
          content: [
            { type: 'text', text: '设 ' },
            { type: 'inlineMath', attrs: { latex: 'a^2+b^2' } },
            { type: 'text', text: '，则 ' },
            { type: 'inlineMath', attrs: { latex: 'c' } },
          ],
        },
      ],
    }
    expect(richDocToPlain(doc)).toBe('设 $a^2+b^2$，则 $c$')
  })

  it('块级公式前后补换行，避免与相邻文字粘连', () => {
    const doc: RichDoc = {
      type: 'doc',
      content: [
        { type: 'paragraph', content: [{ type: 'blockMath', attrs: { latex: 'x=1' } }] },
        { type: 'paragraph', content: [{ type: 'text', text: '综上' }] },
      ],
    }
    const text = richDocToPlain(doc)
    expect(text).toContain('$x=1$')
    expect(text).toContain('综上')
  })

  it('填空位还原成六个下划线', () => {
    const doc: RichDoc = {
      type: 'doc',
      content: [{ type: 'paragraph', content: [{ type: 'blank' }] }],
    }
    expect(richDocToPlain(doc)).toBe('______')
  })

  it('非文档与畸形输入返回空串而不是抛错', () => {
    expect(richDocToPlain(null)).toBe('')
    expect(richDocToPlain(undefined)).toBe('')
    expect(richDocToPlain(42)).toBe('')
    expect(richDocToPlain({ type: 'doc' })).toBe('')
    expect(richDocToPlain({ type: 'doc', content: 'x' })).toBe('')
  })
})

describe('往返', () => {
  const cases = [
    '普通文字',
    '公式 $E=mc^2$ 在中间',
    '第一行\n第二行 $a+b$',
    '价格是 $100 的钢笔',
    '两个公式 $x$ 和 $y$',
  ]

  for (const text of cases) {
    it(`plain → rich → plain 保持不变：${JSON.stringify(text)}`, () => {
      expect(richDocToPlain(plainToRichDoc(text))).toBe(text)
    })
  }
})

describe('isDocEmpty', () => {
  it('空段落算空', () => {
    expect(isDocEmpty(emptyDoc())).toBe(true)
    expect(isDocEmpty({ type: 'doc', content: [{ type: 'paragraph' }] })).toBe(true)
  })

  it('只有公式或空白符的文档算非空', () => {
    expect(isDocEmpty(plainToRichDoc('$x$'))).toBe(false)
  })
})
