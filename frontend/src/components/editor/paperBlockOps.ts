/**
 * 画布上的块级操作：移动、删除。
 *
 * 为什么不直接用 Tiptap 的命令：
 *
 *  - Tiptap 没有内置的"把当前块上移/下移"。ProseMirror 的 `Transform` 也不提供
 *    `moveNode`。要让编辑器里的块换位，只能自己动事务。
 *  - 实现上刻意用**重建整个文档**而不是 delete+insert 两次计算下标：
 *    整树覆盖本来就是这份卷子的保存方式，重建与它同构；而 delete+insert
 *    需要处理"删除之后插入点要前移"的补偿，那正是最容易写错一格的地方
 *    （usePaperAiTools 的 move_node 就栽在这上面，见那边的注释）。
 *    重建的代价是选中态会丢，对一次显式的"上移"来说可以接受。
 *
 * 这些函数需要真实的编辑器实例（要 document），因此测试跑在 jsdom 下。
 */
import type { Editor } from '@tiptap/core'
import type { Node as PMNode } from '@tiptap/pm/model'

/** 取顶层块的当前下标。不在顶层时返回 -1。 */
export function topLevelIndexOf(editor: Editor, pos: number): number {
  const doc = editor.state.doc
  if (pos < 0 || pos > doc.content.size) return -1
  const $pos = doc.resolve(pos)
  // depth 0 表示这个位置直接挂在 doc 下；更深的（解析模块的条目）不参与块级移动。
  if ($pos.depth !== 0) return -1
  return $pos.index(0)
}

/** 文档里顶层块的个数。 */
export function topLevelCount(editor: Editor): number {
  return editor.state.doc.childCount
}

/**
 * 把某个顶层块上移或下移一格。
 *
 * 已经在两端时**不做任何事并返回 false**：让调用方据此禁用按钮，
 * 而不是默默把它扔到另一头（"上移"在第一个块上不该变成"移到最后"）。
 */
export function moveTopLevelBlock(editor: Editor, pos: number, delta: number): boolean {
  if (delta === 0) return false
  const doc = editor.state.doc
  const index = topLevelIndexOf(editor, pos)
  if (index < 0) return false

  const target = index + delta
  if (target < 0 || target >= doc.childCount) return false

  const children: PMNode[] = []
  doc.forEach((child) => children.push(child))
  const moving = children[index]
  if (!moving) return false

  children.splice(index, 1)
  children.splice(target, 0, moving)

  // 用旧文档的 type/attrs 造一份新文档，只把子节点顺序换掉。
  const rebuilt = doc.type.create(doc.attrs, children, doc.marks)
  editor.view.dispatch(editor.state.tr.replaceWith(0, doc.content.size, rebuilt.content))
  return true
}

/**
 * 删除一个顶层块。
 *
 * 解析模块不允许从这里删——它是界面上一个开关管的，删掉会留下一批
 * 无人维护的孤儿条目。返回 false 让调用方提示，而不是静默失败。
 */
export function deleteTopLevelBlock(editor: Editor, pos: number): boolean {
  const doc = editor.state.doc
  const index = topLevelIndexOf(editor, pos)
  if (index < 0) return false

  const node = doc.child(index)
  if (node.type.name === 'paperQuestionDetails') return false

  let from = 0
  for (let i = 0; i < index; i++) from += doc.child(i).nodeSize
  editor.view.dispatch(editor.state.tr.delete(from, from + node.nodeSize))
  return true
}

/** 某个顶层块能不能上移 / 下移。供界面禁用按钮。 */
export function canMoveTopLevelBlock(editor: Editor, pos: number, delta: number): boolean {
  const index = topLevelIndexOf(editor, pos)
  if (index < 0) return false
  const target = index + delta
  return target >= 0 && target < editor.state.doc.childCount
}
