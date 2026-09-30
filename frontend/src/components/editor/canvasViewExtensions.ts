/**
 * 带 NodeView 的画布扩展集。
 *
 * 与 canvasExtensions.ts 分开是**刻意的**：那份是纯 schema，不 import
 * 任何 .vue，因此可以在 node 环境里被直接测（paperNodes.test.ts 就是这么做的）；
 * 这份要挂 Vue 渲染器，必须跑在浏览器/DOM 里。
 *
 * 如果合成一份，schema 测试就会把整棵组件树拖进来——测试会因为"环境不对"
 * 而失败，而不是因为"schema 写错了"，那就等于把一道有用的护栏变成了噪音。
 *
 * 用法：画布用这份；测试与只读导出（不需要交互）用 canvasExtensions()。
 */
import { VueNodeViewRenderer } from '@tiptap/vue-3'
import {
  PaperAnswerItem,
  PaperAnswerSpace,
  PaperHeading,
  PaperPageBreak,
  PaperQuestion,
  PaperQuestionDetails,
  PaperRichText,
} from './paperNodes'
import { canvasExtensions } from './canvasExtensions'
import PaperAnswerItemView from './paper-node-views/PaperAnswerItemView.vue'
import PaperAnswerSpaceView from './paper-node-views/PaperAnswerSpaceView.vue'
import PaperHeadingView from './paper-node-views/PaperHeadingView.vue'
import PaperPageBreakView from './paper-node-views/PaperPageBreakView.vue'
import PaperQuestionView from './paper-node-views/PaperQuestionView.vue'
import PaperQuestionDetailsView from './paper-node-views/PaperQuestionDetailsView.vue'
import PaperRichTextView from './paper-node-views/PaperRichTextView.vue'

/**
 * 给一个节点挂上它的 Vue NodeView。
 *
 * 用 extend 而不是把 addNodeView 直接写进 paperNodes.ts：
 * 那边要保持"纯 schema、可无 DOM 测试"，一旦 import .vue 就做不到了。
 */
export const PaperRichTextWithView = PaperRichText.extend({
  addNodeView() {
    return VueNodeViewRenderer(PaperRichTextView)
  },
})

export const PaperHeadingWithView = PaperHeading.extend({
  addNodeView() {
    return VueNodeViewRenderer(PaperHeadingView)
  },
})

export const PaperQuestionWithView = PaperQuestion.extend({
  addNodeView() {
    return VueNodeViewRenderer(PaperQuestionView)
  },
})

export const PaperPageBreakWithView = PaperPageBreak.extend({
  addNodeView() {
    return VueNodeViewRenderer(PaperPageBreakView)
  },
})

export const PaperAnswerSpaceWithView = PaperAnswerSpace.extend({
  addNodeView() {
    return VueNodeViewRenderer(PaperAnswerSpaceView)
  },
})

export const PaperQuestionDetailsWithView = PaperQuestionDetails.extend({
  addNodeView() {
    return VueNodeViewRenderer(PaperQuestionDetailsView)
  },
})

export const PaperAnswerItemWithView = PaperAnswerItem.extend({
  addNodeView() {
    return VueNodeViewRenderer(PaperAnswerItemView)
  },
})

/** 画布上带 NodeView 的那七个节点，替换掉 canvasExtensions() 里的纯 schema 版本。 */
export const PAPER_NODES_WITH_VIEW = [
  PaperRichTextWithView,
  PaperHeadingWithView,
  PaperQuestionWithView,
  PaperPageBreakWithView,
  PaperAnswerSpaceWithView,
  PaperQuestionDetailsWithView,
  PaperAnswerItemWithView,
]

/**
 * 画布真正使用的扩展集：纯 schema 的那份，但把 paper* 节点换成带 NodeView 的。
 *
 * 替换而不是追加：同名扩展重复注册会让 Tiptap 以"后一个覆盖前一个"的
 * 方式消解，结果是 schema 用的是 A、NodeView 挂在 B 上，症状极难查。
 */
export function canvasViewExtensions(options: { placeholder?: string } = {}) {
  const schemaOnly = canvasExtensions(options)
  return schemaOnly.map((extension) => {
    const replacement = PAPER_NODES_WITH_VIEW.find((n) => n.name === extension.name)
    return replacement ?? extension
  })
}
