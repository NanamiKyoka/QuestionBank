/**
 * 画布与 NodeView 之间的共享状态。
 *
 * 为什么走 `editor.storage` 而不是 provide/inject 或者 props 层层传：
 * NodeView 不是画布的子组件，它是**编辑器内部**实例化出来的（ProseMirror
 * 在渲染文档时自己 new 出来），所以拿不到画布的 provide，也没法从模板传 props。
 * storage 是 Tiptap 给扩展与 NodeView 留的正规通道。
 *
 * 这里放的是**画布外的数据**：题目投影（题库里的题干/选项/答案）、权限、
 * 以及算好的题号。这些都不属于文档本身，改它们不该产生一次排版事务。
 * 放进 storage 而不是塞进节点 attrs，正是为了划清这条线。
 *
 * 对象是 reactive 的：投影随「同步最新内容」变化、题号随拖动变化，
 * NodeView 要能跟着重渲染。
 */
import { reactive } from 'vue'
import type { Editor } from '@tiptap/core'
import type { PaperQuestionProjection } from '@/api/paper'

export interface PaperCanvasState {
  /** 题目 id → 服务端投影。画布只读它，不写。 */
  projections: Record<number, PaperQuestionProjection>
  /** 当前用户能否编辑。只读时所有输入控件都要禁用。 */
  canEdit: boolean
  /** 卷级「自动编号题号」开关，决定题号是显示输入框还是只读文字。 */
  numberingEnabled: boolean
  /** 卷级「启用分值统计」开关。关掉时服务端 sumScore 返回 0，导出也不带分值。 */
  scoringEnabled: boolean
  /**
   * 节点 id → 卷面上显示的题号。
   *
   * 由画布在每次文档变化后重算（`displayNumbers`）。NodeView 用它做输入框的
   * 占位符：留空表示"交给自动编号"，占位符就该显示自动编出来的那个号。
   */
  numbers: Map<string, string>
}

/** storage 上的键名。取一个不太可能撞车的名字。 */
const STORAGE_KEY = 'paper'

/** 建一份初始状态。 */
export function createPaperCanvasState(): PaperCanvasState {
  return reactive<PaperCanvasState>({
    projections: {},
    canEdit: false,
    numberingEnabled: true,
    scoringEnabled: false,
    numbers: new Map<string, string>(),
  })
}

/** 把状态挂到编辑器上。 */
export function installPaperCanvasState(editor: Editor, state: PaperCanvasState): void {
  ;(editor.storage as unknown as Record<string, unknown>)[STORAGE_KEY] = state
}

/**
 * 从编辑器取状态。
 *
 * 取不到时给一份**降级状态**而不是抛错：NodeView 可能在一个还没装上
 * storage 的编辑器里被渲染（例如某个测试或未来的只读预览器），
 * 那种情况下应该安静地展示为只读，而不是整页崩掉。
 */
export function paperCanvasStateOf(editor: Editor | null | undefined): PaperCanvasState {
  const state = editor
    ? ((editor.storage as unknown as Record<string, unknown>)[STORAGE_KEY] as PaperCanvasState | undefined)
    : undefined
  return state ?? FALLBACK
}

const FALLBACK: PaperCanvasState = {
  projections: {},
  canEdit: false,
  numberingEnabled: true,
  scoringEnabled: false,
  numbers: new Map<string, string>(),
}
