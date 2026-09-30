<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { EditorContent, useEditor } from '@tiptap/vue-3'
import type { JSONContent } from '@tiptap/core'
import type { PaperNode, PaperQuestionProjection } from '@/api/paper'
import { docToNodes, nodesToDoc } from '@/lib/paperDocument'
import { displayNumbers, topLevelOf } from '@/lib/paperView'
import { canvasViewExtensions } from './canvasViewExtensions'
import { createPaperCanvasState, installPaperCanvasState } from './paper-node-views/canvasState'

/**
 * 组卷画布：把整份试卷当成**一份 Tiptap 文档**来编辑。
 *
 * 与旧的卡片列表最根本的区别：这里只有一个连续的文档，光标可以在两块之间
 * 停留、Enter 能续写、撤销能一步退掉一次输入、拖动能整块搬家。
 * 卡片列表里每个块都是互不相干的 DOM，这些都做不到。
 *
 * ## 数据流
 *
 *   props.nodes ──nodesToDoc──▶ 编辑器文档
 *        ▲                            │
 *        └──docToNodes + emit── onUpdate
 *
 * `nodes` 始终是**事实源**：AI 的改动、服务端的重载都从 props 进来；
 * 用户在画布上的每一次输入都通过 `update` 事件出去，由父组件决定
 * 是否标脏、是否保存。画布自己不持有"当前节点数组"这份状态——
 * 一旦它自己也存一份，两份状态迟早会分叉，而症状是"保存后少了几个块"。
 *
 * ## 为什么外部改动要整体 setContent
 *
 * 自动保存是整树覆盖，AI 改的也是整棵树的候选，所以从 props 进来的改动
 * 天然是"整棵树"。逐块 diff 再打补丁在这里没有收益：排版是全局的，
 * 插入一道题会让后面所有题号变化。代价是外部改动会重置撤销栈——
 * 这是可接受的（撤销栈属于"用户自己的编辑"，跨一次 AI 应用保留它反而危险）。
 */
const props = withDefaults(
  defineProps<{
    nodes: PaperNode[]
    /** 题目 id → 服务端投影（题干/选项/答案）。 */
    projections: Record<number, PaperQuestionProjection>
    canEdit?: boolean
    numberingEnabled?: boolean
    scoringEnabled?: boolean
  }>(),
  { canEdit: false, numberingEnabled: true, scoringEnabled: false },
)

const emit = defineEmits<{
  /** 文档变化，带出**完整的**节点数组。父组件据此标脏。 */
  update: [nodes: PaperNode[]]
  /** 转换时丢掉了节点（正常恒为空）。父组件应当据此提醒用户。 */
  droppedNodes: [nodes: { id: string; type: string }[]]
}>()

const state = createPaperCanvasState()

/** 转换丢掉的节点，供父组件提示。 */
const dropped = ref<{ id: string; type: string }[]>([])

/**
 * 循环抑制标记。
 *
 * props → setContent 会触发 onUpdate，onUpdate 又 emit 回父组件，
 * 父组件再传回一份等价的 nodes……不加这道闸门就是无限循环，
 * 而且症状是"光标一跳一跳"这种极难定位的表现。
 */
let syncing = false

const editor = useEditor({
  content: nodesToDoc(props.nodes),
  editable: props.canEdit,
  extensions: canvasViewExtensions(),
  onUpdate: ({ editor: instance }) => {
    if (syncing) return
    const conversion = docToNodes(instance.getJSON())
    dropped.value = conversion.dropped
    if (conversion.dropped.length) emit('droppedNodes', conversion.dropped)
    refreshNumbers(conversion.nodes)
    emit('update', conversion.nodes)
  },
})

/** 把共享状态挂上去，并在 props 变化时同步。 */
function syncState() {
  if (!editor.value) return
  state.projections = props.projections
  state.canEdit = props.canEdit
  state.numberingEnabled = props.numberingEnabled
  state.scoringEnabled = props.scoringEnabled
  installPaperCanvasState(editor.value, state)
}

/** 重算卷面题号。NodeView 用它做题号输入框的占位符。 */
function refreshNumbers(nodes: PaperNode[]) {
  state.numbers = displayNumbers(topLevelOf(nodes))
}

// 编辑器实例是异步就绪的（useEditor 在挂载后才给实例），
// 因此状态同步要同时挂在"实例就绪"和"props 变化"两个时机上。
watch(editor, (instance) => {
  if (!instance) return
  syncState()
  refreshNumbers(props.nodes)
}, { immediate: true })

watch(
  () => [props.projections, props.canEdit, props.numberingEnabled, props.scoringEnabled] as const,
  syncState,
  { deep: true },
)

watch(
  () => props.canEdit,
  (canEdit) => {
    editor.value?.setEditable(canEdit)
  },
)

/**
 * 外部节点变化 → 重建文档。
 *
 * 只在**确实不同**时才 setContent：每次输入都会让 props 走一圈回来，
 * 无条件 setContent 会把光标和撤销栈按在每一次按键上重置。
 */
watch(
  () => props.nodes,
  (nodes) => {
    const instance = editor.value
    if (!instance) return
    const next = nodesToDoc(nodes)
    if (JSON.stringify(instance.getJSON()) === JSON.stringify(next)) {
      refreshNumbers(nodes)
      return
    }
    syncing = true
    try {
      instance.commands.setContent(next, { emitUpdate: false })
    } finally {
      syncing = false
    }
    refreshNumbers(nodes)
  },
)

onBeforeUnmount(() => editor.value?.destroy())

/** 空卷时显示引导，而不是一片什么都没有的空白。 */
const isEmpty = computed(() => topLevelOf(props.nodes).length === 0)

const rootClass = computed(() => (props.canEdit ? 'qm-canvas' : 'qm-canvas qm-canvas--readonly'))

defineExpose({
  /** 供父组件在需要时主动取当前文档（例如导出前确认）。 */
  getJSON: (): JSONContent | undefined => editor.value?.getJSON(),
  focus: () => editor.value?.commands.focus(),
})
</script>

<template>
  <div :class="rootClass">
    <div v-if="isEmpty" class="qm-canvas__empty">试卷还是空的，从左侧题库选题加入</div>
    <EditorContent :editor="editor" class="qm-canvas__content" />
  </div>
</template>

<style scoped>
.qm-canvas {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  background: #fff;
  padding: 0.75rem;
  min-height: 20rem;
}
.qm-canvas--readonly {
  background: var(--el-fill-color-blank);
}
.qm-canvas__empty {
  text-align: center;
  color: var(--el-text-color-placeholder);
  padding: 3rem 0;
  font-size: 0.875rem;
}
.qm-canvas__content {
  outline: none;
}
</style>
