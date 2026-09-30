/**
 * 画布的完整扩展集——schema 的**唯一来源**。
 *
 * 为什么单独一个文件：画布节点（paperNodes.ts）的内容表达式里点名了
 * `paragraph`/`table`/`codeBlock`/`image` 这些节点。这些名字只有在对应的
 * 扩展真的注册进 schema 时才存在，少注册一个，`getSchema` 就会在构造
 * content expression 时直接抛错——而这个错误在真打开画布之前根本看不出来。
 *
 * 把清单收在一处，组件与测试都从这里取，就不会再出现「测试里的 schema
 * 少注册了 Table，画布跑起来才炸」这种只在某一边发生的事。
 *
 * **这份清单不随模式变化。** 曾经有过一个 `readOnly` 选项去裁掉图片等扩展，
 * 那是错的：只读场景（版本预览、快照查看）读的是**同一批文档**，
 * 用一份更窄的 schema 去解析，遇到图片就会因为「schema 里没有 image」
 * 直接抛错——而只读预览恰恰是最不该炸的地方。只读要靠 `editable: false`
 * 表达，那是编辑器实例的属性，不是 schema 的属性。
 *
 * 必须与 components/editor/RichTextEditor.vue 保持一致：两边认同一批节点，
 * 同一份富文本才能在题目里打开、在卷子里也打得开。
 */
import StarterKit from '@tiptap/starter-kit'
import Image from '@tiptap/extension-image'
import Placeholder from '@tiptap/extension-placeholder'
import Subscript from '@tiptap/extension-subscript'
import Superscript from '@tiptap/extension-superscript'
import TextAlign from '@tiptap/extension-text-align'
// Tiptap v3 把整个表格家族统一到 extension-table 一个包，都是命名导出。
import { Table, TableCell, TableHeader, TableRow } from '@tiptap/extension-table'
import { Blank, BlockMath, InlineMath } from './mathNodes'
import { PAPER_NODES } from './paperNodes'

/**
 * 组一份画布扩展。
 *
 * 唯一允许变化的只有 placeholder：它不引入新的节点类型，
 * 只是给空段落挂一个提示文案，因此不会让两份 schema 对同一份文档
 * 产生不同的解析结果。
 */
export function canvasExtensions(options: { placeholder?: string } = {}) {
  return [
    StarterKit.configure({
      // 标题层级限制在 1-3 级：试卷排版用不到更深的层级。
      heading: { levels: [1, 2, 3] },
    }),
    InlineMath,
    BlockMath,
    Blank,
    Image.configure({ inline: false }),
    Subscript,
    Superscript,
    TextAlign.configure({ types: ['heading', 'paragraph'] }),
    Table.configure({ resizable: true }),
    TableRow,
    TableHeader,
    TableCell,
    ...PAPER_NODES,
    ...(options.placeholder ? [Placeholder.configure({ placeholder: options.placeholder })] : []),
  ]
}
