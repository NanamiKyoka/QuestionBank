/**
 * 域逻辑测试：画布扩展的**拼装**。
 *
 * 这里要钉住一件容易被静默破坏的事：`canvasViewExtensions()` 是把纯 schema
 * 那份扩展里的 paper* 节点**按名字换成**带 NodeView 的版本。
 *
 * 换错了（名字拼写不一致、漏换、重复追加）不会报任何错——画布照常渲染，
 * 只是每个块都变成没有 NodeView 的裸 div：没有题号输入框、没有分值、
 * 没有题干。用户看到的是一个"能打字但什么都不显示"的编辑器，
 * 而报错日志里一个字都没有。所以必须显式测。
 */
import { describe, expect, it } from 'vitest'
import { canvasExtensions } from '../canvasExtensions'
import { canvasViewExtensions, PAPER_NODES_WITH_VIEW } from '../canvasViewExtensions'
import { CANVAS_NODE } from '@/lib/paperDocument'

/**
 * 读一个扩展有没有挂 NodeView。
 *
 * 需要收窄：扩展配置的类型是个大联合，`addNodeView` 只出现在节点那一支上，
 * 而 StarterKit 这类扩展支上没有这个成员。
 */
function hasNodeView(extension: { config: unknown }): boolean {
  const config = extension.config as { addNodeView?: unknown }
  return typeof config.addNodeView === 'function'
}

describe('canvasViewExtensions', () => {
  it('画布节点一律换成带 NodeView 的版本', () => {
    const withViews = canvasViewExtensions()
    for (const name of Object.values(CANVAS_NODE)) {
      const extension = withViews.find((e) => e.name === name)
      expect(extension, `缺少节点 ${name}`).toBeDefined()
      // NodeView 的存在性就体现在 addNodeView 上。
      expect(hasNodeView(extension!), `${name} 没挂 NodeView`).toBe(true)
    }
  })

  it('不重复注册同名节点', () => {
    // 同名重复注册时 Tiptap 以"后者覆盖前者"消解，
    // 结果是 schema 用 A、NodeView 挂在 B 上，症状极难查。
    const names = canvasViewExtensions().map((e) => e.name)
    expect(new Set(names).size).toBe(names.length)
  })

  it('非画布扩展原样保留（StarterKit 的段落、表格等不能被换掉）', () => {
    const schemaOnly = canvasExtensions()
    const withViews = canvasViewExtensions()
    expect(withViews.map((e) => e.name).sort()).toEqual(schemaOnly.map((e) => e.name).sort())
  })

  it('节点总数与纯 schema 版本一致——是替换不是追加', () => {
    expect(canvasViewExtensions()).toHaveLength(canvasExtensions().length)
  })

  it('七个画布节点都有带 NodeView 的对应物', () => {
    expect(PAPER_NODES_WITH_VIEW).toHaveLength(7)
    for (const node of PAPER_NODES_WITH_VIEW) {
      expect(hasNodeView(node)).toBe(true)
    }
  })

  it('placeholder 选项仍然透传到底层扩展', () => {
    const withPlaceholder = canvasViewExtensions({ placeholder: '试卷内容' })
    expect(withPlaceholder.map((e) => e.name)).toContain('placeholder')
    expect(canvasViewExtensions().map((e) => e.name)).not.toContain('placeholder')
  })
})
