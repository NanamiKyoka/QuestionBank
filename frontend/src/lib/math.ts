import katex from 'katex'

/**
 * 渲染 LaTeX 公式为 HTML。
 *
 * 用 throwOnError:false：题目里的公式由用户或模型录入，写错在所难免。
 * 抛异常会让整个题干预览白屏，而 KaTeX 自带的错误标记（红色原文）
 * 反而能让用户一眼看出是哪一段公式写错了。
 */
export function renderMath(latex: string, displayMode = false): string {
  if (!latex) return ''
  try {
    return katex.renderToString(latex, {
      displayMode,
      throwOnError: false,
      // 允许 \text{中文} 之类的写法
      strict: false,
      trust: false,
      output: 'html',
    })
  } catch {
    // 连 KaTeX 都崩了（极罕见），退化为原文而不是让页面挂掉。
    return escapeHtml(latex)
  }
}

export function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
