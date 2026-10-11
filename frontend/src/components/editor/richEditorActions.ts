/**
 * 富文本编辑器上的插入动作。
 *
 * 为什么单独一个模块：现在有**两个**编辑器要提供同一批能力——
 * 题目编辑用的 `RichTextEditor.vue`，以及组卷画布的富文本块
 * （`PaperRichToolbar.vue` 作用在同一份画布文档上）。把动作留在某一个
 * 组件里，另一个就只能抄一遍；而抄第二遍的那次，多半就是两者开始有能力差的时候
 * （这次画布切换正好踩过一次：工具栏没跟着搬过去，公式/填空/表格/图片
 * 全都失去了入口）。
 *
 * 模块里分成两层，是为了让**能测的部分真的被测**：
 *
 *   - `insertXxx(editor, …)` 只动编辑器，纯 ProseMirror 操作，jsdom 下可测；
 *   - `requestXxx(editor)` 负责弹输入框，是 UI 交互，不测。
 *
 * 公式沿用 `$…$` 的既有约定：题库里所有富文本都以 RichDoc 存储，
 * 行内公式是 `inlineMath` 节点、独立公式是 `blockMath` 节点，
 * 两者在导出时才会变成 OMML / LaTeX。所以这里插入的是**节点**，
 * 不是一段看起来像公式的文本。
 */
import type { Editor } from '@tiptap/core'
import { ElMessageBox } from 'element-plus'

/** 插入行内公式节点。latex 为空时不动。 */
export function insertInlineMath(editor: Editor | null | undefined, latex: string): boolean {
  const value = latex.trim()
  if (!editor || !value) return false
  return editor.chain().focus().insertContent({ type: 'inlineMath', attrs: { latex: value } }).run()
}

/** 插入独立公式节点。 */
export function insertBlockMath(editor: Editor | null | undefined, latex: string): boolean {
  const value = latex.trim()
  if (!editor || !value) return false
  return editor.chain().focus().insertContent({ type: 'blockMath', attrs: { latex: value } }).run()
}

/**
 * 插入填空空位。
 *
 * 空位必须是结构化的 `blank` 节点，不能是一串下划线：导出与自动判分
 * 都靠这个节点认"这里要填一个空"，靠下划线去猜是不可靠的。
 */
export function insertBlank(editor: Editor | null | undefined): boolean {
  if (!editor) return false
  return editor.chain().focus().insertContent({ type: 'blank' }).run()
}

/** 撤销 / 重做。 */
export function undo(editor: Editor | null | undefined): boolean {
  return editor?.chain().focus().undo().run() ?? false
}

export function redo(editor: Editor | null | undefined): boolean {
  return editor?.chain().focus().redo().run() ?? false
}

/** 设置段落样式：0 为正文，1-3 为标题。 */
export function setParagraphStyle(editor: Editor | null | undefined, level: number): boolean {
  if (!editor) return false
  if (level === 0) return editor.chain().focus().setParagraph().run()
  return editor.chain().focus().toggleHeading({ level: level as 1 | 2 | 3 }).run()
}

/** 设置文本对齐。 */
export function setAlignment(
  editor: Editor | null | undefined,
  align: 'left' | 'center' | 'right',
): boolean {
  return editor?.chain().focus().setTextAlign(align).run() ?? false
}

export type TableCommand =
  | 'insert'
  | 'addRowBefore'
  | 'addRowAfter'
  | 'deleteRow'
  | 'addColumnBefore'
  | 'addColumnAfter'
  | 'deleteColumn'
  | 'mergeCells'
  | 'splitCell'
  | 'deleteTable'

export function runTableCommand(editor: Editor | null | undefined, command: TableCommand): boolean {
  if (!editor) return false
  const chain = editor.chain().focus()
  switch (command) {
    case 'insert':
      return chain.insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()
    case 'addRowBefore':
      return chain.addRowBefore().run()
    case 'addRowAfter':
      return chain.addRowAfter().run()
    case 'deleteRow':
      return chain.deleteRow().run()
    case 'addColumnBefore':
      return chain.addColumnBefore().run()
    case 'addColumnAfter':
      return chain.addColumnAfter().run()
    case 'deleteColumn':
      return chain.deleteColumn().run()
    case 'mergeCells':
      return chain.mergeCells().run()
    case 'splitCell':
      return chain.splitCell().run()
    case 'deleteTable':
      return chain.deleteTable().run()
  }
}

/** 插入三行三列的表格（带表头）。 */
export function insertTable(editor: Editor | null | undefined): boolean {
  if (!editor) return false
  return editor.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()
}

/** 插入已上传成功的图片。 */
export function insertImage(editor: Editor | null | undefined, src: string): boolean {
  if (!editor || !src) return false
  return editor.chain().focus().setImage({ src }).run()
}

/** 清除当前块的节点类型与所有行内标记。 */
export function clearFormat(editor: Editor | null | undefined): boolean {
  if (!editor) return false
  return editor.chain().focus().clearNodes().unsetAllMarks().run()
}

/**
 * 弹输入框问 LaTeX 源码。
 *
 * 返回 null 表示用户取消——调用方据此什么都不做，而不是插一个空公式。
 */
export async function promptLatex(title: string, placeholder: string): Promise<string | null> {
  try {
    const { value } = await ElMessageBox.prompt('请输入 LaTeX 源码（不需要写 $ 符号）', title, {
      confirmButtonText: '插入',
      cancelButtonText: '取消',
      inputPlaceholder: placeholder,
      inputValidator: (v) => (v && v.trim() ? true : '公式不能为空'),
    })
    return value.trim()
  } catch {
    return null
  }
}

/** 问一个行内公式再插进去。 */
export async function requestInlineMath(editor: Editor | null | undefined): Promise<boolean> {
  const latex = await promptLatex('插入行内公式', '例如：x^2 + y^2 = r^2')
  if (latex === null) return false
  return insertInlineMath(editor, latex)
}

/** 问一个独立公式再插进去。 */
export async function requestBlockMath(editor: Editor | null | undefined): Promise<boolean> {
  const latex = await promptLatex('插入独立公式', '例如：\\frac{-b \\pm \\sqrt{b^2-4ac}}{2a}')
  if (latex === null) return false
  return insertBlockMath(editor, latex)
}

/**
 * 上传图片并插入。
 *
 * 走后端 `/uploads/images`：那里按文件头校验真实格式、限制尺寸并拒绝 SVG
 * （SVG 可内嵌脚本，当图片直接提供出去等于给了 XSS 机会）。因此这里不做
 * 本地格式判断——客户端校验只是体验优化，说了算的是服务端。
 */
export async function uploadAndInsertImage(editor: Editor | null | undefined, file: File): Promise<boolean> {
  if (!editor || !file) return false
  const { uploadApi } = await import('@/api/ai')
  const saved = await uploadApi.image(file)
  return insertImage(editor, saved.url)
}

/** 画布富文本块里允许插入的节点（供工具栏决定显示哪些按钮）。 */
export const RICH_BLOCK_NODE_NAMES = ['paperRichText', 'paperHeading'] as const

/**
 * 当前选区是否落在可编辑的富文本块里。
 *
 * 工具栏据此显示或隐藏：题目块有自己的属性控件，解析条目只读，
 * 在那些地方露出公式/表格按钮只会让人以为点了没反应。
 */
export function selectionInRichBlock(editor: Editor | null | undefined): boolean {
  if (!editor) return false
  return RICH_BLOCK_NODE_NAMES.some((name) => editor.isActive(name))
}
