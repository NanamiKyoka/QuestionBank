/**
 * 富文本文档的小工具。
 *
 * 存在的意义：选项、标签说明这类**短文本**字段不值得挂一个完整编辑器，
 * 但它们又经常含公式。用一个纯文本输入框 + `$...$` 约定，
 * 既轻量又不牺牲公式能力。
 */

export interface RichTextNode {
  type: string
  text?: string
  marks?: { type: string }[]
  attrs?: Record<string, unknown>
  content?: RichTextNode[]
}

export interface RichDoc {
  type: 'doc'
  content: RichTextNode[]
}

/** 空文档。后端要求题干不能为空，这里给一个最小合法结构。 */
export function emptyDoc(): RichDoc {
  return { type: 'doc', content: [{ type: 'paragraph' }] }
}

/**
 * 把 `$...$` 包裹的片段识别成行内公式，其余作为普通文本。
 *
 * 不用正则直接做替换是因为需要正确处理未闭合的 `$`：
 * "价格是 $100" 这种句子里的单个美元符号不应该被当成公式起点。
 * 因此扫描时要求成对出现，落单的 `$` 原样保留。
 */
export function plainToRichDoc(text: string): RichDoc {
  const source = (text ?? '').trim()
  if (!source) return emptyDoc()

  const content: RichTextNode[] = []
  let buffer = ''
  let i = 0

  const flushText = () => {
    if (buffer) {
      content.push({ type: 'text', text: buffer })
      buffer = ''
    }
  }

  while (i < source.length) {
    const ch = source[i]

    if (ch === '$') {
      const end = source.indexOf('$', i + 1)
      // 必须成对，且公式内容非空；否则按普通字符处理。
      if (end > i + 1) {
        const latex = source.slice(i + 1, end).trim()
        if (latex) {
          flushText()
          content.push({ type: 'inlineMath', attrs: { latex } })
          i = end + 1
          continue
        }
      }
    }

    if (ch === '\n') {
      flushText()
      content.push({ type: 'hardBreak' })
      i++
      continue
    }

    buffer += ch
    i++
  }
  flushText()

  return {
    type: 'doc',
    content: [{ type: 'paragraph', content: content.length ? content : undefined }],
  }
}

/**
 * 从富文本文档提取纯文本，用于列表摘要与回填输入框。
 *
 * 公式被还原成 `$latex$`：这样"读出来再写回去"（如编辑选项时）
 * 不会丢失公式。若还原成排版的字形，用户再保存时公式就变成普通文字了。
 */
export function richDocToPlain(doc: unknown): string {
  if (!doc || typeof doc !== 'object') return ''
  const root = doc as RichDoc
  if (!Array.isArray(root.content)) return ''

  const walk = (nodes: RichTextNode[]): string => {
    let out = ''
    for (const node of nodes) {
      switch (node.type) {
        case 'text':
          out += node.text ?? ''
          break
        case 'inlineMath':
          out += `$${String(node.attrs?.latex ?? '')}$`
          break
        case 'blockMath':
          // 块级公式单独成段，前后补换行避免与相邻文字粘连。
          out += `\n$$${String(node.attrs?.latex ?? '')}$$\n`
          break
        case 'hardBreak':
          out += '\n'
          break
        case 'blank':
          out += '______'
          break
        case 'paragraph':
        case 'heading':
          out += walk(node.content ?? []) + '\n'
          break
        default:
          out += walk(node.content ?? [])
      }
    }
    return out
  }

  return walk(root.content).trim()
}

/** 判断文档是否实质为空（只有空段落也算空）。 */
export function isDocEmpty(doc: unknown): boolean {
  return richDocToPlain(doc).trim().length === 0
}
